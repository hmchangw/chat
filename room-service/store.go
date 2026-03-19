package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/hmchangw/chat/pkg/model"
)

type RoomStore interface {
	CreateRoom(ctx context.Context, room model.Room) error
	GetRoom(ctx context.Context, id string) (*model.Room, error)
	ListRooms(ctx context.Context) ([]model.Room, error)
	GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
	CreateSubscription(ctx context.Context, sub model.Subscription) error
}

type MemoryStore struct {
	mu            sync.RWMutex
	rooms         map[string]model.Room
	subscriptions []model.Subscription
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rooms: make(map[string]model.Room)}
}

func (s *MemoryStore) CreateRoom(_ context.Context, room model.Room) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rooms[room.ID] = room
	return nil
}

func (s *MemoryStore) GetRoom(_ context.Context, id string) (*model.Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	room, ok := s.rooms[id]
	if !ok {
		return nil, fmt.Errorf("room %q not found", id)
	}
	return &room, nil
}

func (s *MemoryStore) ListRooms(_ context.Context) ([]model.Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rooms := make([]model.Room, 0, len(s.rooms))
	for _, r := range s.rooms {
		rooms = append(rooms, r)
	}
	return rooms, nil
}

func (s *MemoryStore) GetSubscription(_ context.Context, userID, roomID string) (*model.Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.subscriptions {
		if s.subscriptions[i].UserID == userID && s.subscriptions[i].RoomID == roomID {
			return &s.subscriptions[i], nil
		}
	}
	return nil, fmt.Errorf("subscription not found: user=%s room=%s", userID, roomID)
}

func (s *MemoryStore) CreateSubscription(_ context.Context, sub model.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions = append(s.subscriptions, sub)
	return nil
}
