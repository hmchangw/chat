package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// MemberLookup reads room membership from a data store.
type MemberLookup interface {
	ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
}

// Publisher abstracts NATS publishing so the handler is testable.
type Publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

// Handler processes MESSAGES_CANONICAL messages and sends notifications.
type Handler struct {
	members MemberLookup
	pub     Publisher
}

func NewHandler(members MemberLookup, pub Publisher) *Handler {
	return &Handler{members: members, pub: pub}
}

// HandleMessage processes a single JetStream message payload.
func (h *Handler) HandleMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	if evt.Event == model.EventReacted {
		return h.handleReaction(ctx, &evt)
	}

	subs, err := h.members.ListSubscriptions(ctx, evt.Message.RoomID)
	if err != nil {
		return fmt.Errorf("list subscriptions for room %s: %w", evt.Message.RoomID, err)
	}

	notif := model.NotificationEvent{
		Type:      "new_message",
		RoomID:    evt.Message.RoomID,
		Message:   evt.Message,
		Timestamp: time.Now().UTC().UnixMilli(),
	}

	notifData, err := natsutil.MarshalResponse(notif)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	senderID := evt.Message.UserID

	for i := range subs {
		if subs[i].User.ID == senderID {
			continue
		}
		subj := subject.Notification(subs[i].User.Account)
		if err := h.pub.Publish(ctx, subj, notifData); err != nil {
			slog.Error("publish notification failed", "error", err, "account", subs[i].User.Account)
		}
	}

	return nil
}

// handleReaction notifies the message author when someone reacts to their
// message. Only the "added" action triggers a notification — un-reacts are
// silent. Self-reactions (actor == author) are also silent.
func (h *Handler) handleReaction(ctx context.Context, evt *model.MessageEvent) error {
	if evt.ReactionDelta == nil {
		return fmt.Errorf("reacted event missing ReactionDelta")
	}
	if evt.ReactionDelta.Action != "added" {
		return nil
	}
	authorAccount := evt.Message.UserAccount
	if authorAccount == "" || authorAccount == evt.ReactionDelta.Actor.Account {
		return nil
	}

	notif := model.NotificationEvent{
		Type:          "reaction",
		RoomID:        evt.Message.RoomID,
		Message:       evt.Message,
		ReactionDelta: evt.ReactionDelta,
		Timestamp:     time.Now().UTC().UnixMilli(),
	}
	data, err := natsutil.MarshalResponse(notif)
	if err != nil {
		return fmt.Errorf("marshal reaction notification: %w", err)
	}
	if err := h.pub.Publish(ctx, subject.Notification(authorAccount), data); err != nil {
		return fmt.Errorf("publish reaction notification to %s: %w", authorAccount, err)
	}
	return nil
}
