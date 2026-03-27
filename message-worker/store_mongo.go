package main

import (
	"context"
	"fmt"

	"github.com/gocql/gocql"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

type MongoStore struct {
	subscriptions *mongo.Collection
	cassSession   *gocql.Session
}

func NewMongoStore(db *mongo.Database, cassSession *gocql.Session) *MongoStore {
	return &MongoStore{
		subscriptions: db.Collection("subscriptions"),
		cassSession:   cassSession,
	}
}

func (s *MongoStore) GetSubscription(ctx context.Context, username, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"u.username": username, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		return nil, fmt.Errorf("subscription not found: %w", err)
	}
	return &sub, nil
}

func (s *MongoStore) SaveMessage(ctx context.Context, msg *model.Message) error {
	return s.cassSession.Query(
		`INSERT INTO messages (room_id, created_at, id, user_id, content) VALUES (?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, msg.UserID, msg.Content,
	).WithContext(ctx).Exec()
}
