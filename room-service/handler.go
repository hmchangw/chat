package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

type Handler struct {
	store           RoomStore
	siteID          string
	currentDomain   string
	maxRoomSize     int
	publishToStream func(subj string, data []byte) error
	publishLocal    func(subj string, data []byte) error
	publishOutbox   func(subj string, data []byte) error
}

func NewHandler(
	store RoomStore,
	siteID string,
	currentDomain string,
	maxRoomSize int,
	publishToStream func(string, []byte) error,
	publishLocal func(string, []byte) error,
	publishOutbox func(string, []byte) error,
) *Handler {
	return &Handler{
		store:           store,
		siteID:          siteID,
		currentDomain:   currentDomain,
		maxRoomSize:     maxRoomSize,
		publishToStream: publishToStream,
		publishLocal:    publishLocal,
		publishOutbox:   publishOutbox,
	}
}

// RegisterCRUD registers NATS request/reply handlers for room CRUD with queue group.
func (h *Handler) RegisterCRUD(nc *nats.Conn) error {
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

func (h *Handler) natsCreateRoom(msg *nats.Msg) {
	resp, err := h.handleCreateRoom(context.Background(), msg.Data)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	if err := msg.Respond(resp); err != nil {
		slog.Error("failed to respond to message", "error", err)
	}
}

func (h *Handler) natsListRooms(msg *nats.Msg) {
	rooms, err := h.store.ListRooms(context.Background())
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	natsutil.ReplyJSON(msg, model.ListRoomsResponse{Rooms: rooms})
}

func (h *Handler) natsGetRoom(msg *nats.Msg) {
	parts := strings.Split(msg.Subject, ".")
	roomID := parts[len(parts)-1]
	room, err := h.store.GetRoom(context.Background(), roomID)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	natsutil.ReplyJSON(msg, room)
}

// NatsHandleInvite handles invite authorization requests.
func (h *Handler) NatsHandleInvite(msg *nats.Msg) {
	resp, err := h.handleInvite(context.Background(), msg.Subject, msg.Data)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	if err := msg.Respond(resp); err != nil {
		slog.Error("failed to respond to message", "error", err)
	}
}

func (h *Handler) natsAddMembers(msg *nats.Msg) {
	resp, err := h.handleAddMembers(context.Background(), msg.Subject, msg.Data)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	if err := msg.Respond(resp); err != nil {
		slog.Error("failed to respond to add-members message", "error", err)
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
		User:               model.SubscriptionUser{ID: req.CreatedBy, Username: req.CreatedByUsername},
		RoomID:             room.ID,
		SiteID:             req.SiteID,
		Roles:              []model.Role{model.RoleOwner},
		SharedHistorySince: now,
		JoinedAt:           now,
	}
	if err := h.store.CreateSubscription(ctx, &sub); err != nil {
		slog.Warn("create owner subscription failed", "error", err)
	}

	return json.Marshal(room)
}

func (h *Handler) handleInvite(ctx context.Context, subj string, data []byte) ([]byte, error) {
	// Extract username and roomID from the subject before unmarshalling for performance.
	inviterUsername, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid invite subject: %s", subj)
	}

	// Verify inviter is owner (cheap DB lookup before expensive unmarshal)
	sub, err := h.store.GetSubscription(ctx, inviterUsername, roomID)
	if err != nil {
		return nil, fmt.Errorf("inviter not found: %w", err)
	}
	if !HasRole(sub.Roles, model.RoleOwner) {
		return nil, fmt.Errorf("only owners can invite members")
	}

	// Check room size via live subscription count (bots excluded)
	count, err := h.store.CountSubscriptions(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("count subscriptions: %w", err)
	}
	if count >= h.maxRoomSize {
		return nil, fmt.Errorf("room is at maximum capacity (%d)", h.maxRoomSize)
	}

	// Only unmarshal after authorization checks pass
	var req model.InviteMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	// Publish to ROOMS stream for room-worker processing
	if err := h.publishToStream(subj, data); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "ok"})
}

