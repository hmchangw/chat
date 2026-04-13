package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

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
	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", InviteeAccount: "bob", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	if err := h.processInvite(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify created subscription has the correct user account
	if createdSub == nil {
		t.Fatal("expected subscription to be created")
	}
	if createdSub.User.Account != "bob" {
		t.Errorf("expected subscription account %q, got %q", "bob", createdSub.User.Account)
	}

	// Verify notifications published (subscription update + room metadata for existing members)
	if len(published) < 3 {
		t.Errorf("expected at least 3 publishes, got %d", len(published))
	}

	subjectSet := make(map[string]bool)
	for _, p := range published {
		subjectSet[p.subj] = true
	}

	if !subjectSet["chat.user.bob.event.subscription.update"] {
		t.Errorf("expected subscription update published to chat.user.bob.event.subscription.update, subjects: %v", subjectSet)
	}
	if !subjectSet["chat.user.alice.event.room.metadata.update"] {
		t.Errorf("expected room metadata published to chat.user.alice.event.room.metadata.update, subjects: %v", subjectSet)
	}
	if !subjectSet["chat.user.bob.event.room.metadata.update"] {
		t.Errorf("expected room metadata published to chat.user.bob.event.room.metadata.update, subjects: %v", subjectSet)
	}

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

type publishedMsg struct {
	subj string
	data []byte
}

func TestHandler_ProcessRoleUpdate_Promote(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().AddRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(nil)
	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember, model.RoleOwner}}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").
		Return(&model.User{ID: "u2", Account: "bob", SiteID: "site-a"}, nil)

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}}

	req := model.UpdateRoleRequest{RoomID: "r1", Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	if err := h.processRoleUpdate(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(published))
	}
	if published[0].subj != "chat.user.bob.event.subscription.update" {
		t.Errorf("subject = %q, want subscription update for bob", published[0].subj)
	}

	var evt model.SubscriptionUpdateEvent
	if err := json.Unmarshal(published[0].data, &evt); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if evt.Action != "role_updated" {
		t.Errorf("action = %q, want role_updated", evt.Action)
	}
	if evt.UserID != "u2" {
		t.Errorf("userID = %q, want u2", evt.UserID)
	}
	if !slices.Contains(evt.Subscription.Roles, model.RoleOwner) {
		t.Errorf("subscription roles = %v, want to contain owner", evt.Subscription.Roles)
	}
	if !slices.Contains(evt.Subscription.Roles, model.RoleMember) {
		t.Errorf("subscription roles = %v, want to contain member", evt.Subscription.Roles)
	}
	if evt.Timestamp <= 0 {
		t.Error("expected Timestamp > 0")
	}
}

func TestHandler_ProcessRoleUpdate_Demote(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().AddRole(gomock.Any(), "bob", "r1", model.RoleMember).Return(nil)
	store.EXPECT().RemoveRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(nil)
	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember}}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").
		Return(&model.User{ID: "u2", Account: "bob", SiteID: "site-a"}, nil)

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}}

	req := model.UpdateRoleRequest{RoomID: "r1", Account: "bob", NewRole: model.RoleMember}
	data, _ := json.Marshal(req)
	if err := h.processRoleUpdate(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(published))
	}

	var evt model.SubscriptionUpdateEvent
	if err := json.Unmarshal(published[0].data, &evt); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if evt.Action != "role_updated" {
		t.Errorf("action = %q, want role_updated", evt.Action)
	}
	if slices.Contains(evt.Subscription.Roles, model.RoleOwner) {
		t.Errorf("subscription roles = %v, should not contain owner after demote", evt.Subscription.Roles)
	}
	if !slices.Contains(evt.Subscription.Roles, model.RoleMember) {
		t.Errorf("subscription roles = %v, want to contain member", evt.Subscription.Roles)
	}
}

