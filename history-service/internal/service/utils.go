package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// getAccessSince verifies the user is subscribed to the room and returns the
// historySharedSince lower bound (nil = full access).
func (s *HistoryService) getAccessSince(ctx context.Context, username, roomID string) (*time.Time, error) {
	accessSince, subscribed, err := s.subscriptions.GetHistorySharedSince(ctx, username, roomID)
	if err != nil {
		return nil, fmt.Errorf("checking subscription: %w", err)
	}
	if !subscribed {
		return nil, natsrouter.ErrForbidden("not subscribed to room")
	}
	return accessSince, nil
}

// millisToTime converts a UTC millisecond timestamp to time.Time.
// Returns zero time if millis is nil (meaning "not provided").
func millisToTime(millis *int64) time.Time {
	if millis == nil {
		return time.Time{}
	}
	return time.UnixMilli(*millis).UTC()
}

func parsePageRequest(cursor string, limit int) (cassrepo.PageRequest, error) {
	q, err := cassrepo.ParsePageRequest(cursor, limit)
	if err != nil {
		slog.Error("invalid pagination cursor", "error", err, "cursor", cursor)
		return cassrepo.PageRequest{}, natsrouter.ErrBadRequest("invalid pagination cursor")
	}
	return q, nil
}

// findMessage looks up a message using the full Cassandra primary key.
// Returns a user-facing error if messageID or createdAtMillis is missing.
func (s *HistoryService) findMessage(ctx context.Context, roomID, messageID string, createdAtMillis int64) (*models.Message, error) {
	if messageID == "" {
		return nil, natsrouter.ErrBadRequest("messageId is required")
	}
	if createdAtMillis == 0 {
		return nil, natsrouter.ErrBadRequest("createdAt is required")
	}
	msg, err := s.messages.GetMessage(ctx, roomID, time.UnixMilli(createdAtMillis).UTC(), messageID)
	if err != nil {
		return nil, fmt.Errorf("finding message: %w", err)
	}
	if msg == nil {
		return nil, natsrouter.ErrNotFound("message not found")
	}
	return msg, nil
}

// derefTime safely dereferences a *time.Time, returning zero time if nil.
func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// timeMax returns the later of two timestamps. Zero values are ignored.
func timeMax(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}
	if a.After(b) {
		return a
	}
	return b
}
