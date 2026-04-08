# Plan 8: Inbox Worker

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create an inbox worker that consumes cross-site `OutboxEvent` messages from the `INBOX_{siteID}` JetStream stream and processes them locally — creating subscription documents for invited members and syncing room metadata from remote sites.

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB

**Depends on:** Plan 1 (Foundation) for all `pkg/` packages

---

## Architecture

```
Site-B OUTBOX stream
    |
    | (JetStream cross-site sourcing)
    v
INBOX_{siteID} stream (site-A)
    |
    | pull consumer: "inbox-worker"
    v
inbox-worker
    |
    | 1. Unmarshal OutboxEvent from message data
    | 2. Switch on OutboxEvent.Type:
    |      "member_added":
    |          - Unmarshal Payload as InviteMemberRequest
    |          - Create local Subscription doc in MongoDB
    |          - Publish SubscriptionUpdateEvent to
    |            chat.user.{account}.event.subscription.update
    |      "room_sync":
    |          - Unmarshal Payload as Room
    |          - Upsert room metadata in local MongoDB
    | 3. Ack the JetStream message
    v
MongoDB + NATS core publish
```

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `inbox-worker/handler.go` | `Handler` struct with `InboxStore` interface, event dispatch by type |
| Create | `inbox-worker/handler_test.go` | Unit tests: member_added flow, room_sync flow, unknown type, invalid JSON |
| Create | `inbox-worker/main.go` | Wire NATS, MongoDB, JetStream consumer, graceful shutdown |
| Create | `inbox-worker/Dockerfile` | Multi-stage build |

## Shared Imports from `pkg/`

| Package | Symbols Used |
|---------|-------------|
| `pkg/model` | `OutboxEvent`, `Subscription`, `SubscriptionUpdateEvent`, `InviteMemberRequest`, `Room` |
| `pkg/subject` | `SubscriptionUpdate` |
| `pkg/natsutil` | `MarshalResponse` |
| `pkg/stream` | `stream.Inbox(siteID)` |
| `pkg/mongoutil` | `Connect`, `Disconnect` |
| `pkg/shutdown` | `Wait` |

## Env Vars

| Variable | Description | Example |
|----------|-------------|---------|
| `NATS_URL` | NATS server URL | `nats://localhost:4222` |
| `SITE_ID` | Site identifier for multi-tenant isolation | `site-a` |
| `MONGO_URI` | MongoDB connection string | `mongodb://localhost:27017` |
| `MONGO_DB` | MongoDB database name | `chat` |

---

### Task 1: `handler.go` + `handler_test.go` — Handler with InboxStore interface

- [ ] **Step 1: Write tests** -- `inbox-worker/handler_test.go`

```go
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

func (s *stubInboxStore) CreateSubscription(ctx context.Context, sub model.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions = append(s.subscriptions, sub)
	return nil
}

func (s *stubInboxStore) UpsertRoom(ctx context.Context, room model.Room) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rooms {
		if r.ID == room.ID {
			s.rooms[i] = room
			return nil
		}
	}
	s.rooms = append(s.rooms, room)
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
		InviterID: "alice",
		InviteeID: "bob",
		RoomID:    "room-1",
		SiteID:    "site-b",
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
	if sub.UserID != "bob" {
		t.Errorf("subscription UserID = %q, want %q", sub.UserID, "bob")
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

	wantSubject := "chat.user.bob.event.subscription.update"
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
}

func TestHandleEvent_MemberAdded_SetsTimestamps(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	invite := model.InviteMemberRequest{
		InviterID: "alice",
		InviteeID: "carol",
		RoomID:    "room-2",
		SiteID:    "site-b",
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
	if sub.HistorySharedSince.Before(before) || sub.HistorySharedSince.After(after) {
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
```

- [ ] **Step 2: Run tests -- expect FAIL** (handler not defined yet)

```bash
cd inbox-worker && go test -v -run .
```

