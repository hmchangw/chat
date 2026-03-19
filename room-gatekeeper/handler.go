package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/nats-io/nats.go"
)

type Handler struct {
	store           RoomStore
	siteID          string
	maxRoomSize     int
	publishToStream func(data []byte) error
}

func NewHandler(store RoomStore, siteID string, maxRoomSize int, publishToStream func([]byte) error) *Handler {
	return &Handler{store: store, siteID: siteID, maxRoomSize: maxRoomSize, publishToStream: publishToStream}
}

// RegisterCRUD registers NATS request/reply handlers for room CRUD.
func (h *Handler) RegisterCRUD(nc *nats.Conn) error {
	if _, err := nc.Subscribe("chat.rooms.create", h.natsCreateRoom); err != nil {
		return err
	}
	if _, err := nc.Subscribe("chat.rooms.list", h.natsListRooms); err != nil {
		return err
	}
	if _, err := nc.Subscribe("chat.rooms.get.*", h.natsGetRoom); err != nil {
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
	msg.Respond(resp)
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
	// Subject: chat.rooms.get.{roomID}
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
	resp, err := h.handleInvite(context.Background(), msg.Data)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	msg.Respond(resp)
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

	if err := h.store.CreateRoom(ctx, room); err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}

	// Auto-create owner subscription
	sub := model.Subscription{
		ID:                 uuid.New().String(),
		UserID:             req.CreatedBy,
		RoomID:             room.ID,
		SiteID:             req.SiteID,
		Role:               model.RoleOwner,
		SharedHistorySince: now,
		JoinedAt:           now,
	}
	if err := h.store.CreateSubscription(ctx, sub); err != nil {
		log.Printf("create owner subscription: %v", err)
	}

	return json.Marshal(room)
}

func (h *Handler) handleInvite(ctx context.Context, data []byte) ([]byte, error) {
	var req model.InviteMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	// Verify inviter is owner
	sub, err := h.store.GetSubscription(ctx, req.InviterID, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("inviter not found: %w", err)
	}
	if sub.Role != model.RoleOwner {
		return nil, fmt.Errorf("only owners can invite members")
	}

	// Check room size
	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("room not found: %w", err)
	}
	if room.UserCount >= h.maxRoomSize {
		return nil, fmt.Errorf("room is at maximum capacity (%d)", h.maxRoomSize)
	}

	// Publish to ROOMS stream for room-worker processing
	if err := h.publishToStream(data); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "ok"})
}
