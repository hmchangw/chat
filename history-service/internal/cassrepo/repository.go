package cassrepo

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
)

// Repository implements service.MessageRepository using Cassandra.
type Repository struct {
	session *gocql.Session
}

// NewRepository creates a new Cassandra repository.
func NewRepository(session *gocql.Session) *Repository {
	return &Repository{session: session}
}

// GetMessagesBefore returns messages before `before` and after `since`, newest-first.
func (r *Repository) GetMessagesBefore(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error) {
	stmt := `SELECT id, room_id, user_id, content, created_at FROM messages
		WHERE room_id = ? AND created_at > ? AND created_at < ?
		ORDER BY created_at DESC LIMIT ?`

	iter := r.session.Query(stmt, roomID, since, before, limit).WithContext(ctx).Iter()

	var messages []model.Message
	var msg model.Message
	for iter.Scan(&msg.ID, &msg.RoomID, &msg.UserID, &msg.Content, &msg.CreatedAt) {
		messages = append(messages, msg)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("querying messages before: %w", err)
	}
	return messages, nil
}

// GetMessagesAfter returns messages after `after`, oldest-first.
func (r *Repository) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, limit int) ([]model.Message, error) {
	stmt := `SELECT id, room_id, user_id, content, created_at FROM messages
		WHERE room_id = ? AND created_at > ?
		ORDER BY created_at ASC LIMIT ?`

	iter := r.session.Query(stmt, roomID, after, limit).WithContext(ctx).Iter()

	var messages []model.Message
	var msg model.Message
	for iter.Scan(&msg.ID, &msg.RoomID, &msg.UserID, &msg.Content, &msg.CreatedAt) {
		messages = append(messages, msg)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("querying messages after: %w", err)
	}
	return messages, nil
}

// GetMessageByID returns a single message by ID within a room.
// Returns (nil, nil) if the message is not found.
func (r *Repository) GetMessageByID(ctx context.Context, roomID, messageID string) (*model.Message, error) {
	stmt := `SELECT id, room_id, user_id, content, created_at FROM messages
		WHERE room_id = ? ALLOW FILTERING`

	iter := r.session.Query(stmt, roomID).WithContext(ctx).Iter()

	var msg model.Message
	for iter.Scan(&msg.ID, &msg.RoomID, &msg.UserID, &msg.Content, &msg.CreatedAt) {
		if msg.ID == messageID {
			iter.Close() //nolint:errcheck
			return &msg, nil
		}
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("querying message by id: %w", err)
	}
	return nil, nil
}

// GetSurroundingMessages returns messages before and after a given message.
// The central message is included in the `after` slice.
func (r *Repository) GetSurroundingMessages(ctx context.Context, roomID, messageID string, limit int) ([]model.Message, []model.Message, error) {
	msg, err := r.GetMessageByID(ctx, roomID, messageID)
	if err != nil {
		return nil, nil, fmt.Errorf("finding central message: %w", err)
	}
	if msg == nil {
		return nil, nil, fmt.Errorf("message %s not found in room %s", messageID, roomID)
	}

	half := limit / 2

	beforeMsgs, err := r.GetMessagesBefore(ctx, roomID, time.Time{}, msg.CreatedAt, half)
	if err != nil {
		return nil, nil, fmt.Errorf("querying surrounding before: %w", err)
	}

	afterStmt := `SELECT id, room_id, user_id, content, created_at FROM messages
		WHERE room_id = ? AND created_at >= ?
		ORDER BY created_at ASC LIMIT ?`

	iter := r.session.Query(afterStmt, roomID, msg.CreatedAt, half+1).WithContext(ctx).Iter()

	var afterMsgs []model.Message
	var m model.Message
	for iter.Scan(&m.ID, &m.RoomID, &m.UserID, &m.Content, &m.CreatedAt) {
		afterMsgs = append(afterMsgs, m)
	}
	if err := iter.Close(); err != nil {
		return nil, nil, fmt.Errorf("querying surrounding after: %w", err)
	}

	return beforeMsgs, afterMsgs, nil
}