func (h *Handler) natsRemoveMember(msg *nats.Msg) {
	resp, err := h.handleRemoveMember(context.Background(), msg.Subject, msg.Data)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	if err := msg.Respond(resp); err != nil {
		slog.Error("failed to respond to remove-member message", "error", err)
	}
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

	isOrgRemoval := req.OrgID != ""
	isSelfLeave := !isOrgRemoval && req.Username == requester

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

	if isOrgRemoval {
		name, locationURL, err := h.store.GetOrgData(ctx, req.OrgID)
		if err != nil {
			return nil, fmt.Errorf("resolve org %q: %w", req.OrgID, err)
		}
		var orgUsername string
		if locationURL == h.currentDomain {
			orgUsername = name
		} else {
			orgUsername = name + "@" + extractDomain(locationURL)
		}
		if err := h.store.DeleteSubscription(ctx, orgUsername, roomID); err != nil {
			return nil, fmt.Errorf("delete org subscription: %w", err)
		}
		if err := h.store.DeleteOrgRoomMember(ctx, req.OrgID, roomID); err != nil {
			return nil, fmt.Errorf("delete org room member: %w", err)
		}
	} else {
		if err := h.store.DeleteSubscription(ctx, req.Username, roomID); err != nil {
			return nil, fmt.Errorf("delete subscription: %w", err)
		}
		if err := h.store.DeleteRoomMember(ctx, req.Username, roomID); err != nil {
			return nil, fmt.Errorf("delete room member: %w", err)
		}
	}

	return json.Marshal(map[string]string{"status": "ok"})
}

func (h *Handler) natsUpdateRole(msg *nats.Msg) {
	resp, err := h.handleUpdateRole(context.Background(), msg.Subject, msg.Data)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	if err := msg.Respond(resp); err != nil {
		slog.Error("failed to respond to update-role message", "error", err)
	}
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

	// Guard: federation users cannot be promoted to owner
	if req.NewRole == model.RoleOwner && strings.Contains(req.Username, "@") {
		return nil, fmt.Errorf("federation users cannot be promoted to owner")
	}

	// Guard: last owner cannot demote themselves
	if req.NewRole == model.RoleMember && req.Username == requester {
		count, err := h.store.CountOwners(ctx, roomID)
		if err != nil {
			return nil, fmt.Errorf("count owners: %w", err)
		}
		if count <= 1 {
			return nil, fmt.Errorf("cannot demote the last owner")
		}
	}

	if err := h.store.UpdateSubscriptionRole(ctx, req.Username, roomID, req.NewRole); err != nil {
		return nil, fmt.Errorf("update role: %w", err)
	}

	return json.Marshal(map[string]string{"status": "ok"})
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

	// 3. Expand channels → channel-sourced org IDs and usernames
	channelOrgIDs, channelUsernames, err := h.expandChannels(ctx, req.Channels)
	if err != nil {
		return nil, err
	}

	// 4. Merge and deduplicate org IDs
	orgIDs := dedup(append(req.Orgs, channelOrgIDs...))

	// 5. Resolve orgs → org-derived usernames
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
	if existingCount+len(usernames) >= h.maxRoomSize {
		return nil, fmt.Errorf("room is at maximum capacity (%d)", h.maxRoomSize)
	}

	// 8. Build subscriptions (skips users whose IDs cannot be resolved)
	now := time.Now().UTC()
	subs, resolvedUsernames := h.buildSubscriptions(ctx, usernames, roomID, req.History.Mode, now)

	// 9. Persist subscriptions
	if err := h.store.BulkCreateSubscriptions(ctx, subs); err != nil {
		return nil, fmt.Errorf("create subscriptions: %w", err)
	}

	// 10. Write room_members documents conditionally
	if err := h.writeRoomMembers(ctx, roomID, orgIDs, resolvedUsernames, now); err != nil {
		return nil, err
	}

	// 11. Dispatch per-user notifications asynchronously
	go h.dispatchMemberEvents(context.Background(), resolvedUsernames, roomID)

	return json.Marshal(map[string]string{"status": "ok"})
}

// expandChannels resolves channel room IDs into org IDs and individual usernames.
// For each channel, it uses room_members when populated, otherwise falls back to subscriptions.
func (h *Handler) expandChannels(ctx context.Context, channelIDs []string) (orgIDs, usernames []string, err error) {
	for _, channelID := range channelIDs {
		channelMembers, err := h.store.GetRoomMembers(ctx, channelID)
		if err != nil {
			return nil, nil, fmt.Errorf("get room members for channel %q: %w", channelID, err)
		}
		if len(channelMembers) > 0 {
			for _, m := range channelMembers {
				switch m.Member.Type {
				case model.RoomMemberTypeOrg:
					orgIDs = append(orgIDs, m.Member.ID)
				case model.RoomMemberTypeIndividual:
					usernames = append(usernames, m.Member.Username)
				}
			}
		} else {
			subs, err := h.store.ListSubscriptionsByRoom(ctx, channelID)
			if err != nil {
				return nil, nil, fmt.Errorf("list subscriptions for channel %q: %w", channelID, err)
			}
			for i := range subs {
				usernames = append(usernames, subs[i].User.Username)
			}
		}
	}
	return orgIDs, usernames, nil
}

