package mongorepo

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

// Repository implements service.SubscriptionRepository using MongoDB.
type Repository struct {
	subscriptions *Collection[model.Subscription]
}

// NewRepository creates a new MongoDB repository.
func NewRepository(db *mongo.Database) *Repository {
	return &Repository{
		subscriptions: NewCollection[model.Subscription](db.Collection("subscriptions")),
	}
}

// GetSubscription returns the subscription for a user in a room.
// Returns (nil, nil) when the user is not subscribed.
func (r *Repository) GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error) {
	return r.subscriptions.FindOne(ctx, bson.D{
		{Key: "userId", Value: userID},
		{Key: "roomId", Value: roomID},
	})
}
