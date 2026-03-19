package main

import (
	"context"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
)

func TestMemoryStore_CreateAndGetRoom(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	room := model.Room{ID: "r1", Name: "general", Type: model.RoomTypeGroup, SiteID: "site-a", CreatedBy: "u1"}
	if err := s.CreateRoom(ctx, room); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	got, err := s.GetRoom(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoom: %v", err)
	}
	if got.Name != "general" {
		t.Errorf("Name = %q", got.Name)
	}
}

func TestMemoryStore_ListRooms(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	s.CreateRoom(ctx, model.Room{ID: "r1", Name: "general"})
	s.CreateRoom(ctx, model.Room{ID: "r2", Name: "random"})

	rooms, err := s.ListRooms(ctx)
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) != 2 {
		t.Errorf("got %d rooms", len(rooms))
	}
}

func TestMemoryStore_Subscription(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	sub := model.Subscription{ID: "s1", UserID: "u1", RoomID: "r1", Role: model.RoleOwner}
	if err := s.CreateSubscription(ctx, sub); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	got, err := s.GetSubscription(ctx, "u1", "r1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if got.Role != model.RoleOwner {
		t.Errorf("Role = %q", got.Role)
	}

	// Not found
	_, err = s.GetSubscription(ctx, "u2", "r1")
	if err == nil {
		t.Error("expected error for missing subscription")
	}
}
