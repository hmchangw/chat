package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// --- In-memory InboxStore stub ---

type roleUpdate struct {
	account string
	roomID  string
	roles   []model.Role
}

type subRead struct {
	roomID     string
	account    string
	lastSeenAt time.Time
	alert      bool
}

type stubInboxStore struct {
	mu                sync.Mutex
	subscriptions     []model.Subscription
	bulkSubscriptions []*model.Subscription
	rooms             []model.Room
	roleUpdates       []roleUpdate
	users             []model.User
	subReads          []subRead
	threadSubs        []model.ThreadSubscription
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

func (s *stubInboxStore) DeleteSubscriptionsByAccounts(_ context.Context, roomID string, accounts []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[string]struct{}, len(accounts))
	for _, a := range accounts {
		want[a] = struct{}{}
	}
	filtered := s.subscriptions[:0]
	for i := range s.subscriptions {
		if s.subscriptions[i].RoomID == roomID {
			if _, match := want[s.subscriptions[i].User.Account]; match {
				continue
			}
		}
		filtered = append(filtered, s.subscriptions[i])
	}
	s.subscriptions = filtered
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

func (s *stubInboxStore) UpdateSubscriptionRoles(_ context.Context, account, roomID string, roles []model.Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roleUpdates = append(s.roleUpdates, roleUpdate{account: account, roomID: roomID, roles: roles})
	return nil
}

func (s *stubInboxStore) getRoleUpdates() []roleUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]roleUpdate, len(s.roleUpdates))
	copy(cp, s.roleUpdates)
	return cp
}

func (s *stubInboxStore) FindUsersByAccounts(_ context.Context, accounts []string) ([]model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	accountSet := make(map[string]struct{}, len(accounts))
	for _, a := range accounts {
		accountSet[a] = struct{}{}
	}
	var result []model.User
	for i := range s.users {
		if _, ok := accountSet[s.users[i].Account]; ok {
			result = append(result, s.users[i])
		}
	}
	return result, nil
}

func (s *stubInboxStore) BulkCreateSubscriptions(_ context.Context, subs []*model.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bulkSubscriptions = append(s.bulkSubscriptions, subs...)
	for _, sub := range subs {
		s.subscriptions = append(s.subscriptions, *sub)
	}
	return nil
}

func (s *stubInboxStore) UpdateSubscriptionRead(_ context.Context, roomID, account string, lastSeenAt time.Time, alert bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subReads = append(s.subReads, subRead{roomID, account, lastSeenAt, alert})
	for i := range s.subscriptions {
		if s.subscriptions[i].RoomID == roomID && s.subscriptions[i].User.Account == account {
			// Order-safe: skip if stored lastSeenAt is not strictly earlier.
			if s.subscriptions[i].LastSeenAt != nil && !s.subscriptions[i].LastSeenAt.Before(lastSeenAt) {
				return nil
			}
			ls := lastSeenAt
			s.subscriptions[i].LastSeenAt = &ls
			s.subscriptions[i].Alert = alert
			return nil
		}
	}
	return nil // missing-subscription → no-op
}

func (s *stubInboxStore) getSubReads() []subRead {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]subRead, len(s.subReads))
	copy(cp, s.subReads)
	return cp
}

func (s *stubInboxStore) UpsertThreadSubscription(_ context.Context, sub *model.ThreadSubscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.threadSubs {
		if s.threadSubs[i].ThreadRoomID == sub.ThreadRoomID && s.threadSubs[i].UserID == sub.UserID {
			// Monotonic hasMention merge — never clear true→false.
			if sub.HasMention {
				s.threadSubs[i].HasMention = true
			}
			s.threadSubs[i].UpdatedAt = sub.UpdatedAt
			return nil
		}
	}
	s.threadSubs = append(s.threadSubs, *sub)
	return nil
}

func (s *stubInboxStore) getThreadSubs() []model.ThreadSubscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]model.ThreadSubscription, len(s.threadSubs))
	copy(cp, s.threadSubs)
	return cp
}

// --- Tests ---

func TestHandleEvent_MemberAdded(t *testing.T) {
	store := &stubInboxStore{
		users: []model.User{
			{ID: "uid-bob", Account: "bob", SiteID: "site-a"},
		},
	}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	hssMillis := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	change := model.MemberAddEvent{
		Type:               "member_added",
		RoomID:             "room-1",
		Accounts:           []string{"bob"},
		SiteID:             "site-b",
		JoinedAt:           time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC).UnixMilli(),
		HistorySharedSince: &hssMillis,
		Timestamp:          time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC).UnixMilli(),
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
	if sub.User.ID != "uid-bob" {
		t.Errorf("subscription User.ID = %q, want %q", sub.User.ID, "uid-bob")
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
	if len(sub.Roles) != 1 || sub.Roles[0] != model.RoleMember {
		t.Errorf("subscription Roles = %v, want [%q]", sub.Roles, model.RoleMember)
	}
	if sub.RoomType != model.RoomTypeChannel {
		t.Errorf("subscription RoomType = %q, want %q", sub.RoomType, model.RoomTypeChannel)
	}
	if sub.ID == "" {
		t.Error("subscription ID should be non-empty (generated UUID)")
	}

}

func TestHandleEvent_MemberAdded_SetsTimestamps(t *testing.T) {
	store := &stubInboxStore{
		users: []model.User{
			{ID: "uid-carol", Account: "carol", SiteID: "site-a"},
		},
	}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	joinedAt := time.Date(2026, 4, 10, 8, 0, 0, 0, time.UTC)
	historyShared := time.Date(2026, 4, 10, 8, 0, 0, 0, time.UTC)
	hssMillis := historyShared.UnixMilli()

	change := model.MemberAddEvent{
		Type:               "member_added",
		RoomID:             "room-2",
		Accounts:           []string{"carol"},
		SiteID:             "site-b",
		JoinedAt:           joinedAt.UnixMilli(),
		HistorySharedSince: &hssMillis,
		Timestamp:          joinedAt.UnixMilli(),
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
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	subs := store.getSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}

	sub := subs[0]
	if !sub.JoinedAt.Equal(joinedAt) {
		t.Errorf("JoinedAt = %v, want %v", sub.JoinedAt, joinedAt)
	}
	if sub.HistorySharedSince == nil {
		t.Fatal("HistorySharedSince should not be nil")
	}
	if !sub.HistorySharedSince.Equal(historyShared) {
		t.Errorf("HistorySharedSince = %v, want %v", *sub.HistorySharedSince, historyShared)
	}
}

