package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

type publishedMsg struct {
	subj string
	data []byte
}

// --- processAddMembers ---

func TestHandler_ProcessAddMembers_HappyPath(t *testing.T) {
	tests := []struct {
		name    string
		payload model.AddMembersRequest
		setup   func(store *MockSubscriptionStore)
		wantErr bool
	}{
		{
			name: "adds members, publishes SubscriptionUpdateEvents, MemberChangeEvent, system message",
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Users:   []string{"bob", "carol"},
				History: model.HistoryConfig{Mode: model.HistoryModeNone},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
					ID: "u-bob", Account: "bob", SiteID: "site-a",
				}, nil)
				store.EXPECT().GetUser(gomock.Any(), "carol").Return(&model.User{
					ID: "u-carol", Account: "carol", SiteID: "site-a",
				}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						require.Len(t, subs, 2)
						accounts := []string{subs[0].User.Account, subs[1].User.Account}
						assert.ElementsMatch(t, []string{"bob", "carol"}, accounts)
						// history=none: HistorySharedSince must be set
						assert.NotNil(t, subs[0].HistorySharedSince, "expected HistorySharedSince set for history=none")
						assert.NotNil(t, subs[1].HistorySharedSince, "expected HistorySharedSince set for history=none")
						return nil
					})
				store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil)
			},
		},
		{
			name: "history=all: HistorySharedSince is nil",
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Users:   []string{"bob"},
				History: model.HistoryConfig{Mode: model.HistoryModeAll},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
					ID: "u-bob", Account: "bob", SiteID: "site-a",
				}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						require.Len(t, subs, 1)
						assert.Nil(t, subs[0].HistorySharedSince, "expected HistorySharedSince nil for history=all")
						return nil
					})
				store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil)
			},
		},
		{
			name: "user not found (ErrNoDocuments): skipped gracefully",
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bad-user", "bob"},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
				store.EXPECT().GetUser(gomock.Any(), "bad-user").Return(nil, mongo.ErrNoDocuments)
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
					ID: "u-bob", Account: "bob", SiteID: "site-a",
				}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						require.Len(t, subs, 1, "expected only bob, bad-user should be skipped")
						assert.Equal(t, "bob", subs[0].User.Account)
						return nil
					})
				store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil)
			},
		},
		{
			name: "all users not found: empty subscription list, no IncrementUserCount",
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bad1", "bad2"},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
				store.EXPECT().GetUser(gomock.Any(), "bad1").Return(nil, mongo.ErrNoDocuments)
				store.EXPECT().GetUser(gomock.Any(), "bad2").Return(nil, mongo.ErrNoDocuments)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						assert.Empty(t, subs)
						return nil
					})
			},
		},
		{
			name: "GetUser returns unexpected error",
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bob"},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(nil, fmt.Errorf("connection refused"))
			},
			wantErr: true,
		},
		{
			name: "GetRoom fails",
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bob"},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(nil, fmt.Errorf("db error"))
			},
			wantErr: true,
		},
		{
			name: "IncrementUserCount fails",
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bob"},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
					ID: "u-bob", Account: "bob", SiteID: "site-a",
				}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(fmt.Errorf("db error"))
			},
			wantErr: true,
		},
		{
			name:    "malformed json",
			payload: model.AddMembersRequest{},
			setup:   func(store *MockSubscriptionStore) {},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockSubscriptionStore(ctrl)
			tt.setup(store)

			h := NewHandler(store, "site-a",
				func(_ context.Context, _ string, _ []byte) error { return nil },
				func(_ context.Context, _ string, _ []byte) error { return nil },
			)

			var data []byte
			if tt.name == "malformed json" {
				data = []byte("{invalid")
			} else {
				data, _ = json.Marshal(tt.payload)
			}

			err := h.processAddMembers(context.Background(), data)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestHandler_ProcessAddMembers_BulkCreateSubscriptionsFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
		ID: "u-bob", Account: "bob", SiteID: "site-a",
	}, nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(fmt.Errorf("write failed"))

	h := NewHandler(store, "site-a",
		func(_ context.Context, _ string, _ []byte) error { return nil },
		func(_ context.Context, _ string, _ []byte) error { return nil },
	)

	req := model.AddMembersRequest{RoomID: "r1", Users: []string{"bob"}}
	data, _ := json.Marshal(req)

	err := h.processAddMembers(context.Background(), data)
	assert.Error(t, err)
}

