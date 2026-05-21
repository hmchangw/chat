package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/service/mocks"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/subject"
)

// reactFixture wires the mocks for ReactMessage tests with permissive room
// defaults so individual tests only stub what they actually care about.
type reactFixture struct {
	svc interface {
		ReactMessage(c *natsrouter.Context, siteID string, req models.ReactMessageRequest) (*models.ReactMessageResponse, error)
	}
	msgs         *mocks.MockMessageRepository
	subs         *mocks.MockSubscriptionRepository
	pub          *mocks.MockEventPublisher
	users        *mocks.MockUserStore
	customEmojis *mocks.MockCustomEmojiStore
}

func newReactFixture(t *testing.T) reactFixture {
	svc, msgs, subs, rooms, pub, _, users, customEmojis := newServiceWithRoomMock(t)
	rooms.EXPECT().GetMinUserLastSeenAt(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	rooms.EXPECT().GetRoomTimes(gomock.Any(), gomock.Any()).Return(defaultRoomLastMsgAt, defaultRoomCreatedAt, nil).AnyTimes()
	return reactFixture{svc: svc, msgs: msgs, subs: subs, pub: pub, users: users, customEmojis: customEmojis}
}

func aliceUser() model.User {
	return model.User{
		ID:          "user-alice",
		Account:     "u1",
		SiteID:      "site-test",
		EngName:     "Alice",
		ChineseName: "Alice CN",
	}
}

func TestHistoryService_ReactMessage_NotSubscribed(t *testing.T) {
	f := newReactFixture(t)
	f.subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, false, nil)

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: "thumbsup"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeForbidden, routeErr.Code)
}

func TestHistoryService_ReactMessage_EmptyMessageID(t *testing.T) {
	f := newReactFixture(t)

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "", Shortcode: "thumbsup"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeBadRequest, routeErr.Code)
}

func TestHistoryService_ReactMessage_EmptyShortcode(t *testing.T) {
	f := newReactFixture(t)

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: ""})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeBadRequest, routeErr.Code)
}

func TestHistoryService_ReactMessage_InvalidShortcodeFormat(t *testing.T) {
	f := newReactFixture(t)

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: ":thumbsup:"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeBadRequest, routeErr.Code)
}

func TestHistoryService_ReactMessage_UnknownCustomShortcode(t *testing.T) {
	f := newReactFixture(t)
	f.customEmojis.EXPECT().CustomEmojiExists(gomock.Any(), "site-test", "no_such_emoji").Return(false, nil)

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: "no_such_emoji"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeBadRequest, routeErr.Code)
}

func TestHistoryService_ReactMessage_MessageNotFound(t *testing.T) {
	f := newReactFixture(t)
	f.subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
	f.msgs.EXPECT().GetMessageByID(gomock.Any(), "missing").Return(nil, nil)

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "missing", Shortcode: "thumbsup"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeNotFound, routeErr.Code)
}

func TestHistoryService_ReactMessage_AddOnDeleted_Blocked(t *testing.T) {
	f := newReactFixture(t)
	createdAt := joinTime.Add(1 * time.Minute)
	f.subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
	deleted := &models.Message{
		MessageID: "m1",
		RoomID:    "r1",
		CreatedAt: createdAt,
		Sender:    models.Participant{ID: "user-bob", Account: "bob"},
		Deleted:   true,
		// No prior reaction from alice — this is a fresh add attempt.
		Reactions: nil,
	}
	f.msgs.EXPECT().GetMessageByID(gomock.Any(), "m1").Return(deleted, nil)
	f.users.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"u1"}).Return([]model.User{aliceUser()}, nil)

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: "thumbsup"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeNotFound, routeErr.Code)
}

