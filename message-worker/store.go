package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

// MessageStore defines persistence operations for the message worker.
type MessageStore interface {
	GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
	SaveMessage(ctx context.Context, msg model.Message) error
	UpdateRoomLastMessage(ctx context.Context, roomID string, at time.Time) error
}

// MemoryStore is an in-memory implementation for testing.
type MemoryStore struct {
	subscriptions []model.Subscription
	messages      []model.Message
	rooms         []model.Room
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (s *MemoryStore) GetSubscription(_ context.Context, userID, roomID string) (*model.Subscription, error) {
	for i := range s.subscriptions {
		if s.subscriptions[i].UserID == userID && s.subscriptions[i].RoomID == roomID {
			return &s.subscriptions[i], nil
		}
	}
	return nil, fmt.Errorf("subscription not found: user=%s room=%s", userID, roomID)
}

func (s *MemoryStore) SaveMessage(_ context.Context, msg model.Message) error {
	s.messages = append(s.messages, msg)
	return nil
}

func (s *MemoryStore) UpdateRoomLastMessage(_ context.Context, roomID string, at time.Time) error {
	for i := range s.rooms {
		if s.rooms[i].ID == roomID {
			s.rooms[i].UpdatedAt = at
			return nil
		}
	}
	return nil
}
