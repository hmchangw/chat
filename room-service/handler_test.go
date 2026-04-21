package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

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

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000}

	req := model.CreateRoomRequest{Name: "general", Type: model.RoomTypeGroup, CreatedBy: "u1", CreatedByAccount: "alice", SiteID: "site-a"}
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
	if capturedSub == nil || capturedSub.User.Account != "alice" {
		t.Errorf("expected owner subscription with Account=alice, got %+v", capturedSub)
	}
}

func TestHandler_InviteOwner_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", UserCount: 1}, nil)

	var jsPublished []byte
	var jsSubject string
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, subj string, data []byte) error {
			jsSubject = subj
			jsPublished = data
			return nil
		},
	}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", InviteeAccount: "bob", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)
	subj := subject.MemberInvite("alice", "r1", "site-a")

	_, err := h.handleInvite(context.Background(), subj, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jsPublished == nil {
		t.Error("expected event published to JetStream")
	}
	if jsSubject != subject.MemberInvite("alice", "r1", "site-a") {
		t.Errorf("published subject = %q, want invite subject", jsSubject)
	}

	// Verify the published InviteMemberRequest has a Timestamp set
	var publishedReq model.InviteMemberRequest
	if err := json.Unmarshal(jsPublished, &publishedReq); err != nil {
		t.Fatalf("unmarshal published request: %v", err)
	}
	if publishedReq.Timestamp <= 0 {
		t.Error("expected Timestamp > 0 on published InviteMemberRequest")
	}
}

func TestHandler_InviteMember_Rejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, data []byte) error { return nil },
	}

	req := model.InviteMemberRequest{InviterID: "u2", InviteeID: "u3", InviteeAccount: "charlie", RoomID: "r1", SiteID: "site-a"}
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
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", UserCount: 1000}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, data []byte) error { return nil },
	}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", InviteeAccount: "bob", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)
	subj := subject.MemberInvite("alice", "r1", "site-a")

	_, err := h.handleInvite(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for room at max size")
	}
}

func TestHandler_UpdateRole_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
	store.EXPECT().
		GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}, nil)

	var publishedData []byte
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, data []byte) error { publishedData = data; return nil },
	}

	req := model.UpdateRoleRequest{Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")

	resp, err := h.handleUpdateRole(context.Background(), subj, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]string
	json.Unmarshal(resp, &result)
	if result["status"] != "accepted" {
		t.Errorf("expected status=accepted, got %v", result)
	}
	if publishedData == nil {
		t.Error("expected event published to JetStream")
	}
}

func TestHandler_UpdateRole_NonOwnerRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().
		GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, data []byte) error { return nil },
	}

	req := model.UpdateRoleRequest{Account: "charlie", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("bob", "r1", "site-a")

	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for non-owner role update")
	}
	if err.Error() != "only owners can update roles" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHandler_UpdateRole_DMRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "dm-room", Type: model.RoomTypeDM}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, data []byte) error { return nil },
	}

	req := model.UpdateRoleRequest{Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")

	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for DM room role update")
	}
	if err.Error() != "role update is only allowed in group rooms" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHandler_UpdateRole_InvalidRole(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, data []byte) error { return nil },
	}

	req := model.UpdateRoleRequest{Account: "bob", NewRole: "admin"}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")

	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for invalid role")
	}
	if err.Error() != "invalid role: must be owner or member" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHandler_UpdateRole_AlreadyHasRole(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
	store.EXPECT().
		GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember, model.RoleOwner}}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, data []byte) error { return nil },
	}

	req := model.UpdateRoleRequest{Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")

	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for duplicate role")
	}
	if err.Error() != "user is already an owner" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHandler_UpdateRole_DemoteNonOwner(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleMember, model.RoleOwner}}, nil)
	store.EXPECT().
		GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, data []byte) error { return nil },
	}

	req := model.UpdateRoleRequest{Account: "bob", NewRole: model.RoleMember}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")

	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for demoting non-owner")
	}
	if err.Error() != "user is not an owner" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHandler_UpdateRole_LastOwnerCannotDemote(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleMember, model.RoleOwner}}, nil).
		Times(2)
	store.EXPECT().
		CountOwners(gomock.Any(), "r1").
		Return(1, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, data []byte) error { return nil },
	}

	req := model.UpdateRoleRequest{Account: "alice", NewRole: model.RoleMember}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")

	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for last owner demotion")
	}
	if err.Error() != "cannot demote the last owner" {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- Error-path tests ---

func TestHandler_UpdateRole_MalformedInput(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")
	_, err := h.handleUpdateRole(context.Background(), subj, []byte("not json"))
	if err == nil {
		t.Fatal("expected error for malformed input")
	}
}

func TestHandler_UpdateRole_GetRoomError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(nil, fmt.Errorf("db error"))
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}
	req := model.UpdateRoleRequest{Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")
	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for GetRoom failure")
	}
}

