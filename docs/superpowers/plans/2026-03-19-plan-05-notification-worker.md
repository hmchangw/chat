# Plan 5: Notification Worker

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create a notification worker that consumes `MessageEvent` from the `FANOUT_{siteID}` JetStream stream, looks up room members in MongoDB, and publishes `NotificationEvent` to each member (excluding the sender).

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB

**Depends on:** Plan 1 (Foundation) for all `pkg/` packages

---

## Architecture

```
FANOUT_{siteID} stream
    |
    | pull consumer: "notification-worker"
    v
notification-worker
    |
    | 1. Unmarshal MessageEvent
    | 2. ListSubscriptions(roomID) from MongoDB
    | 3. For each member != sender:
    |      publish NotificationEvent to chat.user.{memberUserID}.notification
    | 4. Ack
    v
NATS core publish -> per-user notification subjects
```

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `notification-worker/handler.go` | `Handler` struct with `MemberLookup` interface, message processing |
| Create | `notification-worker/handler_test.go` | Unit tests: fan-out logic, sender exclusion, error handling |
| Create | `notification-worker/main.go` | Wire NATS, MongoDB, JetStream consumer, graceful shutdown |
| Create | `notification-worker/Dockerfile` | Multi-stage build |

## Shared Imports from `pkg/`

| Package | Symbols Used |
|---------|-------------|
| `pkg/model` | `MessageEvent`, `NotificationEvent`, `Subscription` |
| `pkg/subject` | `Notification`, `FanoutWildcard` |
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

### Task 1: `handler.go` + `handler_test.go` — Handler with MemberLookup interface

- [ ] **Step 1: Write tests** -- `notification-worker/handler_test.go`

```go
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

func TestHandleMessage_FanOutSkipsSender(t *testing.T) {
	lookup := &stubMemberLookup{
		subs: map[string][]model.Subscription{
			"room-1": {
				{ID: "s1", UserID: "alice", RoomID: "room-1"},
				{ID: "s2", UserID: "bob", RoomID: "room-1"},
				{ID: "s3", UserID: "carol", RoomID: "room-1"},
			},
		},
	}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	evt := model.MessageEvent{
		RoomID: "room-1",
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

	if !subjects["chat.user.bob.notification"] {
		t.Error("missing notification for bob")
	}
	if !subjects["chat.user.carol.notification"] {
		t.Error("missing notification for carol")
	}
	if subjects["chat.user.alice.notification"] {
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
		RoomID: "empty-room",
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
				{ID: "s1", UserID: "alice", RoomID: "room-solo"},
			},
		},
	}
	pub := &mockPublisher{}
	h := NewHandler(lookup, pub)

	evt := model.MessageEvent{
		RoomID: "room-solo",
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
```

- [ ] **Step 2: Run tests -- expect FAIL** (handler not defined yet)

```bash
cd notification-worker && go test -v -run .
```

- [ ] **Step 3: Create `notification-worker/handler.go`**

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

// MemberLookup reads room membership from a data store.
type MemberLookup interface {
	ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
}

// Publisher abstracts NATS publishing so the handler is testable.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Handler processes fanout messages and sends notifications.
type Handler struct {
	members MemberLookup
	pub     Publisher
}

func NewHandler(members MemberLookup, pub Publisher) *Handler {
	return &Handler{members: members, pub: pub}
}

// HandleMessage processes a single JetStream message payload.
// It unmarshals the MessageEvent, looks up room members, and publishes
// a NotificationEvent to every member except the message sender.
func (h *Handler) HandleMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	subs, err := h.members.ListSubscriptions(ctx, evt.RoomID)
	if err != nil {
		return fmt.Errorf("list subscriptions for room %s: %w", evt.RoomID, err)
	}

	notif := model.NotificationEvent{
		Type:    "new_message",
		RoomID:  evt.RoomID,
		Message: evt.Message,
	}

	notifData, err := natsutil.MarshalResponse(notif)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	senderID := evt.Message.UserID

	for _, sub := range subs {
		if sub.UserID == senderID {
			continue
		}
		subj := subject.Notification(sub.UserID)
		if err := h.pub.Publish(subj, notifData); err != nil {
			log.Printf("failed to publish notification to %s: %v", sub.UserID, err)
			// Continue notifying other members; don't fail the whole batch.
		}
	}

	return nil
}
```

- [ ] **Step 4: Run tests -- expect PASS**

```bash
cd notification-worker && go test -v -run .
```

- [ ] **Step 5: Commit**

```bash
git add notification-worker/handler.go notification-worker/handler_test.go && git commit -m "feat(notification-worker): add handler with MemberLookup interface and fan-out tests"
```

---

### Task 2: `main.go` -- Wire everything

- [ ] **Step 1: Create `notification-worker/main.go`**

```go
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// mongoMemberLookup implements MemberLookup using MongoDB.
type mongoMemberLookup struct {
	col *mongo.Collection
}

