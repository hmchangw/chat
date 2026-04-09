//go:build integration

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

const testSiteID = "site-test"

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
	return client.Database("room_worker_test")
}

// noopPublish is a no-op publish function for tests that don't need to verify publishes.
func noopPublish(_ context.Context, _ string, _ []byte) error { return nil }

func TestMongoStore_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed a room for IncrementUserCount and GetRoom
	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{ID: "r1", Name: "general", UserCount: 1})
	require.NoError(t, err)

	// Test CreateSubscription
	sub := model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}
	require.NoError(t, store.CreateSubscription(ctx, &sub))

	// Test ListByRoom
	subs, err := store.ListByRoom(ctx, "r1")
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, "u1", subs[0].User.ID)

	// Test IncrementUserCount
	require.NoError(t, store.IncrementUserCount(ctx, "r1"))
	room, err := store.GetRoom(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 2, room.UserCount)
}

func TestProcessInvite_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed room
	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{ID: "r1", Name: "general", UserCount: 1, SiteID: testSiteID})
	require.NoError(t, err)

	// Seed an existing subscription so ListByRoom returns something for metadata events
	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s-owner", User: model.SubscriptionUser{ID: "u-owner", Account: "owner"}, RoomID: "r1", SiteID: testSiteID, Roles: []model.Role{model.RoleOwner},
	}))

	handler := NewHandler(store, testSiteID, noopPublish, noopPublish)

	req := model.InviteMemberRequest{
		RoomID:         "r1",
		InviteeID:      "u-alice",
		InviteeAccount: "alice",
		SiteID:         testSiteID,
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	require.NoError(t, handler.processInvite(ctx, data))

	// Verify subscription created
	subs, err := store.ListByRoom(ctx, "r1")
	require.NoError(t, err)
	assert.Len(t, subs, 2, "should have owner + invitee")

	// Verify user count incremented
	room, err := store.GetRoom(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 2, room.UserCount)
}

func TestProcessAddMembers_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed room
	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{ID: "r-add", Name: "general", UserCount: 1, SiteID: testSiteID})
	require.NoError(t, err)

	// Seed users
	_, err = db.Collection("users").InsertMany(ctx, []interface{}{
		model.User{ID: "u-alice", Account: "alice", SiteID: testSiteID},
		model.User{ID: "u-bob", Account: "bob", SiteID: testSiteID},
	})
	require.NoError(t, err)

	handler := NewHandler(store, testSiteID, noopPublish, noopPublish)

	req := model.AddMembersRequest{
		RoomID:  "r-add",
		Users:   []string{"alice", "bob"},
		History: model.HistoryConfig{Mode: model.HistoryModeNone},
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	require.NoError(t, handler.processAddMembers(ctx, data))

	// Wait for async goroutines to complete (room members, user count)
	handler.asyncWg.Wait()

	// Verify subscriptions created
	subs, err := store.ListByRoom(ctx, "r-add")
	require.NoError(t, err)
	assert.Len(t, subs, 2)

	// Verify HistorySharedSince is set for mode=none
	for _, sub := range subs {
		assert.NotNil(t, sub.HistorySharedSince, "HistorySharedSince should be set for mode=none")
	}

	// Verify room members created (async)
	members, err := store.GetRoomMembers(ctx, "r-add")
	require.NoError(t, err)
	assert.Len(t, members, 2, "individual room member entries should be created")
}

func TestProcessAddMembers_HistoryAll_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed room
	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{ID: "r-hist", Name: "general", UserCount: 1, SiteID: testSiteID})
	require.NoError(t, err)

	// Seed user
	_, err = db.Collection("users").InsertOne(ctx, model.User{ID: "u-carol", Account: "carol", SiteID: testSiteID})
	require.NoError(t, err)

	handler := NewHandler(store, testSiteID, noopPublish, noopPublish)

	req := model.AddMembersRequest{
		RoomID:  "r-hist",
		Users:   []string{"carol"},
		History: model.HistoryConfig{Mode: model.HistoryModeAll},
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	require.NoError(t, handler.processAddMembers(ctx, data))

	// Verify HistorySharedSince is zero for mode=all
	subs, err := store.ListByRoom(ctx, "r-hist")
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Nil(t, subs[0].HistorySharedSince, "HistorySharedSince should be nil for mode=all")
}

