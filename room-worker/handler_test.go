package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
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

// --- processRemoveMember tests ---

func TestHandler_ProcessRemoveMember_SelfLeave_IndividualOnly(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	const (
		roomID  = "room-1"
		account = "alice"
		siteID  = "site-a"
	)

	userResult := &UserWithOrgMembership{
		User: model.User{
			ID:          "u1",
			Account:     account,
			SiteID:      siteID,
			EngName:     "Alice",
			ChineseName: "愛麗絲",
		},
		HasOrgMembership: false,
	}

	store.EXPECT().
		GetUserWithOrgMembership(gomock.Any(), roomID, account).
		Return(userResult, nil)
	store.EXPECT().
		DeleteSubscription(gomock.Any(), roomID, account).
		Return(nil)
	store.EXPECT().
		DeleteRoomMember(gomock.Any(), roomID, model.RoomMemberIndividual, account).
		Return(nil)
	store.EXPECT().
		DecrementUserCount(gomock.Any(), roomID, 1).
		Return(nil)

	var published []publishedMsg
	h := NewHandler(store, siteID, func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	})

	req := model.RemoveMemberRequest{RoomID: roomID, Account: account}
	data, _ := json.Marshal(req)
	subj := subject.MemberRemove(account, roomID, siteID)

	err := h.processRemoveMember(context.Background(), subj, data)
	require.NoError(t, err)

	// Expect: subscription update + member change event + system message = 3 publishes
	assert.Len(t, published, 3, "expected 3 publishes: sub update, member event, sys msg")

	subjSet := make(map[string]bool)
	for _, p := range published {
		subjSet[p.subj] = true
	}

	assert.True(t, subjSet[subject.SubscriptionUpdate(account)], "expected subscription update published")
	assert.True(t, subjSet[subject.MemberEvent(roomID)], "expected member event published")

	// Verify timestamps on all events
	for _, p := range published {
		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(p.data, &raw))
		tsRaw, ok := raw["timestamp"]
		if !ok {
			continue // sys msg may not have timestamp at top level
		}
		var ts int64
		require.NoError(t, json.Unmarshal(tsRaw, &ts))
		assert.NotZero(t, ts, "timestamp should be non-zero for subject %s", p.subj)
	}
}

func TestHandler_ProcessRemoveMember_SelfLeave_DualMembership(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	const (
		roomID  = "room-1"
		account = "alice"
		siteID  = "site-a"
	)

	userResult := &UserWithOrgMembership{
		User: model.User{
			ID:      "u1",
			Account: account,
			SiteID:  siteID,
		},
		HasOrgMembership: true,
	}

	// Only DeleteRoomMember(individual) called — no subscription delete, no events
	store.EXPECT().
		GetUserWithOrgMembership(gomock.Any(), roomID, account).
		Return(userResult, nil)
	store.EXPECT().
		DeleteRoomMember(gomock.Any(), roomID, model.RoomMemberIndividual, account).
		Return(nil)

	var published []publishedMsg
	h := NewHandler(store, siteID, func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	})

	req := model.RemoveMemberRequest{RoomID: roomID, Account: account}
	data, _ := json.Marshal(req)
	subj := subject.MemberRemove(account, roomID, siteID)

	err := h.processRemoveMember(context.Background(), subj, data)
	require.NoError(t, err)

	assert.Empty(t, published, "expected no publishes for dual-membership self-leave")
}

func TestHandler_ProcessRemoveMember_OwnerRemovesIndividual(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	const (
		roomID    = "room-1"
		account   = "bob"
		requester = "alice"
		siteID    = "site-a"
	)

	userResult := &UserWithOrgMembership{
		User: model.User{
			ID:          "u2",
			Account:     account,
			SiteID:      siteID,
			EngName:     "Bob",
			ChineseName: "鮑伯",
		},
		HasOrgMembership: false,
	}

	store.EXPECT().
		GetUserWithOrgMembership(gomock.Any(), roomID, account).
		Return(userResult, nil)
	store.EXPECT().
		DeleteSubscription(gomock.Any(), roomID, account).
		Return(nil)
	// Owner removes: DeleteRoomMembersByAccount (all entries)
	store.EXPECT().
		DeleteRoomMembersByAccount(gomock.Any(), roomID, account).
		Return(nil)
	store.EXPECT().
		DecrementUserCount(gomock.Any(), roomID, 1).
		Return(nil)

	var published []publishedMsg
	h := NewHandler(store, siteID, func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	})

	req := model.RemoveMemberRequest{RoomID: roomID, Account: account}
	data, _ := json.Marshal(req)
	// requester != account: different account in subject
	subj := subject.MemberRemove(requester, roomID, siteID)

	err := h.processRemoveMember(context.Background(), subj, data)
	require.NoError(t, err)

	assert.Len(t, published, 3, "expected 3 publishes: sub update, member event, sys msg")

	// Verify the sys msg has type "member_removed"
	for _, p := range published {
		if p.subj == subject.MemberEvent(roomID) {
			var evt model.MemberChangeEvent
			require.NoError(t, json.Unmarshal(p.data, &evt))
			assert.Equal(t, "member_removed", evt.Type)
			assert.Contains(t, evt.Accounts, account)
		}
	}
}

