//go:build integration

package main

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/model"
)

func TestMongoStore_Integration(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Test CreateRoom and GetRoom
	room := model.Room{ID: "r1", Name: "general", Type: model.RoomTypeGroup, SiteID: "site-a", CreatedBy: "u1"}
	if err := store.CreateRoom(ctx, &room); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	got, err := store.GetRoom(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoom: %v", err)
	}
	if got.Name != "general" {
		t.Errorf("Name = %q, want general", got.Name)
	}

	// Test ListRooms
	if err := store.CreateRoom(ctx, &model.Room{ID: "r2", Name: "random", Type: model.RoomTypeGroup}); err != nil {
		t.Fatalf("CreateRoom r2: %v", err)
	}
	rooms, err := store.ListRooms(ctx)
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) != 2 {
		t.Errorf("got %d rooms, want 2", len(rooms))
	}

	// Test CreateSubscription and GetSubscription
	sub := model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Username: "u1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}
	if err := store.CreateSubscription(ctx, &sub); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	gotSub, err := store.GetSubscription(ctx, "u1", "r1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if len(gotSub.Roles) != 1 || gotSub.Roles[0] != model.RoleOwner {
		t.Errorf("Roles = %v, want [owner]", gotSub.Roles)
	}

	// Test not found
	_, err = store.GetSubscription(ctx, "u2", "r1")
	if err == nil {
		t.Error("expected error for missing subscription")
	}
}

func TestMongoStore_RoomMembers(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Empty result for a room with no members
	members, err := store.GetRoomMembers(ctx, "r-unknown")
	if err != nil {
		t.Fatalf("GetRoomMembers on empty room: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("expected 0 members, got %d", len(members))
	}

	// Insert org and individual members
	orgMember := model.RoomMember{ID: "m1", RoomID: "r1", Member: model.RoomMemberEntry{ID: "org-eng", Type: model.RoomMemberTypeOrg}}
	if err := store.CreateRoomMember(ctx, &orgMember); err != nil {
		t.Fatalf("CreateRoomMember org: %v", err)
	}
	indMember := model.RoomMember{ID: "m2", RoomID: "r1", Member: model.RoomMemberEntry{ID: "user-alice", Type: model.RoomMemberTypeIndividual, Username: "alice"}}
	if err := store.CreateRoomMember(ctx, &indMember); err != nil {
		t.Fatalf("CreateRoomMember individual: %v", err)
	}

	// Retrieve and verify
	members, err = store.GetRoomMembers(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoomMembers: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("expected 2 members, got %d", len(members))
	}

	// Different room is unaffected
	other, err := store.GetRoomMembers(ctx, "r2")
	if err != nil {
		t.Fatalf("GetRoomMembers r2: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("expected 0 members for r2, got %d", len(other))
	}
}

func TestMongoStore_BulkCreateSubscriptions(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	subs := []*model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{Username: "alice"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember}},
		{ID: "s2", User: model.SubscriptionUser{Username: "bob"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember}},
	}
	if err := store.BulkCreateSubscriptions(ctx, subs); err != nil {
		t.Fatalf("BulkCreateSubscriptions: %v", err)
	}

	got, err := store.GetSubscription(ctx, "alice", "r1")
	if err != nil {
		t.Fatalf("GetSubscription alice: %v", err)
	}
	if len(got.Roles) != 1 || got.Roles[0] != model.RoleMember {
		t.Errorf("Roles = %v, want [member]", got.Roles)
	}

	got2, err := store.GetSubscription(ctx, "bob", "r1")
	if err != nil {
		t.Fatalf("GetSubscription bob: %v", err)
	}
	if got2.User.Username != "bob" {
		t.Errorf("Username = %q, want bob", got2.User.Username)
	}

	// Empty slice must not error
	if err := store.BulkCreateSubscriptions(ctx, []*model.Subscription{}); err != nil {
		t.Fatalf("BulkCreateSubscriptions empty: %v", err)
	}
}

