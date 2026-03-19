package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_CreateRoom(t *testing.T) {
	store := NewMemoryStore()
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000}

	req := model.CreateRoomRequest{Name: "general", Type: model.RoomTypeGroup, CreatedBy: "u1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	resp, err := h.handleCreateRoom(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var room model.Room
	json.Unmarshal(resp, &room)
	if room.Name != "general" || room.CreatedBy != "u1" {
		t.Errorf("got %+v", room)
	}

	// Verify owner subscription was created
	sub, err := store.GetSubscription(context.Background(), "u1", room.ID)
	if err != nil {
		t.Fatalf("owner subscription not created: %v", err)
	}
	if sub.Role != model.RoleOwner {
		t.Errorf("role = %q, want owner", sub.Role)
	}
}

func TestHandler_InviteOwner_Success(t *testing.T) {
	store := NewMemoryStore()
	store.CreateRoom(context.Background(), model.Room{ID: "r1", Name: "general", UserCount: 1})
	store.CreateSubscription(context.Background(), model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleOwner,
	})

	var jsPublished []byte
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(data []byte) error { jsPublished = data; return nil },
	}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	_, err := h.handleInvite(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jsPublished == nil {
		t.Error("expected event published to JetStream")
	}
}

func TestHandler_InviteMember_Rejected(t *testing.T) {
	store := NewMemoryStore()
	store.CreateRoom(context.Background(), model.Room{ID: "r1", Name: "general", UserCount: 1})
	store.CreateSubscription(context.Background(), model.Subscription{
		UserID: "u2", RoomID: "r1", Role: model.RoleMember, // not owner
	})

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(data []byte) error { return nil },
	}

	req := model.InviteMemberRequest{InviterID: "u2", InviteeID: "u3", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	_, err := h.handleInvite(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for non-owner invite")
	}
}

func TestHandler_InviteExceedsMaxSize(t *testing.T) {
	store := NewMemoryStore()
	store.CreateRoom(context.Background(), model.Room{ID: "r1", Name: "general", UserCount: 1000})
	store.CreateSubscription(context.Background(), model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleOwner,
	})

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(data []byte) error { return nil },
	}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	_, err := h.handleInvite(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for room at max size")
	}
}
