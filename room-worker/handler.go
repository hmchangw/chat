package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

type Handler struct {
	store   SubscriptionStore
	siteID  string
	publish func(subj string, data []byte) error
}

func NewHandler(store SubscriptionStore, siteID string, publish func(string, []byte) error) *Handler {
	return &Handler{store: store, siteID: siteID, publish: publish}
}

func (h *Handler) HandleJetStreamMsg(msg jetstream.Msg) {
	if err := h.processInvite(context.Background(), msg.Data()); err != nil {
		slog.Error("process invite failed", "error", err)
	}
	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "err", err)
	}
}

func (h *Handler) processInvite(ctx context.Context, data []byte) error {
	var req model.InviteMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal invite request: %w", err)
	}

	now := time.Now().UTC()

	// Create subscription for invitee
	sub := model.Subscription{
		ID:                 uuid.New().String(),
		User:               model.SubscriptionUser{ID: req.InviteeID, Username: req.InviteeUsername},
		RoomID:             req.RoomID,
		SiteID:             req.SiteID,
		Roles:              []model.Role{model.RoleMember},
		HistorySharedSince: now,
		JoinedAt:           now,
	}
	if err := h.store.CreateSubscription(ctx, &sub); err != nil {
		return fmt.Errorf("create subscription: %w", err)
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
		}
		outboxData, _ := json.Marshal(outbox)
		outboxSubj := subject.Outbox(h.siteID, req.SiteID, "member_added")
		if err := h.publish(outboxSubj, outboxData); err != nil {
			slog.Error("outbox publish failed", "error", err)
		}
	}

	// Notify invitee: subscription update
	subEvt := model.SubscriptionUpdateEvent{UserID: req.InviteeID, Subscription: sub, Action: "added"}
	subEvtData, _ := json.Marshal(subEvt)
	if err := h.publish(subject.SubscriptionUpdate(req.InviteeUsername), subEvtData); err != nil {
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
		}
		metaData, _ := json.Marshal(metaEvt)

		members, _ := h.store.ListByRoom(ctx, req.RoomID)
		for i := range members {
			if err := h.publish(subject.RoomMetadataChanged(members[i].User.Username), metaData); err != nil {
				slog.Error("room metadata publish failed", "error", err, "username", members[i].User.Username)
			}
		}
	}

	return nil
}
