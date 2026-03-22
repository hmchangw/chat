package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/nats-io/nats.go/jetstream"
)

type Handler struct {
	store   MessageStore
	siteID  string
	publish func(subj string, data []byte) error
}

func NewHandler(store MessageStore, siteID string, publish func(string, []byte) error) *Handler {
	return &Handler{store: store, siteID: siteID, publish: publish}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGES stream.
func (h *Handler) HandleJetStreamMsg(msg jetstream.Msg) {
	// Extract userID and roomID from subject: chat.user.{userID}.room.{roomID}.{siteID}.msg.send
	parts := strings.Split(msg.Subject(), ".")
	if len(parts) < 7 {
		slog.Warn("invalid subject", "subject", msg.Subject())
		msg.Ack()
		return
	}
	userID := parts[2]
	roomID := parts[4]
	siteID := parts[5]

	ctx := context.Background()
	replyData, err := h.processMessage(ctx, userID, roomID, siteID, msg.Data())
	if err != nil {
		slog.Error("process message failed", "error", err, "userID", userID, "roomID", roomID)
		msg.Ack()
		return
	}

	// Publish reply to sender's response subject
	if reqID := getRequestID(msg.Data()); reqID != "" {
		respSubj := subject.UserResponse(userID, reqID)
		if err := h.publish(respSubj, replyData); err != nil {
			slog.Error("reply publish failed", "error", err, "subject", respSubj)
		}
	}

	msg.Ack()
}

func (h *Handler) processMessage(ctx context.Context, userID, roomID, siteID string, data []byte) ([]byte, error) {
	var req model.SendMessageRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	// Validate subscription
	_, err := h.store.GetSubscription(ctx, userID, roomID)
	if err != nil {
		return nil, fmt.Errorf("not subscribed: %w", err)
	}

	// Create message
	now := time.Now().UTC()
	msg := model.Message{
		ID:        uuid.New().String(),
		RoomID:    roomID,
		UserID:    userID,
		Content:   req.Content,
		CreatedAt: now,
	}

	// Persist
	if err := h.store.SaveMessage(ctx, msg); err != nil {
		return nil, fmt.Errorf("save message: %w", err)
	}
	if err := h.store.UpdateRoomLastMessage(ctx, roomID, now); err != nil {
		slog.Warn("update room last message failed", "error", err, "roomID", roomID)
	}

	// Publish fanout event
	evt := model.MessageEvent{Message: msg, RoomID: roomID, SiteID: siteID}
	evtData, _ := json.Marshal(evt)
	fanoutSubj := subject.Fanout(siteID, roomID, msg.ID)
	if err := h.publish(fanoutSubj, evtData); err != nil {
		slog.Error("fanout publish failed", "error", err, "subject", fanoutSubj)
	}

	// Return message as reply
	return json.Marshal(msg)
}

func getRequestID(data []byte) string {
	var req model.SendMessageRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return ""
	}
	return req.RequestID
}
