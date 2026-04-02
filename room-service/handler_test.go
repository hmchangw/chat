package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
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
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Username: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
	store.EXPECT().
		CountSubscriptions(gomock.Any(), "r1").
		Return(1, nil)

	var jsPublished []byte
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ string, data []byte) error { jsPublished = data; return nil },
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
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u2", Username: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ string, _ []byte) error { return nil },
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
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Username: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
	store.EXPECT().
		CountSubscriptions(gomock.Any(), "r1").
		Return(1000, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ string, _ []byte) error { return nil },
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
		name        string
		subj        string
		payload     any
		setup       func(store *MockRoomStore)
		wantErr     bool
		checkResult func(t *testing.T, subs []*model.Subscription)
	}{
		{
			name: "individuals only, no existing room members",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Users:   []string{"bob", "carol"},
				History: model.HistoryConfig{Mode: model.HistoryModeNone},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
				store.EXPECT().GetUserID(gomock.Any(), "bob").Return("u-bob", nil)
				store.EXPECT().GetUserID(gomock.Any(), "carol").Return("u-carol", nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if len(subs) != 2 {
							t.Errorf("expected 2 subscriptions, got %d", len(subs))
						}
						usernames := []string{subs[0].User.Username, subs[1].User.Username}
						if usernames[0] != "bob" || usernames[1] != "carol" {
							t.Errorf("unexpected usernames: %v", usernames)
						}
						// history=none: HistorySharedSince must be set
						if subs[0].HistorySharedSince.IsZero() {
							t.Error("expected HistorySharedSince to be set for history=none")
						}
						// userID must be set
						if subs[0].User.ID != "u-bob" {
							t.Errorf("expected user ID u-bob, got %s", subs[0].User.ID)
						}
						if subs[1].User.ID != "u-carol" {
							t.Errorf("expected user ID u-carol, got %s", subs[1].User.ID)
						}
						return nil
					})
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				// CreateRoomMember must NOT be called — no existing members, no orgs
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
			},
		},
		{
			name: "history=all: HistorySharedSince is zero",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:  "r1",
				Users:   []string{"bob"},
				History: model.HistoryConfig{Mode: model.HistoryModeAll},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
				store.EXPECT().GetUserID(gomock.Any(), "bob").Return("u-bob", nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if !subs[0].HistorySharedSince.IsZero() {
							t.Error("expected HistorySharedSince to be zero for history=all")
						}
						return nil
					})
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
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
				store.EXPECT().GetUserID(gomock.Any(), "carol").Return("u-carol", nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if len(subs) != 1 || subs[0].User.Username != "carol" {
							t.Errorf("expected only carol after bot filter, got %d subs", len(subs))
						}
						return nil
					})
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
			},
		},
		{
			name: "individuals only, existing room members present",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"dave"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
				store.EXPECT().GetUserID(gomock.Any(), "dave").Return("u-dave", nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").
					Return([]model.RoomMember{{ID: "m0", RoomID: "r1", Member: model.RoomMemberEntry{ID: "org-eng", Type: model.RoomMemberTypeOrg}}}, nil)
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, m *model.RoomMember) error {
						if m.Member.Type != model.RoomMemberTypeIndividual || m.Member.Username != "dave" {
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
				RoomID: "r1",
				Orgs:   []string{"org-eng"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetOrgData(gomock.Any(), "org-eng").Return("Eng", "http://site-a", nil)
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
				store.EXPECT().GetUserID(gomock.Any(), "Eng").Return("u-eng", nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if len(subs) != 1 {
							t.Errorf("expected 1 subscription for org-expanded user Eng, got %d", len(subs))
						}
						if len(subs) > 0 && subs[0].User.Username != "Eng" {
							t.Errorf("expected username Eng, got %s", subs[0].User.Username)
						}
						return nil
					})
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				// 1 org doc + 1 individual doc (Eng) = 2 CreateRoomMember calls
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).Times(2)
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
			},
		},
		{
			name: "org in request — remote org",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Orgs:   []string{"org-remote"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetOrgData(gomock.Any(), "org-remote").Return("RemoteEng", "https://site-b.example.com", nil)
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
				store.EXPECT().GetUserID(gomock.Any(), "RemoteEng@site-b.example.com").Return("u-remote", nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if len(subs) != 1 {
							t.Errorf("expected 1 subscription, got %d", len(subs))
						}
						if len(subs) > 0 && subs[0].User.Username != "RemoteEng@site-b.example.com" {
							t.Errorf("expected username RemoteEng@site-b.example.com, got %s", subs[0].User.Username)
						}
						return nil
					})
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				// 1 org doc + 1 individual doc = 2 CreateRoomMember calls
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).Times(2)
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-b", nil).AnyTimes()
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
				store.EXPECT().GetOrgData(gomock.Any(), "org-eng").Return("Eng", "http://site-a", nil)
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
				store.EXPECT().GetUserID(gomock.Any(), "dave").Return("u-dave", nil)
				store.EXPECT().GetUserID(gomock.Any(), "Eng").Return("u-eng", nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if len(subs) != 2 {
							t.Errorf("expected 2 subscriptions (dave + Eng), got %d", len(subs))
						}
						return nil
					})
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				// 1 org doc + 2 individual docs (dave + Eng) = 3 CreateRoomMember calls
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).Times(3)
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
			},
		},
		{
			name: "channel source: room_members non-empty — copy orgs and individuals",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:   "r1",
				Channels: []string{"r-source"},
			},
			setup: func(store *MockRoomStore) {
				// channel resolution: room_members has org + individual
				store.EXPECT().GetRoomMembers(gomock.Any(), "r-source").Return([]model.RoomMember{
					{ID: "m1", RoomID: "r-source", Member: model.RoomMemberEntry{ID: "org-eng", Type: model.RoomMemberTypeOrg}},
					{ID: "m2", RoomID: "r-source", Member: model.RoomMemberEntry{Type: model.RoomMemberTypeIndividual, Username: "dave"}},
				}, nil)
				store.EXPECT().ListSubscriptionsByRoom(gomock.Any(), "r-source").Return([]model.Subscription{
					{User: model.SubscriptionUser{Username: "dave"}},
				}, nil)
				store.EXPECT().GetOrgData(gomock.Any(), "org-eng").Return("Eng", "http://site-a", nil)
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
				store.EXPECT().GetUserID(gomock.Any(), "dave").Return("u-dave", nil)
				store.EXPECT().GetUserID(gomock.Any(), "Eng").Return("u-eng", nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if len(subs) != 2 {
							t.Errorf("expected 2 subscriptions (dave + Eng), got %d", len(subs))
						}
						return nil
					})
				// room_members for target room is empty
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				// org present: 1 org doc + 2 individual docs (dave + Eng) = 3 CreateRoomMember calls
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).Times(3)
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
			},
		},
		{
			name: "channel source: room_members empty — fall back to subscriptions",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID:   "r1",
				Channels: []string{"r-source"},
			},
			setup: func(store *MockRoomStore) {
				// channel resolution: room_members empty, fall back to subscriptions
				store.EXPECT().GetRoomMembers(gomock.Any(), "r-source").Return([]model.RoomMember{}, nil)
				store.EXPECT().ListSubscriptionsByRoom(gomock.Any(), "r-source").Return([]model.Subscription{
					{User: model.SubscriptionUser{Username: "bob"}},
					{User: model.SubscriptionUser{Username: "carol"}},
				}, nil)
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
				store.EXPECT().GetUserID(gomock.Any(), "bob").Return("u-bob", nil)
				store.EXPECT().GetUserID(gomock.Any(), "carol").Return("u-carol", nil)
				// room_members for target room
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if len(subs) != 2 {
							t.Errorf("expected 2 subscriptions, got %d", len(subs))
						}
						return nil
					})
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
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
					{User: model.SubscriptionUser{Username: "bob"}},
					{User: model.SubscriptionUser{Username: "notify.bot"}},
					{User: model.SubscriptionUser{Username: "p_webhook"}},
				}, nil)
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
				store.EXPECT().GetUserID(gomock.Any(), "bob").Return("u-bob", nil)
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
						if len(subs) != 1 || subs[0].User.Username != "bob" {
							t.Errorf("expected only bob after bot filter, got %v subs", len(subs))
						}
						return nil
					})
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
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
				store.EXPECT().GetUserID(gomock.Any(), "bob").Return("u-bob", nil)
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
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
				store.EXPECT().GetOrgData(gomock.Any(), "org-bad").Return("", "", fmt.Errorf("orgs collection unavailable"))
			},
			wantErr: true,
		},
		{
			name: "bulk subscription write fails",
			subj: makeSubj("alice", "r1"),
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bob"},
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetUserID(gomock.Any(), "bob").Return("u-bob", nil)
				store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()
				store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)
				store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(fmt.Errorf("write failed"))
			},
			wantErr: true,
		},
		{
			name: "invalid subject",
			subj: "chat.invalid",
			payload: model.AddMembersRequest{
				RoomID: "r1",
				Users:  []string{"bob"},
			},
			setup:   func(store *MockRoomStore) {},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			tt.setup(store)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := &Handler{
				store: store, siteID: "site-a", currentDomain: "http://site-a", maxRoomSize: 1000,
				publishToStream: func(_ string, _ []byte) error { return nil },
				publishLocal:    func(s string, d []byte) error { return nil },
				publishOutbox:   func(s string, d []byte) error { return nil },
				notifCh:         make(chan notifTask, 100),
				stopCtx:         ctx,
				stopCancel:      cancel,
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

	var localPublishes []string
	h := &Handler{
		store: store, siteID: "site-a",
		publishLocal:  func(s string, _ []byte) error { localPublishes = append(localPublishes, s); return nil },
		publishOutbox: func(_ string, _ []byte) error { t.Error("unexpected outbox publish"); return nil },
	}

	h.dispatchMemberEvents(context.Background(), []*model.Subscription{
		{User: model.SubscriptionUser{ID: "u-bob", Username: "bob"}, RoomID: "r1", SiteID: "site-a"},
	}, "r1")

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

	var outboxSubjects []string
	h := &Handler{
		store: store, siteID: "site-a",
		publishLocal:  func(_ string, _ []byte) error { t.Error("unexpected local publish"); return nil },
		publishOutbox: func(s string, _ []byte) error { outboxSubjects = append(outboxSubjects, s); return nil },
	}

	h.dispatchMemberEvents(context.Background(), []*model.Subscription{
		{User: model.SubscriptionUser{ID: "u-carol", Username: "carol"}, RoomID: "r1", SiteID: "site-b"},
	}, "r1")

	if len(outboxSubjects) != 1 {
		t.Fatalf("expected 1 outbox publish, got %d", len(outboxSubjects))
	}
	want := subject.Outbox("site-a", "site-b", "member_added")
	if outboxSubjects[0] != want {
		t.Errorf("got subject %q, want %q", outboxSubjects[0], want)
	}
}

func TestHandler_dispatchMemberEvents_ContextCancelled(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	h := &Handler{
		store: store, siteID: "site-a",
		publishLocal:  func(_ string, _ []byte) error { t.Error("unexpected local publish"); return nil },
		publishOutbox: func(_ string, _ []byte) error { t.Error("unexpected outbox publish"); return nil },
	}

	// Cancelled context should cause immediate return
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.dispatchMemberEvents(ctx, []*model.Subscription{
		{User: model.SubscriptionUser{ID: "u-bob", Username: "bob"}, RoomID: "r1", SiteID: "site-a"},
	}, "r1")
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
					{ID: "m2", RoomID: "r-src", Member: model.RoomMemberEntry{Type: model.RoomMemberTypeIndividual, Username: "alice"}},
				}, nil)
				store.EXPECT().ListSubscriptionsByRoom(gomock.Any(), "r-src").Return([]model.Subscription{
					{User: model.SubscriptionUser{Username: "alice"}},
					{User: model.SubscriptionUser{Username: "bob"}},
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
					{User: model.SubscriptionUser{Username: "bob"}},
					{User: model.SubscriptionUser{Username: "carol"}},
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

			h := &Handler{store: store, siteID: "site-a"}
			orgIDs, usernames, err := h.expandChannels(context.Background(), tt.channelIDs)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(orgIDs) != len(tt.wantOrgIDs) {
				t.Errorf("orgIDs: got %v, want %v", orgIDs, tt.wantOrgIDs)
			}
			for i := range tt.wantOrgIDs {
				if i < len(orgIDs) && orgIDs[i] != tt.wantOrgIDs[i] {
					t.Errorf("orgIDs[%d]: got %q, want %q", i, orgIDs[i], tt.wantOrgIDs[i])
				}
			}
			if len(usernames) != len(tt.wantUsernames) {
				t.Errorf("usernames: got %v, want %v", usernames, tt.wantUsernames)
			}
			for i := range tt.wantUsernames {
				if i < len(usernames) && usernames[i] != tt.wantUsernames[i] {
					t.Errorf("usernames[%d]: got %q, want %q", i, usernames[i], tt.wantUsernames[i])
				}
			}
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
			name:   "local org",
			orgIDs: []string{"org-eng"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetOrgData(gomock.Any(), "org-eng").Return("Eng", "http://site-a", nil)
			},
			wantUsernames: []string{"Eng"},
		},
		{
			name:   "remote org",
			orgIDs: []string{"org-remote"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetOrgData(gomock.Any(), "org-remote").Return("RemoteEng", "https://site-b.example.com", nil)
			},
			wantUsernames: []string{"RemoteEng@site-b.example.com"},
		},
		{
			name:   "GetOrgData error",
			orgIDs: []string{"org-bad"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetOrgData(gomock.Any(), "org-bad").Return("", "", fmt.Errorf("not found"))
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

			h := &Handler{store: store, siteID: "site-a", currentDomain: "http://site-a"}
			usernames, err := h.resolveOrgs(context.Background(), tt.orgIDs)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(usernames) != len(tt.wantUsernames) {
				t.Errorf("usernames: got %v, want %v", usernames, tt.wantUsernames)
			}
			for i := range tt.wantUsernames {
				if i < len(usernames) && usernames[i] != tt.wantUsernames[i] {
					t.Errorf("usernames[%d]: got %q, want %q", i, usernames[i], tt.wantUsernames[i])
				}
			}
		})
	}
}

