package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

// --- In-memory InboxStore stub ---

type stubInboxStore struct {
	mu            sync.Mutex
	subscriptions []model.Subscription
	rooms         []model.Room
}

func (s *stubInboxStore) CreateSubscription(ctx context.Context, sub *model.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions = append(s.subscriptions, *sub)
	return nil
}

func (s *stubInboxStore) UpsertRoom(ctx context.Context, room *model.Room) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rooms {
		if s.rooms[i].ID == room.ID {
			s.rooms[i] = *room
			return nil
		}
	}
	s.rooms = append(s.rooms, *room)
	return nil
}

func (s *stubInboxStore) getSubscriptions() []model.Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]model.Subscription, len(s.subscriptions))
	copy(cp, s.subscriptions)
	return cp
}

func (s *stubInboxStore) getRooms() []model.Room {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]model.Room, len(s.rooms))
	copy(cp, s.rooms)
	return cp
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

func TestHandleEvent_MemberAdded(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	invite := model.InviteMemberRequest{
		InviterID:      "alice",
		InviteeID:      "bob",
		InviteeAccount: "bob",
		RoomID:         "room-1",
		SiteID:         "site-b",
	}
	inviteData, err := json.Marshal(invite)
	if err != nil {
		t.Fatalf("marshal invite: %v", err)
	}

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    inviteData,
	}
	evtData, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	err = h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	// Verify subscription was created
	subs := store.getSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
	sub := subs[0]
	if sub.User.ID != "bob" {
		t.Errorf("subscription User.ID = %q, want %q", sub.User.ID, "bob")
	}
	if sub.User.Account != "bob" {
		t.Errorf("subscription User.Account = %q, want %q", sub.User.Account, "bob")
	}
	if sub.RoomID != "room-1" {
		t.Errorf("subscription RoomID = %q, want %q", sub.RoomID, "room-1")
	}
	if sub.SiteID != "site-b" {
		t.Errorf("subscription SiteID = %q, want %q", sub.SiteID, "site-b")
	}
	if sub.Role != model.RoleMember {
		t.Errorf("subscription Role = %q, want %q", sub.Role, model.RoleMember)
	}
	if sub.ID == "" {
		t.Error("subscription ID should be non-empty (generated UUID)")
	}

	// Verify SubscriptionUpdateEvent was published
	records := pub.getRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(records))
	}

	wantSubject := "chat.user.bob.event.subscription.update" // routed by InviteeAccount "bob"
	if records[0].subject != wantSubject {
		t.Errorf("publish subject = %q, want %q", records[0].subject, wantSubject)
	}

	var updateEvt model.SubscriptionUpdateEvent
	if err := json.Unmarshal(records[0].data, &updateEvt); err != nil {
		t.Fatalf("unmarshal update event: %v", err)
	}
	if updateEvt.UserID != "bob" {
		t.Errorf("update event UserID = %q, want %q", updateEvt.UserID, "bob")
	}
	if updateEvt.Action != "added" {
		t.Errorf("update event Action = %q, want %q", updateEvt.Action, "added")
	}
	if updateEvt.Subscription.RoomID != "room-1" {
		t.Errorf("update event subscription RoomID = %q, want %q", updateEvt.Subscription.RoomID, "room-1")
	}
	if updateEvt.Timestamp <= 0 {
		t.Error("expected Timestamp > 0 on SubscriptionUpdateEvent")
	}
}

func TestHandleEvent_MemberAdded_SetsTimestamps(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	invite := model.InviteMemberRequest{
		InviterID:      "alice",
		InviteeID:      "carol",
		InviteeAccount: "carol",
		RoomID:         "room-2",
		SiteID:         "site-b",
	}
	inviteData, _ := json.Marshal(invite)

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    inviteData,
	}
	evtData, _ := json.Marshal(evt)

	before := time.Now()
	err := h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	after := time.Now()

	subs := store.getSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}

	sub := subs[0]
	if sub.JoinedAt.Before(before) || sub.JoinedAt.After(after) {
		t.Errorf("JoinedAt = %v, want between %v and %v", sub.JoinedAt, before, after)
	}
	if sub.HistorySharedSince == nil || sub.HistorySharedSince.Before(before) || sub.HistorySharedSince.After(after) {
		t.Errorf("HistorySharedSince = %v, want between %v and %v", sub.HistorySharedSince, before, after)
	}
}

func TestHandleEvent_RoomSync(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	room := model.Room{
		ID:        "room-1",
		Name:      "general",
		Type:      model.RoomTypeGroup,
		CreatedBy: "alice",
		SiteID:    "site-b",
		UserCount: 5,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}
	roomData, err := json.Marshal(room)
	if err != nil {
		t.Fatalf("marshal room: %v", err)
	}

	evt := model.OutboxEvent{
		Type:       "room_sync",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    roomData,
	}
	evtData, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	err = h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	// Verify room was upserted
	rooms := store.getRooms()
	if len(rooms) != 1 {
		t.Fatalf("expected 1 room, got %d", len(rooms))
	}
	r := rooms[0]
	if r.ID != "room-1" {
		t.Errorf("room ID = %q, want %q", r.ID, "room-1")
	}
	if r.Name != "general" {
		t.Errorf("room Name = %q, want %q", r.Name, "general")
	}
	if r.SiteID != "site-b" {
		t.Errorf("room SiteID = %q, want %q", r.SiteID, "site-b")
	}
	if r.UserCount != 5 {
		t.Errorf("room UserCount = %d, want %d", r.UserCount, 5)
	}

	// Verify no NATS publish for room_sync (only store update)
	records := pub.getRecords()
	if len(records) != 0 {
		t.Errorf("expected 0 publishes for room_sync, got %d", len(records))
	}
}

