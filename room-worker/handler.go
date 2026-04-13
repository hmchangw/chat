package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

type Handler struct {
	store   SubscriptionStore
	siteID  string
	publish func(ctx context.Context, subj string, data []byte) error
}

func NewHandler(store SubscriptionStore, siteID string, publish func(context.Context, string, []byte) error) *Handler {
	return &Handler{store: store, siteID: siteID, publish: publish}
}

func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	var err error
	subj := msg.Subject()
	switch {
	case strings.HasSuffix(subj, "member.role-update"):
		err = h.processRoleUpdate(ctx, msg.Data())
	default:
		err = h.processInvite(ctx, msg.Data())
	}
	if err != nil {
		slog.Error("process message failed", "error", err, "subject", subj)
		if nakErr := msg.Nak(); nakErr != nil {
			slog.Error("failed to nak message", "error", nakErr)
		}
		return
	}
	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "error", err)
	}
}

func (h *Handler) processInvite(ctx context.Context, data []byte) error {
	var req model.InviteMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return err
	}

	now := time.Now().UTC()

	// Create subscription for invitee
	sub := model.Subscription{
		ID:                 uuid.New().String(),
		User:               model.SubscriptionUser{ID: req.InviteeID, Account: req.InviteeAccount},
		RoomID:             req.RoomID,
		SiteID:             req.SiteID,
		Roles:              []model.Role{model.RoleMember},
		HistorySharedSince: &now,
		JoinedAt:           now,
	}
	if err := h.store.CreateSubscription(ctx, &sub); err != nil {
		return err
	}

	// Increment room user count
	if err := h.store.IncrementUserCount(ctx, req.RoomID); err != nil {
		slog.Warn("increment user count failed", "error", err, "roomID", req.RoomID)
	}

	// If invitee is on different site, publish outbox event
	if req.SiteID != h.siteID {
		outbox := model.OutboxEvent{
			Type:       "member_added",
			SiteID:     h.siteID,
			DestSiteID: req.SiteID,
			Payload:    data,
			Timestamp:  now.UnixMilli(),
		}
		outboxData, _ := json.Marshal(outbox)
		outboxSubj := subject.Outbox(h.siteID, req.SiteID, "member_added")
		if err := h.publish(ctx, outboxSubj, outboxData); err != nil {
			slog.Error("outbox publish failed", "error", err)
		}
	}

	// Notify invitee: subscription update
	subEvt := model.SubscriptionUpdateEvent{UserID: req.InviteeID, Subscription: sub, Action: "added", Timestamp: now.UnixMilli()}
	subEvtData, _ := json.Marshal(subEvt)
	if err := h.publish(ctx, subject.SubscriptionUpdate(req.InviteeAccount), subEvtData); err != nil {
		slog.Error("subscription update publish failed", "error", err)
	}

	// Notify all existing members: room metadata changed
	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err == nil {
		metaEvt := model.RoomMetadataUpdateEvent{
			RoomID:    req.RoomID,
			Name:      room.Name,
			UserCount: room.UserCount,
			UpdatedAt: now,
			Timestamp: now.UnixMilli(),
		}
		metaData, _ := json.Marshal(metaEvt)

		members, _ := h.store.ListByRoom(ctx, req.RoomID)
		for i := range members {
			if err := h.publish(ctx, subject.RoomMetadataChanged(members[i].User.Account), metaData); err != nil {
				slog.Error("room metadata publish failed", "error", err, "account", members[i].User.Account)
			}
		}
	}

	return nil
}

func (h *Handler) processRoleUpdate(ctx context.Context, data []byte) error {
	var req model.UpdateRoleRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal role update request: %w", err)
	}
	sub, err := h.store.GetSubscription(ctx, req.Account, req.RoomID)
	if err != nil {
		return fmt.Errorf("get subscription: %w", err)
	}
	newRoles := []model.Role{req.NewRole}
	if err := h.store.UpdateSubscriptionRoles(ctx, req.Account, req.RoomID, newRoles); err != nil {
		return fmt.Errorf("update subscription roles: %w", err)
	}
	now := time.Now().UTC()
	updatedSub := *sub
	updatedSub.Roles = []model.Role{req.NewRole}
	subEvt := model.SubscriptionUpdateEvent{
		UserID:       sub.User.ID,
		Subscription: updatedSub,
		Action:       "role_updated",
		Timestamp:    now.UnixMilli(),
	}
	subEvtData, err := json.Marshal(subEvt)
	if err != nil {
		return fmt.Errorf("marshal subscription update event: %w", err)
	}
	if err := h.publish(ctx, subject.SubscriptionUpdate(sub.User.Account), subEvtData); err != nil {
		return fmt.Errorf("publish subscription update: %w", err)
	}
	if sub.SiteID != h.siteID {
		outbox := model.OutboxEvent{
			Type:       "role_updated",
			SiteID:     h.siteID,
			DestSiteID: sub.SiteID,
			Payload:    subEvtData,
			Timestamp:  now.UnixMilli(),
		}
		outboxData, err := json.Marshal(outbox)
		if err != nil {
			return fmt.Errorf("marshal outbox event: %w", err)
		}
		outboxSubj := subject.Outbox(h.siteID, sub.SiteID, "role_updated")
		if err := h.publish(ctx, outboxSubj, outboxData); err != nil {
			return fmt.Errorf("publish outbox: %w", err)
		}
	}
	return nil
}
