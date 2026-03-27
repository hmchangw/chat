package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// InboxStore abstracts the data store operations needed by the inbox worker.
type InboxStore interface {
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	UpsertRoom(ctx context.Context, room *model.Room) error
}

// Publisher abstracts NATS publishing so the handler is testable.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Handler processes incoming cross-site OutboxEvent messages.
type Handler struct {
	store InboxStore
	pub   Publisher
}

// NewHandler creates a Handler with the given store and publisher.
func NewHandler(store InboxStore, pub Publisher) *Handler {
	return &Handler{store: store, pub: pub}
}

// HandleEvent processes a single JetStream message payload.
func (h *Handler) HandleEvent(ctx context.Context, data []byte) error {
	var evt model.OutboxEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal outbox event: %w", err)
	}

	switch evt.Type {
	case "member_added":
		return h.handleMemberAdded(ctx, evt)
	case "room_sync":
		return h.handleRoomSync(ctx, evt)
	default:
		slog.Warn("unknown event type, skipping", "type", evt.Type)
		return nil
	}
}

func (h *Handler) handleMemberAdded(ctx context.Context, evt model.OutboxEvent) error {
	var invite model.InviteMemberRequest
	if err := json.Unmarshal(evt.Payload, &invite); err != nil {
		return fmt.Errorf("unmarshal member_added payload: %w", err)
	}

	now := time.Now()
	sub := model.Subscription{
		ID:                 uuid.New().String(),
		User:               model.SubscriptionUser{ID: invite.InviteeID},
		RoomID:             invite.RoomID,
		SiteID:             invite.SiteID,
		Role:               model.RoleMember,
		SharedHistorySince: now,
		JoinedAt:           now,
	}

	if err := h.store.CreateSubscription(ctx, &sub); err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}

	updateEvt := model.SubscriptionUpdateEvent{
		UserID:       invite.InviteeID,
		Subscription: sub,
		Action:       "added",
	}

	updateData, err := natsutil.MarshalResponse(updateEvt)
	if err != nil {
		return fmt.Errorf("marshal subscription update event: %w", err)
	}

	subj := subject.SubscriptionUpdate(invite.InviteeID)
	if err := h.pub.Publish(subj, updateData); err != nil {
		slog.Error("publish subscription update failed", "error", err, "userID", invite.InviteeID)
	}

	return nil
}

func (h *Handler) handleRoomSync(ctx context.Context, evt model.OutboxEvent) error {
	var room model.Room
	if err := json.Unmarshal(evt.Payload, &room); err != nil {
		return fmt.Errorf("unmarshal room_sync payload: %w", err)
	}

	if err := h.store.UpsertRoom(ctx, &room); err != nil {
		return fmt.Errorf("upsert room: %w", err)
	}

	return nil
}