func TestHandler_buildSubscriptions(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name              string
		usernames         []string
		roomID            string
		mode              model.HistoryMode
		setup             func(store *MockRoomStore)
		wantSubCount      int
		wantResolvedCount int
		checkSubs         func(t *testing.T, subs []*model.Subscription, resolved []string)
	}{
		{
			name:      "all users resolved",
			usernames: []string{"alice", "bob"},
			roomID:    "r1",
			mode:      model.HistoryModeNone,
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetUserID(gomock.Any(), "alice").Return("u-alice", nil)
				store.EXPECT().GetUserID(gomock.Any(), "bob").Return("u-bob", nil)
			},
			wantSubCount:      2,
			wantResolvedCount: 2,
			checkSubs: func(t *testing.T, subs []*model.Subscription, resolved []string) {
				t.Helper()
				if subs[0].User.Username != "alice" || subs[1].User.Username != "bob" {
					t.Errorf("unexpected usernames: %s, %s", subs[0].User.Username, subs[1].User.Username)
				}
				if resolved[0] != "alice" || resolved[1] != "bob" {
					t.Errorf("unexpected resolvedUsernames: %v", resolved)
				}
			},
		},
		{
			name:      "some users skipped",
			usernames: []string{"alice", "bad-user", "bob"},
			roomID:    "r1",
			mode:      model.HistoryModeNone,
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetUserID(gomock.Any(), "alice").Return("u-alice", nil)
				store.EXPECT().GetUserID(gomock.Any(), "bad-user").Return("", fmt.Errorf("get user id for %q: %w", "bad-user", mongo.ErrNoDocuments))
				store.EXPECT().GetUserID(gomock.Any(), "bob").Return("u-bob", nil)
			},
			wantSubCount:      2,
			wantResolvedCount: 2,
			checkSubs: func(t *testing.T, subs []*model.Subscription, resolved []string) {
				t.Helper()
				for _, s := range subs {
					if s.User.Username == "bad-user" {
						t.Error("bad-user should have been skipped")
					}
				}
			},
		},
		{
			name:      "all skipped",
			usernames: []string{"bad1", "bad2"},
			roomID:    "r1",
			mode:      model.HistoryModeNone,
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetUserID(gomock.Any(), "bad1").Return("", fmt.Errorf("get user id for %q: %w", "bad1", mongo.ErrNoDocuments))
				store.EXPECT().GetUserID(gomock.Any(), "bad2").Return("", fmt.Errorf("get user id for %q: %w", "bad2", mongo.ErrNoDocuments))
			},
			wantSubCount:      0,
			wantResolvedCount: 0,
		},
		{
			name:      "history mode all: HistorySharedSince zero",
			usernames: []string{"alice"},
			roomID:    "r1",
			mode:      model.HistoryModeAll,
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetUserID(gomock.Any(), "alice").Return("u-alice", nil)
			},
			wantSubCount:      1,
			wantResolvedCount: 1,
			checkSubs: func(t *testing.T, subs []*model.Subscription, _ []string) {
				t.Helper()
				if !subs[0].HistorySharedSince.IsZero() {
					t.Error("expected HistorySharedSince to be zero for history=all")
				}
			},
		},
		{
			name:      "history mode none: HistorySharedSince set",
			usernames: []string{"alice"},
			roomID:    "r1",
			mode:      model.HistoryModeNone,
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetUserID(gomock.Any(), "alice").Return("u-alice", nil)
			},
			wantSubCount:      1,
			wantResolvedCount: 1,
			checkSubs: func(t *testing.T, subs []*model.Subscription, _ []string) {
				t.Helper()
				if subs[0].HistorySharedSince.IsZero() {
					t.Error("expected HistorySharedSince to be set for history=none")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			tt.setup(store)

			store.EXPECT().GetUserSite(gomock.Any(), gomock.Any()).Return("site-a", nil).AnyTimes()

			h := &Handler{store: store, siteID: "site-a"}
			subs, resolved, err := h.buildSubscriptions(context.Background(), tt.usernames, tt.roomID, tt.mode, now)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(subs) != tt.wantSubCount {
				t.Errorf("subs count: got %d, want %d", len(subs), tt.wantSubCount)
			}
			if len(resolved) != tt.wantResolvedCount {
				t.Errorf("resolved count: got %d, want %d", len(resolved), tt.wantResolvedCount)
			}
			if tt.checkSubs != nil && len(subs) > 0 {
				tt.checkSubs(t, subs, resolved)
			}
		})
	}
}

