package main

import (
	"context"
	"encoding/json"
	"errors"
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

func NewHandler(store RoomStore, siteID string, maxRoomSize int, publishToStream func(context.Context, string, []byte) error) *Handler {
	return &Handler{store: store, siteID: siteID, maxRoomSize: maxRoomSize, publishToStream: publishToStream}
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
	if _, err := nc.QueueSubscribe(subject.MemberInviteWildcard(h.siteID), queue, h.NatsHandleInvite); err != nil {
		return fmt.Errorf("subscribe member invite: %w", err)
	}
	if _, err := nc.QueueSubscribe(subject.MemberRoleUpdateWildcard(h.siteID), queue, h.natsUpdateRole); err != nil {
		return fmt.Errorf("subscribe member role update: %w", err)
	}
	if _, err := nc.QueueSubscribe(subject.MemberRemoveWildcard(h.siteID), queue, h.NatsHandleRemoveMember); err != nil {
		return fmt.Errorf("subscribe member remove: %w", err)
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

// NatsHandleInvite handles invite authorization requests.
func (h *Handler) NatsHandleInvite(m otelnats.Msg) {
	resp, err := h.handleInvite(m.Context(), m.Msg.Subject, m.Msg.Data)
	if err != nil {
		slog.Error("invite failed", "error", err)
		natsutil.ReplyError(m.Msg, sanitizeError(err))
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to message", "error", err)
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

// NatsHandleRemoveMember handles remove-member authorization requests.
func (h *Handler) NatsHandleRemoveMember(m otelnats.Msg) {
	resp, err := h.handleRemoveMember(m.Context(), m.Msg.Subject, m.Msg.Data)
	if err != nil {
		slog.Error("remove member failed", "error", err)
		natsutil.ReplyError(m.Msg, sanitizeError(err))
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to message", "error", err)
	}
}

func (h *Handler) handleRemoveMember(ctx context.Context, subj string, data []byte) ([]byte, error) {
	requesterAccount, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid remove-member subject: %s", subj)
	}

	var req model.RemoveMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	if req.RoomID != "" && req.RoomID != roomID {
		return nil, fmt.Errorf("room ID mismatch")
	}
	req.RoomID = roomID
	req.Requester = requesterAccount

	// Exactly one of Account or OrgID must be set.
	if req.Account == "" && req.OrgID == "" {
		return nil, fmt.Errorf("account or orgId must be set")
	}

	if req.Account != "" {
		// Individual removal (self-leave or owner-removes-other). Validation runs
		// cheapest-first: target membership → requester role (owner-removes only)
		// → room counts. Each step short-circuits so we never run the more
		// expensive count aggregation for requests that would fail early anyway.
		target, err := h.store.GetSubscriptionWithMembership(ctx, roomID, req.Account)
		if err != nil {
			return nil, fmt.Errorf("get target subscription: %w", err)
		}
		if target.HasOrgMembership && !target.HasIndividualMembership {
			return nil, fmt.Errorf("org members cannot leave individually")
		}
		if req.Account != requesterAccount {
			requesterSub, err := h.store.GetSubscription(ctx, requesterAccount, roomID)
			if err != nil {
				return nil, fmt.Errorf("get requester subscription: %w", err)
			}
			if !hasRole(requesterSub.Roles, model.RoleOwner) {
				return nil, fmt.Errorf("only owners can remove members")
			}
		}
		counts, err := h.store.CountMembersAndOwners(ctx, roomID)
		if err != nil {
			return nil, fmt.Errorf("count members: %w", err)
		}
		if counts.MemberCount <= 1 {
			return nil, fmt.Errorf("cannot remove the last member of the room")
		}
		if hasRole(target.Subscription.Roles, model.RoleOwner) && counts.OwnerCount <= 1 {
			return nil, fmt.Errorf("last owner cannot leave the room")
		}
	} else {
		// Owner-removes-org path: only the requester's owner role matters; the org
		// member set is resolved downstream in room-worker.
		sub, err := h.store.GetSubscription(ctx, requesterAccount, roomID)
		if err != nil {
			return nil, fmt.Errorf("get requester subscription: %w", err)
		}
		if !hasRole(sub.Roles, model.RoleOwner) {
			return nil, fmt.Errorf("only owners can remove members")
		}
	}

	// Publish to ROOMS stream for room-worker processing.
	var err error
	data, err = json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal remove member request: %w", err)
	}
	if err := h.publishToStream(ctx, subject.RoomCanonical(h.siteID, "member.remove"), data); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "accepted"})
}

func (h *Handler) handleInvite(ctx context.Context, subj string, data []byte) ([]byte, error) {
	// Extract user account and roomID from the subject before unmarshalling for performance.
	inviterAccount, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid invite subject: %s", subj)
	}

	// Verify inviter is owner (cheap DB lookup before expensive unmarshal)
	sub, err := h.store.GetSubscription(ctx, inviterAccount, roomID)
	if err != nil {
		return nil, fmt.Errorf("inviter not found: %w", err)
	}
	if !hasRole(sub.Roles, model.RoleOwner) {
		return nil, fmt.Errorf("only owners can invite members")
	}

	// Check room size
	room, err := h.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("room not found: %w", err)
	}
	if room.UserCount >= h.maxRoomSize {
		return nil, fmt.Errorf("room is at maximum capacity (%d)", h.maxRoomSize)
	}

	// Only unmarshal after authorization checks pass
	var req model.InviteMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	// Set event timestamp
	req.Timestamp = time.Now().UTC().UnixMilli()

	timestampedData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal invite request: %w", err)
	}

	// Publish to ROOMS stream for room-worker processing
	if err := h.publishToStream(ctx, subject.MemberInvite(inviterAccount, roomID, h.siteID), timestampedData); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "ok"})
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
		return nil, fmt.Errorf("invalid request: room ID mismatch")
	}
	req.RoomID = roomID
	if req.NewRole != model.RoleOwner && req.NewRole != model.RoleMember {
		return nil, errInvalidRole
	}
	room, err := h.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("get room: %w", err)
	}
	if room.Type != model.RoomTypeGroup {
		return nil, errRoomTypeGuard
	}
	requesterSub, err := h.store.GetSubscription(ctx, requester, roomID)
	if err != nil {
		return nil, fmt.Errorf("requester not found: %w", err)
	}
	if !hasRole(requesterSub.Roles, model.RoleOwner) {
		return nil, errOnlyOwners
	}
	targetSub, err := h.store.GetSubscription(ctx, req.Account, roomID)
	if err != nil {
		if errors.Is(err, model.ErrSubscriptionNotFound) {
			return nil, errTargetNotMember
		}
		return nil, fmt.Errorf("get target subscription: %w", err)
	}
	// Promote: target must not already be owner. Demote: target must be owner.
	if req.NewRole == model.RoleOwner && hasRole(targetSub.Roles, model.RoleOwner) {
		return nil, errAlreadyOwner
	}
	if req.NewRole == model.RoleMember && !hasRole(targetSub.Roles, model.RoleOwner) {
		return nil, errNotOwner
	}
	// Last-owner guard: only needed for self-demotion since rule #5 guarantees
	// requester is an owner, so demoting another owner always leaves the requester as owner.
	if req.NewRole == model.RoleMember && req.Account == requester {
		count, err := h.store.CountOwners(ctx, roomID)
		if err != nil {
			return nil, fmt.Errorf("count owners: %w", err)
		}
		if count <= 1 {
			return nil, errCannotDemoteLast
		}
	}
	data, err = json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal role update request: %w", err)
	}
	if err := h.publishToStream(ctx, subject.RoomCanonical(h.siteID, "member.role-update"), data); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}
	return json.Marshal(map[string]string{"status": "accepted"})
}