func TestHandler_UpdateRole_RoomIDMismatch(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}
	// Payload RoomID "r-other" does not match subject RoomID "r1"
	req := model.UpdateRoleRequest{RoomID: "r-other", Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")
	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for RoomID mismatch")
	}
	if err.Error() != "invalid request: room ID mismatch" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHandler_UpdateRole_RequesterSubError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(nil, fmt.Errorf("db error"))
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}
	req := model.UpdateRoleRequest{Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")
	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for requester subscription failure")
	}
}

func TestHandler_UpdateRole_TargetSubError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{
		User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1",
		Roles: []model.Role{model.RoleMember, model.RoleOwner},
	}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").Return(nil, fmt.Errorf("db error"))
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}
	req := model.UpdateRoleRequest{Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")
	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for target subscription failure")
	}
}

func TestHandler_UpdateRole_CountOwnersError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Type: model.RoomTypeGroup}, nil)
	// Self-demotion triggers CountOwners
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{
		User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1",
		Roles: []model.Role{model.RoleMember, model.RoleOwner},
	}, nil).Times(2) // requester + target (same user)
	store.EXPECT().CountOwners(gomock.Any(), "r1").Return(0, fmt.Errorf("db error"))
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}
	req := model.UpdateRoleRequest{Account: "alice", NewRole: model.RoleMember}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")
	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for CountOwners failure")
	}
}

func TestHandler_UpdateRole_PublishError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{
		User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1",
		Roles: []model.Role{model.RoleMember, model.RoleOwner},
	}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").Return(&model.Subscription{
		User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1",
		Roles: []model.Role{model.RoleMember},
	}, nil)
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return fmt.Errorf("nats down") },
	}
	req := model.UpdateRoleRequest{Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")
	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for publish failure")
	}
}

func TestHandler_RemoveMember_SelfLeave_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	hss := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember},
		HistorySharedSince: &hss, JoinedAt: hss,
	}
	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "alice").
		Return(&SubscriptionWithMembership{Subscription: sub, HasIndividualMembership: true}, nil)
	store.EXPECT().CountMembersAndOwners(gomock.Any(), "r1").
		Return(&RoomCounts{MemberCount: 3, OwnerCount: 2}, nil)

	var publishedSubj string
	var publishedData []byte
	handler := NewHandler(store, "site-a", 1000, func(ctx context.Context, subj string, data []byte) error {
		publishedSubj = subj
		publishedData = data
		return nil
	})

	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "alice"})
	resp, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, subject.RoomCanonical("site-a", "member.remove"), publishedSubj)
	var status map[string]string
	require.NoError(t, json.Unmarshal(resp, &status))
	assert.Equal(t, "accepted", status["status"])
	require.NotNil(t, publishedData)

	var published model.RemoveMemberRequest
	require.NoError(t, json.Unmarshal(publishedData, &published))
	assert.Equal(t, "alice", published.Requester)
}

