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

### Task 4: Config package

**Files:**
- Create: `history-service/internal/config/config.go`

- [ ] **Step 1: Create config.go**

Write `history-service/internal/config/config.go`:

```go
package config

import "github.com/caarlos0/env/v11"

// CassandraConfig holds Cassandra connection settings.
type CassandraConfig struct {
	Hosts    string `env:"CASSANDRA_HOSTS"    required:"true"`
	Keyspace string `env:"CASSANDRA_KEYSPACE" envDefault:"chat"`
}

// MongoConfig holds MongoDB connection settings.
type MongoConfig struct {
	URI string `env:"MONGO_URI" required:"true"`
	DB  string `env:"MONGO_DB"  envDefault:"chat"`
}

// NATSConfig holds NATS connection settings.
type NATSConfig struct {
	URL string `env:"NATS_URL" required:"true"`
}

// Config is the top-level configuration for history-service.
type Config struct {
	SiteID    string          `env:"SITE_ID" envDefault:"site-local"`
	Cassandra CassandraConfig `envPrefix:""`
	Mongo     MongoConfig     `envPrefix:""`
	NATS      NATSConfig      `envPrefix:""`
}

// Load parses environment variables into Config. Returns error if required vars are missing.
func Load() (Config, error) {
	return env.ParseAs[Config]()
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./history-service/internal/config/...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/config/
git commit -m "feat(history-service): add config package with embedded sub-structs"
```

---

### Task 5: Cassandra pagination toolkit (TDD)

**Files:**
- Create: `history-service/internal/cassrepo/utils.go`
- Create: `history-service/internal/cassrepo/utils_test.go`

- [ ] **Step 1: Write failing tests for Query builder**

Write `history-service/internal/cassrepo/utils_test.go`:

```go
package cassrepo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewQuery(t *testing.T) {
	q := NewQuery(nil, "SELECT * FROM messages WHERE room_id = ?", "r1")

	assert.Equal(t, "SELECT * FROM messages WHERE room_id = ?", q.stmt)
	assert.Equal(t, []interface{}{"r1"}, q.args)
	assert.Equal(t, 0, q.pageSize)
	assert.Nil(t, q.pageState)
}

func TestQuery_PageSize(t *testing.T) {
	q := NewQuery(nil, "SELECT * FROM t").PageSize(25)

	assert.Equal(t, 25, q.pageSize)
}

func TestQuery_WithPageState(t *testing.T) {
	state := []byte{0x01, 0x02, 0x03}
	q := NewQuery(nil, "SELECT * FROM t").WithPageState(state)

	assert.Equal(t, state, q.pageState)
}

func TestQuery_Chaining(t *testing.T) {
	state := []byte{0xAB}
	q := NewQuery(nil, "SELECT * FROM t", "arg1").
		PageSize(10).
		WithPageState(state)

	assert.Equal(t, "SELECT * FROM t", q.stmt)
	assert.Equal(t, []interface{}{"arg1"}, q.args)
	assert.Equal(t, 10, q.pageSize)
	assert.Equal(t, state, q.pageState)
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `go test ./history-service/internal/cassrepo/ -run TestNewQuery -v`
Expected: FAIL — `NewQuery` not defined

- [ ] **Step 3: Implement utils.go**

Write `history-service/internal/cassrepo/utils.go`:

```go
package cassrepo

import "github.com/gocql/gocql"

// Page is a generic paginated result from Cassandra.
type Page[T any] struct {
	Items     []T    `json:"items"`
	PageState []byte `json:"pageState,omitempty"`
	HasMore   bool   `json:"hasMore"`
}

// Scanner scans values from a gocql iterator row into a T.
type Scanner[T any] func(iter *gocql.Iter) (T, error)

// Query wraps a CQL statement with builder-pattern configuration.
type Query struct {
	session   *gocql.Session
	stmt      string
	args      []interface{}
	pageSize  int
	pageState []byte
}

// NewQuery creates a new Query with the given statement and arguments.
func NewQuery(session *gocql.Session, stmt string, args ...interface{}) *Query {
	return &Query{
		session: session,
		stmt:    stmt,
		args:    args,
	}
}

// PageSize sets the number of results per page.
func (q *Query) PageSize(n int) *Query {
	q.pageSize = n
	return q
}

// WithPageState sets the pagination cursor for resuming iteration.
func (q *Query) WithPageState(state []byte) *Query {
	q.pageState = state
	return q
}

