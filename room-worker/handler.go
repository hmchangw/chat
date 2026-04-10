package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

type Handler struct {
	store         SubscriptionStore
	siteID        string
	publishLocal  func(ctx context.Context, subj string, data []byte) error
	publishOutbox func(ctx context.Context, subj string, data []byte) error
}

func NewHandler(store SubscriptionStore, siteID string, publishLocal, publishOutbox func(context.Context, string, []byte) error) *Handler {
	return &Handler{store: store, siteID: siteID, publishLocal: publishLocal, publishOutbox: publishOutbox}
}

func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	var err error
	subj := msg.Subject()
	parts := strings.Split(subj, ".")
	if len(parts) >= 2 {
		op := parts[len(parts)-2] + "." + parts[len(parts)-1]
		switch op {
		case "member.add":
			err = h.processAddMembers(ctx, msg.Data())
		case "member.remove":
			err = h.processRemoveMember(ctx, msg.Data())
		case "member.role-update":
			err = h.processRoleUpdate(ctx, msg.Data())
		default:
			slog.Warn("unknown member operation", "op", op, "subject", subj)
			err = fmt.Errorf("unknown member operation: %s", op)
		}
	}
	if err != nil {
		slog.Error("process message failed", "error", err, "subject", subj)
		if nakErr := msg.Nak(); nakErr != nil {
			slog.Error("failed to nak message", "err", nakErr)
		}
		return
	}
	if ackErr := msg.Ack(); ackErr != nil {
		slog.Error("failed to ack message", "err", ackErr)
	}
}

func (h *Handler) processAddMembers(ctx context.Context, data []byte) error {
	var req model.AddMembersRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal add members request: %w", err)
	}
	now := time.Now().UTC()

	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err != nil {
		return fmt.Errorf("get room %q: %w", req.RoomID, err)
	}

	var subs []*model.Subscription
	var accounts []string
	userSiteIDs := make(map[string]string) // account -> siteID
	for _, account := range req.Users {
		user, err := h.store.GetUser(ctx, account)
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				slog.Warn("user not found, skipping", "account", account)
				continue
			}
			return fmt.Errorf("get user %q: %w", account, err)
		}
		sub := &model.Subscription{
			ID: uuid.New().String(), User: model.SubscriptionUser{ID: user.ID, Account: user.Account},
			RoomID: req.RoomID, SiteID: user.SiteID, Roles: []model.Role{model.RoleMember}, JoinedAt: now,
		}
		if req.History.Mode != model.HistoryModeAll {
			sub.HistorySharedSince = &now
		}
		subs = append(subs, sub)
		accounts = append(accounts, user.Account)
		userSiteIDs[user.Account] = user.SiteID
	}

	if err := h.store.BulkCreateSubscriptions(ctx, subs); err != nil {
		return fmt.Errorf("bulk create subscriptions: %w", err)
	}

	// Write room_members for orgs
	for _, orgID := range req.Orgs {
		m := &model.RoomMember{ID: uuid.New().String(), RoomID: req.RoomID, Ts: now,
			Member: model.RoomMemberEntry{ID: orgID, Type: model.RoomMemberTypeOrg}}
		if err := h.store.CreateRoomMember(ctx, m); err != nil {
			slog.Error("create org room member failed", "error", err, "orgID", orgID)
		}
	}

	if len(accounts) > 0 {
		if err := h.store.IncrementUserCount(ctx, req.RoomID); err != nil {
			return fmt.Errorf("increment user count: %w", err)
		}
	}

	// Publish SubscriptionUpdateEvent per member
	for _, sub := range subs {
		evt := model.SubscriptionUpdateEvent{UserID: sub.User.ID, Subscription: *sub, Action: "added", Timestamp: now.UnixMilli()}
		evtData, _ := json.Marshal(evt)
		if err := h.publishLocal(ctx, subject.SubscriptionUpdate(sub.User.Account), evtData); err != nil {
			slog.Error("subscription update publish failed", "error", err, "account", sub.User.Account)
		}
	}

	// Publish MemberChangeEvent
	memberEvt := model.MemberChangeEvent{Type: "member-added", RoomID: req.RoomID, Accounts: accounts, SiteID: h.siteID}
	memberEvtData, _ := json.Marshal(memberEvt)
	if err := h.publishLocal(ctx, subject.RoomMemberEvent(req.RoomID), memberEvtData); err != nil {
		slog.Error("room member event publish failed", "error", err, "roomID", req.RoomID)
	}

	// Publish system message to MESSAGES_CANONICAL
	sysMsgData, _ := json.Marshal(model.MembersAdded{
		Individuals:     req.Users,
		Orgs:            req.Orgs,
		Channels:        req.Channels,
		AddedUsersCount: len(accounts),
	})
	sysMsg := model.Message{
		ID:         uuid.New().String(),
		RoomID:     req.RoomID,
		Content:    "added members to the channel",
		CreatedAt:  now,
		Type:       "members_added",
		SysMsgData: sysMsgData,
	}
	sysMsgEvt := model.MessageEvent{Message: sysMsg, SiteID: h.siteID, Timestamp: now.UnixMilli()}
	sysMsgEvtData, _ := json.Marshal(sysMsgEvt)
	if err := h.publishLocal(ctx, subject.MsgCanonicalCreated(h.siteID), sysMsgEvtData); err != nil {
		slog.Error("system message publish failed", "error", err, "roomID", req.RoomID)
	}

	// Outbox for cross-site members: outbox.{room.SiteID}.to.{user.SiteID}.member_added
	for account, userSiteID := range userSiteIDs {
		if userSiteID != room.SiteID {
			outbox := model.OutboxEvent{Type: "member_added", SiteID: room.SiteID, DestSiteID: userSiteID, Payload: memberEvtData, Timestamp: now.UnixMilli()}
			outboxData, _ := json.Marshal(outbox)
			if err := h.publishOutbox(ctx, subject.Outbox(room.SiteID, userSiteID, "member_added"), outboxData); err != nil {
				slog.Error("outbox publish failed", "error", err, "account", account)
			}
		}
	}

	return nil
}

