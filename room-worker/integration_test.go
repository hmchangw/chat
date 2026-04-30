//go:build integration

package main

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/testutil"
)

func setupMongo(t *testing.T) *mongo.Database {
	db := testutil.MongoDB(t, "room_worker_test")
	ensureRoomIdempotencyIndexes(t, db)
	return db
}

// ensureRoomIdempotencyIndexes mirrors the room-service-owned indexes that
// processCreateRoom redelivery handling depends on. In production these are
// created by room-service.EnsureIndexes; integration tests must replicate
// them so duplicate-key on retry works.
func ensureRoomIdempotencyIndexes(t *testing.T, db *mongo.Database) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Collection("subscriptions").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "roomId", Value: 1}, {Key: "u.account", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		t.Fatalf("ensure subscriptions unique index: %v", err)
	}
	if _, err := db.Collection("room_members").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "rid", Value: 1}, {Key: "member.type", Value: 1}, {Key: "member.id", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		t.Fatalf("ensure room_members unique index: %v", err)
	}
}

func TestMongoStore_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed a room for ReconcileMemberCounts and GetRoom
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

	// Test ReconcileMemberCounts — sets userCount to the current subscription count.
	if err := store.ReconcileMemberCounts(ctx, "r1"); err != nil {
		t.Fatalf("ReconcileMemberCounts: %v", err)
	}
	room, err := store.GetRoom(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoom: %v", err)
	}
	if room.UserCount != 1 {
		t.Errorf("UserCount = %d, want 1 (actual subscription count)", room.UserCount)
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

func TestMongoStore_GetUserWithMembership_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	_, err := db.Collection("users").InsertOne(ctx, model.User{
		ID: "u1", Account: "alice", SiteID: "site-a", SectID: "eng-org", SectName: "Engineering",
		EngName: "Alice Wang", ChineseName: "愛麗絲",
	})
	require.NoError(t, err)

	t.Run("no org membership and no subscription", func(t *testing.T) {
		result, err := store.GetUserWithMembership(ctx, "r1", "alice")
		require.NoError(t, err)
		assert.Equal(t, "u1", result.ID)
		assert.Equal(t, "alice", result.Account)
		assert.False(t, result.HasOrgMembership)
		assert.Empty(t, result.Roles)
	})

	t.Run("with subscription returns roles", func(t *testing.T) {
		_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
			ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
			RoomID: "r1", Roles: []model.Role{model.RoleOwner, model.RoleMember},
		})
		require.NoError(t, err)

		result, err := store.GetUserWithMembership(ctx, "r1", "alice")
		require.NoError(t, err)
		assert.ElementsMatch(t, []model.Role{model.RoleOwner, model.RoleMember}, result.Roles)
	})

	t.Run("with org membership in room", func(t *testing.T) {
		_, err := db.Collection("room_members").InsertOne(ctx, model.RoomMember{
			ID: "rm1", RoomID: "r1", Ts: time.Now().UTC(),
			Member: model.RoomMemberEntry{ID: "eng-org", Type: model.RoomMemberOrg},
		})
		require.NoError(t, err)

		result, err := store.GetUserWithMembership(ctx, "r1", "alice")
		require.NoError(t, err)
		assert.True(t, result.HasOrgMembership)
	})

	t.Run("user not found", func(t *testing.T) {
		_, err := store.GetUserWithMembership(ctx, "r1", "nonexistent")
		require.Error(t, err)
		assert.ErrorIs(t, err, mongo.ErrNoDocuments)
	})
}

func TestMongoStore_GetOrgMembersWithIndividualStatus_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	users := db.Collection("users")
	_, err := users.InsertOne(ctx, model.User{ID: "u1", Account: "alice", SiteID: "site-a", SectID: "eng-org", SectName: "Engineering"})
	require.NoError(t, err)
	_, err = users.InsertOne(ctx, model.User{ID: "u2", Account: "bob", SiteID: "site-a", SectID: "eng-org", SectName: "Engineering"})
	require.NoError(t, err)

	_, err = db.Collection("room_members").InsertOne(ctx, model.RoomMember{
		ID: "rm1", RoomID: "r1", Ts: time.Now().UTC(),
		Member: model.RoomMemberEntry{ID: "alice", Type: model.RoomMemberIndividual, Account: "alice"},
	})
	require.NoError(t, err)

	results, err := store.GetOrgMembersWithIndividualStatus(ctx, "r1", "eng-org")
	require.NoError(t, err)
	require.Len(t, results, 2)

	statusMap := make(map[string]bool)
	for _, r := range results {
		statusMap[r.Account] = r.HasIndividualMembership
	}
	assert.True(t, statusMap["alice"])
	assert.False(t, statusMap["bob"])
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

