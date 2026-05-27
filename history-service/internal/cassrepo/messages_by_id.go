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
	found, scanErr := structScan(iter, &m)
	if closeErr := iter.Close(); closeErr != nil {
		return nil, fmt.Errorf("querying message by id %s: %w", messageID, closeErr)
	}
	if scanErr != nil {
		return nil, fmt.Errorf("querying message by id %s: %w", messageID, scanErr)
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
	messages, scanErr := scanMsgsFromIter(iter)
	if closeErr := iter.Close(); closeErr != nil {
		return nil, fmt.Errorf("querying messages by IDs: %w", closeErr)
	}
	if scanErr != nil {
		return nil, fmt.Errorf("querying messages by IDs: %w", scanErr)
	}
	return messages, nil
}
