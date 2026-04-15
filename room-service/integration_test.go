//go:build integration

package main

import (
	"context"
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
	sub := model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}
	if err := store.CreateSubscription(ctx, &sub); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	gotSub, err := store.GetSubscription(ctx, "alice", "r1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if len(gotSub.Roles) == 0 || gotSub.Roles[0] != model.RoleOwner {
		t.Errorf("Roles = %v, want [owner]", gotSub.Roles)
	}

	// Test not found
	_, err = store.GetSubscription(ctx, "u2", "r1")
	if err == nil {
		t.Error("expected error for missing subscription")
	}
}

func TestMongoStore_CountOwners_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	_, err := db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}},
		model.Subscription{ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "charlie"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}},
		model.Subscription{ID: "s4", User: model.SubscriptionUser{ID: "u4", Account: "dave"}, RoomID: "r2", Roles: []model.Role{model.RoleOwner}},
	})
	if err != nil {
		t.Fatalf("seed subscriptions: %v", err)
	}

	count, err := store.CountOwners(ctx, "r1")
	if err != nil {
		t.Fatalf("CountOwners: %v", err)
	}
	if count != 2 {
		t.Errorf("CountOwners(r1) = %d, want 2", count)
	}

	count, err = store.CountOwners(ctx, "r2")
	if err != nil {
		t.Fatalf("CountOwners: %v", err)
	}
	if count != 1 {
		t.Errorf("CountOwners(r2) = %d, want 1", count)
	}

	count, err = store.CountOwners(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("CountOwners: %v", err)
	}
	if count != 0 {
		t.Errorf("CountOwners(nonexistent) = %d, want 0", count)
	}
}
