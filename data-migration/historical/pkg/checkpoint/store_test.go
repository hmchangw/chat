// data-migration/historical/pkg/checkpoint/store_test.go
package checkpoint_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/data-migration/historical/pkg/checkpoint"
)

func TestMongoStore_LoadReturnsEmptyWhenAbsent(t *testing.T) {
	coll := newTestColl(t)
	store := checkpoint.NewMongoStore(coll)
	lastID, err := store.Load(context.Background(), "test-job")
	require.NoError(t, err)
	assert.Empty(t, lastID)
}

func TestMongoStore_SaveAndLoad(t *testing.T) {
	coll := newTestColl(t)
	store := checkpoint.NewMongoStore(coll)
	ctx := context.Background()

	err := store.Save(ctx, "test-job", "abc123")
	require.NoError(t, err)

	lastID, err := store.Load(ctx, "test-job")
	require.NoError(t, err)
	assert.Equal(t, "abc123", lastID)
}

func TestMongoStore_SaveOverwritesPrevious(t *testing.T) {
	coll := newTestColl(t)
	store := checkpoint.NewMongoStore(coll)
	ctx := context.Background()

	require.NoError(t, store.Save(ctx, "test-job", "first"))
	require.NoError(t, store.Save(ctx, "test-job", "second"))

	lastID, err := store.Load(ctx, "test-job")
	require.NoError(t, err)
	assert.Equal(t, "second", lastID)
}

func TestMongoStore_IsolatedByJobName(t *testing.T) {
	coll := newTestColl(t)
	store := checkpoint.NewMongoStore(coll)
	ctx := context.Background()

	require.NoError(t, store.Save(ctx, "job-a", "idA"))
	require.NoError(t, store.Save(ctx, "job-b", "idB"))

	idA, _ := store.Load(ctx, "job-a")
	idB, _ := store.Load(ctx, "job-b")
	assert.Equal(t, "idA", idA)
	assert.Equal(t, "idB", idB)
}

func newTestColl(t *testing.T) *mongo.Collection {
	t.Helper()
	client, err := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		t.Skipf("no local MongoDB: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	db := client.Database("migration_test_" + t.Name())
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	return db.Collection("checkpoints")
}