func TestHandleEvent_RoomSync(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	room := model.Room{
		ID:        "room-1",
		Name:      "general",
		Type:      model.RoomTypeChannel,
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
}

func TestHandleEvent_RoomSync_Upsert(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	// Insert initial room
	room1 := model.Room{
		ID: "room-1", Name: "old-name", SiteID: "site-b",
		Type: model.RoomTypeChannel, CreatedBy: "alice", UserCount: 2,
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
		Type: model.RoomTypeChannel, CreatedBy: "alice", UserCount: 10,
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
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

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
}

func TestHandleEvent_InvalidJSON(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	err := h.HandleEvent(context.Background(), []byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestHandleEvent_MemberAdded_InvalidPayload(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

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
	store := &stubInboxStore{
		users: []model.User{
			{ID: "uid-bob", Account: "account-bob", SiteID: "site-a"},
		},
	}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	hssMillis := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	change := model.MemberAddEvent{
		Type:               "member_added",
		RoomID:             "room-1",
		Accounts:           []string{"account-bob"},
		SiteID:             "site-b",
		JoinedAt:           time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC).UnixMilli(),
		HistorySharedSince: &hssMillis,
		Timestamp:          time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC).UnixMilli(),
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

	// No SubscriptionUpdateEvent is published here — room-worker already
	// publishes via the NATS supercluster to the user's home site.
}

func TestHandleEvent_MemberAdded_EventSourcedFields(t *testing.T) {
	store := &stubInboxStore{
		users: []model.User{
			{ID: "uid-alice", Account: "alice", SiteID: "site-a"},
			{ID: "uid-bob", Account: "bob", SiteID: "site-a"},
		},
	}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	joinedAt := time.Date(2026, 4, 5, 10, 30, 0, 0, time.UTC)
	historyShared := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	hssMillis := historyShared.UnixMilli()

	change := model.MemberAddEvent{
		Type:               "member_added",
		RoomID:             "room-99",
		Accounts:           []string{"alice", "bob"},
		SiteID:             "site-b",
		JoinedAt:           joinedAt.UnixMilli(),
		HistorySharedSince: &hssMillis,
		Timestamp:          joinedAt.UnixMilli(),
	}
	changeData, _ := json.Marshal(change)

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    changeData,
	}
	evtData, _ := json.Marshal(evt)

	if err := h.HandleEvent(context.Background(), evtData); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	subs := store.getSubscriptions()
	if len(subs) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(subs))
	}

	for i, want := range []struct {
		userID  string
		account string
	}{
		{"uid-alice", "alice"},
		{"uid-bob", "bob"},
	} {
		sub := subs[i]
		if sub.User.ID != want.userID {
			t.Errorf("sub[%d] User.ID = %q, want %q", i, sub.User.ID, want.userID)
		}
		if sub.User.Account != want.account {
			t.Errorf("sub[%d] User.Account = %q, want %q", i, sub.User.Account, want.account)
		}
		if sub.SiteID != "site-b" {
			t.Errorf("sub[%d] SiteID = %q, want %q", i, sub.SiteID, "site-b")
		}
		if sub.RoomID != "room-99" {
			t.Errorf("sub[%d] RoomID = %q, want %q", i, sub.RoomID, "room-99")
		}
		if !sub.JoinedAt.Equal(joinedAt) {
			t.Errorf("sub[%d] JoinedAt = %v, want %v", i, sub.JoinedAt, joinedAt)
		}
		if sub.HistorySharedSince == nil {
			t.Fatalf("sub[%d] HistorySharedSince should not be nil", i)
		}
		if !sub.HistorySharedSince.Equal(historyShared) {
			t.Errorf("sub[%d] HistorySharedSince = %v, want %v", i, *sub.HistorySharedSince, historyShared)
		}
		if len(sub.Roles) != 1 || sub.Roles[0] != model.RoleMember {
			t.Errorf("sub[%d] Roles = %v, want [%q]", i, sub.Roles, model.RoleMember)
		}
	}

	// No SubscriptionUpdateEvent is published here — room-worker already
	// publishes via the NATS supercluster to the user's home site.
}

func TestHandleEvent_MemberAdded_HistoryAll(t *testing.T) {
	store := &stubInboxStore{
		users: []model.User{
			{ID: "uid-dave", Account: "dave", SiteID: "site-a"},
		},
	}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	change := model.MemberAddEvent{
		Type:     "member_added",
		RoomID:   "room-1",
		Accounts: []string{"dave"},
		SiteID:   "site-b",
		JoinedAt: time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC).UnixMilli(),
		// HistorySharedSince nil → "all history"
		Timestamp: time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC).UnixMilli(),
	}
	changeData, _ := json.Marshal(change)

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    changeData,
	}
	evtData, _ := json.Marshal(evt)

	if err := h.HandleEvent(context.Background(), evtData); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	subs := store.getSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
	if subs[0].HistorySharedSince != nil {
		t.Errorf("HistorySharedSince = %v, want nil (history all)", subs[0].HistorySharedSince)
	}
}

func TestHandleEvent_RoomSync_InvalidPayload(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

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

func TestHandleEvent_RoleUpdated(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)
	subEvt := model.SubscriptionUpdateEvent{
		UserID: "u2",
		Subscription: model.Subscription{
			ID: "s1", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
			RoomID: "room-1", SiteID: "site-a", Roles: []model.Role{model.RoleOwner},
		},
		Action: "role_updated", Timestamp: 1735689600000,
	}
	subEvtData, _ := json.Marshal(subEvt)
	evt := model.OutboxEvent{
		Type: "role_updated", SiteID: "site-a", DestSiteID: "site-b",
		Payload: subEvtData, Timestamp: 1735689600000,
	}
	evtData, _ := json.Marshal(evt)
	err := h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	updates := store.getRoleUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 role update, got %d", len(updates))
	}
	if updates[0].account != "bob" || updates[0].roomID != "room-1" {
		t.Errorf("role update = %+v, want bob/room-1", updates[0])
	}
	if len(updates[0].roles) != 1 || updates[0].roles[0] != model.RoleOwner {
		t.Errorf("role update roles = %v, want [owner]", updates[0].roles)
	}
	// No SubscriptionUpdateEvent publish — room-worker already handles that via NATS supercluster
}

