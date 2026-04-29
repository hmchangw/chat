package cassrepo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/history-service/internal/models"
)

// casMaxRetries bounds the CAS loop; 16 retries cover realistic burst concurrency.
const casMaxRetries = 16

const (
	editMsgByID   = `UPDATE messages_by_id SET msg = ?, edited_at = ?, updated_at = ? WHERE message_id = ? AND created_at = ?`
	editMsgByRoom = `UPDATE messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
	editThreadMsg = `UPDATE thread_messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`
	editPinnedMsg = `UPDATE pinned_messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`

	deleteMsgByIDCAS = `UPDATE messages_by_id SET deleted = true, updated_at = ? WHERE message_id = ? AND created_at = ? IF deleted != true`
	deleteMsgByRoom  = `UPDATE messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
	deleteThreadMsg  = `UPDATE thread_messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`
	deletePinnedMsg  = `UPDATE pinned_messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
)

// casDecrement atomically decrements a nullable INT toward zero (clamping at zero); mirrors message-worker/store_cassandra.go casIncrement.
// When expected is nil, the LWT becomes "IF tcount = null" — Cassandra evaluates a null column as equal to a null bind value, so the update applies.
func casDecrement(maxRetries int, initial *int, update func(newVal int, expected *int) (applied bool, current *int, err error)) error {
	if initial == nil {
		// tcount was never written — nothing to decrement; skip to avoid materialising a zero on a null column.
		return nil
	}
	tcount := initial
	for range maxRetries {
		newVal := 0
		if tcount != nil && *tcount > 0 {
			newVal = *tcount - 1
		}
		applied, current, err := update(newVal, tcount)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
		tcount = current
	}
	return fmt.Errorf("cas decrement exceeded %d retries", maxRetries)
}

func (r *Repository) editInMessagesByID(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	return r.session.Query(editMsgByID, newMsg, editedAt, editedAt, msg.MessageID, msg.CreatedAt).WithContext(ctx).Exec()
}

func (r *Repository) editInMessagesByRoom(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	return r.session.Query(editMsgByRoom, newMsg, editedAt, editedAt, msg.RoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
}

func (r *Repository) editInThreadMessagesByRoom(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	return r.session.Query(editThreadMsg, newMsg, editedAt, editedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
}

func (r *Repository) editInPinnedMessagesByRoom(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	return r.session.Query(editPinnedMsg, newMsg, editedAt, editedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID).WithContext(ctx).Exec()
}

func (r *Repository) deleteInMessagesByRoom(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
	return r.session.Query(deleteMsgByRoom, deletedAt, msg.RoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
}

func (r *Repository) deleteInThreadMessagesByRoom(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
	return r.session.Query(deleteThreadMsg, deletedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
}

func (r *Repository) deleteInPinnedMessagesByRoom(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
	return r.session.Query(deletePinnedMsg, deletedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID).WithContext(ctx).Exec()
}

func (r *Repository) UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	if msg.ThreadParentID != "" && msg.ThreadRoomID == "" {
		return fmt.Errorf("edit thread message %s: ThreadParentID %q is set but ThreadRoomID is empty", msg.MessageID, msg.ThreadParentID)
	}

	if err := r.editInMessagesByID(ctx, msg, newMsg, editedAt); err != nil {
		return fmt.Errorf("update messages_by_id for message %s: %w", msg.MessageID, err)
	}

	if msg.ThreadParentID == "" {
		if err := r.editInMessagesByRoom(ctx, msg, newMsg, editedAt); err != nil {
			return fmt.Errorf("update messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
		}
	} else {
		if err := r.editInThreadMessagesByRoom(ctx, msg, newMsg, editedAt); err != nil {
			return fmt.Errorf("update thread_messages_by_room for message %s room %s thread %s: %w", msg.MessageID, msg.RoomID, msg.ThreadRoomID, err)
		}
	}

	if msg.PinnedAt != nil {
		if err := r.editInPinnedMessagesByRoom(ctx, msg, newMsg, editedAt); err != nil {
			return fmt.Errorf("update pinned_messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
		}
	}

	return nil
}

// SoftDeleteMessage uses a Cassandra LWT on messages_by_id as a one-shot gate so only
// the winning goroutine runs mirror-table updates and tcount decrement, preventing double-decrement.
// `IF deleted != true` matches NULL (message-worker never writes deleted) and false, excluding true.
func (r *Repository) SoftDeleteMessage(ctx context.Context, msg *models.Message, deletedAt time.Time) (time.Time, bool, error) {
	if msg.ThreadParentID != "" && msg.ThreadRoomID == "" {
		return time.Time{}, false, fmt.Errorf("delete thread message %s: ThreadParentID %q is set but ThreadRoomID is empty", msg.MessageID, msg.ThreadParentID)
	}

	var current bool
	applied, err := r.session.Query(
		deleteMsgByIDCAS,
		deletedAt, msg.MessageID, msg.CreatedAt,
	).WithContext(ctx).ScanCAS(&current)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("cas update messages_by_id for message %s: %w", msg.MessageID, err)
	}
	if !applied {
		// Concurrent delete won. Read the existing updated_at so the caller
		// can return an accurate response timestamp.
		var existing time.Time
		if err := r.session.Query(
			`SELECT updated_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			msg.MessageID, msg.CreatedAt,
		).WithContext(ctx).Scan(&existing); err != nil {
			if errors.Is(err, gocql.ErrNotFound) {
				// Row vanished between the CAS and the follow-up SELECT — abnormal race.
				return time.Time{}, false, fmt.Errorf("message %s vanished after cas miss: %w", msg.MessageID, gocql.ErrNotFound)
			}
			return time.Time{}, false, fmt.Errorf("read updated_at after cas miss for message %s: %w", msg.MessageID, err)
		}
		return existing, false, nil
	}

	if msg.ThreadParentID == "" {
		if err := r.deleteInMessagesByRoom(ctx, msg, deletedAt); err != nil {
			return time.Time{}, false, fmt.Errorf("update messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
		}
	} else {
		if err := r.deleteInThreadMessagesByRoom(ctx, msg, deletedAt); err != nil {
			return time.Time{}, false, fmt.Errorf("update thread_messages_by_room for message %s room %s thread %s: %w", msg.MessageID, msg.RoomID, msg.ThreadRoomID, err)
		}
	}

	if msg.PinnedAt != nil {
		if err := r.deleteInPinnedMessagesByRoom(ctx, msg, deletedAt); err != nil {
			return time.Time{}, false, fmt.Errorf("update pinned_messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
		}
	}

	if msg.ThreadParentID != "" {
		if err := r.decrementParentTcount(ctx, msg); err != nil {
			return time.Time{}, false, fmt.Errorf("decrement parent tcount for message %s: %w", msg.MessageID, err)
		}
	}

	return deletedAt, true, nil
}

// decrementParentTcount silently skips if ThreadParentCreatedAt is nil or if the parent row is missing.
func (r *Repository) decrementParentTcount(ctx context.Context, msg *models.Message) error {
	if msg.ThreadParentCreatedAt == nil {
		return nil
	}
	parentID := msg.ThreadParentID
	parentCreatedAt := *msg.ThreadParentCreatedAt

	// CAS decrement on messages_by_id.
	var tcount *int
	if err := r.session.Query(
		`SELECT tcount FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		parentID, parentCreatedAt,
	).WithContext(ctx).Scan(&tcount); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read tcount for parent %s in messages_by_id: %w", parentID, err)
	}
	if err := casDecrement(casMaxRetries, tcount, func(newVal int, expected *int) (bool, *int, error) {
		var current *int
		applied, err := r.session.Query(
			`UPDATE messages_by_id SET tcount = ? WHERE message_id = ? AND created_at = ? IF tcount = ?`,
			newVal, parentID, parentCreatedAt, expected,
		).WithContext(ctx).ScanCAS(&current)
		return applied, current, err
	}); err != nil {
		return fmt.Errorf("cas tcount decrement in messages_by_id for parent %s: %w", parentID, err)
	}

	// CAS decrement on messages_by_room.
	if err := r.session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		msg.RoomID, parentCreatedAt, parentID,
	).WithContext(ctx).Scan(&tcount); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read tcount for parent %s in messages_by_room: %w", parentID, err)
	}
	if err := casDecrement(casMaxRetries, tcount, func(newVal int, expected *int) (bool, *int, error) {
		var current *int
		applied, err := r.session.Query(
			`UPDATE messages_by_room SET tcount = ? WHERE room_id = ? AND created_at = ? AND message_id = ? IF tcount = ?`,
			newVal, msg.RoomID, parentCreatedAt, parentID, expected,
		).WithContext(ctx).ScanCAS(&current)
		return applied, current, err
	}); err != nil {
		return fmt.Errorf("cas tcount decrement in messages_by_room for parent %s: %w", parentID, err)
	}

	return nil
}
