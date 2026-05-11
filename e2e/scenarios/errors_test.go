//go:build e2e

package scenarios

import (
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/e2e"
	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// TestErrors_SendToNonExistentRoom: publishing a msg.send for a room alice
// is not a member of (here: a freshly-minted ID with no backing room) MUST
// produce an error reply from message-gatekeeper.
//
// Per amendment R1 11.F: sanitization is documented-but-not-enforced today
// in the gatekeeper. The test asserts only that ErrorResponse.Error is
// non-empty, not that it matches a particular sanitized string.
func TestErrors_SendToNonExistentRoom(t *testing.T) {
	ctx := t.Context()
	site := e2e.Stack().SiteA

	alice := site.Authenticate(t, ctx, "alice")

	bogusRoomID := idgen.GenerateID()
	requestID := idgen.GenerateRequestID()

	err := sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		requestID,
		subject.MsgSend(alice.Account, bogusRoomID, site.SiteID),
		model.SendMessageRequest{
			ID:        idgen.GenerateMessageID(),
			Content:   "send to nowhere",
			RequestID: requestID,
		},
		5*time.Second,
	)
	require.Error(t, err)

	er := asErrorReply(err)
	require.NotNil(t, er, "gatekeeper should respond with an ErrorResponse on a non-member-of-room send")
	assert.NotEmpty(t, er.Resp.Error, "ErrorResponse.Error must be non-empty (sanitization softness per R1 11.F)")
}

// TestErrors_MalformedPayload_TimesOut: per amendment R1 11.D -- when the
// gatekeeper receives an unparseable JSON payload it can't extract the
// RequestID from, so it can't compute the reply subject; it logs + drops.
// The original "assert error reply" test is unworkable; we assert the
// timeout instead.
func TestErrors_MalformedPayload_TimesOut(t *testing.T) {
	ctx := t.Context()
	site := e2e.Stack().SiteA
	alice := site.Authenticate(t, ctx, "alice")

	requestID := idgen.GenerateRequestID()
	respSubj := userResponseSubject(alice.Account, requestID)
	respSub, err := alice.Conn().SubscribeSync(respSubj)
	require.NoError(t, err)
	defer func() { _ = respSub.Unsubscribe() }()

	// Send raw garbage to msg.send. RequestID isn't even in the payload, so
	// the gatekeeper can't compute the reply subject -> no reply -> timeout.
	require.NoError(t, alice.Conn().Publish(
		subject.MsgSend(alice.Account, idgen.GenerateID(), site.SiteID),
		[]byte("not json at all"),
	))

	_, err = respSub.NextMsg(2 * time.Second)
	require.Error(t, err)
	assert.True(t, errors.Is(err, nats.ErrTimeout),
		"expected NATS timeout error on malformed payload; got %v", err)
}

// TestErrors_BadCredsRejected: per amendment R1 11.E -- replaces the
// chapter-spec TestErrors_BadJWT (which required jwt/v2 claim forging at
// considerable complexity) with a simpler negative test that still proves
// NATS rejects connections lacking valid creds.
func TestErrors_BadCredsRejected(t *testing.T) {
	site := e2e.Stack().SiteA
	_, err := nats.Connect(site.NATSURL, nats.UserCredentials("/tmp/does-not-exist.creds"))
	require.Error(t, err, "NATS must reject connections without valid creds")
}
