//go:build e2e

// Chaos tests: kill a backing dependency mid-flight and assert the
// service-layer recovers to a consistent state once the dep returns.
// These tests intentionally don't try to assert ZERO data loss --
// killing mongo mid-write is allowed to drop the in-flight messages.
// What they DO assert: (a) the system doesn't enter a stuck state
// after the dep comes back, and (b) subsequent traffic flows
// normally.
//
// All chaos tests are SKIPPED under E2E_REUSE_STACK because they
// disrupt shared backing services that other parallel tests depend
// on. They run under `make e2e` (testcontainers) where each
// invocation owns the stack.

package e2e

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// TestChaos_MongoMidWriteRecovers: send a message, stop mongo-a briefly
// while the canonical worker is still draining, restart mongo-a, then
// send a SECOND message and assert it lands in Cassandra normally. This
// is "the system unwedges after a dep blip" not "no data loss."
func TestChaos_MongoMidWriteRecovers(t *testing.T) {
	skipUnderReuse(t, "Chaos test stops mongo-a; would disrupt parallel tests")

	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

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
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, site)}, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)

	// Pre-blip baseline send: confirms the room is healthy.
	js := site.JetStream(t)
	canonical := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx, js, canonical, "message-worker")
	pre, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	preSeq := pre.CachedInfo().State.LastSeq

	msgIDPre := idgen.GenerateMessageID()
	reqIDPre := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t, alice.Conn(), alice.Account, reqIDPre,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID: msgIDPre, Content: "pre-blip", RequestID: reqIDPre,
		},
		10*time.Second,
	))
	awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", preSeq+1)

	// Stop mongo-a. Anyone holding a connection will get io.EOF on
	// their next op. JetStream isn't affected -- only direct mongo
	// users (room-service, message-worker on cassandra-driver, etc.
	// actually only services with mongo deps).
	mongo := newWorkerLifecycle(t, ctx, "mongo-a")
	mongo.Stop(t, ctx)
	t.Logf("mongo-a stopped at %s", time.Now().Format(time.RFC3339Nano))

	// While mongo-a is down, fire a publish. Gatekeeper accepts (it
	// doesn't talk to mongo), message-worker writes to cassandra
	// (which is up). Workers that DO touch mongo (room-service?
	// notification-worker?) will retry on next message.
	msgIDDuring := idgen.GenerateMessageID()
	reqIDDuring := idgen.GenerateRequestID()
	_ = sendAndAwaitReply(
		t, alice.Conn(), alice.Account, reqIDDuring,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID: msgIDDuring, Content: "mid-blip", RequestID: reqIDDuring,
		},
		5*time.Second,
	)
	// We do NOT assert success here; gatekeeper may or may not reply
	// in the mongo-down window (most likely succeeds since gatekeeper
	// only validates on cassandra/jetstream). The point of this send
	// is to ensure SOMETHING was in-flight when mongo went down.

	// Restart mongo-a.
	mongo.Start(t, ctx)
	t.Logf("mongo-a restarted")
	// Wait for the healthcheck to flip (mongo-a's compose healthcheck
	// pings every 2s, with 30 retries).
	require.Eventually(t, func() bool {
		// Cheap health probe: open a fresh client and ping.
		client := site.MongoDB(t)
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return client.RunCommand(pingCtx, map[string]int{"ping": 1}).Err() == nil
	}, 30*time.Second, 500*time.Millisecond, "mongo-a never healthy after restart")

	// THE assertion: post-blip, a fresh send must complete end-to-end.
	// This proves the service-layer reconnected to mongo and isn't
	// stuck in a backoff loop.
	postInfo, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	postBlipPreSeq := postInfo.CachedInfo().State.LastSeq

	msgIDPost := idgen.GenerateMessageID()
	reqIDPost := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t, alice.Conn(), alice.Account, reqIDPost,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID: msgIDPost, Content: "post-blip", RequestID: reqIDPost,
		},
		15*time.Second,
	))
	awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", postBlipPreSeq+1)

	// Spot-check: msgIDPre and msgIDPost should be in Cassandra (the
	// mid-blip message may or may not be -- we don't assert).
	sess := site.CassandraSession(t)
	defer sess.Close()
	for _, id := range []string{msgIDPre, msgIDPost} {
		var c int
		require.NoError(t, sess.Query(
			`SELECT COUNT(*) FROM chat.messages_by_id WHERE message_id = ?`, id,
		).WithContext(ctx).Scan(&c))
		require.Equal(t, 1, c, "post-recovery msgID=%s must be in Cassandra", id)
	}
}

