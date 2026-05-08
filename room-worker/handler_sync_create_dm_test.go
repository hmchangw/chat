package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// newRequestCtx returns a context carrying a syntactically-valid X-Request-ID.
func newRequestCtx() context.Context {
	return natsutil.WithRequestID(context.Background(), "01970a4f-8c2d-7c9a-abcd-e0123456789f")
}

// dmCapturedPublish + dmPublishCapture are unit-test-local equivalents of the
// integration_test.go types; same shape under a different name to avoid collision
// when both files compile together under the `integration` build tag.
type dmCapturedPublish struct {
	subject string
	data    []byte
	msgID   string
}

type dmPublishCapture struct {
	mu       sync.Mutex
	captured []dmCapturedPublish
}

func (c *dmPublishCapture) fn(_ context.Context, subj string, data []byte, msgID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.captured = append(c.captured, dmCapturedPublish{subject: subj, data: append([]byte(nil), data...), msgID: msgID})
	return nil
}

// newSyncDMTestHandler builds a Handler wired to a fresh mock store + capture.
// Mirrors newAddMembersTestHandler's shape for consistency.
func newSyncDMTestHandler(t *testing.T) (*Handler, *MockSubscriptionStore, *dmPublishCapture) {
	t.Helper()
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)
	capture := &dmPublishCapture{}
	h := &Handler{siteID: "site-a", store: store, publish: capture.fn}
	return h, store, capture
}

// marshalReq JSON-encodes v or fails the test on error.
func marshalReq(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func TestSanitizeSyncDMError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want string
	}{
		{"nil returns empty", nil, ""},
		{"missing request ID surfaced", errMissingRequestID, "missing X-Request-ID header"},
		{"invalid request ID surfaced", errInvalidRequestID, "invalid X-Request-ID header"},
		{"invalid sync DM request surfaced", errInvalidSyncDMRequest, "invalid sync DM request"},
		{"user lookup failed surfaced", errUserLookupFailed, "user lookup failed"},
		{"cross-site requester surfaced", errCrossSiteRequester, "requester is not on this site"},
		{"room ID collision masked as internal", errRoomIDCollision, "internal error"},
		{"unknown error masked as internal", errors.New("mongo: connection refused"), "internal error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeSyncDMError(tc.in))
		})
	}
}

func TestHandleSyncCreateDM_MissingRequestID(t *testing.T) {
	h := &Handler{siteID: "site-a"}
	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(context.Background(), data)
	assert.ErrorIs(t, err, errMissingRequestID)
}

func TestHandleSyncCreateDM_InvalidJSON(t *testing.T) {
	h := &Handler{siteID: "site-a"}
	_, err := h.handleSyncCreateDM(newRequestCtx(), []byte("{not json"))
	assert.ErrorIs(t, err, errInvalidSyncDMRequest)
}

func TestHandleSyncCreateDM_InvalidRoomType(t *testing.T) {
	h := &Handler{siteID: "site-a"}
	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeChannel,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errInvalidSyncDMRequest)
}

func TestHandleSyncCreateDM_EmptyAccounts(t *testing.T) {
	h := &Handler{siteID: "site-a"}
	cases := []model.SyncCreateDMRequest{
		{RoomType: model.RoomTypeDM, RequesterAccount: "", OtherAccount: "bob"},
		{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: ""},
	}
	for _, req := range cases {
		data := marshalReq(t, req)
		_, err := h.handleSyncCreateDM(newRequestCtx(), data)
		assert.ErrorIs(t, err, errInvalidSyncDMRequest)
	}
}

func TestHandleSyncCreateDM_SelfDM(t *testing.T) {
	h := &Handler{siteID: "site-a"}
	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "alice",
	}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errInvalidSyncDMRequest)
}

func TestHandleSyncCreateDM_RequesterNotFound(t *testing.T) {
	h, store, _ := newSyncDMTestHandler(t)

	store.EXPECT().GetUser(gomock.Any(), "alice").Return(nil, ErrUserNotFound)

	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errUserLookupFailed)
}

func TestHandleSyncCreateDM_OtherNotFound(t *testing.T) {
	h, store, _ := newSyncDMTestHandler(t)

	store.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(nil, ErrUserNotFound)

	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errUserLookupFailed)
}

