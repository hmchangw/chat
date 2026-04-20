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

func TestMongoStore_CountSubscriptions_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	subs := []interface{}{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1"},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1"},
		model.Subscription{ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "helper.bot"}, RoomID: "r1"},
		model.Subscription{ID: "s4", User: model.SubscriptionUser{ID: "u4", Account: "p_webhook"}, RoomID: "r1"},
		model.Subscription{ID: "s5", User: model.SubscriptionUser{ID: "u5", Account: "charlie"}, RoomID: "r2"},
	}
	if _, err := store.subscriptions.InsertMany(ctx, subs); err != nil {
		t.Fatalf("seed subscriptions: %v", err)
	}

	count, err := store.CountSubscriptions(ctx, "r1")
	if err != nil {
		t.Fatalf("CountSubscriptions: %v", err)
	}
	if count != 2 {
		t.Errorf("CountSubscriptions(r1) = %d, want 2", count)
	}

	count, err = store.CountSubscriptions(ctx, "r2")
	if err != nil {
		t.Fatalf("CountSubscriptions r2: %v", err)
	}
	if count != 1 {
		t.Errorf("CountSubscriptions(r2) = %d, want 1", count)
	}

	count, err = store.CountSubscriptions(ctx, "r999")
	if err != nil {
		t.Fatalf("CountSubscriptions r999: %v", err)
	}
	if count != 0 {
		t.Errorf("CountSubscriptions(r999) = %d, want 0", count)
	}
}

func TestMongoStore_GetRoomMembersByRooms_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	members := []interface{}{
		model.RoomMember{ID: "m1", RoomID: "r1", Ts: now, Member: model.RoomMemberEntry{ID: "alice", Type: model.RoomMemberIndividual, Account: "alice"}},
		model.RoomMember{ID: "m2", RoomID: "r1", Ts: now, Member: model.RoomMemberEntry{ID: "org1", Type: model.RoomMemberOrg}},
		model.RoomMember{ID: "m3", RoomID: "r2", Ts: now, Member: model.RoomMemberEntry{ID: "bob", Type: model.RoomMemberIndividual, Account: "bob"}},
		model.RoomMember{ID: "m4", RoomID: "r3", Ts: now, Member: model.RoomMemberEntry{ID: "charlie", Type: model.RoomMemberIndividual, Account: "charlie"}},
	}
	if _, err := store.roomMembers.InsertMany(ctx, members); err != nil {
		t.Fatalf("seed room members: %v", err)
	}

	got, err := store.GetRoomMembersByRooms(ctx, []string{"r1", "r2"})
	if err != nil {
		t.Fatalf("GetRoomMembersByRooms: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d members, want 3", len(got))
	}

	got, err = store.GetRoomMembersByRooms(ctx, []string{})
	if err != nil {
		t.Fatalf("GetRoomMembersByRooms empty: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty roomIDs, got %v", got)
	}

	got, err = store.GetRoomMembersByRooms(ctx, nil)
	if err != nil {
		t.Fatalf("GetRoomMembersByRooms nil: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nil roomIDs, got %v", got)
	}

	got, err = store.GetRoomMembersByRooms(ctx, []string{"r999"})
	if err != nil {
		t.Fatalf("GetRoomMembersByRooms non-existent: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d members for non-existent room, want 0", len(got))
	}
}

