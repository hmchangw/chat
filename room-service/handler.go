package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
	"github.com/google/uuid"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

type Handler struct {
	store           RoomStore
	siteID          string
	maxRoomSize     int
	publishToStream func(ctx context.Context, subj string, data []byte) error
}

func NewHandler(
	store RoomStore,
	siteID string,
	maxRoomSize int,
	publishToStream func(context.Context, string, []byte) error,
) *Handler {
	return &Handler{
		store:           store,
		siteID:          siteID,
		maxRoomSize:     maxRoomSize,
		publishToStream: publishToStream,
	}
}

// RegisterCRUD registers NATS request/reply handlers for room CRUD with queue group.
func (h *Handler) RegisterCRUD(nc *otelnats.Conn) error {
	const queue = "room-service"
	if _, err := nc.QueueSubscribe(subject.RoomsCreateWildcard(), queue, h.natsCreateRoom); err != nil {
		return err
	}
	if _, err := nc.QueueSubscribe(subject.RoomsListWildcard(), queue, h.natsListRooms); err != nil {
		return err
	}
	if _, err := nc.QueueSubscribe(subject.RoomsGetWildcard(), queue, h.natsGetRoom); err != nil {
		return err
	}
	if _, err := nc.QueueSubscribe(subject.MemberAddWildcard(h.siteID), queue, h.natsAddMembers); err != nil {
		return err
	}
	if _, err := nc.QueueSubscribe(subject.MemberRemoveWildcard(h.siteID), queue, h.natsRemoveMember); err != nil {
		return err
	}
	if _, err := nc.QueueSubscribe(subject.MemberRoleUpdateWildcard(h.siteID), queue, h.natsUpdateRole); err != nil {
		return err
	}
	return nil
}

func (h *Handler) natsCreateRoom(m otelnats.Msg) {
	resp, err := h.handleCreateRoom(m.Context(), m.Msg.Data)
	if err != nil {
		natsutil.ReplyError(m.Msg, err.Error())
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to message", "error", err)
	}
}

func (h *Handler) natsListRooms(m otelnats.Msg) {
	rooms, err := h.store.ListRooms(m.Context())
	if err != nil {
		natsutil.ReplyError(m.Msg, err.Error())
		return
	}
	natsutil.ReplyJSON(m.Msg, model.ListRoomsResponse{Rooms: rooms})
}

func (h *Handler) natsGetRoom(m otelnats.Msg) {
	parts := strings.Split(m.Msg.Subject, ".")
	roomID := parts[len(parts)-1]
	room, err := h.store.GetRoom(m.Context(), roomID)
	if err != nil {
		natsutil.ReplyError(m.Msg, err.Error())
		return
	}
	natsutil.ReplyJSON(m.Msg, room)
}


func (h *Handler) natsAddMembers(m otelnats.Msg) {
	resp, err := h.handleAddMembers(m.Context(), m.Msg.Subject, m.Msg.Data)
	if err != nil {
		slog.Error("add members failed", "error", err)
		natsutil.ReplyError(m.Msg, sanitizeError(err))
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to add-members message", "error", err)
	}
}

func (h *Handler) natsRemoveMember(m otelnats.Msg) {
	resp, err := h.handleRemoveMember(m.Context(), m.Msg.Subject, m.Msg.Data)
	if err != nil {
		slog.Error("remove member failed", "error", err)
		natsutil.ReplyError(m.Msg, sanitizeError(err))
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to remove-member message", "error", err)
	}
}

func (h *Handler) natsUpdateRole(m otelnats.Msg) {
	resp, err := h.handleUpdateRole(m.Context(), m.Msg.Subject, m.Msg.Data)
	if err != nil {
		slog.Error("update role failed", "error", err)
		natsutil.ReplyError(m.Msg, sanitizeError(err))
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to update-role message", "error", err)
	}
}