func TestHandleSyncCreateDM_CrossSiteRequester(t *testing.T) {
	h, store, _ := newSyncDMTestHandler(t)

	store.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u-alice", Account: "alice", SiteID: "site-b"}, nil)

	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errCrossSiteRequester)
}

func TestHandleSyncCreateDM_RoomCollisionMismatch(t *testing.T) {
	roomID := idgen.BuildDMRoomID("u-alice", "u-bob")
	cases := []struct {
		name     string
		existing model.Room
	}{
		{"type mismatch", model.Room{ID: roomID, Type: model.RoomTypeChannel, SiteID: "site-a", Name: "", CreatedBy: "u-alice"}},
		{"siteID mismatch", model.Room{ID: roomID, Type: model.RoomTypeDM, SiteID: "site-other", Name: "", CreatedBy: "u-alice"}},
		{"name mismatch", model.Room{ID: roomID, Type: model.RoomTypeDM, SiteID: "site-a", Name: "leak", CreatedBy: "u-alice"}},
		{"createdBy mismatch", model.Room{ID: roomID, Type: model.RoomTypeDM, SiteID: "site-a", Name: "", CreatedBy: "u-eve"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, _ := newSyncDMTestHandler(t)
			requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
			other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
			store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
			store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
			dupErr := mongo.WriteException{WriteErrors: []mongo.WriteError{{Code: 11000}}}
			store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(dupErr)
			existing := tc.existing
			store.EXPECT().GetRoom(gomock.Any(), gomock.Any()).Return(&existing, nil)

			req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
			data := marshalReq(t, req)
			_, err := h.handleSyncCreateDM(newRequestCtx(), data)
			assert.ErrorIs(t, err, errRoomIDCollision)
		})
	}
}

func TestHandleSyncCreateDM_DM_PersistsSubsAndReturnsRequester(t *testing.T) {
	h, store, _ := newSyncDMTestHandler(t)

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
	var insertedRoom *model.Room
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, r *model.Room) error {
			insertedRoom = r
			return nil
		})

	var captured []*model.Subscription
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
			captured = subs
			return nil
		})
	store.EXPECT().FindDMSubscription(gomock.Any(), "alice", "bob").Return(&model.Subscription{
		ID:       "canonical-alice-sub",
		User:     model.SubscriptionUser{ID: "u-alice", Account: "alice"},
		RoomID:   idgen.BuildDMRoomID("u-alice", "u-bob"),
		Name:     "bob",
		RoomType: model.RoomTypeDM,
	}, nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data := marshalReq(t, req)
	rawReply, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	var reply model.SyncCreateDMReply
	require.NoError(t, json.Unmarshal(rawReply, &reply))
	assert.True(t, reply.Success)
	assert.Equal(t, "canonical-alice-sub", reply.Subscription.ID)

	// DM room is inserted with userCount=2, appCount=0 — no Reconcile needed.
	require.NotNil(t, insertedRoom)
	assert.Equal(t, 2, insertedRoom.UserCount)
	assert.Equal(t, 0, insertedRoom.AppCount)

	// Both subs persisted: requester names other (IsSubscribed=false), other names requester.
	require.Len(t, captured, 2)
	roomID := idgen.BuildDMRoomID("u-alice", "u-bob")
	subByAccount := map[string]*model.Subscription{}
	for _, s := range captured {
		subByAccount[s.User.Account] = s
	}
	require.Contains(t, subByAccount, "alice")
	require.Contains(t, subByAccount, "bob")
	assert.Equal(t, "u-alice", subByAccount["alice"].User.ID)
	assert.Equal(t, roomID, subByAccount["alice"].RoomID)
	assert.Equal(t, "bob", subByAccount["alice"].Name)
	assert.Equal(t, model.RoomTypeDM, subByAccount["alice"].RoomType)
	assert.False(t, subByAccount["alice"].IsSubscribed)
	assert.Equal(t, "u-bob", subByAccount["bob"].User.ID)
	assert.Equal(t, "alice", subByAccount["bob"].Name)
	assert.False(t, subByAccount["bob"].IsSubscribed)
}

