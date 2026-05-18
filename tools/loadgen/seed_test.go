//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/mongoutil"
)

// TestSeedAndTeardown exercises seed.go end-to-end against a real
// MongoDB via testcontainers. Verifies (1) Seed inserts the expected
// documents in users/rooms/subscriptions, (2) Seed is idempotent, and
// (3) Teardown drops the seeded collections.
func TestSeedAndTeardown(t *testing.T) {
	ctx := context.Background()
	mongoURI, stop := setupMongo(t)
	defer stop()

	client, err := mongoutil.Connect(ctx, mongoURI, "", "")
	require.NoError(t, err)
	defer mongoutil.Disconnect(ctx, client)
	db := client.Database("chat_test")

	preset, _ := BuiltinPreset("small")
	fixtures := BuildFixtures(&preset, 42, "site-test")

	// First Seed populates the collections.
	require.NoError(t, Seed(ctx, db, &fixtures))

	count, err := db.Collection("users").CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, int64(len(fixtures.Users)), count)

	count, err = db.Collection("rooms").CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, int64(len(fixtures.Rooms)), count)

	count, err = db.Collection("subscriptions").CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, int64(len(fixtures.Subscriptions)), count)

	// Second Seed must be idempotent (no duplicate-key errors).
	require.NoError(t, Seed(ctx, db, &fixtures))
	count, err = db.Collection("rooms").CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, int64(len(fixtures.Rooms)), count, "Seed must be idempotent")

	// Teardown drops the seeded collections.
	require.NoError(t, Teardown(ctx, db))
	for _, coll := range []string{"users", "rooms", "subscriptions"} {
		count, err := db.Collection(coll).CountDocuments(ctx, bson.M{})
		require.NoError(t, err)
		assert.Zero(t, count, "Teardown must empty %s", coll)
	}
}
