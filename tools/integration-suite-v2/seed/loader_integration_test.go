//go:build integration

package seed

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mongotc "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func setupMongo(t *testing.T) (*mongo.Database, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container, err := mongotc.Run(ctx, "mongo:8")
	require.NoError(t, err)

	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err)

	db := client.Database("seed_test")
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Disconnect(ctx)
		_ = container.Terminate(ctx)
	}
	return db, cleanup
}

func TestLoadAll_PopulatesAllCollections(t *testing.T) {
	db, cleanup := setupMongo(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, LoadAll(ctx, db))

	for name, want := range map[string]int64{
		"users":         4,
		"rooms":         2,
		"subscriptions": 4,
	} {
		got, err := db.Collection(name).CountDocuments(ctx, bson.M{})
		require.NoError(t, err, "count %s", name)
		assert.Equal(t, want, got, "wrong count in %s after LoadAll", name)
	}
}

func TestLoadAll_BsonTagsLandCorrectly(t *testing.T) {
	db, cleanup := setupMongo(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, LoadAll(ctx, db))

	var alice struct {
		ID          string `bson:"_id"`
		Account     string `bson:"account"`
		EngName     string `bson:"engName"`
		ChineseName string `bson:"chineseName"`
		SiteID      string `bson:"siteId"`
	}
	err := db.Collection("users").FindOne(ctx, bson.M{"account": "alice"}).Decode(&alice)
	require.NoError(t, err)
	assert.Equal(t, "u-alice", alice.ID, "_id should be u-alice (json `id` mapping to bson `_id`)")
	assert.Equal(t, "Alice", alice.EngName)
	assert.NotEmpty(t, alice.ChineseName)
	assert.Equal(t, "site-local", alice.SiteID)

	var sub struct {
		ID   string `bson:"_id"`
		User struct {
			ID      string `bson:"_id"`
			Account string `bson:"account"`
		} `bson:"u"`
		RoomID string   `bson:"roomId"`
		Roles  []string `bson:"roles"`
	}
	err = db.Collection("subscriptions").FindOne(ctx, bson.M{"_id": "sub-alice-r-engineering"}).Decode(&sub)
	require.NoError(t, err)
	assert.Equal(t, "u-alice", sub.User.ID)
	assert.Equal(t, "alice", sub.User.Account)
	assert.Equal(t, "r-engineering", sub.RoomID)
	assert.Equal(t, []string{"owner"}, sub.Roles)
}

func TestLoadAll_Idempotent(t *testing.T) {
	db, cleanup := setupMongo(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, LoadAll(ctx, db))
	require.NoError(t, LoadAll(ctx, db), "second LoadAll must succeed without doubling")

	for name, want := range map[string]int64{
		"users":         4,
		"rooms":         2,
		"subscriptions": 4,
	} {
		got, err := db.Collection(name).CountDocuments(ctx, bson.M{})
		require.NoError(t, err)
		assert.Equal(t, want, got, "wrong count in %s after second LoadAll", name)
	}
}