func TestHandler_RemoveMember_OrgOnly_Rejected(t *testing.T) {
	// Org-only guard fires immediately after GetSubscriptionWithMembership;
	// later calls (GetSubscription, CountMembersAndOwners) must not run.
	cases := []struct {
		name      string
		requester string
		target    string
	}{
		{"self-leave", "alice", "alice"},
		{"owner-removes", "bob", "alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			sub := &model.Subscription{
				ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
				RoomID: "r1", Roles: []model.Role{model.RoleMember},
			}
			store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "alice").
				Return(&SubscriptionWithMembership{Subscription: sub, HasOrgMembership: true}, nil)
			handler := NewHandler(store, "site-a", 1000, nil)
			reqSubj := subject.MemberRemove(tc.requester, "r1", "site-a")
			reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: tc.target})
			_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "org members cannot leave individually")
		})
	}
}

func TestHandler_RemoveMember_SelfLeave_NoOrgs_Allowed(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	sub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Roles: []model.Role{model.RoleMember},
	}
	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "alice").
		Return(&SubscriptionWithMembership{Subscription: sub}, nil)
	store.EXPECT().CountMembersAndOwners(gomock.Any(), "r1").
		Return(&RoomCounts{MemberCount: 2, OwnerCount: 1}, nil)

	var publishedData []byte
	handler := NewHandler(store, "site-a", 1000, func(ctx context.Context, _ string, data []byte) error {
		publishedData = data
		return nil
	})
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "alice"})
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.NoError(t, err)
	require.NotNil(t, publishedData)
}

func TestHandler_RemoveMember_LastOwner_Rejected(t *testing.T) {
	cases := []struct {
		name      string
		requester string
	}{
		{"self-leave", "alice"},
		{"owner-removes-last-owner", "bob"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			target := &model.Subscription{
				ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
				RoomID: "r1", Roles: []model.Role{model.RoleOwner},
			}
			store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "alice").
				Return(&SubscriptionWithMembership{Subscription: target, HasIndividualMembership: true}, nil)
			if tc.requester != "alice" {
				ownerSub := &model.Subscription{
					ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
					RoomID: "r1", Roles: []model.Role{model.RoleOwner},
				}
				store.EXPECT().GetSubscription(gomock.Any(), tc.requester, "r1").Return(ownerSub, nil)
			}
			store.EXPECT().CountMembersAndOwners(gomock.Any(), "r1").
				Return(&RoomCounts{MemberCount: 3, OwnerCount: 1}, nil)
			handler := NewHandler(store, "site-a", 1000, nil)
			reqSubj := subject.MemberRemove(tc.requester, "r1", "site-a")
			reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "alice"})
			_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "last owner")
		})
	}
}

func TestHandler_RemoveMember_LastMember_Rejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	sub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Roles: []model.Role{model.RoleMember},
	}
	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "alice").
		Return(&SubscriptionWithMembership{Subscription: sub, HasIndividualMembership: true}, nil)
	store.EXPECT().CountMembersAndOwners(gomock.Any(), "r1").
		Return(&RoomCounts{MemberCount: 1, OwnerCount: 0}, nil)
	handler := NewHandler(store, "site-a", 1000, nil)
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "alice"})
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "last member")
}

func TestHandler_RemoveMember_OwnerRemovesOther_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	targetSub := &model.Subscription{
		ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
		RoomID: "r1", Roles: []model.Role{model.RoleMember},
	}
	ownerSub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Roles: []model.Role{model.RoleOwner},
	}
	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "bob").
		Return(&SubscriptionWithMembership{Subscription: targetSub, HasIndividualMembership: true}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(ownerSub, nil)
	store.EXPECT().CountMembersAndOwners(gomock.Any(), "r1").
		Return(&RoomCounts{MemberCount: 3, OwnerCount: 1}, nil)
	var publishedData []byte
	handler := NewHandler(store, "site-a", 1000, func(ctx context.Context, subj string, data []byte) error {
		publishedData = data
		return nil
	})
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "bob"})
	resp, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, publishedData)
}

func TestHandler_RemoveMember_NonOwnerRemovesOther_Rejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	targetSub := &model.Subscription{
		ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
		RoomID: "r1", Roles: []model.Role{model.RoleMember},
	}
	requesterSub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Roles: []model.Role{model.RoleMember},
	}
	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "bob").
		Return(&SubscriptionWithMembership{Subscription: targetSub, HasIndividualMembership: true}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(requesterSub, nil)
	handler := NewHandler(store, "site-a", 1000, nil)
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "bob"})
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only owners can remove members")
}

