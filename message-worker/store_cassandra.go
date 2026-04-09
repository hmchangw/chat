package main

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
)

type CassandraStore struct {
	cassSession *gocql.Session
}

func NewCassandraStore(session *gocql.Session) *CassandraStore {
	return &CassandraStore{
		cassSession: session,
	}
}

func (s *CassandraStore) SaveMessage(ctx context.Context, msg model.Message) error { //nolint:gocritic // value receiver per Store interface contract
	if err := s.cassSession.Query(
		`INSERT INTO messages (room_id, created_at, id, user_id, content) VALUES (?, ?, ?, ?, ?)`,
		msg.RoomID, time.UnixMilli(msg.CreatedAt).UTC(), msg.ID, msg.UserID, msg.Content,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert message %s: %w", msg.ID, err)
	}
	return nil
}
