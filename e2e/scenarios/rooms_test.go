//go:build e2e

package scenarios

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/e2e"
	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// TestRoom_DM_CreateIsIdempotent: room-service rejects a duplicate DM with
// model.ErrorResponse{Error: "...", RoomID: existingRoomID}. Per amendment
// R1 11.B: the test extracts RoomID via errors.As on *errorReply, NOT by
// expecting a normal success reply.
func TestRoom_DM_CreateIsIdempotent(t *testing.T) {
	ctx := t.Context()
	site := e2e.Stack().SiteA

	alice := site.Authenticate(t, ctx, "alice")
	_ = site.Authenticate(t, ctx, "bob")

	createReq := model.CreateRoomRequest{
		Name:  "",
		Users: []string{"bob"},
	}

	// First create -> normal success reply with a fresh roomID.
	var first model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &first,
	))
	require.NotEmpty(t, first.RoomID)
	assert.Equal(t, string(model.RoomTypeDM), first.RoomType)

	// Second create -> ErrorResponse with the same RoomID (existing DM).
	err := requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &model.CreateRoomReply{},
	)
	require.Error(t, err)
	er := asErrorReply(err)
	require.NotNil(t, er, "expected an *errorReply, got %T: %v", err, err)
	assert.NotEmpty(t, er.Resp.Error, "ErrorResponse must carry a non-empty Error (sanitization softness per R1 11.F)")
	assert.Equal(t, first.RoomID, er.Resp.RoomID,
		"duplicate DM create must return the existing roomID in the error reply")
}

// TestRoom_Channel_InviteAndMemberList: channel-create assigns a server-side
// roomID, then member-list returns alice + bob. Per amendment R1 11.C: the
// channel create itself can carry Users -- no separate MemberAdd needed for
// the initial members.
func TestRoom_Channel_InviteAndMemberList(t *testing.T) {
	ctx := t.Context()
	site := e2e.Stack().SiteA

	alice := site.Authenticate(t, ctx, "alice")
	_ = site.Authenticate(t, ctx, "bob")

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{"bob"},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	require.NotEmpty(t, roomID)

	// MemberList replies with the room's current member set. The reply shape
	// is service-specific; we just assert the call succeeds and the response
	// is non-empty JSON.
	var rawReply map[string]any
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MemberList(alice.Account, roomID, site.SiteID),
		map[string]any{}, 5*time.Second, &rawReply,
	))
	assert.NotEmpty(t, rawReply, "MemberList reply must be non-empty JSON")
}

// TestRoom_Channel_RemoveMember: alice creates a channel with bob, then
// removes bob, then asserts the remove call succeeded. Verifying bob's
// absence from the member list afterwards is a stronger but separate test;
// keep this focused on the remove RPC's happy path.
func TestRoom_Channel_RemoveMember(t *testing.T) {
	ctx := t.Context()
	site := e2e.Stack().SiteA

	alice := site.Authenticate(t, ctx, "alice")
	_ = site.Authenticate(t, ctx, "bob")

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{"bob"},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	require.NotEmpty(t, roomID)

	// MemberRemove takes a payload identifying the account(s) to remove.
	// The exact shape is room-service's RemoveMemberRequest -- not exported
	// as a stable model type, so use a map matching the JSON.
	removeReq := map[string]any{"account": "bob"}
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MemberRemove(alice.Account, roomID, site.SiteID),
		removeReq, 5*time.Second, nil,
	))
}

// TestRoom_MarkMessageRead: read-receipt RPC happy path per R2.C item 5.
// subject.MessageRead is a 3-arg builder (account, roomID, siteID per R4.A);
// payload is empty -- room-service derives the read marker from the message
// timestamp of the most-recent message in the room at request time.
func TestRoom_MarkMessageRead(t *testing.T) {
	ctx := t.Context()
	site := e2e.Stack().SiteA

	alice := site.Authenticate(t, ctx, "alice")
	_ = site.Authenticate(t, ctx, "bob")

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{"bob"},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID

	// Alice sends one message so there's something to mark read.
	msgID := idgen.GenerateMessageID()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		idgen.GenerateRequestID(),
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   "for read receipt",
			RequestID: idgen.GenerateRequestID(),
		},
		10*time.Second,
	))

	// Mark as read. Empty payload; room-service uses the room's current
	// state as the read watermark (per room-service/handler.go: payload arg
	// is `_ []byte`).
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MessageRead(alice.Account, roomID, site.SiteID),
		map[string]any{},
		5*time.Second,
		nil,
	))
}
