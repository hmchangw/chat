//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:4.4.15")
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

func TestMongoStore_GetSubscriptionWithMembership_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	sub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleOwner},
		JoinedAt: time.Now().UTC(),
	}
	require.NoError(t, store.CreateSubscription(ctx, sub))

	t.Run("no individual or org membership", func(t *testing.T) {
		result, err := store.GetSubscriptionWithMembership(ctx, "r1", "alice")
		require.NoError(t, err)
		assert.Equal(t, "alice", result.Subscription.User.Account)
		assert.False(t, result.HasIndividualMembership)
		assert.False(t, result.HasOrgMembership)
	})

	t.Run("with individual membership", func(t *testing.T) {
		_, err := db.Collection("room_members").InsertOne(ctx, model.RoomMember{
			ID: "rm1", RoomID: "r1", Ts: time.Now().UTC(),
			Member: model.RoomMemberEntry{ID: "alice", Type: model.RoomMemberIndividual, Account: "alice"},
		})
		require.NoError(t, err)

		result, err := store.GetSubscriptionWithMembership(ctx, "r1", "alice")
		require.NoError(t, err)
		assert.True(t, result.HasIndividualMembership)
	})

	t.Run("with org membership", func(t *testing.T) {
		_, err := db.Collection("users").InsertOne(ctx, model.User{
			ID: "u1", Account: "alice", SiteID: "site-a", SectID: "eng-org",
		})
		require.NoError(t, err)

		_, err = db.Collection("room_members").InsertOne(ctx, model.RoomMember{
			ID: "rm-org", RoomID: "r1", Ts: time.Now().UTC(),
			Member: model.RoomMemberEntry{ID: "eng-org", Type: model.RoomMemberOrg},
		})
		require.NoError(t, err)

		result, err := store.GetSubscriptionWithMembership(ctx, "r1", "alice")
		require.NoError(t, err)
		assert.True(t, result.HasOrgMembership)
	})

	t.Run("subscription not found", func(t *testing.T) {
		_, err := store.GetSubscriptionWithMembership(ctx, "r1", "nonexistent")
		require.Error(t, err)
	})
}

func TestMongoStore_CountMembersAndOwners_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	_, err := db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner, model.RoleMember}},
		model.Subscription{ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "carol"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}},
		model.Subscription{ID: "s4", User: model.SubscriptionUser{ID: "u4", Account: "dave"}, RoomID: "r2", Roles: []model.Role{model.RoleOwner}},
	})
	require.NoError(t, err)

	t.Run("counts members and owners", func(t *testing.T) {
		counts, err := store.CountMembersAndOwners(ctx, "r1")
		require.NoError(t, err)
		assert.Equal(t, 3, counts.MemberCount)
		assert.Equal(t, 2, counts.OwnerCount)
	})

	t.Run("only owner", func(t *testing.T) {
		counts, err := store.CountMembersAndOwners(ctx, "r2")
		require.NoError(t, err)
		assert.Equal(t, 1, counts.MemberCount)
		assert.Equal(t, 1, counts.OwnerCount)
	})

	t.Run("empty room returns zeros", func(t *testing.T) {
		counts, err := store.CountMembersAndOwners(ctx, "nonexistent")
		require.NoError(t, err)
		assert.Equal(t, 0, counts.MemberCount)
		assert.Equal(t, 0, counts.OwnerCount)
	})
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
