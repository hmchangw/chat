package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

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

func TestHandler_ProcessInvite(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	var createdSub *model.Subscription
	store.EXPECT().
		CreateSubscription(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, s *model.Subscription) error {
			createdSub = s
			return nil
		})
	store.EXPECT().
		IncrementUserCount(gomock.Any(), "r1").
		Return(nil)
	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", UserCount: 2, SiteID: "site-a"}, nil)
	store.EXPECT().
		ListByRoom(gomock.Any(), "r1").
		Return([]model.Subscription{
			{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}},
			{User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}},
		}, nil)

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

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", InviteeAccount: "bob", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	err := h.processInvite(context.Background(), data)
	require.NoError(t, err)

	// Verify created subscription has the correct user account
	require.NotNil(t, createdSub)
	assert.Equal(t, "bob", createdSub.User.Account)

	// Verify notifications published (subscription update + room metadata for existing members)
	assert.GreaterOrEqual(t, len(published), 3, "expected at least 3 publishes")

	subjectSet := make(map[string]bool)
	for _, p := range published {
		subjectSet[p.subj] = true
	}
	assert.True(t, subjectSet["chat.user.bob.event.subscription.update"], "expected subscription update published")
	assert.True(t, subjectSet["chat.user.alice.event.room.metadata.update"], "expected room metadata published to alice")
	assert.True(t, subjectSet["chat.user.bob.event.room.metadata.update"], "expected room metadata published to bob")

	// Verify all published events have Timestamp set to a non-zero value
	for _, p := range published {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(p.data, &raw); err != nil {
			t.Fatalf("unmarshal published data: %v", err)
		}
		tsRaw, ok := raw["timestamp"]
		if !ok {
			t.Errorf("published event to %s missing timestamp field", p.subj)
			continue
		}
		var ts int64
		if err := json.Unmarshal(tsRaw, &ts); err != nil {
			t.Errorf("published event to %s has non-numeric timestamp: %v", p.subj, err)
			continue
		}
		if ts == 0 {
			t.Errorf("published event to %s has zero timestamp, expected non-zero", p.subj)
		}
	}
}

func TestHandler_ProcessAddMembers(t *testing.T) {
	tests := []struct {
		name    string
		payload model.AddMembersRequest
		setup   func(store *MockSubscriptionStore)
		wantErr bool
	}{
		{
			name: "happy path: users resolved, subs created",
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Users:   []string{"bob", "carol"},
				History: model.HistoryConfig{Mode: model.HistoryModeNone},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
					ID: "u-bob", Account: "bob", SiteID: "site-a",
				}, nil)
				store.EXPECT().GetUser(gomock.Any(), "carol").Return(&model.User{
					ID: "u-carol", Account: "carol", SiteID: "site-a",
				}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						assert.Len(t, subs, 2)
						assert.Equal(t, "bob", subs[0].User.Account)
						assert.Equal(t, "carol", subs[1].User.Account)
						assert.Equal(t, "u-bob", subs[0].User.ID)
						// history=none: HistorySharedSince must be set
						assert.NotNil(t, subs[0].HistorySharedSince, "expected HistorySharedSince to be set for history=none")
						return nil
					})
				// Async goroutine calls — use AnyTimes since they may not complete before test ends
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil).AnyTimes()
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil).AnyTimes()
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
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
					ID: "u-bob", Account: "bob", SiteID: "site-a",
				}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						assert.Len(t, subs, 1)
						assert.Nil(t, subs[0].HistorySharedSince, "expected HistorySharedSince to be nil for history=all")
						return nil
					})
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil).AnyTimes()
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil).AnyTimes()
			},
		},
		{
			name: "user not found (ErrNoDocuments): skipped gracefully",
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bad-user", "bob"},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetUser(gomock.Any(), "bad-user").Return(nil, mongo.ErrNoDocuments)
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
					ID: "u-bob", Account: "bob", SiteID: "site-a",
				}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						assert.Len(t, subs, 1, "expected only bob, bad-user should be skipped")
						assert.Equal(t, "bob", subs[0].User.Account)
						return nil
					})
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil).AnyTimes()
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil).AnyTimes()
			},
		},
		{
			name: "all users not found: empty subscription list",
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bad1", "bad2"},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetUser(gomock.Any(), "bad1").Return(nil, mongo.ErrNoDocuments)
				store.EXPECT().GetUser(gomock.Any(), "bad2").Return(nil, mongo.ErrNoDocuments)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						assert.Empty(t, subs)
						return nil
					})
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil).AnyTimes()
			},
		},
		{
			name: "GetUser returns unexpected error",
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bob"},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(nil, fmt.Errorf("connection refused"))
			},
			wantErr: true,
		},
		{
			name: "BulkCreateSubscriptions fails",
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bob"},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
					ID: "u-bob", Account: "bob", SiteID: "site-a",
				}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(fmt.Errorf("write failed"))
			},
			wantErr: true,
		},
		{
			name:    "malformed json",
			payload: model.AddMembersRequest{},
			setup:   func(store *MockSubscriptionStore) {},
			wantErr: true,
		},
		{
			name: "org room members created",
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bob"},
				Orgs:   []string{"org-eng"},
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{
					ID: "u-bob", Account: "bob", SiteID: "site-a",
				}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil).AnyTimes()
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil).AnyTimes()
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

			// Wait for async goroutines to complete before gomock cleanup checks
			h.asyncWg.Wait()
		})
	}
}

