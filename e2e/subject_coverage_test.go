//go:build e2e

// Subject-coverage tests for the documented client-API subjects that the
// other test files don't exercise. Each test is a thin happy-path call
// confirming the subject's handler is wired and returns a structurally
// reasonable reply. Failures here mean a registered handler stopped working,
// not that a complex flow regressed -- pair with the deeper scenario tests
// for full coverage.

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/model/cassandra"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// getMessageByIDRequest / Response mirror history-service/internal/models.
type getMessageByIDRequest struct {
	MessageID string `json:"messageId"`
}

// GetMessageByID replies with cassandra.Message directly (not wrapped).

type loadNextMessagesRequest struct {
	After  *int64 `json:"after,omitempty"`
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor"`
}

type loadNextMessagesResponse struct {
	Messages   []cassandra.Message `json:"messages"`
	NextCursor string              `json:"nextCursor,omitempty"`
	HasNext    bool                `json:"hasNext"`
}

type loadSurroundingMessagesRequest struct {
	MessageID string `json:"messageId"`
	Limit     int    `json:"limit"`
}

type loadSurroundingMessagesResponse struct {
	Messages   []cassandra.Message `json:"messages"`
	MoreBefore bool                `json:"moreBefore"`
	MoreAfter  bool                `json:"moreAfter"`
}

// setupRoomWithOneMessage: alice + bob, channel, one message sent and
// ack'd. Returns roomID + msgID for downstream queries.
func setupRoomWithOneMessage(t *testing.T) (roomID, msgID string) {
	t.Helper()
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
	roomID = createReply.RoomID
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, site)}, roomID)
	mongoA := site.MongoDB(t)
	awaitSubscription(t, ctx, mongoA, alice.Account, roomID)
	awaitSubscription(t, ctx, mongoA, bob.Account, roomID)

	js := site.JetStream(t)
	canonical := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx, js, canonical, "message-worker")

	msgID = idgen.GenerateMessageID()
	reqID := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		reqID,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   "subject-coverage message " + t.Name(),
			RequestID: reqID,
		},
		10*time.Second,
	))
	// Wait by msgID, not by canonical seq -- parallel siblings'
	// messages can satisfy a seq-based wait before THIS message lands
	// in Cassandra (per testing-automation review).
	sess := site.CassandraSession(t)
	defer sess.Close()
	awaitMessageByID(t, ctx, sess, msgID)
	return roomID, msgID
}

// TestSubject_MsgGet exercises subject.MsgGet -> history-service.
// Previously zero coverage of the get-by-id path.
func TestSubject_MsgGet(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA
	alice := site.Authenticate(t, ctx, "alice")
	roomID, msgID := setupRoomWithOneMessage(t)

	var msg cassandra.Message
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MsgGet(alice.Account, roomID, site.SiteID),
		getMessageByIDRequest{MessageID: msgID},
		5*time.Second, &msg,
	))
	assert.Equal(t, msgID, msg.MessageID)
	assert.Equal(t, alice.Account, msg.Sender.Account)
}

// TestSubject_MsgNext exercises subject.MsgNext -> history-service.
// Two messages are sent so the `After` cursor can be proven functional:
// querying After=msg1.createdAt must return msg2 but NOT msg1. A
// previous version of this test used After=0 with one message, which
// would pass even if the handler ignored the `After` filter entirely.
func TestSubject_MsgNext(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA
	alice := site.Authenticate(t, ctx, "alice")
	roomID, msgID1 := setupRoomWithOneMessage(t)

	// Second message in the same room, so we have two messages to
	// discriminate via the After cursor.
	msgID2 := idgen.GenerateMessageID()
	reqID2 := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		reqID2,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID2,
			Content:   "second message " + t.Name(),
			RequestID: reqID2,
		},
		10*time.Second,
	))
	// Wait by msgID (parallel-safe; see setupRoomWithOneMessage).
	sess2 := site.CassandraSession(t)
	defer sess2.Close()
	awaitMessageByID(t, ctx, sess2, msgID2)

	// Find msg1's createdAt via the history endpoint so we can pass it as
	// the After cursor.
	var hist loadHistoryResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MsgHistory(alice.Account, roomID, site.SiteID),
		loadHistoryRequest{Limit: 50},
		5*time.Second, &hist,
	))
	var msg1Created int64
	for _, m := range hist.Messages {
		if m.MessageID == msgID1 {
			msg1Created = m.CreatedAt.UnixMilli()
			break
		}
	}
	require.NotZero(t, msg1Created, "msg1 must appear in history")

	var resp loadNextMessagesResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MsgNext(alice.Account, roomID, site.SiteID),
		loadNextMessagesRequest{After: &msg1Created, Limit: 100},
		5*time.Second, &resp,
	))
	var sawMsg1, sawMsg2 bool
	for _, m := range resp.Messages {
		switch m.MessageID {
		case msgID1:
			sawMsg1 = true
		case msgID2:
			sawMsg2 = true
		}
	}
	assert.True(t, sawMsg2, "MsgNext After=msg1.createdAt must return msg2 (got %d entries)", len(resp.Messages))
	assert.False(t, sawMsg1, "MsgNext After=msg1.createdAt must NOT return msg1 itself")
}