func TestHistoryService_ReactMessage_RemoveOnDeleted_Allowed(t *testing.T) {
	f := newReactFixture(t)
	createdAt := joinTime.Add(1 * time.Minute)
	f.subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
	deleted := &models.Message{
		MessageID: "m1",
		RoomID:    "r1",
		CreatedAt: createdAt,
		Sender:    models.Participant{ID: "user-bob", Account: "bob"},
		Deleted:   true,
		// Alice already reacted before the delete — removing must still work.
		Reactions: map[string][]models.Participant{
			"thumbsup": {{ID: "user-alice", Account: "u1"}},
		},
	}
	f.msgs.EXPECT().GetMessageByID(gomock.Any(), "m1").Return(deleted, nil)
	f.users.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"u1"}).Return([]model.User{aliceUser()}, nil)
	f.msgs.EXPECT().
		ToggleReaction(gomock.Any(), deleted, "thumbsup", gomock.Any(), gomock.Any()).
		Return("removed", nil)
	f.pub.EXPECT().
		Publish(gomock.Any(), subject.MsgCanonicalReacted("site-test"), gomock.Any()).
		Return(nil)

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: "thumbsup"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "removed", resp.Action)
	assert.Equal(t, "thumbsup", resp.Shortcode)
}

func TestHistoryService_ReactMessage_Add_Success_PublishesEvent(t *testing.T) {
	f := newReactFixture(t)
	createdAt := joinTime.Add(1 * time.Minute)
	f.subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
	target := &models.Message{
		MessageID: "m1",
		RoomID:    "r1",
		CreatedAt: createdAt,
		Sender:    models.Participant{ID: "user-bob", Account: "bob"},
	}
	f.msgs.EXPECT().GetMessageByID(gomock.Any(), "m1").Return(target, nil)
	f.users.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"u1"}).Return([]model.User{aliceUser()}, nil)

	f.msgs.EXPECT().
		ToggleReaction(gomock.Any(), target, "thumbsup", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *models.Message, _ string, actor models.Participant, _ time.Time) (string, error) {
			assert.Equal(t, "user-alice", actor.ID)
			assert.Equal(t, "u1", actor.Account)
			assert.Equal(t, "Alice", actor.EngName)
			return "added", nil
		})

	var publishedPayload []byte
	f.pub.EXPECT().
		Publish(gomock.Any(), subject.MsgCanonicalReacted("site-test"), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, data []byte) error {
			publishedPayload = data
			return nil
		})

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: "thumbsup"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "added", resp.Action)
	assert.Equal(t, "m1", resp.MessageID)
	assert.Equal(t, "thumbsup", resp.Shortcode)
	assert.Positive(t, resp.ReactedAt)

	require.NotEmpty(t, publishedPayload)
	var evt model.MessageEvent
	require.NoError(t, json.Unmarshal(publishedPayload, &evt))
	assert.Equal(t, model.EventReacted, evt.Event)
	assert.Equal(t, "m1", evt.Message.ID)
	assert.Equal(t, "user-bob", evt.Message.UserID)
	assert.Equal(t, "site-test", evt.SiteID)
	require.NotNil(t, evt.ReactionDelta)
	assert.Equal(t, "thumbsup", evt.ReactionDelta.Shortcode)
	assert.Equal(t, "added", evt.ReactionDelta.Action)
	assert.Equal(t, "user-alice", evt.ReactionDelta.Actor.UserID)
	assert.Equal(t, "Alice", evt.ReactionDelta.Actor.EngName)
}

func TestHistoryService_ReactMessage_Remove_Success(t *testing.T) {
	f := newReactFixture(t)
	createdAt := joinTime.Add(1 * time.Minute)
	f.subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
	target := &models.Message{
		MessageID: "m1",
		RoomID:    "r1",
		CreatedAt: createdAt,
		Sender:    models.Participant{ID: "user-bob", Account: "bob"},
		Reactions: map[string][]models.Participant{
			"thumbsup": {{ID: "user-alice", Account: "u1", EngName: "Alice"}},
		},
	}
	f.msgs.EXPECT().GetMessageByID(gomock.Any(), "m1").Return(target, nil)
	f.users.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"u1"}).Return([]model.User{aliceUser()}, nil)
	f.msgs.EXPECT().
		ToggleReaction(gomock.Any(), target, "thumbsup", gomock.Any(), gomock.Any()).
		Return("removed", nil)
	f.pub.EXPECT().Publish(gomock.Any(), subject.MsgCanonicalReacted("site-test"), gomock.Any()).Return(nil)

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: "thumbsup"})
	require.NoError(t, err)
	assert.Equal(t, "removed", resp.Action)
}