func TestHandler_ProcessAddMembers_WithOrgs(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
		ID: "u-bob", Account: "bob", SiteID: "site-a",
	}, nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, m *model.RoomMember) error {
			assert.Equal(t, "r1", m.RoomID)
			assert.Equal(t, "org-eng", m.Member.ID)
			assert.Equal(t, model.RoomMemberTypeOrg, m.Member.Type)
			return nil
		})
	store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil)

	h := NewHandler(store, "site-a",
		func(_ context.Context, _ string, _ []byte) error { return nil },
		func(_ context.Context, _ string, _ []byte) error { return nil },
	)

	req := model.AddMembersRequest{
		RoomID: "r1",
		Users:  []string{"bob"},
		Orgs:   []string{"org-eng"},
	}
	data, _ := json.Marshal(req)

	require.NoError(t, h.processAddMembers(context.Background(), data))
}

func TestHandler_ProcessAddMembers_PublishesEvents(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
		ID: "u-bob", Account: "bob", SiteID: "site-a",
	}, nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil)

	var published []publishedMsg
	h := NewHandler(store, "site-a",
		func(_ context.Context, subj string, data []byte) error {
			published = append(published, publishedMsg{subj: subj, data: data})
			return nil
		},
		func(_ context.Context, subj string, data []byte) error {
			published = append(published, publishedMsg{subj: subj, data: data})
			return nil
		},
	)

	req := model.AddMembersRequest{RoomID: "r1", Users: []string{"bob"}}
	data, _ := json.Marshal(req)

	require.NoError(t, h.processAddMembers(context.Background(), data))

	subjectSet := make(map[string]bool)
	msgBySubj := make(map[string][]byte)
	for _, p := range published {
		subjectSet[p.subj] = true
		msgBySubj[p.subj] = p.data
	}

	assert.True(t, subjectSet[subject.SubscriptionUpdate("bob")], "expected subscription update published for bob")
	assert.True(t, subjectSet[subject.RoomMemberEvent("r1")], "expected room member event published")
	assert.True(t, subjectSet[subject.MsgCanonicalCreated("site-a")], "expected system message published to canonical")

	// Verify system message has correct Type
	canonicalData := msgBySubj[subject.MsgCanonicalCreated("site-a")]
	require.NotNil(t, canonicalData)
	var sysMsgEvt model.MessageEvent
	require.NoError(t, json.Unmarshal(canonicalData, &sysMsgEvt))
	assert.Equal(t, "members_added", sysMsgEvt.Message.Type)
	assert.Equal(t, "r1", sysMsgEvt.Message.RoomID)
}

func TestHandler_ProcessAddMembers_CrossSiteOutbox(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	// Room is on site-a, user is on site-b — should publish outbox
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
		ID: "u-bob", Account: "bob", SiteID: "site-b",
	}, nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil)

	var outboxPublished []publishedMsg
	h := NewHandler(store, "site-a",
		func(_ context.Context, _ string, _ []byte) error { return nil },
		func(_ context.Context, subj string, data []byte) error {
			outboxPublished = append(outboxPublished, publishedMsg{subj: subj, data: data})
			return nil
		},
	)

	req := model.AddMembersRequest{RoomID: "r1", Users: []string{"bob"}}
	data, _ := json.Marshal(req)

	require.NoError(t, h.processAddMembers(context.Background(), data))

	require.Len(t, outboxPublished, 1)
	assert.Equal(t, subject.Outbox("site-a", "site-b", "member_added"), outboxPublished[0].subj)
}

