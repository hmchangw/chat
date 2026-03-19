# Message Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create an independently deployable Message Service microservice that manages chat messages (send, list) and broadcasts new messages to room subscribers over NATS pub/sub.

**Architecture:** The Message Service lives under `services/message-service/` with its own `main.go`, handler, and in-memory store. It imports shared types from `pkg/models`, `pkg/subjects`, and `pkg/natsutil`. It subscribes to `chat.messages.send` and `chat.messages.list` NATS subjects. When a message is sent, the handler broadcasts it to `chat.room.{roomID}` for real-time delivery to room subscribers. The Message Service needs to verify room existence — it does this by making a NATS request to the Room Service via `chat.rooms.list` or by accepting the room ID on trust (simpler, chosen here). Room validation is deferred to the Room Service or a future API gateway.

**Tech Stack:** Go 1.24.7, NATS (`github.com/nats-io/nats.go v1.49.0`), shared `pkg/` library

**Important behavioral change:** The existing monolith validates room existence before sending a message (`store/memory.go:80-82`). In this microservices design, the Message Service does **not** validate room existence at the store level — it trusts the room ID. Room validation should be handled upstream by an API gateway or by making a NATS request to the Room Service. This simplifies the Message Service and avoids cross-service coupling at the store layer.

**Depends on:** [Shared Library plan](2026-03-19-shared-library.md) must be completed first.

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `services/message-service/main.go` | Entry point: connect to NATS, start handler, graceful shutdown |
| Create | `services/message-service/handler/handler.go` | Subscribe to `chat.messages.*` subjects, handle send/list, broadcast to room |
| Create | `services/message-service/handler/handler_test.go` | Unit tests for handler using embedded NATS server |
| Create | `services/message-service/store/store.go` | `MessageStore` interface definition |
| Create | `services/message-service/store/memory.go` | In-memory implementation of `MessageStore` |
| Create | `services/message-service/store/memory_test.go` | Unit tests for in-memory message store |

---

### Task 1: Define `MessageStore` interface

**Files:**
- Create: `services/message-service/store/store.go`

- [ ] **Step 1: Create the MessageStore interface**

```go
package store

import "github.com/hmchangw/chat/pkg/models"

// MessageStore defines the interface for message data persistence.
type MessageStore interface {
	SendMessage(roomID, userID, content string) (models.Message, error)
	ListMessages(roomID string, limit int) ([]models.Message, error)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/user/chat && go build ./services/message-service/store/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add services/message-service/store/store.go
git commit -m "feat(message-service): define MessageStore interface"
```

---

### Task 2: Implement in-memory message store with tests

**Files:**
- Create: `services/message-service/store/memory_test.go`
- Create: `services/message-service/store/memory.go`

- [ ] **Step 1: Write the failing tests**

```go
package store_test

import (
	"testing"

	"github.com/hmchangw/chat/services/message-service/store"
)

func TestSendMessage(t *testing.T) {
	s := store.NewMemory()
	msg, err := s.SendMessage("room-1", "user-1", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.RoomID != "room-1" {
		t.Errorf("room_id = %q, want %q", msg.RoomID, "room-1")
	}
	if msg.UserID != "user-1" {
		t.Errorf("user_id = %q, want %q", msg.UserID, "user-1")
	}
	if msg.Content != "hello" {
		t.Errorf("content = %q, want %q", msg.Content, "hello")
	}
	if msg.ID == "" {
		t.Error("expected non-empty ID")
	}
	if msg.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestSendMessageUniqueIDs(t *testing.T) {
	s := store.NewMemory()
	m1, _ := s.SendMessage("room-1", "user-1", "a")
	m2, _ := s.SendMessage("room-1", "user-1", "b")
	if m1.ID == m2.ID {
		t.Errorf("messages should have unique IDs, both got %q", m1.ID)
	}
}

func TestListMessages(t *testing.T) {
	s := store.NewMemory()
	s.SendMessage("room-1", "user-1", "first")
	s.SendMessage("room-1", "user-2", "second")
	s.SendMessage("room-1", "user-1", "third")

	msgs, err := s.ListMessages("room-1", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("got %d messages, want 3", len(msgs))
	}
}

func TestListMessagesWithLimit(t *testing.T) {
	s := store.NewMemory()
	s.SendMessage("room-1", "user-1", "first")
	s.SendMessage("room-1", "user-1", "second")
	s.SendMessage("room-1", "user-1", "third")

	msgs, err := s.ListMessages("room-1", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("got %d messages, want 2", len(msgs))
	}
	// Should return the last 2 messages
	if msgs[0].Content != "second" {
		t.Errorf("first message content = %q, want %q", msgs[0].Content, "second")
	}
	if msgs[1].Content != "third" {
		t.Errorf("second message content = %q, want %q", msgs[1].Content, "third")
	}
}

func TestListMessagesEmpty(t *testing.T) {
	s := store.NewMemory()
	msgs, err := s.ListMessages("room-1", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("got %d messages, want 0", len(msgs))
	}
}

func TestListMessagesDifferentRooms(t *testing.T) {
	s := store.NewMemory()
	s.SendMessage("room-1", "user-1", "in room 1")
	s.SendMessage("room-2", "user-1", "in room 2")

	msgs, err := s.ListMessages("room-1", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("got %d messages for room-1, want 1", len(msgs))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/chat && go test ./services/message-service/store/ -v`