func TestHandler_ProcessRoleUpdate_CrossSite(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().AddRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(nil)
	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember, model.RoleOwner}}, nil)
	// User is on site-b (different from handler's site-a) → cross-site
	store.EXPECT().GetUser(gomock.Any(), "bob").
		Return(&model.User{ID: "u2", Account: "bob", SiteID: "site-b"}, nil)

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}}

	req := model.UpdateRoleRequest{RoomID: "r1", Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	if err := h.processRoleUpdate(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(published) != 2 {
		t.Fatalf("expected 2 publishes, got %d", len(published))
	}
	if published[0].subj != "chat.user.bob.event.subscription.update" {
		t.Errorf("first subject = %q, want subscription update", published[0].subj)
	}

	wantOutboxSubj := "outbox.site-a.to.site-b.role_updated"
	if published[1].subj != wantOutboxSubj {
		t.Errorf("second subject = %q, want %q", published[1].subj, wantOutboxSubj)
	}

	var outbox model.OutboxEvent
	if err := json.Unmarshal(published[1].data, &outbox); err != nil {
		t.Fatalf("unmarshal outbox event: %v", err)
	}
	if outbox.Type != "role_updated" {
		t.Errorf("outbox type = %q, want role_updated", outbox.Type)
	}

	var innerEvt model.SubscriptionUpdateEvent
	if err := json.Unmarshal(outbox.Payload, &innerEvt); err != nil {
		t.Fatalf("unmarshal inner event: %v", err)
	}
	if !slices.Contains(innerEvt.Subscription.Roles, model.RoleOwner) {
		t.Errorf("inner subscription roles = %v, want to contain owner", innerEvt.Subscription.Roles)
	}
	if !slices.Contains(innerEvt.Subscription.Roles, model.RoleMember) {
		t.Errorf("inner subscription roles = %v, want to contain member", innerEvt.Subscription.Roles)
	}
}

// --- Error-path tests for processRoleUpdate ---

func TestHandler_ProcessRoleUpdate_InvalidJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, _ string, _ []byte) error {
		t.Fatal("publish should not be called")
		return nil
	}}

	err := h.processRoleUpdate(context.Background(), []byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestHandler_ProcessRoleUpdate_AddRoleError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)
	store.EXPECT().AddRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(fmt.Errorf("db error"))

	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, _ string, _ []byte) error {
		t.Fatal("publish should not be called")
		return nil
	}}

	req := model.UpdateRoleRequest{RoomID: "r1", Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	err := h.processRoleUpdate(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for AddRole failure")
	}
}

func TestHandler_ProcessRoleUpdate_RemoveRoleError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)
	store.EXPECT().AddRole(gomock.Any(), "bob", "r1", model.RoleMember).Return(nil)
	store.EXPECT().RemoveRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(fmt.Errorf("db error"))

	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, _ string, _ []byte) error {
		t.Fatal("publish should not be called")
		return nil
	}}

	req := model.UpdateRoleRequest{RoomID: "r1", Account: "bob", NewRole: model.RoleMember}
	data, _ := json.Marshal(req)
	err := h.processRoleUpdate(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for RemoveRole failure")
	}
}

func TestHandler_ProcessRoleUpdate_GetSubscriptionError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)
	store.EXPECT().AddRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(nil)
	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").Return(nil, fmt.Errorf("db error"))

	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, _ string, _ []byte) error {
		t.Fatal("publish should not be called")
		return nil
	}}

	req := model.UpdateRoleRequest{RoomID: "r1", Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	err := h.processRoleUpdate(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for GetSubscription failure")
	}
}

func TestHandler_ProcessRoleUpdate_PublishError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)
	store.EXPECT().AddRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(nil)
	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember, model.RoleOwner}}, nil)

	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, _ string, _ []byte) error {
		return fmt.Errorf("nats down")
	}}

	req := model.UpdateRoleRequest{RoomID: "r1", Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	err := h.processRoleUpdate(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for publish failure")
	}
}

func TestHandler_ProcessRoleUpdate_UnsupportedRole(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, _ string, _ []byte) error {
		t.Fatal("publish should not be called")
		return nil
	}}

	req := model.UpdateRoleRequest{RoomID: "r1", Account: "bob", NewRole: "admin"}
	data, _ := json.Marshal(req)
	err := h.processRoleUpdate(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for unsupported role")
	}
}