func TestHandleEvent_RoleUpdated_InvalidPayload(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)
	evt := model.OutboxEvent{
		Type: "role_updated", SiteID: "site-a", DestSiteID: "site-b",
		Payload: []byte("not valid json"),
	}
	evtData, _ := json.Marshal(evt)
	err := h.HandleEvent(context.Background(), evtData)
	if err == nil {
		t.Error("expected error for invalid role_updated payload")
	}
	if len(store.getRoleUpdates()) != 0 {
		t.Error("no role update should have been applied")
	}
}

func TestHandleEvent_MemberRemoved(t *testing.T) {
	store := &stubInboxStore{}
	keyStore, client := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStore, client)

	store.mu.Lock()
	store.subscriptions = append(store.subscriptions, model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
		RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember},
	})
	store.mu.Unlock()

	memberEvt := model.MemberRemoveEvent{
		Type: "member-removed", RoomID: "r1", Accounts: []string{"bob"}, SiteID: "site-a",
	}
	payload, _ := json.Marshal(memberEvt)
	evt := model.OutboxEvent{
		Type: "member_removed", SiteID: "site-a", DestSiteID: "site-b",
		Payload: payload, Timestamp: time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), data)
	require.NoError(t, err)

	subs := store.getSubscriptions()
	assert.Empty(t, subs)
}

func TestHandleEvent_MemberRemoved_InvalidPayload(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	evt := model.OutboxEvent{
		Type: "member_removed", SiteID: "site-a", DestSiteID: "site-b",
		Payload: []byte(`{invalid`), Timestamp: time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), data)
	require.Error(t, err)
}

func TestHandleEvent_MemberRemoved_MultipleAccounts(t *testing.T) {
	store := &stubInboxStore{}
	keyStore, client := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStore, client)

	// Pre-populate subscriptions for both accounts
	store.mu.Lock()
	store.subscriptions = append(store.subscriptions,
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r2", Roles: []model.Role{model.RoleMember}},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "dave"}, RoomID: "r2", Roles: []model.Role{model.RoleMember}},
	)
	store.mu.Unlock()

	// Accounts have already been filtered at the room's site — inbox-worker
	// just deletes whatever the event lists.
	memberEvt := model.MemberRemoveEvent{
		Type: "member-removed", RoomID: "r2", Accounts: []string{"alice", "dave"},
		SiteID: "site-a", OrgID: "finance-org", Timestamp: time.Now().UnixMilli(),
	}
	payload, _ := json.Marshal(memberEvt)
	evt := model.OutboxEvent{
		Type: "member_removed", SiteID: "site-a", DestSiteID: "site-b",
		Payload: payload, Timestamp: time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), data)
	require.NoError(t, err)

	// Both subscriptions should be deleted
	subs := store.getSubscriptions()
	assert.Empty(t, subs)
}

func TestHandleEvent_MemberRemoved_EmptyAccountsNoOp(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	memberEvt := model.MemberRemoveEvent{RoomID: "r1", Accounts: []string{}}
	payload, _ := json.Marshal(memberEvt)
	outboxPayload, _ := json.Marshal(model.OutboxEvent{
		Type: "member_removed", SiteID: "a", DestSiteID: "b", Payload: payload,
	})
	require.NoError(t, h.HandleEvent(context.Background(), outboxPayload))
}

type errorDeleteStore struct {
	*stubInboxStore
}

func (s *errorDeleteStore) DeleteSubscriptionsByAccounts(_ context.Context, _ string, _ []string) error {
	return fmt.Errorf("boom")
}

func TestHandleEvent_MemberRemoved_DeleteError(t *testing.T) {
	store := &errorDeleteStore{stubInboxStore: &stubInboxStore{}}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	memberEvt := model.MemberRemoveEvent{RoomID: "r1", Accounts: []string{"alice"}}
	payload, _ := json.Marshal(memberEvt)
	outboxPayload, _ := json.Marshal(model.OutboxEvent{
		Type: "member_removed", SiteID: "a", DestSiteID: "b", Payload: payload,
	})
	err := h.HandleEvent(context.Background(), outboxPayload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete subscriptions")
}

func TestHandler_HandleEvent_SubscriptionRead_HappyPath(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	inner := model.SubscriptionReadEvent{
		Account:    "alice",
		RoomID:     "r1",
		LastSeenAt: time.Now().UTC().UnixMilli(),
		Alert:      true,
		Timestamp:  time.Now().UTC().UnixMilli(),
	}
	innerData, err := json.Marshal(inner)
	require.NoError(t, err)
	evt := model.OutboxEvent{
		Type:       model.OutboxSubscriptionRead,
		SiteID:     "site-a",
		DestSiteID: "site-b",
		Payload:    innerData,
		Timestamp:  inner.Timestamp,
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)

	require.NoError(t, h.HandleEvent(context.Background(), data))

	calls := store.getSubReads()
	require.Len(t, calls, 1)
	assert.Equal(t, "r1", calls[0].roomID)
	assert.Equal(t, "alice", calls[0].account)
	assert.True(t, calls[0].alert)
	assert.Equal(t, time.UnixMilli(inner.LastSeenAt).UTC(), calls[0].lastSeenAt)
}

func TestHandler_HandleEvent_SubscriptionRead_MalformedPayload(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)
	evt := model.OutboxEvent{Type: model.OutboxSubscriptionRead, Payload: []byte("not-json")}
	data, _ := json.Marshal(evt)
	require.Error(t, h.HandleEvent(context.Background(), data))
}