// ScanPage executes the query and scans results into a Page[T].
// Standalone function because Go methods cannot have type parameters.
func ScanPage[T any](q *Query, scan Scanner[T]) (*Page[T], error) {
	gocqlQuery := q.session.Query(q.stmt, q.args...)
	if q.pageSize > 0 {
		gocqlQuery = gocqlQuery.PageSize(q.pageSize)
	}
	if q.pageState != nil {
		gocqlQuery = gocqlQuery.PageState(q.pageState)
	}

	iter := gocqlQuery.Iter()
	var items []T
	for {
		item, err := scan(iter)
		if err != nil {
			break
		}
		items = append(items, item)
	}

	pageState := iter.PageState()
	if err := iter.Close(); err != nil {
		return nil, err
	}

	return &Page[T]{
		Items:     items,
		PageState: pageState,
		HasMore:   len(pageState) > 0,
	}, nil
}
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `go test ./history-service/internal/cassrepo/ -v`
Expected: PASS — all 4 tests

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/utils.go history-service/internal/cassrepo/utils_test.go
git commit -m "feat(cassrepo): add Cassandra pagination toolkit with Page[T] and Query builder

TDD — Query builder with chaining, generic ScanPage function,
Page[T] type with PageState cursor support."
```

---

### Task 6: MongoDB Collection[T] abstraction (TDD)

**Files:**
- Create: `history-service/internal/mongorepo/utils.go`
- Create: `history-service/internal/mongorepo/utils_test.go`

- [ ] **Step 1: Write failing tests for Collection[T]**

Write `history-service/internal/mongorepo/utils_test.go`:

```go
package mongorepo

import (
	"testing"
)

func TestNewCollection_Compiles(t *testing.T) {
	// Cannot test with nil collection — just verify the type compiles.
	// Real behavior tested in integration tests.
	var _ *Collection[struct{ Name string }]
}

func TestCollection_TypeSafety(t *testing.T) {
	// Verify generic instantiation compiles for different types.
	type User struct {
		ID   string `bson:"_id"`
		Name string `bson:"name"`
	}
	type Room struct {
		ID   string `bson:"_id"`
		Name string `bson:"name"`
	}
	var _ *Collection[User]
	var _ *Collection[Room]
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `go test ./history-service/internal/mongorepo/ -run TestNewCollection -v`
Expected: FAIL — `Collection` not defined

- [ ] **Step 3: Implement utils.go**

Write `history-service/internal/mongorepo/utils.go`:

```go
package mongorepo

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Collection is a type-safe wrapper around *mongo.Collection.
// It handles decoding, ErrNoDocuments normalization, and consistent error wrapping.
type Collection[T any] struct {
	col  *mongo.Collection
	name string
}

// NewCollection creates a typed collection wrapper.
func NewCollection[T any](col *mongo.Collection) *Collection[T] {
	return &Collection[T]{col: col, name: col.Name()}
}

// FindOne returns the first document matching the filter decoded into *T.
// Returns (nil, nil) when no document matches — not an error.
func (c *Collection[T]) FindOne(ctx context.Context, filter bson.D) (*T, error) {
	var result T
	err := c.col.FindOne(ctx, filter).Decode(&result)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding %s: %w", c.name, err)
	}
	return &result, nil
}

// FindByID is a shortcut for finding a document by its _id field.
func (c *Collection[T]) FindByID(ctx context.Context, id string) (*T, error) {
	return c.FindOne(ctx, bson.D{{Key: "_id", Value: id}})
}

// FindMany returns all documents matching the filter decoded into []T.
// Returns an empty slice (not nil) when no documents match.
func (c *Collection[T]) FindMany(ctx context.Context, filter bson.D) ([]T, error) {
	cursor, err := c.col.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("querying %s: %w", c.name, err)
	}
	var results []T
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decoding %s results: %w", c.name, err)
	}
	if results == nil {
		results = []T{}
	}
	return results, nil
}

// Raw returns the underlying *mongo.Collection for escape-hatch scenarios.
func (c *Collection[T]) Raw() *mongo.Collection { return c.col }
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `go test ./history-service/internal/mongorepo/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/mongorepo/utils.go history-service/internal/mongorepo/utils_test.go
git commit -m "feat(mongorepo): add generic Collection[T] with FindOne, FindByID, FindMany

TDD — type-safe mongo wrapper. FindOne returns (nil, nil) on not-found.
Error wrapping includes collection name. FindMany returns []T{} not nil."
```

---
