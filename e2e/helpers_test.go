//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/e2e/harness"
	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// JSON-marshal request, await reply, decode ErrorResponse to *errorReply.
// Use sendAndAwaitReply for msg.send (async UserResponse path).
func requestReply(conn *nats.Conn, subj string, req any, timeout time.Duration, into any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	// room-service rejects requests missing X-Request-ID.
	outbound := &nats.Msg{
		Subject: subj,
		Data:    body,
		Header:  nats.Header{natsutil.RequestIDHeader: []string{idgen.GenerateRequestID()}},
	}
	msg, err := conn.RequestMsg(outbound, timeout)
	if err != nil {
		return fmt.Errorf("request %s: %w", subj, err)
	}

	// ErrorResponse rides the same reply path; sniff the JSON shape.
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

// Publishes msg.send and awaits gatekeeper's async reply on UserResponse.
// On error reply, returns *errorReply.
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

// Mirrors subject.UserResponse; keep in sync with pkg/subject.
func userResponseSubject(account, requestID string) string {
	return fmt.Sprintf("chat.user.%s.response.%s", account, requestID)
}

// Polling budgets. Pick by the operation being awaited:
//   - shortPoll: single in-process step (subscription persisted, durable created).
//   - medPoll: cross-service hop (gatekeeper -> canonical -> message-worker).
//   - longPoll: cross-site federation (gateway sourcing + remote inbox-worker).
//
// Intervals are tuned for the race detector + CI scheduler jitter.
const (
	shortPoll    = 15 * time.Second
	medPoll      = 20 * time.Second
	longPoll     = 30 * time.Second
	pollInterval = 200 * time.Millisecond
)

// Polls until the named durable consumer exists; doesn't prove the worker
// has parked on a fetch.
func awaitDurableReady(t *testing.T, ctx context.Context, js jetstream.JetStream, streamName, durable string) {
	t.Helper()
	require.Eventually(t, func() bool {
		s, err := js.Stream(ctx, streamName)
		if err != nil {
			return false
		}
		_, err = s.Consumer(ctx, durable)
		return err == nil
	}, medPoll, pollInterval,
		"durable %q on stream %q not ready", durable, streamName)
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

// awaitMessageOnSite opens a Cassandra session on the site and polls for
// msgID. Most callers want this; awaitMessageByID is for the rare case where
// the test already holds a session.
func awaitMessageOnSite(t *testing.T, ctx context.Context, site harness.SiteEndpoints, msgID string) {
	t.Helper()
	awaitMessageByID(t, ctx, site.CassandraSession(t), msgID)
}

// awaitMessageByID polls chat.messages_by_id by PK until the row appears.
// Parallel-safe (no seq-based AckFloor race).
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
	}, shortPoll, 100*time.Millisecond,
		"message_id=%s never appeared in chat.messages_by_id "+
			"(canonical worker may have failed to write, or the message never reached the worker)",
		msgID)
}

// awaitSubscription polls mongo until (account, roomID) has a subscription.
// CreateRoomReply ack-s before room-worker writes; gatekeeper rejects sends
// before the subscription lands.
func awaitSubscription(t *testing.T, ctx context.Context, db *mongo.Database, account, roomID string) {
	t.Helper()
	subs := db.Collection("subscriptions")
	require.Eventually(t, func() bool {
		count, err := subs.CountDocuments(ctx, bson.M{
			"u.account": account,
			"roomId":    roomID,
		})
		return err == nil && count > 0
	}, shortPoll, 100*time.Millisecond,
		"subscription for account=%s roomId=%s never persisted", account, roomID)
}

