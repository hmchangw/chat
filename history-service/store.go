package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

type HistoryStore interface {
	GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
	ListMessages(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error)
}

type MemoryStore struct {
	subscriptions []model.Subscription
	messages      []model.Message
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

func (s *MemoryStore) ListMessages(_ context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error) {
	var filtered []model.Message
	for _, m := range s.messages {
		if m.RoomID != roomID {
			continue
		}
		if !since.IsZero() && m.CreatedAt.Before(since) {
			continue
		}
		if !before.IsZero() && !m.CreatedAt.Before(before) {
			continue
		}
		filtered = append(filtered, m)
	}

	// Sort descending by CreatedAt
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}
