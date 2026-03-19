# Plan 6: Room Gatekeeper

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create the room gatekeeper service that handles room CRUD (create/list/get) via NATS request/reply AND member invitation authorization. Validates inviter has owner role before publishing to ROOMS JetStream stream.

**Tech Stack:** Go 1.25, NATS, JetStream, MongoDB, `pkg/model`, `pkg/subject`, `pkg/natsutil`, `pkg/stream`, `pkg/mongoutil`, `pkg/shutdown`

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `room-gatekeeper/store.go` | `RoomStore` interface + in-memory implementation |
| Create | `room-gatekeeper/store_test.go` | Store interface tests |
| Create | `room-gatekeeper/store_mongo.go` | Real MongoDB implementation |
| Create | `room-gatekeeper/handler.go` | Room CRUD + invite authorization handlers |
| Create | `room-gatekeeper/handler_test.go` | Handler tests |
| Create | `room-gatekeeper/main.go` | Entry point |
| Create | `room-gatekeeper/Dockerfile` | Multi-stage Docker build |

---

### Task 1: `store.go` + `store_test.go` — RoomStore interface

- [ ] **Step 1: Write tests** — `room-gatekeeper/store_test.go`

```go
package main

import (
	"context"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
)

func TestMemoryStore_CreateAndGetRoom(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	room := model.Room{ID: "r1", Name: "general", Type: model.RoomTypeGroup, SiteID: "site-a", CreatedBy: "u1"}
	if err := s.CreateRoom(ctx, room); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	got, err := s.GetRoom(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoom: %v", err)
	}
	if got.Name != "general" {
		t.Errorf("Name = %q", got.Name)
	}
}

func TestMemoryStore_ListRooms(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	s.CreateRoom(ctx, model.Room{ID: "r1", Name: "general"})
	s.CreateRoom(ctx, model.Room{ID: "r2", Name: "random"})

	rooms, err := s.ListRooms(ctx)
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) != 2 {
		t.Errorf("got %d rooms", len(rooms))
	}
}

func TestMemoryStore_Subscription(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	sub := model.Subscription{ID: "s1", UserID: "u1", RoomID: "r1", Role: model.RoleOwner}
	if err := s.CreateSubscription(ctx, sub); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	got, err := s.GetSubscription(ctx, "u1", "r1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if got.Role != model.RoleOwner {
		t.Errorf("Role = %q", got.Role)
	}

	// Not found
	_, err = s.GetSubscription(ctx, "u2", "r1")
	if err == nil {
		t.Error("expected error for missing subscription")
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Create `room-gatekeeper/store.go`**

```go
package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/hmchangw/chat/pkg/model"
)

type RoomStore interface {
	CreateRoom(ctx context.Context, room model.Room) error
	GetRoom(ctx context.Context, id string) (*model.Room, error)
	ListRooms(ctx context.Context) ([]model.Room, error)
	GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
	CreateSubscription(ctx context.Context, sub model.Subscription) error
}

type MemoryStore struct {
	mu            sync.RWMutex
	rooms         map[string]model.Room
	subscriptions []model.Subscription
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rooms: make(map[string]model.Room)}
}

func (s *MemoryStore) CreateRoom(_ context.Context, room model.Room) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rooms[room.ID] = room
	return nil
}

func (s *MemoryStore) GetRoom(_ context.Context, id string) (*model.Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	room, ok := s.rooms[id]
	if !ok {
		return nil, fmt.Errorf("room %q not found", id)
	}
	return &room, nil
}

func (s *MemoryStore) ListRooms(_ context.Context) ([]model.Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rooms := make([]model.Room, 0, len(s.rooms))
	for _, r := range s.rooms {
		rooms = append(rooms, r)
	}
	return rooms, nil
}

func (s *MemoryStore) GetSubscription(_ context.Context, userID, roomID string) (*model.Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.subscriptions {
		if s.subscriptions[i].UserID == userID && s.subscriptions[i].RoomID == roomID {
			return &s.subscriptions[i], nil
		}
	}
	return nil, fmt.Errorf("subscription not found: user=%s room=%s", userID, roomID)
}

func (s *MemoryStore) CreateSubscription(_ context.Context, sub model.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions = append(s.subscriptions, sub)
	return nil
}
```

- [ ] **Step 4: Run tests — expect PASS**

- [ ] **Step 5: Commit**

```bash
git add room-gatekeeper/store.go room-gatekeeper/store_test.go && git commit -m "feat(room-gatekeeper): add RoomStore interface and in-memory implementation"
```

---

### Task 2: `handler.go` + `handler_test.go` — Room CRUD + invite auth

- [ ] **Step 1: Write tests** — `room-gatekeeper/handler_test.go`

```go
package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_CreateRoom(t *testing.T) {
	store := NewMemoryStore()
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000}

	req := model.CreateRoomRequest{Name: "general", Type: model.RoomTypeGroup, CreatedBy: "u1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	resp, err := h.handleCreateRoom(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var room model.Room
	json.Unmarshal(resp, &room)
	if room.Name != "general" || room.CreatedBy != "u1" {
		t.Errorf("got %+v", room)
	}

	// Verify owner subscription was created
	sub, err := store.GetSubscription(context.Background(), "u1", room.ID)
	if err != nil {
		t.Fatalf("owner subscription not created: %v", err)
	}
	if sub.Role != model.RoleOwner {
		t.Errorf("role = %q, want owner", sub.Role)
	}
}

