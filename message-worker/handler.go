package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

type Handler struct {
	store      MessageStore
	siteID     string
	publishMsg func(msg *nats.Msg) error
}

func NewHandler(store MessageStore, siteID string, publishMsg func(*nats.Msg) error) *Handler {
	return &Handler{store: store, siteID: siteID, publishMsg: publishMsg}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGES stream.
func (h *Handler) HandleJetStreamMsg(msg jetstream.Msg) {
	// Extract userID and roomID from subject: chat.user.{userID}.room.{roomID}.{siteID}.msg.send
	parts := strings.Split(msg.Subject(), ".")
	if len(parts) < 7 {
		slog.Warn("invalid subject", "subject", msg.Subject())
		if err := msg.Ack(); err != nil {
			slog.Error("failed to ack message", "error", err)
		}
		return
	}
	username := parts[2]
	roomID := parts[4]
	siteID := parts[5]

	ctx := context.Background()
	replyData, err := h.processMessage(ctx, username, roomID, siteID, msg.Data())
	if err != nil {
		slog.Error("process message failed", "error", err, "username", username, "roomID", roomID)
		if err := msg.Ack(); err != nil {
			slog.Error("failed to ack message", "error", err)
		}
		return
	}

	// Publish reply to sender's response subject
	if reqID := getRequestID(msg.Data()); reqID != "" {
		respSubj := subject.UserResponse(username, reqID)
		if err := h.publishMsg(&nats.Msg{Subject: respSubj, Data: replyData}); err != nil {
			slog.Error("reply publish failed", "error", err, "subject", respSubj)
		}
	}

	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "err", err)
	}
}

func (h *Handler) processMessage(ctx context.Context, username, roomID, siteID string, data []byte) ([]byte, error) {
	var req model.SendMessageRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	// Validate subscription
	sub, err := h.store.GetSubscription(ctx, username, roomID)
	if err != nil {
		return nil, fmt.Errorf("not subscribed: %w", err)
	}

	// Create message
	now := time.Now().UTC()
	msg := model.Message{
		ID:        uuid.New().String(),
		RoomID:    roomID,
		UserID:    sub.User.ID,
		Content:   req.Content,
		CreatedAt: now,
	}

	// Persist
	if err := h.store.SaveMessage(ctx, &msg); err != nil {
		return nil, fmt.Errorf("save message: %w", err)
	}

	// Publish fanout event with Nats-Msg-Id for JetStream dedup
	evt := model.MessageEvent{Message: msg, RoomID: roomID, SiteID: siteID}
	evtData, _ := json.Marshal(evt)
	fanoutSubj := subject.Fanout(siteID, roomID)
	fanoutMsg := &nats.Msg{
		Subject: fanoutSubj,
		Data:    evtData,
		Header:  nats.Header{"Nats-Msg-Id": []string{msg.ID}},
	}
	if err := h.publishMsg(fanoutMsg); err != nil {
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
