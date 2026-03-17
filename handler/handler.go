package handler

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/hmchangw/chat/model"
	"github.com/hmchangw/chat/store"
	"github.com/nats-io/nats.go"
)

// NATS subjects
const (
	SubjectRoomsCreate   = "chat.rooms.create"
	SubjectRoomsList     = "chat.rooms.list"
	SubjectMessagesSend  = "chat.messages.send"
	SubjectMessagesList  = "chat.messages.list"
	SubjectRoomBroadcast = "chat.room.%s" // chat.room.{roomID} for pub/sub
)

// RoomBroadcastSubject returns the pub/sub subject for a specific room.
func RoomBroadcastSubject(roomID string) string {
	return fmt.Sprintf(SubjectRoomBroadcast, roomID)
}

// Handler processes NATS messages using the store.
type Handler struct {
	store store.Store
	nc    *nats.Conn
}

func New(nc *nats.Conn, s store.Store) *Handler {
	return &Handler{nc: nc, store: s}
}

// Register subscribes to all NATS subjects for request/reply CRUD operations.
func (h *Handler) Register() error {
	subs := map[string]nats.MsgHandler{
		SubjectRoomsCreate:  h.createRoom,
		SubjectRoomsList:    h.listRooms,
		SubjectMessagesSend: h.sendMessage,
		SubjectMessagesList: h.listMessages,
	}
	for subj, handler := range subs {
		if _, err := h.nc.Subscribe(subj, handler); err != nil {
			return fmt.Errorf("subscribe %s: %w", subj, err)
		}
		log.Printf("subscribed to %s", subj)
	}
	return nil
}

func (h *Handler) createRoom(msg *nats.Msg) {
	var req model.CreateRoomRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		replyError(msg, "invalid request: "+err.Error())
		return
	}

	room, err := h.store.CreateRoom(req.Name, req.CreatedBy)
	if err != nil {
		replyError(msg, err.Error())
		return
	}
	replyJSON(msg, room)
}

func (h *Handler) listRooms(msg *nats.Msg) {
	rooms, err := h.store.ListRooms()
	if err != nil {
		replyError(msg, err.Error())
		return
	}
	replyJSON(msg, model.ListRoomsResponse{Rooms: rooms})
}

func (h *Handler) sendMessage(msg *nats.Msg) {
	var req model.SendMessageRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		replyError(msg, "invalid request: "+err.Error())
		return
	}

	chatMsg, err := h.store.SendMessage(req.RoomID, req.UserID, req.Content)
	if err != nil {
		replyError(msg, err.Error())
		return
	}

	// Reply to the sender with the created message
	replyJSON(msg, chatMsg)

	// Broadcast to room subscribers (pub/sub)
	data, _ := json.Marshal(chatMsg)
	subject := RoomBroadcastSubject(req.RoomID)
	if err := h.nc.Publish(subject, data); err != nil {
		log.Printf("broadcast to %s failed: %v", subject, err)
	}
}

func (h *Handler) listMessages(msg *nats.Msg) {
	var req model.ListMessagesRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		replyError(msg, "invalid request: "+err.Error())
		return
	}

	messages, err := h.store.ListMessages(req.RoomID, req.Limit)
	if err != nil {
		replyError(msg, err.Error())
		return
	}
	replyJSON(msg, model.ListMessagesResponse{Messages: messages})
}

func replyJSON(msg *nats.Msg, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		replyError(msg, "marshal error: "+err.Error())
		return
	}
	if err := msg.Respond(data); err != nil {
		log.Printf("reply failed: %v", err)
	}
}

func replyError(msg *nats.Msg, errMsg string) {
	data, _ := json.Marshal(model.ErrorResponse{Error: errMsg})
	if err := msg.Respond(data); err != nil {
		log.Printf("error reply failed: %v", err)
	}
}
