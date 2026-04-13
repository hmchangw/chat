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

// mentionRe matches @mention tokens in message content.
// Note: a bare @ not preceded by whitespace (e.g. "hello@bob") also matches —
// this is intentional per spec. Non-existent accounts are silently skipped by
// resolveMentions during the MongoDB lookup.
var mentionRe = regexp.MustCompile(`(^|\s|>?)@([0-9a-zA-Z_-]+(\.[0-9a-zA-Z_-]+)*(@[0-9a-zA-Z_-]+(\.[0-9a-zA-Z_-]+)*)?)`)

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

// resolveMentions parses @mention tokens from content, looks up real users in
// MongoDB, and returns them as Participants. @all is always included as a
// special entry without a DB lookup. Accounts not found in MongoDB are skipped.
// Returns nil when content has no mentions.
func (h *Handler) resolveMentions(ctx context.Context, content string) ([]model.Participant, error) {
	parsed := parseMentions(content)
	if len(parsed) == 0 {
		return nil, nil
	}

	var mentionAll bool
	var userAccounts []string
	for _, account := range parsed {
		if account == "all" {
			mentionAll = true
		} else {
			userAccounts = append(userAccounts, account)
		}
	}

	var participants []model.Participant

	if len(userAccounts) > 0 {
		users, err := h.userStore.FindUsersByAccounts(ctx, userAccounts)
		if err != nil {
			return nil, fmt.Errorf("find mentioned users: %w", err)
		}
		for _, u := range users {
			participants = append(participants, model.Participant{
				UserID:      u.ID,
				Account:     u.Account,
				ChineseName: u.ChineseName,
				EngName:     u.EngName,
			})
		}
	}

	if mentionAll {
		participants = append(participants, model.Participant{
			Account: "all",
			EngName: "all",
		})
	}

	if len(participants) == 0 {
		return nil, nil
	}
	return participants, nil
}

func (h *Handler) processMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	mentions, err := h.resolveMentions(ctx, evt.Message.Content)
	if err != nil {
		return fmt.Errorf("resolve mentions: %w", err)
	}
	evt.Message.Mentions = mentions

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
