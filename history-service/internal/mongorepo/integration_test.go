//go:build integration

package mongorepo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:8")
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err)
	t.Cleanup(func() { client.Disconnect(ctx) })
	return client.Database("chat_test")
}

func TestSubscriptionRepo_GetSubscription(t *testing.T) {
	db := setupMongo(t)
	repo := NewSubscriptionRepo(db)
	ctx := context.Background()

	joinTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID:     "s1",
		User:   model.SubscriptionUser{ID: "u1", Username: "u1"},
		RoomID: "r1", SiteID: "site-local",
		Role: model.RoleMember, HistorySharedSince: &joinTime, JoinedAt: joinTime,
	})
	require.NoError(t, err)

	sub, err := repo.GetSubscription(ctx, "u1", "r1")
	require.NoError(t, err)
	require.NotNil(t, sub)
	assert.Equal(t, "u1", sub.User.ID)
	assert.Equal(t, "r1", sub.RoomID)
	require.NotNil(t, sub.HistorySharedSince)
	assert.Equal(t, joinTime.UTC(), sub.HistorySharedSince.UTC())
}

func TestSubscriptionRepo_GetSubscription_NotFound(t *testing.T) {
	db := setupMongo(t)
	repo := NewSubscriptionRepo(db)
	ctx := context.Background()

	sub, err := repo.GetSubscription(ctx, "nonexistent", "r1")
	require.NoError(t, err)
	assert.Nil(t, sub)
}

func TestSubscriptionRepo_GetHistorySharedSince_NilHSS(t *testing.T) {
	db := setupMongo(t)
	repo := NewSubscriptionRepo(db)
	ctx := context.Background()

	// Insert subscription with no HistorySharedSince (owner — full history access)
	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID:     "s2",
		User:   model.SubscriptionUser{ID: "owner", Username: "owner"},
		RoomID: "r1", SiteID: "site-local",
		Role: model.RoleOwner, JoinedAt: time.Now(),
	})
	require.NoError(t, err)

	hss, err := repo.GetHistorySharedSince(ctx, "owner", "r1")
	require.NoError(t, err)
	assert.Nil(t, hss) // nil means no lower-bound restriction
}

func TestSubscriptionRepo_GetHistorySharedSince_WithHSS(t *testing.T) {
	db := setupMongo(t)
	repo := NewSubscriptionRepo(db)
	ctx := context.Background()

	joinTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID:     "s3",
		User:   model.SubscriptionUser{ID: "u2", Username: "u2"},
		RoomID: "r2", SiteID: "site-local",
		Role: model.RoleMember, HistorySharedSince: &joinTime, JoinedAt: joinTime,
	})
	require.NoError(t, err)

	hss, err := repo.GetHistorySharedSince(ctx, "u2", "r2")
	require.NoError(t, err)
	require.NotNil(t, hss)
	assert.Equal(t, joinTime.UTC(), hss.UTC())
}

func TestSubscriptionRepo_GetHistorySharedSince_NotSubscribed(t *testing.T) {
	db := setupMongo(t)
	repo := NewSubscriptionRepo(db)
	ctx := context.Background()

	hss, err := repo.GetHistorySharedSince(ctx, "nobody", "r1")
	require.NoError(t, err)
	assert.Nil(t, hss)
}

// --- Collection[T] integration tests ---

type testDoc struct {
	ID   string `bson:"_id"`
	Name string `bson:"name"`
	Age  int    `bson:"age"`
}

func TestCollection_FindOne_Success(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()
	col := NewCollection[testDoc](db.Collection("test_docs"))

	_, err := db.Collection("test_docs").InsertOne(ctx, testDoc{ID: "d1", Name: "Alice", Age: 30})
	require.NoError(t, err)

	result, err := col.FindOne(ctx, bson.M{"name": "Alice"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "d1", result.ID)
	assert.Equal(t, "Alice", result.Name)
}

func TestCollection_FindOne_NotFound_ReturnsNilNil(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()
	col := NewCollection[testDoc](db.Collection("test_docs"))

	result, err := col.FindOne(ctx, bson.M{"name": "Nobody"})
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestCollection_FindByID(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()
	col := NewCollection[testDoc](db.Collection("test_docs"))

	_, err := db.Collection("test_docs").InsertOne(ctx, testDoc{ID: "d1", Name: "Bob", Age: 25})
	require.NoError(t, err)

	result, err := col.FindByID(ctx, "d1")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "Bob", result.Name)
}

func TestCollection_FindByID_NotFound(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()
	col := NewCollection[testDoc](db.Collection("test_docs"))

	result, err := col.FindByID(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestCollection_FindMany_Success(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()
	col := NewCollection[testDoc](db.Collection("test_docs"))

	_, err := db.Collection("test_docs").InsertMany(ctx, []interface{}{
		testDoc{ID: "d1", Name: "Alice", Age: 30},
		testDoc{ID: "d2", Name: "Bob", Age: 25},
		testDoc{ID: "d3", Name: "Charlie", Age: 35},
	})
	require.NoError(t, err)

	results, err := col.FindMany(ctx, bson.M{"age": bson.M{"$gte": 30}})
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestCollection_FindMany_Empty_ReturnsEmptySlice(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()
	col := NewCollection[testDoc](db.Collection("test_docs"))

	results, err := col.FindMany(ctx, bson.M{"name": "Nobody"})
	require.NoError(t, err)
	assert.NotNil(t, results)
	assert.Empty(t, results)
}

func TestCollection_Raw(t *testing.T) {
	db := setupMongo(t)
	col := NewCollection[testDoc](db.Collection("test_docs"))

	raw := col.Raw()
	assert.Equal(t, "test_docs", raw.Name())
}
