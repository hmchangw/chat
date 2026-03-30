# Plan 7: Room Worker

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create the room worker that processes approved member invitations from the ROOMS JetStream stream. Creates subscription documents, updates room user counts, publishes outbox events for cross-site members, and notifies all room members.

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB, `pkg/model`, `pkg/subject`, `pkg/natsutil`, `pkg/stream`, `pkg/mongoutil`, `pkg/shutdown`

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `room-worker/store.go` | `SubscriptionStore` interface + in-memory implementation |
| Create | `room-worker/store_test.go` | Store tests |
| Create | `room-worker/store_mongo.go` | Real MongoDB implementation |
| Create | `room-worker/handler.go` | JetStream consumer handler |
| Create | `room-worker/handler_test.go` | Handler tests |
| Create | `room-worker/main.go` | Entry point |
| Create | `room-worker/Dockerfile` | Multi-stage Docker build |

---

### Task 1: `store.go` + `store_test.go`

- [ ] **Step 1: Write tests** — `room-worker/store_test.go`

```go
package main

import (
	"context"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
)

func TestMemoryStore_CreateSubscription(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	sub := model.Subscription{ID: "s1", UserID: "u1", RoomID: "r1", Role: model.RoleMember}
	if err := s.CreateSubscription(ctx, sub); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	subs, err := s.ListByRoom(ctx, "r1")
	if err != nil {
		t.Fatalf("ListByRoom: %v", err)
	}
	if len(subs) != 1 || subs[0].UserID != "u1" {
		t.Errorf("got %+v", subs)
	}
}

func TestMemoryStore_IncrementUserCount(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	s.rooms["r1"] = model.Room{ID: "r1", Name: "general", UserCount: 5}
	if err := s.IncrementUserCount(ctx, "r1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	room, _ := s.GetRoom(ctx, "r1")
	if room.UserCount != 6 {
		t.Errorf("UserCount = %d, want 6", room.UserCount)
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Create `room-worker/store.go`**

```go
package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/hmchangw/chat/pkg/model"
)

type SubscriptionStore interface {
	CreateSubscription(ctx context.Context, sub model.Subscription) error
	ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
	IncrementUserCount(ctx context.Context, roomID string) error
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
}

type MemoryStore struct {
	mu            sync.RWMutex
	subscriptions []model.Subscription
	rooms         map[string]model.Room
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rooms: make(map[string]model.Room)}
}

func (s *MemoryStore) CreateSubscription(_ context.Context, sub model.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions = append(s.subscriptions, sub)
	return nil
}

func (s *MemoryStore) ListByRoom(_ context.Context, roomID string) ([]model.Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []model.Subscription
	for _, sub := range s.subscriptions {
		if sub.RoomID == roomID {
			result = append(result, sub)
		}
	}
	return result, nil
}

func (s *MemoryStore) IncrementUserCount(_ context.Context, roomID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	room, ok := s.rooms[roomID]
	if !ok {
		return fmt.Errorf("room %q not found", roomID)
	}
	room.UserCount++
	s.rooms[roomID] = room
	return nil
}

func (s *MemoryStore) GetRoom(_ context.Context, roomID string) (*model.Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	room, ok := s.rooms[roomID]
	if !ok {
		return nil, fmt.Errorf("room %q not found", roomID)
	}
	return &room, nil
}
```

- [ ] **Step 4: Run tests — expect PASS**

- [ ] **Step 5: Commit**

```bash
git add room-worker/store.go room-worker/store_test.go && git commit -m "feat(room-worker): add SubscriptionStore interface and in-memory implementation"
```

---

### Task 2: `handler.go` + `handler_test.go`

- [ ] **Step 1: Write tests** — `room-worker/handler_test.go`

```go
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
```

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Create `room-worker/handler.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/nats-io/nats.go/jetstream"
)

type Handler struct {
	store   SubscriptionStore
	siteID  string
	publish func(subj string, data []byte) error
}

func NewHandler(store SubscriptionStore, siteID string, publish func(string, []byte) error) *Handler {
	return &Handler{store: store, siteID: siteID, publish: publish}
}

func (h *Handler) HandleJetStreamMsg(msg jetstream.Msg) {
	if err := h.processInvite(context.Background(), msg.Data()); err != nil {
		log.Printf("process invite error: %v", err)
	}
	msg.Ack()
}

