package cassrepo

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/history-service/internal/models"
)

const messageQuery = "SELECT room_id, created_at, message_id, sender, target_user, " +
	"msg, mentions, attachments, file, card, card_action, tshow, " +
	"thread_parent_created_at, visible_to, unread, reactions, deleted, " +
	"sys_msg_type, sys_msg_data, federate_from, edited_at, updated_at " +
	"FROM messages_by_room"

// messageScanDest returns the Scan destination pointers for a Message in column order.
func messageScanDest(m *models.Message) []any {
	return []any{
		&m.RoomID, &m.CreatedAt, &m.MessageID,
		&m.Sender, &m.TargetUser, &m.Msg,
		&m.Mentions, &m.Attachments, &m.File,
		&m.Card, &m.CardAction, &m.TShow,
		&m.ThreadParentCreatedAt, &m.VisibleTo, &m.Unread,
		&m.Reactions, &m.Deleted, &m.SysMsgType,
		&m.SysMsgData, &m.FederateFrom, &m.EditedAt,
		&m.UpdatedAt,
	}
}

// Repository implements service.MessageRepository using Cassandra.
type Repository struct {
	session *gocql.Session
}

// NewRepository creates a new Cassandra repository.
func NewRepository(session *gocql.Session) *Repository {
	return &Repository{session: session}
}

func scanMessages(iter *gocql.Iter) []models.Message {
	messages := make([]models.Message, 0)
	for {
		var m models.Message
		if !iter.Scan(messageScanDest(&m)...) {
			break
		}
		messages = append(messages, m)
	}
	return messages
}

// GetMessagesBefore returns a paginated set of messages strictly before `before`, newest-first.
func (r *Repository) GetMessagesBefore(ctx context.Context, roomID string, before time.Time, q PageRequest) (Page[models.Message], error) {
	var messages []models.Message

	nextCursor, err := NewQueryBuilder(
		r.session.Query(
			messageQuery+` WHERE room_id = ? AND created_at < ? ORDER BY created_at DESC`,
			roomID, before,
		).WithContext(ctx),
	).
		WithCursor(q.Cursor).
		WithPageSize(q.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanMessages(iter)
		})
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("querying messages before: %w", err)
	}

	return Page[models.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}

// GetMessagesBetweenDesc returns a paginated set of messages between `since` and `before`, newest-first.
// Used when a lower-bound access restriction (e.g. historySharedSince) must be enforced.
func (r *Repository) GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, q PageRequest) (Page[models.Message], error) {
	var messages []models.Message

	nextCursor, err := NewQueryBuilder(
		r.session.Query(
			messageQuery+` WHERE room_id = ? AND created_at > ? AND created_at < ? ORDER BY created_at DESC`,
			roomID, since, before,
		).WithContext(ctx),
	).
		WithCursor(q.Cursor).
		WithPageSize(q.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanMessages(iter)
		})
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("querying messages between desc: %w", err)
	}

	return Page[models.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}

// GetMessagesAfter returns a paginated set of messages strictly after `after`, oldest-first.
func (r *Repository) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, q PageRequest) (Page[models.Message], error) {
	var messages []models.Message

	nextCursor, err := NewQueryBuilder(
		r.session.Query(
			messageQuery+` WHERE room_id = ? AND created_at > ? ORDER BY created_at ASC`,
			roomID, after,
		).WithContext(ctx),
	).
		WithCursor(q.Cursor).
		WithPageSize(q.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanMessages(iter)
		})
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("querying messages after: %w", err)
	}

	return Page[models.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}

// GetAllMessagesAsc returns a paginated set of all messages in the room, oldest-first.
// Used when no lower-bound cursor exists.
func (r *Repository) GetAllMessagesAsc(ctx context.Context, roomID string, q PageRequest) (Page[models.Message], error) {
	var messages []models.Message

	nextCursor, err := NewQueryBuilder(
		r.session.Query(
			messageQuery+` WHERE room_id = ? ORDER BY created_at ASC`,
			roomID,
		).WithContext(ctx),
	).
		WithCursor(q.Cursor).
		WithPageSize(q.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanMessages(iter)
		})
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("querying all messages asc: %w", err)
	}

	return Page[models.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}

// GetMessageByTimestamp returns a single message using the full primary key (room_id, created_at, message_id).
// This is an O(1) lookup with no ALLOW FILTERING. Returns (nil, nil) if not found.
func (r *Repository) GetMessageByTimestamp(ctx context.Context, roomID string, createdAt time.Time, messageID string) (*models.Message, error) {
	var m models.Message
	err := r.session.Query(
		messageQuery+` WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, messageID,
	).WithContext(ctx).Scan(messageScanDest(&m)...)
	if err == gocql.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying message by timestamp: %w", err)
	}
	return &m, nil
}

// GetMessageByID returns a single message by ID within a room.
// Returns (nil, nil) if the message is not found.
// NOTE: Uses ALLOW FILTERING to push the id filter server-side within a single
// partition (room_id). Prefer GetMessageByTimestamp when created_at is available.
func (r *Repository) GetMessageByID(ctx context.Context, roomID, messageID string) (*models.Message, error) {
	var m models.Message
	err := r.session.Query(
		messageQuery+` WHERE room_id = ? AND message_id = ? ALLOW FILTERING`,
		roomID, messageID,
	).WithContext(ctx).Scan(messageScanDest(&m)...)
	if err == gocql.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying message by id: %w", err)
	}
	return &m, nil
}
