//go:build e2e

// Cassandra-direct assertions. The other tests assert via subject.MsgHistory
// (a NATS round-trip that uses Cassandra under the hood). These tests query
// Cassandra DIRECTLY so a regression in the storage path can't pass under
// cover of a buggy-but-self-consistent history-service.
//
// Coverage targets the column-family invariants:
//   - messages_by_room PK is (room_id, created_at, message_id) -- a single
//     message lands in EXACTLY ONE row.
//   - messages_by_id PK is (message_id, created_at) -- the dedup table.
//   - The two views agree on the same row's content (denormalization is
//     handled by message-worker on write; a divergence here means the
//     write path is splitting books).

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// TestCassandra_MessagesByRoomSingleRow: send one message, query
// chat.messages_by_room directly with (room_id), assert exactly one row
// has our message_id. Catches: a regression where message-worker writes
// the same message under multiple (created_at, message_id) clustering
// keys (e.g. retry without dedup).
func TestCassandra_MessagesByRoomSingleRow(t *testing.T) {
	t.Parallel()
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

	js := site.JetStream(t)
	canonical := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx, js, canonical, "message-worker")
	pre, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	preSeq := pre.CachedInfo().State.LastSeq

	msgID := idgen.GenerateMessageID()
	reqID := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		reqID,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   "cassandra-direct " + t.Name(),
			RequestID: reqID,
		},
		10*time.Second,
	))
	awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", preSeq+1)

	sess := site.CassandraSession(t)
	defer sess.Close()

	var count int
	require.NoError(t, sess.Query(
		`SELECT COUNT(*) FROM chat.messages_by_room WHERE room_id = ? AND message_id = ? ALLOW FILTERING`,
		roomID, msgID,
	).WithContext(ctx).Scan(&count))
	assert.Equal(t, 1, count,
		"exactly one row in messages_by_room must exist for (room=%s, msg=%s)",
		roomID, msgID)
}

// TestCassandra_MessagesByIdMatchesByRoom: same message must appear in BOTH
// messages_by_room AND messages_by_id with identical content. The two
// tables are denormalized views; message-worker writes both. A divergence
// would mean the dedup view (messages_by_id) and the room view
// (messages_by_room) disagree, which would silently break get-by-id +
// duplicate detection.
func TestCassandra_MessagesByIdMatchesByRoom(t *testing.T) {
	t.Parallel()
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

	js := site.JetStream(t)
	canonical := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx, js, canonical, "message-worker")
	pre, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	preSeq := pre.CachedInfo().State.LastSeq

	msgID := idgen.GenerateMessageID()
	reqID := idgen.GenerateRequestID()
	body := "denormalized view test " + t.Name()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		reqID,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   body,
			RequestID: reqID,
		},
		10*time.Second,
	))
	awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", preSeq+1)

	sess := site.CassandraSession(t)
	defer sess.Close()

	// Use Iter() not Scan() -- Scan() silently picks the first row, which
	// would mask a hypothetical duplicate-write regression. Iter() with
	// an explicit row count catches it.
	byRoomMsg, byRoomRID, byRoomCount := scanOneRow(t, ctx, sess,
		`SELECT message_id, room_id FROM chat.messages_by_room WHERE room_id = ? AND message_id = ? ALLOW FILTERING`,
		roomID, msgID,
	)
	require.Equal(t, 1, byRoomCount,
		"messages_by_room must have exactly 1 row for (room=%s, msg=%s); got %d",
		roomID, msgID, byRoomCount)

	byIdMsg, byIdRID, byIdCount := scanOneRow(t, ctx, sess,
		`SELECT message_id, room_id FROM chat.messages_by_id WHERE message_id = ?`,
		msgID,
	)
	require.Equal(t, 1, byIdCount,
		"messages_by_id must have exactly 1 row for msg=%s; got %d", msgID, byIdCount)

	assert.Equal(t, msgID, byRoomMsg)
	assert.Equal(t, msgID, byIdMsg)
	assert.Equal(t, byRoomRID, byIdRID,
		"messages_by_room and messages_by_id must agree on room_id "+
			"(message-worker writes both; denormalization broken if they diverge)")
	assert.Equal(t, roomID, byRoomRID)
}

// scanOneRow runs a SELECT message_id, room_id query and returns the row's
// fields plus the total row count. Caller asserts count==1 to catch
// hypothetical duplicates that Scan() would mask.
func scanOneRow(t *testing.T, ctx context.Context, sess *gocql.Session, cql string, args ...any) (msg, room string, count int) {
	t.Helper()
	iter := sess.Query(cql, args...).WithContext(ctx).Iter()
	var m, r string
	for iter.Scan(&m, &r) {
		if count == 0 {
			msg, room = m, r
		}
		count++
	}
	require.NoError(t, iter.Close(), "iter close")
	return
}

// TestCassandra_MessagesByIdDedup: send the SAME messageID twice. The
// PRIMARY KEY (message_id, created_at) on messages_by_id should produce
// exactly one row. Companion to TestNegative_DuplicateMessageID, which
// asserts via the history-service round-trip; this one queries directly.
func TestCassandra_MessagesByIdDedup(t *testing.T) {
	t.Parallel()
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

	js := site.JetStream(t)
	canonical := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx, js, canonical, "message-worker")

	sendOnce := func(msgID, body string) {
		t.Helper()
		pre, err := js.Stream(ctx, canonical)
		require.NoError(t, err)
		preSeq := pre.CachedInfo().State.LastSeq
		reqID := idgen.GenerateRequestID()
		require.NoError(t, sendAndAwaitReply(
			t,
			alice.Conn(),
			alice.Account,
			reqID,
			subject.MsgSend(alice.Account, roomID, site.SiteID),
			model.SendMessageRequest{ID: msgID, Content: body, RequestID: reqID},
			10*time.Second,
		))
		// Best-effort canonical wait; if the second send was rejected
		// upstream the seq won't have advanced.
		post, err := js.Stream(ctx, canonical)
		require.NoError(t, err)
		if post.CachedInfo().State.LastSeq > preSeq {
			awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", preSeq+1)
		}
	}

	msgID := idgen.GenerateMessageID()
	sendOnce(msgID, "first attempt")
	sendOnce(msgID, "second attempt with same msgID")

	sess := site.CassandraSession(t)
	defer sess.Close()

	var count int
	require.NoError(t, sess.Query(
		`SELECT COUNT(*) FROM chat.messages_by_id WHERE message_id = ?`,
		msgID,
	).WithContext(ctx).Scan(&count))
	assert.Equal(t, 1, count,
		"messages_by_id must contain exactly one row for msgID=%s "+
			"(PRIMARY KEY (message_id, created_at) enforces dedup); got %d",
		msgID, count)
}
