package cassrepo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/atrest"
	cassmodel "github.com/hmchangw/chat/pkg/model/cassandra"
)

// casMaxRetries bounds the CAS loop; 16 retries cover realistic burst concurrency.
const casMaxRetries = 16

const (
	editMsgByID   = `UPDATE messages_by_id SET msg = ?, edited_at = ?, updated_at = ? WHERE message_id = ? AND created_at = ?`
	editMsgByRoom = `UPDATE messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
	editThreadMsg = `UPDATE thread_messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`
	editPinnedMsg = `UPDATE pinned_messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`

	editMsgByIDEncrypted   = `UPDATE messages_by_id SET enc_payload = ?, enc_meta = ?, msg = null, edited_at = ?, updated_at = ? WHERE message_id = ? AND created_at = ?`
	editMsgByRoomEncrypted = `UPDATE messages_by_room SET enc_payload = ?, enc_meta = ?, msg = null, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
	editThreadMsgEncrypted = `UPDATE thread_messages_by_room SET enc_payload = ?, enc_meta = ?, msg = null, edited_at = ?, updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`
	editPinnedMsgEncrypted = `UPDATE pinned_messages_by_room SET enc_payload = ?, enc_meta = ?, msg = null, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`

	deleteMsgByIDCAS = `UPDATE messages_by_id SET deleted = true, enc_payload = null, enc_meta = null, updated_at = ? WHERE message_id = ? AND created_at = ? IF deleted != true`
	deleteMsgByRoom  = `UPDATE messages_by_room SET deleted = true, enc_payload = null, enc_meta = null, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
	deleteThreadMsg  = `UPDATE thread_messages_by_room SET deleted = true, enc_payload = null, enc_meta = null, updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`
	deletePinnedMsg  = `UPDATE pinned_messages_by_room SET deleted = true, enc_payload = null, enc_meta = null, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
)

// MessageTypeRemoved is the Cassandra type value written to thread parent messages
// when they are soft-deleted, signalling to the frontend that the thread's
// parent message has been removed.
const MessageTypeRemoved = "message_removed"

// Thread-parent delete queries — identical to the regular delete queries but also
// set type = MessageTypeRemoved. Used when msg.TCount != nil && *msg.TCount > 0.
const (
	deleteThreadParentMsgByIDCAS = "UPDATE messages_by_id SET deleted = true, enc_payload = null, enc_meta = null, type = '" + MessageTypeRemoved + "', updated_at = ? WHERE message_id = ? AND created_at = ? IF deleted != true"
	deleteThreadParentMsgByRoom  = "UPDATE messages_by_room SET deleted = true, enc_payload = null, enc_meta = null, type = '" + MessageTypeRemoved + "', updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?"
	deleteThreadParentThreadMsg  = "UPDATE thread_messages_by_room SET deleted = true, enc_payload = null, enc_meta = null, type = '" + MessageTypeRemoved + "', updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?"
	deleteThreadParentPinnedMsg  = "UPDATE pinned_messages_by_room SET deleted = true, enc_payload = null, enc_meta = null, type = '" + MessageTypeRemoved + "', updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?"
)

// casDecrement atomically decrements a nullable INT toward zero (clamping at zero); mirrors message-worker/store_cassandra.go casIncrement.
// When initial is nil the column was never written and the function returns immediately — no LWT is issued and no zero is materialised.
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

// editPayload is the shared, pre-prepared edit payload passed to each
// per-table UPDATE. It carries either the new plaintext body (cipher
// disabled) or a pre-encrypted bundle (cipher enabled). Building it once
// and reusing it across the 2-3 mirror-table UPDATEs avoids repeated
// encryption and preserves any other encrypted fields that already
// existed on the row (attachments, sysMsgData, quoted parent body).
type editPayload struct {
	plain   string
	payload []byte             // nil when cipher is disabled
	meta    *cassmodel.EncMeta // nil when cipher is disabled
}

