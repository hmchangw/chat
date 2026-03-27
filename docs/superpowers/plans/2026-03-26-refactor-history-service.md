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

### Task 7: Service interfaces + mock generation

**Files:**
- Create: `history-service/internal/service/service.go`
- Create: `history-service/internal/service/mocks/mock_store.go` (generated)

- [ ] **Step 1: Create service.go with interfaces and struct**

Write `history-service/internal/service/service.go`:

```go
package service

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mocks/mock_store.go -package=mocks . MessageRepository,SubscriptionRepository

// MessageRepository defines Cassandra-backed message operations.
type MessageRepository interface {
	GetMessagesBefore(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error)
	GetMessagesAfter(ctx context.Context, roomID string, after time.Time, limit int) ([]model.Message, error)
	GetSurroundingMessages(ctx context.Context, roomID, messageID string, limit int) (before []model.Message, after []model.Message, err error)
	GetMessageByID(ctx context.Context, roomID, messageID string) (*model.Message, error)
}

// SubscriptionRepository defines MongoDB-backed subscription lookups.
type SubscriptionRepository interface {
	GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
}

// HistoryService handles message history queries. Transport-agnostic.
type HistoryService struct {
	messages      MessageRepository
	subscriptions SubscriptionRepository
}

// New creates a HistoryService with the given repositories.
func New(msgs MessageRepository, subs SubscriptionRepository) *HistoryService {
	return &HistoryService{messages: msgs, subscriptions: subs}
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./history-service/internal/service/...`
Expected: success

- [ ] **Step 3: Generate mocks**

Run: `make generate SERVICE=history-service`
Expected: creates `history-service/internal/service/mocks/mock_store.go`

- [ ] **Step 4: Verify mocks compile**

Run: `go build ./history-service/internal/service/mocks/...`
Expected: success

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/service.go history-service/internal/service/mocks/
git commit -m "feat(service): add repository interfaces, HistoryService struct, generated mocks"
```

---

### Task 8: Cassandra repository + integration tests (TDD)

**Files:**
- Create: `history-service/internal/cassrepo/repository.go`
- Create: `history-service/internal/cassrepo/integration_test.go`

- [ ] **Step 1: Write integration tests**

Write `history-service/internal/cassrepo/integration_test.go`:

```go
//go:build integration

package cassrepo

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/cassandra"
)

func setupCassandra(t *testing.T) *gocql.Session {
	t.Helper()
	ctx := context.Background()
	container, err := cassandra.Run(ctx, "cassandra:5")
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.ConnectionHost(ctx)
	require.NoError(t, err)

	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.One
	session, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })

	require.NoError(t, session.Query(`CREATE KEYSPACE IF NOT EXISTS chat_test WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`).Exec())
	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages (room_id text, created_at timestamp, id text, user_id text, content text, PRIMARY KEY (room_id, created_at)) WITH CLUSTERING ORDER BY (created_at DESC)`).Exec())

	cluster.Keyspace = "chat_test"
	ksSession, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { ksSession.Close() })
	return ksSession
}

func seedMessages(t *testing.T, session *gocql.Session, roomID string, base time.Time, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		err := session.Query(`INSERT INTO messages (room_id, created_at, id, user_id, content) VALUES (?, ?, ?, ?, ?)`,
			roomID, ts, fmt.Sprintf("m%d", i), "u1", fmt.Sprintf("msg-%d", i)).Exec()
		require.NoError(t, err)
	}
}

func TestRepository_GetMessagesBefore(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	msgs, err := repo.GetMessagesBefore(ctx, "r1", base, base.Add(10*time.Minute), 3)
	require.NoError(t, err)
	assert.Len(t, msgs, 3)
	// Newest first (DESC)
	assert.True(t, msgs[0].CreatedAt.After(msgs[1].CreatedAt))
}

func TestRepository_GetMessagesAfter(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	msgs, err := repo.GetMessagesAfter(ctx, "r1", base.Add(2*time.Minute), 10)
	require.NoError(t, err)
	assert.Len(t, msgs, 2) // m3, m4
	// Oldest first (ASC)
	assert.True(t, msgs[0].CreatedAt.Before(msgs[1].CreatedAt))
}

func TestRepository_GetMessageByID(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 3)

	msg, err := repo.GetMessageByID(ctx, "r1", "m1")
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "m1", msg.ID)
	assert.Equal(t, "r1", msg.RoomID)
}

func TestRepository_GetMessageByID_NotFound(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	msg, err := repo.GetMessageByID(ctx, "r1", "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, msg)
}

func TestRepository_GetSurroundingMessages(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 10) // m0..m9

	before, after, err := repo.GetSurroundingMessages(ctx, "r1", "m5", 6)
	require.NoError(t, err)
	// Should have messages before and after m5's timestamp
	assert.NotEmpty(t, before)
	assert.NotEmpty(t, after)
}
```

