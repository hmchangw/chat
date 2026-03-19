package main

import (
	"context"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

func TestMemoryStore_GetSubscription(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	// No subscription → error
	_, err := s.GetSubscription(ctx, "u1", "r1")
	if err == nil {
		t.Fatal("expected error for missing subscription")
	}

	// Add subscription → found
	s.subscriptions = append(s.subscriptions, model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleMember,
	})
	sub, err := s.GetSubscription(ctx, "u1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.UserID != "u1" || sub.RoomID != "r1" {
		t.Errorf("got %+v", sub)
	}
}

func TestMemoryStore_SaveMessage(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	msg := model.Message{ID: "m1", RoomID: "r1", UserID: "u1", Content: "hello", CreatedAt: time.Now()}
	if err := s.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(s.messages) != 1 || s.messages[0].ID != "m1" {
		t.Errorf("messages = %+v", s.messages)
	}
}

func TestMemoryStore_UpdateRoomLastMessage(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	s.rooms = append(s.rooms, model.Room{ID: "r1", Name: "general"})
	now := time.Now()
	if err := s.UpdateRoomLastMessage(ctx, "r1", now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