func TestHandler_RemoveMember(t *testing.T) {
	tests := []struct {
		name    string
		subj    string
		payload any
		setup   func(store *MockRoomStore)
		wantErr bool
	}{
		{
			name: "self-leave: member removes themselves",
			subj: subject.MemberRemove("alice", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				Username: "alice",
				RoomID:   "r1",
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Roles: []model.Role{model.RoleMember}}, nil)
				store.EXPECT().DeleteSubscription(gomock.Any(), "alice", "r1").Return(nil)
				store.EXPECT().DeleteRoomMember(gomock.Any(), "alice", "r1").Return(nil)
				store.EXPECT().DecrementUserCount(gomock.Any(), "r1").Return(nil)
			},
		},
		{
			name: "owner removes individual member",
			subj: subject.MemberRemove("owner1", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				Username: "bob",
				RoomID:   "r1",
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{Roles: []model.Role{model.RoleOwner}}, nil)
				store.EXPECT().DeleteSubscription(gomock.Any(), "bob", "r1").Return(nil)
				store.EXPECT().DeleteRoomMember(gomock.Any(), "bob", "r1").Return(nil)
				store.EXPECT().DecrementUserCount(gomock.Any(), "r1").Return(nil)
			},
		},
		{
			name: "non-owner cannot remove another member",
			subj: subject.MemberRemove("bob", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				Username: "carol",
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
				store.EXPECT().
					GetOrgData(gomock.Any(), "org-eng").
					Return("Eng", "http://site-a", nil)
				store.EXPECT().DeleteSubscription(gomock.Any(), "Eng", "r1").Return(nil)
				store.EXPECT().DeleteOrgRoomMember(gomock.Any(), "org-eng", "r1").Return(nil)
				store.EXPECT().DeleteRoomMember(gomock.Any(), "Eng", "r1").Return(nil)
				store.EXPECT().DecrementUserCount(gomock.Any(), "r1").Return(nil)
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
			name:    "invalid subject",
			subj:    "chat.invalid",
			payload: model.RemoveMemberRequest{RoomID: "r1", Username: "alice"},
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
		{
			name: "delete subscription fails",
			subj: subject.MemberRemove("alice", "r1", "site-a"),
			payload: model.RemoveMemberRequest{
				Username: "alice",
				RoomID:   "r1",
			},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
					Return(&model.Subscription{Roles: []model.Role{model.RoleMember}}, nil)
				store.EXPECT().DeleteSubscription(gomock.Any(), "alice", "r1").Return(fmt.Errorf("db error"))
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
				store: store, siteID: "site-a", currentDomain: "http://site-a", maxRoomSize: 1000,
				publishToStream: func(_ string, _ []byte) error { return nil },
				publishLocal:    func(_ string, _ []byte) error { return nil },
				publishOutbox:   func(_ string, _ []byte) error { return nil },
			}

			var data []byte
			if tt.payload != nil {
				data, _ = json.Marshal(tt.payload)
			} else {
				data = []byte("{invalid")
			}

			resp, err := h.handleRemoveMember(context.Background(), tt.subj, data)
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

func TestHandler_writeRoomMembers(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name              string
		roomID            string
		orgIDs            []string
		resolvedUsernames []string
		setup             func(store *MockRoomStore)
		wantErr           bool
	}{
		{
			name:              "orgs present, no existing members",
			roomID:            "r1",
			orgIDs:            []string{"org-eng"},
			resolvedUsernames: []string{"alice"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				// 1 org doc + 1 individual doc
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).Times(2)
			},
		},
		{
			name:              "orgs present, existing members present",
			roomID:            "r1",
			orgIDs:            []string{"org-eng"},
			resolvedUsernames: []string{"alice"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{
					{ID: "m0", RoomID: "r1", Member: model.RoomMemberEntry{ID: "org-other", Type: model.RoomMemberTypeOrg}},
				}, nil)
				// 1 org doc + 1 individual doc
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).Times(2)
			},
		},
		{
			name:              "no orgs, existing members",
			roomID:            "r1",
			orgIDs:            []string{},
			resolvedUsernames: []string{"bob"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{
					{ID: "m0", RoomID: "r1", Member: model.RoomMemberEntry{ID: "org-other", Type: model.RoomMemberTypeOrg}},
				}, nil)
				// Only 1 individual doc
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(nil).Times(1)
			},
		},
		{
			name:              "no orgs, no existing members",
			roomID:            "r1",
			orgIDs:            []string{},
			resolvedUsernames: []string{"carol"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				// No CreateRoomMember calls
			},
		},
		{
			name:              "CreateRoomMember error",
			roomID:            "r1",
			orgIDs:            []string{"org-eng"},
			resolvedUsernames: []string{},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return([]model.RoomMember{}, nil)
				store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).Return(fmt.Errorf("write failed"))
			},
			wantErr: true,
		},
		{
			name:              "GetRoomMembers error",
			roomID:            "r1",
			orgIDs:            []string{},
			resolvedUsernames: []string{"bob"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().GetRoomMembers(gomock.Any(), "r1").Return(nil, fmt.Errorf("db error"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			tt.setup(store)

			h := &Handler{store: store, siteID: "site-a"}
			err := h.writeRoomMembers(context.Background(), tt.roomID, tt.orgIDs, tt.resolvedUsernames, now)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestHandler_UpdateRole(t *testing.T) {
	makeSubj := func(username, roomID string) string {
		return subject.MemberRoleUpdate(username, roomID, "site-a")
	}

	tests := []struct {
		name    string
		subj    string
		payload any
		setup   func(store *MockRoomStore)
		wantErr bool
	}{
		{
			name:    "owner promotes member to owner",
			subj:    makeSubj("owner1", "r1"),
			payload: model.UpdateRoleRequest{Username: "bob", NewRole: model.RoleOwner},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Username: "owner1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
				store.EXPECT().
					UpdateSubscriptionRole(gomock.Any(), "bob", "r1", model.RoleOwner).
					Return(nil)
			},
		},
		{
			name:    "owner demotes another owner to member",
			subj:    makeSubj("owner1", "r1"),
			payload: model.UpdateRoleRequest{Username: "owner2", NewRole: model.RoleMember},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Username: "owner1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
				store.EXPECT().
					UpdateSubscriptionRole(gomock.Any(), "owner2", "r1", model.RoleMember).
					Return(nil)
			},
		},
		{
			name:    "non-owner cannot change roles",
			subj:    makeSubj("bob", "r1"),
			payload: model.UpdateRoleRequest{Username: "alice", NewRole: model.RoleOwner},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "bob", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Username: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}, nil)
			},
			wantErr: true,
		},
		{
			name:    "cannot promote federation user to owner",
			subj:    makeSubj("owner1", "r1"),
			payload: model.UpdateRoleRequest{Username: "Eng@site-b.example.com", NewRole: model.RoleOwner},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Username: "owner1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
			},
			wantErr: true,
		},
		{
			name:    "last owner cannot demote themselves",
			subj:    makeSubj("owner1", "r1"),
			payload: model.UpdateRoleRequest{Username: "owner1", NewRole: model.RoleMember},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Username: "owner1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
				store.EXPECT().
					CountOwners(gomock.Any(), "r1").
					Return(1, nil)
			},
			wantErr: true,
		},
		{
			name:    "owner with co-owners can demote themselves",
			subj:    makeSubj("owner1", "r1"),
			payload: model.UpdateRoleRequest{Username: "owner1", NewRole: model.RoleMember},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Username: "owner1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
				store.EXPECT().
					CountOwners(gomock.Any(), "r1").
					Return(2, nil)
				store.EXPECT().
					UpdateSubscriptionRole(gomock.Any(), "owner1", "r1", model.RoleMember).
					Return(nil)
			},
		},
		{
			name:    "invalid role value",
			subj:    makeSubj("owner1", "r1"),
			payload: model.UpdateRoleRequest{Username: "bob", NewRole: "admin"},
			setup: func(store *MockRoomStore) {
				store.EXPECT().
					GetSubscription(gomock.Any(), "owner1", "r1").
					Return(&model.Subscription{User: model.SubscriptionUser{Username: "owner1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
			},
			wantErr: true,
		},
		{
			name:    "invalid subject",
			subj:    "chat.invalid",
			payload: model.UpdateRoleRequest{Username: "bob", NewRole: model.RoleOwner},
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

			h := &Handler{
				store: store, siteID: "site-a", currentDomain: "http://site-a", maxRoomSize: 1000,
				publishToStream: func(_ string, _ []byte) error { return nil },
				publishLocal:    func(_ string, _ []byte) error { return nil },
				publishOutbox:   func(_ string, _ []byte) error { return nil },
			}

			var data []byte
			if tt.payload != nil {
				data, _ = json.Marshal(tt.payload)
			}

			resp, err := h.handleUpdateRole(context.Background(), tt.subj, data)

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
				t.Fatalf("failed to unmarshal response: %v", err)
			}
			if result["status"] != "ok" {
				t.Errorf("expected status=ok, got %v", result)
			}
		})
	}
}
