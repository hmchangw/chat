//go:build e2e

package scenarios

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

// requestReply is a thin wrapper over nats.Conn.Request that JSON-marshals
// the request, JSON-unmarshals the reply (if into != nil), and surfaces
// model.ErrorResponse as a Go error.
//
// Used for classic synchronous request/reply handlers: room-service's room
// create/list/get, room/member RPCs, history-service.LoadHistory, search-
// service. NOT for message-gatekeeper's send path, which uses async out-of-
// band reply via subject.UserResponse -- use sendAndAwaitReply for that.
//
// On error reply: returns an *errorReply wrapping the model.ErrorResponse;
// callers that need to inspect RoomID (e.g. DM idempotency per R1 11.B)
// can `errors.As(err, &er)` and read er.Resp.RoomID.
func requestReply(conn *nats.Conn, subj string, req any, timeout time.Duration, into any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg, err := conn.Request(subj, body, timeout)
	if err != nil {
		return fmt.Errorf("request %s: %w", subj, err)
	}

	// Try to decode as ErrorResponse first; the server returns it on the
	// same reply path with no special marker, so we sniff the JSON shape.
	var errResp model.ErrorResponse
	if jerr := json.Unmarshal(msg.Data, &errResp); jerr == nil && errResp.Error != "" {
		return &errorReply{Resp: errResp, Subject: subj}
	}

	if into != nil {
		if err := json.Unmarshal(msg.Data, into); err != nil {
			return fmt.Errorf("unmarshal reply %s: %w (raw=%s)", subj, err, msg.Data)
		}
	}
	return nil
}

// errorReply lets callers inspect the model.ErrorResponse's RoomID via
// errors.As. Per amendment R1 11.B (DM idempotency).
type errorReply struct {
	Resp    model.ErrorResponse
	Subject string
}

func (e *errorReply) Error() string {
	return fmt.Sprintf("%s rejected: %s", e.Subject, e.Resp.Error)
}

// asErrorReply extracts the ErrorResponse if err wraps one. Returns nil
// when err is nil or doesn't carry an ErrorResponse.
func asErrorReply(err error) *errorReply {
	var er *errorReply
	if errors.As(err, &er) {
		return er
	}
	return nil
}

// sendAndAwaitReply publishes a JetStream-captured message-send request and
// waits for message-gatekeeper's async reply on subject.UserResponse(account,
// requestID). Use ONLY for the msg.send subject (which is JS-published +
// async-replied per amendment R1 10.B). For classic NATS request/reply use
// requestReply.
//
// On error reply (model.ErrorResponse with non-empty Error), returns an
// *errorReply.
func sendAndAwaitReply(t *testing.T, conn *nats.Conn, account, requestID, sendSubj string, payload any, timeout time.Duration) error {
	t.Helper()

	respSubj := userResponseSubject(account, requestID)
	respSub, err := conn.SubscribeSync(respSubj)
	require.NoError(t, err, "subscribe response subject %s", respSubj)
	defer func() { _ = respSub.Unsubscribe() }()

	body, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, conn.Publish(sendSubj, body), "publish %s", sendSubj)

	msg, err := respSub.NextMsg(timeout)
	if err != nil {
		return fmt.Errorf("await msg.send reply on %s: %w", respSubj, err)
	}

	var errResp model.ErrorResponse
	if jerr := json.Unmarshal(msg.Data, &errResp); jerr == nil && errResp.Error != "" {
		return &errorReply{Resp: errResp, Subject: sendSubj}
	}
	return nil
}

// userResponseSubject is here rather than calling subject.UserResponse so
// that helpers_test.go has a single import surface; the format is pinned
// by pkg/subject (`chat.user.{account}.response.{requestID}`) and we'd
// fail-loud on drift via the federation_test.go-style shape pinning.
// The pkg/subject builder is the canonical source; keep this in sync.
func userResponseSubject(account, requestID string) string {
	return fmt.Sprintf("chat.user.%s.response.%s", account, requestID)
}

// awaitDurableReady polls until a named durable consumer exists on a stream.
// Confirms the worker's startup completed CreateOrUpdateConsumer; does NOT
// confirm the worker has parked on a fetch.
//
// Per amendment R1 10.C: replaces the chapter-spec awaitConsumerReady
// (which relied on NumWaiting > 0 -- racy and didn't filter by durable).
func awaitDurableReady(t *testing.T, ctx context.Context, js jetstream.JetStream, streamName, durable string) {
	t.Helper()
	require.Eventually(t, func() bool {
		s, err := js.Stream(ctx, streamName)
		if err != nil {
			return false
		}
		_, err = s.Consumer(ctx, durable)
		return err == nil
	}, 20*time.Second, 200*time.Millisecond,
		"durable %q on stream %q not ready in 20s", durable, streamName)
}

// awaitCanonicalAcked waits until the named durable's AckFloor.Stream
// reaches publishSeq. Use after publishing a known event to confirm the
// worker has processed it (e.g. after MsgSend, wait for message-worker to
// ack before reading from Cassandra via LoadHistory). Per amendment R1 10.D.
func awaitCanonicalAcked(t *testing.T, ctx context.Context, js jetstream.JetStream, streamName, durable string, publishSeq uint64) {
	t.Helper()
	require.Eventually(t, func() bool {
		s, err := js.Stream(ctx, streamName)
		if err != nil {
			return false
		}
		c, err := s.Consumer(ctx, durable)
		if err != nil {
			return false
		}
		info, err := c.Info(ctx)
		if err != nil {
			return false
		}
		return info.AckFloor.Stream >= publishSeq
	}, 15*time.Second, 100*time.Millisecond,
		"%s ack floor never reached %d on stream %s", durable, publishSeq, streamName)
}

// awaitMessage waits for a single NATS message with a clear test-level
// failure message on timeout.
func awaitMessage(t *testing.T, sub *nats.Subscription, timeout time.Duration) *nats.Msg {
	t.Helper()
	msg, err := sub.NextMsg(timeout)
	require.NoError(t, err, "no message on %s within %s", sub.Subject, timeout)
	return msg
}
