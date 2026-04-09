package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	asyncWg       sync.WaitGroup
}

func NewHandler(store SubscriptionStore, siteID string, publishLocal, publishOutbox func(context.Context, string, []byte) error) *Handler {
	return &Handler{store: store, siteID: siteID, publishLocal: publishLocal, publishOutbox: publishOutbox}
}

func (h *Handler) WaitAsync() {
	h.asyncWg.Wait()
}

func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	var err error
	subj := msg.Subject()
	parts := strings.Split(subj, ".")
	if len(parts) >= 2 {
		op := parts[len(parts)-2] + "." + parts[len(parts)-1]
		switch op {
		case "member.invite":
			err = h.processInvite(ctx, msg.Data())
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

func (h *Handler) processInvite(ctx context.Context, data []byte) error {
	var req model.InviteMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal invite request: %w", err)
	}
	now := time.Now().UTC()
	sub := model.Subscription{
		ID: uuid.New().String(), User: model.SubscriptionUser{ID: req.InviteeID, Account: req.InviteeAccount},
		RoomID: req.RoomID, SiteID: req.SiteID, Roles: []model.Role{model.RoleMember},
		HistorySharedSince: &now, JoinedAt: now,
	}
	if err := h.store.CreateSubscription(ctx, &sub); err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	if err := h.store.IncrementUserCount(ctx, req.RoomID); err != nil {
		slog.Warn("increment user count failed", "error", err, "roomID", req.RoomID)
	}
	if req.SiteID != h.siteID {
		outbox := model.OutboxEvent{Type: "member_added", SiteID: h.siteID, DestSiteID: req.SiteID, Payload: data, Timestamp: now.UnixMilli()}
		outboxData, _ := json.Marshal(outbox)
		if err := h.publishOutbox(ctx, subject.Outbox(h.siteID, req.SiteID, "member_added"), outboxData); err != nil {
			slog.Error("outbox publish failed", "error", err)
		}
	}
	subEvt := model.SubscriptionUpdateEvent{UserID: req.InviteeID, Subscription: sub, Action: "added", Timestamp: now.UnixMilli()}
	subEvtData, _ := json.Marshal(subEvt)
	if err := h.publishLocal(ctx, subject.SubscriptionUpdate(req.InviteeAccount), subEvtData); err != nil {
		slog.Error("subscription update publish failed", "error", err)
	}
	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err == nil {
		metaEvt := model.RoomMetadataUpdateEvent{RoomID: req.RoomID, Name: room.Name, UserCount: room.UserCount, UpdatedAt: now, Timestamp: now.UnixMilli()}
		metaData, _ := json.Marshal(metaEvt)
		members, _ := h.store.ListByRoom(ctx, req.RoomID)
		for i := range members {
			if err := h.publishLocal(ctx, subject.RoomMetadataChanged(members[i].User.Account), metaData); err != nil {
				slog.Error("room metadata publish failed", "error", err, "account", members[i].User.Account)
			}
		}
	}
	return nil
}

func (h *Handler) processAddMembers(ctx context.Context, data []byte) error {
	var req model.AddMembersRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal add members request: %w", err)
	}
	now := time.Now().UTC()
	var subs []*model.Subscription
	var accounts []string
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
	}
	if err := h.store.BulkCreateSubscriptions(ctx, subs); err != nil {
		return fmt.Errorf("bulk create subscriptions: %w", err)
	}
	asyncSubs := make([]*model.Subscription, len(subs))
	copy(asyncSubs, subs)
	asyncAccounts := make([]string, len(accounts))
	copy(asyncAccounts, accounts)
	asyncOrgs := make([]string, len(req.Orgs))
	copy(asyncOrgs, req.Orgs)
	roomID := req.RoomID
	h.asyncWg.Add(1)
	go func() {
		defer h.asyncWg.Done()
		bgCtx := context.Background()
		room, err := h.store.GetRoom(bgCtx, roomID)
		if err != nil {
			slog.Error("get room for add members", "error", err, "roomID", roomID)
			return
		}
		for _, account := range asyncAccounts {
			m := &model.RoomMember{ID: uuid.New().String(), RoomID: roomID, Ts: now,
				Member: model.RoomMemberEntry{ID: account, Type: model.RoomMemberTypeIndividual, Username: account}}
			if err := h.store.CreateRoomMember(bgCtx, m); err != nil {
				slog.Error("create room member failed", "error", err, "account", account)
			}
		}
		for _, orgID := range asyncOrgs {
			m := &model.RoomMember{ID: uuid.New().String(), RoomID: roomID, Ts: now,
				Member: model.RoomMemberEntry{ID: orgID, Type: model.RoomMemberTypeOrg}}
			if err := h.store.CreateRoomMember(bgCtx, m); err != nil {
				slog.Error("create org room member failed", "error", err, "orgID", orgID)
			}
		}
		if len(asyncAccounts) > 0 {
			if err := h.store.IncrementUserCount(bgCtx, roomID); err != nil {
				slog.Error("increment user count failed", "error", err, "roomID", roomID)
			}
		}
		for _, sub := range asyncSubs {
			evt := model.SubscriptionUpdateEvent{UserID: sub.User.ID, Subscription: *sub, Action: "added", Timestamp: now.UnixMilli()}
			evtData, _ := json.Marshal(evt)
			if err := h.publishLocal(bgCtx, subject.SubscriptionUpdate(sub.User.Account), evtData); err != nil {
				slog.Error("subscription update publish failed", "error", err, "account", sub.User.Account)
			}
		}
		memberEvt := model.MemberChangeEvent{Type: "member-added", RoomID: roomID, Accounts: asyncAccounts, SiteID: h.siteID}
		memberEvtData, _ := json.Marshal(memberEvt)
		if err := h.publishLocal(bgCtx, subject.RoomMemberEvent(roomID), memberEvtData); err != nil {
			slog.Error("room member event publish failed", "error", err, "roomID", roomID)
		}
		if room.SiteID != h.siteID {
			outbox := model.OutboxEvent{Type: "member_added", SiteID: h.siteID, DestSiteID: room.SiteID, Payload: memberEvtData, Timestamp: now.UnixMilli()}
			outboxData, _ := json.Marshal(outbox)
			if err := h.publishOutbox(bgCtx, subject.Outbox(h.siteID, room.SiteID, "member_added"), outboxData); err != nil {
				slog.Error("outbox publish failed", "error", err)
			}
		}
	}()
	return nil
}

