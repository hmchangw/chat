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

// GetMessagesBefore returns a paginated set of messages strictly before `before`, newest-first.
func (r *Repository) GetMessagesBefore(ctx context.Context, roomID string, before time.Time, q PageRequest) (Page[model.Message], error) {
	var messages []model.Message

	nextCursor, err := NewQueryBuilder(
		r.session.Query(
			`SELECT id, room_id, user_id, content, created_at FROM messages
			WHERE room_id = ? AND created_at < ?
			ORDER BY created_at DESC`,
			roomID, before,
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

// GetMessagesInRange returns a paginated set of messages between `since` and `before`, newest-first.
// Used when a lower-bound access restriction (e.g. historySharedSince) must be enforced.
func (r *Repository) GetMessagesInRange(ctx context.Context, roomID string, since, before time.Time, q PageRequest) (Page[model.Message], error) {
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
		return Page[model.Message]{}, fmt.Errorf("querying messages in range: %w", err)
	}

	return Page[model.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}

// GetMessagesBetween returns a paginated set of messages between `after` and `before`, oldest-first.
// Used for forward pagination and for finding unread messages within a range.
func (r *Repository) GetMessagesBetween(ctx context.Context, roomID string, after, before time.Time, q PageRequest) (Page[model.Message], error) {
	var messages []model.Message

	nextCursor, err := NewQueryBuilder(
		r.session.Query(
			`SELECT id, room_id, user_id, content, created_at FROM messages
			WHERE room_id = ? AND created_at > ? AND created_at < ?
			ORDER BY created_at ASC`,
			roomID, after, before,
		).WithContext(ctx),
	).
		WithCursor(q.Cursor).
		WithPageSize(q.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanMessages(iter)
		})
	if err != nil {
		return Page[model.Message]{}, fmt.Errorf("querying messages between: %w", err)
	}

	return Page[model.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}

// GetMessagesAfter returns a paginated set of messages strictly after `after`, oldest-first.
func (r *Repository) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, q PageRequest) (Page[model.Message], error) {
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

// GetLatestMessages returns a paginated set of all messages in the room, oldest-first.
// Used when no lower-bound cursor exists.
func (r *Repository) GetLatestMessages(ctx context.Context, roomID string, q PageRequest) (Page[model.Message], error) {
	var messages []model.Message

	nextCursor, err := NewQueryBuilder(
		r.session.Query(
			`SELECT id, room_id, user_id, content, created_at FROM messages
			WHERE room_id = ?
			ORDER BY created_at ASC`,
			roomID,
		).WithContext(ctx),
	).
		WithCursor(q.Cursor).
		WithPageSize(q.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanMessages(iter)
		})
	if err != nil {
		return Page[model.Message]{}, fmt.Errorf("querying latest messages: %w", err)
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
