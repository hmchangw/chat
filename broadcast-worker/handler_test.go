package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

// --- In-memory RoomLookup stub ---

type stubRoomLookup struct {
	rooms map[string]*model.Room
	subs  map[string][]model.Subscription
}

func (s *stubRoomLookup) GetRoom(ctx context.Context, roomID string) (*model.Room, error) {
	room, ok := s.rooms[roomID]
	if !ok {
		return nil, fmt.Errorf("room not found: %s", roomID)
	}
	return room, nil
}

func (s *stubRoomLookup) ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error) {
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

func (m *mockPublisher) Publish(subject string, data []byte) error {
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

func TestHandleMessage_GroupRoom(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	lookup := &stubRoomLookup{
		rooms: map[string]*model.Room{
			"room-1": {
				ID: "room-1", Name: "general", Type: model.RoomTypeGroup,
				CreatedBy: "alice", SiteID: "site-a", UserCount: 5,
				CreatedAt: now, UpdatedAt: now,
			},
		},
	}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	evt := model.MessageEvent{
		RoomID: "room-1",
		SiteID: "site-a",
		Message: model.Message{
			ID:        "m1",
			RoomID:    "room-1",
			UserID:    "alice",
			Content:   "hello group",
			CreatedAt: now,
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

	// Expect exactly 2 publishes:
	// 1. RoomMetadataUpdateEvent to chat.room.room-1.event.metadata.update
	// 2. MessageEvent to chat.room.room-1.stream.msg
	if len(records) != 2 {
		t.Fatalf("expected 2 publishes, got %d", len(records))
	}

	// --- Verify metadata update publish ---
	metaRec := records[0]
	wantMetaSubj := "chat.room.room-1.event.metadata.update"
	if metaRec.subject != wantMetaSubj {
		t.Errorf("metadata subject = %q, want %q", metaRec.subject, wantMetaSubj)
	}

	var metaEvt model.RoomMetadataUpdateEvent
	if err := json.Unmarshal(metaRec.data, &metaEvt); err != nil {
		t.Fatalf("unmarshal metadata event: %v", err)
	}
	if metaEvt.RoomID != "room-1" {
		t.Errorf("metadata roomID = %q, want %q", metaEvt.RoomID, "room-1")
	}
	if metaEvt.Name != "general" {
		t.Errorf("metadata name = %q, want %q", metaEvt.Name, "general")
	}
	if metaEvt.UserCount != 5 {
		t.Errorf("metadata userCount = %d, want %d", metaEvt.UserCount, 5)
	}

	// --- Verify message stream publish ---
	msgRec := records[1]
	wantMsgSubj := "chat.room.room-1.stream.msg"
	if msgRec.subject != wantMsgSubj {
		t.Errorf("message subject = %q, want %q", msgRec.subject, wantMsgSubj)
	}

	var msgEvt model.MessageEvent
	if err := json.Unmarshal(msgRec.data, &msgEvt); err != nil {
		t.Fatalf("unmarshal message event: %v", err)
	}
	if msgEvt.Message.ID != "m1" {
		t.Errorf("message ID = %q, want %q", msgEvt.Message.ID, "m1")
	}
	if msgEvt.Message.Content != "hello group" {
		t.Errorf("message content = %q, want %q", msgEvt.Message.Content, "hello group")
	}
}

func TestHandleMessage_DMRoom(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	lookup := &stubRoomLookup{
		rooms: map[string]*model.Room{
			"dm-1": {
				ID: "dm-1", Name: "", Type: model.RoomTypeDM,
				CreatedBy: "alice", SiteID: "site-a", UserCount: 2,
				CreatedAt: now, UpdatedAt: now,
			},
		},
		subs: map[string][]model.Subscription{
			"dm-1": {
				{ID: "s1", User: model.SubscriptionUser{ID: "alice"}, RoomID: "dm-1", SiteID: "site-a"},
				{ID: "s2", User: model.SubscriptionUser{ID: "bob"}, RoomID: "dm-1", SiteID: "site-a"},
			},
		},
	}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	evt := model.MessageEvent{
		RoomID: "dm-1",
		SiteID: "site-a",
		Message: model.Message{
			ID:        "m2",
			RoomID:    "dm-1",
			UserID:    "alice",
			Content:   "hey bob",
			CreatedAt: now,
		},
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleMessage(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	records := pub.getRecords()

	// Expect exactly 3 publishes:
	// 1. RoomMetadataUpdateEvent to chat.room.dm-1.event.metadata.update
	// 2. MessageEvent to chat.user.alice.stream.msg
	// 3. MessageEvent to chat.user.bob.stream.msg
	if len(records) != 3 {
		t.Fatalf("expected 3 publishes, got %d", len(records))
	}

	// --- Verify metadata update is first ---
	wantMetaSubj := "chat.room.dm-1.event.metadata.update"
	if records[0].subject != wantMetaSubj {
		t.Errorf("first publish subject = %q, want %q", records[0].subject, wantMetaSubj)
	}

	// --- Verify per-user DM publishes ---
	subjects := map[string]bool{}
	for _, r := range records[1:] {
		subjects[r.subject] = true

		var msgEvt model.MessageEvent
		if err := json.Unmarshal(r.data, &msgEvt); err != nil {
			t.Fatalf("unmarshal message event: %v", err)
		}
		if msgEvt.Message.ID != "m2" {
			t.Errorf("message ID = %q, want %q", msgEvt.Message.ID, "m2")
		}
	}

	if !subjects["chat.user.alice.stream.msg"] {
		t.Error("missing DM publish for alice")
	}
	if !subjects["chat.user.bob.stream.msg"] {
		t.Error("missing DM publish for bob")
	}
}

func TestHandleMessage_DMRoom_NoSubscriptions(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	lookup := &stubRoomLookup{
		rooms: map[string]*model.Room{
			"dm-empty": {
				ID: "dm-empty", Name: "", Type: model.RoomTypeDM,
				CreatedBy: "alice", SiteID: "site-a", UserCount: 0,
				CreatedAt: now, UpdatedAt: now,
			},
		},
		subs: map[string][]model.Subscription{},
	}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	evt := model.MessageEvent{
		RoomID: "dm-empty",
		SiteID: "site-a",
		Message: model.Message{
			ID: "m3", RoomID: "dm-empty", UserID: "alice", Content: "anyone?",
			CreatedAt: now,
		},
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleMessage(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	records := pub.getRecords()

	// Should still publish metadata update, but no user stream publishes
	if len(records) != 1 {
		t.Fatalf("expected 1 publish (metadata only), got %d", len(records))
	}
	wantMetaSubj := "chat.room.dm-empty.event.metadata.update"
	if records[0].subject != wantMetaSubj {
		t.Errorf("subject = %q, want %q", records[0].subject, wantMetaSubj)
	}
}

func TestHandleMessage_RoomNotFound(t *testing.T) {
	lookup := &stubRoomLookup{
		rooms: map[string]*model.Room{},
	}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	evt := model.MessageEvent{
		RoomID: "nonexistent",
		SiteID: "site-a",
		Message: model.Message{
			ID: "m4", RoomID: "nonexistent", UserID: "alice", Content: "hello",
		},
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleMessage(context.Background(), evtData)
	if err == nil {
		t.Error("expected error for nonexistent room, got nil")
	}

	records := pub.getRecords()
	if len(records) != 0 {
		t.Errorf("expected 0 publishes on room lookup failure, got %d", len(records))
	}
}

func TestHandleMessage_InvalidJSON(t *testing.T) {
	lookup := &stubRoomLookup{}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	err := h.HandleMessage(context.Background(), []byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestHandleMessage_MetadataFields(t *testing.T) {
	now := time.Date(2026, 3, 19, 14, 30, 0, 0, time.UTC)
	msgTime := time.Date(2026, 3, 19, 15, 0, 0, 0, time.UTC)
	lookup := &stubRoomLookup{
		rooms: map[string]*model.Room{
			"room-meta": {
				ID: "room-meta", Name: "dev-team", Type: model.RoomTypeGroup,
				CreatedBy: "alice", SiteID: "site-a", UserCount: 10,
				CreatedAt: now, UpdatedAt: now,
			},
		},
	}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	evt := model.MessageEvent{
		RoomID: "room-meta",
		SiteID: "site-a",
		Message: model.Message{
			ID: "m5", RoomID: "room-meta", UserID: "bob",
			Content: "checking metadata", CreatedAt: msgTime,
		},
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleMessage(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	records := pub.getRecords()
	if len(records) < 1 {
		t.Fatal("expected at least 1 publish")
	}

	var metaEvt model.RoomMetadataUpdateEvent
	if err := json.Unmarshal(records[0].data, &metaEvt); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}

	if metaEvt.RoomID != "room-meta" {
		t.Errorf("RoomID = %q, want %q", metaEvt.RoomID, "room-meta")
	}
	if metaEvt.Name != "dev-team" {
		t.Errorf("Name = %q, want %q", metaEvt.Name, "dev-team")
	}
	if metaEvt.UserCount != 10 {
		t.Errorf("UserCount = %d, want %d", metaEvt.UserCount, 10)
	}
	if metaEvt.LastMessageAt != msgTime {
		t.Errorf("LastMessageAt = %v, want %v", metaEvt.LastMessageAt, msgTime)
	}
}
