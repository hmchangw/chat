package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_ProcessInvite(t *testing.T) {
	store := NewMemoryStore()
	store.rooms["r1"] = model.Room{ID: "r1", Name: "general", UserCount: 1, SiteID: "site-a"}
	store.subscriptions = append(store.subscriptions, model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleOwner, SiteID: "site-a",
	})

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-a", publish: func(subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	if err := h.processInvite(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify subscription created
	subs, _ := store.ListByRoom(context.Background(), "r1")
	found := false
	for _, s := range subs {
		if s.UserID == "u2" {
			found = true
			if s.Role != model.RoleMember {
				t.Errorf("role = %q, want member", s.Role)
			}
		}
	}
	if !found {
		t.Error("subscription for u2 not created")
	}

	// Verify room user count incremented
	room, _ := store.GetRoom(context.Background(), "r1")
	if room.UserCount != 2 {
		t.Errorf("UserCount = %d, want 2", room.UserCount)
	}

	// Verify notifications published (subscription update + room metadata for existing members)
	if len(published) < 2 {
		t.Errorf("expected at least 2 publishes, got %d", len(published))
	}
}

type publishedMsg struct {
	subj string
	data []byte
}
