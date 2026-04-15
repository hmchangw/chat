package service

import (
	"log/slog"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// getAccessSince verifies the user is subscribed to the room and returns the
// historySharedSince lower bound (nil = full access).
func (s *HistoryService) getAccessSince(c *natsrouter.Context, account, roomID string) (*time.Time, error) {
	accessSince, subscribed, err := s.subscriptions.GetHistorySharedSince(c, account, roomID)
	if err != nil {
		slog.Error("checking subscription", "error", err, "account", account, "roomID", roomID, "requestID", c.RequestID())
		return nil, natsrouter.ErrInternal("unable to verify room access")
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

func parsePageRequest(c *natsrouter.Context, cursor string, limit int) (cassrepo.PageRequest, error) {
	q, err := cassrepo.ParsePageRequest(cursor, limit)
	if err != nil {
		slog.Error("invalid pagination cursor", "error", err, "cursor", cursor, "requestID", c.RequestID())
		return cassrepo.PageRequest{}, natsrouter.ErrBadRequest("invalid pagination cursor")
	}
	return q, nil
}

// findMessage looks up a message by ID from the messages_by_id lookup table.
// Verifies the message belongs to the expected room.
func (s *HistoryService) findMessage(c *natsrouter.Context, roomID, messageID string) (*models.Message, error) {
	if messageID == "" {
		return nil, natsrouter.ErrBadRequest("messageId is required")
	}
	msg, err := s.messages.GetMessageByID(c, messageID)
	if err != nil {
		slog.Error("finding message", "error", err, "messageID", messageID, "requestID", c.RequestID())
		return nil, natsrouter.ErrInternal("failed to retrieve message")
	}
	if msg == nil {
		return nil, natsrouter.ErrNotFound("message not found")
	}
	if msg.RoomID != roomID {
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
