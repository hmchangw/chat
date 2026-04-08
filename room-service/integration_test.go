//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/model"
)

func TestMongoStore_Integration(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Test CreateRoom and GetRoom
	room := model.Room{ID: "r1", Name: "general", Type: model.RoomTypeGroup, SiteID: "site-a", CreatedBy: "u1"}
	require.NoError(t, store.CreateRoom(ctx, &room))

	got, err := store.GetRoom(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, "general", got.Name)

	// Test ListRooms
	require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "r2", Name: "random", Type: model.RoomTypeGroup}))

	rooms, err := store.ListRooms(ctx)
	require.NoError(t, err)
	assert.Len(t, rooms, 2)

	// Test CreateSubscription and GetSubscription
	sub := model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1"}, RoomID: "r1", Role: model.RoleOwner}
	require.NoError(t, store.CreateSubscription(ctx, &sub))

	gotSub, err := store.GetSubscription(ctx, "u1", "r1")
	require.NoError(t, err)
	assert.Equal(t, model.RoleOwner, gotSub.Role)

	// Test not found
	_, err = store.GetSubscription(ctx, "u2", "r1")
	assert.Error(t, err, "expected error for missing subscription")
}

func TestMongoStore_RoomMembers(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Empty result for a room with no members
	members, err := store.GetRoomMembers(ctx, "r-unknown")
	require.NoError(t, err)
	assert.Empty(t, members)

	// Insert members directly into MongoDB (room-service only reads room_members, doesn't write them)
	_, err = db.Collection("room_members").InsertMany(ctx, []interface{}{
		model.RoomMember{ID: "m1", RoomID: "r1", Member: model.RoomMemberEntry{ID: "org-eng", Type: model.RoomMemberTypeOrg}},
		model.RoomMember{ID: "m2", RoomID: "r1", Member: model.RoomMemberEntry{ID: "user-alice", Type: model.RoomMemberTypeIndividual}},
	})
	require.NoError(t, err)

	// Retrieve and verify
	members, err = store.GetRoomMembers(ctx, "r1")
	require.NoError(t, err)
	assert.Len(t, members, 2)

	// Different room is unaffected
	other, err := store.GetRoomMembers(ctx, "r2")
	require.NoError(t, err)
	assert.Empty(t, other)
}

func TestMongoStore_GetOrgAccounts(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed hr_data collection with individual account entries
	_, err := db.Collection("hr_data").InsertMany(ctx, []interface{}{
		bson.M{"sectId": "org-eng", "accountName": "alice"},
		bson.M{"sectId": "org-eng", "accountName": "bob"},
	})
	require.NoError(t, err)

	accounts, err := store.GetOrgAccounts(ctx, "org-eng")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"alice", "bob"}, accounts)

	// Unknown org returns empty slice, not error
	unknown, err := store.GetOrgAccounts(ctx, "org-unknown")
	require.NoError(t, err)
	assert.Empty(t, unknown)
}

func TestMongoStore_CountSubscriptions(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Empty room
	count, err := store.CountSubscriptions(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Seed subscriptions individually (BulkCreateSubscriptions no longer in room-service store)
	subs := []model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{Account: "alice"}, RoomID: "r1", SiteID: "site-a", Role: model.RoleMember},
		{ID: "s2", User: model.SubscriptionUser{Account: "bob"}, RoomID: "r1", SiteID: "site-a", Role: model.RoleMember},
		// bot usernames -- must be excluded
		{ID: "s3", User: model.SubscriptionUser{Account: "notify.bot"}, RoomID: "r1", SiteID: "site-a", Role: model.RoleMember},
		{ID: "s4", User: model.SubscriptionUser{Account: "p_webhook"}, RoomID: "r1", SiteID: "site-a", Role: model.RoleMember},
	}
	for i := range subs {
		require.NoError(t, store.CreateSubscription(ctx, &subs[i]))
	}

	count, err = store.CountSubscriptions(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 2, count, "bots should be excluded")
}

func TestMongoStore_CountOwners(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Empty room
	count, err := store.CountOwners(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	subs := []model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{Account: "alice"}, RoomID: "r1", Role: model.RoleOwner},
		{ID: "s2", User: model.SubscriptionUser{Account: "bob"}, RoomID: "r1", Role: model.RoleMember},
	}
	for i := range subs {
		require.NoError(t, store.CreateSubscription(ctx, &subs[i]))
	}

	count, err = store.CountOwners(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestMongoStore_ListSubscriptionsByRoom(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Empty room returns empty slice, not error
	subs, err := store.ListSubscriptionsByRoom(ctx, "r-unknown")
	require.NoError(t, err)
	assert.Empty(t, subs)

	seed := []model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{Account: "alice"}, RoomID: "r1", SiteID: "site-a", Role: model.RoleMember},
		{ID: "s2", User: model.SubscriptionUser{Account: "bob"}, RoomID: "r1", SiteID: "site-a", Role: model.RoleMember},
		{ID: "s3", User: model.SubscriptionUser{Account: "carol"}, RoomID: "r2", SiteID: "site-a", Role: model.RoleMember},
	}
	for i := range seed {
		require.NoError(t, store.CreateSubscription(ctx, &seed[i]))
	}

	subs, err = store.ListSubscriptionsByRoom(ctx, "r1")
	require.NoError(t, err)
	assert.Len(t, subs, 2)

	// Different room unaffected
	subs, err = store.ListSubscriptionsByRoom(ctx, "r2")
	require.NoError(t, err)
	assert.Len(t, subs, 1)
}

func TestMongoStore_EnsureIndexes(t *testing.T) {
	db := freshDB(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	require.NoError(t, store.EnsureIndexes(ctx))

	// Idempotent: calling again should not error
	require.NoError(t, store.EnsureIndexes(ctx))
}