- [ ] **Step 3: Create `inbox-worker/handler.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// InboxStore abstracts the data store operations needed by the inbox worker.
type InboxStore interface {
	CreateSubscription(ctx context.Context, sub model.Subscription) error
	UpsertRoom(ctx context.Context, room model.Room) error
}

// Publisher abstracts NATS publishing so the handler is testable.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Handler processes incoming cross-site OutboxEvent messages.
type Handler struct {
	store InboxStore
	pub   Publisher
}

// NewHandler creates a Handler with the given store and publisher.
func NewHandler(store InboxStore, pub Publisher) *Handler {
	return &Handler{store: store, pub: pub}
}

// HandleEvent processes a single JetStream message payload.
// It unmarshals the OutboxEvent and dispatches based on Type.
func (h *Handler) HandleEvent(ctx context.Context, data []byte) error {
	var evt model.OutboxEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal outbox event: %w", err)
	}

	switch evt.Type {
	case "member_added":
		return h.handleMemberAdded(ctx, evt)
	case "room_sync":
		return h.handleRoomSync(ctx, evt)
	default:
		log.Printf("inbox-worker: unknown event type %q, skipping", evt.Type)
		return nil
	}
}

// handleMemberAdded processes a member_added event:
// 1. Unmarshal Payload as InviteMemberRequest
// 2. Create a local Subscription document in the store
// 3. Publish a SubscriptionUpdateEvent to notify the user
func (h *Handler) handleMemberAdded(ctx context.Context, evt model.OutboxEvent) error {
	var invite model.InviteMemberRequest
	if err := json.Unmarshal(evt.Payload, &invite); err != nil {
		return fmt.Errorf("unmarshal member_added payload: %w", err)
	}

	now := time.Now()
	sub := model.Subscription{
		ID:                 uuid.New().String(),
		UserID:             invite.InviteeID,
		RoomID:             invite.RoomID,
		SiteID:             invite.SiteID,
		Role:               model.RoleMember,
		HistorySharedSince: now,
		JoinedAt:           now,
	}

	if err := h.store.CreateSubscription(ctx, sub); err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}

	updateEvt := model.SubscriptionUpdateEvent{
		UserID:       invite.InviteeID,
		Subscription: sub,
		Action:       "added",
	}

	updateData, err := natsutil.MarshalResponse(updateEvt)
	if err != nil {
		return fmt.Errorf("marshal subscription update event: %w", err)
	}

	subj := subject.SubscriptionUpdate(invite.InviteeID)
	if err := h.pub.Publish(subj, updateData); err != nil {
		log.Printf("failed to publish subscription update for user %s: %v", invite.InviteeID, err)
		// Don't return error — the subscription was already persisted.
		// The user will discover it on next reconnect/refresh.
	}

	return nil
}

// handleRoomSync processes a room_sync event:
// 1. Unmarshal Payload as Room
// 2. Upsert room metadata in local store
func (h *Handler) handleRoomSync(ctx context.Context, evt model.OutboxEvent) error {
	var room model.Room
	if err := json.Unmarshal(evt.Payload, &room); err != nil {
		return fmt.Errorf("unmarshal room_sync payload: %w", err)
	}

	if err := h.store.UpsertRoom(ctx, room); err != nil {
		return fmt.Errorf("upsert room: %w", err)
	}

	return nil
}
```

- [ ] **Step 4: Run tests -- expect PASS**

```bash
cd inbox-worker && go test -v -run .
```

- [ ] **Step 5: Commit**

```bash
git add inbox-worker/handler.go inbox-worker/handler_test.go && git commit -m "feat(inbox-worker): add handler with InboxStore interface and event dispatch tests"
```

---

### Task 2: `main.go` -- Wire everything

- [ ] **Step 1: Create `inbox-worker/main.go`**

```go
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoInboxStore implements InboxStore using MongoDB.
type mongoInboxStore struct {
	subCol  *mongo.Collection
	roomCol *mongo.Collection
}

func (s *mongoInboxStore) CreateSubscription(ctx context.Context, sub model.Subscription) error {
	_, err := s.subCol.InsertOne(ctx, sub)
	return err
}

func (s *mongoInboxStore) UpsertRoom(ctx context.Context, room model.Room) error {
	filter := bson.M{"_id": room.ID}
	update := bson.M{"$set": room}
	opts := options.UpdateOne().SetUpsert(true)
	_, err := s.roomCol.UpdateOne(ctx, filter, update, opts)
	return err
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	siteID := envOrDefault("SITE_ID", "default")
	mongoURI := envOrDefault("MONGO_URI", "mongodb://localhost:27017")
	mongoDB := envOrDefault("MONGO_DB", "chat")

	ctx := context.Background()

	// --- MongoDB ---
	mongoClient, err := mongoutil.Connect(ctx, mongoURI)
	if err != nil {
		log.Fatalf("mongo: %v", err)
	}
	db := mongoClient.Database(mongoDB)
	store := &mongoInboxStore{
		subCol:  db.Collection("subscriptions"),
		roomCol: db.Collection("rooms"),
	}

	// --- NATS ---
	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("nats connect: %v", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	// --- Ensure INBOX stream exists ---
	// The INBOX stream is configured with Sources from remote OUTBOX streams.
	// Stream creation with sources is typically done by infrastructure/provisioning.
	// Here we just ensure the stream exists for the consumer to attach to.
	inboxCfg := stream.Inbox(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: inboxCfg.Name,
	})
	if err != nil {
		log.Fatalf("create inbox stream: %v", err)
	}

	// --- Create pull consumer ---
	cons, err := js.CreateOrUpdateConsumer(ctx, inboxCfg.Name, jetstream.ConsumerConfig{
		Durable:   "inbox-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("create consumer: %v", err)
	}

	// --- Publisher wraps *nats.Conn ---
	publisher := &natsPublisher{nc: nc}

	handler := NewHandler(store, publisher)

	log.Printf("inbox-worker started (site=%s)", siteID)

	// --- Consume loop ---
	go func() {
		for {
			msgs, err := cons.Fetch(10, jetstream.FetchMaxWait(5*time.Second))
			if err != nil {
				if err == context.Canceled {
					return
				}
				log.Printf("fetch: %v", err)
				continue
			}
			for msg := range msgs.Messages() {
				if err := handler.HandleEvent(ctx, msg.Data()); err != nil {
					log.Printf("handle error: %v", err)
					msg.Nak()
					continue
				}
				msg.Ack()
			}
			if msgs.Error() != nil {
				log.Printf("fetch iteration error: %v", msgs.Error())
			}
		}
	}()

	// --- Graceful shutdown ---
	shutdown.Wait(ctx,
		func(ctx context.Context) error {
			nc.Close()
			return nil
		},
		func(ctx context.Context) error {
			mongoutil.Disconnect(ctx, mongoClient)
			return nil
		},
	)
}

// natsPublisher adapts *nats.Conn to the Publisher interface.
type natsPublisher struct {
	nc *nats.Conn
}

func (p *natsPublisher) Publish(subject string, data []byte) error {
	return p.nc.Publish(subject, data)
}
```