func TestHandler_InviteOwner_Success(t *testing.T) {
	store := NewMemoryStore()
	store.CreateRoom(context.Background(), model.Room{ID: "r1", Name: "general", UserCount: 1})
	store.CreateSubscription(context.Background(), model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleOwner,
	})

	var jsPublished []byte
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(data []byte) error { jsPublished = data; return nil },
	}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	_, err := h.handleInvite(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jsPublished == nil {
		t.Error("expected event published to JetStream")
	}
}

func TestHandler_InviteMember_Rejected(t *testing.T) {
	store := NewMemoryStore()
	store.CreateRoom(context.Background(), model.Room{ID: "r1", Name: "general", UserCount: 1})
	store.CreateSubscription(context.Background(), model.Subscription{
		UserID: "u2", RoomID: "r1", Role: model.RoleMember, // not owner
	})

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(data []byte) error { return nil },
	}

	req := model.InviteMemberRequest{InviterID: "u2", InviteeID: "u3", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	_, err := h.handleInvite(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for non-owner invite")
	}
}

func TestHandler_InviteExceedsMaxSize(t *testing.T) {
	store := NewMemoryStore()
	store.CreateRoom(context.Background(), model.Room{ID: "r1", Name: "general", UserCount: 1000})
	store.CreateSubscription(context.Background(), model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleOwner,
	})

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(data []byte) error { return nil },
	}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	_, err := h.handleInvite(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for room at max size")
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Create `room-gatekeeper/handler.go`**

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
	"github.com/nats-io/nats.go"
)

type Handler struct {
	store           RoomStore
	siteID          string
	maxRoomSize     int
	publishToStream func(data []byte) error
}

func NewHandler(store RoomStore, siteID string, maxRoomSize int, publishToStream func([]byte) error) *Handler {
	return &Handler{store: store, siteID: siteID, maxRoomSize: maxRoomSize, publishToStream: publishToStream}
}

// RegisterCRUD registers NATS request/reply handlers for room CRUD.
func (h *Handler) RegisterCRUD(nc *nats.Conn) error {
	if _, err := nc.Subscribe("chat.rooms.create", h.natsCreateRoom); err != nil {
		return err
	}
	if _, err := nc.Subscribe("chat.rooms.list", h.natsListRooms); err != nil {
		return err
	}
	if _, err := nc.Subscribe("chat.rooms.get.*", h.natsGetRoom); err != nil {
		return err
	}
	return nil
}

func (h *Handler) natsCreateRoom(msg *nats.Msg) {
	resp, err := h.handleCreateRoom(context.Background(), msg.Data)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	msg.Respond(resp)
}

func (h *Handler) natsListRooms(msg *nats.Msg) {
	rooms, err := h.store.ListRooms(context.Background())
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	natsutil.ReplyJSON(msg, model.ListRoomsResponse{Rooms: rooms})
}

func (h *Handler) natsGetRoom(msg *nats.Msg) {
	// Subject: chat.rooms.get.{roomID}
	parts := nats.GetSubjectToken(msg.Subject, 3)
	room, err := h.store.GetRoom(context.Background(), parts)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	natsutil.ReplyJSON(msg, room)
}

// NatsHandleInvite handles invite authorization requests.
func (h *Handler) NatsHandleInvite(msg *nats.Msg) {
	resp, err := h.handleInvite(context.Background(), msg.Data)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	msg.Respond(resp)
}

func (h *Handler) handleCreateRoom(ctx context.Context, data []byte) ([]byte, error) {
	var req model.CreateRoomRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	now := time.Now().UTC()
	room := model.Room{
		ID:        uuid.New().String(),
		Name:      req.Name,
		Type:      req.Type,
		CreatedBy: req.CreatedBy,
		SiteID:    req.SiteID,
		UserCount: 1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.store.CreateRoom(ctx, room); err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}

	// Auto-create owner subscription
	sub := model.Subscription{
		ID:                 uuid.New().String(),
		UserID:             req.CreatedBy,
		RoomID:             room.ID,
		SiteID:             req.SiteID,
		Role:               model.RoleOwner,
		SharedHistorySince: now,
		JoinedAt:           now,
	}
	if err := h.store.CreateSubscription(ctx, sub); err != nil {
		log.Printf("create owner subscription: %v", err)
	}

	return json.Marshal(room)
}

