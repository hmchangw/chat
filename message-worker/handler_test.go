package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_ProcessMessage_Success(t *testing.T) {
	store := NewMemoryStore()
	store.subscriptions = append(store.subscriptions, model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleMember,
	})
	store.rooms = append(store.rooms, model.Room{ID: "r1", Name: "general"})

	var published []publishedMsg
	publisher := func(subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}

	h := &Handler{store: store, siteID: "site-a", publish: publisher}

	req := model.SendMessageRequest{RoomID: "r1", Content: "hello", RequestID: "req-1"}
	data, _ := json.Marshal(req)

	reply, err := h.processMessage(context.Background(), "u1", "r1", "site-a", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify reply contains created message
	var msg model.Message
	if err := json.Unmarshal(reply, &msg); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if msg.Content != "hello" || msg.UserID != "u1" || msg.RoomID != "r1" {
		t.Errorf("got %+v", msg)
	}
	if msg.ID == "" {
		t.Error("message ID should be set")
	}

	// Verify fanout was published
	if len(published) == 0 {
		t.Fatal("expected fanout publish")
	}
}

func TestHandler_ProcessMessage_NotSubscribed(t *testing.T) {
	store := NewMemoryStore() // no subscriptions
	h := &Handler{store: store, siteID: "site-a", publish: func(string, []byte) error { return nil }}

	req := model.SendMessageRequest{RoomID: "r1", Content: "hello", RequestID: "req-1"}
	data, _ := json.Marshal(req)

	_, err := h.processMessage(context.Background(), "u1", "r1", "site-a", data)
	if err == nil {
		t.Fatal("expected error for unsubscribed user")
	}
}

type publishedMsg struct {
	subj string
	data []byte
}

// Satisfy the compiler: time is used in store_test.go but we need it here too
var _ = time.Now