func (m *mongoMemberLookup) ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error) {
	import "github.com/hmchangw/chat/pkg/model"
	// This import is at the top of the file; shown here for clarity.

	filter := map[string]string{"roomId": roomID}
	cursor, err := m.col.Find(ctx, filter)
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
	subCol := mongoClient.Database(mongoDB).Collection("subscriptions")
	memberLookup := &mongoMemberLookup{col: subCol}

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

	// --- Create pull consumer (different name than broadcast-worker) ---
	cons, err := js.CreateOrUpdateConsumer(ctx, fanoutCfg.Name, jetstream.ConsumerConfig{
		Durable:   "notification-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("create consumer: %v", err)
	}

	// --- natsPublisher wraps *nats.Conn to satisfy Publisher interface ---
	publisher := &natsPublisher{nc: nc}

	handler := NewHandler(memberLookup, publisher)

	log.Printf("notification-worker started (site=%s)", siteID)

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

**Important:** The above shows the conceptual structure. The actual file must have proper imports at the top. Here is the corrected, compilable version:

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

// mongoMemberLookup implements MemberLookup using MongoDB.
type mongoMemberLookup struct {
	col *mongo.Collection
}

func (m *mongoMemberLookup) ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error) {
	filter := map[string]string{"roomId": roomID}
	cursor, err := m.col.Find(ctx, filter)
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
	subCol := mongoClient.Database(mongoDB).Collection("subscriptions")
	memberLookup := &mongoMemberLookup{col: subCol}

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

	// --- Create pull consumer (different name than broadcast-worker) ---
	cons, err := js.CreateOrUpdateConsumer(ctx, fanoutCfg.Name, jetstream.ConsumerConfig{
		Durable:   "notification-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("create consumer: %v", err)
	}

	// --- natsPublisher wraps *nats.Conn to satisfy Publisher interface ---
	publisher := &natsPublisher{nc: nc}

	handler := NewHandler(memberLookup, publisher)

	log.Printf("notification-worker started (site=%s)", siteID)

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
cd notification-worker && go build .
```

- [ ] **Step 3: Commit**

```bash
git add notification-worker/main.go && git commit -m "feat(notification-worker): add main.go wiring NATS consumer, MongoDB, and shutdown"
```

---

### Task 3: `Dockerfile` -- Multi-stage build

- [ ] **Step 1: Create `notification-worker/Dockerfile`**

```dockerfile
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY pkg/ pkg/
COPY notification-worker/ notification-worker/

RUN CGO_ENABLED=0 go build -o /notification-worker ./notification-worker/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /notification-worker /notification-worker

ENTRYPOINT ["/notification-worker"]
```

- [ ] **Step 2: Verify Dockerfile syntax** (dry-run parse)

```bash
head -n 1 notification-worker/Dockerfile
```

- [ ] **Step 3: Commit**

```bash
git add notification-worker/Dockerfile && git commit -m "feat(notification-worker): add Dockerfile for multi-stage build"
```

---

## Summary

| Task | Files | TDD Cycle | Key Assertions |
|------|-------|-----------|----------------|
| 1 | `handler.go`, `handler_test.go` | Red -> Green | Fan-out publishes to all members except sender; empty room produces zero publishes; sole-member-is-sender produces zero publishes; invalid JSON returns error |
| 2 | `main.go` | Build check | Wires NATS pull consumer (`"notification-worker"` durable on `FANOUT_{siteID}`), MongoDB `subscriptions` collection, graceful shutdown |
| 3 | `Dockerfile` | Build check | Multi-stage, copies `pkg/` and `notification-worker/` |

### Key Design Decisions

1. **Different consumer name:** The consumer is named `"notification-worker"` (vs `"broadcast-worker"` in Plan 4), so both workers independently receive every message on the same `FANOUT_{siteID}` stream.
2. **Publisher interface:** `handler.go` depends on a `Publisher` interface rather than `*nats.Conn` directly, making the handler fully unit-testable without a running NATS server.
3. **MemberLookup interface:** The handler depends on `MemberLookup` rather than MongoDB directly. Tests use an in-memory stub; `main.go` provides the real MongoDB implementation.
4. **Sender exclusion:** The handler skips publishing to `sub.UserID == evt.Message.UserID`, ensuring the message author does not receive a self-notification.
5. **Partial failure tolerance:** If publishing to one member fails, the handler logs the error and continues notifying the remaining members rather than failing the entire batch.
