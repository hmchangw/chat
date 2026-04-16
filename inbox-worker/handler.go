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
	UpdateSubscriptionRoles(ctx context.Context, account, roomID string, roles []model.Role) error
	DeleteSubscription(ctx context.Context, roomID, account string) error
	DeleteSubscriptionsByAccounts(ctx context.Context, roomID string, accounts []string) error
	DeleteRoomMember(ctx context.Context, roomID string, memberType model.RoomMemberType, memberID string) error
	DeleteRoomMembersByAccount(ctx context.Context, roomID, account string) error
	GetIndividualMemberAccounts(ctx context.Context, roomID string, accounts []string) ([]string, error)
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
	case "room_sync":
		return h.handleRoomSync(ctx, &evt)
	case "role_updated":
		return h.handleRoleUpdated(ctx, &evt)
	default:
		slog.Warn("unknown event type, skipping", "type", evt.Type)
		return nil
	}
}

func (h *Handler) handleMemberAdded(ctx context.Context, evt *model.OutboxEvent) error {
	var invite model.InviteMemberRequest
	if err := json.Unmarshal(evt.Payload, &invite); err != nil {
		return fmt.Errorf("unmarshal member_added payload: %w", err)
	}

	now := time.Now().UTC()
	sub := model.Subscription{
		ID:                 uuid.New().String(),
		User:               model.SubscriptionUser{ID: invite.InviteeID, Account: invite.InviteeAccount},
		RoomID:             invite.RoomID,
		SiteID:             invite.SiteID,
		Roles:              []model.Role{model.RoleMember},
		HistorySharedSince: &now,
		JoinedAt:           now,
	}

	if err := h.store.CreateSubscription(ctx, &sub); err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}

	updateEvt := model.SubscriptionUpdateEvent{
		UserID:       invite.InviteeID,
		Subscription: sub,
		Action:       "added",
		Timestamp:    now.UnixMilli(),
	}

	updateData, err := natsutil.MarshalResponse(updateEvt)
	if err != nil {
		return fmt.Errorf("marshal subscription update event: %w", err)
	}

	subj := subject.SubscriptionUpdate(invite.InviteeAccount)
	if err := h.pub.Publish(ctx, subj, updateData); err != nil {
		slog.Error("publish subscription update failed", "error", err, "account", invite.InviteeAccount)
	}

	return nil
}

func (h *Handler) handleMemberRemoved(ctx context.Context, evt *model.OutboxEvent) error {
	var memberEvt model.MemberRemoveEvent
	if err := json.Unmarshal(evt.Payload, &memberEvt); err != nil {
		return fmt.Errorf("unmarshal member removed payload: %w", err)
	}

	var removedAccounts []string

	if memberEvt.OrgID != "" {
		// Org removal: delete org room_members doc first
		if err := h.store.DeleteRoomMember(ctx, memberEvt.RoomID, model.RoomMemberOrg, memberEvt.OrgID); err != nil {
			return fmt.Errorf("delete org room member: %w", err)
		}
		// Dual-membership guard: find accounts that also have individual membership
		individualAccounts, err := h.store.GetIndividualMemberAccounts(ctx, memberEvt.RoomID, memberEvt.Accounts)
		if err != nil {
			return fmt.Errorf("get individual member accounts: %w", err)
		}
		individualSet := make(map[string]bool, len(individualAccounts))
		for _, a := range individualAccounts {
			individualSet[a] = true
		}
		// Only delete subscriptions for accounts WITHOUT individual membership
		for _, account := range memberEvt.Accounts {
			if !individualSet[account] {
				removedAccounts = append(removedAccounts, account)
			}
		}
		if len(removedAccounts) > 0 {
			if err := h.store.DeleteSubscriptionsByAccounts(ctx, memberEvt.RoomID, removedAccounts); err != nil {
				return fmt.Errorf("delete subscriptions: %w", err)
			}
		}
	} else {
		// Individual removal: delete subscription + individual room_members
		if err := h.store.DeleteSubscription(ctx, memberEvt.RoomID, memberEvt.Accounts[0]); err != nil {
			return fmt.Errorf("delete subscription: %w", err)
		}
		if err := h.store.DeleteRoomMembersByAccount(ctx, memberEvt.RoomID, memberEvt.Accounts[0]); err != nil {
			return fmt.Errorf("delete room members: %w", err)
		}
		removedAccounts = memberEvt.Accounts
	}

	// Publish SubscriptionUpdateEvent per actually removed account
	now := time.Now().UTC()
	for _, account := range removedAccounts {
		subEvt := model.SubscriptionUpdateEvent{
			Subscription: model.Subscription{
				RoomID: memberEvt.RoomID,
				User:   model.SubscriptionUser{Account: account},
			},
			Action:    "removed",
			Timestamp: now.UnixMilli(),
		}
		subEvtData, _ := json.Marshal(subEvt)
		if err := h.pub.Publish(ctx, subject.SubscriptionUpdate(account), subEvtData); err != nil {
			slog.Error("subscription update publish failed", "error", err, "account", account)
		}
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

// handleRoleUpdated updates the local subscription roles.
// No SubscriptionUpdateEvent is published here — room-worker already publishes to
// the user's subject, and NATS supercluster routes it to the user's site.
func (h *Handler) handleRoleUpdated(ctx context.Context, evt *model.OutboxEvent) error {
	var subEvt model.SubscriptionUpdateEvent
	if err := json.Unmarshal(evt.Payload, &subEvt); err != nil {
		return fmt.Errorf("unmarshal role_updated payload: %w", err)
	}
	account := subEvt.Subscription.User.Account
	roomID := subEvt.Subscription.RoomID
	roles := subEvt.Subscription.Roles
	if len(roles) == 0 {
		return fmt.Errorf("role_updated event has empty roles")
	}
	if err := h.store.UpdateSubscriptionRoles(ctx, account, roomID, roles); err != nil {
		return fmt.Errorf("update subscription roles: %w", err)
	}
	return nil
}