func (h *Handler) handleCreateRoom(ctx context.Context, data []byte) ([]byte, error) {
	var req model.CreateRoomRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	now := time.Now().UTC()
	room := model.Room{
		ID:        uuid.New().String(),
		Name:      req.Name,
		Type:      req.Type,
		CreatedBy: req.CreatedBy,
		SiteID:    req.SiteID,
		UserCount: 1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.store.CreateRoom(ctx, &room); err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}

	// Auto-create owner subscription
	sub := model.Subscription{
		ID:                 uuid.New().String(),
		User:               model.SubscriptionUser{ID: req.CreatedBy, Account: req.CreatedByAccount},
		RoomID:             room.ID,
		SiteID:             req.SiteID,
		Roles:              []model.Role{model.RoleOwner},
		HistorySharedSince: &now,
		JoinedAt:           now,
	}
	if err := h.store.CreateSubscription(ctx, &sub); err != nil {
		slog.Warn("create owner subscription failed", "error", err)
	}

	return json.Marshal(room)
}


func (h *Handler) handleRemoveMember(ctx context.Context, subj string, data []byte) ([]byte, error) {
	requester, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid subject: %s", subj)
	}

	var req model.RemoveMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	if req.RoomID != "" && req.RoomID != roomID {
		return nil, fmt.Errorf("room ID mismatch")
	}
	req.RoomID = roomID

	isOrgRemoval := req.OrgID != ""
	hasAccount := req.Account != ""

	// Ambiguous request validation: must specify exactly one of orgId or account
	if isOrgRemoval && hasAccount {
		return nil, fmt.Errorf("ambiguous request: specify either orgId or account, not both")
	}
	if !isOrgRemoval && !hasAccount {
		return nil, fmt.Errorf("invalid request: one of orgId or account is required")
	}

	isSelfLeave := !isOrgRemoval && req.Account == requester

	// Authorization: self-leave needs no check; org removal and remove-other require owner
	if !isSelfLeave {
		sub, err := h.store.GetSubscription(ctx, requester, roomID)
		if err != nil {
			return nil, fmt.Errorf("requester not found: %w", err)
		}
		if !HasRole(sub.Roles, model.RoleOwner) {
			return nil, fmt.Errorf("only owners can remove other members")
		}
	}

	// Last-owner self-leave guard
	if isSelfLeave {
		sub, err := h.store.GetSubscription(ctx, requester, roomID)
		if err != nil {
			return nil, fmt.Errorf("requester not found: %w", err)
		}
		if HasRole(sub.Roles, model.RoleOwner) {
			count, err := h.store.CountOwners(ctx, roomID)
			if err != nil {
				return nil, fmt.Errorf("count owners: %w", err)
			}
			if count <= 1 {
				return nil, fmt.Errorf("last owner cannot leave the room")
			}
		}
	}

	// Re-marshal with roomID set from subject
	data, marshalErr := json.Marshal(req)
	if marshalErr != nil {
		return nil, fmt.Errorf("marshal remove request: %w", marshalErr)
	}

	// Publish validated request to ROOMS stream for room-worker processing
	if err := h.publishToStream(ctx, subj, data); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "accepted"})
}

func (h *Handler) handleUpdateRole(ctx context.Context, subj string, data []byte) ([]byte, error) {
	requester, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid subject: %s", subj)
	}

	var req model.UpdateRoleRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	if req.RoomID != "" && req.RoomID != roomID {
		return nil, fmt.Errorf("room ID mismatch")
	}
	req.RoomID = roomID

	// Authorization: only owners can change roles
	sub, err := h.store.GetSubscription(ctx, requester, roomID)
	if err != nil {
		return nil, fmt.Errorf("requester not found: %w", err)
	}
	if !HasRole(sub.Roles, model.RoleOwner) {
		return nil, fmt.Errorf("only owners can change roles")
	}

	// Validate role
	if req.NewRole != model.RoleOwner && req.NewRole != model.RoleMember {
		return nil, fmt.Errorf("invalid role: %q", req.NewRole)
	}

	// Guard: last owner cannot demote themselves
	if req.NewRole == model.RoleMember && req.Account == requester {
		count, err := h.store.CountOwners(ctx, roomID)
		if err != nil {
			return nil, fmt.Errorf("count owners: %w", err)
		}
		if count <= 1 {
			return nil, fmt.Errorf("cannot demote the last owner")
		}
	}

	// Re-marshal with roomID set from subject
	data, marshalErr := json.Marshal(req)
	if marshalErr != nil {
		return nil, fmt.Errorf("marshal role update request: %w", marshalErr)
	}

	// Publish validated request to ROOMS stream for room-worker processing
	if err := h.publishToStream(ctx, subj, data); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "accepted"})
}

