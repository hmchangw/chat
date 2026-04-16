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
	DeleteSubscriptionsByAccounts(ctx context.Context, roomID string, accounts []string) error
	FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error)
	BulkCreateSubscriptions(ctx context.Context, subs []*model.Subscription) error
	CreateRoomMember(ctx context.Context, member *model.RoomMember) error
	HasOrgRoomMembers(ctx context.Context, roomID string) (bool, error)
	GetSubscriptionAccounts(ctx context.Context, roomID string) ([]string, error)
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
	var event model.MemberAddEvent
	if err := json.Unmarshal(evt.Payload, &event); err != nil {
		return fmt.Errorf("unmarshal member_added payload: %w", err)
	}

	// 1. Look up users locally
	users, err := h.store.FindUsersByAccounts(ctx, event.Accounts)
	if err != nil {
		return fmt.Errorf("find users by accounts: %w", err)
	}
	userMap := make(map[string]model.User, len(users))
	for _, u := range users {
		userMap[u.Account] = u
	}

	joinedAt := time.UnixMilli(event.JoinedAt).UTC()
	var historySharedSince *time.Time
	if event.HistorySharedSince > 0 {
		t := time.UnixMilli(event.HistorySharedSince).UTC()
		historySharedSince = &t
	}

	// 2. Build subscriptions
	subs := make([]*model.Subscription, 0, len(event.Accounts))
	for _, account := range event.Accounts {
		user, ok := userMap[account]
		if !ok {
			slog.Warn("user not found for account", "account", account)
			continue
		}
		sub := &model.Subscription{
			ID:                 uuid.New().String(),
			User:               model.SubscriptionUser{ID: user.ID, Account: user.Account},
			RoomID:             event.RoomID,
			SiteID:             event.SiteID,
			Roles:              []model.Role{model.RoleMember},
			HistorySharedSince: historySharedSince,
			JoinedAt:           joinedAt,
		}
		subs = append(subs, sub)
	}

	// 3. Bulk create subscriptions
	if err := h.store.BulkCreateSubscriptions(ctx, subs); err != nil {
		return fmt.Errorf("bulk create subscriptions: %w", err)
	}

	// 4. Write room_members
	now := time.Now().UTC()
	if len(event.Orgs) > 0 {
		// Write org room_member docs
		for _, org := range event.Orgs {
			member := &model.RoomMember{
				ID:     uuid.New().String(),
				RoomID: event.RoomID,
				Ts:     now,
				Member: model.RoomMemberEntry{
					ID:   org,
					Type: model.RoomMemberOrg,
				},
			}
			if err := h.store.CreateRoomMember(ctx, member); err != nil {
				slog.Error("create org room member failed", "error", err, "org", org)
			}
		}
		// Write individual room_member docs for new accounts
		for _, sub := range subs {
			member := &model.RoomMember{
				ID:     uuid.New().String(),
				RoomID: event.RoomID,
				Ts:     now,
				Member: model.RoomMemberEntry{
					ID:      sub.User.ID,
					Type:    model.RoomMemberIndividual,
					Account: sub.User.Account,
				},
			}
			if err := h.store.CreateRoomMember(ctx, member); err != nil {
				slog.Error("create individual room member failed", "error", err, "account", sub.User.Account)
			}
		}
		// Backfill existing subscription accounts
		existingAccounts, err := h.store.GetSubscriptionAccounts(ctx, event.RoomID)
		if err != nil {
			slog.Warn("get subscription accounts failed", "error", err, "roomID", event.RoomID)
		} else {
			// Build set of new accounts to avoid duplicates
			newAccountSet := make(map[string]struct{}, len(subs))
			for _, sub := range subs {
				newAccountSet[sub.User.Account] = struct{}{}
			}
			for _, account := range existingAccounts {
				if _, isNew := newAccountSet[account]; isNew {
					continue
				}
				user, ok := userMap[account]
				if !ok {
					// Need to look up user - but we only have the accounts from subs
					// Skip accounts we don't have user info for
					continue
				}
				member := &model.RoomMember{
					ID:     uuid.New().String(),
					RoomID: event.RoomID,
					Ts:     now,
					Member: model.RoomMemberEntry{
						ID:      user.ID,
						Type:    model.RoomMemberIndividual,
						Account: user.Account,
					},
				}
				if err := h.store.CreateRoomMember(ctx, member); err != nil {
					slog.Error("backfill room member failed", "error", err, "account", account)
				}
			}
		}
	} else {
		hasOrgs, err := h.store.HasOrgRoomMembers(ctx, event.RoomID)
		if err != nil {
			slog.Warn("check existing org room members failed", "error", err, "roomID", event.RoomID)
		}
		if hasOrgs {
			for _, sub := range subs {
				member := &model.RoomMember{
					ID:     uuid.New().String(),
					RoomID: event.RoomID,
					Ts:     now,
					Member: model.RoomMemberEntry{
						ID:      sub.User.ID,
						Type:    model.RoomMemberIndividual,
						Account: sub.User.Account,
					},
				}
				if err := h.store.CreateRoomMember(ctx, member); err != nil {
					slog.Error("create individual room member failed", "error", err, "account", sub.User.Account)
				}
			}
		}
	}

	// 5. Publish SubscriptionUpdateEvent per new member
	publishNow := time.Now().UTC().UnixMilli()
	for _, sub := range subs {
		updateEvt := model.SubscriptionUpdateEvent{
			UserID: sub.User.ID, Subscription: *sub, Action: "added",
			Timestamp: publishNow,
		}
		updateData, err := natsutil.MarshalResponse(updateEvt)
		if err != nil {
			return fmt.Errorf("marshal subscription update event: %w", err)
		}
		if err := h.pub.Publish(ctx, subject.SubscriptionUpdate(sub.User.Account), updateData); err != nil {
			slog.Error("publish subscription update failed", "error", err, "account", sub.User.Account)
		}
	}

	return nil
}

// handleMemberRemoved deletes the subscriptions for the accounts listed in the
// event. The room's home site has already filtered out dual-membership users,
// so this site only needs to sync subscriptions in a single round trip. No
// SubscriptionUpdateEvent is published here — room-worker already publishes
// to the user's subject and the NATS supercluster routes it to the user's
// home site.
func (h *Handler) handleMemberRemoved(ctx context.Context, evt *model.OutboxEvent) error {
	var memberEvt model.MemberRemoveEvent
	if err := json.Unmarshal(evt.Payload, &memberEvt); err != nil {
		return fmt.Errorf("unmarshal member removed payload: %w", err)
	}
	if len(memberEvt.Accounts) == 0 {
		return nil
	}
	if err := h.store.DeleteSubscriptionsByAccounts(ctx, memberEvt.RoomID, memberEvt.Accounts); err != nil {
		return fmt.Errorf("delete subscriptions for room %s: %w", memberEvt.RoomID, err)
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
