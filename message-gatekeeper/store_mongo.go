package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

type MongoStore struct {
	subscriptions *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{subscriptions: db.Collection("subscriptions")}
}

func (s *MongoStore) GetSubscription(ctx context.Context, username, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"u.username": username, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		return nil, fmt.Errorf("find subscription for user %s in room %s: %w", username, roomID, err)
	}
	return &sub, nil
}
