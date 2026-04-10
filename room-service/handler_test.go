package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

func TestHandler_CreateRoom(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	var capturedSub *model.Subscription
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().CreateSubscription(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, sub *model.Subscription) error {
		capturedSub = sub
		return nil
	})

	h := NewHandler(store, "site-a", 1000, nil)

	req := model.CreateRoomRequest{Name: "general", Type: model.RoomTypeGroup, CreatedBy: "u1", CreatedByAccount: "alice", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	resp, err := h.handleCreateRoom(context.Background(), data)
	require.NoError(t, err)

	var room model.Room
	require.NoError(t, json.Unmarshal(resp, &room))
	assert.Equal(t, "general", room.Name)
	assert.Equal(t, "u1", room.CreatedBy)
	require.NotNil(t, capturedSub)
	assert.Equal(t, "alice", capturedSub.User.Account)
}


func TestHandler_AddMembers(t *testing.T) {
	makeSubj := func(inviter, roomID string) string {
		return subject.MemberAdd(inviter, roomID, "site-a")
	}

	tests := []struct {
		name         string
		subj         string
		payload      any
		setup        func(store *MockRoomStore)
		wantErr      bool
		checkPublish func(t *testing.T, published []byte)
	}{
		{
			name: "individuals only",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Users:   []string{"bob", "carol"},
				History: model.HistoryConfig{Mode: model.HistoryModeNone},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
			},
			checkPublish: func(t *testing.T, published []byte) {
				t.Helper()
				var req model.AddMembersRequest
				require.NoError(t, json.Unmarshal(published, &req))
				assert.Equal(t, "r1", req.RoomID)
				assert.ElementsMatch(t, []string{"bob", "carol"}, req.Users)
			},
		},
		{
			name: "bot users are filtered out",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bot.bot", "p_system", "carol"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
			},
			checkPublish: func(t *testing.T, published []byte) {
				t.Helper()
				var req model.AddMembersRequest
				require.NoError(t, json.Unmarshal(published, &req))
				assert.Equal(t, []string{"carol"}, req.Users)
			},
		},
		{
			name: "org in request",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Orgs:   []string{"org-eng"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetOrgAccounts(gomock.Any(), "org-eng").Return([]string{"eng-alice", "eng-bob"}, nil)
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
			},
			checkPublish: func(t *testing.T, published []byte) {
				t.Helper()
				var req model.AddMembersRequest
				require.NoError(t, json.Unmarshal(published, &req))
				assert.ElementsMatch(t, []string{"eng-alice", "eng-bob"}, req.Users)
				assert.Equal(t, []string{"org-eng"}, req.Orgs)
			},
		},
		{
			name: "mixed users and orgs",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"dave"},
				Orgs:   []string{"org-eng"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetOrgAccounts(gomock.Any(), "org-eng").Return([]string{"eng-alice", "eng-bob"}, nil)
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
			},
			checkPublish: func(t *testing.T, published []byte) {
				t.Helper()
				var req model.AddMembersRequest
				require.NoError(t, json.Unmarshal(published, &req))
				assert.ElementsMatch(t, []string{"dave", "eng-alice", "eng-bob"}, req.Users)
				assert.Equal(t, []string{"org-eng"}, req.Orgs)
			},
		},
		{
			name: "channel source: room_members with org and individual, merged with subscriptions",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:   "r1",
				Channels: []string{"r-source"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r-source").Return([]model.RoomMember{
					{ID: "m1", RoomID: "r-source", Member: model.RoomMemberEntry{ID: "org-eng", Type: model.RoomMemberTypeOrg}},
					{ID: "m2", RoomID: "r-source", Member: model.RoomMemberEntry{Type: model.RoomMemberTypeIndividual, Account: "dave"}},
				}, nil)
				store.EXPECT().ListSubscriptionsByRoom(gomock.Any(), "r-source").Return([]model.Subscription{
					{User: model.SubscriptionUser{Account: "dave"}},
				}, nil)
				store.EXPECT().GetOrgAccounts(gomock.Any(), "org-eng").Return([]string{"eng-alice", "eng-bob"}, nil)
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
			},
			checkPublish: func(t *testing.T, published []byte) {
				t.Helper()
				var req model.AddMembersRequest
				require.NoError(t, json.Unmarshal(published, &req))
				assert.Contains(t, req.Users, "dave")
				assert.Contains(t, req.Users, "eng-alice")
				assert.Contains(t, req.Users, "eng-bob")
				assert.Contains(t, req.Orgs, "org-eng")
			},
		},
		{
			name: "channel source: room_members empty, fallback to subscriptions",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:   "r1",
				Channels: []string{"r-source"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r-source").Return([]model.RoomMember{}, nil)
				store.EXPECT().ListSubscriptionsByRoom(gomock.Any(), "r-source").Return([]model.Subscription{
					{User: model.SubscriptionUser{Account: "bob"}},
					{User: model.SubscriptionUser{Account: "carol"}},
				}, nil)
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
			},
			checkPublish: func(t *testing.T, published []byte) {
				t.Helper()
				var req model.AddMembersRequest
				require.NoError(t, json.Unmarshal(published, &req))
				assert.ElementsMatch(t, []string{"bob", "carol"}, req.Users)
			},
		},
		{
			name: "channel source: bots filtered from subscription fallback",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:   "r1",
				Channels: []string{"r-source"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r-source").Return([]model.RoomMember{}, nil)
				store.EXPECT().ListSubscriptionsByRoom(gomock.Any(), "r-source").Return([]model.Subscription{
					{User: model.SubscriptionUser{Account: "bob"}},
					{User: model.SubscriptionUser{Account: "notify.bot"}},
					{User: model.SubscriptionUser{Account: "p_webhook"}},
				}, nil)
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
			},
			checkPublish: func(t *testing.T, published []byte) {
				t.Helper()
				var req model.AddMembersRequest
				require.NoError(t, json.Unmarshal(published, &req))
				assert.Equal(t, []string{"bob"}, req.Users)
			},
		},
		{
			name: "room at capacity",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bob"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(1000, nil)
			},
			wantErr: true,
		},
		{
			name:    "malformed json",
			subj:    makeSubj("alice", "r1"),
			payload: nil,
			setup:   func(store *MockRoomStore) {},
			wantErr: true,
		},
		{
			name: "org resolution fails",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Orgs:   []string{"org-bad"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetOrgAccounts(gomock.Any(), "org-bad").Return(nil, fmt.Errorf("orgs collection unavailable"))
			},
			wantErr: true,
		},
		{
			name: "publish to stream fails",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bob"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
			},
			wantErr: true,
		},
		{
			name:    "invalid subject",
			subj:    "chat.invalid",
			payload: model.AddMembersRequest{RoomID: "r1", Users: []string{"bob"}},
			setup:   func(store *MockRoomStore) {},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			tt.setup(store)

			var publishedData []byte
			publishFn := func(_ context.Context, _ string, data []byte) error {
				publishedData = data
				return nil
			}
			// For the "publish to stream fails" test case
			if tt.name == "publish to stream fails" {
				publishFn = func(_ context.Context, _ string, _ []byte) error { return fmt.Errorf("stream unavailable") }
			}

			h := NewHandler(store, "site-a", 1000, publishFn)

			var data []byte
			if tt.payload != nil {
				data, _ = json.Marshal(tt.payload)
			} else {
				data = []byte("{invalid")
			}

			resp, err := h.handleAddMembers(context.Background(), tt.subj, data)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			var result map[string]string
			require.NoError(t, json.Unmarshal(resp, &result))
			assert.Equal(t, "accepted", result["status"])

			if tt.checkPublish != nil {
				assert.NotNil(t, publishedData, "expected data to be published to stream")
				tt.checkPublish(t, publishedData)
			}
		})
	}
}

