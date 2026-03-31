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

// GetMessagesBetweenDesc returns a paginated set of messages between `since` and `before`, newest-first.
// Used when a lower-bound access restriction (e.g. historySharedSince) must be enforced.
func (r *Repository) GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, q PageRequest) (Page[model.Message], error) {
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
		return Page[model.Message]{}, fmt.Errorf("querying messages between desc: %w", err)
	}

	return Page[model.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}

// GetMessagesBetweenAsc returns a paginated set of messages between `after` and `before`, oldest-first.
// When inclusive is true the upper bound is `<=`, otherwise it is strictly `<`.
// Used for finding unread messages within a range.
func (r *Repository) GetMessagesBetweenAsc(ctx context.Context, roomID string, after, before time.Time, inclusive bool, q PageRequest) (Page[model.Message], error) {
	var messages []model.Message

	upperOp := "<"
	if inclusive {
		upperOp = "<="
	}
	cql := fmt.Sprintf(`SELECT id, room_id, user_id, content, created_at FROM messages WHERE room_id = ? AND created_at > ? AND created_at %s ? ORDER BY created_at ASC`, upperOp)

	nextCursor, err := NewQueryBuilder(
		r.session.Query(cql, roomID, after, before).WithContext(ctx),
	).
		WithCursor(q.Cursor).
		WithPageSize(q.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanMessages(iter)
		})
	if err != nil {
		return Page[model.Message]{}, fmt.Errorf("querying messages between asc: %w", err)
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

// GetAllMessagesAsc returns a paginated set of all messages in the room, oldest-first.
// Used when no lower-bound cursor exists.
func (r *Repository) GetAllMessagesAsc(ctx context.Context, roomID string, q PageRequest) (Page[model.Message], error) {
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
// NOTE: Uses ALLOW FILTERING to push the id filter server-side within a single
// partition (room_id). Consider adding a messages_by_id lookup table if this
// becomes a bottleneck in rooms with very large message counts.
func (r *Repository) GetMessageByID(ctx context.Context, roomID, messageID string) (*model.Message, error) {
	var msg model.Message
	err := r.session.Query(
		`SELECT id, room_id, user_id, content, created_at FROM messages
		WHERE room_id = ? AND id = ?
		ALLOW FILTERING`,
		roomID, messageID,
	).WithContext(ctx).Scan(&msg.ID, &msg.RoomID, &msg.UserID, &msg.Content, &msg.CreatedAt)
	if err == gocql.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying message by id: %w", err)
	}
	return &msg, nil
}