func (h *Handler) handleInvite(ctx context.Context, data []byte) ([]byte, error) {
	var req model.InviteMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	// Verify inviter is owner
	sub, err := h.store.GetSubscription(ctx, req.InviterID, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("inviter not found: %w", err)
	}
	if sub.Role != model.RoleOwner {
		return nil, fmt.Errorf("only owners can invite members")
	}

	// Check room size
	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("room not found: %w", err)
	}
	if room.UserCount >= h.maxRoomSize {
		return nil, fmt.Errorf("room is at maximum capacity (%d)", h.maxRoomSize)
	}

	// Publish to ROOMS stream for room-worker processing
	if err := h.publishToStream(data); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "ok"})
}
```

- [ ] **Step 4: Run tests — expect PASS**

- [ ] **Step 5: Commit**

```bash
git add room-gatekeeper/handler.go room-gatekeeper/handler_test.go && git commit -m "feat(room-gatekeeper): add room CRUD and invite authorization handlers"
```

---

### Task 3: `store_mongo.go` — Real MongoDB implementation

- [ ] **Step 1: Create `room-gatekeeper/store_mongo.go`**

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
	rooms         *mongo.Collection
	subscriptions *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		rooms:         db.Collection("rooms"),
		subscriptions: db.Collection("subscriptions"),
	}
}

func (s *MongoStore) CreateRoom(ctx context.Context, room model.Room) error {
	_, err := s.rooms.InsertOne(ctx, room)
	return err
}

func (s *MongoStore) GetRoom(ctx context.Context, id string) (*model.Room, error) {
	var room model.Room
	if err := s.rooms.FindOne(ctx, bson.M{"_id": id}).Decode(&room); err != nil {
		return nil, fmt.Errorf("room %q not found: %w", id, err)
	}
	return &room, nil
}

func (s *MongoStore) ListRooms(ctx context.Context) ([]model.Room, error) {
	cursor, err := s.rooms.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	var rooms []model.Room
	if err := cursor.All(ctx, &rooms); err != nil {
		return nil, err
	}
	return rooms, nil
}

func (s *MongoStore) GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"userId": userID, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		return nil, fmt.Errorf("subscription not found: %w", err)
	}
	return &sub, nil
}

func (s *MongoStore) CreateSubscription(ctx context.Context, sub model.Subscription) error {
	_, err := s.subscriptions.InsertOne(ctx, sub)
	return err
}
```

- [ ] **Step 2: Verify compiles** — `go build ./room-gatekeeper/`

- [ ] **Step 3: Commit**

```bash
git add room-gatekeeper/store_mongo.go && git commit -m "feat(room-gatekeeper): add MongoDB store implementation"
```

---

### Task 4: `main.go` + `Dockerfile`

- [ ] **Step 1: Create `room-gatekeeper/main.go`**

```go
package main

import (
	"context"
	"log"
	"os"
	"strconv"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	natsURL := envOr("NATS_URL", nats.DefaultURL)
	siteID := envOr("SITE_ID", "site-local")
	mongoURI := envOr("MONGO_URI", "mongodb://localhost:27017")
	mongoDB := envOr("MONGO_DB", "chat")
	maxRoomSize, _ := strconv.Atoi(envOr("MAX_ROOM_SIZE", "1000"))

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
	db := mongoClient.Database(mongoDB)

	// Ensure ROOMS stream exists
	streamCfg := stream.Rooms(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: streamCfg.Name, Subjects: streamCfg.Subjects,
	})
	if err != nil {
		log.Fatalf("create stream: %v", err)
	}

	store := NewMongoStore(db)
	handler := NewHandler(store, siteID, maxRoomSize, func(data []byte) error {
		_, err := js.Publish(ctx, subject.MemberInviteWildcard(siteID), data)
		return err
	})

	// Register CRUD handlers
	if err := handler.RegisterCRUD(nc); err != nil {
		log.Fatalf("register: %v", err)
	}

	// Subscribe to invite requests
	inviteSubj := subject.MemberInviteWildcard(siteID)
	if _, err := nc.Subscribe(inviteSubj, handler.NatsHandleInvite); err != nil {
		log.Fatalf("subscribe invite: %v", err)
	}

	log.Printf("room-gatekeeper running (site=%s)", siteID)

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

- [ ] **Step 2: Create `room-gatekeeper/Dockerfile`**

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY room-gatekeeper/ room-gatekeeper/
RUN CGO_ENABLED=0 go build -o /room-gatekeeper ./room-gatekeeper/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /room-gatekeeper /room-gatekeeper
ENTRYPOINT ["/room-gatekeeper"]
```

- [ ] **Step 3: Verify compiles** — `go build ./room-gatekeeper/`

- [ ] **Step 4: Commit**

```bash
git add room-gatekeeper/main.go room-gatekeeper/Dockerfile && git commit -m "feat(room-gatekeeper): add main entry point and Dockerfile"
```
