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
	maxRoomSize     int
	publishToStream func(data []byte) error
	publishLocal    func(subj string, data []byte) error
	publishOutbox   func(subj string, data []byte) error
}

func NewHandler(
	store RoomStore,
	siteID string,
	maxRoomSize int,
	publishToStream func([]byte) error,
	publishLocal func(string, []byte) error,
	publishOutbox func(string, []byte) error,
) *Handler {
	return &Handler{
		store:           store,
		siteID:          siteID,
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
		Role:               model.RoleOwner,
		HistorySharedSince: &now,
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
	if sub.Role != model.RoleOwner {
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

	// Publish to ROOMS stream for room-worker processing
	if err := h.publishToStream(data); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "ok"})
}

func (h *Handler) handleAddMembers(ctx context.Context, subj string, data []byte) ([]byte, error) {
	// 1. Extract inviter and roomID from subject
	inviterUsername, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid subject: %s", subj)
	}

	// 2. Verify inviter is owner
	sub, err := h.store.GetSubscription(ctx, inviterUsername, roomID)
	if err != nil {
		return nil, fmt.Errorf("inviter not found: %w", err)
	}
	if sub.Role != model.RoleOwner {
		return nil, fmt.Errorf("only owners can add members")
	}

	// 3. Check room capacity
	room, err := h.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("room not found: %w", err)
	}
	if room.UserCount >= h.maxRoomSize {
		return nil, fmt.Errorf("room is at maximum capacity (%d)", h.maxRoomSize)
	}

	// 4. Unmarshal request (after authorization to avoid unnecessary work)
	var req model.AddMembersRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	// 5. Resolve all members to usernames; track whether any orgs are present
	var usernames []string
	hasOrgs := false
	for _, entry := range req.Members {
		switch entry.Type {
		case model.MemberEntryTypeUser:
			usernames = append(usernames, entry.Username)
		case model.MemberEntryTypeOrg:
			hasOrgs = true
			orgUsers, err := h.store.GetOrgUsers(ctx, entry.OrgID)
			if err != nil {
				return nil, fmt.Errorf("resolve org %q: %w", entry.OrgID, err)
			}
			usernames = append(usernames, orgUsers...)
		default:
			return nil, fmt.Errorf("unknown member type: %q", entry.Type)
		}
	}

	// 6. Check whether room_members docs exist for this room
	existingMembers, err := h.store.GetRoomMembers(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("get room members: %w", err)
	}
	hasExistingMembers := len(existingMembers) > 0

	// 7. Write subscriptions for all resolved users
	now := time.Now().UTC()
	subs := make([]*model.Subscription, len(usernames))
	for i, username := range usernames {
		subs[i] = &model.Subscription{
			ID:                 uuid.New().String(),
			User:               model.SubscriptionUser{Username: username},
			RoomID:             roomID,
			SiteID:             h.siteID,
			Role:               model.RoleMember,
			SharedHistorySince: now,
			JoinedAt:           now,
		}
	}
	if err := h.store.BulkCreateSubscriptions(ctx, subs); err != nil {
		return nil, fmt.Errorf("create subscriptions: %w", err)
	}

	// 8. Conditionally write to room_members:
	//    - orgs present: write org docs + individual docs for all resolved users
	//    - only individuals + existing members: write individual docs only
	//    - only individuals + no existing members: skip room_members entirely
	if hasOrgs {
		for _, entry := range req.Members {
			if entry.Type == model.MemberEntryTypeOrg {
				orgMember := &model.RoomMember{
					ID:     uuid.New().String(),
					RoomID: roomID,
					Type:   model.RoomMemberTypeOrg,
					OrgID:  entry.OrgID,
				}
				if err := h.store.CreateRoomMember(ctx, orgMember); err != nil {
					return nil, fmt.Errorf("create org room member %q: %w", entry.OrgID, err)
				}
			}
		}
		for _, username := range usernames {
			indMember := &model.RoomMember{
				ID:       uuid.New().String(),
				RoomID:   roomID,
				Type:     model.RoomMemberTypeIndividual,
				Username: username,
			}
			if err := h.store.CreateRoomMember(ctx, indMember); err != nil {
				return nil, fmt.Errorf("create individual room member %q: %w", username, err)
			}
		}
	} else if hasExistingMembers {
		for _, username := range usernames {
			indMember := &model.RoomMember{
				ID:       uuid.New().String(),
				RoomID:   roomID,
				Type:     model.RoomMemberTypeIndividual,
				Username: username,
			}
			if err := h.store.CreateRoomMember(ctx, indMember); err != nil {
				return nil, fmt.Errorf("create individual room member %q: %w", username, err)
			}
		}
	}

	// 9. Dispatch per-user notifications asynchronously
	go h.dispatchMemberEvents(context.Background(), usernames, roomID)

	return json.Marshal(map[string]string{"status": "ok"})
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