func (h *Handler) processRemoveMember(ctx context.Context, data []byte) error {
	var req model.RemoveMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal remove member request: %w", err)
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
		if err := h.store.DeleteSubscription(ctx, req.Username, req.RoomID); err != nil {
			return fmt.Errorf("delete subscription: %w", err)
		}
		accounts = []string{req.Username}
	}
	asyncAccounts := make([]string, len(accounts))
	copy(asyncAccounts, accounts)
	roomID := req.RoomID
	orgID := req.OrgID
	username := req.Username
	h.asyncWg.Add(1)
	go func() {
		defer h.asyncWg.Done()
		bgCtx := context.Background()
		room, err := h.store.GetRoom(bgCtx, roomID)
		if err != nil {
			slog.Error("get room for remove member", "error", err, "roomID", roomID)
			return
		}
		if orgID != "" {
			if err := h.store.DeleteOrgRoomMember(bgCtx, orgID, roomID); err != nil {
				slog.Error("delete org room member failed", "error", err, "orgID", orgID)
			}
			for _, account := range asyncAccounts {
				if err := h.store.DeleteRoomMember(bgCtx, account, roomID); err != nil {
					slog.Error("delete room member failed", "error", err, "account", account)
				}
			}
		} else {
			if err := h.store.DeleteRoomMember(bgCtx, username, roomID); err != nil {
				slog.Error("delete room member failed", "error", err, "username", username)
			}
		}
		if len(asyncAccounts) > 0 {
			if err := h.store.DecrementUserCount(bgCtx, roomID); err != nil {
				slog.Error("decrement user count failed", "error", err, "roomID", roomID)
			}
		}
		for _, account := range asyncAccounts {
			evt := model.SubscriptionUpdateEvent{
				Subscription: model.Subscription{RoomID: roomID, User: model.SubscriptionUser{Account: account}},
				Action:       "removed", Timestamp: time.Now().UnixMilli(),
			}
			evtData, _ := json.Marshal(evt)
			if err := h.publishLocal(bgCtx, subject.SubscriptionUpdate(account), evtData); err != nil {
				slog.Error("subscription update publish failed", "error", err, "account", account)
			}
		}
		memberEvt := model.MemberChangeEvent{Type: "member-removed", RoomID: roomID, Accounts: asyncAccounts, SiteID: h.siteID}
		memberEvtData, _ := json.Marshal(memberEvt)
		if err := h.publishLocal(bgCtx, subject.RoomMemberEvent(roomID), memberEvtData); err != nil {
			slog.Error("room member event publish failed", "error", err, "roomID", roomID)
		}
		if room.SiteID != h.siteID {
			outbox := model.OutboxEvent{Type: "member_removed", SiteID: h.siteID, DestSiteID: room.SiteID, Payload: memberEvtData, Timestamp: time.Now().UnixMilli()}
			outboxData, _ := json.Marshal(outbox)
			if err := h.publishOutbox(bgCtx, subject.Outbox(h.siteID, room.SiteID, "member_removed"), outboxData); err != nil {
				slog.Error("outbox publish failed", "error", err)
			}
		}
	}()
	return nil
}

func (h *Handler) processRoleUpdate(ctx context.Context, data []byte) error {
	var req model.UpdateRoleRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal role update request: %w", err)
	}
	if err := h.store.UpdateSubscriptionRole(ctx, req.Username, req.RoomID, req.NewRole); err != nil {
		return fmt.Errorf("update subscription role: %w", err)
	}
	evt := model.SubscriptionUpdateEvent{
		Subscription: model.Subscription{RoomID: req.RoomID, User: model.SubscriptionUser{Account: req.Username}, Roles: []model.Role{req.NewRole}},
		Action:       "role_updated", Timestamp: time.Now().UnixMilli(),
	}
	evtData, _ := json.Marshal(evt)
	if err := h.publishLocal(ctx, subject.SubscriptionUpdate(req.Username), evtData); err != nil {
		slog.Error("subscription update publish failed", "error", err, "username", req.Username)
	}
	return nil
}
