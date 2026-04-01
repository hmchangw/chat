package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
)

// --- In-memory MemberLookup stub ---

type stubMemberLookup struct {
	subs map[string][]model.Subscription
}

func (s *stubMemberLookup) ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error) {
	return s.subs[roomID], nil
}

// --- NATS publish recorder ---

type publishRecord struct {
	subject string
	data    []byte
}

type mockPublisher struct {
	mu      sync.Mutex
	records []publishRecord
}

func (m *mockPublisher) Publish(_ context.Context, subject string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, publishRecord{subject: subject, data: data})
	return nil
}

func (m *mockPublisher) getRecords() []publishRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]publishRecord, len(m.records))
	copy(cp, m.records)
	return cp
}

// --- Tests ---

func TestHandleMessage_FanOutSkipsSender(t *testing.T) {
	lookup := &stubMemberLookup{
		subs: map[string][]model.Subscription{
			"room-1": {
				{ID: "s1", User: model.SubscriptionUser{ID: "alice", Username: "username-alice"}, RoomID: "room-1"},
				{ID: "s2", User: model.SubscriptionUser{ID: "bob", Username: "username-bob"}, RoomID: "room-1"},
				{ID: "s3", User: model.SubscriptionUser{ID: "carol", Username: "username-carol"}, RoomID: "room-1"},
			},
		},
	}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	evt := model.MessageEvent{
		SiteID: "site-a",
		Message: model.Message{
			ID:      "m1",
			RoomID:  "room-1",
			UserID:  "alice", // sender
			Content: "hello",
		},
	}
	evtData, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	err = h.HandleMessage(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	records := pub.getRecords()

	// Should notify bob and carol, but NOT alice (the sender)
	if len(records) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(records))
	}

	subjects := map[string]bool{}
	for _, r := range records {
		subjects[r.subject] = true

		var notif model.NotificationEvent
		if err := json.Unmarshal(r.data, &notif); err != nil {
			t.Fatalf("unmarshal notification: %v", err)
		}
		if notif.Type != "new_message" {
			t.Errorf("notification type = %q, want %q", notif.Type, "new_message")
		}
		if notif.RoomID != "room-1" {
			t.Errorf("notification roomID = %q, want %q", notif.RoomID, "room-1")
		}
		if notif.Message.ID != "m1" {
			t.Errorf("notification message ID = %q, want %q", notif.Message.ID, "m1")
		}
	}

	if !subjects["chat.user.username-bob.notification"] {
		t.Error("missing notification for bob")
	}
	if !subjects["chat.user.username-carol.notification"] {
		t.Error("missing notification for carol")
	}
	if subjects["chat.user.username-alice.notification"] {
		t.Error("sender alice should NOT receive notification")
	}
}

func TestHandleMessage_NoMembers(t *testing.T) {
	lookup := &stubMemberLookup{
		subs: map[string][]model.Subscription{},
	}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	evt := model.MessageEvent{
		SiteID: "site-a",
		Message: model.Message{
			ID:      "m1",
			RoomID:  "empty-room",
			UserID:  "alice",
			Content: "hello?",
		},
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleMessage(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	records := pub.getRecords()
	if len(records) != 0 {
		t.Errorf("expected 0 notifications for empty room, got %d", len(records))
	}
}

func TestHandleMessage_SoleMember(t *testing.T) {
	lookup := &stubMemberLookup{
		subs: map[string][]model.Subscription{
			"room-solo": {
				{ID: "s1", User: model.SubscriptionUser{ID: "alice", Username: "username-alice"}, RoomID: "room-solo"},
			},
		},
	}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	evt := model.MessageEvent{
		SiteID: "site-a",
		Message: model.Message{
			ID:      "m1",
			RoomID:  "room-solo",
			UserID:  "alice",
			Content: "talking to myself",
		},
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleMessage(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	records := pub.getRecords()
	if len(records) != 0 {
		t.Errorf("expected 0 notifications when sender is sole member, got %d", len(records))
	}
}

func TestHandleMessage_InvalidJSON(t *testing.T) {
	lookup := &stubMemberLookup{}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	err := h.HandleMessage(context.Background(), []byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}