func TestHandleEvent_ThreadSubscriptionUpserted_Insert(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	// SiteID is the room's home site (site-a), preserved across federation.
	sub := model.ThreadSubscription{
		ID:              "sub-1",
		ParentMessageID: "pm-1",
		RoomID:          "r1",
		ThreadRoomID:    "tr-1",
		UserID:          "u-bob",
		UserAccount:     "bob",
		SiteID:          "site-a",
		HasMention:      false,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	subData, err := json.Marshal(sub)
	require.NoError(t, err)

	evt := model.OutboxEvent{
		Type:       "thread_subscription_upserted",
		SiteID:     "site-a",
		DestSiteID: "site-b",
		Payload:    subData,
		Timestamp:  now.UnixMilli(),
	}
	evtData, _ := json.Marshal(evt)

	require.NoError(t, h.HandleEvent(context.Background(), evtData))

	got := store.getThreadSubs()
	require.Len(t, got, 1)
	assert.Equal(t, sub, got[0])
}

func TestHandleEvent_ThreadSubscriptionUpserted_MonotonicHasMention(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	// SiteID is the room's home site (site-a), preserved across federation.
	mentionSub := model.ThreadSubscription{
		ID: "sub-1", ParentMessageID: "pm-1", RoomID: "r1", ThreadRoomID: "tr-1",
		UserID: "u-bob", UserAccount: "bob", SiteID: "site-a",
		HasMention: true, CreatedAt: now, UpdatedAt: now,
	}
	mentionData, _ := json.Marshal(mentionSub)
	mentionEvt, _ := json.Marshal(model.OutboxEvent{
		Type: "thread_subscription_upserted", SiteID: "site-a", DestSiteID: "site-b",
		Payload: mentionData, Timestamp: now.UnixMilli(),
	})
	require.NoError(t, h.HandleEvent(context.Background(), mentionEvt))

	// Second event for same (threadRoomID, userID) with HasMention=false must NOT clear it.
	plainSub := mentionSub
	plainSub.HasMention = false
	plainSub.UpdatedAt = now.Add(time.Minute)
	plainData, _ := json.Marshal(plainSub)
	plainEvt, _ := json.Marshal(model.OutboxEvent{
		Type: "thread_subscription_upserted", SiteID: "site-a", DestSiteID: "site-b",
		Payload: plainData, Timestamp: plainSub.UpdatedAt.UnixMilli(),
	})
	require.NoError(t, h.HandleEvent(context.Background(), plainEvt))

	got := store.getThreadSubs()
	require.Len(t, got, 1)
	assert.True(t, got[0].HasMention, "hasMention must remain true after a non-mention event")
}

func TestHandleEvent_ThreadSubscriptionUpserted_InvalidPayload(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	evt := model.OutboxEvent{
		Type: "thread_subscription_upserted", SiteID: "site-a", DestSiteID: "site-b",
		Payload: []byte("not json"),
	}
	evtData, _ := json.Marshal(evt)

	require.Error(t, h.HandleEvent(context.Background(), evtData))
	assert.Empty(t, store.getThreadSubs())
}

func TestHandleEvent_ThreadSubscriptionUpserted_StoreError(t *testing.T) {
	store := &errorThreadSubStore{stubInboxStore: &stubInboxStore{}}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	sub := model.ThreadSubscription{
		ID: "sub-1", ThreadRoomID: "tr-1", UserID: "u-bob", SiteID: "site-a",
		CreatedAt: now, UpdatedAt: now,
	}
	subData, _ := json.Marshal(sub)
	evtData, _ := json.Marshal(model.OutboxEvent{
		Type: "thread_subscription_upserted", SiteID: "site-a", DestSiteID: "site-b",
		Payload: subData, Timestamp: now.UnixMilli(),
	})

	err := h.HandleEvent(context.Background(), evtData)
	require.Error(t, err)
}

type errorThreadSubStore struct {
	*stubInboxStore
}

func (s *errorThreadSubStore) UpsertThreadSubscription(_ context.Context, _ *model.ThreadSubscription) error {
	return fmt.Errorf("boom")
}

func TestRolesForType(t *testing.T) {
	assert.Equal(t, []model.Role{model.RoleMember}, rolesForType(model.RoomTypeChannel))
	assert.Nil(t, rolesForType(model.RoomTypeDM))
	assert.Nil(t, rolesForType(model.RoomTypeBotDM))
}

func TestSubscriptionName(t *testing.T) {
	d := model.RoomCreatedOutbox{
		RoomType:         model.RoomTypeChannel,
		RoomName:         "deal team",
		RequesterAccount: "alice",
	}
	assert.Equal(t, "deal team", subscriptionName(&d, &model.User{Account: "bob"}))

	d.RoomType = model.RoomTypeDM
	assert.Equal(t, "alice", subscriptionName(&d, &model.User{Account: "bob"}))

	d.RoomType = model.RoomTypeBotDM
	assert.Equal(t, "alice", subscriptionName(&d, &model.User{Account: "weather.bot"}))
}

func TestSubscriptionIsSubscribed(t *testing.T) {
	d := model.RoomCreatedOutbox{RoomType: model.RoomTypeChannel}
	assert.False(t, subscriptionIsSubscribed(&d, &model.User{Account: "bob"}))

	d.RoomType = model.RoomTypeDM
	assert.False(t, subscriptionIsSubscribed(&d, &model.User{Account: "bob"}))

	d.RoomType = model.RoomTypeBotDM
	assert.False(t, subscriptionIsSubscribed(&d, &model.User{Account: "weather.bot"}))
	assert.True(t, subscriptionIsSubscribed(&d, &model.User{Account: "alice"}))
	// p_ webhook bots: same as .bot — bot side gets IsSubscribed=false.
	assert.False(t, subscriptionIsSubscribed(&d, &model.User{Account: "p_webhook"}))
}

func TestHandleRoomCreatedRequiresRequestID(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)
	payload, _ := json.Marshal(model.RoomCreatedOutbox{
		RoomID: "r1", RoomType: model.RoomTypeChannel,
		Accounts: []string{"bob"},
	})
	err := h.handleRoomCreated(context.Background(), &model.OutboxEvent{Payload: payload})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing X-Request-ID")
}

