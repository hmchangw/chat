package main

import (
	"context"
	"encoding/json"
	"errors"
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

	createSubErr  error
	deleteSubErr  error
	updateRoleErr error
	upsertRoomErr error
}

func (s *stubInboxStore) CreateSubscription(ctx context.Context, sub *model.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createSubErr != nil {
		return s.createSubErr
	}
	s.subscriptions = append(s.subscriptions, *sub)
	return nil
}

func (s *stubInboxStore) DeleteSubscription(ctx context.Context, account string, roomID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteSubErr != nil {
		return s.deleteSubErr
	}
	filtered := s.subscriptions[:0]
	for i := range s.subscriptions {
		if s.subscriptions[i].User.Account != account || s.subscriptions[i].RoomID != roomID {
			filtered = append(filtered, s.subscriptions[i])
		}
	}
	s.subscriptions = filtered
	return nil
}

func (s *stubInboxStore) UpdateSubscriptionRole(ctx context.Context, account string, roomID string, role model.Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateRoleErr != nil {
		return s.updateRoleErr
	}
	for i := range s.subscriptions {
		if s.subscriptions[i].User.Account == account && s.subscriptions[i].RoomID == roomID {
			s.subscriptions[i].Roles = []model.Role{role}
			return nil
		}
	}
	return nil
}

func (s *stubInboxStore) UpsertRoom(ctx context.Context, room *model.Room) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upsertRoomErr != nil {
		return s.upsertRoomErr
	}
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

func TestHandleEvent_MemberAdded(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	change := model.MemberChangeEvent{
		Type:               "member-added",
		RoomID:             "room-1",
		Accounts:           []string{"bob"},
		SiteID:             "site-b",
		UserIDs:            []string{"u-bob"},
		JoinedAt:           time.Now().UnixMilli(),
		HistorySharedSince: time.Now().UnixMilli(),
	}
	changeData, err := json.Marshal(change)
	if err != nil {
		t.Fatalf("marshal change: %v", err)
	}

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    changeData,
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
	if sub.User.ID != "u-bob" {
		t.Errorf("subscription User.ID = %q, want %q", sub.User.ID, "u-bob")
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
	if len(sub.Roles) == 0 || sub.Roles[0] != model.RoleMember {
		t.Errorf("subscription Role = %v, want member", sub.Roles)
	}
	if sub.ID == "" {
		t.Error("subscription ID should be non-empty (generated UUID)")
	}

	// Verify SubscriptionUpdateEvent was published
	records := pub.getRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(records))
	}

	wantSubject := "chat.user.bob.event.subscription.update"
	if records[0].subject != wantSubject {
		t.Errorf("publish subject = %q, want %q", records[0].subject, wantSubject)
	}

	var updateEvt model.SubscriptionUpdateEvent
	if err := json.Unmarshal(records[0].data, &updateEvt); err != nil {
		t.Fatalf("unmarshal update event: %v", err)
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

	eventTime := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	change := model.MemberChangeEvent{
		Type:               "member-added",
		RoomID:             "room-2",
		Accounts:           []string{"carol"},
		SiteID:             "site-b",
		JoinedAt:           eventTime.UnixMilli(),
		HistorySharedSince: eventTime.UnixMilli(),
	}
	inviteData, _ := json.Marshal(change)

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    inviteData,
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	subs := store.getSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}

	sub := subs[0]
	if !sub.JoinedAt.Equal(eventTime) {
		t.Errorf("JoinedAt = %v, want %v", sub.JoinedAt, eventTime)
	}
	if sub.HistorySharedSince == nil || !sub.HistorySharedSince.Equal(eventTime) {
		t.Errorf("HistorySharedSince = %v, want %v", sub.HistorySharedSince, eventTime)
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

func TestHandleEvent_MemberAdded_MultipleAccounts(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	change := model.MemberChangeEvent{
		Type:               "member-added",
		RoomID:             "room-1",
		Accounts:           []string{"alice", "bob"},
		SiteID:             "site-b",
		UserIDs:            []string{"u-alice", "u-bob"},
		JoinedAt:           time.Now().UnixMilli(),
		HistorySharedSince: time.Now().UnixMilli(),
	}
	changeData, err := json.Marshal(change)
	if err != nil {
		t.Fatalf("marshal change: %v", err)
	}

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    changeData,
	}
	evtData, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	err = h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	// Verify subscriptions created for both accounts
	subs := store.getSubscriptions()
	if len(subs) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(subs))
	}

	// Verify subjects are routed by account
	records := pub.getRecords()
	if len(records) != 2 {
		t.Fatalf("expected 2 publishes, got %d", len(records))
	}
	wantSubjects := map[string]bool{
		"chat.user.alice.event.subscription.update": true,
		"chat.user.bob.event.subscription.update":   true,
	}
	for _, r := range records {
		if !wantSubjects[r.subject] {
			t.Errorf("unexpected publish subject = %q", r.subject)
		}
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

// --- member_removed tests ---

func TestHandleEvent_MemberRemoved(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	// Pre-populate subscriptions that will be removed
	store.subscriptions = []model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "room-1", SiteID: "site-b"},
		{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "room-1", SiteID: "site-b"},
		{ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "carol"}, RoomID: "room-2", SiteID: "site-b"},
	}

	change := model.MemberChangeEvent{
		Type:     "member-removed",
		RoomID:   "room-1",
		Accounts: []string{"alice", "bob"},
		SiteID:   "site-b",
	}
	changeData, err := json.Marshal(change)
	if err != nil {
		t.Fatalf("marshal change: %v", err)
	}

	evt := model.OutboxEvent{
		Type:       "member_removed",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    changeData,
	}
	evtData, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	err = h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	// Verify alice and bob subscriptions in room-1 were deleted, carol in room-2 remains
	subs := store.getSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 remaining subscription, got %d", len(subs))
	}
	if subs[0].User.Account != "carol" {
		t.Errorf("remaining subscription account = %q, want %q", subs[0].User.Account, "carol")
	}

	// Verify SubscriptionUpdateEvent published for each removed account
	records := pub.getRecords()
	if len(records) != 2 {
		t.Fatalf("expected 2 publishes, got %d", len(records))
	}

	for i, account := range []string{"alice", "bob"} {
		wantSubject := "chat.user." + account + ".event.subscription.update"
		if records[i].subject != wantSubject {
			t.Errorf("publish[%d] subject = %q, want %q", i, records[i].subject, wantSubject)
		}

		var updateEvt model.SubscriptionUpdateEvent
		if err := json.Unmarshal(records[i].data, &updateEvt); err != nil {
			t.Fatalf("unmarshal update event[%d]: %v", i, err)
		}
		if updateEvt.Action != "removed" {
			t.Errorf("update event[%d] Action = %q, want %q", i, updateEvt.Action, "removed")
		}
		if updateEvt.Subscription.RoomID != "room-1" {
			t.Errorf("update event[%d] RoomID = %q, want %q", i, updateEvt.Subscription.RoomID, "room-1")
		}
		if updateEvt.Subscription.User.Account != account {
			t.Errorf("update event[%d] Account = %q, want %q", i, updateEvt.Subscription.User.Account, account)
		}
		if updateEvt.Timestamp <= 0 {
			t.Errorf("expected Timestamp > 0 on update event[%d]", i)
		}
	}
}

