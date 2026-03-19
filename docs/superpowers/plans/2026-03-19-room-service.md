# Room Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create an independently deployable Room Service microservice that manages chat room lifecycle (create, list, get) over NATS request/reply.

**Architecture:** The Room Service lives under `services/room-service/` with its own `main.go`, handler, and in-memory store. It imports shared types from `pkg/models` and `pkg/subjects`. It subscribes to `chat.rooms.create` and `chat.rooms.list` NATS subjects. The store implements only room-related operations. A `RoomStore` interface is defined locally for the service since it only needs room operations, not the full `Store` interface.

**Tech Stack:** Go 1.24.7, NATS (`github.com/nats-io/nats.go v1.49.0`), shared `pkg/` library

**Depends on:** [Shared Library plan](2026-03-19-shared-library.md) must be completed first.

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `services/room-service/main.go` | Entry point: connect to NATS, start handler, graceful shutdown |
| Create | `services/room-service/handler/handler.go` | Subscribe to `chat.rooms.*` subjects, handle create/list room |
| Create | `services/room-service/handler/handler_test.go` | Unit tests for handler using mock store |
| Create | `services/room-service/store/store.go` | `RoomStore` interface definition |
| Create | `services/room-service/store/memory.go` | In-memory implementation of `RoomStore` |
| Create | `services/room-service/store/memory_test.go` | Unit tests for in-memory room store |

---

### Task 0: Add embedded NATS server test dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the nats-server test dependency**

Run: `cd /home/user/chat && go get github.com/nats-io/nats-server/v2@latest`
Expected: `go.mod` and `go.sum` updated with the new dependency

- [ ] **Step 2: Verify the dependency resolves**

Run: `cd /home/user/chat && go mod tidy`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add nats-server/v2 test dependency for embedded NATS"
```

---

### Task 1: Define `RoomStore` interface

**Files:**
- Create: `services/room-service/store/store.go`

- [ ] **Step 1: Create the RoomStore interface**

```go
package store

import "github.com/hmchangw/chat/pkg/models"