Expected: FAIL — `NewMemory` not defined

- [ ] **Step 3: Implement the in-memory message store**

```go
package store

import (
	"fmt"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/models"
)

// Memory is an in-memory implementation of MessageStore.
type Memory struct {
	mu       sync.RWMutex
	messages map[string][]models.Message // roomID -> messages
	nextID   int
}

func NewMemory() *Memory {
	return &Memory{
		messages: make(map[string][]models.Message),
	}
}

func (m *Memory) genID() string {
	m.nextID++
	return fmt.Sprintf("%d", m.nextID)
}

func (m *Memory) SendMessage(roomID, userID, content string) (models.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	msg := models.Message{
		ID:        m.genID(),
		RoomID:    roomID,
		UserID:    userID,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	m.messages[roomID] = append(m.messages[roomID], msg)
	return msg, nil
}

func (m *Memory) ListMessages(roomID string, limit int) ([]models.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	msgs := m.messages[roomID]
	if limit > 0 && limit < len(msgs) {
		msgs = msgs[len(msgs)-limit:]
	}
	result := make([]models.Message, len(msgs))
	copy(result, msgs)
	return result, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/user/chat && go test ./services/message-service/store/ -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add services/message-service/store/memory.go services/message-service/store/memory_test.go
git commit -m "feat(message-service): implement in-memory message store with tests"
```

---

### Task 3: Implement message handler with tests

**Files:**
- Create: `services/message-service/handler/handler_test.go`
- Create: `services/message-service/handler/handler.go`

- [ ] **Step 1: Write the failing tests**

```go
package handler_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/models"
	"github.com/hmchangw/chat/pkg/subjects"
	"github.com/hmchangw/chat/services/message-service/handler"
	"github.com/nats-io/nats.go"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
)

func startTestNATS(t *testing.T) (*natsserver.Server, *nats.Conn) {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
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
	messages map[string][]models.Message
	nextID   int
}

func newMockStore() *mockStore {
	return &mockStore{messages: make(map[string][]models.Message)}
}

func (m *mockStore) SendMessage(roomID, userID, content string) (models.Message, error) {
	m.nextID++
	msg := models.Message{
		ID:        fmt.Sprintf("mock-%d", m.nextID),
		RoomID:    roomID,
		UserID:    userID,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	m.messages[roomID] = append(m.messages[roomID], msg)
	return msg, nil
}

func (m *mockStore) ListMessages(roomID string, limit int) ([]models.Message, error) {
	msgs := m.messages[roomID]
	if limit > 0 && limit < len(msgs) {
		msgs = msgs[len(msgs)-limit:]
	}
	result := make([]models.Message, len(msgs))
	copy(result, msgs)
	return result, nil
}

func TestHandleSendMessage(t *testing.T) {
	_, nc := startTestNATS(t)
	ms := newMockStore()
	h := handler.New(nc, ms)
	if err := h.Register(); err != nil {
		t.Fatalf("register: %v", err)
	}

	reqData, _ := json.Marshal(models.SendMessageRequest{
		RoomID:  "room-1",
		UserID:  "user-1",
		Content: "hello world",
	})
	resp, err := nc.Request(subjects.MessagesSend, reqData, 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var msg models.Message
	if err := json.Unmarshal(resp.Data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Content != "hello world" {
		t.Errorf("content = %q, want %q", msg.Content, "hello world")
	}
	if msg.RoomID != "room-1" {
		t.Errorf("room_id = %q, want %q", msg.RoomID, "room-1")
	}
}

func TestHandleSendMessageBroadcasts(t *testing.T) {
	_, nc := startTestNATS(t)
	ms := newMockStore()
	h := handler.New(nc, ms)
	if err := h.Register(); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Subscribe to the room broadcast subject
	broadcastCh := make(chan *nats.Msg, 1)
	sub, err := nc.Subscribe(subjects.RoomBroadcast("room-1"), func(msg *nats.Msg) {
		broadcastCh <- msg
	})
	if err != nil {
		t.Fatalf("subscribe broadcast: %v", err)
	}
	defer sub.Unsubscribe()
	nc.Flush()

	reqData, _ := json.Marshal(models.SendMessageRequest{
		RoomID:  "room-1",
		UserID:  "user-1",
		Content: "broadcast me",
	})
	_, err = nc.Request(subjects.MessagesSend, reqData, 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	// Wait for broadcast
	select {
	case bmsg := <-broadcastCh:
		var got models.Message
		if err := json.Unmarshal(bmsg.Data, &got); err != nil {
			t.Fatalf("unmarshal broadcast: %v", err)
		}
		if got.Content != "broadcast me" {
			t.Errorf("broadcast content = %q, want %q", got.Content, "broadcast me")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for broadcast")
	}
}

func TestHandleListMessages(t *testing.T) {
	_, nc := startTestNATS(t)
	ms := newMockStore()
	ms.SendMessage("room-1", "user-1", "first")
	ms.SendMessage("room-1", "user-2", "second")

	h := handler.New(nc, ms)
	if err := h.Register(); err != nil {
		t.Fatalf("register: %v", err)
	}

	reqData, _ := json.Marshal(models.ListMessagesRequest{RoomID: "room-1"})
	resp, err := nc.Request(subjects.MessagesList, reqData, 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var listResp models.ListMessagesResponse
	if err := json.Unmarshal(resp.Data, &listResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listResp.Messages) != 2 {
		t.Errorf("got %d messages, want 2", len(listResp.Messages))
	}
}
```

