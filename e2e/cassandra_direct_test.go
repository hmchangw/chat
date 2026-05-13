//go:build e2e

// Direct Cassandra assertions that bypass history-service, so a storage-path
// regression can't hide behind a self-consistent service layer.

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

// Catches a retry-without-dedup regression that writes the same message
// under multiple clustering keys.
func TestCassandra_MessagesByRoomSingleRow(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	alice, _, roomID := setupChannelRoom(t, ctx, site, "alice", "bob")

	js := site.JetStream(t)
	canonical := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx, js, canonical, "message-worker")

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
	awaitMessageOnSite(t, ctx, site, msgID)

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

// Both denormalized views (messages_by_room, messages_by_id) must agree;
// a divergence silently breaks get-by-id + duplicate detection.
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
	awaitMessageOnSite(t, ctx, site, msgID)

	sess := site.CassandraSession(t)
	defer sess.Close()

	// Iter() (not Scan) so a duplicate-write regression surfaces as count>1.
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

// scanOneRow returns (message_id, room_id, row_count); caller asserts count==1.
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

// Send the same messageID twice; PK (message_id, created_at) must dedupe to one row.
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
	}

	msgID := idgen.GenerateMessageID()
	sendOnce(msgID, "first attempt")
	// Wait for the FIRST landing before sending the dup so we don't race
	// the dedup PK with two pre-write attempts.
	awaitMessageOnSite(t, ctx, site, msgID)
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