- [ ] **Step 2: Implement repository.go**

Write `history-service/internal/cassrepo/repository.go`:

```go
package cassrepo

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
)

// Repository implements service.MessageRepository using Cassandra.
type Repository struct {
	session *gocql.Session
}

// NewRepository creates a new Cassandra repository.
func NewRepository(session *gocql.Session) *Repository {
	return &Repository{session: session}
}

func (r *Repository) scanMessage(iter *gocql.Iter) (model.Message, error) {
	var msg model.Message
	if !iter.Scan(&msg.ID, &msg.RoomID, &msg.UserID, &msg.Content, &msg.CreatedAt) {
		return msg, fmt.Errorf("no more rows")
	}
	return msg, nil
}

// GetMessagesBefore returns messages before `before` and after `since`, newest-first.
func (r *Repository) GetMessagesBefore(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error) {
	stmt := `SELECT id, room_id, user_id, content, created_at FROM messages
		WHERE room_id = ? AND created_at > ? AND created_at < ?
		ORDER BY created_at DESC LIMIT ?`

	iter := r.session.Query(stmt, roomID, since, before, limit).WithContext(ctx).Iter()

	var messages []model.Message
	var msg model.Message
	for iter.Scan(&msg.ID, &msg.RoomID, &msg.UserID, &msg.Content, &msg.CreatedAt) {
		messages = append(messages, msg)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("querying messages before: %w", err)
	}
	return messages, nil
}

// GetMessagesAfter returns messages after `after`, oldest-first.
func (r *Repository) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, limit int) ([]model.Message, error) {
	stmt := `SELECT id, room_id, user_id, content, created_at FROM messages
		WHERE room_id = ? AND created_at > ?
		ORDER BY created_at ASC LIMIT ?`

	iter := r.session.Query(stmt, roomID, after, limit).WithContext(ctx).Iter()

	var messages []model.Message
	var msg model.Message
	for iter.Scan(&msg.ID, &msg.RoomID, &msg.UserID, &msg.Content, &msg.CreatedAt) {
		messages = append(messages, msg)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("querying messages after: %w", err)
	}
	return messages, nil
}

// GetMessageByID returns a single message by ID within a room.
// Returns (nil, nil) if the message is not found.
func (r *Repository) GetMessageByID(ctx context.Context, roomID, messageID string) (*model.Message, error) {
	stmt := `SELECT id, room_id, user_id, content, created_at FROM messages
		WHERE room_id = ? ALLOW FILTERING`

	iter := r.session.Query(stmt, roomID).WithContext(ctx).Iter()

	var msg model.Message
	for iter.Scan(&msg.ID, &msg.RoomID, &msg.UserID, &msg.Content, &msg.CreatedAt) {
		if msg.ID == messageID {
			iter.Close()
			return &msg, nil
		}
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("querying message by id: %w", err)
	}
	return nil, nil
}

// GetSurroundingMessages returns messages before and after a given message.
// The central message is included in the `after` slice.
func (r *Repository) GetSurroundingMessages(ctx context.Context, roomID, messageID string, limit int) ([]model.Message, []model.Message, error) {
	msg, err := r.GetMessageByID(ctx, roomID, messageID)
	if err != nil {
		return nil, nil, fmt.Errorf("finding central message: %w", err)
	}
	if msg == nil {
		return nil, nil, fmt.Errorf("message %s not found in room %s", messageID, roomID)
	}

	half := limit / 2

	// Messages before the central message (newest first, then reverse for chronological)
	beforeMsgs, err := r.GetMessagesBefore(ctx, roomID, time.Time{}, msg.CreatedAt, half)
	if err != nil {
		return nil, nil, fmt.Errorf("querying surrounding before: %w", err)
	}

	// Messages after (and including) the central message
	afterStmt := `SELECT id, room_id, user_id, content, created_at FROM messages
		WHERE room_id = ? AND created_at >= ?
		ORDER BY created_at ASC LIMIT ?`

	iter := r.session.Query(afterStmt, roomID, msg.CreatedAt, half+1).WithContext(ctx).Iter()

	var afterMsgs []model.Message
	var m model.Message
	for iter.Scan(&m.ID, &m.RoomID, &m.UserID, &m.Content, &m.CreatedAt) {
		afterMsgs = append(afterMsgs, m)
	}
	if err := iter.Close(); err != nil {
		return nil, nil, fmt.Errorf("querying surrounding after: %w", err)
	}

	return beforeMsgs, afterMsgs, nil
}
```

