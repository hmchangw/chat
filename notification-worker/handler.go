package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

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
	Publish(subject string, data []byte) error
}

// Handler processes fanout messages and sends notifications.
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

	subs, err := h.members.ListSubscriptions(ctx, evt.RoomID)
	if err != nil {
		return fmt.Errorf("list subscriptions for room %s: %w", evt.RoomID, err)
	}

	notif := model.NotificationEvent{
		Type:    "new_message",
		RoomID:  evt.RoomID,
		Message: evt.Message,
	}

	notifData, err := natsutil.MarshalResponse(notif)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	senderID := evt.Message.UserID

	for _, sub := range subs {
		if sub.UserID == senderID {
			continue
		}
		subj := subject.Notification(sub.UserID)
		if err := h.pub.Publish(subj, notifData); err != nil {
			slog.Error("publish notification failed", "error", err, "userID", sub.UserID)
		}
	}

	return nil
}
