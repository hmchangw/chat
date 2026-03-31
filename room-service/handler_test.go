package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

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

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000}

	req := model.CreateRoomRequest{Name: "general", Type: model.RoomTypeGroup, CreatedBy: "u1", CreatedByUsername: "alice", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	resp, err := h.handleCreateRoom(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var room model.Room
	json.Unmarshal(resp, &room)
	if room.Name != "general" || room.CreatedBy != "u1" {
		t.Errorf("got %+v", room)
	}
	if capturedSub == nil || capturedSub.User.Username != "alice" {
		t.Errorf("expected owner subscription with Username=alice, got %+v", capturedSub)
	}
}

func TestHandler_InviteOwner_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Username: "alice"}, RoomID: "r1", Role: model.RoleOwner}, nil)
	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", UserCount: 1}, nil)

	var jsPublished []byte
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(data []byte) error { jsPublished = data; return nil },
	}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", InviteeUsername: "bob", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)
	subj := subject.MemberInvite("alice", "r1", "site-a")

	_, err := h.handleInvite(context.Background(), subj, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jsPublished == nil {
		t.Error("expected event published to JetStream")
	}
}

func TestHandler_InviteMember_Rejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u2", Username: "bob"}, RoomID: "r1", Role: model.RoleMember}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(data []byte) error { return nil },
	}

	req := model.InviteMemberRequest{InviterID: "u2", InviteeID: "u3", InviteeUsername: "charlie", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)
	subj := subject.MemberInvite("bob", "r1", "site-a")

	_, err := h.handleInvite(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for non-owner invite")
	}
}

func TestHandler_InviteExceedsMaxSize(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Username: "alice"}, RoomID: "r1", Role: model.RoleOwner}, nil)
	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", UserCount: 1000}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(data []byte) error { return nil },
	}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", InviteeUsername: "bob", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)
	subj := subject.MemberInvite("alice", "r1", "site-a")

	_, err := h.handleInvite(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for room at max size")
	}
}

func TestHandler_AddMembers(t *testing.T) {
	makeSubj := func(inviter, roomID string) string {
		return subject.MemberAdd(inviter, roomID, "site-a")
	}

	tests := []struct {
		name    string
		subj    string
		payload any
		setup   func(store *MockRoomStore)
		wantErr bool
	}{
		{
			name: "individuals only, no existing room members",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Members: []model.MemberEntry{{Type: model.MemberEntryTypeUser, Username: "bob"}, {Type: model.MemberEntryTypeUser, Username: "carol"}},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Role: model.RoleOwner}, nil)
				store.EXPECT().GetRoom(gomock.Any(), "r1").
					Return(&model.Room{ID: "r1", UserCount: 5}, nil)
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").
					Return([]model.RoomMember{}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if len(subs) != 2 {
							t.Errorf("expected 2 subscriptions, got %d", len(subs))
						}
						if subs[0].User.Username != "bob" || subs[1].User.Username != "carol" {
							t.Errorf("unexpected usernames: %v, %v", subs[0].User.Username, subs[1].User.Username)
						}
						return nil
					})
				// CreateRoomMember must NOT be called — no existing members, no orgs
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
			},
		},
		{
			name: "individuals only, existing room members present",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Members: []model.MemberEntry{{Type: model.MemberEntryTypeUser, Username: "dave"}},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Role: model.RoleOwner}, nil)
				store.EXPECT().GetRoom(gomock.Any(), "r1").
					Return(&model.Room{ID: "r1", UserCount: 5}, nil)
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").
					Return([]model.RoomMember{{ID: "m0", RoomID: "r1", Type: model.RoomMemberTypeOrg, OrgID: "org-eng"}}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, m *model.RoomMember) error {
						if m.Type != model.RoomMemberTypeIndividual || m.Username != "dave" {
							t.Errorf("expected individual member dave, got %+v", m)
						}
						return nil
					})
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
			},
		},
		{
			name: "org in request",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Members: []model.MemberEntry{{Type: model.MemberEntryTypeOrg, OrgID: "org-eng"}},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Role: model.RoleOwner}, nil)
				store.EXPECT().GetRoom(gomock.Any(), "r1").
					Return(&model.Room{ID: "r1", UserCount: 5}, nil)
				store.EXPECT().GetOrgUsers(gomock.Any(), "org-eng").
					Return([]string{"bob", "carol"}, nil)
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").
					Return([]model.RoomMember{}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if len(subs) != 2 {
							t.Errorf("expected 2 subscriptions from org, got %d", len(subs))
						}
						return nil
					})
				// 1 org doc + 2 individual docs = 3 CreateRoomMember calls
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).Times(3)
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
			},
		},
		{
			name: "mixed users and orgs",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Members: []model.MemberEntry{
					{Type: model.MemberEntryTypeUser, Username: "dave"},
					{Type: model.MemberEntryTypeOrg, OrgID: "org-eng"},
				},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Role: model.RoleOwner}, nil)
				store.EXPECT().GetRoom(gomock.Any(), "r1").
					Return(&model.Room{ID: "r1", UserCount: 5}, nil)
				store.EXPECT().GetOrgUsers(gomock.Any(), "org-eng").
					Return([]string{"bob", "carol"}, nil)
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").
					Return([]model.RoomMember{}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if len(subs) != 3 { // dave + bob + carol
							t.Errorf("expected 3 subscriptions, got %d", len(subs))
						}
						return nil
					})
				// 1 org doc + 3 individual docs (dave, bob, carol) = 4 calls
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).Times(4)
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
			},
		},
		{
			name: "inviter not found",
			subj: makeSubj("unknown", "r1"),
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Members: []model.MemberEntry{{Type: model.MemberEntryTypeUser, Username: "bob"}},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "unknown", "r1").
					Return(nil, fmt.Errorf("not found"))
			},
			wantErr: true,
		},
		{
			name: "inviter not owner",
			subj: makeSubj("bob", "r1"),
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Members: []model.MemberEntry{{Type: model.MemberEntryTypeUser, Username: "carol"}},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").
					Return(&model.Subscription{Role: model.RoleMember}, nil)
			},
			wantErr: true,
		},
		{
			name: "room at capacity",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Members: []model.MemberEntry{{Type: model.MemberEntryTypeUser, Username: "bob"}},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Role: model.RoleOwner}, nil)
				store.EXPECT().GetRoom(gomock.Any(), "r1").
					Return(&model.Room{ID: "r1", UserCount: 1000}, nil)
			},
			wantErr: true,
		},
		{
			name:    "malformed json",
			subj:    makeSubj("alice", "r1"),
			payload: nil, // triggers raw invalid bytes path in test body
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Role: model.RoleOwner}, nil)
				store.EXPECT().GetRoom(gomock.Any(), "r1").
					Return(&model.Room{ID: "r1", UserCount: 5}, nil)
			},
			wantErr: true,
		},
		{
			name: "org resolution fails",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Members: []model.MemberEntry{{Type: model.MemberEntryTypeOrg, OrgID: "org-bad"}},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Role: model.RoleOwner}, nil)
				store.EXPECT().GetRoom(gomock.Any(), "r1").
					Return(&model.Room{ID: "r1", UserCount: 5}, nil)
				store.EXPECT().GetOrgUsers(gomock.Any(), "org-bad").
					Return(nil, fmt.Errorf("hr_data unavailable"))
			},
			wantErr: true,
		},
		{
			name: "bulk subscription write fails",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Members: []model.MemberEntry{{Type: model.MemberEntryTypeUser, Username: "bob"}},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Role: model.RoleOwner}, nil)
				store.EXPECT().GetRoom(gomock.Any(), "r1").
					Return(&model.Room{ID: "r1", UserCount: 5}, nil)
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").
					Return([]model.RoomMember{}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					Return(fmt.Errorf("write failed"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			tt.setup(store)

			h := &Handler{
				store: store, siteID: "site-a", maxRoomSize: 1000,
				publishToStream: func(data []byte) error { return nil },
				publishLocal:    func(s string, d []byte) error { return nil },
				publishOutbox:   func(s string, d []byte) error { return nil },
			}

			var data []byte
			if tt.payload != nil {
				data, _ = json.Marshal(tt.payload)
			} else {
				data = []byte("{invalid")
			}

			resp, err := h.handleAddMembers(context.Background(), tt.subj, data)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var result map[string]string
			if err := json.Unmarshal(resp, &result); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if result["status"] != "ok" {
				t.Errorf("expected status=ok, got %v", result)
			}
		})
	}
}

