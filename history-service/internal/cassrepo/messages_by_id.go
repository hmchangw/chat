package cassrepo

import (
	"context"
	"errors"
	"fmt"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/history-service/internal/models"
)

// messageByIDExtraColumns are only present in messages_by_id, not messages_by_room.
const messageByIDExtraColumns = ", pinned_at, pinned_by"

const messageByIDQuery = "SELECT " + baseColumns + messageByIDExtraColumns + " FROM messages_by_id"

func messageByIDScanDest(m *models.Message) []any {
	return append(baseScanDest(m), &m.PinnedAt, &m.PinnedBy)
}

func scanMessagesByID(iter *gocql.Iter) []models.Message { return scanWith(iter, messageByIDScanDest) }

// GetMessageByID returns a single message by ID, or (nil, nil) if not found.
func (r *Repository) GetMessageByID(ctx context.Context, messageID string) (*models.Message, error) {
	var m models.Message
	err := r.session.Query(
		messageByIDQuery+` WHERE message_id = ?`,
		messageID,
	).WithContext(ctx).Scan(messageByIDScanDest(&m)...)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying message by id: %w", err)
	}
	return &m, nil
}

// GetMessagesByIDs returns messages for the given IDs. Missing IDs are silently
// omitted and order is not guaranteed. Returns an empty slice when messageIDs is empty.
func (r *Repository) GetMessagesByIDs(ctx context.Context, messageIDs []string) ([]models.Message, error) {
	if len(messageIDs) == 0 {
		return []models.Message{}, nil
	}
	iter := r.session.Query(
		messageByIDQuery+` WHERE message_id IN ?`,
		messageIDs,
	).WithContext(ctx).Iter()
	messages := scanMessagesByID(iter)
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("querying messages by IDs: %w", err)
	}
	return messages, nil
}