func TestHandler_RemoveMember_OwnerRemovesOrg_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	ownerSub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Roles: []model.Role{model.RoleOwner},
	}
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(ownerSub, nil)
	var publishedData []byte
	handler := NewHandler(store, "site-a", 1000, func(ctx context.Context, subj string, data []byte) error {
		publishedData = data
		return nil
	})
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", OrgID: "eng-org"})
	resp, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.NoError(t, err)
	require.NotNil(t, resp)
	var published model.RemoveMemberRequest
	require.NoError(t, json.Unmarshal(publishedData, &published))
	assert.Equal(t, "eng-org", published.OrgID)
}

func TestHandler_RemoveMember_BothAccountAndOrgID_Rejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	handler := NewHandler(store, "site-a", 1000, nil)
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "bob", OrgID: "eng-org"})
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestHandler_RemoveMember_NeitherAccountNorOrgID_Rejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	handler := NewHandler(store, "site-a", 1000, nil)
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1"})
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestHandler_RemoveMember_InvalidSubject(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	handler := NewHandler(store, "site-a", 1000, nil)
	_, err := handler.handleRemoveMember(context.Background(), "bogus", []byte("{}"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid remove-member subject")
}

func TestHandler_RemoveMember_InvalidJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	handler := NewHandler(store, "site-a", 1000, nil)
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, []byte("{not json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid request")
}

func TestHandler_RemoveMember_RoomIDMismatch(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	handler := NewHandler(store, "site-a", 1000, nil)
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	body, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r2", Account: "alice"})
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "room ID mismatch")
}

func TestHandler_RemoveMember_GetTargetError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "alice").
		Return(nil, fmt.Errorf("db down"))
	handler := NewHandler(store, "site-a", 1000, nil)
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	body, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "alice"})
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get target subscription")
}

func TestHandler_RemoveMember_OwnerRemoves_RequesterLookupError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	targetSub := &model.Subscription{
		User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember},
	}
	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "bob").
		Return(&SubscriptionWithMembership{Subscription: targetSub, HasIndividualMembership: true}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
		Return(nil, fmt.Errorf("db down"))
	handler := NewHandler(store, "site-a", 1000, nil)
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	body, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "bob"})
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get requester subscription")
}

func TestHandler_RemoveMember_CountsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	sub := &model.Subscription{
		User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleMember},
	}
	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "alice").
		Return(&SubscriptionWithMembership{Subscription: sub, HasIndividualMembership: true}, nil)
	store.EXPECT().CountMembersAndOwners(gomock.Any(), "r1").
		Return(nil, fmt.Errorf("db down"))
	handler := NewHandler(store, "site-a", 1000, nil)
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	body, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "alice"})
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "count members")
}

func TestHandler_RemoveMember_OrgPath_RequesterLookupError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
		Return(nil, fmt.Errorf("db down"))
	handler := NewHandler(store, "site-a", 1000, nil)
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	body, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", OrgID: "eng-org"})
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get requester subscription")
}

func TestHandler_RemoveMember_PublishError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	sub := &model.Subscription{
		User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleMember},
	}
	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "alice").
		Return(&SubscriptionWithMembership{Subscription: sub, HasIndividualMembership: true}, nil)
	store.EXPECT().CountMembersAndOwners(gomock.Any(), "r1").
		Return(&RoomCounts{MemberCount: 3, OwnerCount: 2}, nil)
	handler := NewHandler(store, "site-a", 1000, func(_ context.Context, _ string, _ []byte) error {
		return fmt.Errorf("nats down")
	})
	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	body, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "alice"})
	_, err := handler.handleRemoveMember(context.Background(), reqSubj, body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish to stream")
}

// --- Add Members tests ---

