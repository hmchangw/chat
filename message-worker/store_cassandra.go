package main

import (
	"context"
	"fmt"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
)

// cassParticipant maps to the Cassandra "Participant" UDT.
type cassParticipant struct {
	ID          string
	EngName     string
	CompanyName string // ChineseName
	Account     string
	AppID       string
	AppName     string
	IsBot       bool
}

// MarshalUDT implements gocql.UDTMarshaler for cassParticipant.
func (p *cassParticipant) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "id":
		return gocql.Marshal(info, p.ID)
	case "eng_name":
		return gocql.Marshal(info, p.EngName)
	case "company_name":
		return gocql.Marshal(info, p.CompanyName)
	case "account":
		return gocql.Marshal(info, p.Account)
	case "app_id":
		return gocql.Marshal(info, p.AppID)
	case "app_name":
		return gocql.Marshal(info, p.AppName)
	case "is_bot":
		return gocql.Marshal(info, p.IsBot)
	default:
		return nil, nil
	}
}

// CassandraStore implements Store using a Cassandra session.
type CassandraStore struct {
	cassSession *gocql.Session
}

func NewCassandraStore(session *gocql.Session) *CassandraStore {
	return &CassandraStore{cassSession: session}
}

// SaveMessage inserts msg into both messages_by_room and messages_by_id.
// updated_at is set to msg.CreatedAt (equals created_at on first insert — not yet edited).
// If either insert fails the error is returned immediately; JetStream will redeliver the message.
func (s *CassandraStore) SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error {
	if err := s.cassSession.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, site_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, sender, msg.Content, siteID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_room %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO messages_by_id (message_id, created_at, sender, msg, site_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, sender, msg.Content, siteID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_id %s: %w", msg.ID, err)
	}

	return nil
}

// SaveThreadMessage inserts a thread reply into messages_by_id and thread_messages_by_room.
// thread_room_id is the parent message ID that anchors the thread.
// If either insert fails the error is returned immediately; JetStream will redeliver the message.
func (s *CassandraStore) SaveThreadMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error {
	if err := s.cassSession.Query(
		`INSERT INTO messages_by_id (message_id, created_at, sender, msg, site_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, sender, msg.Content, siteID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_id %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, thread_message_id, sender, msg, site_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.ThreadParentMessageID, msg.CreatedAt, msg.ID, msg.ID, sender, msg.Content, siteID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert thread_messages_by_room %s: %w", msg.ID, err)
	}

	return nil
}
