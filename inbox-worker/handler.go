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
	DeleteSubscription(ctx context.Context, account string, roomID string) error
	UpdateSubscriptionRole(ctx context.Context, account string, roomID string, role model.Role) error
	UpsertRoom(ctx context.Context, room *model.Room) error
}

// Publisher abstracts NATS publishing so the handler is testable.
type Publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
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
		return h.handleMemberAdded(ctx, &evt)
	case "member_removed":
		return h.handleMemberRemoved(ctx, &evt)
	case "role_updated":
		return h.handleRoleUpdated(ctx, &evt)
	case "room_sync":
		return h.handleRoomSync(ctx, &evt)
	default:
		slog.Warn("unknown event type, skipping", "type", evt.Type)
		return nil
	}
}

func (h *Handler) handleMemberAdded(ctx context.Context, evt *model.OutboxEvent) error {
	var change model.MemberChangeEvent
	if err := json.Unmarshal(evt.Payload, &change); err != nil {
		return fmt.Errorf("unmarshal member_added payload: %w", err)
	}

	now := time.Now().UTC()
	joinedAt := now
	if change.JoinedAt > 0 {
		joinedAt = time.UnixMilli(change.JoinedAt).UTC()
	}
	var historySharedSince *time.Time
	if change.HistorySharedSince > 0 {
		t := time.UnixMilli(change.HistorySharedSince).UTC()
		historySharedSince = &t
	}

	for i, account := range change.Accounts {
		userID := account // fallback
		if i < len(change.UserIDs) {
			userID = change.UserIDs[i]
		}
		sub := model.Subscription{
			ID:                 uuid.New().String(),
			User:               model.SubscriptionUser{ID: userID, Account: account},
			RoomID:             change.RoomID,
			SiteID:             change.SiteID,
			Roles:              []model.Role{model.RoleMember},
			HistorySharedSince: historySharedSince,
			JoinedAt:           joinedAt,
		}

		if err := h.store.CreateSubscription(ctx, &sub); err != nil {
			return fmt.Errorf("create subscription for %q: %w", account, err)
		}

		updateEvt := model.SubscriptionUpdateEvent{
			Subscription: sub,
			Action:       "added",
			Timestamp:    now.UnixMilli(),
		}

		updateData, err := natsutil.MarshalResponse(updateEvt)
		if err != nil {
			return fmt.Errorf("marshal subscription update event: %w", err)
		}

		subj := subject.SubscriptionUpdate(account)
		if err := h.pub.Publish(ctx, subj, updateData); err != nil {
			slog.Error("publish subscription update failed", "error", err, "account", account)
		}
	}

	return nil
}

func (h *Handler) handleMemberRemoved(ctx context.Context, evt *model.OutboxEvent) error {
	var change model.MemberChangeEvent
	if err := json.Unmarshal(evt.Payload, &change); err != nil {
		return fmt.Errorf("unmarshal member_removed payload: %w", err)
	}

	for _, account := range change.Accounts {
		if err := h.store.DeleteSubscription(ctx, account, change.RoomID); err != nil {
			return fmt.Errorf("delete subscription for %q: %w", account, err)
		}

		updateEvt := model.SubscriptionUpdateEvent{
			Subscription: model.Subscription{RoomID: change.RoomID, User: model.SubscriptionUser{Account: account}},
			Action:       "removed",
			Timestamp:    time.Now().UTC().UnixMilli(),
		}

		updateData, err := natsutil.MarshalResponse(updateEvt)
		if err != nil {
			return fmt.Errorf("marshal subscription update event: %w", err)
		}

		subj := subject.SubscriptionUpdate(account)
		if err := h.pub.Publish(ctx, subj, updateData); err != nil {
			slog.Error("publish subscription update failed", "error", err, "account", account)
		}
	}

	return nil
}

func (h *Handler) handleRoleUpdated(ctx context.Context, evt *model.OutboxEvent) error {
	var subEvt model.SubscriptionUpdateEvent
	if err := json.Unmarshal(evt.Payload, &subEvt); err != nil {
		return fmt.Errorf("unmarshal role_updated payload: %w", err)
	}
	account := subEvt.Subscription.User.Account
	roomID := subEvt.Subscription.RoomID
	if len(subEvt.Subscription.Roles) == 0 {
		return fmt.Errorf("no role in subscription update event")
	}
	newRole := subEvt.Subscription.Roles[0]
	if err := h.store.UpdateSubscriptionRole(ctx, account, roomID, newRole); err != nil {
		return fmt.Errorf("update subscription role for %q: %w", account, err)
	}
	updateData, err := natsutil.MarshalResponse(subEvt)
	if err != nil {
		return fmt.Errorf("marshal subscription update event: %w", err)
	}
	if err := h.pub.Publish(ctx, subject.SubscriptionUpdate(account), updateData); err != nil {
		slog.Error("publish subscription update failed", "error", err, "account", account)
	}
	return nil
}

func (h *Handler) handleRoomSync(ctx context.Context, evt *model.OutboxEvent) error {
	var room model.Room
	if err := json.Unmarshal(evt.Payload, &room); err != nil {
		return fmt.Errorf("unmarshal room_sync payload: %w", err)
	}

	if err := h.store.UpsertRoom(ctx, &room); err != nil {
		return fmt.Errorf("upsert room: %w", err)
	}

	return nil
}