func TestHandler_RemoveMember(t *testing.T) {
	tests := []struct {
		name         string
		subj         string
		payload      any
		setup        func(store *MockRoomStore)
		wantErr      bool
		checkPublish func(t *testing.T, subj string, data []byte)
	}{
		{
			name: "self-leave: member removes themselves",
			subj: subject.MemberRemove("alice", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				Account: "alice",
				RoomID:   "r1",
			},
			setup: func(store *MockRoomStore) {
				// Self-leave: GetSubscription to check if owner for last-owner guard
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Roles: []model.Role{model.RoleMember}}, nil)
			},
		},
		{
			name: "self-leave: owner with co-owners",
			subj: subject.MemberRemove("alice", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				Account: "alice",
				RoomID:   "r1",
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Roles: []model.Role{model.RoleOwner}}, nil)
				store.EXPECT().CountOwners(gomock.Any(), "r1").Return(2, nil)
			},
		},
		{
			name: "self-leave: last owner cannot leave",
			subj: subject.MemberRemove("alice", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				Account: "alice",
				RoomID:   "r1",
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Roles: []model.Role{model.RoleOwner}}, nil)
				store.EXPECT().CountOwners(gomock.Any(), "r1").Return(1, nil)
			},
			wantErr: true,
		},
		{
			name: "owner removes individual member",
			subj: subject.MemberRemove("owner1", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				Account: "bob",
				RoomID:   "r1",
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{Roles: []model.Role{model.RoleOwner}}, nil)
			},
		},
		{
			name: "non-owner cannot remove another member",
			subj: subject.MemberRemove("bob", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				Account: "carol",
				RoomID:   "r1",
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "bob", "r1").
					Return(&model.Subscription{Roles: []model.Role{model.RoleMember}}, nil)
			},
			wantErr: true,
		},
		{
			name: "owner removes org",
			subj: subject.MemberRemove("owner1", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				OrgID:  "org-eng",
				RoomID: "r1",
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{Roles: []model.Role{model.RoleOwner}}, nil)
			},
		},
		{
			name: "non-owner cannot remove org",
			subj: subject.MemberRemove("bob", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				OrgID:  "org-eng",
				RoomID: "r1",
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "bob", "r1").
					Return(&model.Subscription{Roles: []model.Role{model.RoleMember}}, nil)
			},
			wantErr: true,
		},
		{
			name: "ambiguous request: both orgId and username",
			subj: subject.MemberRemove("owner1", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				OrgID:    "org-eng",
				Account: "bob",
				RoomID:   "r1",
			},
			setup:   func(store *MockRoomStore) {},
			wantErr: true,
		},
		{
			name: "invalid request: neither orgId nor username",
			subj: subject.MemberRemove("owner1", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				RoomID: "r1",
			},
			setup:   func(store *MockRoomStore) {},
			wantErr: true,
		},
		{
			name:    "invalid subject",
			subj:    "chat.invalid",
			payload: model.RemoveMemberRequest{RoomID: "r1", Account: "alice"},
			setup:   func(store *MockRoomStore) {},
			wantErr: true,
		},
		{
			name:    "malformed json",
			subj:    subject.MemberRemove("alice", "r1", "site-a"),
			payload: nil,
			setup:   func(store *MockRoomStore) {},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			tt.setup(store)

			var publishedSubj string
			var publishedData []byte
			h := NewHandler(store, "site-a", 1000, func(_ context.Context, subj string, data []byte) error {
				publishedSubj = subj
				publishedData = data
				return nil
			})

			var data []byte
			if tt.payload != nil {
				data, _ = json.Marshal(tt.payload)
			} else {
				data = []byte("{invalid")
			}

			resp, err := h.handleRemoveMember(context.Background(), tt.subj, data)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			var result map[string]string
			require.NoError(t, json.Unmarshal(resp, &result))
			assert.Equal(t, "accepted", result["status"])

			// Verify data was published to stream
			assert.NotEmpty(t, publishedSubj)
			assert.NotNil(t, publishedData)

			if tt.checkPublish != nil {
				tt.checkPublish(t, publishedSubj, publishedData)
			}
		})
	}
}

