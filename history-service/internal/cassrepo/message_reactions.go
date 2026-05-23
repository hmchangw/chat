package cassrepo

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/hmchangw/chat/pkg/model/cassandra"
)

// ReactionMap is a per-message reactions view: emoji -> users who reacted.
type ReactionMap = map[string][]cassandra.Participant

// GetReactionsByMessageID returns all reactions on a single message as
// emoji -> users. Returns an empty ReactionMap (not nil) when the message
// has no reactions; never returns gocql.ErrNotFound for an empty partition.
//
// The singular variant exists alongside GetReactionsByMessageIDs to skip the
// errgroup + semaphore setup on the hot single-message path (GetMessageByID).
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
		users = nil // gocql reuses the backing array otherwise
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("loading reactions for message %s: %w", messageID, err)
	}
	return out, nil
}

// GetReactionsByMessageIDs fans out parallel single-partition reads via
// errgroup. Each goroutine reads one message's reactions partition directly
// from the replica that owns it (token-aware routing). Returns
// messageID -> ReactionMap. Messages with no reactions are omitted from
// the returned map (callers treat absence as "no reactions"). Empty
// messageIDs returns an empty map without contacting Cassandra. Duplicate
// IDs in input are deduplicated before fan-out. First goroutine error
// cancels siblings via the errgroup-derived context.
func (r *Repository) GetReactionsByMessageIDs(ctx context.Context, messageIDs []string) (map[string]ReactionMap, error) {
	out := make(map[string]ReactionMap)
	if len(messageIDs) == 0 {
		return out, nil
	}

	// Dedupe.
	seen := make(map[string]struct{}, len(messageIDs))
	ids := make([]string, 0, len(messageIDs))
	for _, id := range messageIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, r.reactionsConcurrency)
	var mu sync.Mutex

	for _, id := range ids {
		id := id
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
			case <-gctx.Done():
				return gctx.Err()
			}
			defer func() { <-sem }()

			reactions, err := r.GetReactionsByMessageID(gctx, id)
			if err != nil {
				return err
			}
			if len(reactions) == 0 {
				return nil
			}
			mu.Lock()
			out[id] = reactions
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("loading reactions for messages: %w", err)
	}
	return out, nil
}