func TestHandler_AddMembers_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", Type: model.RoomTypeChannel}, nil)
	store.EXPECT().
		GetRoomMembersByRooms(gomock.Any(), []string{"ch1"}).
		Return([]model.RoomMember{
			{ID: "rm1", RoomID: "ch1", Member: model.RoomMemberEntry{ID: "org1", Type: model.RoomMemberOrg}},
			{ID: "rm2", RoomID: "ch1", Member: model.RoomMemberEntry{ID: "bob", Type: model.RoomMemberIndividual, Account: "bob"}},
		}, nil)
	store.EXPECT().
		ResolveAccounts(gomock.Any(), []string{"org1"}, []string{"bob"}, "r1").
		Return([]string{"bob"}, nil)
	store.EXPECT().
		CountSubscriptions(gomock.Any(), "r1").
		Return(5, nil)

	var publishedSubj string
	var publishedData []byte
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 10,
		publishToStream: func(_ context.Context, subj string, data []byte) error {
			publishedSubj = subj
			publishedData = data
			return nil
		},
	}

	req := model.AddMembersRequest{RoomID: "r1", Channels: []string{"ch1"}}
	data, _ := json.Marshal(req)
	subj := subject.MemberAdd("alice", "r1", "site-a")

	resp, err := h.handleAddMembers(context.Background(), subj, data)
	require.NoError(t, err)

	var result map[string]string
	require.NoError(t, json.Unmarshal(resp, &result))
	assert.Equal(t, "accepted", result["status"])

	// Verify published subject is RoomCanonical
	assert.Equal(t, subject.RoomCanonical("site-a", "member.add"), publishedSubj)

	// Verify normalized request
	var normalized model.AddMembersRequest
	require.NoError(t, json.Unmarshal(publishedData, &normalized))
	assert.Equal(t, "r1", normalized.RoomID)
	assert.Equal(t, []string{"bob"}, normalized.Users)
	assert.Equal(t, []string{"org1"}, normalized.Orgs)
}

func TestHandler_AddMembers_DMRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "dm-room", Type: model.RoomTypeDM}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 10,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}

	req := model.AddMembersRequest{RoomID: "r1", Users: []string{"bob"}}
	data, _ := json.Marshal(req)
	subj := subject.MemberAdd("alice", "r1", "site-a")

	_, err := h.handleAddMembers(context.Background(), subj, data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-channel room")
}

func TestHandler_AddMembers_RestrictedNonOwnerRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}, nil)
	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "restricted-room", Type: model.RoomTypeChannel, Restricted: true}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 10,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}

	req := model.AddMembersRequest{RoomID: "r1", Users: []string{"charlie"}}
	data, _ := json.Marshal(req)
	subj := subject.MemberAdd("bob", "r1", "site-a")

	_, err := h.handleAddMembers(context.Background(), subj, data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only owners can add members")
}