// registerRoomCleanup deletes a room's rows from each listed site's backends
// at test teardown: Mongo (always), and Cassandra/ES/Valkey when their fields
// are set on SiteDB. messages_by_id cannot be room-keyed (its PK is
// message_id), so rows leak there. All steps are best-effort: errors log and
// continue so teardown can't mask a real test failure.
func registerRoomCleanup(t *testing.T, sites []SiteDB, roomID string) {
	t.Helper()
	if roomID == "" {
		return
	}
	t.Cleanup(func() {
		// Fresh context: t.Cleanup ordering may have canceled the test ctx.
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
// Errors are swallowed (cleanup must not mask test failures).
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
		// Three single-key filters (worst case 3 passes) beats a $or scan.
		_, _ = coll.DeleteMany(ctx, bson.M{"roomId": roomID})
		_, _ = coll.DeleteMany(ctx, bson.M{"rid": roomID})
		_, _ = coll.DeleteMany(ctx, bson.M{"_id": roomID})
	}
}

// cleanupCassandra drops message history for the room from the two by-room
// tables. messages_by_id leaks (PK=message_id, not room_id).
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

// cleanupElasticsearch delete-by-querys the room from messages-* and
// user-room-* indices. Skip refresh=true: the 200ms auto-refresh is
// sufficient and the forced flush costs 5-15s/run across the suite.
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
		url := sd.ESURL + "/" + indexPattern + "/_delete_by_query?conflicts=proceed"
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
		// 404 (no index yet) and 409 (conflicts=proceed) are fine.
		if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
			t.Logf("cleanup es status site=%s index=%s roomID=%s: %d (ignoring)",
				sd.SiteID, indexPattern, roomID, resp.StatusCode)
		}
	}
}

// cleanupValkey drops room:<rid>:key{,:prev} entries (roomkeystore).
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

// SiteDB carries the per-backend handles registerRoomCleanup needs. Only DB
// is required; missing optional fields silently skip that backend's cleanup.
// Use asSiteDB to populate everything.
type SiteDB struct {
	SiteID     string
	DB         *mongo.Database
	Cassandra  *gocql.Session
	ESURL      string
	ValkeyAddr string
}

// asSiteDB returns a fully-populated SiteDB so registerRoomCleanup catches
// every backend. Partial `{SiteID, DB}` literals silently skip Cassandra/ES/Valkey.
func asSiteDB(t *testing.T, site harness.SiteEndpoints) SiteDB {
	t.Helper()
	return SiteDB{
		SiteID:     site.SiteID,
		DB:         site.MongoDB(t),
		Cassandra:  site.CassandraSession(t),
		ESURL:      site.ESURL,
		ValkeyAddr: site.ValkeyAddr,
	}
}

// skipUnderReuse short-circuits a test under E2E_REUSE_STACK=1. Used by
// chaos tests (disrupt shared deps), auth-load (saturates Keycloak), and
// federation catchup (worker-restart consumer-reattach is racy on shared
// stack; `make e2e` testcontainers ownership makes it reliable).
func skipUnderReuse(t *testing.T, why string) {
	t.Helper()
	if reuse, _ := strconv.ParseBool(os.Getenv("E2E_REUSE_STACK")); reuse {
		t.Skipf("E2E_REUSE_STACK=1: %s", why)
	}
}

// setupChannelRoom authenticates the two named realm users on `site`, opens
// a channel room with the first inviting the second, registers full backend
// cleanup, and awaits both subscriptions persisted on mongo. Collapses the
// 7-line setup that prefixes ~15 tests across the suite.
//
// IMPORTANT: roomID is request-accepted, not data-durable, until the
// awaitSubscription calls return -- relying on the bare reply is the
// classic gatekeeper-rejects-send trap.
func setupChannelRoom(t *testing.T, ctx context.Context, site harness.SiteEndpoints, requester, invitee string) (alice, bob *harness.Identity, roomID string) {
	t.Helper()
	alice = site.Authenticate(t, ctx, requester)
	bob = site.Authenticate(t, ctx, invitee)

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID = createReply.RoomID
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, site)}, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), bob.Account, roomID)
	return alice, bob, roomID
}

// enableAlert flips the per-room subscription's `alert` flag to true so
// notification-worker dispatches notifications to this account. New subs
// from room creation default to alert=false; tests that assert on
// notification arrival must call this after awaitSubscription.
func enableAlert(t *testing.T, ctx context.Context, db *mongo.Database, account, roomID string) {
	t.Helper()
	res, err := db.Collection("subscriptions").UpdateOne(ctx,
		bson.M{"u.account": account, "roomId": roomID},
		bson.M{"$set": bson.M{"alert": true, "isSubscribed": true}},
	)
	require.NoError(t, err, "enableAlert(%s,%s)", account, roomID)
	require.Equal(t, int64(1), res.MatchedCount,
		"enableAlert(%s,%s): expected exactly one sub matched", account, roomID)
}