func TestHandler_ProcessRemoveMember_OwnerRemovesOrg(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	const (
		roomID    = "room-1"
		orgID     = "org-1"
		requester = "alice"
		siteID    = "site-a"
	)

	// 3 org members: carol and dave have no individual membership, eve does
	orgMembers := []OrgMemberStatus{
		{Account: "carol", SiteID: siteID, SectName: "Engineering", HasIndividualMembership: false},
		{Account: "dave", SiteID: siteID, SectName: "Engineering", HasIndividualMembership: false},
		{Account: "eve", SiteID: siteID, SectName: "Engineering", HasIndividualMembership: true},
	}

	store.EXPECT().
		GetOrgMembersWithIndividualStatus(gomock.Any(), roomID, orgID).
		Return(orgMembers, nil)
	store.EXPECT().
		DeleteSubscriptionsByAccounts(gomock.Any(), roomID, gomock.InAnyOrder([]string{"carol", "dave"})).
		Return(nil)
	store.EXPECT().
		DeleteRoomMember(gomock.Any(), roomID, model.RoomMemberOrg, orgID).
		Return(nil)
	store.EXPECT().
		DecrementUserCount(gomock.Any(), roomID, 2). // carol + dave only
		Return(nil)

	var published []publishedMsg
	h := NewHandler(store, siteID, func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	})

	req := model.RemoveMemberRequest{RoomID: roomID, OrgID: orgID}
	data, _ := json.Marshal(req)
	subj := subject.MemberRemove(requester, roomID, siteID)

	err := h.processRemoveMember(context.Background(), subj, data)
	require.NoError(t, err)

	// Expect: 2 sub updates (carol, dave) + 1 member event + 1 sys msg = 4 publishes
	assert.Len(t, published, 4, "expected 4 publishes: 2 sub updates, member event, sys msg")

	subjSet := make(map[string]bool)
	for _, p := range published {
		subjSet[p.subj] = true
	}

	assert.True(t, subjSet[subject.SubscriptionUpdate("carol")], "expected subscription update for carol")
	assert.True(t, subjSet[subject.SubscriptionUpdate("dave")], "expected subscription update for dave")
	assert.False(t, subjSet[subject.SubscriptionUpdate("eve")], "eve has individual membership, should not be removed")
	assert.True(t, subjSet[subject.MemberEvent(roomID)], "expected member event published")
}

func TestHandler_ProcessRemoveMember_CrossSiteOutbox(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	const (
		roomID    = "room-1"
		account   = "alice"
		localSite = "site-a"
		userSite  = "site-b" // user is on a different site
	)

	userResult := &UserWithOrgMembership{
		User: model.User{
			ID:      "u1",
			Account: account,
			SiteID:  userSite, // different from local site
		},
		HasOrgMembership: false,
	}

	store.EXPECT().
		GetUserWithOrgMembership(gomock.Any(), roomID, account).
		Return(userResult, nil)
	store.EXPECT().
		DeleteSubscription(gomock.Any(), roomID, account).
		Return(nil)
	store.EXPECT().
		DeleteRoomMember(gomock.Any(), roomID, model.RoomMemberIndividual, account).
		Return(nil)
	store.EXPECT().
		DecrementUserCount(gomock.Any(), roomID, 1).
		Return(nil)

	var published []publishedMsg
	h := NewHandler(store, localSite, func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	})

	req := model.RemoveMemberRequest{RoomID: roomID, Account: account}
	data, _ := json.Marshal(req)
	subj := subject.MemberRemove(account, roomID, localSite)

	err := h.processRemoveMember(context.Background(), subj, data)
	require.NoError(t, err)

	// Expect: sub update + member event + sys msg + outbox = 4 publishes
	assert.Len(t, published, 4, "expected 4 publishes including outbox for federated user")

	outboxSubj := subject.Outbox(localSite, userSite, "member_removed")
	subjSet := make(map[string]bool)
	for _, p := range published {
		subjSet[p.subj] = true
	}
	assert.True(t, subjSet[outboxSubj], "expected outbox event published for remote user")
}
