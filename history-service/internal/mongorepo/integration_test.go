//go:build integration

package mongorepo

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

func TestRepository_GetSubscription(t *testing.T) {
	db := setupMongo(t)
	repo := NewRepository(db)
	ctx := context.Background()

	joinTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID: "s1", UserID: "u1", RoomID: "r1", SiteID: "site-local",
		Role: model.RoleMember, SharedHistorySince: joinTime, JoinedAt: joinTime,
	})
	require.NoError(t, err)

	sub, err := repo.GetSubscription(ctx, "u1", "r1")
	require.NoError(t, err)
	require.NotNil(t, sub)
	assert.Equal(t, "u1", sub.UserID)
	assert.Equal(t, "r1", sub.RoomID)
	assert.Equal(t, joinTime.UTC(), sub.SharedHistorySince.UTC())
}

func TestRepository_GetSubscription_NotFound(t *testing.T) {
	db := setupMongo(t)
	repo := NewRepository(db)
	ctx := context.Background()

	sub, err := repo.GetSubscription(ctx, "nonexistent", "r1")
	require.NoError(t, err)
	assert.Nil(t, sub)
}
