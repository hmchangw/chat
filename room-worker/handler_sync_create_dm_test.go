package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// newRequestCtx returns a context carrying a syntactically-valid X-Request-ID.
func newRequestCtx() context.Context {
	return natsutil.WithRequestID(context.Background(), "01970a4f-8c2d-7c9a-abcd-e0123456789f")
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
		{"room ID collision surfaced", errRoomIDCollision, "room ID collision (existing room metadata mismatch)"},
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
	data, _ := json.Marshal(req)
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
	data, _ := json.Marshal(req)
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
		data, _ := json.Marshal(req)
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
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errInvalidSyncDMRequest)
}

func TestHandleSyncCreateDM_RequesterNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{siteID: "site-a", store: store}

	store.EXPECT().GetUser(gomock.Any(), "alice").Return(nil, ErrUserNotFound)

	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errUserLookupFailed)
}

func TestHandleSyncCreateDM_OtherNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{siteID: "site-a", store: store}

	store.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(nil, ErrUserNotFound)

	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errUserLookupFailed)
}

func TestHandleSyncCreateDM_CrossSiteRequester(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{siteID: "site-a", store: store}

	store.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u-alice", Account: "alice", SiteID: "site-b"}, nil)

	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errCrossSiteRequester)
}

func TestHandleSyncCreateDM_RoomCollisionMismatch(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{siteID: "site-a", store: store}

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)

	dupErr := mongo.WriteException{WriteErrors: []mongo.WriteError{{Code: 11000}}}
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(dupErr)
	store.EXPECT().GetRoom(gomock.Any(), gomock.Any()).Return(&model.Room{
		ID: idgen.BuildDMRoomID("u-alice", "u-bob"), Type: model.RoomTypeChannel,
		SiteID: "site-a", Name: "", CreatedBy: "u-alice",
	}, nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errRoomIDCollision)
}
