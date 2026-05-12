//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
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
	// Every request carries an X-Request-ID header. room-service in
	// particular rejects requests that lack one (errMissingRequestID).
	// idgen.GenerateRequestID returns a UUIDv7 in hyphenated form, which
	// matches the natsutil/idgen contract.
	outbound := &nats.Msg{
		Subject: subj,
		Data:    body,
		Header:  nats.Header{natsutil.RequestIDHeader: []string{idgen.GenerateRequestID()}},
	}
	msg, err := conn.RequestMsg(outbound, timeout)
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

// awaitMessageByID polls Cassandra's messages_by_id table until a row with
// the given message_id appears. Use this INSTEAD of awaitCanonicalAcked
// when the test runs under t.Parallel() on a shared canonical durable
// (per testing-automation reviewer): the seq-based wait races because
// AckFloor advances on whichever message finishes first, so a parallel
// sibling's message-worker ack can satisfy the wait before YOUR message
// is in Cassandra.
//
// Polls the dedup view directly: PRIMARY KEY (message_id, created_at)
// returns exactly one row when the write has landed. Times out after
// 15s — typical observed latency is sub-second.
func awaitMessageByID(t *testing.T, ctx context.Context, sess *gocql.Session, msgID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var count int
		if err := sess.Query(
			`SELECT COUNT(*) FROM chat.messages_by_id WHERE message_id = ?`,
			msgID,
		).WithContext(ctx).Scan(&count); err != nil {
			return false
		}
		return count == 1
	}, 15*time.Second, 100*time.Millisecond,
		"message_id=%s never appeared in chat.messages_by_id within 15s "+
			"(canonical worker may have failed to write, or the message never reached the worker)",
		msgID)
}

// awaitSubscription waits until the named account has a subscription record
// for the given roomID in the site's mongo. CreateRoomReply returns BEFORE
// room-worker persists the subscriptions (it's a "request accepted"
// acknowledgement, not a "data is durable" confirmation). Sending to the
// room before this point trips the gatekeeper's "user is not subscribed"
// check.
func awaitSubscription(t *testing.T, ctx context.Context, db *mongo.Database, account, roomID string) {
	t.Helper()
	subs := db.Collection("subscriptions")
	require.Eventually(t, func() bool {
		count, err := subs.CountDocuments(ctx, bson.M{
			"u.account": account,
			"roomId":    roomID,
		})
		return err == nil && count > 0
	}, 15*time.Second, 100*time.Millisecond,
		"subscription for account=%s roomId=%s never persisted", account, roomID)
}

// registerRoomCleanup deletes a room's rows from all listed sites on test
// teardown. Without this, state accumulates across runs: subscriptions,
// rooms, room_members, thread_subscriptions collections grow monotonically,
// Cassandra messages_by_room partitions stay forever, ES messages-* / user-
// room-* docs persist, and Valkey room-key entries leak. Tests that count
// or list start to drift.
//
// Cleanup is per-site and best-effort across four backends:
//
//   - Mongo (always): subscriptions, rooms, room_members, thread_subscriptions,
//     thread_rooms. Matches roomId / rid / _id field names.
//   - Cassandra (only when SiteDB.Cassandra is set): chat.messages_by_room and
//     chat.thread_messages_by_room, partitioned by room_id, so they delete in
//     O(1). The chat.messages_by_id dedup view CANNOT be cleaned by room (its
//     PK is message_id, not room_id); rows leak there permanently — tests that
//     count messages_by_id rows globally must account for that drift.
//   - Elasticsearch (only when SiteDB.ESURL is set): delete-by-query
//     {"term":{"roomId":"<rid>"}} against messages-* and user-room-* indices.
//   - Valkey (only when SiteDB.ValkeyAddr is set): DEL room:<rid>:key and
//     DEL room:<rid>:key:prev for the encryption room-key store.
//
// Every Cassandra/ES/Valkey step logs and continues on error so a failing
// teardown can't mask the actual test failure. Mongo DeleteMany errors are
// also swallowed (idempotent — safe to call even if no rows exist).
//
// Per test-quality reviewer's must-fix #3 + testing-automation reviewer's
// expansion.
func registerRoomCleanup(t *testing.T, sites []SiteDB, roomID string) {
	t.Helper()
	if roomID == "" {
		return
	}
	t.Cleanup(func() {
		// Use a fresh context: the test's context may already be canceled
		// by t.Cleanup ordering.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, sd := range sites {
			cleanupMongo(t, cleanupCtx, sd, roomID)
			cleanupCassandra(t, cleanupCtx, sd, roomID)
			cleanupElasticsearch(t, cleanupCtx, sd, roomID)
			cleanupValkey(t, cleanupCtx, sd, roomID)
		}
	})
}