func TestHandler_ProcessRemoveMember(t *testing.T) {
	tests := []struct {
		name    string
		payload model.RemoveMemberRequest
		setup   func(store *MockSubscriptionStore)
		wantErr bool
	}{
		{
			name: "individual removal: delete subscription called",
			payload: model.RemoveMemberRequest{
				Username: "bob",
				RoomID:   "r1",
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().DeleteSubscription(gomock.Any(), "bob", "r1").Return(nil)
				// Async goroutine calls
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil).AnyTimes()
				store.EXPECT().DeleteRoomMember(gomock.Any(), "bob", "r1").Return(nil).AnyTimes()
				store.EXPECT().DecrementUserCount(gomock.Any(), "r1").Return(nil).AnyTimes()
			},
		},
		{
			name: "org removal: GetOrgAccounts called, multiple deletions",
			payload: model.RemoveMemberRequest{
				OrgID:  "org-eng",
				RoomID: "r1",
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetOrgAccounts(gomock.Any(), "org-eng").Return([]string{"alice", "bob"}, nil)
				store.EXPECT().DeleteSubscription(gomock.Any(), "alice", "r1").Return(nil)
				store.EXPECT().DeleteSubscription(gomock.Any(), "bob", "r1").Return(nil)
				// Async goroutine calls
				store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil).AnyTimes()
				store.EXPECT().DeleteOrgRoomMember(gomock.Any(), "org-eng", "r1").Return(nil).AnyTimes()
				store.EXPECT().DeleteRoomMember(gomock.Any(), gomock.Any(), "r1").Return(nil).AnyTimes()
				store.EXPECT().DecrementUserCount(gomock.Any(), "r1").Return(nil).AnyTimes()
			},
		},
		{
			name: "delete subscription fails",
			payload: model.RemoveMemberRequest{
				Username: "bob",
				RoomID:   "r1",
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().DeleteSubscription(gomock.Any(), "bob", "r1").Return(fmt.Errorf("db error"))
			},
			wantErr: true,
		},
		{
			name: "GetOrgAccounts fails",
			payload: model.RemoveMemberRequest{
				OrgID:  "org-bad",
				RoomID: "r1",
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().GetOrgAccounts(gomock.Any(), "org-bad").Return(nil, fmt.Errorf("db error"))
			},
			wantErr: true,
		},
		{
			name:    "malformed json",
			payload: model.RemoveMemberRequest{},
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

			err := h.processRemoveMember(context.Background(), data)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Wait for async goroutines to complete before gomock cleanup checks
			h.asyncWg.Wait()
		})
	}
}

func TestHandler_ProcessRoleUpdate(t *testing.T) {
	tests := []struct {
		name         string
		payload      model.UpdateRoleRequest
		setup        func(store *MockSubscriptionStore)
		wantErr      bool
		checkPublish func(t *testing.T, published []publishedMsg)
	}{
		{
			name: "happy path: role updated, event published",
			payload: model.UpdateRoleRequest{
				Username: "bob",
				RoomID:   "r1",
				NewRole:  model.RoleOwner,
			},
			setup: func(store *MockSubscriptionStore) {
				store.EXPECT().UpdateSubscriptionRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(nil)
			},
			checkPublish: func(t *testing.T, published []publishedMsg) {
				t.Helper()
				require.Len(t, published, 1)
				assert.Equal(t, subject.SubscriptionUpdate("bob"), published[0].subj)
				var evt model.SubscriptionUpdateEvent
				require.NoError(t, json.Unmarshal(published[0].data, &evt))
				assert.Equal(t, "role_updated", evt.Action)
				assert.Equal(t, "r1", evt.Subscription.RoomID)
				assert.Equal(t, model.RoleOwner, evt.Subscription.Roles[0])
			},
		},
		{
			name: "UpdateSubscriptionRole fails",
			payload: model.UpdateRoleRequest{
				Username: "bob",
				RoomID:   "r1",
				NewRole:  model.RoleOwner,
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
				tt.checkPublish(t, published)
			}
		})
	}
}