func TestHandler_ProcessAddMembers_SameSiteNoOutbox(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	// Room and user both on site-a — no outbox should be published
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
		ID: "u-bob", Account: "bob", SiteID: "site-a",
	}, nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil)

	var outboxPublished []publishedMsg
	h := NewHandler(store, "site-a",
		func(_ context.Context, _ string, _ []byte) error { return nil },
		func(_ context.Context, subj string, data []byte) error {
			outboxPublished = append(outboxPublished, publishedMsg{subj: subj, data: data})
			return nil
		},
	)

	req := model.AddMembersRequest{RoomID: "r1", Users: []string{"bob"}}
	data, _ := json.Marshal(req)

	require.NoError(t, h.processAddMembers(context.Background(), data))

	assert.Empty(t, outboxPublished, "expected no outbox for same-site member")
}

// --- processRemoveMember ---

func TestHandler_ProcessRemoveMember_ByAccount(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
	store.EXPECT().DeleteSubscription(gomock.Any(), "bob", "r1").Return(nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}, nil)
	store.EXPECT().DeleteRoomMember(gomock.Any(), "bob", "r1").Return(nil)
	store.EXPECT().DecrementUserCount(gomock.Any(), "r1").Return(nil)

	var published []publishedMsg
	h := NewHandler(store, "site-a",
		func(_ context.Context, subj string, data []byte) error {
			published = append(published, publishedMsg{subj: subj, data: data})
			return nil
		},
		func(_ context.Context, subj string, data []byte) error {
			published = append(published, publishedMsg{subj: subj, data: data})
			return nil
		},
	)

	req := model.RemoveMemberRequest{Account: "bob", RoomID: "r1"}
	data, _ := json.Marshal(req)

	require.NoError(t, h.processRemoveMember(context.Background(), data))

	subjectSet := make(map[string]bool)
	msgBySubj := make(map[string][]byte)
	for _, p := range published {
		subjectSet[p.subj] = true
		msgBySubj[p.subj] = p.data
	}

	assert.True(t, subjectSet[subject.SubscriptionUpdate("bob")], "expected subscription update published for bob")
	assert.True(t, subjectSet[subject.RoomMemberEvent("r1")], "expected room member event published")
	assert.True(t, subjectSet[subject.MsgCanonicalCreated("site-a")], "expected system message published")

	// Verify system message has correct Type
	canonicalData := msgBySubj[subject.MsgCanonicalCreated("site-a")]
	require.NotNil(t, canonicalData)
	var sysMsgEvt model.MessageEvent
	require.NoError(t, json.Unmarshal(canonicalData, &sysMsgEvt))
	assert.Equal(t, "member_removed", sysMsgEvt.Message.Type)
	assert.Equal(t, "r1", sysMsgEvt.Message.RoomID)
}

func TestHandler_ProcessRemoveMember_ByOrgID(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
	store.EXPECT().GetOrgAccounts(gomock.Any(), "org-eng").Return([]string{"alice", "bob"}, nil)
	store.EXPECT().DeleteSubscription(gomock.Any(), "alice", "r1").Return(nil)
	store.EXPECT().DeleteSubscription(gomock.Any(), "bob", "r1").Return(nil)
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}, nil)
	store.EXPECT().DeleteOrgRoomMember(gomock.Any(), "org-eng", "r1").Return(nil)
	store.EXPECT().DeleteRoomMember(gomock.Any(), "alice", "r1").Return(nil)
	store.EXPECT().DeleteRoomMember(gomock.Any(), "bob", "r1").Return(nil)
	store.EXPECT().DecrementUserCount(gomock.Any(), "r1").Return(nil)

	var published []publishedMsg
	h := NewHandler(store, "site-a",
		func(_ context.Context, subj string, data []byte) error {
			published = append(published, publishedMsg{subj: subj, data: data})
			return nil
		},
		func(_ context.Context, subj string, data []byte) error {
			published = append(published, publishedMsg{subj: subj, data: data})
			return nil
		},
	)

	req := model.RemoveMemberRequest{OrgID: "org-eng", RoomID: "r1"}
	data, _ := json.Marshal(req)

	require.NoError(t, h.processRemoveMember(context.Background(), data))

	subjectSet := make(map[string]bool)
	for _, p := range published {
		subjectSet[p.subj] = true
	}
	assert.True(t, subjectSet[subject.SubscriptionUpdate("alice")], "expected subscription update for alice")
	assert.True(t, subjectSet[subject.SubscriptionUpdate("bob")], "expected subscription update for bob")
	assert.True(t, subjectSet[subject.RoomMemberEvent("r1")], "expected room member event published")
	assert.True(t, subjectSet[subject.MsgCanonicalCreated("site-a")], "expected system message published")
}

