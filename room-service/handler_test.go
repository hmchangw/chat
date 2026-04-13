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