func TestHandler_AddMembers_CapacityExceeded(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", Type: model.RoomTypeChannel}, nil)
	store.EXPECT().
		ResolveAccounts(gomock.Any(), gomock.Any(), []string{"u1", "u2", "u3", "u4", "u5"}, "r1").
		Return([]string{"u1", "u2", "u3", "u4", "u5"}, nil)
	store.EXPECT().
		CountSubscriptions(gomock.Any(), "r1").
		Return(8, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 10,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}

	req := model.AddMembersRequest{RoomID: "r1", Users: []string{"u1", "u2", "u3", "u4", "u5"}}
	data, _ := json.Marshal(req)
	subj := subject.MemberAdd("alice", "r1", "site-a")

	_, err := h.handleAddMembers(context.Background(), subj, data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum capacity")
}

func TestHandler_AddMembers_WithChannels(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	var publishedData []byte
	publish := func(_ context.Context, _ string, data []byte) error {
		publishedData = data
		return nil
	}
	h := NewHandler(store, "site-a", 100, publish)

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{
		Roles: []model.Role{model.RoleMember},
	}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{
		ID: "r1", Type: model.RoomTypeChannel,
	}, nil)
	store.EXPECT().GetRoomMembersByRooms(gomock.Any(), []string{"c1", "c2"}).Return([]model.RoomMember{
		{RoomID: "c1", Member: model.RoomMemberEntry{ID: "eng", Type: model.RoomMemberOrg}},
		{RoomID: "c1", Member: model.RoomMemberEntry{ID: "bob", Type: model.RoomMemberIndividual, Account: "bob"}},
	}, nil)
	store.EXPECT().GetAccountsByRooms(gomock.Any(), []string{"c2"}).Return([]string{"dave"}, nil)
	store.EXPECT().ResolveAccounts(gomock.Any(), []string{"eng"}, gomock.Any(), "r1").
		Return([]string{"bob", "dave"}, nil)
	store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(2, nil)

	req := model.AddMembersRequest{
		RoomID:   "r1",
		Channels: []string{"c1", "c2"},
		History:  model.HistoryConfig{Mode: model.HistoryModeAll},
	}
	reqData, _ := json.Marshal(req)

	resp, err := h.handleAddMembers(context.Background(), subject.MemberAdd("alice", "r1", "site-a"), reqData)
	require.NoError(t, err)

	var status map[string]string
	require.NoError(t, json.Unmarshal(resp, &status))
	assert.Equal(t, "accepted", status["status"])

	var published model.AddMembersRequest
	require.NoError(t, json.Unmarshal(publishedData, &published))
	assert.Contains(t, published.Users, "bob")
	assert.Contains(t, published.Users, "dave")
	assert.Equal(t, []string{"eng"}, published.Orgs)
}

func TestHandler_AddMembers_RestrictedOwnerAllowed(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	publish := func(_ context.Context, _ string, _ []byte) error { return nil }
	h := NewHandler(store, "site-a", 100, publish)

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{
		Roles: []model.Role{model.RoleOwner},
	}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{
		ID: "r1", Type: model.RoomTypeChannel, Restricted: true,
	}, nil)
	store.EXPECT().ResolveAccounts(gomock.Any(), gomock.Any(), gomock.Any(), "r1").
		Return([]string{"bob"}, nil)
	store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(1, nil)

	req := model.AddMembersRequest{RoomID: "r1", Users: []string{"bob"}}
	reqData, _ := json.Marshal(req)

	resp, err := h.handleAddMembers(context.Background(), subject.MemberAdd("alice", "r1", "site-a"), reqData)
	require.NoError(t, err)

	var status map[string]string
	require.NoError(t, json.Unmarshal(resp, &status))
	assert.Equal(t, "accepted", status["status"])
}

func TestHandler_AddMembers_EmptyAfterResolve(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	publish := func(_ context.Context, _ string, _ []byte) error { return nil }
	h := NewHandler(store, "site-a", 100, publish)

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{
		Roles: []model.Role{model.RoleMember},
	}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{
		ID: "r1", Type: model.RoomTypeChannel,
	}, nil)
	store.EXPECT().ResolveAccounts(gomock.Any(), gomock.Any(), gomock.Any(), "r1").
		Return(nil, nil)
	store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)

	req := model.AddMembersRequest{RoomID: "r1", Users: []string{"alice"}}
	reqData, _ := json.Marshal(req)

	resp, err := h.handleAddMembers(context.Background(), subject.MemberAdd("alice", "r1", "site-a"), reqData)
	require.NoError(t, err)

	var status map[string]string
	require.NoError(t, json.Unmarshal(resp, &status))
	assert.Equal(t, "accepted", status["status"])
}

