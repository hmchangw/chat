package main

import (
	"context"
	"fmt"

	"github.com/gocql/gocql"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

type MongoStore struct {
	cassSession *gocql.Session
}

func NewMongoStore(db *mongo.Database, cassSession *gocql.Session) *MongoStore {
	return &MongoStore{
		cassSession: cassSession,
	}
}

func (s *MongoStore) SaveMessage(ctx context.Context, msg *model.Message) error {
	if err := s.cassSession.Query(
		`INSERT INTO messages (room_id, created_at, id, user_id, content) VALUES (?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, msg.UserID, msg.Content,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert message %s: %w", msg.ID, err)
	}
	return nil
}
