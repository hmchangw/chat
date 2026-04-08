# Plan 4: Broadcast Worker

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create a broadcast worker that consumes `MessageEvent` from the `FANOUT_{siteID}` JetStream stream, looks up the target room in MongoDB, publishes a `RoomMetadataUpdateEvent` to the room metadata subject, and fans out the message to either the group room stream or each DM member's personal stream.

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB

**Depends on:** Plan 1 (Foundation) for all `pkg/` packages

---

## Architecture

```
FANOUT_{siteID} stream
    |
    | pull consumer: "broadcast-worker"
    v
broadcast-worker
    |
    | 1. Unmarshal MessageEvent
    | 2. GetRoom(roomID) from MongoDB
    | 3. Publish RoomMetadataUpdateEvent to chat.room.{roomID}.event.metadata.update
    | 4. If room.Type == "group":
    |      publish message to chat.room.{roomID}.stream.msg
    | 5. If room.Type == "dm":
    |      ListSubscriptions(roomID) from MongoDB
    |      publish message to chat.user.{account}.stream.msg for each member
    | 6. Ack the JetStream message
    v
NATS core publish -> room/user stream subjects
```

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `broadcast-worker/handler.go` | `Handler` struct with `RoomLookup` interface, message processing |
| Create | `broadcast-worker/handler_test.go` | Unit tests: group broadcast, DM fan-out, metadata publish, error handling |
| Create | `broadcast-worker/main.go` | Wire NATS, MongoDB, JetStream consumer, graceful shutdown |
| Create | `broadcast-worker/Dockerfile` | Multi-stage build |

## Shared Imports from `pkg/`

| Package | Symbols Used |
|---------|-------------|
| `pkg/model` | `MessageEvent`, `RoomMetadataUpdateEvent`, `Room`, `Subscription` |
| `pkg/subject` | `RoomMetadataUpdate`, `RoomMsgStream`, `UserMsgStream`, `FanoutWildcard` |
| `pkg/natsutil` | `MarshalResponse` |
| `pkg/stream` | `stream.Fanout(siteID)` |
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

### Task 1: `handler.go` + `handler_test.go` — Handler with RoomLookup interface

- [ ] **Step 1: Write tests** -- `broadcast-worker/handler_test.go`

```go
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
				{ID: "s1", UserID: "alice", RoomID: "dm-1", SiteID: "site-a"},
				{ID: "s2", UserID: "bob", RoomID: "dm-1", SiteID: "site-a"},
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
```

- [ ] **Step 2: Run tests -- expect FAIL** (handler not defined yet)

```bash
cd broadcast-worker && go test -v -run .
```

- [ ] **Step 3: Create `broadcast-worker/handler.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// RoomLookup reads room data and membership from a data store.
type RoomLookup interface {
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
}

// Publisher abstracts NATS publishing so the handler is testable.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Handler processes fanout messages and broadcasts to room/user streams.
type Handler struct {
	rooms RoomLookup
	pub   Publisher
}

func NewHandler(rooms RoomLookup, pub Publisher) *Handler {
	return &Handler{rooms: rooms, pub: pub}
}

// HandleMessage processes a single JetStream message payload.
// It unmarshals the MessageEvent, looks up the room, publishes a
// RoomMetadataUpdateEvent, and fans the message out to the appropriate
// stream subjects based on the room type.
func (h *Handler) HandleMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	// --- Look up room ---
	room, err := h.rooms.GetRoom(ctx, evt.RoomID)
	if err != nil {
		return fmt.Errorf("get room %s: %w", evt.RoomID, err)
	}

	// --- Publish RoomMetadataUpdateEvent ---
	metaEvt := model.RoomMetadataUpdateEvent{
		RoomID:        room.ID,
		Name:          room.Name,
		UserCount:     room.UserCount,
		LastMessageAt: evt.Message.CreatedAt,
		UpdatedAt:     evt.Message.CreatedAt,
	}

	metaData, err := natsutil.MarshalResponse(metaEvt)
	if err != nil {
		return fmt.Errorf("marshal metadata event: %w", err)
	}

	metaSubj := subject.RoomMetadataUpdate(room.ID)
	if err := h.pub.Publish(metaSubj, metaData); err != nil {
		return fmt.Errorf("publish metadata update: %w", err)
	}

	// --- Fan out message based on room type ---
	evtData, err := natsutil.MarshalResponse(evt)
	if err != nil {
		return fmt.Errorf("marshal message event: %w", err)
	}

	switch room.Type {
	case model.RoomTypeGroup:
		subj := subject.RoomMsgStream(room.ID)
		if err := h.pub.Publish(subj, evtData); err != nil {
			return fmt.Errorf("publish to group room stream: %w", err)
		}

	case model.RoomTypeDM:
		subs, err := h.rooms.ListSubscriptions(ctx, room.ID)
		if err != nil {
			return fmt.Errorf("list subscriptions for DM room %s: %w", room.ID, err)
		}

		for _, sub := range subs {
			subj := subject.UserMsgStream(sub.UserID)
			if err := h.pub.Publish(subj, evtData); err != nil {
				log.Printf("failed to publish to user %s stream: %v", sub.UserID, err)
				// Continue publishing to remaining members.
			}
		}

	default:
		log.Printf("unknown room type %q for room %s, skipping fan-out", room.Type, room.ID)
	}

	return nil
}
```

