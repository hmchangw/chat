package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
)

type Handler struct {
	store MessageStore
}

func NewHandler(store MessageStore) *Handler {
	return &Handler{store: store}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGE_SSOT stream.
func (h *Handler) HandleJetStreamMsg(msg jetstream.Msg) {
	ctx := context.Background()
	if err := h.processMessage(ctx, msg.Data()); err != nil {
		slog.Error("process message failed", "error", err)
		if err := msg.Nak(); err != nil {
			slog.Error("failed to nack message", "error", err)
		}
		return
	}

	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "err", err)
	}
}

func (h *Handler) processMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	if err := h.store.SaveMessage(ctx, &evt.Message); err != nil {
		return fmt.Errorf("save message: %w", err)
	}

	return nil
}