func TestHandleRoomCreatedEmptyAccountsAcksWithWarn(t *testing.T) {
	store := &stubInboxStore{}
	keyStoreT, clientT := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStoreT, clientT)
	const reqID = "0193abcd-0193-7abc-89ab-0193abcd0193"
	ctx := natsutil.WithRequestID(context.Background(), reqID)

	payload, _ := json.Marshal(model.RoomCreatedOutbox{
		RoomID: "r1", RoomType: model.RoomTypeChannel, Accounts: []string{},
	})
	require.NoError(t, h.handleRoomCreated(ctx, &model.OutboxEvent{Payload: payload}))
}

func TestHandleRoomCreatedDMBuildsRemoteSub(t *testing.T) {
	store := &stubInboxStore{
		users: []model.User{
			{ID: "u_bob", Account: "bob", SiteID: "site-B"},
		},
	}
	keyStore, client := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStore, client)
	const reqID = "0193abcd-0193-7abc-89ab-0193abcd0193"
	ctx := natsutil.WithRequestID(context.Background(), reqID)

	payload, _ := json.Marshal(model.RoomCreatedOutbox{
		RoomID:           "u_aliceu_bob",
		RoomType:         model.RoomTypeDM,
		RoomName:         "",
		HomeSiteID:       "site-A",
		Accounts:         []string{"bob"},
		RequesterAccount: "alice",
		Timestamp:        1740000000000,
	})
	require.NoError(t, h.handleRoomCreated(ctx, &model.OutboxEvent{Payload: payload}))

	subs := store.bulkSubscriptions
	require.Len(t, subs, 1)
	assert.True(t, idgen.IsValidUUIDv7(subs[0].ID))
	assert.Equal(t, "u_aliceu_bob", subs[0].RoomID)
	assert.Equal(t, "site-A", subs[0].SiteID)
	assert.Equal(t, "alice", subs[0].Name)
	assert.Nil(t, subs[0].Roles)
	assert.False(t, subs[0].IsSubscribed)
	assert.Equal(t, model.RoomTypeDM, subs[0].RoomType)
}

func TestHandleRoomCreatedChannelBulkInsert(t *testing.T) {
	store := &stubInboxStore{
		users: []model.User{
			{ID: "u_bob", Account: "bob", SiteID: "site-B"},
			{ID: "u_ian", Account: "ian", SiteID: "site-B"},
		},
	}
	keyStore, client := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStore, client)
	const reqID = "0193abcd-0193-7abc-89ab-0193abcd0193"
	ctx := natsutil.WithRequestID(context.Background(), reqID)

	payload, _ := json.Marshal(model.RoomCreatedOutbox{
		RoomID:           "r1",
		RoomType:         model.RoomTypeChannel,
		RoomName:         "deal team",
		HomeSiteID:       "site-A",
		Accounts:         []string{"bob", "ian"},
		RequesterAccount: "alice",
		Timestamp:        1,
	})
	require.NoError(t, h.handleRoomCreated(ctx, &model.OutboxEvent{Payload: payload}))

	subs := store.bulkSubscriptions
	require.Len(t, subs, 2)
	for _, s := range subs {
		assert.Equal(t, "deal team", s.Name)
		assert.Equal(t, []model.Role{model.RoleMember}, s.Roles)
		assert.Equal(t, model.RoomTypeChannel, s.RoomType)
		assert.Equal(t, "site-A", s.SiteID)
	}
}

func TestHandleMemberAddedSetsNameAndRoomType(t *testing.T) {
	store := &stubInboxStore{
		users: []model.User{
			{ID: "u_bob", Account: "bob", SiteID: "site-B"},
		},
	}
	keyStore, client := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStore, client)

	change := model.MemberAddEvent{
		Type:      "member_added",
		RoomID:    "r1",
		RoomName:  "deal team",
		Accounts:  []string{"bob"},
		SiteID:    "site-A",
		JoinedAt:  1740000000000,
		Timestamp: 1740000000000,
	}
	changeData, err := json.Marshal(change)
	require.NoError(t, err)

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-A",
		DestSiteID: "site-B",
		Payload:    changeData,
	}
	evtData, err := json.Marshal(evt)
	require.NoError(t, err)

	require.NoError(t, h.HandleEvent(context.Background(), evtData))

	subs := store.getSubscriptions()
	require.Len(t, subs, 1)
	assert.Equal(t, "deal team", subs[0].Name)
	assert.Equal(t, model.RoomTypeChannel, subs[0].RoomType)
}

func TestHandleRoomCreatedBotDMBuildsRemoteBotSub(t *testing.T) {
	// Cross-site botDM: human (alice) is the requester on site-A; bot
	// (weather.bot) lives on site-B. The outbox event lands at site-B's
	// inbox-worker, which must materialize the bot's sub with:
	//   Name        = human's account ("alice")
	//   IsSubscribed = false
	//   Roles       = nil (no member role for botDM)
	//   SiteID      = home site (site-A)
	store := &stubInboxStore{
		users: []model.User{
			{ID: "u_weather", Account: "weather.bot", SiteID: "site-B"},
		},
	}
	keyStore, client := newKeyDepsForTest()
	h := NewHandler(store, "site-test", keyStore, client)
	const reqID = "0193abcd-0193-7abc-89ab-0193abcd0193"
	ctx := natsutil.WithRequestID(context.Background(), reqID)

	payload, _ := json.Marshal(model.RoomCreatedOutbox{
		RoomID:           "u_aliceu_weather",
		RoomType:         model.RoomTypeBotDM,
		RoomName:         "",
		HomeSiteID:       "site-A",
		Accounts:         []string{"weather.bot"},
		RequesterAccount: "alice",
		Timestamp:        1740000000000,
	})
	require.NoError(t, h.handleRoomCreated(ctx, &model.OutboxEvent{Payload: payload}))

	subs := store.bulkSubscriptions
	require.Len(t, subs, 1, "exactly one remote sub for the bot")
	assert.True(t, idgen.IsValidUUIDv7(subs[0].ID))
	assert.Equal(t, "u_aliceu_weather", subs[0].RoomID)
	assert.Equal(t, "site-A", subs[0].SiteID, "bot's sub.siteID is the room's home site")
	assert.Equal(t, "alice", subs[0].Name, "bot's sub.Name is the human account")
	assert.Nil(t, subs[0].Roles)
	assert.False(t, subs[0].IsSubscribed)
	assert.Equal(t, model.RoomTypeBotDM, subs[0].RoomType)
	assert.Equal(t, "u_weather", subs[0].User.ID)
	assert.Equal(t, "weather.bot", subs[0].User.Account)
}