// cleanupMongo deletes the per-room rows from the high-churn collections.
// Errors are intentionally swallowed: cleanup must not mask test failures,
// and DeleteMany on a missing match returns DeletedCount=0 (not an error).
func cleanupMongo(t *testing.T, ctx context.Context, sd SiteDB, roomID string) {
	t.Helper()
	if sd.DB == nil {
		return
	}
	for _, collName := range []string{
		"subscriptions",
		"rooms",
		"room_members",
		"thread_subscriptions",
		"thread_rooms",
	} {
		coll := sd.DB.Collection(collName)
		// Match a few common field names that carry the roomID.
		// CountDocuments-style filter is cheaper than a $or here;
		// the worst case is two passes per collection.
		_, _ = coll.DeleteMany(ctx, bson.M{"roomId": roomID})
		_, _ = coll.DeleteMany(ctx, bson.M{"rid": roomID})
		_, _ = coll.DeleteMany(ctx, bson.M{"_id": roomID})
	}
}

// cleanupCassandra drops message history for the room from the two by-room
// tables. The messages_by_id dedup view has PK=message_id and CANNOT be
// cleaned per-room without a full scan — accepted leak, documented above.
func cleanupCassandra(t *testing.T, ctx context.Context, sd SiteDB, roomID string) {
	t.Helper()
	if sd.Cassandra == nil {
		return
	}
	for _, table := range []string{
		"chat.messages_by_room",
		"chat.thread_messages_by_room",
	} {
		if err := sd.Cassandra.Query(
			"DELETE FROM "+table+" WHERE room_id = ?", roomID,
		).WithContext(ctx).Exec(); err != nil {
			t.Logf("cleanup cassandra %s site=%s roomID=%s: %v (ignoring)",
				table, sd.SiteID, roomID, err)
		}
	}
}

// cleanupElasticsearch removes per-room ES docs from both the message index
// and the user-room index via best-effort delete-by-query. We hit the index
// wildcards messages-* and user-room-* so the cleanup is robust to per-site
// index naming. refresh=true forces visibility for any follow-up assertions.
func cleanupElasticsearch(t *testing.T, ctx context.Context, sd SiteDB, roomID string) {
	t.Helper()
	if sd.ESURL == "" {
		return
	}
	body, err := json.Marshal(map[string]any{
		"query": map[string]any{
			"term": map[string]any{"roomId": roomID},
		},
	})
	if err != nil {
		t.Logf("cleanup es marshal site=%s roomID=%s: %v (ignoring)",
			sd.SiteID, roomID, err)
		return
	}
	for _, indexPattern := range []string{"messages-*", "user-room-*"} {
		url := sd.ESURL + "/" + indexPattern + "/_delete_by_query?refresh=true&conflicts=proceed"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			t.Logf("cleanup es new request site=%s index=%s: %v (ignoring)",
				sd.SiteID, indexPattern, err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("cleanup es do site=%s index=%s roomID=%s: %v (ignoring)",
				sd.SiteID, indexPattern, roomID, err)
			continue
		}
		_ = resp.Body.Close()
		// 404 is fine (index not yet created on this site). 409 is fine
		// (conflicts=proceed is best-effort). Anything else: log + continue.
		if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
			t.Logf("cleanup es status site=%s index=%s roomID=%s: %d (ignoring)",
				sd.SiteID, indexPattern, roomID, resp.StatusCode)
		}
	}
}

// cleanupValkey drops the room encryption key entries written by
// SeedRoomKey / broadcast-worker. The key format mirrors
// roomkeystore.ValkeyStore (`room:<rid>:key` and `room:<rid>:key:prev`);
// DEL is idempotent so missing keys are a no-op.
func cleanupValkey(t *testing.T, ctx context.Context, sd SiteDB, roomID string) {
	t.Helper()
	if sd.ValkeyAddr == "" {
		return
	}
	client := redis.NewClient(&redis.Options{Addr: sd.ValkeyAddr})
	defer func() { _ = client.Close() }()
	for _, key := range []string{
		"room:" + roomID + ":key",
		"room:" + roomID + ":key:prev",
	} {
		if err := client.Del(ctx, key).Err(); err != nil {
			t.Logf("cleanup valkey DEL site=%s key=%s: %v (ignoring)",
				sd.SiteID, key, err)
		}
	}
}

// SiteDB pairs a site label with the per-backend handles registerRoomCleanup
// uses for teardown. Tests commonly want to clean both sites (cross-site
// invite leaves rows on mongo-b) so this lets them pass a slice.
//
// Only DB is required for backward compatibility with existing call sites.
// Cassandra / ESURL / ValkeyAddr are optional: when zero, the corresponding
// cleanup step is skipped silently. New tests should fill them in to keep
// the four backends bounded across long REUSE sessions.
type SiteDB struct {
	SiteID     string
	DB         *mongo.Database
	Cassandra  *gocql.Session
	ESURL      string
	ValkeyAddr string
}