func (h *Handler) processInvite(ctx context.Context, data []byte) error {
	var req model.InviteMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return err
	}

	now := time.Now().UTC()

	// Create subscription for invitee
	sub := model.Subscription{
		ID:                 uuid.New().String(),
		UserID:             req.InviteeID,
		RoomID:             req.RoomID,
		SiteID:             req.SiteID,
		Role:               model.RoleMember,
		HistorySharedSince: now,
		JoinedAt:           now,
	}
	if err := h.store.CreateSubscription(ctx, sub); err != nil {
		return err
	}

	// Increment room user count
	if err := h.store.IncrementUserCount(ctx, req.RoomID); err != nil {
		log.Printf("increment user count: %v", err)
	}

	// If invitee is on different site, publish outbox event
	if req.SiteID != h.siteID {
		outbox := model.OutboxEvent{
			Type:       "member_added",
			SiteID:     h.siteID,
			DestSiteID: req.SiteID,
			Payload:    data,
		}
		outboxData, _ := json.Marshal(outbox)
		outboxSubj := subject.Outbox(h.siteID, req.SiteID, "member_added")
		if err := h.publish(outboxSubj, outboxData); err != nil {
			log.Printf("outbox publish: %v", err)
		}
	}

	// Notify invitee: subscription update
	subEvt := model.SubscriptionUpdateEvent{UserID: req.InviteeID, Subscription: sub, Action: "added"}
	subEvtData, _ := json.Marshal(subEvt)
	h.publish(subject.SubscriptionUpdate(req.InviteeID), subEvtData)

	// Notify all existing members: room metadata changed
	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err == nil {
		metaEvt := model.RoomMetadataUpdateEvent{
			RoomID:    req.RoomID,
			Name:      room.Name,
			UserCount: room.UserCount,
			UpdatedAt: now,
		}
		metaData, _ := json.Marshal(metaEvt)

		members, _ := h.store.ListByRoom(ctx, req.RoomID)
		for _, m := range members {
			h.publish(subject.RoomMetadataChanged(m.UserID), metaData)
		}
	}

	return nil
}
```

- [ ] **Step 4: Run tests — expect PASS**

- [ ] **Step 5: Commit**

```bash
git add room-worker/handler.go room-worker/handler_test.go && git commit -m "feat(room-worker): add invite processing handler"
```

---

### Task 3: `store_mongo.go` + `main.go` + `Dockerfile`

- [ ] **Step 1: Create `room-worker/store_mongo.go`**

```go
package main

import (
	"context"
	"fmt"

	"github.com/hmchangw/chat/pkg/model"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type MongoStore struct {
	subscriptions *mongo.Collection
	rooms         *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		subscriptions: db.Collection("subscriptions"),
		rooms:         db.Collection("rooms"),
	}
}

func (s *MongoStore) CreateSubscription(ctx context.Context, sub model.Subscription) error {
	_, err := s.subscriptions.InsertOne(ctx, sub)
	return err
}

func (s *MongoStore) ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error) {
	cursor, err := s.subscriptions.Find(ctx, bson.M{"roomId": roomID})
	if err != nil {
		return nil, err
	}
	var subs []model.Subscription
	return subs, cursor.All(ctx, &subs)
}

func (s *MongoStore) IncrementUserCount(ctx context.Context, roomID string) error {
	_, err := s.rooms.UpdateOne(ctx, bson.M{"_id": roomID}, bson.M{"$inc": bson.M{"userCount": 1}})
	return err
}

func (s *MongoStore) GetRoom(ctx context.Context, roomID string) (*model.Room, error) {
	var room model.Room
	if err := s.rooms.FindOne(ctx, bson.M{"_id": roomID}).Decode(&room); err != nil {
		return nil, fmt.Errorf("room %q not found: %w", roomID, err)
	}
	return &room, nil
}
```

- [ ] **Step 2: Create `room-worker/main.go`**

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	natsURL := envOr("NATS_URL", nats.DefaultURL)
	siteID := envOr("SITE_ID", "site-local")
	mongoURI := envOr("MONGO_URI", "mongodb://localhost:27017")
	mongoDB := envOr("MONGO_DB", "chat")

	ctx := context.Background()

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("nats: %v", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	mongoClient, err := mongoutil.Connect(ctx, mongoURI)
	if err != nil {
		log.Fatalf("mongo: %v", err)
	}

	streamCfg := stream.Rooms(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: streamCfg.Name, Subjects: streamCfg.Subjects,
	})
	if err != nil {
		log.Fatalf("create stream: %v", err)
	}

	store := NewMongoStore(mongoClient.Database(mongoDB))
	handler := NewHandler(store, siteID, func(subj string, data []byte) error {
		return nc.Publish(subj, data)
	})

	cons, err := js.CreateOrUpdateConsumer(ctx, streamCfg.Name, jetstream.ConsumerConfig{
		Durable: "room-worker", AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("create consumer: %v", err)
	}
	cons.Consume(handler.HandleJetStreamMsg)

	log.Printf("room-worker running (site=%s)", siteID)

	shutdown.Wait(ctx,
		func(ctx context.Context) error { nc.Drain(); return nil },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 3: Create `room-worker/Dockerfile`**

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY room-worker/ room-worker/
RUN CGO_ENABLED=0 go build -o /room-worker ./room-worker/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /room-worker /room-worker
ENTRYPOINT ["/room-worker"]
```

- [ ] **Step 4: Verify compiles** — `go build ./room-worker/`

- [ ] **Step 5: Commit**

```bash
git add room-worker/ && git commit -m "feat(room-worker): add MongoDB store, main entry point, and Dockerfile"
```