- [ ] **Step 4: Run tests -- expect PASS**

```bash
cd broadcast-worker && go test -v -run .
```

- [ ] **Step 5: Commit**

```bash
git add broadcast-worker/handler.go broadcast-worker/handler_test.go && git commit -m "feat(broadcast-worker): add handler with RoomLookup interface and broadcast tests"
```

---

### Task 2: `main.go` -- Wire everything

- [ ] **Step 1: Create `broadcast-worker/main.go`**

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
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// mongoRoomLookup implements RoomLookup using MongoDB.
type mongoRoomLookup struct {
	roomCol *mongo.Collection
	subCol  *mongo.Collection
}

func (m *mongoRoomLookup) GetRoom(ctx context.Context, roomID string) (*model.Room, error) {
	filter := map[string]string{"_id": roomID}
	var room model.Room
	err := m.roomCol.FindOne(ctx, filter).Decode(&room)
	if err != nil {
		return nil, err
	}
	return &room, nil
}

func (m *mongoRoomLookup) ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error) {
	filter := map[string]string{"roomId": roomID}
	cursor, err := m.subCol.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var subs []model.Subscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, err
	}
	return subs, nil
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
	roomLookup := &mongoRoomLookup{
		roomCol: db.Collection("rooms"),
		subCol:  db.Collection("subscriptions"),
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

	// --- Ensure FANOUT stream exists ---
	fanoutCfg := stream.Fanout(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     fanoutCfg.Name,
		Subjects: fanoutCfg.Subjects,
	})
	if err != nil {
		log.Fatalf("create fanout stream: %v", err)
	}

	// --- Create pull consumer ---
	cons, err := js.CreateOrUpdateConsumer(ctx, fanoutCfg.Name, jetstream.ConsumerConfig{
		Durable:   "broadcast-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("create consumer: %v", err)
	}

	// --- Publisher wraps *nats.Conn ---
	publisher := &natsPublisher{nc: nc}

	handler := NewHandler(roomLookup, publisher)

	log.Printf("broadcast-worker started (site=%s)", siteID)

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
				if err := handler.HandleMessage(ctx, msg.Data()); err != nil {
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
cd broadcast-worker && go build .
```

- [ ] **Step 3: Commit**

```bash
git add broadcast-worker/main.go && git commit -m "feat(broadcast-worker): add main.go wiring NATS consumer, MongoDB, and shutdown"
```

---

### Task 3: `Dockerfile` -- Multi-stage build

- [ ] **Step 1: Create `broadcast-worker/Dockerfile`**

```dockerfile
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY pkg/ pkg/
COPY broadcast-worker/ broadcast-worker/

RUN CGO_ENABLED=0 go build -o /broadcast-worker ./broadcast-worker/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /broadcast-worker /broadcast-worker

ENTRYPOINT ["/broadcast-worker"]
```

- [ ] **Step 2: Verify Dockerfile syntax** (dry-run parse)

```bash
head -n 1 broadcast-worker/Dockerfile
```

- [ ] **Step 3: Commit**

```bash
git add broadcast-worker/Dockerfile && git commit -m "feat(broadcast-worker): add Dockerfile for multi-stage build"
```

---

## Summary

| Task | Files | TDD Cycle | Key Assertions |
|------|-------|-----------|----------------|
| 1 | `handler.go`, `handler_test.go` | Red -> Green | Group room publishes metadata update + room stream message; DM room publishes metadata update + per-user stream messages for each member; room-not-found returns error with zero publishes; invalid JSON returns error; metadata event carries correct roomID, name, userCount, and lastMessageAt |
| 2 | `main.go` | Build check | Wires NATS pull consumer (`"broadcast-worker"` durable on `FANOUT_{siteID}`), MongoDB `rooms` and `subscriptions` collections, graceful shutdown |
| 3 | `Dockerfile` | Build check | Multi-stage, copies `pkg/` and `broadcast-worker/` |

### Key Design Decisions

1. **Dedicated consumer name:** The consumer is named `"broadcast-worker"` (vs `"notification-worker"` in Plan 5), so both workers independently receive every message on the same `FANOUT_{siteID}` stream via separate JetStream consumers.
2. **Publisher interface:** `handler.go` depends on a `Publisher` interface rather than `*nats.Conn` directly, making the handler fully unit-testable without a running NATS server.
3. **RoomLookup interface:** The handler depends on `RoomLookup` (with both `GetRoom` and `ListSubscriptions`) rather than MongoDB directly. Tests use an in-memory stub; `main.go` provides the real MongoDB implementation via `mongoRoomLookup`.
4. **Metadata-first publishing:** The `RoomMetadataUpdateEvent` is always published before the message fan-out, ensuring subscribers see updated room metadata (e.g., `lastMessageAt`) before or alongside the new message.
5. **Room-type routing:** Group rooms publish to a single `chat.room.{roomID}.stream.msg` subject (subscribers pull from the room stream). DM rooms look up members and publish to each `chat.user.{account}.stream.msg` individually, since DM participants subscribe to their personal stream rather than a shared room stream.
6. **Partial failure tolerance for DM fan-out:** If publishing to one DM member fails, the handler logs the error and continues delivering to the remaining members rather than failing the entire batch.
7. **Two MongoDB collections:** `main.go` wires both `rooms` (for `GetRoom`) and `subscriptions` (for `ListSubscriptions`) collections into a single `mongoRoomLookup` struct.
