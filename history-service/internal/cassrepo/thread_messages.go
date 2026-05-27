package cassrepo

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/history-service/internal/models"
)

// Subset of columns present in thread_messages_by_thread (no tshow, thread_parent_created_at, or pinned_* columns).
const threadMessageColumns = "room_id, thread_room_id, created_at, message_id, thread_parent_id, " +
	"sender, msg, mentions, attachments, file, card, card_action, " +
	"quoted_parent_message, visible_to, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"

// roomID is retained in the signature for caller symmetry and for redaction
// checks done above this layer; the query partitions by thread_room_id only.
func (r *Repository) GetThreadMessages(
	ctx context.Context, _ string, threadRoomID string,
	before time.Time, floor time.Time,
	pageReq PageRequest,
) (Page[models.Message], error) {
	q := r.session.Query(
		"SELECT "+threadMessageColumns+
			` FROM thread_messages_by_thread WHERE thread_room_id = ? AND created_at < ? AND created_at >= ? ORDER BY created_at DESC`,
		threadRoomID, before, floor,
	).WithContext(ctx)

	rows := make([]models.Message, 0, pageReq.PageSize)
	nextCursor, err := NewQueryBuilder(q).
		WithCursor(pageReq.Cursor).
		WithPageSize(pageReq.PageSize).
		Fetch(func(iter *gocql.Iter) {
			for len(rows) < pageReq.PageSize {
				var m models.Message
				if !structScan(iter, &m) {
					break
				}
				rows = append(rows, m)
			}
		})
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("get thread messages: %w", err)
	}

	return Page[models.Message]{
		Data:       rows,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}
