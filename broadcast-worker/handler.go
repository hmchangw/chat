package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// Publisher abstracts NATS publishing so the handler is testable.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Handler processes MESSAGES_CANONICAL messages and broadcasts room events.
type Handler struct {
	store Store
	pub   Publisher
}

func NewHandler(store Store, pub Publisher) *Handler {
	return &Handler{store: store, pub: pub}
}

// HandleMessage processes a single MESSAGES_CANONICAL message payload.
func (h *Handler) HandleMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	msg := evt.Message

	room, err := h.store.GetRoom(ctx, msg.RoomID)
	if err != nil {
		return fmt.Errorf("get room %s: %w", msg.RoomID, err)
	}

	mentionAll := detectMentionAll(msg.Content)
	mentionedUsernames := extractMentionedUsernames(msg.Content)

	if err := h.store.UpdateRoomOnNewMessage(ctx, room.ID, msg.ID, msg.CreatedAt, mentionAll); err != nil {
		return fmt.Errorf("update room on new message: %w", err)
	}

	if len(mentionedUsernames) > 0 {
		if err := h.store.SetSubscriptionMentions(ctx, room.ID, mentionedUsernames); err != nil {
			return fmt.Errorf("set subscription mentions: %w", err)
		}
	}

	switch room.Type {
	case model.RoomTypeGroup:
		return h.publishGroupEvent(room, &msg, mentionAll, mentionedUsernames)
	case model.RoomTypeDM:
		return h.publishDMEvents(ctx, room, &msg, mentionedUsernames)
	default:
		slog.Warn("unknown room type, skipping fan-out", "type", room.Type, "roomID", room.ID)
		return nil
	}
}

func (h *Handler) publishGroupEvent(room *model.Room, msg *model.Message, mentionAll bool, mentions []string) error {
	evt := buildRoomEvent(room, msg)
	evt.MentionAll = mentionAll
	if len(mentions) > 0 {
		evt.Mentions = mentions
	}

	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal group room event: %w", err)
	}
	return h.pub.Publish(subject.RoomEvent(room.ID), payload)
}

func (h *Handler) publishDMEvents(ctx context.Context, room *model.Room, msg *model.Message, mentionedUsernames []string) error {
	subs, err := h.store.ListSubscriptions(ctx, room.ID)
	if err != nil {
		return fmt.Errorf("list subscriptions for DM room %s: %w", room.ID, err)
	}

	mentionSet := make(map[string]struct{}, len(mentionedUsernames))
	for _, name := range mentionedUsernames {
		mentionSet[name] = struct{}{}
	}

	for i := range subs {
		_, hasMention := mentionSet[subs[i].User.Username]

		evt := buildRoomEvent(room, msg)
		evt.HasMention = hasMention
		evt.Message = msg

		payload, err := json.Marshal(evt)
		if err != nil {
			return fmt.Errorf("marshal DM event for user %s: %w", subs[i].User.Username, err)
		}
		if err := h.pub.Publish(subject.UserRoomEvent(subs[i].User.Username), payload); err != nil {
			slog.Error("publish DM event failed", "error", err, "username", subs[i].User.Username)
		}
	}
	return nil
}

func buildRoomEvent(room *model.Room, msg *model.Message) model.RoomEvent {
	return model.RoomEvent{
		Type:      model.RoomEventNewMessage,
		RoomID:    room.ID,
		Timestamp: msg.CreatedAt,
		RoomName:  room.Name,
		RoomType:  room.Type,
		Origin:    room.Origin,
		UserCount: room.UserCount,
		LastMsgAt: msg.CreatedAt,
		LastMsgID: msg.ID,
	}
}

func detectMentionAll(content string) bool {
	for _, token := range strings.Fields(content) {
		lower := strings.ToLower(token)
		if lower == "@all" || lower == "@here" {
			return true
		}
	}
	return false
}

func extractMentionedUsernames(content string) []string {
	seen := make(map[string]struct{})
	var usernames []string
	for _, token := range strings.Fields(content) {
		if !strings.HasPrefix(token, "@") || len(token) == 1 {
			continue
		}
		name := strings.ToLower(token[1:])
		if name == "all" || name == "here" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		usernames = append(usernames, name)
	}
	return usernames
}
