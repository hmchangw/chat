# Plan 3: Message Worker

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create the message worker service that consumes incoming chat messages from JetStream, validates sender subscription, persists to Cassandra + MongoDB, replies to sender, and publishes fanout events.

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB, Cassandra, `pkg/model`, `pkg/subject`, `pkg/natsutil`, `pkg/stream`, `pkg/mongoutil`, `pkg/cassutil`, `pkg/shutdown`

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `message-worker/store.go` | `MessageStore` interface + in-memory implementation |
| Create | `message-worker/store_test.go` | Store interface tests |
| Create | `message-worker/store_mongo.go` | Real MongoDB + Cassandra implementation |
| Create | `message-worker/handler.go` | JetStream consumer handler |
| Create | `message-worker/handler_test.go` | Handler tests with in-memory store |
| Create | `message-worker/main.go` | Entry point, wiring |
| Create | `message-worker/Dockerfile` | Multi-stage Docker build |

---

### Task 1: `store.go` + `store_test.go` — MessageStore interface

- [ ] **Step 1: Write tests** — `message-worker/store_test.go`

```go
package main

import (
	"context"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

func TestMemoryStore_GetSubscription(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	// No subscription → error
	_, err := s.GetSubscription(ctx, "u1", "r1")
	if err == nil {
		t.Fatal("expected error for missing subscription")
	}

	// Add subscription → found
	s.subscriptions = append(s.subscriptions, model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleMember,
	})
	sub, err := s.GetSubscription(ctx, "u1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.UserID != "u1" || sub.RoomID != "r1" {
		t.Errorf("got %+v", sub)
	}
}

func TestMemoryStore_SaveMessage(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	msg := model.Message{ID: "m1", RoomID: "r1", UserID: "u1", Content: "hello", CreatedAt: time.Now()}
	if err := s.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(s.messages) != 1 || s.messages[0].ID != "m1" {
		t.Errorf("messages = %+v", s.messages)
	}
}

func TestMemoryStore_UpdateRoomLastMessage(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	s.rooms = append(s.rooms, model.Room{ID: "r1", Name: "general"})
	now := time.Now()
	if err := s.UpdateRoomLastMessage(ctx, "r1", now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Create `message-worker/store.go`**

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

// MessageStore defines persistence operations for the message worker.
type MessageStore interface {
	GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
	SaveMessage(ctx context.Context, msg model.Message) error
	UpdateRoomLastMessage(ctx context.Context, roomID string, at time.Time) error
}

// MemoryStore is an in-memory implementation for testing.
type MemoryStore struct {
	subscriptions []model.Subscription
	messages      []model.Message
	rooms         []model.Room
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (s *MemoryStore) GetSubscription(_ context.Context, userID, roomID string) (*model.Subscription, error) {
	for i := range s.subscriptions {
		if s.subscriptions[i].UserID == userID && s.subscriptions[i].RoomID == roomID {
			return &s.subscriptions[i], nil
		}
	}
	return nil, fmt.Errorf("subscription not found: user=%s room=%s", userID, roomID)
}

func (s *MemoryStore) SaveMessage(_ context.Context, msg model.Message) error {
	s.messages = append(s.messages, msg)
	return nil
}

func (s *MemoryStore) UpdateRoomLastMessage(_ context.Context, roomID string, at time.Time) error {
	for i := range s.rooms {
		if s.rooms[i].ID == roomID {
			s.rooms[i].UpdatedAt = at
			return nil
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests — expect PASS**

- [ ] **Step 5: Commit**

```bash
git add message-worker/store.go message-worker/store_test.go && git commit -m "feat(message-worker): add MessageStore interface and in-memory implementation"
```

---

### Task 2: `handler.go` + `handler_test.go` — Message processing handler

- [ ] **Step 1: Write tests** — `message-worker/handler_test.go`

```go
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
```

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Create `message-worker/handler.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/nats-io/nats.go/jetstream"
)

type Handler struct {
	store   MessageStore
	siteID  string
	publish func(subj string, data []byte) error
}

func NewHandler(store MessageStore, siteID string, publish func(string, []byte) error) *Handler {
	return &Handler{store: store, siteID: siteID, publish: publish}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGES stream.
func (h *Handler) HandleJetStreamMsg(msg jetstream.Msg) {
	// Extract userID and roomID from subject: chat.user.{account}.room.{roomID}.{siteID}.msg.send
	parts := strings.Split(msg.Subject(), ".")
	if len(parts) < 7 {
		log.Printf("invalid subject: %s", msg.Subject())
		msg.Ack()
		return
	}
	userID := parts[2]
	roomID := parts[4]
	siteID := parts[5]

	ctx := context.Background()
	replyData, err := h.processMessage(ctx, userID, roomID, siteID, msg.Data())
	if err != nil {
		log.Printf("process message error: %v", err)
		// Reply with error if there's a reply subject in headers
		msg.Ack()
		return
	}

	// Publish reply to sender's response subject
	if reqID := getRequestID(msg.Data()); reqID != "" {
		respSubj := subject.UserResponse(userID, reqID)
		if err := h.publish(respSubj, replyData); err != nil {
			log.Printf("reply publish error: %v", err)
		}
	}

	msg.Ack()
}

func (h *Handler) processMessage(ctx context.Context, userID, roomID, siteID string, data []byte) ([]byte, error) {
	var req model.SendMessageRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	// Validate subscription
	_, err := h.store.GetSubscription(ctx, userID, roomID)
	if err != nil {
		return nil, fmt.Errorf("not subscribed: %w", err)
	}

	// Create message
	now := time.Now().UTC()
	msg := model.Message{
		ID:        uuid.New().String(),
		RoomID:    roomID,
		UserID:    userID,
		Content:   req.Content,
		CreatedAt: now,
	}

	// Persist
	if err := h.store.SaveMessage(ctx, msg); err != nil {
		return nil, fmt.Errorf("save message: %w", err)
	}
	if err := h.store.UpdateRoomLastMessage(ctx, roomID, now); err != nil {
		log.Printf("update room last message: %v", err)
	}

	// Publish fanout event
	evt := model.MessageEvent{Message: msg, RoomID: roomID, SiteID: siteID}
	evtData, _ := json.Marshal(evt)
	fanoutSubj := subject.Fanout(siteID, roomID, msg.ID)
	if err := h.publish(fanoutSubj, evtData); err != nil {
		log.Printf("fanout publish error: %v", err)
	}

	// Return message as reply
	return json.Marshal(msg)
}

func getRequestID(data []byte) string {
	var req model.SendMessageRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return ""
	}
	return req.RequestID
}
```

- [ ] **Step 4: Run tests — expect PASS**

- [ ] **Step 5: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go && git commit -m "feat(message-worker): add message processing handler"
```

