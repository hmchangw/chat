package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/hmchangw/chat/pkg/model"
)

type SubscriptionStore interface {
	CreateSubscription(ctx context.Context, sub model.Subscription) error
	ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
	IncrementUserCount(ctx context.Context, roomID string) error
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
}

type MemoryStore struct {
	mu            sync.RWMutex
	subscriptions []model.Subscription
	rooms         map[string]model.Room
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rooms: make(map[string]model.Room)}
}

func (s *MemoryStore) CreateSubscription(_ context.Context, sub model.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions = append(s.subscriptions, sub)
	return nil
}

func (s *MemoryStore) ListByRoom(_ context.Context, roomID string) ([]model.Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []model.Subscription
	for _, sub := range s.subscriptions {
		if sub.RoomID == roomID {
			result = append(result, sub)
		}
	}
	return result, nil
}

func (s *MemoryStore) IncrementUserCount(_ context.Context, roomID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	room, ok := s.rooms[roomID]
	if !ok {
		return fmt.Errorf("room %q not found", roomID)
	}
	room.UserCount++
	s.rooms[roomID] = room
	return nil
}

func (s *MemoryStore) GetRoom(_ context.Context, roomID string) (*model.Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	room, ok := s.rooms[roomID]
	if !ok {
		return nil, fmt.Errorf("room %q not found", roomID)
	}
	return &room, nil
}