func TestHandleSyncCreateDM_BotDM_RequesterSubIsSubscribedTrue(t *testing.T) {
	h, store, _ := newSyncDMTestHandler(t)

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	bot := &model.User{ID: "u-bot", Account: "helper.bot", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "helper.bot").Return(bot, nil)
	var insertedRoom *model.Room
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, r *model.Room) error {
			insertedRoom = r
			return nil
		})
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().FindDMSubscription(gomock.Any(), "alice", "helper.bot").Return(&model.Subscription{
		User: model.SubscriptionUser{ID: "u-alice", Account: "alice"}, IsSubscribed: true,
	}, nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeBotDM, RequesterAccount: "alice", OtherAccount: "helper.bot"}
	data := marshalReq(t, req)
	rawReply, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	var reply model.SyncCreateDMReply
	require.NoError(t, json.Unmarshal(rawReply, &reply))
	assert.True(t, reply.Subscription.IsSubscribed)
	assert.Equal(t, "alice", reply.Subscription.User.Account)

	// botDM room is inserted with userCount=1, appCount=1 — no Reconcile needed.
	require.NotNil(t, insertedRoom)
	assert.Equal(t, 1, insertedRoom.UserCount)
	assert.Equal(t, 1, insertedRoom.AppCount)
}

// On dup-key race, BulkCreateSubscriptions swallows the error and the in-memory subs
// carry stale state; the handler must return the canonical persisted sub via FindDMSubscription.
func TestHandleSyncCreateDM_ReturnsCanonicalPersistedSub(t *testing.T) {
	h, store, _ := newSyncDMTestHandler(t)

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	existingSub := &model.Subscription{
		ID:       "canonical-sub",
		User:     model.SubscriptionUser{ID: "u-alice", Account: "alice"},
		RoomID:   idgen.BuildDMRoomID("u-alice", "u-bob"),
		Name:     "bob",
		RoomType: model.RoomTypeDM,
	}
	store.EXPECT().FindDMSubscription(gomock.Any(), "alice", "bob").Return(existingSub, nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data := marshalReq(t, req)
	rawReply, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	var reply model.SyncCreateDMReply
	require.NoError(t, json.Unmarshal(rawReply, &reply))
	assert.Equal(t, "canonical-sub", reply.Subscription.ID)
}

// Transient store errors on GetUser must NOT be sanitized as errUserLookupFailed (which
// signals "user does not exist"); they should propagate as wrapped errors and surface
// as "internal error" via sanitizeSyncDMError.
func TestHandleSyncCreateDM_GetUserTransientError_Internal(t *testing.T) {
	h, store, _ := newSyncDMTestHandler(t)

	store.EXPECT().GetUser(gomock.Any(), "alice").Return(nil, errors.New("mongo: connection refused"))

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errUserLookupFailed,
		"transient error must not be tagged as user-not-found")
	assert.Equal(t, "internal error", sanitizeSyncDMError(err))
}

func TestHandleSyncCreateDM_PublishesSubscriptionUpdateForBothUsers(t *testing.T) {
	h, store, capture := newSyncDMTestHandler(t)

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().FindDMSubscription(gomock.Any(), "alice", "bob").Return(&model.Subscription{
		User: model.SubscriptionUser{ID: "u-alice", Account: "alice"},
	}, nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	subjects := map[string]int{}
	for _, p := range capture.captured {
		subjects[p.subject]++
	}
	assert.Equal(t, 1, subjects[subject.SubscriptionUpdate("alice")])
	assert.Equal(t, 1, subjects[subject.SubscriptionUpdate("bob")])
}

func TestHandleSyncCreateDM_CrossSite_EmitsOutbox(t *testing.T) {
	h, store, capture := newSyncDMTestHandler(t)

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-b"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().FindDMSubscription(gomock.Any(), "alice", "bob").Return(&model.Subscription{
		User: model.SubscriptionUser{ID: "u-alice", Account: "alice"},
	}, nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	var outbox *dmCapturedPublish
	for i := range capture.captured {
		if capture.captured[i].subject == subject.Outbox("site-a", "site-b", model.OutboxTypeRoomCreated) {
			outbox = &capture.captured[i]
			break
		}
	}
	require.NotNil(t, outbox, "expected an outbox publish to site-b")

	var env model.OutboxEvent
	require.NoError(t, json.Unmarshal(outbox.data, &env))
	assert.Equal(t, model.OutboxEventType(model.OutboxTypeRoomCreated), env.Type)
	assert.Equal(t, "site-a", env.SiteID)
	assert.Equal(t, "site-b", env.DestSiteID)

	var payload model.RoomCreatedOutbox
	require.NoError(t, json.Unmarshal(env.Payload, &payload))
	assert.Equal(t, model.RoomTypeDM, payload.RoomType)
	assert.Equal(t, "", payload.RoomName)
	assert.Equal(t, "site-a", payload.HomeSiteID)
	assert.Equal(t, []string{"bob"}, payload.Accounts)
	assert.Equal(t, "alice", payload.RequesterAccount)
	assert.Equal(t, "01970a4f-8c2d-7c9a-abcd-e0123456789f:site-b", outbox.msgID)
}

