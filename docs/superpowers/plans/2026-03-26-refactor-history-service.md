# History-Service Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor history-service from a flat pre-generated structure into a well-organized Go service with `cmd/` + `internal/` layout, Cassandra pagination toolkit, MongoDB `Collection[T]` abstraction, and 4 NATS request/reply endpoints.

**Architecture:** Transport-agnostic service layer with repository interfaces. NATS handler bridges transport to service. Cassandra for message storage, MongoDB for subscription metadata. TDD throughout.

**Tech Stack:** Go 1.24, NATS, Cassandra (gocql), MongoDB (mongo-driver/v2), testcontainers-go, go.uber.org/mock, testify

**Spec:** `docs/superpowers/specs/2026-03-25-refactor-history-service-design.md`

---

## File Map

| File | Responsibility |
|------|---------------|
| `history-service/cmd/main.go` | Config, wiring, NATS subscriptions, graceful shutdown |
| `history-service/internal/config/config.go` | Config struct with embedded sub-configs |
| `history-service/internal/service/service.go` | Repository interfaces, HistoryService struct, constructor |
| `history-service/internal/service/messages.go` | 4 transport-agnostic handler methods |
| `history-service/internal/service/messages_test.go` | Unit tests with mocked repositories |
| `history-service/internal/service/mocks/mock_store.go` | Generated mocks (mockgen) |
| `history-service/internal/natshandler/handler.go` | NATSHandler struct, Register(), NATS-to-service glue |
| `history-service/internal/natshandler/utils.go` | Generic HandleRequest helper |
| `history-service/internal/natshandler/utils_test.go` | Unit tests for HandleRequest |
| `history-service/internal/cassrepo/utils.go` | Page[T], Query builder, ScanPage[T] |
| `history-service/internal/cassrepo/utils_test.go` | Unit tests for Query builder |
| `history-service/internal/cassrepo/repository.go` | CassandraRepository implementing MessageRepository |
| `history-service/internal/cassrepo/integration_test.go` | Integration tests with testcontainers |
| `history-service/internal/mongorepo/utils.go` | Generic Collection[T] |
| `history-service/internal/mongorepo/utils_test.go` | Unit tests for Collection[T] |
| `history-service/internal/mongorepo/repository.go` | MongoRepository implementing SubscriptionRepository |
| `history-service/internal/mongorepo/integration_test.go` | Integration tests with testcontainers |
| `pkg/model/history.go` | New request/response model types |
| `pkg/subject/subject.go` | New subject builders for next, surrounding, get |
| `Makefile` | Build path override for history-service |
| `history-service/deploy/Dockerfile` | Updated build path |

---

### Task 1: Scaffold — Delete old files, create directory structure, update build tooling

**Files:**
- Delete: `history-service/main.go`, `history-service/handler.go`, `history-service/store.go`, `history-service/store_real.go`, `history-service/handler_test.go`, `history-service/mock_store_test.go`, `history-service/integration_test.go`
- Create: directory structure under `history-service/cmd/` and `history-service/internal/`
- Modify: `Makefile`, `history-service/deploy/Dockerfile`

- [ ] **Step 1: Delete old history-service files**

```bash
rm history-service/main.go history-service/handler.go history-service/store.go history-service/store_real.go history-service/handler_test.go history-service/mock_store_test.go history-service/integration_test.go
```

- [ ] **Step 2: Create new directory structure**

```bash
mkdir -p history-service/cmd
mkdir -p history-service/internal/config
mkdir -p history-service/internal/service/mocks
mkdir -p history-service/internal/natshandler
mkdir -p history-service/internal/cassrepo
mkdir -p history-service/internal/mongorepo
```

- [ ] **Step 3: Update Makefile build target**

Replace the `build` target in `Makefile` with a per-service override for `history-service`:

```makefile
# Build a single service binary (requires SERVICE=<name>)
build:
ifndef SERVICE
	$(error SERVICE is required. Usage: make build SERVICE=<name>)
endif
ifeq ($(SERVICE),history-service)
	CGO_ENABLED=0 go build -o bin/$(SERVICE) ./$(SERVICE)/cmd/
else
	CGO_ENABLED=0 go build -o bin/$(SERVICE) ./$(SERVICE)/
endif
```

- [ ] **Step 4: Update Dockerfile build path**

Replace line 7 in `history-service/deploy/Dockerfile`:

```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY history-service/ history-service/
RUN CGO_ENABLED=0 go build -o /history-service ./history-service/cmd/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /history-service /history-service
ENTRYPOINT ["/history-service"]
```