func TestHandleEvent_MemberRemoved_InvalidPayload(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	evt := model.OutboxEvent{
		Type:       "member_removed",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    []byte("not valid json"),
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err == nil {
		t.Error("expected error for invalid member_removed payload, got nil")
	}
}

func TestHandleEvent_MemberRemoved_StoreError(t *testing.T) {
	store := &stubInboxStore{deleteSubErr: errors.New("db failure")}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	change := model.MemberChangeEvent{
		Type:     "member-removed",
		RoomID:   "room-1",
		Accounts: []string{"alice"},
		SiteID:   "site-b",
	}
	changeData, _ := json.Marshal(change)

	evt := model.OutboxEvent{
		Type:       "member_removed",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    changeData,
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err == nil {
		t.Error("expected error when store.DeleteSubscription fails, got nil")
	}

	// No publishes should have been made
	if len(pub.getRecords()) != 0 {
		t.Error("expected no publishes when store fails")
	}
}

func TestHandleEvent_MemberRemoved_EmptyAccounts(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	change := model.MemberChangeEvent{
		Type:     "member-removed",
		RoomID:   "room-1",
		Accounts: []string{},
		SiteID:   "site-b",
	}
	changeData, _ := json.Marshal(change)

	evt := model.OutboxEvent{
		Type:       "member_removed",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    changeData,
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	// No publishes for empty accounts
	if len(pub.getRecords()) != 0 {
		t.Error("expected no publishes for empty accounts list")
	}
}

// --- Store error tests for existing handlers ---

func TestHandleEvent_MemberAdded_StoreError(t *testing.T) {
	store := &stubInboxStore{createSubErr: errors.New("db failure")}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	change := model.MemberChangeEvent{
		Type:     "member-added",
		RoomID:   "room-1",
		Accounts: []string{"bob"},
		SiteID:   "site-b",
	}
	changeData, _ := json.Marshal(change)

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    changeData,
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err == nil {
		t.Error("expected error when store.CreateSubscription fails, got nil")
	}

	// No publishes should have been made
	if len(pub.getRecords()) != 0 {
		t.Error("expected no publishes when store fails")
	}
}

func TestHandleEvent_RoomSync_StoreError(t *testing.T) {
	store := &stubInboxStore{upsertRoomErr: errors.New("db failure")}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	room := model.Room{
		ID:     "room-1",
		Name:   "general",
		SiteID: "site-b",
	}
	roomData, _ := json.Marshal(room)

	evt := model.OutboxEvent{
		Type:       "room_sync",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    roomData,
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err == nil {
		t.Error("expected error when store.UpsertRoom fails, got nil")
	}
}

func TestHandleEvent_RoleUpdated(t *testing.T) {
	store := &stubInboxStore{}
	store.subscriptions = []model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{Account: "bob"}, RoomID: "room-1", Roles: []model.Role{model.RoleMember}},
	}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	subEvt := model.SubscriptionUpdateEvent{
		Subscription: model.Subscription{
			RoomID: "room-1",
			User:   model.SubscriptionUser{Account: "bob"},
			Roles:  []model.Role{model.RoleOwner},
		},
		Action:    "role_updated",
		Timestamp: 1735689600000,
	}
	subEvtData, _ := json.Marshal(subEvt)

	evt := model.OutboxEvent{
		Type:       "role_updated",
		SiteID:     "site-eu",
		DestSiteID: "site-us",
		Payload:    subEvtData,
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	subs := store.getSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
	if subs[0].Roles[0] != model.RoleOwner {
		t.Errorf("role = %v, want owner", subs[0].Roles)
	}

	records := pub.getRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(records))
	}
	if records[0].subject != "chat.user.bob.event.subscription.update" {
		t.Errorf("subject = %q, want chat.user.bob.event.subscription.update", records[0].subject)
	}
}
