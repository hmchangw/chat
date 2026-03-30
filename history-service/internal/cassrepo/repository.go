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

func scanMessages(iter *gocql.Iter) []model.Message {
	var messages []model.Message
	var msg model.Message
	for iter.Scan(&msg.ID, &msg.RoomID, &msg.UserID, &msg.Content, &msg.CreatedAt) {
		messages = append(messages, msg)
	}
	return messages
}

// GetMessagesBefore returns a paginated set of messages before `before` and after `since`, newest-first.
func (r *Repository) GetMessagesBefore(ctx context.Context, roomID string, since, before time.Time, q Query) (Page[model.Message], error) {
	var messages []model.Message

	nextCursor, err := NewQueryBuilder(
		r.session.Query(
			`SELECT id, room_id, user_id, content, created_at FROM messages
			WHERE room_id = ? AND created_at > ? AND created_at < ?
			ORDER BY created_at DESC`,
			roomID, since, before,
		).WithContext(ctx),
	).
		WithCursor(q.Cursor).
		WithPageSize(q.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanMessages(iter)
		})
	if err != nil {
		return Page[model.Message]{}, fmt.Errorf("querying messages before: %w", err)
	}

	return Page[model.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}

// GetMessagesAfter returns a paginated set of messages after `after`, oldest-first.
func (r *Repository) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, q Query) (Page[model.Message], error) {
	var messages []model.Message

	nextCursor, err := NewQueryBuilder(
		r.session.Query(
			`SELECT id, room_id, user_id, content, created_at FROM messages
			WHERE room_id = ? AND created_at > ?
			ORDER BY created_at ASC`,
			roomID, after,
		).WithContext(ctx),
	).
		WithCursor(q.Cursor).
		WithPageSize(q.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanMessages(iter)
		})
	if err != nil {
		return Page[model.Message]{}, fmt.Errorf("querying messages after: %w", err)
	}

	return Page[model.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}

// GetMessageByID returns a single message by ID within a room.
// Returns (nil, nil) if the message is not found.
// NOTE: This scans the full partition because `id` is not part of the primary key.
// TODO: Add a messages_by_id lookup table or secondary index when schema is finalized.
func (r *Repository) GetMessageByID(ctx context.Context, roomID, messageID string) (*model.Message, error) {
	iter := r.session.Query(
		`SELECT id, room_id, user_id, content, created_at FROM messages WHERE room_id = ?`,
		roomID,
	).WithContext(ctx).Iter()

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

	beforePage, err := r.GetMessagesBefore(ctx, roomID, time.Time{}, msg.CreatedAt, Query{
		Cursor: &Cursor{}, PageSize: half,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("querying surrounding before: %w", err)
	}

	var afterMsgs []model.Message
	_, err = NewQueryBuilder(
		r.session.Query(
			`SELECT id, room_id, user_id, content, created_at FROM messages
			WHERE room_id = ? AND created_at >= ?
			ORDER BY created_at ASC`,
			roomID, msg.CreatedAt,
		).WithContext(ctx),
	).
		WithPageSize(half + 1).
		Fetch(func(iter *gocql.Iter) {
			afterMsgs = scanMessages(iter)
		})
	if err != nil {
		return nil, nil, fmt.Errorf("querying surrounding after: %w", err)
	}

	return beforePage.Data, afterMsgs, nil
}