func TestHandler_ListMembers(t *testing.T) {
	const siteID = "site-a"
	const roomID = "r1"
	const requester = "alice"
	subj := subject.MemberList(requester, roomID, siteID)

	existingMember := model.RoomMember{
		ID: "rm1", RoomID: roomID, Ts: time.Unix(1, 0).UTC(),
		Member: model.RoomMemberEntry{ID: "alice", Type: model.RoomMemberIndividual, Account: "alice"},
	}
	orgMember := model.RoomMember{
		ID: "rm2", RoomID: roomID, Ts: time.Unix(2, 0).UTC(),
		Member: model.RoomMemberEntry{ID: "org-1", Type: model.RoomMemberOrg},
	}

	type want struct {
		errContains string
		errIs       error
		members     []model.RoomMember
	}
	tests := []struct {
		name      string
		subject   string
		body      []byte
		setupMock func(*MockRoomStore)
		want      want
	}{
		{
			name:    "happy path returns members",
			subject: subj,
			body:    nil,
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
					Return(&model.Subscription{User: model.SubscriptionUser{Account: requester}, RoomID: roomID}, nil)
				s.EXPECT().ListRoomMembers(gomock.Any(), roomID, (*int)(nil), (*int)(nil), false).
					Return([]model.RoomMember{orgMember, existingMember}, nil)
			},
			want: want{members: []model.RoomMember{orgMember, existingMember}},
		},
		{
			name:    "fallback path returns synthesized individuals",
			subject: subj,
			body:    nil,
			setupMock: func(s *MockRoomStore) {
				synth := model.RoomMember{
					ID: "sub-xyz", RoomID: roomID, Ts: time.Unix(3, 0).UTC(),
					Member: model.RoomMemberEntry{ID: "u-alice", Type: model.RoomMemberIndividual, Account: "alice"},
				}
				s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
					Return(&model.Subscription{User: model.SubscriptionUser{Account: requester}, RoomID: roomID}, nil)
				s.EXPECT().ListRoomMembers(gomock.Any(), roomID, (*int)(nil), (*int)(nil), false).
					Return([]model.RoomMember{synth}, nil)
			},
			want: want{members: []model.RoomMember{{
				ID: "sub-xyz", RoomID: roomID, Ts: time.Unix(3, 0).UTC(),
				Member: model.RoomMemberEntry{ID: "u-alice", Type: model.RoomMemberIndividual, Account: "alice"},
			}}},
		},
		{
			name:    "requester not a member",
			subject: subj,
			body:    nil,
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
					Return(nil, fmt.Errorf("missing: %w", model.ErrSubscriptionNotFound))
			},
			want: want{errIs: errNotRoomMember},
		},
		{
			name:      "invalid subject",
			subject:   "chat.garbage",
			body:      nil,
			setupMock: func(s *MockRoomStore) {},
			want:      want{errContains: "invalid list-members subject"},
		},
		{
			name:    "invalid JSON body",
			subject: subj,
			body:    []byte("{not json"),
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
					Return(&model.Subscription{User: model.SubscriptionUser{Account: requester}, RoomID: roomID}, nil)
			},
			want: want{errContains: "invalid request"},
		},
		{
			name:    "non-positive limit: negative",
			subject: subj,
			body:    []byte(`{"limit":-1}`),
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
					Return(&model.Subscription{User: model.SubscriptionUser{Account: requester}, RoomID: roomID}, nil)
			},
			want: want{errContains: "limit must be > 0"},
		},
		{
			name:    "non-positive limit: zero",
			subject: subj,
			body:    []byte(`{"limit":0}`),
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
					Return(&model.Subscription{User: model.SubscriptionUser{Account: requester}, RoomID: roomID}, nil)
			},
			want: want{errContains: "limit must be > 0"},
		},
		{
			name:    "negative offset",
			subject: subj,
			body:    []byte(`{"offset":-1}`),
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
					Return(&model.Subscription{User: model.SubscriptionUser{Account: requester}, RoomID: roomID}, nil)
			},
			want: want{errContains: "offset must be >= 0"},
		},
		{
			name:    "pagination passed through",
			subject: subj,
			body:    []byte(`{"limit":10,"offset":5}`),
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
					Return(&model.Subscription{User: model.SubscriptionUser{Account: requester}, RoomID: roomID}, nil)
				s.EXPECT().ListRoomMembers(gomock.Any(), roomID, gomock.Any(), gomock.Any(), false).
					DoAndReturn(func(_ context.Context, _ string, limit, offset *int, _ bool) ([]model.RoomMember, error) {
						require.NotNil(t, limit)
						require.NotNil(t, offset)
						assert.Equal(t, 10, *limit)
						assert.Equal(t, 5, *offset)
						return []model.RoomMember{}, nil
					})
			},
			want: want{members: []model.RoomMember{}},
		},
		{
			name:    "auth probe infra error",
			subject: subj,
			body:    nil,
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
					Return(nil, fmt.Errorf("mongo exploded"))
			},
			want: want{errContains: "check room membership"},
		},
		{
			name:    "store error on list",
			subject: subj,
			body:    nil,
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
					Return(&model.Subscription{User: model.SubscriptionUser{Account: requester}, RoomID: roomID}, nil)
				s.EXPECT().ListRoomMembers(gomock.Any(), roomID, (*int)(nil), (*int)(nil), false).
					Return(nil, fmt.Errorf("mongo exploded"))
			},
			want: want{errContains: "get room members"},
		},
		{
			name:    "enrich=true passed through to store",
			subject: subj,
			body:    []byte(`{"enrich":true}`),
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
					Return(&model.Subscription{User: model.SubscriptionUser{Account: requester}, RoomID: roomID}, nil)
				s.EXPECT().ListRoomMembers(gomock.Any(), roomID, (*int)(nil), (*int)(nil), true).
					Return([]model.RoomMember{
						{
							ID: "rm1", RoomID: roomID, Ts: time.Unix(1, 0).UTC(),
							Member: model.RoomMemberEntry{
								ID: "alice", Type: model.RoomMemberIndividual, Account: "alice",
								EngName: "Alice Wang", ChineseName: "愛麗絲", IsOwner: true,
							},
						},
					}, nil)
			},
			want: want{members: []model.RoomMember{
				{
					ID: "rm1", RoomID: roomID, Ts: time.Unix(1, 0).UTC(),
					Member: model.RoomMemberEntry{
						ID: "alice", Type: model.RoomMemberIndividual, Account: "alice",
						EngName: "Alice Wang", ChineseName: "愛麗絲", IsOwner: true,
					},
				},
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			tc.setupMock(store)

			h := &Handler{store: store, siteID: siteID}
			resp, err := h.handleListMembers(context.Background(), tc.subject, tc.body)

			if tc.want.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want.errContains)
				return
			}
			if tc.want.errIs != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.want.errIs), "error chain should contain %v, got %v", tc.want.errIs, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want.members, resp.Members)
		})
	}
}

