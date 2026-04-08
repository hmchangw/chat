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
	publishToStream func(ctx context.Context, data []byte) error
}

func NewHandler(store RoomStore, siteID string, maxRoomSize int, publishToStream func(context.Context, []byte) error) *Handler {
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
		natsutil.ReplyError(m.Msg, err.Error())
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

	// Set event timestamp
	req.Timestamp = time.Now().UTC().UnixMilli()

	timestampedData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal invite request: %w", err)
	}

	// Publish to ROOMS stream for room-worker processing
	if err := h.publishToStream(ctx, timestampedData); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "ok"})
}
