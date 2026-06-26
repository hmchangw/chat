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
		{"account": "alice", "employeeId": "E001", "siteId": "site-a"},
		{"account": "bob", "employeeId": "E002", "siteId": "site-b"},
		{"account": "carol", "employeeId": "E003", "siteId": "site-a"},
		{"account": "eve", "employeeId": "E004", "siteId": "site-a"},
	} {
		_, err := db.Collection("hr_employee").InsertOne(ctx, doc)
		require.NoError(t, err)
	}
	// alice/bob are provisioned on their home site; carol has no users row; eve's
	// users row is on a different site; dave is only in users.
	_, err := db.Collection("users").InsertMany(ctx, []any{
		bson.M{"_id": "u-alice", "account": "alice", "siteId": "site-a"},
		bson.M{"_id": "u-bob", "account": "bob", "siteId": "site-b"},
		bson.M{"_id": "u-eve", "account": "eve", "siteId": "site-b"},
		bson.M{"_id": "u-dave", "account": "dave", "siteId": "site-a"},
	})
	require.NoError(t, err)

	emps, err := store.ListEmployees(ctx)
	require.NoError(t, err)

	byAccount := make(map[string]employee, len(emps))
	for _, e := range emps {
		byAccount[e.Account] = e
	}
	// Only accounts present in BOTH collections for the same site survive the
	// $lookup, with users._id projected as UserID.
	assert.Equal(t, employee{
		Account: "alice", EmployeeID: "E001", SiteID: "site-a", UserID: "u-alice",
	}, byAccount["alice"])
	assert.Equal(t, employee{
		Account: "bob", EmployeeID: "E002", SiteID: "site-b", UserID: "u-bob",
	}, byAccount["bob"])
	require.Len(t, emps, 2, "carol (no users row), eve (site mismatch), and dave (users-only) must be excluded")
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
	_, err := coll.InsertOne(ctx, bson.M{"account": "alice", "employeeId": "E001", "siteId": "site-a"})
	require.NoError(t, err)

	_, err = coll.InsertOne(ctx, bson.M{"account": "alice", "employeeId": "E099", "siteId": "site-b"})
	require.Error(t, err, "a second row for the same account must be rejected at write time")
	assert.True(t, mongo.IsDuplicateKeyError(err))

	// A distinct account is unaffected by the unique index.
	_, err = coll.InsertOne(ctx, bson.M{"account": "bob", "employeeId": "E002", "siteId": "site-b"})
	require.NoError(t, err)
}
