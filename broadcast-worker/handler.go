package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// RoomLookup reads room data and membership from a data store.
type RoomLookup interface {
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
}

// Publisher abstracts NATS publishing so the handler is testable.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Handler processes fanout messages and broadcasts to room/user streams.
type Handler struct {
	rooms RoomLookup
	pub   Publisher
}

func NewHandler(rooms RoomLookup, pub Publisher) *Handler {
	return &Handler{rooms: rooms, pub: pub}
}

// HandleMessage processes a single JetStream message payload.
// It unmarshals the MessageEvent, looks up the room, publishes a
// RoomMetadataUpdateEvent, and fans the message out to the appropriate
// stream subjects based on the room type.
func (h *Handler) HandleMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	// --- Look up room ---
	room, err := h.rooms.GetRoom(ctx, evt.RoomID)
	if err != nil {
		return fmt.Errorf("get room %s: %w", evt.RoomID, err)
	}

	// --- Publish RoomMetadataUpdateEvent ---
	metaEvt := model.RoomMetadataUpdateEvent{
		RoomID:        room.ID,
		Name:          room.Name,
		UserCount:     room.UserCount,
		LastMessageAt: evt.Message.CreatedAt,
		UpdatedAt:     evt.Message.CreatedAt,
	}

	metaData, err := natsutil.MarshalResponse(metaEvt)
	if err != nil {
		return fmt.Errorf("marshal metadata event: %w", err)
	}

	metaSubj := subject.RoomMetadataUpdate(room.ID)
	if err := h.pub.Publish(metaSubj, metaData); err != nil {
		return fmt.Errorf("publish metadata update: %w", err)
	}

	// --- Fan out message based on room type ---
	evtData, err := natsutil.MarshalResponse(evt)
	if err != nil {
		return fmt.Errorf("marshal message event: %w", err)
	}

	switch room.Type {
	case model.RoomTypeGroup:
		subj := subject.RoomMsgStream(room.ID)
		if err := h.pub.Publish(subj, evtData); err != nil {
			return fmt.Errorf("publish to group room stream: %w", err)
		}

	case model.RoomTypeDM:
		subs, err := h.rooms.ListSubscriptions(ctx, room.ID)
		if err != nil {
			return fmt.Errorf("list subscriptions for DM room %s: %w", room.ID, err)
		}

		for _, sub := range subs {
			subj := subject.UserMsgStream(sub.UserID)
			if err := h.pub.Publish(subj, evtData); err != nil {
				log.Printf("failed to publish to user %s stream: %v", sub.UserID, err)
				// Continue publishing to remaining members.
			}
		}

	default:
		log.Printf("unknown room type %q for room %s, skipping fan-out", room.Type, room.ID)
	}

	return nil
}
