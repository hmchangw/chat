package mongorepo

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

// SubscriptionRepo implements service.SubscriptionRepository using MongoDB.
type SubscriptionRepo struct {
	subscriptions *Collection[model.Subscription]
}

// NewSubscriptionRepo creates a new MongoDB subscription repository.
func NewSubscriptionRepo(db *mongo.Database) *SubscriptionRepo {
	return &SubscriptionRepo{
		subscriptions: NewCollection[model.Subscription](db.Collection("subscriptions")),
	}
}

// GetSubscription returns the subscription for a user in a room.
// Returns (nil, nil) when the user is not subscribed.
func (r *SubscriptionRepo) GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error) {
	return r.subscriptions.FindOne(ctx, bson.M{"userId": userID, "roomId": roomID})
}