func TestHandler_ProcessRemoveMember_StoreErrors(t *testing.T) {
	tests := []struct {
		name    string
		payload model.RemoveMemberRequest
		setup   func(store *MockSubscriptionStore)
	}{
		{
			name:    "delete subscription fails",
			payload: model.RemoveMemberRequest{Account: "bob", RoomID: "r1"},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
				store.EXPECT().DeleteSubscription(gomock.Any(), "bob", "r1").Return(fmt.Errorf("db error"))
			},
		},
		{
			name:    "GetOrgAccounts fails",
			payload: model.RemoveMemberRequest{OrgID: "org-bad", RoomID: "r1"},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
				store.EXPECT().GetOrgAccounts(gomock.Any(), "org-bad").Return(nil, fmt.Errorf("db error"))
			},
		},
		{
			name:    "GetRoom fails",
			payload: model.RemoveMemberRequest{Account: "bob", RoomID: "r1"},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(nil, fmt.Errorf("db error"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockSubscriptionStore(ctrl)
			tt.setup(store)

			h := NewHandler(store, "site-a",
				func(_ context.Context, _ string, _ []byte) error { return nil },
				func(_ context.Context, _ string, _ []byte) error { return nil },
			)

			data, _ := json.Marshal(tt.payload)
			err := h.processRemoveMember(context.Background(), data)
			assert.Error(t, err)
		})
	}
}

func TestHandler_ProcessRemoveMember_MalformedJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	h := NewHandler(store, "site-a",
		func(_ context.Context, _ string, _ []byte) error { return nil },
		func(_ context.Context, _ string, _ []byte) error { return nil },
	)

	err := h.processRemoveMember(context.Background(), []byte("{invalid"))
	assert.Error(t, err)
}