- [ ] **Step 5: Commit scaffold**

```bash
git add -A history-service/ Makefile
git commit -m "refactor(history-service): scaffold new directory structure

Delete old flat files, create cmd/ + internal/ layout,
update Makefile and Dockerfile build paths."
```

---

### Task 2: Model types — Replace pkg/model/history.go

**Files:**
- Modify: `pkg/model/history.go`

- [ ] **Step 1: Replace history.go with new model types**

Write the complete file `pkg/model/history.go`:

```go
package model

// LoadHistoryRequest is the payload for loading message history before a timestamp.
type LoadHistoryRequest struct {
	RoomID   string `json:"roomId"   bson:"roomId"`
	Before   string `json:"before"   bson:"before"`   // RFC3339Nano cursor — fetch messages before this
	Limit    int    `json:"limit"    bson:"limit"`     // default 50
	LastSeen string `json:"lastSeen" bson:"lastSeen"`  // RFC3339Nano — last message seen by user
}

// LoadHistoryResponse is the response for LoadHistory.
type LoadHistoryResponse struct {
	Messages    []Message `json:"messages"              bson:"messages"`
	FirstUnread *Message  `json:"firstUnread,omitempty" bson:"firstUnread,omitempty"`
	HasMore     bool      `json:"hasMore"               bson:"hasMore"`
}

// LoadNextMessagesRequest is the payload for loading messages after a timestamp.
type LoadNextMessagesRequest struct {
	RoomID string `json:"roomId" bson:"roomId"`
	After  string `json:"after"  bson:"after"` // RFC3339Nano cursor — fetch messages after this (empty for latest)
	Limit  int    `json:"limit"  bson:"limit"` // default 50
}

// LoadNextMessagesResponse is the response for LoadNextMessages.
type LoadNextMessagesResponse struct {
	Messages []Message `json:"messages" bson:"messages"`
	HasMore  bool      `json:"hasMore"  bson:"hasMore"`
}

// LoadSurroundingMessagesRequest is the payload for loading messages around a central message.
type LoadSurroundingMessagesRequest struct {
	RoomID    string `json:"roomId"    bson:"roomId"`
	MessageID string `json:"messageId" bson:"messageId"` // central message ID
	Limit     int    `json:"limit"     bson:"limit"`     // total messages including central
}

// LoadSurroundingMessagesResponse contains messages before and after the central message.
type LoadSurroundingMessagesResponse struct {
	Before []Message `json:"before" bson:"before"`
	After  []Message `json:"after"  bson:"after"` // includes the central message
}

// GetMessageByIDRequest is the payload for fetching a single message.
type GetMessageByIDRequest struct {
	RoomID    string `json:"roomId"    bson:"roomId"`
	MessageID string `json:"messageId" bson:"messageId"`
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./pkg/model/...`
Expected: success, no errors

- [ ] **Step 3: Commit model types**

```bash
git add pkg/model/history.go
git commit -m "feat(model): replace history request/response types

Replace HistoryRequest/HistoryResponse with typed models for all 4
history-service endpoints: LoadHistory, LoadNextMessages,
LoadSurroundingMessages, GetMessageByID."
```

---

### Task 3: Subject builders — Add new NATS subject functions

**Files:**
- Modify: `pkg/subject/subject.go`

- [ ] **Step 1: Add new subject builder functions**

Append the following after the existing `MsgHistory` function (after line 58) in `pkg/subject/subject.go`:

```go
func MsgNext(userID, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.next", userID, roomID, siteID)
}

func MsgSurrounding(userID, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.surrounding", userID, roomID, siteID)
}

func MsgGet(userID, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.get", userID, roomID, siteID)
}
```

- [ ] **Step 2: Add new wildcard functions**

Append after the existing `MsgHistoryWildcard` function (after line 106) in `pkg/subject/subject.go`:

```go
func MsgNextWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.next", siteID)
}

func MsgSurroundingWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.surrounding", siteID)
}

func MsgGetWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.get", siteID)
}
```

- [ ] **Step 3: Verify compilation**

Run: `go build ./pkg/subject/...`
Expected: success

- [ ] **Step 4: Commit subject builders**

```bash
git add pkg/subject/subject.go
git commit -m "feat(subject): add NATS subject builders for history endpoints

Add MsgNext, MsgSurrounding, MsgGet builders and their wildcard
counterparts for the 3 new history-service NATS endpoints."
```

---