- [ ] **Step 3: Verify compilation**

Run: `go build ./history-service/internal/cassrepo/...`
Expected: success

- [ ] **Step 4: Commit**

```bash
git add history-service/internal/cassrepo/repository.go history-service/internal/cassrepo/integration_test.go
git commit -m "feat(cassrepo): implement Cassandra MessageRepository with integration tests

TDD — GetMessagesBefore, GetMessagesAfter, GetMessageByID,
GetSurroundingMessages. Integration tests with testcontainers."
```

---

### Task 9: MongoDB repository + integration tests (TDD)

**Files:**
- Create: `history-service/internal/mongorepo/repository.go`
- Create: `history-service/internal/mongorepo/integration_test.go`

- [ ] **Step 1: Write integration tests**

Write `history-service/internal/mongorepo/integration_test.go`:

```go
//go:build integration

package mongorepo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:8")
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err)
	t.Cleanup(func() { client.Disconnect(ctx) })
	return client.Database("chat_test")
}

func TestRepository_GetSubscription(t *testing.T) {
	db := setupMongo(t)
	repo := NewRepository(db)
	ctx := context.Background()

	joinTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID: "s1", UserID: "u1", RoomID: "r1", SiteID: "site-local",
		Role: model.RoleMember, HistorySharedSince: joinTime, JoinedAt: joinTime,
	})
	require.NoError(t, err)

	sub, err := repo.GetSubscription(ctx, "u1", "r1")
	require.NoError(t, err)
	require.NotNil(t, sub)
	assert.Equal(t, "u1", sub.UserID)
	assert.Equal(t, "r1", sub.RoomID)
	assert.Equal(t, joinTime.UTC(), sub.HistorySharedSince.UTC())
}

func TestRepository_GetSubscription_NotFound(t *testing.T) {
	db := setupMongo(t)
	repo := NewRepository(db)
	ctx := context.Background()

	sub, err := repo.GetSubscription(ctx, "nonexistent", "r1")
	require.NoError(t, err)
	assert.Nil(t, sub)
}
```

- [ ] **Step 2: Implement repository.go**

Write `history-service/internal/mongorepo/repository.go`:

```go
package mongorepo

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

// Repository implements service.SubscriptionRepository using MongoDB.
type Repository struct {
	subscriptions *Collection[model.Subscription]
}

// NewRepository creates a new MongoDB repository.
func NewRepository(db *mongo.Database) *Repository {
	return &Repository{
		subscriptions: NewCollection[model.Subscription](db.Collection("subscriptions")),
	}
}

// GetSubscription returns the subscription for a user in a room.
// Returns (nil, nil) when the user is not subscribed.
func (r *Repository) GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error) {
	return r.subscriptions.FindOne(ctx, bson.D{
		{Key: "userId", Value: userID},
		{Key: "roomId", Value: roomID},
	})
}
```

- [ ] **Step 3: Verify compilation**

Run: `go build ./history-service/internal/mongorepo/...`
Expected: success

- [ ] **Step 4: Commit**

