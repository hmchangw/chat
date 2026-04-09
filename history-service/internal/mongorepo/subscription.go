package mongorepo

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

const subscriptionsCollection = "subscriptions"

// SubscriptionRepo implements service.SubscriptionRepository using MongoDB.
type SubscriptionRepo struct {
	subscriptions *Collection[model.Subscription]
}

// NewSubscriptionRepo creates a new MongoDB subscription repository.
func NewSubscriptionRepo(db *mongo.Database) *SubscriptionRepo {
	return &SubscriptionRepo{
		subscriptions: NewCollection[model.Subscription](db.Collection(subscriptionsCollection)),
	}
}

// GetSubscription returns the full subscription for a user in a room.
// Returns (nil, nil) when the user is not subscribed.
func (r *SubscriptionRepo) GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	return r.subscriptions.FindOne(ctx, bson.M{"u.account": account, "roomId": roomID})
}

// GetHistorySharedSince returns the HistorySharedSince timestamp for a subscription.
// Returns (nil, true, nil) when subscribed but HistorySharedSince is zero (owner — full history access).
// Returns (&t, true, nil) when subscribed with a restriction.
// Returns (nil, false, nil) when not subscribed.
func (r *SubscriptionRepo) GetHistorySharedSince(ctx context.Context, account, roomID string) (*time.Time, bool, error) {
	sub, err := r.subscriptions.FindOne(ctx,
		bson.M{"u.account": account, "roomId": roomID},
		WithProjection(bson.M{"historySharedSince": 1, "_id": 0}),
	)
	if err != nil {
		return nil, false, err
	}
	if sub == nil {
		return nil, false, nil
	}
	if sub.HistorySharedSince == nil || sub.HistorySharedSince.IsZero() {
		return nil, true, nil
	}
	return sub.HistorySharedSince, true, nil
}