func TestMongoStore_DeleteSubscription_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Roles: []model.Role{model.RoleMember}, JoinedAt: time.Now().UTC(),
	}))

	deleted, err := store.DeleteSubscription(ctx, "r1", "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	subs, err := store.ListByRoom(ctx, "r1")
	require.NoError(t, err)
	assert.Empty(t, subs)
}

func TestMongoStore_DeleteSubscriptionsByAccounts_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Roles: []model.Role{model.RoleMember}, JoinedAt: time.Now().UTC(),
	}))
	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
		RoomID: "r1", Roles: []model.Role{model.RoleMember}, JoinedAt: time.Now().UTC(),
	}))
	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "carol"},
		RoomID: "r1", Roles: []model.Role{model.RoleMember}, JoinedAt: time.Now().UTC(),
	}))

	deleted, err := store.DeleteSubscriptionsByAccounts(ctx, "r1", []string{"alice", "bob"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)

	subs, err := store.ListByRoom(ctx, "r1")
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, "carol", subs[0].User.Account)
}

func TestMongoStore_ReconcileMemberCounts_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Room with a stale userCount (e.g., from a drift scenario).
	room := &model.Room{ID: "r1", Name: "general", UserCount: 10, SiteID: "site-a", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	_, err := db.Collection("rooms").InsertOne(ctx, room)
	require.NoError(t, err)

	// Seed 3 subscriptions for r1 — this is the ground truth.
	_, err = db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1"},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1"},
		model.Subscription{ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "carol"}, RoomID: "r1"},
	})
	require.NoError(t, err)

	require.NoError(t, store.ReconcileMemberCounts(ctx, "r1"))

	updated, err := store.GetRoom(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 3, updated.UserCount, "reconcile must set userCount to actual subscription count")

	// Idempotency: running it again yields the same value.
	require.NoError(t, store.ReconcileMemberCounts(ctx, "r1"))
	updated, err = store.GetRoom(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 3, updated.UserCount, "reconcile must be idempotent")
}

func TestMongoStore_DeleteRoomMember_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	_, err := db.Collection("room_members").InsertMany(ctx, []interface{}{
		model.RoomMember{
			ID: "rm-ind", RoomID: "r1", Ts: time.Now().UTC(),
			Member: model.RoomMemberEntry{ID: "u1", Type: model.RoomMemberIndividual, Account: "alice"},
		},
		model.RoomMember{
			ID: "rm-org", RoomID: "r1", Ts: time.Now().UTC(),
			Member: model.RoomMemberEntry{ID: "eng-org", Type: model.RoomMemberOrg},
		},
	})
	require.NoError(t, err)

	t.Run("individual deletes by user id", func(t *testing.T) {
		require.NoError(t, store.DeleteRoomMember(ctx, "r1", model.RoomMemberIndividual, "u1"))
		count, err := db.Collection("room_members").CountDocuments(ctx, bson.M{"_id": "rm-ind"})
		require.NoError(t, err)
		assert.Equal(t, int64(0), count)
	})

	t.Run("passing the account for an individual is a no-op", func(t *testing.T) {
		require.NoError(t, store.DeleteRoomMember(ctx, "r1", model.RoomMemberIndividual, "alice"))
	})

	t.Run("org deletes by id", func(t *testing.T) {
		require.NoError(t, store.DeleteRoomMember(ctx, "r1", model.RoomMemberOrg, "eng-org"))
		count, err := db.Collection("room_members").CountDocuments(ctx, bson.M{"_id": "rm-org"})
		require.NoError(t, err)
		assert.Equal(t, int64(0), count)
	})
}