func TestHandler_UpdateRole(t *testing.T) {
	makeSubj := func(username, roomID string) string {
		return subject.MemberRoleUpdate(username, roomID, "site-a")
	}

	tests := []struct {
		name         string
		subj         string
		payload      any
		setup        func(store *MockRoomStore)
		wantErr      bool
		checkPublish func(t *testing.T, data []byte)
	}{
		{
			name:    "owner promotes member to owner",
			subj:    makeSubj("owner1", "r1"),
			payload: model.UpdateRoleRequest{Account: "bob", RoomID: "r1", NewRole: model.RoleOwner},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Account: "owner1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
			},
		},
		{
			name:    "owner demotes another owner to member",
			subj:    makeSubj("owner1", "r1"),
			payload: model.UpdateRoleRequest{Account: "owner2", RoomID: "r1", NewRole: model.RoleMember},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Account: "owner1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
			},
		},
		{
			name:    "non-owner cannot change roles",
			subj:    makeSubj("bob", "r1"),
			payload: model.UpdateRoleRequest{Account: "alice", RoomID: "r1", NewRole: model.RoleOwner},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "bob", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}, nil)
			},
			wantErr: true,
		},
		{
			name:    "last owner cannot demote themselves",
			subj:    makeSubj("owner1", "r1"),
			payload: model.UpdateRoleRequest{Account: "owner1", RoomID: "r1", NewRole: model.RoleMember},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Account: "owner1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
				store.EXPECT().
					CountOwners(gomock.Any(), "r1").
					Return(1, nil)
			},
			wantErr: true,
		},
		{
			name:    "owner with co-owners can demote themselves",
			subj:    makeSubj("owner1", "r1"),
			payload: model.UpdateRoleRequest{Account: "owner1", RoomID: "r1", NewRole: model.RoleMember},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Account: "owner1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
				store.EXPECT().
					CountOwners(gomock.Any(), "r1").
					Return(2, nil)
			},
		},
		{
			name:    "invalid role value",
			subj:    makeSubj("owner1", "r1"),
			payload: model.UpdateRoleRequest{Account: "bob", RoomID: "r1", NewRole: "admin"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Account: "owner1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
			},
			wantErr: true,
		},
		{
			name:    "invalid subject",
			subj:    "chat.invalid",
			payload: model.UpdateRoleRequest{Account: "bob", RoomID: "r1", NewRole: model.RoleOwner},
			setup:   func(store *MockRoomStore) {},
			wantErr: true,
		},
		{
			name:    "malformed json",
			subj:    makeSubj("owner1", "r1"),
			payload: nil,
			setup:   func(store *MockRoomStore) {},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			tt.setup(store)

			var publishedData []byte
			h := NewHandler(store, "site-a", 1000, func(_ context.Context, _ string, data []byte) error {
				publishedData = data
				return nil
			})

			var data []byte
			if tt.payload != nil {
				data, _ = json.Marshal(tt.payload)
			}

			resp, err := h.handleUpdateRole(context.Background(), tt.subj, data)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			var result map[string]string
			require.NoError(t, json.Unmarshal(resp, &result))
			assert.Equal(t, "accepted", result["status"])

			// Verify data was published to stream
			assert.NotNil(t, publishedData)

			if tt.checkPublish != nil {
				tt.checkPublish(t, publishedData)
			}
		})
	}
}

