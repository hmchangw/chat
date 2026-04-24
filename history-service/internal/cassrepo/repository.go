package cassrepo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/history-service/internal/models"
)

// baseColumns are shared across messages_by_room and messages_by_id.
const baseColumns = "room_id, created_at, message_id, thread_room_id, sender, target_user, " +
	"msg, mentions, attachments, file, card, card_action, tshow, tcount, " +
	"thread_parent_id, thread_parent_created_at, quoted_parent_message, " +
	"visible_to, unread, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"

// messageByIDExtraColumns are the columns only present in messages_by_id.
const messageByIDExtraColumns = ", pinned_at, pinned_by"

const messageByRoomQuery = "SELECT " + baseColumns + " FROM messages_by_room"
const messageByIDQuery = "SELECT " + baseColumns + messageByIDExtraColumns + " FROM messages_by_id"

// baseScanDest returns Scan destination pointers for the baseColumns in order.
func baseScanDest(m *models.Message) []any {
	return []any{
		&m.RoomID, &m.CreatedAt, &m.MessageID, &m.ThreadRoomID,
		&m.Sender, &m.TargetUser, &m.Msg,
		&m.Mentions, &m.Attachments, &m.File,
		&m.Card, &m.CardAction, &m.TShow, &m.TCount,
		&m.ThreadParentID, &m.ThreadParentCreatedAt, &m.QuotedParentMessage,
		&m.VisibleTo, &m.Unread, &m.Reactions,
		&m.Deleted, &m.Type, &m.SysMsgData,
		&m.SiteID, &m.EditedAt, &m.UpdatedAt,
	}
}

// messageByIDScanDest returns Scan destination pointers for all messages_by_id columns.
func messageByIDScanDest(m *models.Message) []any {
	return append(baseScanDest(m), &m.PinnedAt, &m.PinnedBy)
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
		if !iter.Scan(baseScanDest(&m)...) {
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
			messageByRoomQuery+` WHERE room_id = ? AND created_at < ? ORDER BY created_at DESC`,
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
			messageByRoomQuery+` WHERE room_id = ? AND created_at > ? AND created_at < ? ORDER BY created_at DESC`,
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
			messageByRoomQuery+` WHERE room_id = ? AND created_at > ? ORDER BY created_at ASC`,
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
			messageByRoomQuery+` WHERE room_id = ? ORDER BY created_at ASC`,
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

// GetMessageByID returns a single message from the messages_by_id lookup table.
// Returns (nil, nil) if the message is not found.
func (r *Repository) GetMessageByID(ctx context.Context, messageID string) (*models.Message, error) {
	var m models.Message
	err := r.session.Query(
		messageByIDQuery+` WHERE message_id = ?`,
		messageID,
	).WithContext(ctx).Scan(messageByIDScanDest(&m)...)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying message by id: %w", err)
	}
	return &m, nil
}

// UpdateMessageContent updates the msg, edited_at, and updated_at fields
// across the Cassandra tables that actually hold the row, determined from
// msg's own metadata. Top-level messages (msg.ThreadParentID == "") land in
// messages_by_room; thread replies land in thread_messages_by_room; pinned
// messages additionally land in pinned_messages_by_room. messages_by_id is
// always updated. All UPDATEs use the full PK; none is a no-op against a
// missing row — see spec doc for the Cassandra phantom-row rationale.
// Idempotent with respect to msg content; timestamps advance per call.
func (r *Repository) UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	// Always: messages_by_id
	if err := r.session.Query(
		`UPDATE messages_by_id SET msg = ?, edited_at = ?, updated_at = ? WHERE message_id = ? AND created_at = ?`,
		newMsg, editedAt, editedAt, msg.MessageID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update messages_by_id: %w", err)
	}

	// Top-level only: messages_by_room
	if msg.ThreadParentID == "" {
		if err := r.session.Query(
			`UPDATE messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			newMsg, editedAt, editedAt, msg.RoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update messages_by_room: %w", err)
		}
	}

	// Thread-reply and pinned branches are added in Tasks 8 and 9.
	return nil
}
