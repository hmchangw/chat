//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:8")
	if err != nil {
		t.Fatalf("start mongo: %v", err)
	}
	t.Cleanup(func() { container.Terminate(ctx) })

	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("get mongo uri: %v", err)
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	t.Cleanup(func() { client.Disconnect(ctx) })
	return client.Database("chat_test")
}

func TestMongoStore_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Test CreateRoom and GetRoom
	room := model.Room{ID: "r1", Name: "general", Type: model.RoomTypeGroup, SiteID: "site-a", CreatedBy: "u1", UserCount: 1}
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
	store.CreateRoom(ctx, &model.Room{ID: "r2", Name: "random", Type: model.RoomTypeGroup})
	rooms, err := store.ListRooms(ctx)
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) != 2 {
		t.Errorf("got %d rooms, want 2", len(rooms))
	}

	// Test CreateSubscription and GetSubscription
	sub := model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Username: "u1"}, RoomID: "r1", Role: model.RoleOwner}
	if err := store.CreateSubscription(ctx, &sub); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	gotSub, err := store.GetSubscription(ctx, "u1", "r1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if gotSub.Role != model.RoleOwner {
		t.Errorf("Role = %q, want owner", gotSub.Role)
	}

	// Test not found
	_, err = store.GetSubscription(ctx, "u2", "r1")
	if err == nil {
		t.Error("expected error for missing subscription")
	}
}

func TestMongoStore_RoomMembers(t *testing.T) {
	db := setupMongo(t)
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
	orgMember := model.RoomMember{ID: "m1", RoomID: "r1", Type: model.RoomMemberTypeOrg, OrgID: "org-eng"}
	if err := store.CreateRoomMember(ctx, &orgMember); err != nil {
		t.Fatalf("CreateRoomMember org: %v", err)
	}
	indMember := model.RoomMember{ID: "m2", RoomID: "r1", Type: model.RoomMemberTypeIndividual, Username: "alice"}
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
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	subs := []*model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{Username: "alice"}, RoomID: "r1", SiteID: "site-a", Role: model.RoleMember},
		{ID: "s2", User: model.SubscriptionUser{Username: "bob"}, RoomID: "r1", SiteID: "site-a", Role: model.RoleMember},
	}
	if err := store.BulkCreateSubscriptions(ctx, subs); err != nil {
		t.Fatalf("BulkCreateSubscriptions: %v", err)
	}

	got, err := store.GetSubscription(ctx, "alice", "r1")
	if err != nil {
		t.Fatalf("GetSubscription alice: %v", err)
	}
	if got.Role != model.RoleMember {
		t.Errorf("Role = %q, want member", got.Role)
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

func TestMongoStore_GetOrgUsers(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed hr_data directly
	hrData := db.Collection("hr_data")
	_, _ = hrData.InsertMany(ctx, []any{
		bson.M{"sectId": "org-eng", "username": "alice"},
		bson.M{"sectId": "org-eng", "username": "bob"},
		bson.M{"sectId": "org-hr", "username": "carol"},
	})

	users, err := store.GetOrgUsers(ctx, "org-eng")
	if err != nil {
		t.Fatalf("GetOrgUsers org-eng: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d: %v", len(users), users)
	}

	// Unknown org returns empty slice, not error
	users, err = store.GetOrgUsers(ctx, "org-unknown")
	if err != nil {
		t.Fatalf("GetOrgUsers unknown: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("expected 0 users for unknown org, got %d", len(users))
	}
}

func TestMongoStore_GetUserSite(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed users collection directly
	usersCol := db.Collection("users")
	_, _ = usersCol.InsertOne(ctx, bson.M{
		"username":   "alice",
		"federation": bson.M{"origin": "site-a"},
	})

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
