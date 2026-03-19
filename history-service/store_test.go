package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

func TestMemoryStore_ListMessages_Pagination(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		s.messages = append(s.messages, model.Message{
			ID: fmt.Sprintf("m%d", i), RoomID: "r1", UserID: "u1",
			Content:   fmt.Sprintf("msg-%d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}

	// Get last 3 messages (before now, since epoch)
	msgs, err := s.ListMessages(ctx, "r1", time.Time{}, time.Now(), 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	// Should be most recent first
	if msgs[0].Content != "msg-9" {
		t.Errorf("first message = %q, want msg-9", msgs[0].Content)
	}
}

func TestMemoryStore_ListMessages_SharedHistorySince(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		s.messages = append(s.messages, model.Message{
			ID: fmt.Sprintf("m%d", i), RoomID: "r1",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}

	// Since = 5 minutes after base → should only get messages 5-9
	since := base.Add(5 * time.Minute)
	msgs, err := s.ListMessages(ctx, "r1", since, time.Now(), 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("got %d messages, want 5", len(msgs))
	}
}

func TestMemoryStore_GetSubscription(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	s.subscriptions = append(s.subscriptions, model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleMember,
		SharedHistorySince: time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC),
	})

	sub, err := s.GetSubscription(ctx, "u1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.UserID != "u1" {
		t.Errorf("UserID = %q", sub.UserID)
	}

	_, err = s.GetSubscription(ctx, "u2", "r1")
	if err == nil {
		t.Error("expected error for missing subscription")
	}
}
