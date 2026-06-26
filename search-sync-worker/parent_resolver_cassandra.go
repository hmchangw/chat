package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

// cassParentResolver reads a message's authoritative createdAt from
// messages_by_id. It satisfies parentCreatedAtResolver and mirrors
// message-worker's GetMessageCreatedAt point read — message history is owned by
// Cassandra, so this is the authoritative source for the thread-parent stamp.
type cassParentResolver struct {
	session *gocql.Session
}

func newCassParentResolver(session *gocql.Session) *cassParentResolver {
	return &cassParentResolver{session: session}
}

// GetMessageCreatedAt returns the parent message's createdAt. found=false with a
// nil error means no row matched (the parent isn't persisted yet).
func (r *cassParentResolver) GetMessageCreatedAt(ctx context.Context, messageID string) (time.Time, bool, error) {
	var createdAt time.Time
	if err := r.session.Query(
		`SELECT created_at FROM messages_by_id WHERE message_id = ? LIMIT 1`,
		messageID,
	).WithContext(ctx).Scan(&createdAt); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("get createdAt for message %s: %w", messageID, err)
	}
	return createdAt, true, nil
}
