# Plan 9: History Service

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create the history service that handles paginated message history requests. Verifies subscription, filters by `sharedHistorySince`, and queries Cassandra for messages.

**Tech Stack:** Go 1.25, NATS, MongoDB (subscriptions), Cassandra (messages), `pkg/model`, `pkg/subject`, `pkg/natsutil`, `pkg/mongoutil`, `pkg/cassutil`, `pkg/shutdown`

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `history-service/store.go` | `HistoryStore` interface + in-memory implementation |
| Create | `history-service/store_test.go` | Store tests (pagination, sharedHistorySince filtering) |
| Create | `history-service/store_real.go` | Real MongoDB + Cassandra implementation |
| Create | `history-service/handler.go` | NATS request/reply handler |
| Create | `history-service/handler_test.go` | Handler tests |
| Create | `history-service/main.go` | Entry point |
| Create | `history-service/Dockerfile` | Multi-stage Docker build |

---

### Task 1: `store.go` + `store_test.go`

- [ ] **Step 1: Write tests** — `history-service/store_test.go`

```go
package main

import (
	"context"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

func TestMemoryStore_ListMessages_Pagination(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		s.messages = append(s.messages, model.Message{
			ID: fmt.Sprintf("m%d", i), RoomID: "r1", UserID: "u1",
			Content:   fmt.Sprintf("msg-%d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}

	// Get last 3 messages (before now, since epoch)
	msgs, err := s.ListMessages(ctx, "r1", time.Time{}, time.Now(), 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	// Should be most recent first
	if msgs[0].Content != "msg-9" {
		t.Errorf("first message = %q, want msg-9", msgs[0].Content)
	}
}

func TestMemoryStore_ListMessages_SharedHistorySince(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		s.messages = append(s.messages, model.Message{
			ID: fmt.Sprintf("m%d", i), RoomID: "r1",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}

	// Since = 5 minutes after base → should only get messages 5-9
	since := base.Add(5 * time.Minute)
	msgs, err := s.ListMessages(ctx, "r1", since, time.Now(), 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("got %d messages, want 5", len(msgs))
	}
}

func TestMemoryStore_GetSubscription(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	s.subscriptions = append(s.subscriptions, model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleMember,
		SharedHistorySince: time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC),
	})

	sub, err := s.GetSubscription(ctx, "u1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.UserID != "u1" {
		t.Errorf("UserID = %q", sub.UserID)
	}

	_, err = s.GetSubscription(ctx, "u2", "r1")
	if err == nil {
		t.Error("expected error for missing subscription")
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Create `history-service/store.go`**

```go
package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

type HistoryStore interface {
	GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
	ListMessages(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error)
}