// TestSubject_MsgSurrounding exercises subject.MsgSurrounding ->
// history-service. The "surrounding" query returns messages around a
// central message ID; with one message in the room the central message
// itself is the only result.
func TestSubject_MsgSurrounding(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA
	alice := site.Authenticate(t, ctx, "alice")
	roomID, msgID := setupRoomWithOneMessage(t)

	var resp loadSurroundingMessagesResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MsgSurrounding(alice.Account, roomID, site.SiteID),
		loadSurroundingMessagesRequest{MessageID: msgID, Limit: 5},
		5*time.Second, &resp,
	))
	var found bool
	for _, m := range resp.Messages {
		if m.MessageID == msgID {
			found = true
			break
		}
	}
	assert.True(t, found, "MsgSurrounding must include the central message")
}

// TestSubject_RoomsList exercises subject.RoomsList -> room-service. After
// creating a room, alice's room list must include it.
func TestSubject_RoomsList(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA
	alice := site.Authenticate(t, ctx, "alice")
	roomID, _ := setupRoomWithOneMessage(t)

	var resp model.ListRoomsResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomsList(alice.Account),
		map[string]any{},
		5*time.Second, &resp,
	))
	var found bool
	for _, r := range resp.Rooms {
		if r.ID == roomID {
			found = true
			break
		}
	}
	assert.True(t, found, "RoomsList must include the freshly created room %s", roomID)
}

// TestSubject_RoomsGet exercises subject.RoomsGet -> room-service. The
// roomID is part of the subject; reply carries the Room struct.
func TestSubject_RoomsGet(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA
	alice := site.Authenticate(t, ctx, "alice")
	roomID, _ := setupRoomWithOneMessage(t)

	var room model.Room
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomsGet(alice.Account, roomID),
		map[string]any{},
		5*time.Second, &room,
	))
	assert.Equal(t, roomID, room.ID)
	assert.Equal(t, model.RoomTypeChannel, room.Type)
}

// TestSubject_OrgMembers exercises subject.OrgMembers -> room-service.
// The realm has no org fixture, so an unknown orgID returns an empty list
// (the realm-export.json doesn't populate org membership). The point of
// this test is to confirm the handler is wired and returns a structured
// ListOrgMembersResponse, not the specific membership content.
func TestSubject_OrgMembers(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA
	alice := site.Authenticate(t, ctx, "alice")

	var resp model.ListOrgMembersResponse
	err := requestReply(
		alice.Conn(),
		subject.OrgMembers(alice.Account, "test-org"),
		map[string]any{},
		5*time.Second, &resp,
	)
	// Accept either a successful (likely empty) response OR an
	// ErrorResponse (room-service may reject a totally-unknown orgID).
	// What we won't accept: a NATS timeout / nil decode -- those indicate
	// the handler isn't wired.
	if err != nil {
		er := asErrorReply(err)
		require.NotNil(t, er, "OrgMembers handler must reply (structured error ok); got %T", err)
	}
	// resp.Members may be empty or populated; either is structurally valid.
}

// TestSubject_SearchRooms exercises subject.SearchRooms -> search-service.
// search-service authorizes via the user-room ES doc -- same caching that
// blocks TestSearch_MessageVisibleAfterIndex (the messages-search path).
// Here we just confirm the handler is wired; result-correctness is out of
// scope until the deeper auth unwind from item 1.
func TestSubject_SearchRooms(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA
	alice := site.Authenticate(t, ctx, "alice")

	var resp model.SearchRoomsResponse
	err := requestReply(
		alice.Conn(),
		subject.SearchRooms(alice.Account),
		model.SearchRoomsRequest{Query: "anything", Size: 10},
		5*time.Second, &resp,
	)
	if err != nil {
		er := asErrorReply(err)
		require.NotNil(t, er, "SearchRooms handler must reply (structured error ok); got %T", err)
	}
}