func (h *Handler) handleAddMembers(ctx context.Context, subj string, data []byte) ([]byte, error) {
	// 1. Extract roomID from subject
	_, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid subject: %s", subj)
	}

	// 2. Unmarshal request
	var req model.AddMembersRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	// 3. Expand channels -> channel-sourced org IDs and usernames
	channelOrgIDs, channelUsernames, err := h.expandChannels(ctx, req.Channels)
	if err != nil {
		return nil, err
	}

	// 4. Merge and deduplicate org IDs
	orgIDs := dedup(append(req.Orgs, channelOrgIDs...))

	// 5. Resolve orgs -> org-derived usernames
	orgUsernames, err := h.resolveOrgs(ctx, orgIDs)
	if err != nil {
		return nil, err
	}

	// 6. Merge, deduplicate, and filter bots from all usernames
	usernames := filterBots(dedup(append(append(req.Users, channelUsernames...), orgUsernames...)))

	// 7. Capacity check
	existingCount, err := h.store.CountSubscriptions(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("count subscriptions: %w", err)
	}
	if existingCount+len(usernames) > h.maxRoomSize {
		return nil, fmt.Errorf("room is at maximum capacity (%d)", h.maxRoomSize)
	}

	// 8. Publish normalized request to ROOMS stream for room-worker processing
	normalizedReq := model.AddMembersRequest{
		RoomID:  roomID,
		Users:   usernames,
		Orgs:    orgIDs,
		History: req.History,
	}
	normalizedData, err := json.Marshal(normalizedReq)
	if err != nil {
		return nil, fmt.Errorf("marshal normalized request: %w", err)
	}
	if err := h.publishToStream(ctx, subj, normalizedData); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "accepted"})
}

// expandChannels resolves channel room IDs into org IDs and individual usernames.
// It always merges both room_members and subscriptions for each channel.
func (h *Handler) expandChannels(ctx context.Context, channelIDs []string) (orgIDs, usernames []string, err error) {
	for _, channelID := range channelIDs {
		channelMembers, err := h.store.GetRoomMembers(ctx, channelID)
		if err != nil {
			return nil, nil, fmt.Errorf("get room members for channel %q: %w", channelID, err)
		}
		for _, m := range channelMembers {
			switch m.Member.Type {
			case model.RoomMemberTypeOrg:
				orgIDs = append(orgIDs, m.Member.ID)
			case model.RoomMemberTypeIndividual:
				usernames = append(usernames, m.Member.Account)
			}
		}

		subs, err := h.store.ListSubscriptionsByRoom(ctx, channelID)
		if err != nil {
			return nil, nil, fmt.Errorf("list subscriptions for channel %q: %w", channelID, err)
		}
		for i := range subs {
			usernames = append(usernames, subs[i].User.Account)
		}
	}
	return orgIDs, usernames, nil
}

// resolveOrgs converts org IDs into account names by querying users.
func (h *Handler) resolveOrgs(ctx context.Context, orgIDs []string) ([]string, error) {
	var accounts []string
	for _, orgID := range orgIDs {
		orgAccounts, err := h.store.GetOrgAccounts(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("resolve org %q: %w", orgID, err)
		}
		accounts = append(accounts, orgAccounts...)
	}
	return accounts, nil
}
