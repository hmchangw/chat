package cassrepo

import (
	"context"
	"fmt"

	"github.com/hmchangw/chat/history-service/internal/models"
)

const messageByIDQuery = "SELECT " + baseColumns + ", pinned_at, pinned_by FROM messages_by_id"

// Returns (nil, nil) when not found.
func (r *Repository) GetMessageByID(ctx context.Context, messageID string) (*models.Message, error) {
	iter := r.session.Query(
		messageByIDQuery+` WHERE message_id = ? LIMIT 1`,
		messageID,
	).WithContext(ctx).Iter()

	var m models.Message
	found := structScan(iter, &m)
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("querying message by id %s: %w", messageID, err)
	}
	if !found {
		return nil, nil
	}
	return &m, nil
}

// Missing IDs are silently omitted; order is not guaranteed.
func (r *Repository) GetMessagesByIDs(ctx context.Context, messageIDs []string) ([]models.Message, error) {
	if len(messageIDs) == 0 {
		return []models.Message{}, nil
	}
	iter := r.session.Query(
		messageByIDQuery+` WHERE message_id IN ?`,
		messageIDs,
	).WithContext(ctx).Iter()
	messages := scanMsgsFromIter(iter)
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("querying messages by IDs: %w", err)
	}
	return messages, nil
}
