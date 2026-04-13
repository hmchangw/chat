package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/userstore"
)

var mentionRe = regexp.MustCompile(`(^|\s|>?)@([0-9a-zA-Z\-_.]+(@[0-9a-zA-Z\-_.]+)?)`)

// parseMentions returns the unique mention targets found in content (without the @ prefix).
// Returns nil when content has no mentions.
func parseMentions(content string) []string {
	matches := mentionRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	var out []string
	for _, m := range matches {
		account := m[2]
		if _, exists := seen[account]; !exists {
			seen[account] = struct{}{}
			out = append(out, account)
		}
	}
	return out
}

type Handler struct {
	store     Store
	userStore userstore.UserStore
}

func NewHandler(store Store, userStore userstore.UserStore) *Handler {
	return &Handler{store: store, userStore: userStore}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGES_CANONICAL stream.
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
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

	user, err := h.userStore.FindUserByID(ctx, evt.Message.UserID)
	if err != nil {
		return fmt.Errorf("lookup user %s: %w", evt.Message.UserID, err)
	}

	sender := cassParticipant{
		ID:          user.ID,
		EngName:     user.EngName,
		CompanyName: user.ChineseName,
		Account:     evt.Message.UserAccount,
	}

	if evt.Message.ThreadParentMessageID != "" {
		if err := h.store.SaveThreadMessage(ctx, &evt.Message, &sender, evt.SiteID); err != nil {
			return fmt.Errorf("save thread message: %w", err)
		}
	} else {
		if err := h.store.SaveMessage(ctx, &evt.Message, &sender, evt.SiteID); err != nil {
			return fmt.Errorf("save message: %w", err)
		}
	}

	return nil
}