func TestMongoStore_GetOrgData(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed orgs collection directly
	orgsCol := db.Collection("orgs")
	_, err := orgsCol.InsertOne(ctx, bson.M{
		"_id":         "org-eng",
		"name":        "Engineering",
		"locationUrl": "http://site-a/orgs/eng",
	})
	if err != nil {
		t.Fatalf("InsertOne org: %v", err)
	}

	name, locationURL, err := store.GetOrgData(ctx, "org-eng")
	if err != nil {
		t.Fatalf("GetOrgData org-eng: %v", err)
	}
	if name != "Engineering" {
		t.Errorf("name = %q, want Engineering", name)
	}
	if locationURL != "http://site-a/orgs/eng" {
		t.Errorf("locationURL = %q, want http://site-a/orgs/eng", locationURL)
	}

	// Unknown org returns error
	_, _, err = store.GetOrgData(ctx, "org-unknown")
	if err == nil {
		t.Error("expected error for unknown org, got nil")
	}
}

func TestMongoStore_GetUserID(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed users collection directly
	usersCol := db.Collection("users")
	_, err := usersCol.InsertOne(ctx, bson.M{
		"_id":      "user-123",
		"username": "alice",
	})
	if err != nil {
		t.Fatalf("InsertOne user: %v", err)
	}

	id, err := store.GetUserID(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUserID alice: %v", err)
	}
	if id != "user-123" {
		t.Errorf("id = %q, want user-123", id)
	}

	// Unknown user returns error
	_, err = store.GetUserID(ctx, "nobody")
	if err == nil {
		t.Error("expected error for unknown user, got nil")
	}
}

func TestMongoStore_GetUserSite(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed users collection directly
	usersCol := db.Collection("users")
	_, err := usersCol.InsertOne(ctx, bson.M{
		"username":   "alice",
		"federation": bson.M{"origin": "site-a"},
	})
	if err != nil {
		t.Fatalf("InsertOne user: %v", err)
	}

	site, err := store.GetUserSite(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUserSite alice: %v", err)
	}
	if site != "site-a" {
		t.Errorf("site = %q, want site-a", site)
	}

	// Unknown user returns error
	_, err = store.GetUserSite(ctx, "nobody")
	if err == nil {
		t.Error("expected error for unknown user, got nil")
	}
}

