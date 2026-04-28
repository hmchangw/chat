package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// getAccessSince checks subscription and returns the historySharedSince lower bound (nil = full access).
func (s *HistoryService) getAccessSince(ctx context.Context, account, roomID string) (*time.Time, error) {
	accessSince, subscribed, err := s.subscriptions.GetHistorySharedSince(ctx, account, roomID)
	if err != nil {
		slog.Error("checking subscription", "error", err, "account", account, "roomID", roomID)
		return nil, natsrouter.ErrInternal("unable to verify room access")
	}
	if !subscribed {
		return nil, natsrouter.ErrForbidden("not subscribed to room")
	}
	return accessSince, nil
}

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

func (s *HistoryService) findMessage(ctx context.Context, roomID, messageID string) (*models.Message, error) {
	if messageID == "" {
		return nil, natsrouter.ErrBadRequest("messageId is required")
	}
	msg, err := s.messages.GetMessageByID(ctx, messageID)
	if err != nil {
		slog.Error("finding message", "error", err, "messageID", messageID)
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

// UnavailableQuoteMsg is written into QuotedParentMessage.Msg when the quoted message is outside the access window.
const UnavailableQuoteMsg = "This message is unavailable"

func redactUnavailableQuote(m *models.Message, accessSince *time.Time) {
	if m == nil || accessSince == nil {
		return
	}
	tmp := []models.Message{*m}
	redactUnavailableQuotes(tmp, accessSince)
	*m = tmp[0]
}

// For TShow replies, inaccessibility uses ThreadParentCreatedAt embedded at write time by message-worker.
func redactUnavailableQuotes(msgs []models.Message, accessSince *time.Time) {
	if accessSince == nil {
		return
	}
	for i := range msgs {
		q := msgs[i].QuotedParentMessage
		if q == nil {
			continue
		}
		tshowParentInaccessible := msgs[i].TShow &&
			q.ThreadParentID != "" &&
			q.ThreadParentCreatedAt != nil &&
			q.ThreadParentCreatedAt.Before(*accessSince)
		// Legacy TShow quotes where ThreadParentCreatedAt wasn't captured cannot be
		// verified against the access window — redact conservatively to prevent leaks.
		legacyTShowMissingParentTime := msgs[i].TShow &&
			q.ThreadParentID != "" &&
			q.ThreadParentCreatedAt == nil
		if q.CreatedAt.Before(*accessSince) || tshowParentInaccessible || legacyTShowMissingParentTime {
			msgs[i].QuotedParentMessage = &models.QuotedParentMessage{Msg: UnavailableQuoteMsg}
		}
	}
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// timeMax returns the later of two timestamps; zero values are ignored.
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

// canModify reports whether the given account is authorized to edit or
// soft-delete the target message. Under sender-only authorization, the caller
// must be the message's original sender; room-owner roles are not honored.
// The helper treats an empty account on either side as unauthorized to avoid
// matching messages with missing sender data.
func canModify(msg *models.Message, account string) bool {
	if account == "" {
		return false
	}
	if msg.Sender.Account == "" {
		return false
	}
	return msg.Sender.Account == account
}
