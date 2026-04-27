package cassrepo

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/history-service/internal/models"
)

const baseColumns = "room_id, created_at, message_id, thread_room_id, sender, target_user, " +
	"msg, mentions, attachments, file, card, card_action, tshow, tcount, " +
	"thread_parent_id, thread_parent_created_at, quoted_parent_message, " +
	"visible_to, unread, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"

const messageByRoomQuery = "SELECT " + baseColumns + " FROM messages_by_room"

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

// scanWith iterates a Cassandra result set, using dest for each row's scan destinations.
// Shared by all three table-specific scan helpers.
func scanWith(iter *gocql.Iter, dest func(*models.Message) []any) []models.Message {
	messages := make([]models.Message, 0)
	for {
		var m models.Message
		if !iter.Scan(dest(&m)...) {
			break
		}
		messages = append(messages, m)
	}
	return messages
}

func scanMessages(iter *gocql.Iter) []models.Message { return scanWith(iter, baseScanDest) }

// GetMessagesBefore returns messages strictly before `before`, newest-first.
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

// GetMessagesBetweenDesc returns messages between `since` and `before`, newest-first.
// Used when a historySharedSince lower bound must be enforced.
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

// GetMessagesAfter returns messages strictly after `after`, oldest-first.
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

// GetAllMessagesAsc returns all messages in the room, oldest-first.
// Used when there is no lower-bound access restriction.
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
