package main

import (
	"context"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
)

func TestMemoryStore_CreateSubscription(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	sub := model.Subscription{ID: "s1", UserID: "u1", RoomID: "r1", Role: model.RoleMember}
	if err := s.CreateSubscription(ctx, sub); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	subs, err := s.ListByRoom(ctx, "r1")
	if err != nil {
		t.Fatalf("ListByRoom: %v", err)
	}
	if len(subs) != 1 || subs[0].UserID != "u1" {
		t.Errorf("got %+v", subs)
	}
}

func TestMemoryStore_IncrementUserCount(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	s.rooms["r1"] = model.Room{ID: "r1", Name: "general", UserCount: 5}
	if err := s.IncrementUserCount(ctx, "r1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	room, _ := s.GetRoom(ctx, "r1")
	if room.UserCount != 6 {
		t.Errorf("UserCount = %d, want 6", room.UserCount)
	}
}