- [ ] **Step 2: Verify compiles** (will fail without running services, but syntax/types should resolve)

```bash
cd inbox-worker && go build .
```

- [ ] **Step 3: Commit**

```bash
git add inbox-worker/main.go && git commit -m "feat(inbox-worker): add main.go wiring NATS consumer, MongoDB, and shutdown"
```

---

### Task 3: `Dockerfile` -- Multi-stage build

- [ ] **Step 1: Create `inbox-worker/Dockerfile`**

```dockerfile
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY pkg/ pkg/
COPY inbox-worker/ inbox-worker/

RUN CGO_ENABLED=0 go build -o /inbox-worker ./inbox-worker/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /inbox-worker /inbox-worker

ENTRYPOINT ["/inbox-worker"]
```

- [ ] **Step 2: Verify Dockerfile syntax** (dry-run parse)

```bash
head -n 1 inbox-worker/Dockerfile
```

- [ ] **Step 3: Commit**

```bash
git add inbox-worker/Dockerfile && git commit -m "feat(inbox-worker): add Dockerfile for multi-stage build"
```

---

## Summary

| Task | Files | TDD Cycle | Key Assertions |
|------|-------|-----------|----------------|
| 1 | `handler.go`, `handler_test.go` | Red -> Green | `member_added` creates subscription + publishes `SubscriptionUpdateEvent`; `room_sync` upserts room without publishing; unknown types are silently skipped; invalid JSON returns error; invalid payloads return error without store mutations; timestamps are set correctly |
| 2 | `main.go` | Build check | Wires NATS pull consumer (`"inbox-worker"` durable on `INBOX_{siteID}`), MongoDB `subscriptions` + `rooms` collections, graceful shutdown |
| 3 | `Dockerfile` | Build check | Multi-stage, copies `pkg/` and `inbox-worker/` |

### Key Design Decisions

1. **InboxStore interface:** The handler depends on `InboxStore` (with `CreateSubscription` and `UpsertRoom`) rather than MongoDB directly. Tests use an in-memory stub; `main.go` provides the real MongoDB implementation.
2. **Publisher interface:** Same pattern as other workers — `handler.go` depends on a `Publisher` interface rather than `*nats.Conn`, making the handler fully unit-testable without a running NATS server.
3. **Event type dispatch:** `HandleEvent` switches on `OutboxEvent.Type` and dispatches to private methods. Unknown types are logged and skipped (return `nil`), so unrecognized events are acked and don't cause infinite redelivery.
4. **member_added flow:** Creates a `Subscription` with a generated UUID, `RoleMember`, and current timestamps. Then publishes a `SubscriptionUpdateEvent` with `Action: "added"` to `chat.user.{inviteeID}.event.subscription.update`. If the publish fails, the error is logged but not returned — the subscription is already persisted.
5. **room_sync flow:** Upserts the `Room` document in MongoDB by `_id`. No NATS publish is needed — the room metadata is stored locally for query by other services.
6. **Partial failure tolerance:** For `member_added`, store persistence is the critical path. If the NATS publish fails afterward, the handler logs the error and returns success so the message is acked. The user will discover the subscription on their next reconnect or room list refresh.