// TestHandleMemberAdded_ReplicatesLocalKeyOnMiss verifies that on a local Valkey miss,
// handleMemberAdded fetches from origin via RPC and stores the key locally.
// No user-side fan-out happens here — origin room-worker handles that via supercluster.
func TestHandleMemberAdded_ReplicatesLocalKeyOnMiss(t *testing.T) {
	store := &stubInboxStore{}
	store.users = []model.User{
		{ID: "u-c", Account: "charlie", SiteID: "site-b"},
	}
	keyStore := newStubKeyStore()
	client := &stubInterSiteClient{
		getResp: &model.RoomKeyEvent{
			RoomID: "r1", Version: 2,
			PublicKey:  bytes.Repeat([]byte{0x04}, 65),
			PrivateKey: bytes.Repeat([]byte{0x07}, 32),
		},
	}

	h := NewHandler(store, "site-b", keyStore, client)

	memberAdded := model.MemberAddEvent{
		RoomID: "r1", Accounts: []string{"charlie"}, SiteID: "site-origin",
		RoomName: "general", JoinedAt: time.Now().UnixMilli(),
	}
	pData, _ := json.Marshal(memberAdded)
	envelope := &model.OutboxEvent{Type: "member_added", SiteID: "site-origin", DestSiteID: "site-b", Payload: pData}

	require.NoError(t, h.handleMemberAdded(context.Background(), envelope))

	// Key must be replicated to local Valkey.
	pair, err := keyStore.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.NotNil(t, pair, "key must be stored locally after RPC fetch")
	assert.Equal(t, client.getResp.PublicKey, pair.KeyPair.PublicKey)
}

// TestHandleMemberAdded_NoRPCOnLocalHit verifies that when the key is already
// in local Valkey, no RPC is made. No user-side fan-out either.
func TestHandleMemberAdded_NoRPCOnLocalHit(t *testing.T) {
	store := &stubInboxStore{}
	store.users = []model.User{
		{ID: "u-c", Account: "charlie", SiteID: "site-b"},
	}
	keyStore := newStubKeyStore()
	// Pre-seed local key.
	_, _ = keyStore.Set(context.Background(), "r1", roomkeystore.RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0x04}, 65),
		PrivateKey: bytes.Repeat([]byte{0x09}, 32),
	})
	client := &stubInterSiteClient{}

	h := NewHandler(store, "site-b", keyStore, client)

	memberAdded := model.MemberAddEvent{
		RoomID: "r1", Accounts: []string{"charlie"}, SiteID: "site-origin",
		RoomName: "general", JoinedAt: time.Now().UnixMilli(),
	}
	pData, _ := json.Marshal(memberAdded)
	envelope := &model.OutboxEvent{Type: "member_added", SiteID: "site-origin", DestSiteID: "site-b", Payload: pData}

	require.NoError(t, h.handleMemberAdded(context.Background(), envelope))
	// RPC should NOT have been called (local hit).
	assert.Empty(t, client.calls)
}

// TestHandleMemberRemoved_RotatesLocalKey verifies that on member_removed the local
// Valkey key is rotated. No user-side fan-out — origin room-worker handles that.
func TestHandleMemberRemoved_RotatesLocalKey(t *testing.T) {
	store := &stubInboxStore{}
	store.subscriptions = []model.Subscription{
		{User: model.SubscriptionUser{Account: "alice"}, RoomID: "r1", SiteID: "site-b"},
	}
	keyStore := newStubKeyStore()
	// Pre-seed previous key so Rotate succeeds (not falls through to Set).
	_, _ = keyStore.Set(context.Background(), "r1", roomkeystore.RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0x04}, 65),
		PrivateKey: bytes.Repeat([]byte{0x01}, 32),
	})
	client := &stubInterSiteClient{
		getResp: &model.RoomKeyEvent{
			RoomID: "r1", Version: 5,
			PublicKey:  bytes.Repeat([]byte{0x04}, 65),
			PrivateKey: bytes.Repeat([]byte{0x08}, 32),
		},
	}

	h := NewHandler(store, "site-b", keyStore, client)

	rmv := model.MemberRemoveEvent{RoomID: "r1", Accounts: []string{"bob"}, SiteID: "site-origin", NewKeyVersion: 5}
	pData, _ := json.Marshal(rmv)
	envelope := &model.OutboxEvent{Type: "member_removed", SiteID: "site-origin", DestSiteID: "site-b", Payload: pData}
	require.NoError(t, h.handleMemberRemoved(context.Background(), envelope))

	// Valkey key rotated to the new pair.
	pair, err := keyStore.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.NotNil(t, pair)
	assert.Equal(t, client.getResp.PrivateKey, pair.KeyPair.PrivateKey, "key must be rotated to new pair")
}

func TestHandleMemberRemoved_NaksOnRPCFailure(t *testing.T) {
	store := &stubInboxStore{}
	keyStore := newStubKeyStore()
	// Pre-seed a key so Rotate (not Set) is attempted.
	_, _ = keyStore.Set(context.Background(), "r1", roomkeystore.RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0x04}, 65),
		PrivateKey: bytes.Repeat([]byte{0x01}, 32),
	})
	client := &stubInterSiteClient{getErr: fmt.Errorf("rpc timeout")}

	h := NewHandler(store, "site-b", keyStore, client)

	rmv := model.MemberRemoveEvent{RoomID: "r1", Accounts: []string{"bob"}, SiteID: "site-origin"}
	pData, _ := json.Marshal(rmv)
	envelope := &model.OutboxEvent{Type: "member_removed", SiteID: "site-origin", DestSiteID: "site-b", Payload: pData}

	err := h.handleMemberRemoved(context.Background(), envelope)
	require.Error(t, err, "expected error to be propagated for NAK")
	assert.Contains(t, err.Error(), "rotate local key")
	assert.Contains(t, err.Error(), "rpc timeout")
}