**Note:** Handler tests need the embedded NATS server dependency (same as Room Service). If not already added:

Run: `cd /home/user/chat && go get github.com/nats-io/nats-server/v2@latest`

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/chat && go test ./services/message-service/handler/ -v`
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
	"github.com/hmchangw/chat/services/message-service/store"
	"github.com/nats-io/nats.go"
)

// Handler processes NATS messages for message operations.
type Handler struct {
	store store.MessageStore
	nc    *nats.Conn
}

func New(nc *nats.Conn, s store.MessageStore) *Handler {
	return &Handler{nc: nc, store: s}
}

// Register subscribes to message-related NATS subjects.
func (h *Handler) Register() error {
	subs := map[string]nats.MsgHandler{
		subjects.MessagesSend: h.sendMessage,
		subjects.MessagesList: h.listMessages,
	}
	for subj, handler := range subs {
		if _, err := h.nc.Subscribe(subj, handler); err != nil {
			return fmt.Errorf("subscribe %s: %w", subj, err)
		}
		log.Printf("subscribed to %s", subj)
	}
	return nil
}

func (h *Handler) sendMessage(msg *nats.Msg) {
	var req models.SendMessageRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		natsutil.ReplyError(msg, "invalid request: "+err.Error())
		return
	}

	chatMsg, err := h.store.SendMessage(req.RoomID, req.UserID, req.Content)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}

	// Reply to sender
	natsutil.ReplyJSON(msg, chatMsg)

	// Broadcast to room subscribers
	data, _ := json.Marshal(chatMsg)
	subject := subjects.RoomBroadcast(req.RoomID)
	if err := h.nc.Publish(subject, data); err != nil {
		log.Printf("broadcast to %s failed: %v", subject, err)
	}
}

func (h *Handler) listMessages(msg *nats.Msg) {
	var req models.ListMessagesRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		natsutil.ReplyError(msg, "invalid request: "+err.Error())
		return
	}

	messages, err := h.store.ListMessages(req.RoomID, req.Limit)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	natsutil.ReplyJSON(msg, models.ListMessagesResponse{Messages: messages})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/user/chat && go test ./services/message-service/handler/ -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add services/message-service/handler/
git commit -m "feat(message-service): implement message handler with broadcast"
```

---

### Task 4: Create Message Service `main.go`

**Files:**
- Create: `services/message-service/main.go`

- [ ] **Step 1: Write the entry point**

```go
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hmchangw/chat/services/message-service/handler"
	"github.com/hmchangw/chat/services/message-service/store"
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

	log.Println("message-service running, press Ctrl+C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	nc.Drain()
	log.Println("message-service shut down")
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/user/chat && go build ./services/message-service/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add services/message-service/main.go
git commit -m "feat(message-service): add main entry point"
```

---

### Task 5: Run all Message Service tests end-to-end

- [ ] **Step 1: Run all message-service tests**

Run: `cd /home/user/chat && go test ./services/message-service/... -v`
Expected: all PASS

- [ ] **Step 2: Verify build**

Run: `cd /home/user/chat && go build ./services/message-service/`
Expected: no errors

- [ ] **Step 3: Run all tests across the entire repo**

Run: `cd /home/user/chat && go test ./... -v`
Expected: all PASS

- [ ] **Step 4: Commit (if any fixes were needed)**

```bash
git add -A services/message-service/
git commit -m "fix(message-service): address test/build issues"
```

---

### Task 6: Clean up old monolith code

After both services are working, remove the original monolith files that have been superseded.

- [ ] **Step 1: Remove old monolith files**

Delete these files which are now replaced by the microservices:
- `model/model.go` → replaced by `pkg/models/*`
- `store/memory.go` → replaced by per-service stores
- `handler/handler.go` → replaced by per-service handlers
- `server/server.go` → replaced by per-service `main.go`
- `main.go` → replaced by per-service `main.go`

```bash
git rm model/model.go store/memory.go handler/handler.go server/server.go main.go
```

- [ ] **Step 2: Verify nothing is broken**

Run: `cd /home/user/chat && go test ./... -v`
Expected: all PASS (only `pkg/` and `services/` tests remain)

- [ ] **Step 3: Commit**

```bash
git commit -m "refactor: remove monolith code replaced by microservices"
```