type MemoryStore struct {
	subscriptions []model.Subscription
	messages      []model.Message
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

func (s *MemoryStore) ListMessages(_ context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error) {
	var filtered []model.Message
	for _, m := range s.messages {
		if m.RoomID != roomID {
			continue
		}
		if !since.IsZero() && m.CreatedAt.Before(since) {
			continue
		}
		if !before.IsZero() && !m.CreatedAt.Before(before) {
			continue
		}
		filtered = append(filtered, m)
	}

	// Sort descending by CreatedAt
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}
```

- [ ] **Step 4: Run tests — expect PASS**

- [ ] **Step 5: Commit**

```bash
git add history-service/store.go history-service/store_test.go && git commit -m "feat(history-service): add HistoryStore interface and in-memory implementation"
```

---

### Task 2: `handler.go` + `handler_test.go`

- [ ] **Step 1: Write tests** — `history-service/handler_test.go`

```go
package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_HandleHistory_Success(t *testing.T) {
	store := NewMemoryStore()
	joinTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	store.subscriptions = append(store.subscriptions, model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleMember,
		SharedHistorySince: joinTime,
	})

	base := joinTime
	for i := 0; i < 5; i++ {
		store.messages = append(store.messages, model.Message{
			ID: fmt.Sprintf("m%d", i), RoomID: "r1", UserID: "u1",
			Content:   fmt.Sprintf("msg-%d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}

	h := &Handler{store: store}

	req := model.HistoryRequest{RoomID: "r1", Limit: 3}
	data, _ := json.Marshal(req)

	resp, err := h.handleHistory("u1", "r1", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var histResp model.HistoryResponse
	json.Unmarshal(resp, &histResp)
	if len(histResp.Messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(histResp.Messages))
	}
	if !histResp.HasMore {
		t.Error("HasMore should be true")
	}
}

func TestHandler_HandleHistory_NotSubscribed(t *testing.T) {
	store := NewMemoryStore()
	h := &Handler{store: store}

	req := model.HistoryRequest{RoomID: "r1", Limit: 10}
	data, _ := json.Marshal(req)

	_, err := h.handleHistory("u1", "r1", data)
	if err == nil {
		t.Fatal("expected error for unsubscribed user")
	}
}

func TestHandler_HandleHistory_SharedHistorySinceFilter(t *testing.T) {
	store := NewMemoryStore()
	joinTime := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)

	store.subscriptions = append(store.subscriptions, model.Subscription{
		UserID: "u1", RoomID: "r1", SharedHistorySince: joinTime,
	})

	// Messages before and after join
	store.messages = append(store.messages,
		model.Message{ID: "m1", RoomID: "r1", CreatedAt: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)}, // before join
		model.Message{ID: "m2", RoomID: "r1", CreatedAt: time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC)}, // after join
		model.Message{ID: "m3", RoomID: "r1", CreatedAt: time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)}, // after join
	)

	h := &Handler{store: store}
	req := model.HistoryRequest{RoomID: "r1", Limit: 100}
	data, _ := json.Marshal(req)

	resp, err := h.handleHistory("u1", "r1", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var histResp model.HistoryResponse
	json.Unmarshal(resp, &histResp)
	if len(histResp.Messages) != 2 {
		t.Fatalf("got %d messages, want 2 (only after join)", len(histResp.Messages))
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Create `history-service/handler.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/nats-io/nats.go"
)

type Handler struct {
	store HistoryStore
}

func NewHandler(store HistoryStore) *Handler {
	return &Handler{store: store}
}

// NatsHandleHistory handles NATS request/reply for message history.
// Subject: chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history
func (h *Handler) NatsHandleHistory(msg *nats.Msg) {
	parts := strings.Split(msg.Subject, ".")
	if len(parts) < 8 {
		natsutil.ReplyError(msg, "invalid subject")
		return
	}
	userID := parts[2]
	roomID := parts[5]

	resp, err := h.handleHistory(userID, roomID, msg.Data)
	if err != nil {
		natsutil.ReplyError(msg, err.Error())
		return
	}
	msg.Respond(resp)
}

func (h *Handler) handleHistory(userID, roomID string, data []byte) ([]byte, error) {
	var req model.HistoryRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	ctx := context.Background()

	// Verify subscription
	sub, err := h.store.GetSubscription(ctx, userID, roomID)
	if err != nil {
		return nil, fmt.Errorf("not subscribed: %w", err)
	}

	since := sub.SharedHistorySince
	before := time.Now().UTC()

	// If "before" cursor is provided, parse it as a timestamp
	if req.Before != "" {
		parsed, err := time.Parse(time.RFC3339Nano, req.Before)
		if err == nil {
			before = parsed
		}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}

	// Fetch limit+1 to determine HasMore
	msgs, err := h.store.ListMessages(ctx, roomID, since, before, limit+1)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}

	hasMore := len(msgs) > limit
	if hasMore {
		msgs = msgs[:limit]
	}

	resp := model.HistoryResponse{
		Messages: msgs,
		HasMore:  hasMore,
	}
	return json.Marshal(resp)
}
```

- [ ] **Step 4: Run tests — expect PASS**

- [ ] **Step 5: Commit**

```bash
git add history-service/handler.go history-service/handler_test.go && git commit -m "feat(history-service): add history request handler"
```

---

### Task 3: `store_real.go` — MongoDB + Cassandra implementation

- [ ] **Step 1: Create `history-service/store_real.go`**

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

type RealStore struct {
	subscriptions *mongo.Collection
	cassSession   *gocql.Session
}

func NewRealStore(db *mongo.Database, cassSession *gocql.Session) *RealStore {
	return &RealStore{
		subscriptions: db.Collection("subscriptions"),
		cassSession:   cassSession,
	}
}

func (s *RealStore) GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"userId": userID, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		return nil, fmt.Errorf("subscription not found: %w", err)
	}
	return &sub, nil
}

func (s *RealStore) ListMessages(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error) {
	query := `SELECT id, room_id, user_id, content, created_at FROM messages
		WHERE room_id = ? AND created_at > ? AND created_at < ?
		ORDER BY created_at DESC LIMIT ?`

	iter := s.cassSession.Query(query, roomID, since, before, limit).WithContext(ctx).Iter()

	var messages []model.Message
	var msg model.Message
	for iter.Scan(&msg.ID, &msg.RoomID, &msg.UserID, &msg.Content, &msg.CreatedAt) {
		messages = append(messages, msg)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	return messages, nil
}
```

- [ ] **Step 2: Verify compiles** — `go build ./history-service/`

- [ ] **Step 3: Commit**

```bash
git add history-service/store_real.go && git commit -m "feat(history-service): add MongoDB + Cassandra store implementation"
```

---

### Task 4: `main.go` + `Dockerfile`

- [ ] **Step 1: Create `history-service/main.go`**

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
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/nats-io/nats.go"
)

func main() {
	natsURL := envOr("NATS_URL", nats.DefaultURL)
	siteID := envOr("SITE_ID", "site-local")
	mongoURI := envOr("MONGO_URI", "mongodb://localhost:27017")
	mongoDB := envOr("MONGO_DB", "chat")
	cassHosts := envOr("CASSANDRA_HOSTS", "localhost")
	cassKeyspace := envOr("CASSANDRA_KEYSPACE", "chat")

	ctx := context.Background()

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("nats: %v", err)
	}

	mongoClient, err := mongoutil.Connect(ctx, mongoURI)
	if err != nil {
		log.Fatalf("mongo: %v", err)
	}

	cassSession, err := cassutil.Connect(strings.Split(cassHosts, ","), cassKeyspace)
	if err != nil {
		log.Fatalf("cassandra: %v", err)
	}

	store := NewRealStore(mongoClient.Database(mongoDB), cassSession)
	handler := NewHandler(store)

	histSubj := subject.MsgHistoryWildcard(siteID)
	if _, err := nc.Subscribe(histSubj, handler.NatsHandleHistory); err != nil {
		log.Fatalf("subscribe: %v", err)
	}

	log.Printf("history-service running (site=%s)", siteID)

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

- [ ] **Step 2: Create `history-service/Dockerfile`**

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY history-service/ history-service/
RUN CGO_ENABLED=0 go build -o /history-service ./history-service/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /history-service /history-service
ENTRYPOINT ["/history-service"]
```

- [ ] **Step 3: Verify compiles** — `go build ./history-service/`

- [ ] **Step 4: Commit**

```bash
git add history-service/main.go history-service/Dockerfile && git commit -m "feat(history-service): add main entry point and Dockerfile"
```
