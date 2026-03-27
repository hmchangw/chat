package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

type Handler struct {
	store HistoryStore
}

func NewHandler(store HistoryStore) *Handler {
	return &Handler{store: store}
}

// NatsHandleHistory handles NATS request/reply for message history.
// Subject: chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history
func (h *Handler) NatsHandleHistory(msg *nats.Msg) {
	username, roomID, ok := subject.ParseUserRoomSubject(msg.Subject)
	if !ok {
		natsutil.ReplyError(msg, "invalid subject")
		return
	}

	resp, err := h.handleHistory(username, roomID, msg.Data)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	if err := msg.Respond(resp); err != nil {
		slog.Error("failed to respond to message", "error", err)
	}
}

func (h *Handler) handleHistory(username, roomID string, data []byte) ([]byte, error) {
	ctx := context.Background()

	// Verify subscription before unmarshalling request data for performance
	sub, err := h.store.GetSubscription(ctx, username, roomID)
	if err != nil {
		return nil, fmt.Errorf("not subscribed: %w", err)
	}

	var req model.HistoryRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	since := sub.SharedHistorySince
	before := time.Now().UTC()

	// If "before" cursor is provided, parse it as a timestamp
	if req.Before != "" {
		parsed, err := time.Parse(time.RFC3339Nano, req.Before)
		if err == nil {
			before = parsed
		}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}

	// Fetch limit+1 to determine HasMore
	msgs, err := h.store.ListMessages(ctx, roomID, since, before, limit+1)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}

	hasMore := len(msgs) > limit
	if hasMore {
		msgs = msgs[:limit]
	}

	resp := model.HistoryResponse{
		Messages: msgs,
		HasMore:  hasMore,
	}
	return json.Marshal(resp)
}