// resolveOrgs converts org IDs into usernames by looking up org data.
// Local orgs (matching currentDomain) produce a plain username; remote orgs produce name@domain.
func (h *Handler) resolveOrgs(ctx context.Context, orgIDs []string) ([]string, error) {
	usernames := make([]string, 0, len(orgIDs))
	for _, orgID := range orgIDs {
		name, locationURL, err := h.store.GetOrgData(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("resolve org %q: %w", orgID, err)
		}
		var username string
		if locationURL == h.currentDomain {
			username = name
		} else {
			username = name + "@" + extractDomain(locationURL)
		}
		usernames = append(usernames, username)
	}
	return usernames, nil
}

// buildSubscriptions creates Subscription structs for the given usernames.
// Users whose IDs cannot be resolved are logged and skipped.
// Returns the built subscriptions and the corresponding slice of resolved usernames.
func (h *Handler) buildSubscriptions(ctx context.Context, usernames []string, roomID string, mode model.HistoryMode, now time.Time) ([]*model.Subscription, []string) {
	var subs []*model.Subscription
	var resolvedUsernames []string
	for _, username := range usernames {
		userID, err := h.store.GetUserID(ctx, username)
		if err != nil {
			slog.Warn("skipping user, id not found", "username", username)
			continue
		}
		sub := &model.Subscription{
			ID:       uuid.New().String(),
			User:     model.SubscriptionUser{ID: userID, Username: username},
			RoomID:   roomID,
			SiteID:   h.siteID,
			Roles:    []model.Role{model.RoleMember},
			JoinedAt: now,
		}
		if mode != model.HistoryModeAll {
			sub.SharedHistorySince = now
		}
		subs = append(subs, sub)
		resolvedUsernames = append(resolvedUsernames, username)
	}
	return subs, resolvedUsernames
}

// writeRoomMembers conditionally writes org and individual room_members documents.
// Org docs are written when orgIDs is non-empty; individual docs are written when
// orgIDs is non-empty or the room already has existing members.
func (h *Handler) writeRoomMembers(ctx context.Context, roomID string, orgIDs, resolvedUsernames []string, now time.Time) error {
	existingMembers, err := h.store.GetRoomMembers(ctx, roomID)
	if err != nil {
		return fmt.Errorf("get room members: %w", err)
	}
	hasOrgs := len(orgIDs) > 0
	hasExistingMembers := len(existingMembers) > 0

	if hasOrgs {
		for _, orgID := range orgIDs {
			orgMember := &model.RoomMember{
				ID:     uuid.New().String(),
				RoomID: roomID,
				Ts:     now,
				Member: model.RoomMemberEntry{ID: orgID, Type: model.RoomMemberTypeOrg},
			}
			if err := h.store.CreateRoomMember(ctx, orgMember); err != nil {
				return fmt.Errorf("create org room member %q: %w", orgID, err)
			}
		}
	}
	if hasOrgs || hasExistingMembers {
		for _, username := range resolvedUsernames {
			indMember := &model.RoomMember{
				ID:     uuid.New().String(),
				RoomID: roomID,
				Ts:     now,
				Member: model.RoomMemberEntry{Type: model.RoomMemberTypeIndividual, Username: username},
			}
			if err := h.store.CreateRoomMember(ctx, indMember); err != nil {
				return fmt.Errorf("create individual room member %q: %w", username, err)
			}
		}
	}
	return nil
}

// dispatchMemberEvents sends a notification to each added user.
// Users on the same site receive a direct NATS publish; remote users receive an OUTBOX event.
// The federation.origin value in the users collection must match the siteID format used in NATS subjects.
func (h *Handler) dispatchMemberEvents(ctx context.Context, usernames []string, roomID string) {
	for _, username := range usernames {
		siteID, err := h.store.GetUserSite(ctx, username)
		if err != nil {
			slog.Error("get user site failed", "username", username, "error", err)
			continue
		}

		payload, err := json.Marshal(map[string]string{"username": username, "roomId": roomID})
		if err != nil {
			slog.Error("marshal notification payload failed", "username", username, "error", err)
			continue
		}

		if siteID == h.siteID {
			notifSubj := subject.Notification(username)
			if err := h.publishLocal(notifSubj, payload); err != nil {
				slog.Error("publish local notification failed", "username", username, "error", err)
			}
		} else {
			event := model.OutboxEvent{
				Type:       "member_added",
				SiteID:     h.siteID,
				DestSiteID: siteID,
				Payload:    payload,
			}
			eventData, err := json.Marshal(event)
			if err != nil {
				slog.Error("marshal outbox event failed", "username", username, "error", err)
				continue
			}
			outboxSubj := subject.Outbox(h.siteID, siteID, "member_added")
			if err := h.publishOutbox(outboxSubj, eventData); err != nil {
				slog.Error("publish outbox event failed", "username", username, "error", err)
			}
		}
	}
}