func TestHistoryService_ReactMessage_UserLookupError(t *testing.T) {
	f := newReactFixture(t)
	createdAt := joinTime.Add(1 * time.Minute)
	f.subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
	target := &models.Message{
		MessageID: "m1", RoomID: "r1", CreatedAt: createdAt,
		Sender: models.Participant{ID: "user-bob", Account: "bob"},
	}
	f.msgs.EXPECT().GetMessageByID(gomock.Any(), "m1").Return(target, nil)
	f.users.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"u1"}).Return(nil, errors.New("mongo down"))

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: "thumbsup"})
	assert.Nil(t, resp)
	assertInternalErr(t, err, "failed to resolve actor")
}

func TestHistoryService_ReactMessage_UserNotFound(t *testing.T) {
	f := newReactFixture(t)
	createdAt := joinTime.Add(1 * time.Minute)
	f.subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
	target := &models.Message{
		MessageID: "m1", RoomID: "r1", CreatedAt: createdAt,
		Sender: models.Participant{ID: "user-bob", Account: "bob"},
	}
	f.msgs.EXPECT().GetMessageByID(gomock.Any(), "m1").Return(target, nil)
	f.users.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"u1"}).Return(nil, nil)

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: "thumbsup"})
	assert.Nil(t, resp)
	assertInternalErr(t, err, "actor not found")
}

func TestHistoryService_ReactMessage_StoreError(t *testing.T) {
	f := newReactFixture(t)
	createdAt := joinTime.Add(1 * time.Minute)
	f.subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
	target := &models.Message{
		MessageID: "m1", RoomID: "r1", CreatedAt: createdAt,
		Sender: models.Participant{ID: "user-bob", Account: "bob"},
	}
	f.msgs.EXPECT().GetMessageByID(gomock.Any(), "m1").Return(target, nil)
	f.users.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"u1"}).Return([]model.User{aliceUser()}, nil)
	f.msgs.EXPECT().
		ToggleReaction(gomock.Any(), target, "thumbsup", gomock.Any(), gomock.Any()).
		Return("", errors.New("cas exceeded"))

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: "thumbsup"})
	assert.Nil(t, resp)
	assertInternalErr(t, err, "failed to toggle reaction")
}

func TestHistoryService_ReactMessage_CustomEmojiFound_Success(t *testing.T) {
	f := newReactFixture(t)
	createdAt := joinTime.Add(1 * time.Minute)
	f.customEmojis.EXPECT().CustomEmojiExists(gomock.Any(), "site-test", "acme_party").Return(true, nil)
	f.subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
	target := &models.Message{
		MessageID: "m1", RoomID: "r1", CreatedAt: createdAt,
		Sender: models.Participant{ID: "user-bob", Account: "bob"},
	}
	f.msgs.EXPECT().GetMessageByID(gomock.Any(), "m1").Return(target, nil)
	f.users.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"u1"}).Return([]model.User{aliceUser()}, nil)
	f.msgs.EXPECT().
		ToggleReaction(gomock.Any(), target, "acme_party", gomock.Any(), gomock.Any()).
		Return("added", nil)
	f.pub.EXPECT().Publish(gomock.Any(), subject.MsgCanonicalReacted("site-test"), gomock.Any()).Return(nil)

	resp, err := f.svc.ReactMessage(testContext(), "site-test",
		models.ReactMessageRequest{MessageID: "m1", Shortcode: "acme_party"})
	require.NoError(t, err)
	assert.Equal(t, "added", resp.Action)
}
