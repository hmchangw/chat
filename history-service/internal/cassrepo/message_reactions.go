package cassrepo

import (
	"context"
	"fmt"

	"github.com/hmchangw/chat/pkg/model/cassandra"
)

// ReactionMap is a per-message reactions view: emoji -> users who reacted.
type ReactionMap = map[string][]cassandra.Participant

// GetReactionsByMessageID returns all reactions on a single message as
// emoji -> users. Returns an empty ReactionMap (not nil) when the message
// has no reactions; never returns gocql.ErrNotFound for an empty partition.
func (r *Repository) GetReactionsByMessageID(ctx context.Context, messageID string) (ReactionMap, error) {
	iter := r.session.Query(
		`SELECT emoji, users FROM message_reactions WHERE message_id = ?`,
		messageID,
	).WithContext(ctx).Iter()

	out := make(ReactionMap)
	var emoji string
	var users []cassandra.Participant
	for iter.Scan(&emoji, &users) {
		out[emoji] = users
		users = nil // gocql reuses the slice header otherwise
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("loading reactions for message %s: %w", messageID, err)
	}
	return out, nil
}