func TestMongoStore_GetAccountsByRooms_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	subs := []interface{}{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1"},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1"},
		model.Subscription{ID: "s3", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r2"},
		model.Subscription{ID: "s4", User: model.SubscriptionUser{ID: "u3", Account: "charlie"}, RoomID: "r2"},
		model.Subscription{ID: "s5", User: model.SubscriptionUser{ID: "u4", Account: "dave"}, RoomID: "r3"},
	}
	if _, err := store.subscriptions.InsertMany(ctx, subs); err != nil {
		t.Fatalf("seed subscriptions: %v", err)
	}

	accounts, err := store.GetAccountsByRooms(ctx, []string{"r1", "r2"})
	if err != nil {
		t.Fatalf("GetAccountsByRooms: %v", err)
	}
	if len(accounts) != 3 {
		t.Errorf("got %d accounts, want 3", len(accounts))
	}
	accountSet := make(map[string]bool)
	for _, a := range accounts {
		accountSet[a] = true
	}
	for _, expected := range []string{"alice", "bob", "charlie"} {
		if !accountSet[expected] {
			t.Errorf("missing account %q in results", expected)
		}
	}

	accounts, err = store.GetAccountsByRooms(ctx, []string{})
	if err != nil {
		t.Fatalf("GetAccountsByRooms empty: %v", err)
	}
	if accounts != nil {
		t.Errorf("expected nil for empty roomIDs, got %v", accounts)
	}

	accounts, err = store.GetAccountsByRooms(ctx, []string{"r999"})
	if err != nil {
		t.Fatalf("GetAccountsByRooms non-existent: %v", err)
	}
	if accounts != nil {
		t.Errorf("expected nil for non-existent room, got %v", accounts)
	}
}

func TestMongoStore_ResolveAccounts_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	users := []interface{}{
		model.User{ID: "u1", Account: "alice", SiteID: "site-a", SectID: "org1"},
		model.User{ID: "u2", Account: "bob", SiteID: "site-a", SectID: "org1"},
		model.User{ID: "u3", Account: "charlie", SiteID: "site-a", SectID: "org1"},
		model.User{ID: "u4", Account: "helper.bot", SiteID: "site-a", SectID: "org1"},
		model.User{ID: "u5", Account: "p_webhook", SiteID: "site-a", SectID: "org1"},
		model.User{ID: "u6", Account: "dave", SiteID: "site-a", SectID: "org2"},
		model.User{ID: "u7", Account: "eve", SiteID: "site-a", SectID: "org3"},
	}
	if _, err := store.users.InsertMany(ctx, users); err != nil {
		t.Fatalf("seed users: %v", err)
	}

	subs := []interface{}{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1"},
	}
	if _, err := store.subscriptions.InsertMany(ctx, subs); err != nil {
		t.Fatalf("seed subscriptions: %v", err)
	}

	accounts, err := store.ResolveAccounts(ctx, []string{"org1"}, nil, "r1")
	if err != nil {
		t.Fatalf("ResolveAccounts org1: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("got %d accounts, want 2: %v", len(accounts), accounts)
	}
	accountSet := make(map[string]bool)
	for _, a := range accounts {
		accountSet[a] = true
	}
	if !accountSet["bob"] || !accountSet["charlie"] {
		t.Errorf("expected bob and charlie, got %v", accounts)
	}

	accounts, err = store.ResolveAccounts(ctx, nil, []string{"eve"}, "r1")
	if err != nil {
		t.Fatalf("ResolveAccounts direct: %v", err)
	}
	if len(accounts) != 1 || accounts[0] != "eve" {
		t.Errorf("expected [eve], got %v", accounts)
	}

	accounts, err = store.ResolveAccounts(ctx, []string{"org2"}, []string{"dave"}, "r1")
	if err != nil {
		t.Fatalf("ResolveAccounts dedup: %v", err)
	}
	if len(accounts) != 1 || accounts[0] != "dave" {
		t.Errorf("expected [dave], got %v", accounts)
	}

	accounts, err = store.ResolveAccounts(ctx, nil, nil, "r1")
	if err != nil {
		t.Fatalf("ResolveAccounts empty: %v", err)
	}
	if accounts != nil {
		t.Errorf("expected nil for empty inputs, got %v", accounts)
	}

	accounts, err = store.ResolveAccounts(ctx, nil, []string{"alice"}, "r1")
	if err != nil {
		t.Fatalf("ResolveAccounts all existing: %v", err)
	}
	if accounts != nil {
		t.Errorf("expected nil when all accounts already members, got %v", accounts)
	}

	accounts, err = store.ResolveAccounts(ctx, nil, []string{"helper.bot", "p_webhook"}, "r1")
	if err != nil {
		t.Fatalf("ResolveAccounts bots: %v", err)
	}
	if accounts != nil {
		t.Errorf("expected nil for bot accounts, got %v", accounts)
	}
}
