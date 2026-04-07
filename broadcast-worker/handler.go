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
	Publish(ctx context.Context, subject string, data []byte) error
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

	// Collect all usernames for employee lookup (sender + mentioned)
	lookupUsernames := make([]string, 0, 1+len(mentionedUsernames))
	lookupUsernames = append(lookupUsernames, msg.Username)
	for _, u := range mentionedUsernames {
		if u != msg.Username {
			lookupUsernames = append(lookupUsernames, u)
		}
	}

	employeeMap := make(map[string]model.Employee)
	employees, err := h.store.FindEmployeesByAccountNames(ctx, lookupUsernames)
	if err != nil {
		slog.Warn("employee lookup failed, falling back to usernames", "error", err)
	} else {
		for _, emp := range employees {
			employeeMap[emp.AccountName] = emp
		}
	}

	clientMsg := buildClientMessage(&msg, employeeMap)
	mentionParticipants := buildMentionParticipants(mentionedUsernames, employeeMap)

	switch room.Type {
	case model.RoomTypeGroup:
		return h.publishGroupEvent(ctx, room, clientMsg, mentionAll, mentionParticipants)
	case model.RoomTypeDM:
		return h.publishDMEvents(ctx, room, clientMsg, mentionedUsernames)
	default:
		slog.Warn("unknown room type, skipping fan-out", "type", room.Type, "roomID", room.ID)
		return nil
	}
}

func (h *Handler) publishGroupEvent(ctx context.Context, room *model.Room, clientMsg *model.ClientMessage, mentionAll bool, mentions []model.Participant) error {
	evt := buildRoomEvent(room, clientMsg)
	evt.MentionAll = mentionAll
	if len(mentions) > 0 {
		evt.Mentions = mentions
	}

	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal group room event: %w", err)
	}
	return h.pub.Publish(ctx, subject.RoomEvent(room.ID), payload)
}

func (h *Handler) publishDMEvents(ctx context.Context, room *model.Room, clientMsg *model.ClientMessage, mentionedUsernames []string) error {
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

		evt := buildRoomEvent(room, clientMsg)
		evt.HasMention = hasMention

		payload, err := json.Marshal(evt)
		if err != nil {
			return fmt.Errorf("marshal DM event for user %s: %w", subs[i].User.Username, err)
		}
		if err := h.pub.Publish(ctx, subject.UserRoomEvent(subs[i].User.Username), payload); err != nil {
			slog.Error("publish DM event failed", "error", err, "username", subs[i].User.Username)
		}
	}
	return nil
}

func buildRoomEvent(room *model.Room, clientMsg *model.ClientMessage) model.RoomEvent {
	return model.RoomEvent{
		Type:      model.RoomEventNewMessage,
		RoomID:    room.ID,
		Timestamp: clientMsg.CreatedAt,
		RoomName:  room.Name,
		RoomType:  room.Type,
		SiteID:    room.SiteID,
		UserCount: room.UserCount,
		LastMsgAt: clientMsg.CreatedAt,
		LastMsgID: clientMsg.ID,
		Message:   clientMsg,
	}
}

func buildClientMessage(msg *model.Message, employeeMap map[string]model.Employee) *model.ClientMessage {
	sender := model.Participant{
		UserID:   msg.UserID,
		Username: msg.Username,
	}
	if emp, ok := employeeMap[msg.Username]; ok {
		sender.ChineseName = emp.Name
		sender.EngName = emp.EngName
	} else {
		sender.ChineseName = msg.Username
		sender.EngName = msg.Username
	}
	return &model.ClientMessage{
		Message: *msg,
		Sender:  &sender,
	}
}

func buildMentionParticipants(mentionedUsernames []string, employeeMap map[string]model.Employee) []model.Participant {
	if len(mentionedUsernames) == 0 {
		return nil
	}
	participants := make([]model.Participant, len(mentionedUsernames))
	for i, username := range mentionedUsernames {
		p := model.Participant{Username: username}
		if emp, ok := employeeMap[username]; ok {
			p.ChineseName = emp.Name
			p.EngName = emp.EngName
		} else {
			p.ChineseName = username
			p.EngName = username
		}
		participants[i] = p
	}
	return participants
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