// TestChaos_CassandraMidWriteRecovers: same shape, but kill cassandra
// briefly. Catches: message-worker getting wedged on a cassandra
// driver retry loop.
func TestChaos_CassandraMidWriteRecovers(t *testing.T) {
	skipUnderReuse(t, "Chaos test stops cass-a")

	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

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
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, site)}, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)

	cass := newWorkerLifecycle(t, ctx, "cass-a")
	cass.Stop(t, ctx)
	t.Logf("cass-a stopped")

	// Try a send during the outage; message-worker will fail to write
	// and (probably) NAK the canonical event for redelivery.
	msgIDDuring := idgen.GenerateMessageID()
	reqIDDuring := idgen.GenerateRequestID()
	_ = sendAndAwaitReply(
		t, alice.Conn(), alice.Account, reqIDDuring,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID: msgIDDuring, Content: "cass-down", RequestID: reqIDDuring,
		},
		5*time.Second,
	)

	cass.Start(t, ctx)
	t.Logf("cass-a restarted")

	// Wait for cass-a to be reachable. Probes directly via gocql (NOT
	// site.CassandraSession, which calls require.NoError and triggers
	// runtime.Goexit on connect failure -- recover() can't catch Goexit,
	// so the previous defer-recover() was misleading and broke the
	// Eventually retry loop).
	require.Eventually(t, func() bool {
		cluster := gocql.NewCluster("localhost:19042")
		cluster.Keyspace = "chat"
		cluster.Consistency = gocql.LocalQuorum
		cluster.ConnectTimeout = 2 * time.Second
		cluster.Timeout = 2 * time.Second
		s, err := cluster.CreateSession()
		if err != nil {
			return false
		}
		defer s.Close()
		return s.Query("SELECT now() FROM system.local").WithContext(ctx).Exec() == nil
	}, 60*time.Second, 2*time.Second, "cass-a never healthy after restart")

	// Post-blip: a new send must complete and land in cassandra.
	js := site.JetStream(t)
	canonical := stream.MessagesCanonical(site.SiteID).Name
	postInfo, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	postBlipPreSeq := postInfo.CachedInfo().State.LastSeq

	msgIDPost := idgen.GenerateMessageID()
	reqIDPost := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t, alice.Conn(), alice.Account, reqIDPost,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID: msgIDPost, Content: "post-blip", RequestID: reqIDPost,
		},
		20*time.Second,
	))
	awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", postBlipPreSeq+1)

	sess := site.CassandraSession(t)
	defer sess.Close()
	var c int
	require.NoError(t, sess.Query(
		`SELECT COUNT(*) FROM chat.messages_by_id WHERE message_id = ?`, msgIDPost,
	).WithContext(ctx).Scan(&c))
	require.Equal(t, 1, c, "post-recovery msgID=%s must be in Cassandra", msgIDPost)
}

// skipUnderReuse short-circuits a test under E2E_REUSE_STACK=1. Chaos
// tests share dep containers with parallel-running tests; running them
// in REUSE mode breaks the entire suite for the duration of the chaos.
func skipUnderReuse(t *testing.T, why string) {
	t.Helper()
	if reuse, _ := strconv.ParseBool(os.Getenv("E2E_REUSE_STACK")); reuse {
		t.Skipf("E2E_REUSE_STACK=1: %s", why)
	}
}