// TestHandleRoomCreated_ReplicatesLocalKey verifies that on room_created the local
// Valkey key is populated via RPC. No user-side fan-out — origin room-worker handles that.
func TestHandleRoomCreated_ReplicatesLocalKey(t *testing.T) {
	store := &stubInboxStore{
		users: []model.User{
			{ID: "u-bob", Account: "bob", SiteID: "site-b"},
		},
	}
	keyStore := newStubKeyStore()
	client := &stubInterSiteClient{
		getResp: &model.RoomKeyEvent{
			RoomID:     "r1",
			Version:    1,
			PublicKey:  bytes.Repeat([]byte{0x04}, 65),
			PrivateKey: bytes.Repeat([]byte{0x06}, 32),
		},
	}

	h := NewHandler(store, "site-b", keyStore, client)

	outbox := model.RoomCreatedOutbox{
		RoomID:           "r1",
		HomeSiteID:       "site-origin",
		Accounts:         []string{"bob"},
		RoomType:         model.RoomTypeChannel,
		RequesterAccount: "alice",
		Timestamp:        time.Now().UnixMilli(),
	}
	pData, _ := json.Marshal(outbox)
	envelope := &model.OutboxEvent{
		Type:       model.OutboxTypeRoomCreated,
		SiteID:     "site-origin",
		DestSiteID: "site-b",
		Payload:    pData,
	}

	ctx := natsutil.WithRequestID(context.Background(), "0193abcd-0193-7abc-89ab-0193abcd0193")
	require.NoError(t, h.handleRoomCreated(ctx, envelope))

	// Verify Set was called with the fetched keypair.
	pair, err := keyStore.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.NotNil(t, pair)
	assert.Equal(t, client.getResp.PublicKey, pair.KeyPair.PublicKey)
	assert.Equal(t, client.getResp.PrivateKey, pair.KeyPair.PrivateKey)
}

func TestFetchAndStoreKey_AdoptsOriginVersionWhenLocalLags(t *testing.T) {
	// Pre-seed local store with a version 0 key.
	keyStore := newStubKeyStore()
	_, _ = keyStore.Set(context.Background(), "r1", roomkeystore.RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0x01}, 65),
		PrivateKey: bytes.Repeat([]byte{0x02}, 32),
	})

	client := &stubInterSiteClient{
		getResp: &model.RoomKeyEvent{
			RoomID: "r1", Version: 5,
			PublicKey:  bytes.Repeat([]byte{0x04}, 65),
			PrivateKey: bytes.Repeat([]byte{0x03}, 32),
		},
	}

	h := NewHandler(nil, "site-b", keyStore, client)

	require.NoError(t, h.fetchAndStoreKey(context.Background(), "site-origin", "r1"))

	// Local must mirror origin's version so on-wire message envelopes match what clients hold.
	pair, err := keyStore.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.NotNil(t, pair)
	assert.Equal(t, client.getResp.Version, pair.Version,
		"replicated key must adopt origin's version, not bump local independently")
	assert.Equal(t, client.getResp.PrivateKey, pair.KeyPair.PrivateKey)
}

func TestFetchAndStoreKey_SkipsWhenLocalAtOrAheadOfOrigin(t *testing.T) {
	keyStore := newStubKeyStore()
	require.NoError(t, keyStore.SetWithVersion(context.Background(), "r1", roomkeystore.RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0x09}, 65),
		PrivateKey: bytes.Repeat([]byte{0x0a}, 32),
	}, 5))

	client := &stubInterSiteClient{
		getResp: &model.RoomKeyEvent{
			RoomID: "r1", Version: 5,
			PublicKey:  bytes.Repeat([]byte{0x04}, 65),
			PrivateKey: bytes.Repeat([]byte{0x03}, 32),
		},
	}
	h := NewHandler(nil, "site-b", keyStore, client)

	require.NoError(t, h.fetchAndStoreKey(context.Background(), "site-origin", "r1"))

	pair, err := keyStore.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.NotNil(t, pair)
	// Redelivery must not bump or overwrite when versions match.
	assert.Equal(t, 5, pair.Version)
	assert.Equal(t, []byte{0x09}, pair.KeyPair.PublicKey[:1],
		"local key bytes must not change when versions are equal")
}

// --- replicateLocalKey direct tests ---

// TestReplicateLocalKey_NoRPCOnCacheHit confirms that when the local key
// is already cached, no RPC is made (it's a no-op).
func TestReplicateLocalKey_NoRPCOnCacheHit(t *testing.T) {
	keyStore := newStubKeyStore()
	_, err := keyStore.Set(context.Background(), "r1", roomkeystore.RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0x04}, 65),
		PrivateKey: bytes.Repeat([]byte{0x03}, 32),
	})
	require.NoError(t, err)

	client := &stubInterSiteClient{}

	h := NewHandler(nil, "site-b", keyStore, client)

	require.NoError(t, h.replicateLocalKey(context.Background(), "site-a", "r1"))

	// Key was served from cache — interSiteClient must not have been called.
	client.mu.Lock()
	nCalls := len(client.calls)
	client.mu.Unlock()
	assert.Equal(t, 0, nCalls, "interSiteClient must not be called on a cache hit")
}

// TestReplicateLocalKey_FallsBackToRPCOnMiss confirms that when the
// local cache is empty the function fetches from the origin via RPC and stores
// the key locally. No user-side fan-out.
func TestReplicateLocalKey_FallsBackToRPCOnMiss(t *testing.T) {
	keyStore := newStubKeyStore() // empty cache

	client := &stubInterSiteClient{
		getResp: &model.RoomKeyEvent{
			RoomID:     "r1",
			Version:    3,
			PublicKey:  bytes.Repeat([]byte{0x04}, 65),
			PrivateKey: bytes.Repeat([]byte{0x03}, 32),
		},
	}

	h := NewHandler(nil, "site-b", keyStore, client)

	require.NoError(t, h.replicateLocalKey(context.Background(), "site-a", "r1"))

	// RPC was made to fetch from origin.
	client.mu.Lock()
	nCalls := len(client.calls)
	client.mu.Unlock()
	assert.Equal(t, 1, nCalls, "expected one RPC call to interSiteClient")

	// Key should now be stored locally.
	pair, err := keyStore.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.NotNil(t, pair, "key must be persisted locally after RPC fetch")
}

