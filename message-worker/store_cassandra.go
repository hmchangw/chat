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

// UnmarshalUDT implements gocql.UDTUnmarshaler for cassParticipant.
// Required to scan SET<FROZEN<"Participant">> columns back from Cassandra.
func (p *cassParticipant) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	switch name {
	case "id":
		return gocql.Unmarshal(info, data, &p.ID)
	case "eng_name":
		return gocql.Unmarshal(info, data, &p.EngName)
	case "company_name":
		return gocql.Unmarshal(info, data, &p.CompanyName)
	case "account":
		return gocql.Unmarshal(info, data, &p.Account)
	case "app_id":
		return gocql.Unmarshal(info, data, &p.AppID)
	case "app_name":
		return gocql.Unmarshal(info, data, &p.AppName)
	case "is_bot":
		return gocql.Unmarshal(info, data, &p.IsBot)
	default:
		return nil
	}
}

// toMentionSet converts []model.Participant to []*cassParticipant for binding
// to a Cassandra SET<FROZEN<"Participant">> column.
func toMentionSet(mentions []model.Participant) []*cassParticipant {
	if len(mentions) == 0 {
		return nil
	}
	result := make([]*cassParticipant, len(mentions))
	for i, m := range mentions {
		result[i] = &cassParticipant{
			ID:          m.UserID,
			EngName:     m.EngName,
			CompanyName: m.ChineseName,
			Account:     m.Account,
		}
	}
	return result
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
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, sender, msg.Content, siteID, msg.CreatedAt, toMentionSet(msg.Mentions),
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_room %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO messages_by_id (message_id, created_at, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, sender, msg.Content, siteID, msg.CreatedAt, toMentionSet(msg.Mentions),
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
		`INSERT INTO messages_by_id (message_id, created_at, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, sender, msg.Content, siteID, msg.CreatedAt, toMentionSet(msg.Mentions),
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_id %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, thread_message_id, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.ThreadParentMessageID, msg.CreatedAt, msg.ID, msg.ID, sender, msg.Content, siteID, msg.CreatedAt, toMentionSet(msg.Mentions),
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert thread_messages_by_room %s: %w", msg.ID, err)
	}

	return nil
}

// GetMessageSender reads the sender UDT from messages_by_id for the given message ID.
// Returns an error if the message does not exist.
func (s *CassandraStore) GetMessageSender(ctx context.Context, messageID string) (*cassParticipant, error) {
	var sender cassParticipant
	if err := s.cassSession.Query(
		`SELECT sender FROM messages_by_id WHERE message_id = ? LIMIT 1`,
		messageID,
	).WithContext(ctx).Scan(&sender); err != nil {
		return nil, fmt.Errorf("get sender for message %s: %w", messageID, err)
	}
	return &sender, nil
}