func TestHandler_expandChannels(t *testing.T) {
	tests := []struct {
		name          string
		channelIDs    []string
		setup         func(store *MockRoomStore)
		wantOrgIDs    []string
		wantUsernames []string
		wantErr       bool
	}{
		{
			name:       "room_members non-empty: merged with subscriptions",
			channelIDs: []string{"r-src"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r-src").Return([]model.RoomMember{
					{ID: "m1", RoomID: "r-src", Member: model.RoomMemberEntry{ID: "org-eng", Type: model.RoomMemberTypeOrg}},
					{ID: "m2", RoomID: "r-src", Member: model.RoomMemberEntry{Type: model.RoomMemberTypeIndividual, Account: "alice"}},
				}, nil)
				store.EXPECT().ListSubscriptionsByRoom(gomock.Any(), "r-src").Return([]model.Subscription{
					{User: model.SubscriptionUser{Account: "alice"}},
					{User: model.SubscriptionUser{Account: "bob"}},
				}, nil)
			},
			wantOrgIDs:    []string{"org-eng"},
			wantUsernames: []string{"alice", "alice", "bob"},
		},
		{
			name:       "room_members empty: fallback to subscriptions",
			channelIDs: []string{"r-src"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r-src").Return([]model.RoomMember{}, nil)
				store.EXPECT().ListSubscriptionsByRoom(gomock.Any(), "r-src").Return([]model.Subscription{
					{User: model.SubscriptionUser{Account: "bob"}},
					{User: model.SubscriptionUser{Account: "carol"}},
				}, nil)
			},
			wantOrgIDs:    nil,
			wantUsernames: []string{"bob", "carol"},
		},
		{
			name:       "GetRoomMembers error",
			channelIDs: []string{"r-src"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r-src").Return(nil, fmt.Errorf("db error"))
			},
			wantErr: true,
		},
		{
			name:       "ListSubscriptionsByRoom error",
			channelIDs: []string{"r-src"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r-src").Return([]model.RoomMember{}, nil)
				store.EXPECT().ListSubscriptionsByRoom(gomock.Any(), "r-src").Return(nil, fmt.Errorf("db error"))
			},
			wantErr: true,
		},
		{
			name:          "empty channel list",
			channelIDs:    []string{},
			setup:         func(store *MockRoomStore) {},
			wantOrgIDs:    nil,
			wantUsernames: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			tt.setup(store)

			h := NewHandler(store, "site-a", 1000, nil)
			orgIDs, usernames, err := h.expandChannels(context.Background(), tt.channelIDs)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOrgIDs, orgIDs)
			assert.Equal(t, tt.wantUsernames, usernames)
		})
	}
}