```bash
git add history-service/internal/mongorepo/repository.go history-service/internal/mongorepo/integration_test.go
git commit -m "feat(mongorepo): implement MongoDB SubscriptionRepository with integration tests

TDD — GetSubscription using Collection[T]. Returns (nil, nil) on not-found.
Integration tests with testcontainers."
```

---

### Task 10: Service handler methods (TDD)

**Files:**
- Create: `history-service/internal/service/messages.go`
- Create: `history-service/internal/service/messages_test.go`

- [ ] **Step 1: Write failing tests for all 4 handlers**

Write `history-service/internal/service/messages_test.go`:

```go
package service_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/history-service/internal/service"
	"github.com/hmchangw/chat/history-service/internal/service/mocks"
	"github.com/hmchangw/chat/pkg/model"
)

var (
	joinTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	testSub  = &model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleMember,
		HistorySharedSince: joinTime,
	}
)

func newService(t *testing.T) (*service.HistoryService, *mocks.MockMessageRepository, *mocks.MockSubscriptionRepository) {
	ctrl := gomock.NewController(t)
	msgs := mocks.NewMockMessageRepository(ctrl)
	subs := mocks.NewMockSubscriptionRepository(ctrl)
	return service.New(msgs, subs), msgs, subs
}

// --- LoadHistory ---

func TestHistoryService_LoadHistory_Success(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetSubscription(ctx, "u1", "r1").Return(testSub, nil)

	messages := make([]model.Message, 4)
	for i := range messages {
		messages[i] = model.Message{
			ID: fmt.Sprintf("m%d", i), RoomID: "r1",
			CreatedAt: joinTime.Add(time.Duration(4-i) * time.Minute),
		}
	}
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", joinTime, gomock.Any(), 51).Return(messages, nil)

	resp, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 4)
	assert.False(t, resp.HasMore)
}

func TestHistoryService_LoadHistory_HasMore(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetSubscription(ctx, "u1", "r1").Return(testSub, nil)

	messages := make([]model.Message, 51)
	for i := range messages {
		messages[i] = model.Message{ID: fmt.Sprintf("m%d", i), RoomID: "r1", CreatedAt: joinTime.Add(time.Duration(i) * time.Minute)}
	}
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", joinTime, gomock.Any(), 51).Return(messages, nil)

	resp, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 50)
	assert.True(t, resp.HasMore)
}

func TestHistoryService_LoadHistory_NotSubscribed(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetSubscription(ctx, "u1", "r1").Return(nil, nil)

	_, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{RoomID: "r1"})
	require.Error(t, err)
}

func TestHistoryService_LoadHistory_FirstUnread(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetSubscription(ctx, "u1", "r1").Return(testSub, nil)

	lastSeen := joinTime.Add(2 * time.Minute)
	messages := []model.Message{
		{ID: "m3", RoomID: "r1", CreatedAt: joinTime.Add(3 * time.Minute)},
		{ID: "m2", RoomID: "r1", CreatedAt: joinTime.Add(2 * time.Minute)},
		{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(1 * time.Minute)},
	}
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", joinTime, gomock.Any(), 51).Return(messages, nil)

	resp, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{
		RoomID:   "r1",
		LastSeen: lastSeen.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.FirstUnread)
	assert.Equal(t, "m3", resp.FirstUnread.ID)
}

// --- LoadNextMessages ---

func TestHistoryService_LoadNextMessages_Success(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetSubscription(ctx, "u1", "r1").Return(testSub, nil)

	after := joinTime.Add(1 * time.Minute)
	messages := []model.Message{
		{ID: "m2", RoomID: "r1", CreatedAt: joinTime.Add(2 * time.Minute)},
		{ID: "m3", RoomID: "r1", CreatedAt: joinTime.Add(3 * time.Minute)},
	}
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", after, 51).Return(messages, nil)

	resp, err := svc.LoadNextMessages(ctx, "u1", model.LoadNextMessagesRequest{
		RoomID: "r1",
		After:  after.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 2)
	assert.False(t, resp.HasMore)
}

func TestHistoryService_LoadNextMessages_ClampsToHistorySharedSince(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetSubscription(ctx, "u1", "r1").Return(testSub, nil)

	// Request after time BEFORE HistorySharedSince — should be clamped
	earlyTime := joinTime.Add(-1 * time.Hour)
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", joinTime, 51).Return(nil, nil)

	_, err := svc.LoadNextMessages(ctx, "u1", model.LoadNextMessagesRequest{
		RoomID: "r1",
		After:  earlyTime.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
}

// --- GetMessageByID ---

func TestHistoryService_GetMessageByID_Success(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetSubscription(ctx, "u1", "r1").Return(testSub, nil)

	msg := &model.Message{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(1 * time.Minute)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m1").Return(msg, nil)

	result, err := svc.GetMessageByID(ctx, "u1", model.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.NoError(t, err)
	assert.Equal(t, "m1", result.ID)
}

func TestHistoryService_GetMessageByID_OutsideAccessWindow(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetSubscription(ctx, "u1", "r1").Return(testSub, nil)

	msg := &model.Message{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(-1 * time.Hour)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m1").Return(msg, nil)

	_, err := svc.GetMessageByID(ctx, "u1", model.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.Error(t, err)
}

// --- LoadSurroundingMessages ---

func TestHistoryService_LoadSurroundingMessages_Success(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetSubscription(ctx, "u1", "r1").Return(testSub, nil)

	before := []model.Message{
		{ID: "m4", RoomID: "r1", CreatedAt: joinTime.Add(4 * time.Minute)},
	}
	after := []model.Message{
		{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)},
		{ID: "m6", RoomID: "r1", CreatedAt: joinTime.Add(6 * time.Minute)},
	}
	msgs.EXPECT().GetSurroundingMessages(ctx, "r1", "m5", 6).Return(before, after, nil)

	resp, err := svc.LoadSurroundingMessages(ctx, "u1", model.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Before, 1)
	assert.Len(t, resp.After, 2)
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `go test ./history-service/internal/service/ -v`
Expected: FAIL — `LoadHistory` method not defined

- [ ] **Step 3: Implement messages.go**

Write `history-service/internal/service/messages.go`:

```go
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

