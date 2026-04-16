package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
)

// errMessageNotFound is returned by GetMessageSender when the message row is
// missing from Cassandra. Handler code checks for this sentinel to ack-and-skip
// instead of NAK'ing (which would cause infinite JetStream redelivery).
var errMessageNotFound = errors.New("message not found")

// cassParticipant maps to the Cassandra "Participant" UDT.
// cql struct tags tell gocql's reflection-based UDT marshaler how to map each
// Go field to its Cassandra UDT field name. Without these tags, gocql would
// lowercase the Go field names (e.g. "EngName" → "engname") which would not
// match the snake_case UDT fields (e.g. "eng_name").
type cassParticipant struct {
	ID          string `cql:"id"`
	EngName     string `cql:"eng_name"`
	CompanyName string `cql:"company_name"` // ChineseName
	Account     string `cql:"account"`
	AppID       string `cql:"app_id"`
	AppName     string `cql:"app_name"`
	IsBot       bool   `cql:"is_bot"`
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
// threadRoomID is the ID of the ThreadRoom document that anchors this thread (created or
// fetched by handleThreadRoomAndSubscriptions before this call).
// If either insert fails the error is returned immediately; JetStream will redeliver the message.
func (s *CassandraStore) SaveThreadMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string, threadRoomID string) error {
	if err := s.cassSession.Query(
		`INSERT INTO messages_by_id
		 (message_id, created_at, sender, msg, site_id, updated_at, mentions,
		  thread_room_id, thread_parent_id, thread_parent_created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, sender, msg.Content, siteID, msg.CreatedAt, toMentionSet(msg.Mentions),
		threadRoomID, msg.ThreadParentMessageID, msg.ThreadParentMessageCreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_id %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO thread_messages_by_room
		 (room_id, thread_room_id, created_at, message_id, thread_parent_id, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, threadRoomID, msg.CreatedAt, msg.ID, msg.ThreadParentMessageID,
		sender, msg.Content, siteID, msg.CreatedAt, toMentionSet(msg.Mentions),
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
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("get sender for message %s: %w", messageID, errMessageNotFound)
		}
		return nil, fmt.Errorf("get sender for message %s: %w", messageID, err)
	}
	return &sender, nil
}
