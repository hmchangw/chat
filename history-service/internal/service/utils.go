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

const unavailableQuoteMsg = "This message is unavailable"

// redactUnavailableQuotes replaces quoted-message previews that fall outside
// the caller's access window with a stub. For TShow messages — thread replies
// that are also surfaced in the main channel — the thread parent's actual
// Cassandra row is re-fetched and checked, because the snapshot stored on the
// reply could be stale or tampered. Thread parents are fetched in one batch,
// deduped.
func (s *HistoryService) redactUnavailableQuotes(ctx context.Context, msgs []models.Message, accessSince *time.Time) {
	if accessSince == nil {
		return
	}

	// Collect unique thread-parent IDs for tshow messages that have a quote
	// snapshot — no fetch needed for messages that would be skipped anyway.
	visitedParentIDs := make(map[string]struct{})
	var tshowParentIDs []string
	for i := range msgs {
		if msgs[i].TShow && msgs[i].ThreadParentID != "" && msgs[i].QuotedParentMessage != nil {
			if _, dup := visitedParentIDs[msgs[i].ThreadParentID]; !dup {
				visitedParentIDs[msgs[i].ThreadParentID] = struct{}{}
				tshowParentIDs = append(tshowParentIDs, msgs[i].ThreadParentID)
			}
		}
	}

	tshowParentCreatedAt := make(map[string]time.Time, len(tshowParentIDs))
	if len(tshowParentIDs) > 0 {
		tshowParents, err := s.messages.GetMessagesByIDs(ctx, tshowParentIDs)
		if err != nil {
			slog.Error("fetching thread parents for tshow redaction", "error", err)
			// tshow path skipped for this batch; standard path still runs below.
		} else {
			for i := range tshowParents {
				tshowParentCreatedAt[tshowParents[i].MessageID] = tshowParents[i].CreatedAt
			}
		}
	}

	for i := range msgs {
		if msgs[i].QuotedParentMessage == nil {
			continue
		}

		tshowParentInaccessible := false
		if msgs[i].TShow && msgs[i].ThreadParentID != "" {
			if parentAt, ok := tshowParentCreatedAt[msgs[i].ThreadParentID]; ok {
				tshowParentInaccessible = parentAt.Before(*accessSince)
			}
		}

		if msgs[i].QuotedParentMessage.CreatedAt.Before(*accessSince) || tshowParentInaccessible {
			msgs[i].QuotedParentMessage = &models.QuotedParentMessage{Msg: unavailableQuoteMsg}
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