func mustInsertSub(t *testing.T, db *mongo.Database, sub *model.Subscription) {
	t.Helper()
	_, err := db.Collection("subscriptions").InsertOne(context.Background(), sub)
	require.NoError(t, err)
}

func mustInsertRoom(t *testing.T, db *mongo.Database, r *model.Room) {
	t.Helper()
	_, err := db.Collection("rooms").InsertOne(context.Background(), r)
	require.NoError(t, err)
}

func TestMongoStore_ListNewMembers_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	users := []interface{}{
		model.User{ID: "u1", Account: "alice", SectID: "org1"},
		model.User{ID: "u2", Account: "bob", SectID: "org1"},
		model.User{ID: "u3", Account: "carol", SectID: "org2"},
		model.User{ID: "u4", Account: "dave"},
		model.User{ID: "u5", Account: "helper.bot", SectID: "org1"},
	}
	_, err := db.Collection("users").InsertMany(ctx, users)
	require.NoError(t, err)

	_, err = db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID:     "s1",
		User:   model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1",
	})
	require.NoError(t, err)

	t.Run("merges org members and direct accounts, excludes already-subscribed and bots", func(t *testing.T) {
		got, err := store.ListNewMembers(ctx, []string{"org1"}, []string{"carol", "dave"}, "r1")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"bob", "carol", "dave"}, got)
	})

	t.Run("empty inputs return nil", func(t *testing.T) {
		got, err := store.ListNewMembers(ctx, nil, nil, "r1")
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("orgIDs only", func(t *testing.T) {
		got, err := store.ListNewMembers(ctx, []string{"org2"}, nil, "r1")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"carol"}, got)
	})

	t.Run("directAccounts only", func(t *testing.T) {
		got, err := store.ListNewMembers(ctx, nil, []string{"dave"}, "r1")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"dave"}, got)
	})
}

func TestReconcileMemberCountsSplitsBots(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := NewMongoStore(db)

	// Seed: 3 user subs and 1 bot sub for room r1.
	mustInsertSub(t, db, &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{Account: "alice"}, RoomID: "r1",
	})
	mustInsertSub(t, db, &model.Subscription{
		ID: "s2", User: model.SubscriptionUser{Account: "bob"}, RoomID: "r1",
	})
	mustInsertSub(t, db, &model.Subscription{
		ID: "s3", User: model.SubscriptionUser{Account: "carol"}, RoomID: "r1",
	})
	mustInsertSub(t, db, &model.Subscription{
		ID: "s4", User: model.SubscriptionUser{Account: "weather.bot"}, RoomID: "r1",
	})
	mustInsertRoom(t, db, &model.Room{ID: "r1", Type: model.RoomTypeChannel})

	require.NoError(t, store.ReconcileMemberCounts(ctx, "r1"))

	got, err := store.GetRoom(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 3, got.UserCount)
	assert.Equal(t, 1, got.AppCount)
}

// mustInsertUser inserts a user document directly into the users collection.
func mustInsertUser(t *testing.T, db *mongo.Database, u *model.User) {
	t.Helper()
	_, err := db.Collection("users").InsertOne(context.Background(), u)
	require.NoError(t, err)
}

// newIntegrationHandler creates a Handler wired to the given store and siteID with a no-op publish function.
func newIntegrationHandler(t *testing.T, store *MongoStore, siteID string) *Handler {
	t.Helper()
	noopPublish := func(_ context.Context, _ string, _ []byte, _ string) error { return nil }
	return NewHandler(store, siteID, noopPublish)
}

