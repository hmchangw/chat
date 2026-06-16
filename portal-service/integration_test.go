//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/testutil"
)

func TestMain(m *testing.M) { testutil.RunTests(m) }

func TestMongoDirectoryStore_ListEmployees(t *testing.T) {
	db := testutil.MongoDB(t, "portal")
	store := newMongoDirectoryStore(db)
	ctx := context.Background()

	for _, doc := range []bson.M{
		{"account": "alice", "employeeId": "E001", "siteId": "site-a", "natsUrl": "wss://nats-3.site-a.example.com"},
		{"account": "bob", "employeeId": "E002", "siteId": "site-b", "natsUrl": "wss://nats.site-b.example.com"},
	} {
		_, err := db.Collection("hr_employee").InsertOne(ctx, doc)
		require.NoError(t, err)
	}

	emps, err := store.ListEmployees(ctx)
	require.NoError(t, err)
	require.Len(t, emps, 2)

	byAccount := make(map[string]employee, len(emps))
	for _, e := range emps {
		byAccount[e.Account] = e
	}
	assert.Equal(t, employee{
		Account:    "alice",
		EmployeeID: "E001",
		SiteID:     "site-a",
		NATSURL:    "wss://nats-3.site-a.example.com",
	}, byAccount["alice"])
	assert.Equal(t, employee{
		Account:    "bob",
		EmployeeID: "E002",
		SiteID:     "site-b",
		NATSURL:    "wss://nats.site-b.example.com",
	}, byAccount["bob"])
}

func TestMongoDirectoryStore_ListEmployees_Empty(t *testing.T) {
	db := testutil.MongoDB(t, "portal")
	store := newMongoDirectoryStore(db)

	emps, err := store.ListEmployees(context.Background())
	require.NoError(t, err)
	assert.Empty(t, emps)
}

func TestMongoDirectoryStore_EnsureIndexes_UniqueAccount(t *testing.T) {
	db := testutil.MongoDB(t, "portal")
	store := newMongoDirectoryStore(db)
	ctx := context.Background()

	require.NoError(t, store.EnsureIndexes(ctx))
	require.NoError(t, store.EnsureIndexes(ctx), "EnsureIndexes must be idempotent")

	coll := db.Collection("hr_employee")
	_, err := coll.InsertOne(ctx, bson.M{"account": "alice", "employeeId": "E001", "siteId": "site-a", "natsUrl": "wss://nats.site-a.example.com"})
	require.NoError(t, err)

	_, err = coll.InsertOne(ctx, bson.M{"account": "alice", "employeeId": "E099", "siteId": "site-b", "natsUrl": "wss://nats.site-b.example.com"})
	require.Error(t, err, "a second row for the same account must be rejected at write time")
	assert.True(t, mongo.IsDuplicateKeyError(err))

	// A distinct account is unaffected by the unique index.
	_, err = coll.InsertOne(ctx, bson.M{"account": "bob", "employeeId": "E002", "siteId": "site-b", "natsUrl": "wss://nats.site-b.example.com"})
	require.NoError(t, err)
}

func TestMongoDirectoryStore_AccountProvisioned(t *testing.T) {
	db := testutil.MongoDB(t, "portal")
	store := newMongoDirectoryStore(db)
	ctx := context.Background()

	_, err := db.Collection("users").InsertMany(ctx, []any{
		bson.M{"_id": "u-alice", "account": "alice", "siteId": "site-a"},
		bson.M{"_id": "u-ivan", "account": "ivan", "siteId": "site-b"},
	})
	require.NoError(t, err)

	tests := []struct {
		name    string
		account string
		siteID  string
		want    bool
	}{
		{"provisioned on this site", "alice", "site-a", true},
		// ivan exists in users but is homed on site-b; the compound predicate
		// must not match him for site-a.
		{"homed on another site", "ivan", "site-a", false},
		{"unknown account", "carol", "site-a", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.AccountProvisioned(ctx, tt.account, tt.siteID)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