func TestHandler_ListOrgMembers(t *testing.T) {
	const orgID = "sect-eng"
	subj := subject.OrgMembers("alice", orgID)

	members := []model.OrgMember{
		{ID: "u-a", Account: "a", EngName: "A", ChineseName: "AA", SiteID: "site-a"},
		{ID: "u-b", Account: "b", EngName: "B", ChineseName: "BB", SiteID: "site-a"},
	}

	type want struct {
		errContains string
		errIs       error
		members     []model.OrgMember
	}
	tests := []struct {
		name      string
		subject   string
		setupMock func(*MockRoomStore)
		want      want
	}{
		{
			name:    "happy path returns members",
			subject: subj,
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().ListOrgMembers(gomock.Any(), orgID).Return(members, nil)
			},
			want: want{members: members},
		},
		{
			name:      "invalid subject",
			subject:   "chat.garbage",
			setupMock: func(s *MockRoomStore) {},
			want:      want{errContains: "invalid org-members subject"},
		},
		{
			name:    "empty org returns errInvalidOrg",
			subject: subj,
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().ListOrgMembers(gomock.Any(), orgID).Return(nil, errInvalidOrg)
			},
			want: want{errIs: errInvalidOrg},
		},
		{
			name:    "store error is wrapped",
			subject: subj,
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().ListOrgMembers(gomock.Any(), orgID).
					Return(nil, fmt.Errorf("mongo exploded"))
			},
			want: want{errContains: "get org members"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			tc.setupMock(store)

			h := &Handler{store: store, siteID: "site-a"}
			resp, err := h.handleListOrgMembers(context.Background(), tc.subject)

			if tc.want.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want.errContains)
				return
			}
			if tc.want.errIs != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.want.errIs), "error chain should contain %v, got %v", tc.want.errIs, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want.members, resp.Members)
		})
	}
}