// buildEditPayload prepares the payload for an edit. When the cipher is
// enabled, it reads the existing encrypted row from messages_by_id,
// decrypts it, replaces only Msg with newMsg, and re-encrypts so that
// previously-encrypted fields (attachments, card, sysMsgData, quoted
// parent body) are preserved. Editing a legacy plaintext row produces a
// fresh bundle containing just Msg.
func (r *Repository) buildEditPayload(ctx context.Context, msg *models.Message, newMsg string) (editPayload, error) {
	if r.cipher == nil {
		return editPayload{plain: newMsg}, nil
	}
	fields, err := r.readEncryptedFields(ctx, msg)
	if err != nil {
		return editPayload{}, err
	}
	fields.Msg = newMsg
	payload, meta, err := r.cipher.Encrypt(ctx, msg.RoomID, fields)
	if err != nil {
		return editPayload{}, fmt.Errorf("encrypt edit body for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
	}
	return editPayload{
		plain:   newMsg,
		payload: payload,
		meta:    &cassmodel.EncMeta{Nonce: meta.Nonce},
	}, nil
}

// readEncryptedFields fetches the existing enc_payload from messages_by_id
// and decrypts it. Legacy plaintext rows (no enc_payload) return a zero
// EncryptedFields — there's nothing prior to preserve.
func (r *Repository) readEncryptedFields(ctx context.Context, msg *models.Message) (atrest.EncryptedFields, error) {
	var (
		encPayload []byte
		encMeta    *cassmodel.EncMeta
	)
	err := r.session.Query(
		`SELECT enc_payload, enc_meta FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msg.MessageID, msg.CreatedAt,
	).WithContext(ctx).Scan(&encPayload, &encMeta)
	if err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return atrest.EncryptedFields{}, nil
		}
		return atrest.EncryptedFields{}, fmt.Errorf("read existing enc_payload for message %s: %w", msg.MessageID, err)
	}
	if len(encPayload) == 0 {
		return atrest.EncryptedFields{}, nil
	}
	meta := atrest.EncMeta{}
	if encMeta != nil {
		meta.Nonce = encMeta.Nonce
	}
	fields, err := r.cipher.Decrypt(ctx, msg.RoomID, encPayload, meta)
	if err != nil {
		return atrest.EncryptedFields{}, fmt.Errorf("decrypt existing enc_payload for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
	}
	return fields, nil
}

// editOne runs the appropriate plaintext or encrypted UPDATE for one of
// the four message tables. plainQ binds (newMsg, editedAt, editedAt,
// whereArgs...); encQ binds (encPayload, encMeta, editedAt, editedAt,
// whereArgs...).
func (r *Repository) editOne(ctx context.Context, plainQ, encQ string, ep editPayload, editedAt time.Time, whereArgs ...any) error {
	if ep.payload == nil {
		args := append([]any{ep.plain, editedAt, editedAt}, whereArgs...)
		return r.session.Query(plainQ, args...).WithContext(ctx).Exec()
	}
	args := append([]any{ep.payload, ep.meta, editedAt, editedAt}, whereArgs...)
	return r.session.Query(encQ, args...).WithContext(ctx).Exec()
}

func (r *Repository) editInMessagesByID(ctx context.Context, msg *models.Message, ep editPayload, editedAt time.Time) error {
	return r.editOne(ctx, editMsgByID, editMsgByIDEncrypted, ep, editedAt, msg.MessageID, msg.CreatedAt)
}

func (r *Repository) editInMessagesByRoom(ctx context.Context, msg *models.Message, ep editPayload, editedAt time.Time) error {
	return r.editOne(ctx, editMsgByRoom, editMsgByRoomEncrypted, ep, editedAt, msg.RoomID, msg.CreatedAt, msg.MessageID)
}

func (r *Repository) editInThreadMessagesByRoom(ctx context.Context, msg *models.Message, ep editPayload, editedAt time.Time) error {
	return r.editOne(ctx, editThreadMsg, editThreadMsgEncrypted, ep, editedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID)
}

func (r *Repository) editInPinnedMessagesByRoom(ctx context.Context, msg *models.Message, ep editPayload, editedAt time.Time) error {
	return r.editOne(ctx, editPinnedMsg, editPinnedMsgEncrypted, ep, editedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID)
}

func (r *Repository) deleteInMessagesByRoom(ctx context.Context, q string, msg *models.Message, deletedAt time.Time) error {
	return r.session.Query(q, deletedAt, msg.RoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
}

func (r *Repository) deleteInThreadMessagesByRoom(ctx context.Context, q string, msg *models.Message, deletedAt time.Time) error {
	return r.session.Query(q, deletedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
}

func (r *Repository) deleteInPinnedMessagesByRoom(ctx context.Context, q string, msg *models.Message, deletedAt time.Time) error {
	return r.session.Query(q, deletedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID).WithContext(ctx).Exec()
}

func (r *Repository) UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	if msg.ThreadParentID != "" && msg.ThreadRoomID == "" {
		return fmt.Errorf("edit thread message %s: ThreadParentID %q is set but ThreadRoomID is empty", msg.MessageID, msg.ThreadParentID)
	}

	ep, err := r.buildEditPayload(ctx, msg, newMsg)
	if err != nil {
		return fmt.Errorf("prepare edit payload for message %s: %w", msg.MessageID, err)
	}

	if err := r.editInMessagesByID(ctx, msg, ep, editedAt); err != nil {
		return fmt.Errorf("update messages_by_id for message %s: %w", msg.MessageID, err)
	}

	if msg.ThreadParentID == "" {
		if err := r.editInMessagesByRoom(ctx, msg, ep, editedAt); err != nil {
			return fmt.Errorf("update messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
		}
	} else {
		if err := r.editInThreadMessagesByRoom(ctx, msg, ep, editedAt); err != nil {
			return fmt.Errorf("update thread_messages_by_room for message %s room %s thread %s: %w", msg.MessageID, msg.RoomID, msg.ThreadRoomID, err)
		}
	}

	if msg.PinnedAt != nil {
		if err := r.editInPinnedMessagesByRoom(ctx, msg, ep, editedAt); err != nil {
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

	isThreadParent := msg.TCount != nil && *msg.TCount > 0

	casQuery := deleteMsgByIDCAS
	if isThreadParent {
		casQuery = deleteThreadParentMsgByIDCAS
	}

	var current bool
	applied, err := r.session.Query(
		casQuery,
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

	msgByRoomQ := deleteMsgByRoom
	threadMsgQ := deleteThreadMsg
	pinnedMsgQ := deletePinnedMsg
	if isThreadParent {
		msgByRoomQ = deleteThreadParentMsgByRoom
		threadMsgQ = deleteThreadParentThreadMsg
		pinnedMsgQ = deleteThreadParentPinnedMsg
	}

	if msg.ThreadParentID == "" {
		if err := r.deleteInMessagesByRoom(ctx, msgByRoomQ, msg, deletedAt); err != nil {
			return time.Time{}, false, fmt.Errorf("update messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
		}
	} else {
		if err := r.deleteInThreadMessagesByRoom(ctx, threadMsgQ, msg, deletedAt); err != nil {
			return time.Time{}, false, fmt.Errorf("update thread_messages_by_room for message %s room %s thread %s: %w", msg.MessageID, msg.RoomID, msg.ThreadRoomID, err)
		}
	}

	if msg.PinnedAt != nil {
		if err := r.deleteInPinnedMessagesByRoom(ctx, pinnedMsgQ, msg, deletedAt); err != nil {
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