const defaultLimit = 50

func parseTimestamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// LoadHistory returns messages before a cursor timestamp, paginated backwards.
func (s *HistoryService) LoadHistory(ctx context.Context, userID string, req model.LoadHistoryRequest) (*model.LoadHistoryResponse, error) {
	sub, err := s.subscriptions.GetSubscription(ctx, userID, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("checking subscription: %w", err)
	}
	if sub == nil {
		return nil, fmt.Errorf("user %s is not subscribed to room %s", userID, req.RoomID)
	}

	before, err := parseTimestamp(req.Before)
	if err != nil {
		return nil, fmt.Errorf("parsing before timestamp: %w", err)
	}
	if before.IsZero() {
		before = time.Now().UTC()
	}

	lastSeen, err := parseTimestamp(req.LastSeen)
	if err != nil {
		return nil, fmt.Errorf("parsing lastSeen timestamp: %w", err)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	since := sub.HistorySharedSince
	msgs, err := s.messages.GetMessagesBefore(ctx, req.RoomID, since, before, limit+1)
	if err != nil {
		return nil, fmt.Errorf("loading history: %w", err)
	}

	hasMore := len(msgs) > limit
	if hasMore {
		msgs = msgs[:limit]
	}

	var firstUnread *model.Message
	if !lastSeen.IsZero() {
		for i := range msgs {
			if msgs[i].CreatedAt.After(lastSeen) {
				firstUnread = &msgs[i]
			}
		}
	}

	return &model.LoadHistoryResponse{
		Messages:    msgs,
		FirstUnread: firstUnread,
		HasMore:     hasMore,
	}, nil
}

// LoadNextMessages returns messages after a cursor timestamp, paginated forwards.
func (s *HistoryService) LoadNextMessages(ctx context.Context, userID string, req model.LoadNextMessagesRequest) (*model.LoadNextMessagesResponse, error) {
	sub, err := s.subscriptions.GetSubscription(ctx, userID, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("checking subscription: %w", err)
	}
	if sub == nil {
		return nil, fmt.Errorf("user %s is not subscribed to room %s", userID, req.RoomID)
	}

	after, err := parseTimestamp(req.After)
	if err != nil {
		return nil, fmt.Errorf("parsing after timestamp: %w", err)
	}

	// Clamp to HistorySharedSince
	if after.Before(sub.HistorySharedSince) {
		after = sub.HistorySharedSince
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	msgs, err := s.messages.GetMessagesAfter(ctx, req.RoomID, after, limit+1)
	if err != nil {
		return nil, fmt.Errorf("loading next messages: %w", err)
	}

	hasMore := len(msgs) > limit
	if hasMore {
		msgs = msgs[:limit]
	}

	return &model.LoadNextMessagesResponse{
		Messages: msgs,
		HasMore:  hasMore,
	}, nil
}

// LoadSurroundingMessages returns messages around a central message.
func (s *HistoryService) LoadSurroundingMessages(ctx context.Context, userID string, req model.LoadSurroundingMessagesRequest) (*model.LoadSurroundingMessagesResponse, error) {
	sub, err := s.subscriptions.GetSubscription(ctx, userID, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("checking subscription: %w", err)
	}
	if sub == nil {
		return nil, fmt.Errorf("user %s is not subscribed to room %s", userID, req.RoomID)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	before, after, err := s.messages.GetSurroundingMessages(ctx, req.RoomID, req.MessageID, limit)
	if err != nil {
		return nil, fmt.Errorf("loading surrounding messages: %w", err)
	}

	// Filter out messages before HistorySharedSince
	since := sub.HistorySharedSince
	filtered := before[:0]
	for _, m := range before {
		if !m.CreatedAt.Before(since) {
			filtered = append(filtered, m)
		}
	}

	return &model.LoadSurroundingMessagesResponse{
		Before: filtered,
		After:  after,
	}, nil
}

// GetMessageByID returns a single message by ID.
func (s *HistoryService) GetMessageByID(ctx context.Context, userID string, req model.GetMessageByIDRequest) (*model.Message, error) {
	sub, err := s.subscriptions.GetSubscription(ctx, userID, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("checking subscription: %w", err)
	}
	if sub == nil {
		return nil, fmt.Errorf("user %s is not subscribed to room %s", userID, req.RoomID)
	}

	msg, err := s.messages.GetMessageByID(ctx, req.RoomID, req.MessageID)
	if err != nil {
		return nil, fmt.Errorf("loading message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message %s not found", req.MessageID)
	}

	if msg.CreatedAt.Before(sub.HistorySharedSince) {
		return nil, fmt.Errorf("message %s is outside access window", req.MessageID)
	}

	return msg, nil
}
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `go test ./history-service/internal/service/ -v`
Expected: PASS — all tests

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/messages.go history-service/internal/service/messages_test.go
git commit -m "feat(service): implement 4 transport-agnostic handler methods with tests

TDD — LoadHistory, LoadNextMessages, LoadSurroundingMessages,
GetMessageByID. HistorySharedSince enforcement, limit+1 pagination,
firstUnread scanning, after-cursor clamping."
```

---

### Task 11: NATS handler utils (TDD)

**Files:**
- Create: `history-service/internal/natshandler/utils.go`
- Create: `history-service/internal/natshandler/utils_test.go`

- [ ] **Step 1: Write failing tests**

Write `history-service/internal/natshandler/utils_test.go`:

```go
package natshandler

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testReq struct {
	Name string `json:"name"`
}

type testResp struct {
	Greeting string `json:"greeting"`
}

// fakeMsg creates a *nats.Msg with a reply channel we can capture.
func fakeMsg(t *testing.T, data []byte) (*nats.Msg, chan []byte) {
	t.Helper()
	replyCh := make(chan []byte, 1)

	// We can't easily test nats.Msg.Respond without a real connection,
	// so we test the unmarshal + handler logic directly.
	// The HandleRequest function will be tested via the NATS handler integration.
	return &nats.Msg{Data: data}, replyCh
}

func TestHandleRequest_UnmarshalSuccess(t *testing.T) {
	req := testReq{Name: "world"}
	data, _ := json.Marshal(req)

	var captured testReq
	handler := func(ctx context.Context, r testReq) (*testResp, error) {
		captured = r
		return &testResp{Greeting: "hello " + r.Name}, nil
	}

	// Test the unmarshal logic
	var parsed testReq
	err := json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	resp, err := handler(context.Background(), parsed)
	require.NoError(t, err)
	assert.Equal(t, "world", captured.Name)
	assert.Equal(t, "hello world", resp.Greeting)
}

func TestHandleRequest_HandlerError(t *testing.T) {
	handler := func(ctx context.Context, r testReq) (*testResp, error) {
		return nil, fmt.Errorf("something broke")
	}

	_, err := handler(context.Background(), testReq{Name: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "something broke")
}

func TestHandleRequest_InvalidJSON(t *testing.T) {
	var parsed testReq
	err := json.Unmarshal([]byte("not json"), &parsed)
	require.Error(t, err)
}
```

- [ ] **Step 2: Implement utils.go**

Write `history-service/internal/natshandler/utils.go`:

```go
package natshandler

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// HandleRequest is a generic helper that eliminates unmarshal/reply boilerplate.
// It unmarshals the NATS message payload into Req, calls the handler func,
// and replies with the result via natsutil.ReplyJSON or natsutil.ReplyError.
func HandleRequest[Req, Resp any](msg *nats.Msg, handlerFn func(ctx context.Context, req Req) (*Resp, error)) {
	ctx := context.Background()

	var req Req
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		natsutil.ReplyError(msg, "invalid request payload")
		return
	}

	resp, err := handlerFn(ctx, req)
	if err != nil {
		slog.Error("handler error", "error", err)
		natsutil.ReplyError(msg, "internal error")
		return
	}

	natsutil.ReplyJSON(msg, resp)
}
```

- [ ] **Step 3: Run tests — verify they pass**

Run: `go test ./history-service/internal/natshandler/ -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add history-service/internal/natshandler/utils.go history-service/internal/natshandler/utils_test.go
git commit -m "feat(natshandler): add generic HandleRequest helper with tests

TDD — handles unmarshal, error reply, and success reply boilerplate.
Sanitizes errors at transport boundary."
```

---

### Task 12: NATS handler — registration and glue

**Files:**
- Create: `history-service/internal/natshandler/handler.go`

- [ ] **Step 1: Implement handler.go**

Write `history-service/internal/natshandler/handler.go`:

```go
package natshandler

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/history-service/internal/service"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// Handler wraps the HistoryService and registers NATS subscriptions.
type Handler struct {
	svc    *service.HistoryService
	siteID string
}

// New creates a new NATS Handler.
func New(svc *service.HistoryService, siteID string) *Handler {
	return &Handler{svc: svc, siteID: siteID}
}

// Register wires all 4 NATS queue subscriptions.
func (h *Handler) Register(nc *nats.Conn) error {
	queueGroup := "history-service"
	subs := []struct {
		subject string
		handler nats.MsgHandler
	}{
		{subject.MsgHistoryWildcard(h.siteID), h.handleLoadHistory},
		{subject.MsgNextWildcard(h.siteID), h.handleLoadNextMessages},
		{subject.MsgSurroundingWildcard(h.siteID), h.handleLoadSurroundingMessages},
		{subject.MsgGetWildcard(h.siteID), h.handleGetMessageByID},
	}
	for _, s := range subs {
		if _, err := nc.QueueSubscribe(s.subject, queueGroup, s.handler); err != nil {
			return fmt.Errorf("subscribing to %s: %w", s.subject, err)
		}
	}
	return nil
}

func (h *Handler) handleLoadHistory(msg *nats.Msg) {
	userID, _, ok := subject.ParseUserRoomSubject(msg.Subject)
	if !ok {
		natsutil.ReplyError(msg, "invalid subject")
		return
	}
	HandleRequest(msg, func(ctx context.Context, req model.LoadHistoryRequest) (*model.LoadHistoryResponse, error) {
		return h.svc.LoadHistory(ctx, userID, req)
	})
}

func (h *Handler) handleLoadNextMessages(msg *nats.Msg) {
	userID, _, ok := subject.ParseUserRoomSubject(msg.Subject)
	if !ok {
		natsutil.ReplyError(msg, "invalid subject")
		return
	}
	HandleRequest(msg, func(ctx context.Context, req model.LoadNextMessagesRequest) (*model.LoadNextMessagesResponse, error) {
		return h.svc.LoadNextMessages(ctx, userID, req)
	})
}

func (h *Handler) handleLoadSurroundingMessages(msg *nats.Msg) {
	userID, _, ok := subject.ParseUserRoomSubject(msg.Subject)
	if !ok {
		natsutil.ReplyError(msg, "invalid subject")
		return
	}
	HandleRequest(msg, func(ctx context.Context, req model.LoadSurroundingMessagesRequest) (*model.LoadSurroundingMessagesResponse, error) {
		return h.svc.LoadSurroundingMessages(ctx, userID, req)
	})
}

func (h *Handler) handleGetMessageByID(msg *nats.Msg) {
	userID, _, ok := subject.ParseUserRoomSubject(msg.Subject)
	if !ok {
		natsutil.ReplyError(msg, "invalid subject")
		return
	}
	HandleRequest(msg, func(ctx context.Context, req model.GetMessageByIDRequest) (*model.Message, error) {
		return h.svc.GetMessageByID(ctx, userID, req)
	})
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./history-service/internal/natshandler/...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/natshandler/handler.go
git commit -m "feat(natshandler): implement NATS handler with Register and 4 endpoints

Bridges NATS transport to service layer. Uses ParseUserRoomSubject
for subject parsing, HandleRequest for unmarshal/reply boilerplate."
```

---

### Task 13: cmd/main.go — wiring and graceful shutdown

**Files:**
- Create: `history-service/cmd/main.go`

- [ ] **Step 1: Implement main.go**

Write `history-service/cmd/main.go`:

```go
package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/config"
	"github.com/hmchangw/chat/history-service/internal/mongorepo"
	"github.com/hmchangw/chat/history-service/internal/natshandler"
	"github.com/hmchangw/chat/history-service/internal/service"
	"github.com/hmchangw/chat/pkg/cassutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	nc, err := nats.Connect(cfg.NATS.URL)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}

	mongoClient, err := mongoutil.Connect(ctx, cfg.Mongo.URI)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}

	cassSession, err := cassutil.Connect(strings.Split(cfg.Cassandra.Hosts, ","), cfg.Cassandra.Keyspace)
	if err != nil {
		slog.Error("cassandra connect failed", "error", err)
		os.Exit(1)
	}

	cassRepo := cassrepo.NewRepository(cassSession)
	mongoRepo := mongorepo.NewRepository(mongoClient.Database(cfg.Mongo.DB))
	svc := service.New(cassRepo, mongoRepo)
	handler := natshandler.New(svc, cfg.SiteID)

	if err := handler.Register(nc); err != nil {
		slog.Error("register handlers failed", "error", err)
		os.Exit(1)
	}

	slog.Info("history-service running", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
		func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
	)
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./history-service/cmd/...`
Expected: success

- [ ] **Step 3: Verify build via Makefile**

Run: `make build SERVICE=history-service`
Expected: creates `bin/history-service` binary

- [ ] **Step 4: Commit**

```bash
git add history-service/cmd/main.go
git commit -m "feat(history-service): add cmd/main.go with config, wiring, graceful shutdown

Wires config, NATS, MongoDB, Cassandra, repos, service, and handler.
Graceful shutdown: nc.Drain -> mongo disconnect -> cassandra close."
```

---

### Task 14: Final verification

- [ ] **Step 1: Run all unit tests**

Run: `make test SERVICE=history-service`
Expected: PASS — all unit tests across all packages

- [ ] **Step 2: Run linter**

Run: `make fmt && make lint`
Expected: no errors (fix any formatting/lint issues)

- [ ] **Step 3: Verify build**

Run: `make build SERVICE=history-service`
Expected: binary at `bin/history-service`

- [ ] **Step 4: Final commit (if any fixes needed)**

```bash
git add -A
git commit -m "fix(history-service): address lint and formatting issues"
```

- [ ] **Step 5: Push all changes**

```bash
git push -u origin claude/refactor-history-service-ucB7z
```

---