func TestHandler_ProcessRemoveMember_CrossSiteOutbox(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	// Room on site-a, user on site-b — should publish outbox
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
	store.EXPECT().DeleteSubscription(gomock.Any(), "bob", "r1").Return(nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u-bob", Account: "bob", SiteID: "site-b"}, nil)
	store.EXPECT().DeleteRoomMember(gomock.Any(), "bob", "r1").Return(nil)
	store.EXPECT().DecrementUserCount(gomock.Any(), "r1").Return(nil)

	var outboxPublished []publishedMsg
	h := NewHandler(store, "site-a",
		func(_ context.Context, _ string, _ []byte) error { return nil },
		func(_ context.Context, subj string, data []byte) error {
			outboxPublished = append(outboxPublished, publishedMsg{subj: subj, data: data})
			return nil
		},
	)

	req := model.RemoveMemberRequest{Account: "bob", RoomID: "r1"}
	data, _ := json.Marshal(req)

	require.NoError(t, h.processRemoveMember(context.Background(), data))

	require.Len(t, outboxPublished, 1)
	assert.Equal(t, subject.Outbox("site-a", "site-b", "member_removed"), outboxPublished[0].subj)
}

// --- processRoleUpdate ---

func TestHandler_ProcessRoleUpdate(t *testing.T) {
	tests := []struct {
		name         string
		payload      model.UpdateRoleRequest
		setup        func(store *MockSubscriptionStore)
		wantErr      bool
		checkPublish func(t *testing.T, local, outbox []publishedMsg)
	}{
		{
			name: "happy path: updates role, publishes SubscriptionUpdateEvent",
			payload: model.UpdateRoleRequest{
				Account: "bob",
				RoomID:  "r1",
				NewRole: model.RoleOwner,
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().UpdateSubscriptionRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(nil)
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}, nil)
			},
			checkPublish: func(t *testing.T, local, outbox []publishedMsg) {
				t.Helper()
				require.Len(t, local, 1)
				assert.Equal(t, subject.SubscriptionUpdate("bob"), local[0].subj)

				var evt model.SubscriptionUpdateEvent
				require.NoError(t, json.Unmarshal(local[0].data, &evt))
				assert.Equal(t, "role_updated", evt.Action)
				assert.Equal(t, "r1", evt.Subscription.RoomID)
				assert.Equal(t, "bob", evt.Subscription.User.Account)
				require.Len(t, evt.Subscription.Roles, 1)
				assert.Equal(t, model.RoleOwner, evt.Subscription.Roles[0])

				assert.Empty(t, outbox, "expected no outbox for same-site role update")
			},
		},
		{
			name: "cross-site: publishes outbox to user's site",
			payload: model.UpdateRoleRequest{
				Account: "bob",
				RoomID:  "r1",
				NewRole: model.RoleOwner,
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().UpdateSubscriptionRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(nil)
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u-bob", Account: "bob", SiteID: "site-b"}, nil)
			},
			checkPublish: func(t *testing.T, local, outbox []publishedMsg) {
				t.Helper()
				localSubjects := make(map[string]bool)
				for _, p := range local {
					localSubjects[p.subj] = true
				}
				assert.True(t, localSubjects[subject.SubscriptionUpdate("bob")], "expected subscription update")

				require.Len(t, outbox, 1)
				assert.Equal(t, subject.Outbox("site-a", "site-b", "role_updated"), outbox[0].subj)

				var outboxEvt model.OutboxEvent
				require.NoError(t, json.Unmarshal(outbox[0].data, &outboxEvt))
				assert.Equal(t, "role_updated", outboxEvt.Type)
				assert.Equal(t, "site-a", outboxEvt.SiteID)
				assert.Equal(t, "site-b", outboxEvt.DestSiteID)
			},
		},
		{
			name: "UpdateSubscriptionRole fails",
			payload: model.UpdateRoleRequest{
				Account: "bob",
				RoomID:  "r1",
				NewRole: model.RoleOwner,
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().UpdateSubscriptionRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(fmt.Errorf("db error"))
			},
			wantErr: true,
		},
		{
			name:    "malformed json",
			payload: model.UpdateRoleRequest{},
			setup:   func(store *MockSubscriptionStore) {},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockSubscriptionStore(ctrl)
			tt.setup(store)

			var localPublished []publishedMsg
			var outboxPublished []publishedMsg
			h := NewHandler(store, "site-a",
				func(_ context.Context, subj string, data []byte) error {
					localPublished = append(localPublished, publishedMsg{subj: subj, data: data})
					return nil
				},
				func(_ context.Context, subj string, data []byte) error {
					outboxPublished = append(outboxPublished, publishedMsg{subj: subj, data: data})
					return nil
				},
			)

			var data []byte
			if tt.name == "malformed json" {
				data = []byte("{invalid")
			} else {
				data, _ = json.Marshal(tt.payload)
			}

			err := h.processRoleUpdate(context.Background(), data)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tt.checkPublish != nil {
				tt.checkPublish(t, localPublished, outboxPublished)
			}
		})
	}
}

// --- HandleJetStreamMsg routing ---