// RoomStore defines the interface for room data persistence.
type RoomStore interface {
	CreateRoom(name, createdBy string) (models.Room, error)
	ListRooms() ([]models.Room, error)
	GetRoom(id string) (models.Room, error)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/user/chat && go build ./services/room-service/store/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add services/room-service/store/store.go
git commit -m "feat(room-service): define RoomStore interface"
```

---

### Task 2: Implement in-memory room store with tests

**Files:**
- Create: `services/room-service/store/memory_test.go`
- Create: `services/room-service/store/memory.go`

- [ ] **Step 1: Write the failing tests**

```go
package store_test

import (
	"testing"

	"github.com/hmchangw/chat/services/room-service/store"
)

func TestCreateRoom(t *testing.T) {
	s := store.NewMemory()
	room, err := s.CreateRoom("general", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if room.Name != "general" {
		t.Errorf("name = %q, want %q", room.Name, "general")
	}
	if room.CreatedBy != "user-1" {
		t.Errorf("created_by = %q, want %q", room.CreatedBy, "user-1")
	}
	if room.ID == "" {
		t.Error("expected non-empty ID")
	}
	if room.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestCreateRoomUniqueIDs(t *testing.T) {
	s := store.NewMemory()
	r1, _ := s.CreateRoom("a", "user-1")
	r2, _ := s.CreateRoom("b", "user-1")
	if r1.ID == r2.ID {
		t.Errorf("rooms should have unique IDs, both got %q", r1.ID)
	}
}

func TestListRooms(t *testing.T) {
	s := store.NewMemory()
	s.CreateRoom("general", "user-1")
	s.CreateRoom("random", "user-2")

	rooms, err := s.ListRooms()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rooms) != 2 {
		t.Errorf("got %d rooms, want 2", len(rooms))
	}
}

func TestListRoomsEmpty(t *testing.T) {
	s := store.NewMemory()
	rooms, err := s.ListRooms()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rooms) != 0 {
		t.Errorf("got %d rooms, want 0", len(rooms))
	}
}

func TestGetRoom(t *testing.T) {
	s := store.NewMemory()
	created, _ := s.CreateRoom("general", "user-1")

	got, err := s.GetRoom(created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("got ID %q, want %q", got.ID, created.ID)
	}
}

func TestGetRoomNotFound(t *testing.T) {
	s := store.NewMemory()
	_, err := s.GetRoom("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent room")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/chat && go test ./services/room-service/store/ -v`
Expected: FAIL — `NewMemory` not defined

- [ ] **Step 3: Implement the in-memory room store**

```go
package store

import (
	"fmt"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/models"
)

// Memory is an in-memory implementation of RoomStore.
type Memory struct {
	mu     sync.RWMutex
	rooms  map[string]models.Room
	nextID int
}

func NewMemory() *Memory {
	return &Memory{
		rooms: make(map[string]models.Room),
	}
}

func (m *Memory) genID() string {
	m.nextID++
	return fmt.Sprintf("%d", m.nextID)
}

func (m *Memory) CreateRoom(name, createdBy string) (models.Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	room := models.Room{
		ID:        m.genID(),
		Name:      name,
		CreatedBy: createdBy,
		CreatedAt: time.Now().UTC(),
	}
	m.rooms[room.ID] = room
	return room, nil
}

func (m *Memory) ListRooms() ([]models.Room, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rooms := make([]models.Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	return rooms, nil
}

func (m *Memory) GetRoom(id string) (models.Room, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	room, ok := m.rooms[id]
	if !ok {
		return models.Room{}, fmt.Errorf("room %q not found", id)
	}
	return room, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/user/chat && go test ./services/room-service/store/ -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add services/room-service/store/memory.go services/room-service/store/memory_test.go
git commit -m "feat(room-service): implement in-memory room store with tests"
```

---

### Task 3: Implement room handler with tests

**Files:**
- Create: `services/room-service/handler/handler_test.go`
- Create: `services/room-service/handler/handler.go`

- [ ] **Step 1: Write the failing tests**

The handler tests use a mock store to avoid NATS dependency in unit tests.

```go
package handler_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/models"
	"github.com/hmchangw/chat/services/room-service/handler"
	"github.com/nats-io/nats.go"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
)

// startTestNATS starts an embedded NATS server for testing.
func startTestNATS(t *testing.T) (*natsserver.Server, *nats.Conn) {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1 // random port
	ns := natstest.RunServer(&opts)

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		ns.Shutdown()
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		nc.Close()
		ns.Shutdown()
	})
	return ns, nc
}

type mockStore struct {
	rooms []models.Room
}

func (m *mockStore) CreateRoom(name, createdBy string) (models.Room, error) {
	room := models.Room{
		ID:        "mock-1",
		Name:      name,
		CreatedBy: createdBy,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	m.rooms = append(m.rooms, room)
	return room, nil
}

func (m *mockStore) ListRooms() ([]models.Room, error) {
	return m.rooms, nil
}

func (m *mockStore) GetRoom(id string) (models.Room, error) {
	for _, r := range m.rooms {
		if r.ID == id {
			return r, nil
		}
	}
	return models.Room{}, fmt.Errorf("room %q not found", id)
}

func TestHandleCreateRoom(t *testing.T) {
	_, nc := startTestNATS(t)
	ms := &mockStore{}
	h := handler.New(nc, ms)
	if err := h.Register(); err != nil {
		t.Fatalf("register: %v", err)
	}

	reqData, _ := json.Marshal(models.CreateRoomRequest{Name: "general", CreatedBy: "user-1"})
	resp, err := nc.Request("chat.rooms.create", reqData, 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var room models.Room
	if err := json.Unmarshal(resp.Data, &room); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if room.Name != "general" {
		t.Errorf("name = %q, want %q", room.Name, "general")
	}
}

func TestHandleListRooms(t *testing.T) {
	_, nc := startTestNATS(t)
	ms := &mockStore{
		rooms: []models.Room{
			{ID: "1", Name: "general"},
			{ID: "2", Name: "random"},
		},
	}
	h := handler.New(nc, ms)
	if err := h.Register(); err != nil {
		t.Fatalf("register: %v", err)
	}

	resp, err := nc.Request("chat.rooms.list", nil, 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var listResp models.ListRoomsResponse
	if err := json.Unmarshal(resp.Data, &listResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listResp.Rooms) != 2 {
		t.Errorf("got %d rooms, want 2", len(listResp.Rooms))
	}
}
```

**Note:** The handler tests require the embedded NATS server. Add this dependency:

Run: `cd /home/user/chat && go get github.com/nats-io/nats-server/v2@latest`

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/chat && go test ./services/room-service/handler/ -v`
Expected: FAIL — handler package not found

- [ ] **Step 3: Implement the handler**

```go
package handler

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/hmchangw/chat/pkg/models"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subjects"
	"github.com/hmchangw/chat/services/room-service/store"
	"github.com/nats-io/nats.go"
)

// Handler processes NATS messages for room operations.
type Handler struct {
	store store.RoomStore
	nc    *nats.Conn
}

func New(nc *nats.Conn, s store.RoomStore) *Handler {
	return &Handler{nc: nc, store: s}
}

// Register subscribes to room-related NATS subjects.
func (h *Handler) Register() error {
	subs := map[string]nats.MsgHandler{
		subjects.RoomsCreate: h.createRoom,
		subjects.RoomsList:   h.listRooms,
	}
	for subj, handler := range subs {
		if _, err := h.nc.Subscribe(subj, handler); err != nil {
			return fmt.Errorf("subscribe %s: %w", subj, err)
		}
		log.Printf("subscribed to %s", subj)
	}
	return nil
}

func (h *Handler) createRoom(msg *nats.Msg) {
	var req models.CreateRoomRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		natsutil.ReplyError(msg, "invalid request: "+err.Error())
		return
	}

	room, err := h.store.CreateRoom(req.Name, req.CreatedBy)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	natsutil.ReplyJSON(msg, room)
}

func (h *Handler) listRooms(msg *nats.Msg) {
	rooms, err := h.store.ListRooms()
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	natsutil.ReplyJSON(msg, models.ListRoomsResponse{Rooms: rooms})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/user/chat && go test ./services/room-service/handler/ -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add services/room-service/handler/
git commit -m "feat(room-service): implement room handler with NATS subscriptions"
```

---

### Task 4: Create Room Service `main.go`

**Files:**
- Create: `services/room-service/main.go`

- [ ] **Step 1: Write the entry point**

```go
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hmchangw/chat/services/room-service/handler"
	"github.com/hmchangw/chat/services/room-service/store"
	"github.com/nats-io/nats.go"
)

func main() {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = nats.DefaultURL
	}

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("failed to connect to NATS at %s: %v", natsURL, err)
	}
	log.Printf("connected to NATS at %s", natsURL)

	mem := store.NewMemory()
	h := handler.New(nc, mem)

	if err := h.Register(); err != nil {
		log.Fatalf("failed to register handlers: %v", err)
	}

	log.Println("room-service running, press Ctrl+C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	nc.Drain()
	log.Println("room-service shut down")
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/user/chat && go build ./services/room-service/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add services/room-service/main.go
git commit -m "feat(room-service): add main entry point"
```

---

### Task 5: Run all Room Service tests end-to-end

- [ ] **Step 1: Run all room-service tests**

Run: `cd /home/user/chat && go test ./services/room-service/... -v`
Expected: all PASS

- [ ] **Step 2: Verify build**

Run: `cd /home/user/chat && go build ./services/room-service/`
Expected: no errors

- [ ] **Step 3: Commit (if any fixes were needed)**

```bash
git add -A services/room-service/
git commit -m "fix(room-service): address test/build issues"
```
