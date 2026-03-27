package main

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

type RealStore struct {
	subscriptions *mongo.Collection
	cassSession   *gocql.Session
}

func NewRealStore(db *mongo.Database, cassSession *gocql.Session) *RealStore {
	return &RealStore{
		subscriptions: db.Collection("subscriptions"),
		cassSession:   cassSession,
	}
}

func (s *RealStore) GetSubscription(ctx context.Context, username, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"u.username": username, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		return nil, fmt.Errorf("subscription not found: %w", err)
	}
	return &sub, nil
}

func (s *RealStore) ListMessages(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error) {
	query := `SELECT id, room_id, user_id, content, created_at FROM messages
		WHERE room_id = ? AND created_at > ? AND created_at < ?
		ORDER BY created_at DESC LIMIT ?`

	iter := s.cassSession.Query(query, roomID, since, before, limit).WithContext(ctx).Iter()

	var messages []model.Message
	var msg model.Message
	for iter.Scan(&msg.ID, &msg.RoomID, &msg.UserID, &msg.Content, &msg.CreatedAt) {
		messages = append(messages, msg)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	return messages, nil
}