// TestReplicateLocalKey_ReturnsErrorOnKeyStoreFailure verifies that a
// Valkey Get failure is propagated as an error rather than silently falling
// through to the RPC path.
func TestReplicateLocalKey_ReturnsErrorOnKeyStoreFailure(t *testing.T) {
	valkeyErr := errors.New("valkey: connection refused")
	keyStore := &stubKeyStore{
		store:  map[string]*roomkeystore.VersionedKeyPair{},
		getErr: valkeyErr,
	}
	client := &stubInterSiteClient{}

	h := NewHandler(nil, "site-b", keyStore, client)

	err := h.replicateLocalKey(context.Background(), "site-a", "r1")
	require.Error(t, err, "expected error when keyStore.Get fails")
	require.ErrorIs(t, err, valkeyErr, "error must wrap the underlying Valkey error")

	// RPC path must NOT be reached when Get returns an error.
	client.mu.Lock()
	nCalls := len(client.calls)
	client.mu.Unlock()
	assert.Equal(t, 0, nCalls, "interSiteClient must not be called on Valkey Get failure")
}

// --- fetchAndStoreKey direct tests ---

// TestFetchAndStoreKey_HappyPath verifies that on an empty local store the
// fetched key is written with origin's exact version (no Set-at-version-0 quirk).
func TestFetchAndStoreKey_HappyPath(t *testing.T) {
	keyStore := newStubKeyStore() // empty
	client := &stubInterSiteClient{
		getResp: &model.RoomKeyEvent{
			RoomID:     "r1",
			Version:    1,
			PublicKey:  bytes.Repeat([]byte{0x04}, 65),
			PrivateKey: bytes.Repeat([]byte{0x05}, 32),
		},
	}
	h := NewHandler(nil, "site-b", keyStore, client)

	require.NoError(t, h.fetchAndStoreKey(context.Background(), "site-origin", "r1"))

	pair, err := keyStore.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.NotNil(t, pair)
	assert.Equal(t, client.getResp.Version, pair.Version, "local must adopt origin's version exactly")
	assert.Equal(t, client.getResp.PublicKey, pair.KeyPair.PublicKey)
	assert.Equal(t, client.getResp.PrivateKey, pair.KeyPair.PrivateKey)
}

// TestFetchAndStoreKey_AdvancesLocalWhenOriginNewer verifies version catch-up:
// when origin is at version=3 but local is at version=0, fetchAndStoreKey writes
// the fetched key at version=3 (not local+1).
func TestFetchAndStoreKey_AdvancesLocalWhenOriginNewer(t *testing.T) {
	keyStore := newStubKeyStore()
	_, _ = keyStore.Set(context.Background(), "r1", roomkeystore.RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0x01}, 65),
		PrivateKey: bytes.Repeat([]byte{0x02}, 32),
	})
	client := &stubInterSiteClient{
		getResp: &model.RoomKeyEvent{
			RoomID:     "r1",
			Version:    3,
			PublicKey:  bytes.Repeat([]byte{0x04}, 65),
			PrivateKey: bytes.Repeat([]byte{0x07}, 32),
		},
	}
	h := NewHandler(nil, "site-b", keyStore, client)

	require.NoError(t, h.fetchAndStoreKey(context.Background(), "site-origin", "r1"))

	pair, err := keyStore.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.NotNil(t, pair)
	assert.Equal(t, client.getResp.Version, pair.Version, "local must adopt origin's version exactly")
	assert.Equal(t, client.getResp.PrivateKey, pair.KeyPair.PrivateKey)
}

// TestFetchAndStoreKey_RPCFailurePropagates verifies that an RPC error is returned.
func TestFetchAndStoreKey_RPCFailurePropagates(t *testing.T) {
	keyStore := newStubKeyStore()
	rpcErr := fmt.Errorf("origin unreachable")
	client := &stubInterSiteClient{getErr: rpcErr}
	h := NewHandler(nil, "site-b", keyStore, client)

	err := h.fetchAndStoreKey(context.Background(), "site-origin", "r1")
	require.Error(t, err)
	assert.ErrorIs(t, err, rpcErr)
}

// TestHandleEvent_MemberRemoved_RotatesLocalKey verifies that a
// member_removed OutboxEvent passes through the dispatch table and reaches the
// key-rotation path when key dependencies are fully wired. No fan-out.
func TestHandleEvent_MemberRemoved_RotatesLocalKey(t *testing.T) {
	store := &stubInboxStore{}

	store.mu.Lock()
	store.subscriptions = append(store.subscriptions, model.Subscription{
		ID: "s-alice", User: model.SubscriptionUser{ID: "u-alice", Account: "alice"},
		RoomID: "r1", SiteID: "site-b",
	})
	store.mu.Unlock()

	keyStore := newStubKeyStore()
	// Pre-seed the origin key in the interSiteClient so GetRoomKey succeeds.
	client := &stubInterSiteClient{
		getResp: &model.RoomKeyEvent{
			RoomID:     "r1",
			Version:    5,
			PublicKey:  bytes.Repeat([]byte{0x04}, 65),
			PrivateKey: bytes.Repeat([]byte{0x03}, 32),
		},
	}
	h := NewHandler(store, "site-b", keyStore, client)

	memberEvt := model.MemberRemoveEvent{
		Type:          "member-removed",
		RoomID:        "r1",
		Accounts:      []string{"charlie"},
		SiteID:        "site-a",
		NewKeyVersion: 5,
	}
	payload, _ := json.Marshal(memberEvt)
	outboxEvt := model.OutboxEvent{
		Type:       "member_removed",
		SiteID:     "site-a",
		DestSiteID: "site-b",
		Payload:    payload,
		Timestamp:  time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(outboxEvt)

	err := h.HandleEvent(context.Background(), data)
	require.NoError(t, err)

	// Valkey has the rotated key — proves dispatch reached rotation path.
	pair, err := keyStore.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.NotNil(t, pair, "local key must be stored after rotation")
	assert.Equal(t, client.getResp.PrivateKey, pair.KeyPair.PrivateKey)
}
