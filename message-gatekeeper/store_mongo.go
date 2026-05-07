package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

type MongoStore struct {
	subscriptions *mongo.Collection
	rooms         *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		subscriptions: db.Collection("subscriptions"),
		rooms:         db.Collection("rooms"),
	}
}

func (s *MongoStore) GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"u.account": account, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("user %s not subscribed to room %s: %w", account, roomID, errNotSubscribed)
		}
		return nil, fmt.Errorf("find subscription for user %s in room %s: %w", account, roomID, err)
	}
	return &sub, nil
}

// GetRoom fetches a room document by its ID. Any error (including
// mongo.ErrNoDocuments) is wrapped and returned — the handler treats every
// failure here as an infrastructure error, since reaching this call already
// implies a subscription for the room exists.
func (s *MongoStore) GetRoom(ctx context.Context, roomID string) (*model.Room, error) {
	var room model.Room
	if err := s.rooms.FindOne(ctx, bson.M{"_id": roomID}).Decode(&room); err != nil {
		return nil, fmt.Errorf("find room %q: %w", roomID, err)
	}
	return &room, nil
}