func (h *Handler) processRemoveMember(ctx context.Context, data []byte) error {
	var req model.RemoveMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal remove member request: %w", err)
	}
	now := time.Now().UTC()

	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err != nil {
		return fmt.Errorf("get room %q: %w", req.RoomID, err)
	}

	var accounts []string
	if req.OrgID != "" {
		orgAccounts, err := h.store.GetOrgAccounts(ctx, req.OrgID)
		if err != nil {
			return fmt.Errorf("get org accounts %q: %w", req.OrgID, err)
		}
		for _, account := range orgAccounts {
			if err := h.store.DeleteSubscription(ctx, account, req.RoomID); err != nil {
				slog.Error("delete subscription failed", "error", err, "account", account)
			}
		}
		accounts = orgAccounts
	} else {
		if err := h.store.DeleteSubscription(ctx, req.Account, req.RoomID); err != nil {
			return fmt.Errorf("delete subscription: %w", err)
		}
		accounts = []string{req.Account}
	}

	// Look up each user for SiteID
	userSiteIDs := make(map[string]string) // account -> siteID
	for _, account := range accounts {
		user, err := h.store.GetUser(ctx, account)
		if err != nil {
			slog.Warn("get user for outbox failed, skipping cross-site check", "account", account, "error", err)
			continue
		}
		userSiteIDs[account] = user.SiteID
	}

	// Delete room_members
	if req.OrgID != "" {
		if err := h.store.DeleteOrgRoomMember(ctx, req.OrgID, req.RoomID); err != nil {
			slog.Error("delete org room member failed", "error", err, "orgID", req.OrgID)
		}
		for _, account := range accounts {
			if err := h.store.DeleteRoomMember(ctx, account, req.RoomID); err != nil {
				slog.Error("delete room member failed", "error", err, "account", account)
			}
		}
	} else {
		if err := h.store.DeleteRoomMember(ctx, req.Account, req.RoomID); err != nil {
			slog.Error("delete room member failed", "error", err, "account", req.Account)
		}
	}

	if len(accounts) > 0 {
		if err := h.store.DecrementUserCount(ctx, req.RoomID); err != nil {
			return fmt.Errorf("decrement user count: %w", err)
		}
	}

	// Publish SubscriptionUpdateEvent per removed account
	for _, account := range accounts {
		evt := model.SubscriptionUpdateEvent{
			Subscription: model.Subscription{RoomID: req.RoomID, User: model.SubscriptionUser{Account: account}},
			Action:       "removed", Timestamp: now.UnixMilli(),
		}
		evtData, _ := json.Marshal(evt)
		if err := h.publishLocal(ctx, subject.SubscriptionUpdate(account), evtData); err != nil {
			slog.Error("subscription update publish failed", "error", err, "account", account)
		}
	}

	// Publish MemberChangeEvent
	memberEvt := model.MemberChangeEvent{Type: "member-removed", RoomID: req.RoomID, Accounts: accounts, SiteID: h.siteID}
	memberEvtData, _ := json.Marshal(memberEvt)
	if err := h.publishLocal(ctx, subject.RoomMemberEvent(req.RoomID), memberEvtData); err != nil {
		slog.Error("room member event publish failed", "error", err, "roomID", req.RoomID)
	}

	// Publish system message to MESSAGES_CANONICAL
	removedID := req.Account
	if req.OrgID != "" {
		removedID = req.OrgID
	}
	sysMsgData, _ := json.Marshal(model.MembersRemoved{
		Account:           req.Account,
		OrgID:             req.OrgID,
		RemovedUsersCount: len(accounts),
	})
	sysMsg := model.Message{
		ID:         uuid.New().String(),
		RoomID:     req.RoomID,
		Content:    fmt.Sprintf("removed %s from the room", removedID),
		CreatedAt:  now,
		Type:       "member_removed",
		SysMsgData: sysMsgData,
	}
	sysMsgEvt := model.MessageEvent{Message: sysMsg, SiteID: h.siteID, Timestamp: now.UnixMilli()}
	sysMsgEvtData, _ := json.Marshal(sysMsgEvt)
	if err := h.publishLocal(ctx, subject.MsgCanonicalCreated(h.siteID), sysMsgEvtData); err != nil {
		slog.Error("system message publish failed", "error", err, "roomID", req.RoomID)
	}

	// Outbox for cross-site members: outbox.{room.SiteID}.to.{user.SiteID}.member_removed
	for account, userSiteID := range userSiteIDs {
		if userSiteID != room.SiteID {
			outbox := model.OutboxEvent{Type: "member_removed", SiteID: room.SiteID, DestSiteID: userSiteID, Payload: memberEvtData, Timestamp: now.UnixMilli()}
			outboxData, _ := json.Marshal(outbox)
			if err := h.publishOutbox(ctx, subject.Outbox(room.SiteID, userSiteID, "member_removed"), outboxData); err != nil {
				slog.Error("outbox publish failed", "error", err, "account", account)
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
	if err := h.store.UpdateSubscriptionRole(ctx, req.Account, req.RoomID, req.NewRole); err != nil {
		return fmt.Errorf("update subscription role: %w", err)
	}
	evt := model.SubscriptionUpdateEvent{
		Subscription: model.Subscription{RoomID: req.RoomID, User: model.SubscriptionUser{Account: req.Account}, Roles: []model.Role{req.NewRole}},
		Action:       "role_updated", Timestamp: time.Now().UnixMilli(),
	}
	evtData, _ := json.Marshal(evt)
	if err := h.publishLocal(ctx, subject.SubscriptionUpdate(req.Account), evtData); err != nil {
		slog.Error("subscription update publish failed", "error", err, "account", req.Account)
	}

	// Cross-site outbox for role update
	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err != nil {
		slog.Error("get room for role update outbox failed", "error", err, "roomID", req.RoomID)
		return nil
	}
	user, err := h.store.GetUser(ctx, req.Account)
	if err != nil {
		slog.Error("get user for role update outbox failed", "error", err, "account", req.Account)
		return nil
	}
	if user.SiteID != room.SiteID {
		outbox := model.OutboxEvent{Type: "role_updated", SiteID: room.SiteID, DestSiteID: user.SiteID, Payload: evtData, Timestamp: time.Now().UnixMilli()}
		outboxData, _ := json.Marshal(outbox)
		if err := h.publishOutbox(ctx, subject.Outbox(room.SiteID, user.SiteID, "role_updated"), outboxData); err != nil {
			slog.Error("outbox publish failed", "error", err, "account", req.Account)
		}
	}
	return nil
}
