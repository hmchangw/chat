package cassrepo

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/history-service/internal/models"
)

const (
	// readReactionsForShortcode loads the current reactors for a shortcode
	// from messages_by_id so we can compute the next state under LWT.
	readReactionsForShortcode = `SELECT reactions FROM messages_by_id WHERE message_id = ? AND created_at = ?`

	// LWT pattern: set the per-key map value conditionally. If the current
	// value matches our expected snapshot, the write applies; otherwise the
	// caller reloads and retries. Setting an entry to an empty set is how
	// we encode "no reactors" without removing the map key — see
	// ToggleReaction for the rationale.
	casReactionMsgByID = `UPDATE messages_by_id SET reactions[?] = ?, updated_at = ? WHERE message_id = ? AND created_at = ? IF reactions[?] = ?`

	// Mirror-table writes are non-LWT — messages_by_id is the source of
	// truth for the toggle decision; mirrors echo the resolved value.
	writeReactionMsgByRoom = `UPDATE messages_by_room SET reactions[?] = ?, updated_at = ? WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?`
	writeReactionThreadMsg = `UPDATE thread_messages_by_room SET reactions[?] = ?, updated_at = ? WHERE room_id = ? AND bucket = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`
	writeReactionPinnedMsg = `UPDATE pinned_messages_by_room SET reactions[?] = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
)

// ToggleReaction applies a single user's toggle for the given shortcode on a
// message. It loads the current reactors for that shortcode from
// messages_by_id, decides add vs remove by membership-on-id, and CAS-writes
// the new set; on CAS miss it reloads and retries up to casMaxRetries. After
// the CAS applies, it mirrors the new value to messages_by_room or
// thread_messages_by_room (and pinned_messages_by_room when applicable).
//
// The returned action is "added" or "removed". On CAS exhaustion the function
// returns a non-nil error and the caller MUST NOT publish a canonical event
// (the row state is unchanged and a publish would be a phantom).
//
//nolint:gocritic // hugeParam: actor is intentionally passed by value to match Sender/Mentions in models.Message
func (r *Repository) ToggleReaction(ctx context.Context, msg *models.Message, shortcode string, actor models.Participant, reactedAt time.Time) (string, error) {
	if msg.ThreadParentID != "" && msg.ThreadRoomID == "" {
		return "", fmt.Errorf("react thread message %s: ThreadParentID %q is set but ThreadRoomID is empty", msg.MessageID, msg.ThreadParentID)
	}

	for range casMaxRetries {
		current, err := r.readReactors(ctx, msg.MessageID, msg.CreatedAt, shortcode)
		if err != nil {
			return "", fmt.Errorf("read reactors for message %s shortcode %q: %w", msg.MessageID, shortcode, err)
		}

		alreadyReacted := participantSetContainsID(current, actor.ID)
		var next []models.Participant
		var action string
		if alreadyReacted {
			next = removeParticipantByID(current, actor.ID)
			action = "removed"
		} else {
			next = append(append([]models.Participant{}, current...), actor)
			action = "added"
		}

		applied, err := r.casReactionByID(ctx, msg, shortcode, current, next, reactedAt)
		if err != nil {
			return "", fmt.Errorf("cas reaction on messages_by_id for message %s: %w", msg.MessageID, err)
		}
		if !applied {
			continue
		}

		if err := r.mirrorReaction(ctx, msg, shortcode, next, reactedAt); err != nil {
			return "", fmt.Errorf("mirror reaction for message %s: %w", msg.MessageID, err)
		}
		return action, nil
	}
	return "", fmt.Errorf("cas reaction toggle exceeded %d retries for message %s shortcode %q", casMaxRetries, msg.MessageID, shortcode)
}

// readReactors returns the current reactor set for a shortcode on a message
// from messages_by_id. A nil/empty set is returned when the key is absent or
// the set is empty — both states are equivalent for the toggle decision.
func (r *Repository) readReactors(ctx context.Context, messageID string, createdAt time.Time, shortcode string) ([]models.Participant, error) {
	var reactions map[string][]models.Participant
	if err := r.session.Query(readReactionsForShortcode, messageID, createdAt).
		WithContext(ctx).Scan(&reactions); err != nil {
		if err == gocql.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	return reactions[shortcode], nil
}

// casReactionByID writes the new reactor set under an LWT that asserts the
// current value matches our snapshot. gocql lets us pass a typed slice
// directly for SET<FROZEN<UDT>> columns.
func (r *Repository) casReactionByID(ctx context.Context, msg *models.Message, shortcode string, expected, next []models.Participant, reactedAt time.Time) (bool, error) {
	var existing []models.Participant
	applied, err := r.session.Query(
		casReactionMsgByID,
		shortcode, next, reactedAt, msg.MessageID, msg.CreatedAt, shortcode, expected,
	).WithContext(ctx).ScanCAS(&existing)
	return applied, err
}

// mirrorReaction writes the post-toggle reactor set to the room/thread/pinned
// mirror tables that apply to this message. messages_by_room and
// thread_messages_by_room are mutually exclusive based on whether this is a
// thread reply; pinned_messages_by_room is independent and additive.
func (r *Repository) mirrorReaction(ctx context.Context, msg *models.Message, shortcode string, next []models.Participant, reactedAt time.Time) error {
	if msg.ThreadParentID == "" {
		b := r.bucket.Of(msg.CreatedAt)
		if err := r.session.Query(
			writeReactionMsgByRoom,
			shortcode, next, reactedAt, msg.RoomID, b, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
		}
	} else {
		b := r.bucket.Of(msg.CreatedAt)
		if err := r.session.Query(
			writeReactionThreadMsg,
			shortcode, next, reactedAt, msg.RoomID, b, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update thread_messages_by_room for message %s room %s thread %s: %w", msg.MessageID, msg.RoomID, msg.ThreadRoomID, err)
		}
	}

	if msg.PinnedAt != nil {
		if err := r.session.Query(
			writeReactionPinnedMsg,
			shortcode, next, reactedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update pinned_messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
		}
	}

	return nil
}

func participantSetContainsID(set []models.Participant, id string) bool {
	for i := range set {
		if set[i].ID == id {
			return true
		}
	}
	return false
}

func removeParticipantByID(set []models.Participant, id string) []models.Participant {
	out := make([]models.Participant, 0, len(set))
	for i := range set {
		if set[i].ID == id {
			continue
		}
		out = append(out, set[i])
	}
	return out
}