func TestHandler_dispatchMemberEvents_LocalUser(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().GetUserSite(gomock.Any(), "bob").Return("site-a", nil)

	var localPublishes []string
	h := &Handler{
		store: store, siteID: "site-a",
		publishLocal:  func(s string, _ []byte) error { localPublishes = append(localPublishes, s); return nil },
		publishOutbox: func(_ string, _ []byte) error { t.Error("unexpected outbox publish"); return nil },
	}

	h.dispatchMemberEvents(context.Background(), []string{"bob"}, "r1")

	if len(localPublishes) != 1 {
		t.Fatalf("expected 1 local publish, got %d", len(localPublishes))
	}
	if localPublishes[0] != subject.Notification("bob") {
		t.Errorf("got subject %q, want %q", localPublishes[0], subject.Notification("bob"))
	}
}

func TestHandler_dispatchMemberEvents_RemoteUser(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().GetUserSite(gomock.Any(), "carol").Return("site-b", nil)

	var outboxSubjects []string
	h := &Handler{
		store: store, siteID: "site-a",
		publishLocal:  func(_ string, _ []byte) error { t.Error("unexpected local publish"); return nil },
		publishOutbox: func(s string, _ []byte) error { outboxSubjects = append(outboxSubjects, s); return nil },
	}

	h.dispatchMemberEvents(context.Background(), []string{"carol"}, "r1")

	if len(outboxSubjects) != 1 {
		t.Fatalf("expected 1 outbox publish, got %d", len(outboxSubjects))
	}
	want := subject.Outbox("site-a", "site-b", "member_added")
	if outboxSubjects[0] != want {
		t.Errorf("got subject %q, want %q", outboxSubjects[0], want)
	}
}

func TestHandler_dispatchMemberEvents_GetUserSiteFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().GetUserSite(gomock.Any(), "bob").Return("", fmt.Errorf("users collection unavailable"))

	h := &Handler{
		store: store, siteID: "site-a",
		publishLocal:  func(_ string, _ []byte) error { t.Error("unexpected local publish"); return nil },
		publishOutbox: func(_ string, _ []byte) error { t.Error("unexpected outbox publish"); return nil },
	}

	// Must not panic — errors are logged and skipped
	h.dispatchMemberEvents(context.Background(), []string{"bob"}, "r1")
}
