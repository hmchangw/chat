//go:build integration

package main

import (
	"context"
	"slices"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/mongodb"
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

	// Seed a room for IncrementUserCount and GetRoom
	db.Collection("rooms").InsertOne(ctx, model.Room{ID: "r1", Name: "general", UserCount: 1})

	// Test CreateSubscription
	sub := model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}
	if err := store.CreateSubscription(ctx, &sub); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	// Test ListByRoom
	subs, err := store.ListByRoom(ctx, "r1")
	if err != nil {
		t.Fatalf("ListByRoom: %v", err)
	}
	if len(subs) != 1 || subs[0].User.ID != "u1" {
		t.Errorf("got %+v", subs)
	}

	// Test IncrementUserCount
	if err := store.IncrementUserCount(ctx, "r1"); err != nil {
		t.Fatalf("IncrementUserCount: %v", err)
	}
	room, err := store.GetRoom(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoom: %v", err)
	}
	if room.UserCount != 2 {
		t.Errorf("UserCount = %d, want 2", room.UserCount)
	}
}

func TestMongoStore_GetSubscription_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleOwner},
	})
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	sub, err := store.GetSubscription(ctx, "alice", "r1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if sub.User.Account != "alice" || sub.RoomID != "r1" {
		t.Errorf("got %+v", sub)
	}
	if !slices.Contains(sub.Roles, model.RoleOwner) {
		t.Errorf("roles = %v, want to contain owner", sub.Roles)
	}

	_, err = store.GetSubscription(ctx, "nonexistent", "r1")
	if err == nil {
		t.Error("expected error for nonexistent subscription")
	}
}

func TestMongoStore_AddRole_RemoveRole_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Roles: []model.Role{model.RoleMember},
	})
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	// Promote: add owner role
	err = store.AddRole(ctx, "alice", "r1", model.RoleOwner)
	if err != nil {
		t.Fatalf("AddRole: %v", err)
	}

	sub, err := store.GetSubscription(ctx, "alice", "r1")
	if err != nil {
		t.Fatalf("GetSubscription after promote: %v", err)
	}
	if !slices.Contains(sub.Roles, model.RoleOwner) {
		t.Errorf("roles after promote = %v, want to contain owner", sub.Roles)
	}
	if !slices.Contains(sub.Roles, model.RoleMember) {
		t.Errorf("roles after promote = %v, want to still contain member", sub.Roles)
	}

	// AddRole is idempotent ($addToSet)
	err = store.AddRole(ctx, "alice", "r1", model.RoleOwner)
	if err != nil {
		t.Fatalf("AddRole idempotent: %v", err)
	}
	sub, err = store.GetSubscription(ctx, "alice", "r1")
	if err != nil {
		t.Fatalf("GetSubscription after idempotent add: %v", err)
	}
	if len(sub.Roles) != 2 {
		t.Errorf("roles after idempotent add = %v, want exactly 2", sub.Roles)
	}

	// Demote: remove owner role
	err = store.RemoveRole(ctx, "alice", "r1", model.RoleOwner)
	if err != nil {
		t.Fatalf("RemoveRole: %v", err)
	}

	sub, err = store.GetSubscription(ctx, "alice", "r1")
	if err != nil {
		t.Fatalf("GetSubscription after demote: %v", err)
	}
	if slices.Contains(sub.Roles, model.RoleOwner) {
		t.Errorf("roles after demote = %v, should not contain owner", sub.Roles)
	}
	if !slices.Contains(sub.Roles, model.RoleMember) {
		t.Errorf("roles after demote = %v, want to contain member", sub.Roles)
	}
}