func TestHandleEvent_RoomSync_Upsert(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	// Insert initial room
	room1 := model.Room{
		ID: "room-1", Name: "old-name", SiteID: "site-b",
		Type: model.RoomTypeGroup, CreatedBy: "alice", UserCount: 2,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	roomData1, _ := json.Marshal(room1)
	evt1 := model.OutboxEvent{Type: "room_sync", SiteID: "site-b", DestSiteID: "site-a", Payload: roomData1}
	evtData1, _ := json.Marshal(evt1)
	if err := h.HandleEvent(context.Background(), evtData1); err != nil {
		t.Fatalf("first HandleEvent: %v", err)
	}

	// Update same room with new name
	room2 := model.Room{
		ID: "room-1", Name: "new-name", SiteID: "site-b",
		Type: model.RoomTypeGroup, CreatedBy: "alice", UserCount: 10,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}
	roomData2, _ := json.Marshal(room2)
	evt2 := model.OutboxEvent{Type: "room_sync", SiteID: "site-b", DestSiteID: "site-a", Payload: roomData2}
	evtData2, _ := json.Marshal(evt2)
	if err := h.HandleEvent(context.Background(), evtData2); err != nil {
		t.Fatalf("second HandleEvent: %v", err)
	}

	// Verify only 1 room with updated data
	rooms := store.getRooms()
	if len(rooms) != 1 {
		t.Fatalf("expected 1 room after upsert, got %d", len(rooms))
	}
	if rooms[0].Name != "new-name" {
		t.Errorf("room Name = %q, want %q after upsert", rooms[0].Name, "new-name")
	}
	if rooms[0].UserCount != 10 {
		t.Errorf("room UserCount = %d, want %d after upsert", rooms[0].UserCount, 10)
	}
}

func TestHandleEvent_UnknownType(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	evt := model.OutboxEvent{
		Type:       "unknown_type",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    []byte(`{}`),
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	// Unknown types should be logged and skipped, not cause an error
	// (so we don't Nak and endlessly retry unrecognized event types).
	if err != nil {
		t.Errorf("expected nil error for unknown type, got %v", err)
	}

	// No store mutations
	if len(store.getSubscriptions()) != 0 {
		t.Error("unexpected subscriptions created for unknown type")
	}
	if len(store.getRooms()) != 0 {
		t.Error("unexpected rooms created for unknown type")
	}
	// No publishes
	if len(pub.getRecords()) != 0 {
		t.Error("unexpected publishes for unknown type")
	}
}

func TestHandleEvent_InvalidJSON(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	err := h.HandleEvent(context.Background(), []byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestHandleEvent_MemberAdded_InvalidPayload(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    []byte("not valid json"),
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err == nil {
		t.Error("expected error for invalid member_added payload, got nil")
	}

	// No subscription should have been created
	if len(store.getSubscriptions()) != 0 {
		t.Error("subscription should not be created with invalid payload")
	}
}

func TestHandleEvent_MemberAdded_AccountRoutedSubject(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	invite := model.InviteMemberRequest{
		InviterID:      "alice",
		InviteeID:      "uid-bob",
		InviteeAccount: "account-bob",
		RoomID:         "room-1",
		SiteID:         "site-b",
	}
	inviteData, err := json.Marshal(invite)
	if err != nil {
		t.Fatalf("marshal invite: %v", err)
	}

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    inviteData,
	}
	evtData, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	err = h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	// Verify subscription carries both user ID and user account
	subs := store.getSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
	sub := subs[0]
	if sub.User.ID != "uid-bob" {
		t.Errorf("subscription User.ID = %q, want %q", sub.User.ID, "uid-bob")
	}
	if sub.User.Account != "account-bob" {
		t.Errorf("subscription User.Account = %q, want %q", sub.User.Account, "account-bob")
	}

	// Verify subject is routed by user account, not user ID
	records := pub.getRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(records))
	}
	wantSubject := "chat.user.account-bob.event.subscription.update"
	if records[0].subject != wantSubject {
		t.Errorf("publish subject = %q, want %q", records[0].subject, wantSubject)
	}
}

func TestHandleEvent_RoomSync_InvalidPayload(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	evt := model.OutboxEvent{
		Type:       "room_sync",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    []byte("not valid json"),
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err == nil {
		t.Error("expected error for invalid room_sync payload, got nil")
	}

	// No room should have been upserted
	if len(store.getRooms()) != 0 {
		t.Error("room should not be upserted with invalid payload")
	}
}