func TestProcessCreateRoomChannelPersistsAllState(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := NewMongoStore(db)
	mustInsertUser(t, db, &model.User{
		ID: "u_alice", Account: "alice", SiteID: "site-A",
		EngName: "Alice", ChineseName: "爱丽丝",
	})
	mustInsertUser(t, db, &model.User{
		ID: "u_bob", Account: "bob", SiteID: "site-A",
		EngName: "Bob", ChineseName: "鲍勃",
	})

	h := newIntegrationHandler(t, store, "site-A")
	const reqID = "0193abcd-0193-7abc-89ab-0193abcd0193"
	ctx = natsutil.WithRequestID(ctx, reqID)

	body, err := json.Marshal(model.CreateRoomRequest{
		RoomID: "r_xyz", Name: "deal team",
		Users:            []string{"bob"},
		ResolvedUsers:    []string{"bob"},
		RequesterID:      "u_alice",
		RequesterAccount: "alice",
		Timestamp:        time.Now().UTC().UnixMilli(),
	})
	require.NoError(t, err)
	require.NoError(t, h.processCreateRoom(ctx, body))

	room, err := store.GetRoom(ctx, "r_xyz")
	require.NoError(t, err)
	assert.Equal(t, "deal team", room.Name)
	assert.Equal(t, model.RoomTypeChannel, room.Type)
	assert.Equal(t, 2, room.UserCount)
	assert.Equal(t, 0, room.AppCount)

	subCount, err := db.Collection("subscriptions").CountDocuments(ctx, bson.M{"roomId": "r_xyz"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), subCount)

	rmCount, err := db.Collection("room_members").CountDocuments(ctx, bson.M{"rid": "r_xyz"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rmCount)
}

func TestProcessCreateRoomDMPersistsTwoSubsAndZeroMembers(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := NewMongoStore(db)
	mustInsertUser(t, db, &model.User{ID: "u_alice", Account: "alice",
		EngName: "A", ChineseName: "A", SiteID: "site-A"})
	mustInsertUser(t, db, &model.User{ID: "u_bob", Account: "bob",
		EngName: "B", ChineseName: "B", SiteID: "site-B"})

	h := newIntegrationHandler(t, store, "site-A")
	const reqID = "0193abcd-0193-7abc-89ab-0193abcd0193"
	ctx = natsutil.WithRequestID(ctx, reqID)

	roomID := idgen.BuildDMRoomID("u_alice", "u_bob")
	body, err := json.Marshal(model.CreateRoomRequest{
		RoomID:           roomID,
		Users:            []string{"bob"},
		RequesterID:      "u_alice",
		RequesterAccount: "alice",
		Timestamp:        time.Now().UTC().UnixMilli(),
	})
	require.NoError(t, err)
	require.NoError(t, h.processCreateRoom(ctx, body))

	subCount, err := db.Collection("subscriptions").CountDocuments(ctx, bson.M{"roomId": roomID})
	require.NoError(t, err)
	assert.Equal(t, int64(2), subCount)

	rmCount, err := db.Collection("room_members").CountDocuments(ctx, bson.M{"rid": roomID})
	require.NoError(t, err)
	assert.Equal(t, int64(0), rmCount)

	room, err := store.GetRoom(ctx, roomID)
	require.NoError(t, err)
	assert.Equal(t, model.RoomTypeDM, room.Type)
	assert.Empty(t, room.CreatedBy)
}

func TestProcessCreateRoomIdempotentRedelivery(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := NewMongoStore(db)
	mustInsertUser(t, db, &model.User{ID: "u_alice", Account: "alice",
		EngName: "A", ChineseName: "A", SiteID: "site-A"})
	mustInsertUser(t, db, &model.User{ID: "u_bob", Account: "bob",
		EngName: "B", ChineseName: "B", SiteID: "site-A"})

	h := newIntegrationHandler(t, store, "site-A")
	const reqID = "0193abcd-0193-7abc-89ab-0193abcd0193"
	ctx = natsutil.WithRequestID(ctx, reqID)

	body, err := json.Marshal(model.CreateRoomRequest{
		RoomID: "r_idem", Name: "team",
		Users:            []string{"bob"},
		ResolvedUsers:    []string{"bob"},
		RequesterID:      "u_alice",
		RequesterAccount: "alice",
		Timestamp:        time.Now().UTC().UnixMilli(),
	})
	require.NoError(t, err)

	require.NoError(t, h.processCreateRoom(ctx, body))
	require.NoError(t, h.processCreateRoom(ctx, body))

	subCount, err := db.Collection("subscriptions").CountDocuments(ctx, bson.M{"roomId": "r_idem"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), subCount, "redelivery must not create duplicate subs")
}