func TestHandleSyncCreateDM_SameSite_NoOutbox(t *testing.T) {
	h, store, capture := newSyncDMTestHandler(t)

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().FindDMSubscription(gomock.Any(), "alice", "bob").Return(&model.Subscription{
		User: model.SubscriptionUser{ID: "u-alice", Account: "alice"},
	}, nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	for _, p := range capture.captured {
		assert.NotContains(t, p.subject, "outbox.", "no outbox publish expected for same-site DM")
	}
}

// Outbox publish failure must fail the request — otherwise the requester sees success
// while the remote site never learns about the room.
func TestHandleSyncCreateDM_OutboxPublishFails_FailsRequest(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)
	failingPublish := func(_ context.Context, subj string, _ []byte, _ string) error {
		if strings.HasPrefix(subj, "outbox.") {
			return errors.New("jetstream pubAck failed")
		}
		return nil
	}
	h := &Handler{siteID: "site-a", store: store, publish: failingPublish}

	store.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u-bob", Account: "bob", SiteID: "site-b"}, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().FindDMSubscription(gomock.Any(), "alice", "bob").Return(&model.Subscription{
		User: model.SubscriptionUser{ID: "u-alice", Account: "alice"},
	}, nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.Error(t, err)
	assert.Equal(t, "internal error", sanitizeSyncDMError(err))
}

// BulkCreateSubscriptions returning a non-dup-key error must surface as "internal error".
func TestHandleSyncCreateDM_BulkCreateSubsTransientError(t *testing.T) {
	h, store, _ := newSyncDMTestHandler(t)

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
		Return(errors.New("mongo: connection reset"))

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.Error(t, err)
	assert.Equal(t, "internal error", sanitizeSyncDMError(err))
}

// On a CreateRoom dup-key with matching existing room (idempotent re-delivery),
// the handler must reuse existing.CreatedAt as acceptedAt — sub.JoinedAt and event
// timestamps reflect the original creation, not retry wall-clock.
func TestHandleSyncCreateDM_IdempotentRecreate_UsesExistingCreatedAt(t *testing.T) {
	h, store, _ := newSyncDMTestHandler(t)

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)

	// CreateRoom hits dup-key; GetRoom returns a matching existing room with a known CreatedAt.
	originalCreatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	roomID := idgen.BuildDMRoomID("u-alice", "u-bob")
	dupErr := mongo.WriteException{WriteErrors: []mongo.WriteError{{Code: 11000}}}
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(dupErr)
	store.EXPECT().GetRoom(gomock.Any(), gomock.Any()).Return(&model.Room{
		ID: roomID, Type: model.RoomTypeDM, SiteID: "site-a",
		Name: "", CreatedBy: "u-alice",
		CreatedAt: originalCreatedAt, UpdatedAt: originalCreatedAt,
	}, nil)

	var captured []*model.Subscription
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
			captured = subs
			return nil
		})
	store.EXPECT().FindDMSubscription(gomock.Any(), "alice", "bob").Return(&model.Subscription{
		User: model.SubscriptionUser{ID: "u-alice", Account: "alice"}, JoinedAt: originalCreatedAt,
	}, nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data := marshalReq(t, req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	require.Len(t, captured, 2)
	for _, s := range captured {
		assert.Equal(t, originalCreatedAt, s.JoinedAt,
			"sub.JoinedAt must reflect existing.CreatedAt on idempotent re-delivery, not retry wall-clock")
	}
}