func TestHandler_resolveOrgs(t *testing.T) {
	tests := []struct {
		name          string
		orgIDs        []string
		setup         func(store *MockRoomStore)
		wantUsernames []string
		wantErr       bool
	}{
		{
			name:   "single org resolves to accounts",
			orgIDs: []string{"org-eng"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetOrgAccounts(gomock.Any(), "org-eng").Return([]string{"eng-alice", "eng-bob"}, nil)
			},
			wantUsernames: []string{"eng-alice", "eng-bob"},
		},
		{
			name:   "multiple orgs merged",
			orgIDs: []string{"org-eng", "org-sales"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetOrgAccounts(gomock.Any(), "org-eng").Return([]string{"eng-alice"}, nil)
				store.EXPECT().GetOrgAccounts(gomock.Any(), "org-sales").Return([]string{"sales-bob"}, nil)
			},
			wantUsernames: []string{"eng-alice", "sales-bob"},
		},
		{
			name:   "GetOrgAccounts error",
			orgIDs: []string{"org-bad"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetOrgAccounts(gomock.Any(), "org-bad").Return(nil, fmt.Errorf("not found"))
			},
			wantErr: true,
		},
		{
			name:          "empty org list",
			orgIDs:        []string{},
			setup:         func(store *MockRoomStore) {},
			wantUsernames: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			tt.setup(store)

			h := NewHandler(store, "site-a", 1000, nil)
			usernames, err := h.resolveOrgs(context.Background(), tt.orgIDs)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantUsernames, usernames)
		})
	}
}
