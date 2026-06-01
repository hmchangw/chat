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

// CAS = Compare-And-Set: a Cassandra UPDATE/INSERT with an `IF` clause,
// executed as a Paxos lightweight transaction (LWT) so the read-check-
// write is atomic across replicas. Costs ~4× a normal QUORUM write but
// is the only safe primitive when correctness depends on the row's
// current state (e.g. "edit only if not soft-deleted"). The `applied`
// boolean returned by gocql tells you whether the `IF` predicate held.
//
// casMaxRetries bounds the CAS loop; 16 retries cover realistic burst concurrency.
const casMaxRetries = 16

const (
	// Plaintext-path edits on the canonical row (messages_by_id) gate via
	// LWT `IF deleted != true` so a non-existent or soft-deleted target
	// fails atomically instead of materialising a ghost row or resurrecting
	// a tombstone. Mirror-table edits stay non-LWT — they're best-effort
	// projections of the canonical row and the messages_by_id CAS is the
	// authoritative gate. enc_payload/enc_meta are nulled to keep a
	// cipher-disabled (rollback) edit from leaving stale ciphertext that
	// would silently override the new plaintext on re-enabled reads.
	editMsgByIDCAS = `UPDATE messages_by_id SET msg = ?, enc_payload = null, enc_meta = null, edited_at = ?, updated_at = ? WHERE message_id = ? AND created_at = ? IF deleted != true`
	editMsgByRoom  = `UPDATE messages_by_room SET msg = ?, enc_payload = null, enc_meta = null, edited_at = ?, updated_at = ? WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?`
	editThreadMsg  = `UPDATE thread_messages_by_thread SET msg = ?, enc_payload = null, enc_meta = null, edited_at = ?, updated_at = ? WHERE thread_room_id = ? AND created_at = ? AND message_id = ?`
	editPinnedMsg  = `UPDATE pinned_messages_by_room SET msg = ?, enc_payload = null, enc_meta = null, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`

	// Encrypted-path edits null every legacy body column (msg, attachments,
	// card, card_action, sys_msg_data, quoted_parent_message). buildEditPayload
	// has already promoted those into the new bundle; leaving any plaintext
	// column behind would defeat the rollout's at-rest goal on edited legacy
	// rows. The canonical row uses LWT for the same not-found/tombstone gate
	// as the plaintext path.
	editMsgByIDEncryptedCAS = `UPDATE messages_by_id SET enc_payload = ?, enc_meta = ?, msg = null, attachments = null, card = null, card_action = null, sys_msg_data = null, quoted_parent_message = null, edited_at = ?, updated_at = ? WHERE message_id = ? AND created_at = ? IF deleted != true`
	editMsgByRoomEncrypted  = `UPDATE messages_by_room SET enc_payload = ?, enc_meta = ?, msg = null, attachments = null, card = null, card_action = null, sys_msg_data = null, quoted_parent_message = null, edited_at = ?, updated_at = ? WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?`
	editThreadMsgEncrypted  = `UPDATE thread_messages_by_thread SET enc_payload = ?, enc_meta = ?, msg = null, attachments = null, card = null, card_action = null, sys_msg_data = null, quoted_parent_message = null, edited_at = ?, updated_at = ? WHERE thread_room_id = ? AND created_at = ? AND message_id = ?`
	editPinnedMsgEncrypted  = `UPDATE pinned_messages_by_room SET enc_payload = ?, enc_meta = ?, msg = null, attachments = null, card = null, card_action = null, sys_msg_data = null, quoted_parent_message = null, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`

	deleteMsgByIDCAS = `UPDATE messages_by_id SET deleted = true, enc_payload = null, enc_meta = null, updated_at = ? WHERE message_id = ? AND created_at = ? IF deleted != true`
	deleteMsgByRoom  = `UPDATE messages_by_room SET deleted = true, enc_payload = null, enc_meta = null, updated_at = ? WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?`
	deleteThreadMsg  = `UPDATE thread_messages_by_thread SET deleted = true, enc_payload = null, enc_meta = null, updated_at = ? WHERE thread_room_id = ? AND created_at = ? AND message_id = ?`
	deletePinnedMsg  = `UPDATE pinned_messages_by_room SET deleted = true, enc_payload = null, enc_meta = null, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
)

// ErrMessageNotFound is returned by edit operations when the target
// (message_id, created_at) does not exist. Cassandra's UPDATE is upsert-
// equivalent, so callers must short-circuit on this error instead of
// issuing the UPDATE — otherwise the edit would materialise a partial
// ghost row with no sender/room_id/type/mentions.
var ErrMessageNotFound = errors.New("message not found")

// MessageTypeRemoved is the Cassandra type value written to thread parent messages
// when they are soft-deleted, signalling to the frontend that the thread's
// parent message has been removed.
const MessageTypeRemoved = "message_removed"

// Thread-parent delete queries — identical to the regular delete queries but also
// set type = MessageTypeRemoved. Used when msg.TCount != nil && *msg.TCount > 0.
const (
	deleteThreadParentMsgByIDCAS = "UPDATE messages_by_id SET deleted = true, enc_payload = null, enc_meta = null, type = '" + MessageTypeRemoved + "', updated_at = ? WHERE message_id = ? AND created_at = ? IF deleted != true"
	deleteThreadParentMsgByRoom  = "UPDATE messages_by_room SET deleted = true, enc_payload = null, enc_meta = null, type = '" + MessageTypeRemoved + "', updated_at = ? WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?"
	deleteThreadParentThreadMsg  = "UPDATE thread_messages_by_thread SET deleted = true, enc_payload = null, enc_meta = null, type = '" + MessageTypeRemoved + "', updated_at = ? WHERE thread_room_id = ? AND created_at = ? AND message_id = ?"
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

// readEncryptedFields fetches the existing row body from messages_by_id
// and produces an EncryptedFields struct that the caller can re-encrypt.
// When the row already has enc_payload set, that ciphertext is decrypted
// and returned. When it doesn't (legacy plaintext row written before the
// at-rest rollout), the plaintext body columns are promoted into the
// returned EncryptedFields so the next encrypt produces a complete
// bundle — without this, editing a legacy row would silently drop its
// attachments / card / sys_msg_data because ApplyDecryptedFields
// unconditionally overwrites those fields with the (empty) bundle.
func (r *Repository) readEncryptedFields(ctx context.Context, msg *models.Message) (atrest.EncryptedFields, error) {
	var (
		encPayload  []byte
		encMeta     *cassmodel.EncMeta
		msgText     string
		attachments [][]byte
		card        *cassmodel.Card
		cardAction  *cassmodel.CardAction
		sysMsgData  []byte
		quoted      *cassmodel.QuotedParentMessage
	)
	err := r.session.Query(
		`SELECT enc_payload, enc_meta, msg, attachments, card, card_action, sys_msg_data, quoted_parent_message
		 FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msg.MessageID, msg.CreatedAt,
	).WithContext(ctx).Scan(&encPayload, &encMeta, &msgText, &attachments, &card, &cardAction, &sysMsgData, &quoted)
	if err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return atrest.EncryptedFields{}, ErrMessageNotFound
		}
		return atrest.EncryptedFields{}, fmt.Errorf("read existing fields for message %s: %w", msg.MessageID, err)
	}
	if len(encPayload) > 0 {
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
	// Legacy plaintext row — promote the plaintext body columns into the
	// bundle so the subsequent encrypt carries them forward. The legacy
	// columns become stale after the UPDATE but are harmless: the read
	// path branches on enc_payload != nil and ApplyDecryptedFields will
	// overwrite the structScan'd plaintext fields with the bundle's.
	fields := atrest.EncryptedFields{
		Msg:         msgText,
		Attachments: attachments,
		Card:        card,
		CardAction:  cardAction,
		SysMsgData:  sysMsgData,
	}
	if quoted != nil && (quoted.Msg != "" || len(quoted.Attachments) > 0) {
		fields.QuotedParentContent = &atrest.QuotedParentEncrypted{
			Msg:         quoted.Msg,
			Attachments: quoted.Attachments,
		}
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

// editInMessagesByIDCAS runs the LWT edit on the canonical row. Returns
// applied=false when the row is missing OR deleted=true; the caller maps
// !applied to ErrMessageNotFound so a TOCTOU between findMessage and this
// call (or a stale messageID/created_at) can't materialise a ghost row or
// resurrect a tombstoned message.
func (r *Repository) editInMessagesByIDCAS(ctx context.Context, msg *models.Message, ep editPayload, editedAt time.Time) (bool, error) {
	var prevDeleted bool
	if ep.payload == nil {
		applied, err := r.session.Query(
			editMsgByIDCAS,
			ep.plain, editedAt, editedAt, msg.MessageID, msg.CreatedAt,
		).WithContext(ctx).ScanCAS(&prevDeleted)
		return applied, err
	}
	applied, err := r.session.Query(
		editMsgByIDEncryptedCAS,
		ep.payload, ep.meta, editedAt, editedAt, msg.MessageID, msg.CreatedAt,
	).WithContext(ctx).ScanCAS(&prevDeleted)
	return applied, err
}

func (r *Repository) editInMessagesByRoom(ctx context.Context, msg *models.Message, ep editPayload, editedAt time.Time) error {
	b := r.bucket.Of(msg.CreatedAt)
	return r.editOne(ctx, editMsgByRoom, editMsgByRoomEncrypted, ep, editedAt, msg.RoomID, b, msg.CreatedAt, msg.MessageID)
}

// editInThreadMessagesByThread edits the thread mirror row. thread_messages_by_thread
// is partitioned by thread_room_id alone, so room_id/bucket no longer enter the key.
func (r *Repository) editInThreadMessagesByThread(ctx context.Context, msg *models.Message, ep editPayload, editedAt time.Time) error {
	return r.editOne(ctx, editThreadMsg, editThreadMsgEncrypted, ep, editedAt, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID)
}

func (r *Repository) editInPinnedMessagesByRoom(ctx context.Context, msg *models.Message, ep editPayload, editedAt time.Time) error {
	return r.editOne(ctx, editPinnedMsg, editPinnedMsgEncrypted, ep, editedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID)
}

func (r *Repository) deleteInMessagesByRoom(ctx context.Context, q string, msg *models.Message, deletedAt time.Time) error {
	b := r.bucket.Of(msg.CreatedAt)
	return r.session.Query(q, deletedAt, msg.RoomID, b, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
}

func (r *Repository) deleteInPinnedMessagesByRoom(ctx context.Context, q string, msg *models.Message, deletedAt time.Time) error {
	return r.session.Query(q, deletedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID).WithContext(ctx).Exec()
}

func (r *Repository) UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	if msg.ThreadParentID != "" && msg.ThreadRoomID == "" {
		return fmt.Errorf("edit thread message %s: ThreadParentID %q is set but ThreadRoomID is empty", msg.MessageID, msg.ThreadParentID)
	}

	// Two gates work together to keep edits from materialising ghost rows
	// or resurrecting tombstones:
	//
	//   1. Existence pre-check (cipher-disabled path) / readEncryptedFields
	//      (cipher-enabled path) catches a non-existent (message_id,
	//      created_at). This is necessary because Cassandra's LWT
	//      `IF deleted != true` evaluates NULL columns on a missing row
	//      as `NULL != true → true`, so the CAS would APPLY and create a
	//      ghost row. Without this check the LWT alone doesn't gate the
	//      not-found case.
	//
	//   2. LWT `IF deleted != true` on the CAS edit catches the TOCTOU
	//      where a concurrent SoftDeleteMessage lands between the
	//      existence check and the UPDATE — !applied means the row was
	//      tombstoned in that window.
	if r.cipher == nil {
		var deleted bool
		if err := r.session.Query(
			`SELECT deleted FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			msg.MessageID, msg.CreatedAt,
		).WithContext(ctx).Scan(&deleted); err != nil {
			if errors.Is(err, gocql.ErrNotFound) {
				return fmt.Errorf("edit message %s: %w", msg.MessageID, ErrMessageNotFound)
			}
			return fmt.Errorf("verify message %s exists: %w", msg.MessageID, err)
		}
	}

	ep, err := r.buildEditPayload(ctx, msg, newMsg)
	if err != nil {
		if errors.Is(err, ErrMessageNotFound) {
			return fmt.Errorf("edit message %s: %w", msg.MessageID, ErrMessageNotFound)
		}
		return fmt.Errorf("prepare edit payload for message %s: %w", msg.MessageID, err)
	}

	applied, err := r.editInMessagesByIDCAS(ctx, msg, ep, editedAt)
	if err != nil {
		return fmt.Errorf("update messages_by_id for message %s: %w", msg.MessageID, err)
	}
	if !applied {
		return fmt.Errorf("edit message %s: %w", msg.MessageID, ErrMessageNotFound)
	}

	if msg.ThreadParentID == "" {
		if err := r.editInMessagesByRoom(ctx, msg, ep, editedAt); err != nil {
			return fmt.Errorf("update messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
		}
	} else {
		if err := r.editInThreadMessagesByThread(ctx, msg, ep, editedAt); err != nil {
			return fmt.Errorf("update thread_messages_by_thread for message %s thread %s: %w", msg.MessageID, msg.ThreadRoomID, err)
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
		if err := r.session.Query(threadMsgQ, deletedAt, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec(); err != nil {
			return time.Time{}, false, fmt.Errorf("update thread_messages_by_thread for message %s thread %s: %w", msg.MessageID, msg.ThreadRoomID, err)
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
	parentBucket := r.bucket.Of(parentCreatedAt)
	if err := r.session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?`,
		msg.RoomID, parentBucket, parentCreatedAt, parentID,
	).WithContext(ctx).Scan(&tcount); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read tcount for parent %s in messages_by_room: %w", parentID, err)
	}
	if err := casDecrement(casMaxRetries, tcount, func(newVal int, expected *int) (bool, *int, error) {
		var current *int
		applied, err := r.session.Query(
			`UPDATE messages_by_room SET tcount = ? WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ? IF tcount = ?`,
			newVal, msg.RoomID, parentBucket, parentCreatedAt, parentID, expected,
		).WithContext(ctx).ScanCAS(&current)
		return applied, current, err
	}); err != nil {
		return fmt.Errorf("cas tcount decrement in messages_by_room for parent %s: %w", parentID, err)
	}
	return nil
}