func TestMongoStore_CountSubscriptions(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Empty room
	count, err := store.CountSubscriptions(ctx, "r1")
	if err != nil {
		t.Fatalf("CountSubscriptions empty: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	subs := []*model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{Username: "alice"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember}},
		{ID: "s2", User: model.SubscriptionUser{Username: "bob"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember}},
		// bot usernames — must be excluded
		{ID: "s3", User: model.SubscriptionUser{Username: "notify.bot"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember}},
		{ID: "s4", User: model.SubscriptionUser{Username: "p_webhook"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember}},
	}
	if err := store.BulkCreateSubscriptions(ctx, subs); err != nil {
		t.Fatalf("BulkCreateSubscriptions: %v", err)
	}

	count, err = store.CountSubscriptions(ctx, "r1")
	if err != nil {
		t.Fatalf("CountSubscriptions: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 (bots excluded), got %d", count)
	}
}

func TestMongoStore_DeleteSubscription(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	sub := &model.Subscription{ID: "s1", User: model.SubscriptionUser{Username: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}
	if err := store.CreateSubscription(ctx, sub); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	// Verify it exists
	if _, err := store.GetSubscription(ctx, "alice", "r1"); err != nil {
		t.Fatalf("GetSubscription before delete: %v", err)
	}

	// Delete it
	if err := store.DeleteSubscription(ctx, "alice", "r1"); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}

	// Verify it's gone
	_, err := store.GetSubscription(ctx, "alice", "r1")
	if err == nil {
		t.Error("expected error after deletion, got nil")
	}
}

func TestMongoStore_DeleteRoomMember(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	member := &model.RoomMember{ID: "m1", RoomID: "r1", Member: model.RoomMemberEntry{ID: "user-alice", Type: model.RoomMemberTypeIndividual, Username: "alice"}}
	if err := store.CreateRoomMember(ctx, member); err != nil {
		t.Fatalf("CreateRoomMember: %v", err)
	}

	// Verify it exists
	members, err := store.GetRoomMembers(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoomMembers before delete: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}

	// Delete it
	if err := store.DeleteRoomMember(ctx, "alice", "r1"); err != nil {
		t.Fatalf("DeleteRoomMember: %v", err)
	}

	// Verify it's gone
	members, err = store.GetRoomMembers(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoomMembers after delete: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("expected 0 members after delete, got %d", len(members))
	}
}

func TestMongoStore_DeleteRoomMember_NoMatch(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Delete nonexistent member should not error
	if err := store.DeleteRoomMember(ctx, "nobody", "r-unknown"); err != nil {
		t.Errorf("DeleteRoomMember nonexistent: expected nil error, got %v", err)
	}
}

func TestMongoStore_DeleteOrgRoomMember(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	orgMember := &model.RoomMember{ID: "m1", RoomID: "r1", Member: model.RoomMemberEntry{ID: "org-eng", Type: model.RoomMemberTypeOrg}}
	if err := store.CreateRoomMember(ctx, orgMember); err != nil {
		t.Fatalf("CreateRoomMember: %v", err)
	}

	// Verify it exists
	members, err := store.GetRoomMembers(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoomMembers before delete: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}

	// Delete it
	if err := store.DeleteOrgRoomMember(ctx, "org-eng", "r1"); err != nil {
		t.Fatalf("DeleteOrgRoomMember: %v", err)
	}

	// Verify it's gone
	members, err = store.GetRoomMembers(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoomMembers after delete: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("expected 0 members after delete, got %d", len(members))
	}
}

func TestMongoStore_UpdateSubscriptionRole(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	sub := &model.Subscription{ID: "s1", User: model.SubscriptionUser{Username: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}
	if err := store.CreateSubscription(ctx, sub); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	// Update to owner
	if err := store.UpdateSubscriptionRole(ctx, "alice", "r1", model.RoleOwner); err != nil {
		t.Fatalf("UpdateSubscriptionRole: %v", err)
	}

	// Verify role changed
	got, err := store.GetSubscription(ctx, "alice", "r1")
	if err != nil {
		t.Fatalf("GetSubscription after update: %v", err)
	}
	if len(got.Roles) != 1 || got.Roles[0] != model.RoleOwner {
		t.Errorf("Roles = %v, want [owner]", got.Roles)
	}

	// Update nonexistent subscription should error
	err = store.UpdateSubscriptionRole(ctx, "nobody", "r-unknown", model.RoleOwner)
	if err == nil {
		t.Error("expected error for nonexistent subscription, got nil")
	}
}

func TestMongoStore_CountOwners(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Empty room
	count, err := store.CountOwners(ctx, "r1")
	if err != nil {
		t.Fatalf("CountOwners empty: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	subs := []*model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{Username: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}},
		{ID: "s2", User: model.SubscriptionUser{Username: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}},
	}
	if err := store.BulkCreateSubscriptions(ctx, subs); err != nil {
		t.Fatalf("BulkCreateSubscriptions: %v", err)
	}

	count, err = store.CountOwners(ctx, "r1")
	if err != nil {
		t.Fatalf("CountOwners: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 owner, got %d", count)
	}
}

func TestMongoStore_ListSubscriptionsByRoom(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Empty room returns empty slice, not error
	subs, err := store.ListSubscriptionsByRoom(ctx, "r-unknown")
	if err != nil {
		t.Fatalf("ListSubscriptionsByRoom empty: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("expected 0, got %d", len(subs))
	}

	seed := []*model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{Username: "alice"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember}},
		{ID: "s2", User: model.SubscriptionUser{Username: "bob"}, RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember}},
		{ID: "s3", User: model.SubscriptionUser{Username: "carol"}, RoomID: "r2", SiteID: "site-a", Roles: []model.Role{model.RoleMember}},
	}
	if err := store.BulkCreateSubscriptions(ctx, seed); err != nil {
		t.Fatalf("BulkCreateSubscriptions: %v", err)
	}

	subs, err = store.ListSubscriptionsByRoom(ctx, "r1")
	if err != nil {
		t.Fatalf("ListSubscriptionsByRoom r1: %v", err)
	}
	if len(subs) != 2 {
		t.Errorf("expected 2 subs for r1, got %d", len(subs))
	}

	// Different room unaffected
	subs, err = store.ListSubscriptionsByRoom(ctx, "r2")
	if err != nil {
		t.Fatalf("ListSubscriptionsByRoom r2: %v", err)
	}
	if len(subs) != 1 {
		t.Errorf("expected 1 sub for r2, got %d", len(subs))
	}
}