func TestProcessRemoveMember_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed room
	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{ID: "r-rm", Name: "general", UserCount: 2, SiteID: testSiteID})
	require.NoError(t, err)

	// Seed subscription
	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s-alice", User: model.SubscriptionUser{ID: "u-alice", Account: "alice"}, RoomID: "r-rm", SiteID: testSiteID, Roles: []model.Role{model.RoleMember},
	}))

	handler := NewHandler(store, testSiteID, noopPublish, noopPublish)

	req := model.RemoveMemberRequest{
		RoomID:   "r-rm",
		Username: "alice",
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	require.NoError(t, handler.processRemoveMember(ctx, data))

	// Wait for async goroutines to complete
	handler.asyncWg.Wait()

	// Verify subscription deleted
	subs, err := store.ListByRoom(ctx, "r-rm")
	require.NoError(t, err)
	assert.Len(t, subs, 0, "subscription should be deleted")

	// Verify user count decremented
	room, err := store.GetRoom(ctx, "r-rm")
	require.NoError(t, err)
	assert.Equal(t, 1, room.UserCount)
}

func TestProcessRemoveMember_OrgRemoval_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed room
	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{ID: "r-org-rm", Name: "general", UserCount: 3, SiteID: testSiteID})
	require.NoError(t, err)

	// Seed hr_data for org resolution
	_, err = db.Collection("hr_data").InsertMany(ctx, []interface{}{
		bson.M{"accountName": "alice", "sectId": "org-eng"},
		bson.M{"accountName": "bob", "sectId": "org-eng"},
	})
	require.NoError(t, err)

	// Seed subscriptions for org members
	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s-alice", User: model.SubscriptionUser{ID: "u-alice", Account: "alice"}, RoomID: "r-org-rm", SiteID: testSiteID, Roles: []model.Role{model.RoleMember},
	}))
	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s-bob", User: model.SubscriptionUser{ID: "u-bob", Account: "bob"}, RoomID: "r-org-rm", SiteID: testSiteID, Roles: []model.Role{model.RoleMember},
	}))

	handler := NewHandler(store, testSiteID, noopPublish, noopPublish)

	req := model.RemoveMemberRequest{
		RoomID: "r-org-rm",
		OrgID:  "org-eng",
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	require.NoError(t, handler.processRemoveMember(ctx, data))

	// Wait for async goroutines to complete
	handler.asyncWg.Wait()

	// Verify subscriptions deleted
	subs, err := store.ListByRoom(ctx, "r-org-rm")
	require.NoError(t, err)
	assert.Len(t, subs, 0, "all org member subscriptions should be deleted")
}

func TestProcessRoleUpdate_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed subscription
	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s-bob", User: model.SubscriptionUser{ID: "u-bob", Account: "bob"}, RoomID: "r-role", SiteID: testSiteID, Roles: []model.Role{model.RoleMember},
	}))

	handler := NewHandler(store, testSiteID, noopPublish, noopPublish)

	req := model.UpdateRoleRequest{
		RoomID:   "r-role",
		Username: "bob",
		NewRole:  model.RoleOwner,
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	require.NoError(t, handler.processRoleUpdate(ctx, data))

	// Verify role updated
	subs, err := store.ListByRoom(ctx, "r-role")
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, model.RoleOwner, subs[0].Role, "bob should now be owner")
}

func TestProcessRoleUpdate_Demote_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed subscription as owner
	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s-carol", User: model.SubscriptionUser{ID: "u-carol", Account: "carol"}, RoomID: "r-demote", SiteID: testSiteID, Roles: []model.Role{model.RoleOwner},
	}))

	handler := NewHandler(store, testSiteID, noopPublish, noopPublish)

	req := model.UpdateRoleRequest{
		RoomID:   "r-demote",
		Username: "carol",
		NewRole:  model.RoleMember,
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	require.NoError(t, handler.processRoleUpdate(ctx, data))

	// Verify role updated
	subs, err := store.ListByRoom(ctx, "r-demote")
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, model.RoleMember, subs[0].Role, "carol should now be member")
}
