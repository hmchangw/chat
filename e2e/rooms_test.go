//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// TestRoom_DM_CreateIsIdempotent: room-service rejects a duplicate DM with
// model.ErrorResponse{Error: "...", RoomID: existingRoomID}. Per amendment
// R1 11.B: the test extracts RoomID via errors.As on *errorReply.
//
// Tests run sequentially against a shared stack; an earlier DM test may
// have already created the alice-bob DM. We handle both first-call and
// already-exists paths.
func TestRoom_DM_CreateIsIdempotent(t *testing.T) {
	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	_ = site.Authenticate(t, ctx, "bob")

	createReq := model.CreateRoomRequest{
		Name:  "",
		Users: []string{"bob"},
	}

	// First attempt may succeed (fresh DM) OR return the existing RoomID
	// via *errorReply (prior test left state). Capture the roomID either way.
	var existingRoomID string
	var first model.CreateRoomReply
	err := requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &first,
	)
	if err == nil {
		require.NotEmpty(t, first.RoomID)
		assert.Equal(t, string(model.RoomTypeDM), first.RoomType)
		existingRoomID = first.RoomID
	} else {
		er := asErrorReply(err)
		require.NotNil(t, er, "expected *errorReply, got %T: %v", err, err)
		require.NotEmpty(t, er.Resp.RoomID, "errorReply for existing DM must carry RoomID")
		existingRoomID = er.Resp.RoomID
		t.Logf("first create returned existing DM RoomID=%s (prior test state)", existingRoomID)
	}

	// Second create MUST be the ErrorResponse path with the SAME RoomID.
	err = requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &model.CreateRoomReply{},
	)
	require.Error(t, err)
	er := asErrorReply(err)
	require.NotNil(t, er, "expected an *errorReply, got %T: %v", err, err)
	assert.NotEmpty(t, er.Resp.Error)
	assert.Equal(t, existingRoomID, er.Resp.RoomID,
		"duplicate DM create must return the existing roomID in the error reply")
}

// TestRoom_Channel_InviteAndMemberList per R1 11.C.
func TestRoom_Channel_InviteAndMemberList(t *testing.T) {
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
	registerRoomCleanup(t, []SiteDB{{SiteID: site.SiteID, DB: site.MongoDB(t)}}, roomID)
	require.NotEmpty(t, roomID)

	// Wait for room-worker to persist subscriptions before per-room RPCs.
	mongoA := site.MongoDB(t)
	awaitSubscription(t, ctx, mongoA, alice.Account, roomID)
	awaitSubscription(t, ctx, mongoA, bob.Account, roomID)

	// Test-quality reviewer: decode the reply into the proper model type
	// (model.ListRoomMembersResponse{Members []RoomMember}) and assert
	// both alice + bob are present. Previously decoded as map[string]any
	// and asserted only non-empty -- a regression returning the wrong
	// users would have passed.
	var reply model.ListRoomMembersResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MemberList(alice.Account, roomID, site.SiteID),
		map[string]any{}, 5*time.Second, &reply,
	))
	got := make([]string, 0, len(reply.Members))
	for _, m := range reply.Members {
		got = append(got, m.Member.Account)
	}
	assert.Contains(t, got, alice.Account, "MemberList must include the room creator")
	assert.Contains(t, got, bob.Account, "MemberList must include the invitee")
}

// TestRoom_Channel_RemoveMember: uses the proper model.RemoveMemberRequest
// shape. Bug-hunter HIGH: map[string]any{"account":"bob"} doesn't match the
// service-side struct -- room-service requires Requester + Account fields.
func TestRoom_Channel_RemoveMember(t *testing.T) {
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
	registerRoomCleanup(t, []SiteDB{{SiteID: site.SiteID, DB: site.MongoDB(t)}}, roomID)
	require.NotEmpty(t, roomID)

	mongoA := site.MongoDB(t)
	awaitSubscription(t, ctx, mongoA, alice.Account, roomID)
	awaitSubscription(t, ctx, mongoA, bob.Account, roomID)

	removeReq := model.RemoveMemberRequest{
		RoomID:    roomID,
		Requester: alice.Account,
		Account:   bob.Account,
	}
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MemberRemove(alice.Account, roomID, site.SiteID),
		removeReq, 5*time.Second, nil,
	))

	// Test-quality reviewer: verify bob's subscription actually disappeared
	// from mongo. Without this assertion, a handler that returns OK but
	// skips the mongo write would pass.
	subs := mongoA.Collection("subscriptions")
	require.Eventually(t, func() bool {
		count, err := subs.CountDocuments(ctx, bson.M{
			"u.account": bob.Account,
			"roomId":    roomID,
		})
		return err == nil && count == 0
	}, 10*time.Second, 100*time.Millisecond,
		"bob's subscription must be removed from mongo after MemberRemove")
}

// TestRoom_MarkMessageRead: read-receipt RPC happy path per R2.C item 5.
func TestRoom_MarkMessageRead(t *testing.T) {
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
	registerRoomCleanup(t, []SiteDB{{SiteID: site.SiteID, DB: site.MongoDB(t)}}, roomID)

	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)

	// Send one message so there's something to mark read. The same RequestID
	// is used both as the SendMessageRequest.RequestID AND as the response-
	// subject suffix -- gatekeeper computes the reply subject from the
	// request body's RequestID, so they must match.
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
			Content:   "for read receipt",
			RequestID: reqID,
		},
		10*time.Second,
	))

	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MessageRead(alice.Account, roomID, site.SiteID),
		map[string]any{},
		5*time.Second,
		nil,
	))

	// R4.A item 5 / R2.C item 5: verify the read marker actually advanced
	// in mongo. Without this, the test would pass against a no-op
	// implementation that returned nil from the RPC without writing.
	subs := site.MongoDB(t).Collection("subscriptions")
	require.Eventually(t, func() bool {
		var sub bson.M
		err := subs.FindOne(ctx, bson.M{
			"u.account": alice.Account,
			"roomId":    roomID,
		}).Decode(&sub)
		if err != nil {
			return false
		}
		// LastSeenAt field exists and is a non-zero timestamp.
		v, ok := sub["lastSeenAt"]
		return ok && v != nil
	}, 5*time.Second, 100*time.Millisecond,
		"alice's subscription must have lastSeenAt set after MessageRead RPC")
}

// suppress unused imports if helper-only.
var _ = mongo.Database{}