func TestHandler_HandleJetStreamMsg_Routing(t *testing.T) {
	tests := []struct {
		name    string
		subj    string
		payload []byte
		// nak expected when op unknown or processing fails
		wantNak bool
		setup   func(store *MockSubscriptionStore)
	}{
		{
			name:    "unknown operation naks message",
			subj:    "chat.user.bob.request.room.r1.site-a.member.unknown",
			payload: []byte(`{}`),
			wantNak: true,
			setup:   func(store *MockSubscriptionStore) {},
		},
		{
			name:    "member.add routes to processAddMembers; bad json naks",
			subj:    "chat.user.bob.request.room.r1.site-a.member.add",
			payload: []byte(`{invalid`),
			wantNak: true,
			setup:   func(store *MockSubscriptionStore) {},
		},
		{
			name:    "member.remove routes to processRemoveMember; bad json naks",
			subj:    "chat.user.bob.request.room.r1.site-a.member.remove",
			payload: []byte(`{invalid`),
			wantNak: true,
			setup:   func(store *MockSubscriptionStore) {},
		},
		{
			name:    "member.role-update routes to processRoleUpdate; bad json naks",
			subj:    "chat.user.bob.request.room.r1.site-a.member.role-update",
			payload: []byte(`{invalid`),
			wantNak: true,
			setup:   func(store *MockSubscriptionStore) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockSubscriptionStore(ctrl)
			tt.setup(store)

			h := NewHandler(store, "site-a",
				func(_ context.Context, _ string, _ []byte) error { return nil },
				func(_ context.Context, _ string, _ []byte) error { return nil },
			)

			msg := &fakeJetStreamMsg{subj: tt.subj, data: tt.payload}
			h.HandleJetStreamMsg(context.Background(), msg)

			if tt.wantNak {
				assert.True(t, msg.nacked, "expected Nak() to be called")
				assert.False(t, msg.acked, "expected Ack() not to be called")
			} else {
				assert.True(t, msg.acked, "expected Ack() to be called")
				assert.False(t, msg.nacked, "expected Nak() not to be called")
			}
		})
	}
}

// fakeJetStreamMsg implements jetstream.Msg for routing tests.
type fakeJetStreamMsg struct {
	subj   string
	data   []byte
	acked  bool
	nacked bool
}

func (m *fakeJetStreamMsg) Subject() string { return m.subj }
func (m *fakeJetStreamMsg) Data() []byte    { return m.data }
func (m *fakeJetStreamMsg) Ack() error      { m.acked = true; return nil }
func (m *fakeJetStreamMsg) Nak() error      { m.nacked = true; return nil }
func (m *fakeJetStreamMsg) NakWithDelay(delay time.Duration) error {
	m.nacked = true
	return nil
}
func (m *fakeJetStreamMsg) Term() error                               { return nil }
func (m *fakeJetStreamMsg) TermWithReason(reason string) error        { return nil }
func (m *fakeJetStreamMsg) InProgress() error                         { return nil }
func (m *fakeJetStreamMsg) Headers() nats.Header                      { return nil }
func (m *fakeJetStreamMsg) Reply() string                             { return "" }
func (m *fakeJetStreamMsg) DoubleAck(ctx context.Context) error       { return nil }
func (m *fakeJetStreamMsg) Metadata() (*jetstream.MsgMetadata, error) { return nil, nil }

// Ensure the outbox subject pattern is correct: outbox.{room.SiteID}.to.{user.SiteID}.{eventType}
func TestHandler_OutboxSubjectPattern(t *testing.T) {
	outboxSubj := subject.Outbox("site-a", "site-b", "member_added")
	assert.True(t, strings.HasPrefix(outboxSubj, "outbox.site-a.to.site-b."), "outbox subject must be outbox.{roomSite}.to.{userSite}.{type}")
	assert.Equal(t, "outbox.site-a.to.site-b.member_added", outboxSubj)
}
