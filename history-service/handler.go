package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/nats-io/nats.go"
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
	parts := strings.Split(msg.Subject, ".")
	if len(parts) < 8 {
		natsutil.ReplyError(msg, "invalid subject")
		return
	}
	userID := parts[2]
	roomID := parts[5]

	resp, err := h.handleHistory(userID, roomID, msg.Data)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	msg.Respond(resp)
}

func (h *Handler) handleHistory(userID, roomID string, data []byte) ([]byte, error) {
	var req model.HistoryRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	ctx := context.Background()

	// Verify subscription
	sub, err := h.store.GetSubscription(ctx, userID, roomID)
	if err != nil {
		return nil, fmt.Errorf("not subscribed: %w", err)
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
