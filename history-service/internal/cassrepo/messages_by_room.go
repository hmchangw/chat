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
	"visible_to, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"

const messageByRoomQuery = "SELECT " + baseColumns + " FROM messages_by_room"

// scanMsgsFromIter collects all rows from iter into a slice.
// structScan ignores columns absent from the struct's cql tags, so this
// helper is safe to use with any column subset (e.g. messageByIDQuery
// includes pinned_at/pinned_by which are absent from the base column list).
func scanMsgsFromIter(iter *gocql.Iter) []models.Message {
	messages := make([]models.Message, 0)
	for {
		var m models.Message
		if !structScan(iter, &m) {
			break
		}
		messages = append(messages, m)
	}
	return messages
}

func fetchMessagesPage(q *gocql.Query, pageReq PageRequest, errMsg string) (Page[models.Message], error) {
	var messages []models.Message
	nextCursor, err := NewQueryBuilder(q).
		WithCursor(pageReq.Cursor).
		WithPageSize(pageReq.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanMsgsFromIter(iter)
		})
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("%s: %w", errMsg, err)
	}
	return Page[models.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}

func (r *Repository) GetMessagesBefore(ctx context.Context, roomID string, before time.Time, pageReq PageRequest) (Page[models.Message], error) {
	return fetchMessagesPage(
		r.session.Query(
			messageByRoomQuery+` WHERE room_id = ? AND created_at < ? ORDER BY created_at DESC`,
			roomID, before,
		).WithContext(ctx),
		pageReq, "querying messages before",
	)
}

func (r *Repository) GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, pageReq PageRequest) (Page[models.Message], error) {
	return fetchMessagesPage(
		r.session.Query(
			messageByRoomQuery+` WHERE room_id = ? AND created_at > ? AND created_at < ? ORDER BY created_at DESC`,
			roomID, since, before,
		).WithContext(ctx),
		pageReq, "querying messages between desc",
	)
}

func (r *Repository) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, pageReq PageRequest) (Page[models.Message], error) {
	return fetchMessagesPage(
		r.session.Query(
			messageByRoomQuery+` WHERE room_id = ? AND created_at > ? ORDER BY created_at ASC`,
			roomID, after,
		).WithContext(ctx),
		pageReq, "querying messages after",
	)
}

func (r *Repository) GetAllMessagesAsc(ctx context.Context, roomID string, pageReq PageRequest) (Page[models.Message], error) {
	return fetchMessagesPage(
		r.session.Query(
			messageByRoomQuery+` WHERE room_id = ? ORDER BY created_at ASC`,
			roomID,
		).WithContext(ctx),
		pageReq, "querying all messages asc",
	)
}