---

### Task 3: `store_mongo.go` — Real MongoDB + Cassandra store

- [ ] **Step 1: Create `message-worker/store_mongo.go`**

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"github.com/hmchangw/chat/pkg/model"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type MongoStore struct {
	subscriptions *mongo.Collection
	rooms         *mongo.Collection
	cassSession   *gocql.Session
}

func NewMongoStore(db *mongo.Database, cassSession *gocql.Session) *MongoStore {
	return &MongoStore{
		subscriptions: db.Collection("subscriptions"),
		rooms:         db.Collection("rooms"),
		cassSession:   cassSession,
	}
}

func (s *MongoStore) GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"userId": userID, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		return nil, fmt.Errorf("subscription not found: %w", err)
	}
	return &sub, nil
}

func (s *MongoStore) SaveMessage(ctx context.Context, msg model.Message) error {
	return s.cassSession.Query(
		`INSERT INTO messages (room_id, created_at, id, user_id, content) VALUES (?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, msg.UserID, msg.Content,
	).WithContext(ctx).Exec()
}

func (s *MongoStore) UpdateRoomLastMessage(ctx context.Context, roomID string, at time.Time) error {
	filter := bson.M{"_id": roomID}
	update := bson.M{"$set": bson.M{"updatedAt": at}}
	_, err := s.rooms.UpdateOne(ctx, filter, update)
	return err
}
```

- [ ] **Step 2: Verify compiles** — `go build ./message-worker/`

- [ ] **Step 3: Commit**

```bash
git add message-worker/store_mongo.go && git commit -m "feat(message-worker): add MongoDB + Cassandra store implementation"
```

---

### Task 4: `main.go` — Entry point

- [ ] **Step 1: Create `message-worker/main.go`**

```go
package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/hmchangw/chat/pkg/cassutil"
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
	cassHosts := envOr("CASSANDRA_HOSTS", "localhost")
	cassKeyspace := envOr("CASSANDRA_KEYSPACE", "chat")

	ctx := context.Background()

	// Connect to NATS
	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("nats connect: %v", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	// Connect to MongoDB
	mongoClient, err := mongoutil.Connect(ctx, mongoURI)
	if err != nil {
		log.Fatalf("mongo: %v", err)
	}
	db := mongoClient.Database(mongoDB)

	// Connect to Cassandra
	cassSession, err := cassutil.Connect(strings.Split(cassHosts, ","), cassKeyspace)
	if err != nil {
		log.Fatalf("cassandra: %v", err)
	}

	// Create store and handler
	store := NewMongoStore(db, cassSession)
	handler := NewHandler(store, siteID, func(subj string, data []byte) error {
		return nc.Publish(subj, data)
	})

	// Ensure stream exists
	streamCfg := stream.Messages(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamCfg.Name,
		Subjects: streamCfg.Subjects,
	})
	if err != nil {
		log.Fatalf("create stream: %v", err)
	}

	// Create pull consumer
	cons, err := js.CreateOrUpdateConsumer(ctx, streamCfg.Name, jetstream.ConsumerConfig{
		Durable:   "message-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("create consumer: %v", err)
	}

	// Start consuming
	_, err = cons.Consume(handler.HandleJetStreamMsg)
	if err != nil {
		log.Fatalf("consume: %v", err)
	}

	log.Printf("message-worker running (site=%s)", siteID)

	shutdown.Wait(ctx,
		func(ctx context.Context) error { nc.Drain(); return nil },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
		func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
	)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 2: Verify compiles** — `go build ./message-worker/`

- [ ] **Step 3: Commit**

```bash
git add message-worker/main.go && git commit -m "feat(message-worker): add main entry point"
```

---

### Task 5: `Dockerfile`

- [ ] **Step 1: Create `message-worker/Dockerfile`**

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY message-worker/ message-worker/
RUN CGO_ENABLED=0 go build -o /message-worker ./message-worker/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /message-worker /message-worker
ENTRYPOINT ["/message-worker"]
```

- [ ] **Step 2: Commit**

```bash
git add message-worker/Dockerfile && git commit -m "feat(message-worker): add Dockerfile"
```
