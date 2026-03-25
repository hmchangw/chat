# History-Service Refactor Design

## Overview

Refactor `history-service` from its current flat, pre-generated structure into a well-organized Go service with `cmd/` + `internal/` layout. The service handles 4 NATS request/reply endpoints for message history queries, backed by Cassandra (message storage) and MongoDB (subscription metadata).

## Goals

- Clean folder structure with clear separation of concerns
- Proper dependency injection via interfaces defined in the consumer
- Reusable Cassandra pagination toolkit (generic `Page[T]`, query builder)
- Lightweight MongoDB generic helpers (`FindOne[T]`, `FindMany[T]`)
- Comprehensive tests: unit tests with mocks for service layer, integration tests with testcontainers for repositories
- Solid foundation for future development

## Non-Goals

- Implementing full business logic for all 4 handlers (placeholders/scaffolds are fine)
- Promoting utils to `pkg/` (stays in `internal/` until reviewed)
- Changing other services or shared packages
- End-to-end integration tests (NATS + Cassandra + MongoDB wired together)

## Known Follow-ups

- `model.Message` in `pkg/model/message.go` is missing `bson` tags (CLAUDE.md requires both `json` and `bson`). Will address separately.

## Folder Structure

```
history-service/
├── cmd/
│   └── main.go                    # Config, wiring, NATS subscriptions, graceful shutdown
├── internal/
│   ├── config/
│   │   └── config.go              # Config struct with embedded sub-configs
│   ├── service/
│   │   ├── service.go             # Repository interfaces, HistoryService struct, New()
│   │   ├── messages.go            # 4 transport-agnostic handler methods
│   │   ├── messages_test.go       # Unit tests with mocked repositories
│   │   └── mocks/
│   │       └── mock_store.go      # Generated mocks (mockgen)
│   ├── natshandler/
│   │   ├── handler.go             # NATSHandler struct, Register(), NATS-to-service glue
│   │   ├── utils.go               # Subject parsing, generic request handler wrapper
│   │   └── utils_test.go          # Unit tests for utils
│   ├── cassrepo/
│   │   ├── repository.go          # CassandraRepository implementing service.MessageRepository
│   │   ├── utils.go               # Page[T], Query builder, ScanPage[T] — pagination toolkit
│   │   ├── utils_test.go          # Unit tests for pagination utilities
│   │   └── integration_test.go    # Integration tests with testcontainers
│   └── mongorepo/
│       ├── repository.go          # MongoRepository implementing service.SubscriptionRepository
│       ├── utils.go               # Generic FindOne[T], FindMany[T] helpers
│       ├── utils_test.go          # Unit tests for helpers
│       └── integration_test.go    # Integration tests with testcontainers
├── deploy/
│   ├── Dockerfile
│   ├── docker-compose.yml
│   └── azure-pipelines.yml
```

## Component Designs

### 1. Config (`internal/config/config.go`)

Embedded sub-structs for logical grouping, parsed via `caarlos0/env`. Connection strings are `required` (no defaults) per CLAUDE.md conventions.

```go
type CassandraConfig struct {
    Hosts    string `env:"CASSANDRA_HOSTS"    required:"true"`
    Keyspace string `env:"CASSANDRA_KEYSPACE" envDefault:"chat"`
}

type MongoConfig struct {
    URI string `env:"MONGO_URI" required:"true"`
    DB  string `env:"MONGO_DB"  envDefault:"chat"`
}

type NATSConfig struct {
    URL string `env:"NATS_URL" required:"true"`
}

type Config struct {
    SiteID    string          `env:"SITE_ID" envDefault:"site-local"`
    Cassandra CassandraConfig `envPrefix:""`
    Mongo     MongoConfig     `envPrefix:""`
    NATS      NATSConfig      `envPrefix:""`
}

func Load() (Config, error) {
    return env.ParseAs[Config]()
}
```

No env prefix on embedded structs — env var names stay consistent with the rest of the repo. Connection strings (`NATS_URL`, `MONGO_URI`, `CASSANDRA_HOSTS`) are required with no defaults — fail fast if missing.

### 2. Service Layer (`internal/service/`)

#### `service.go` — Interfaces, struct, wiring

```go
// MessageRepository defines Cassandra-backed message operations.
type MessageRepository interface {
    GetMessages(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error)
    GetMessagesAfter(ctx context.Context, roomID string, after time.Time, limit int) ([]model.Message, error)
    GetSurroundingMessages(ctx context.Context, roomID string, anchor time.Time, limit int) ([]model.Message, []model.Message, error)
    GetMessageByID(ctx context.Context, roomID, messageID string) (*model.Message, error)
}

// SubscriptionRepository defines MongoDB-backed subscription lookups.
type SubscriptionRepository interface {
    GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
}

// HistoryService handles message history queries. Transport-agnostic — accepts
// parsed Go structs and returns Go structs. NATS plumbing lives in natshandler.
type HistoryService struct {
    messages      MessageRepository
    subscriptions SubscriptionRepository
}

func New(msgs MessageRepository, subs SubscriptionRepository) *HistoryService {
    return &HistoryService{messages: msgs, subscriptions: subs}
}
```

#### `messages.go` — Handler implementations

Four methods on `*HistoryService`. Each accepts a context + userID + parsed request struct, returns a response struct + error. No NATS dependency — pure business logic.

1. **`LoadHistory(ctx, userID, req) (*HistoryResponse, error)`** — Messages before a cursor timestamp, paginated backwards. Validates user subscription, checks `SharedHistorySince`, returns `HistoryResponse` with `HasMore`.

2. **`LoadNextMessages(ctx, userID, req) (*HistoryResponse, error)`** — Messages after a cursor timestamp, paginated forwards. Same subscription validation. Returns messages in chronological order.

3. **`LoadSurroundingMessages(ctx, userID, req) (*SurroundingMessagesResponse, error)`** — Given a timestamp anchor, returns N messages before and N after. Used for "jump to message" scenarios.

4. **`GetMessageByID(ctx, userID, req) (*Message, error)`** — Single message lookup by ID within a room. Validates subscription access.

All handlers follow the pattern:
- Validate subscription access via `SubscriptionRepository`
- Query messages via `MessageRepository`
- Return typed response or wrapped error

#### `mocks/mock_store.go` — Generated

```go
//go:generate mockgen -destination=mocks/mock_store.go -package=mocks . MessageRepository,SubscriptionRepository
```

Directive lives on `service.go`. Generated file is never edited manually. The mocks live in a separate `mocks` package — tests use `package service_test` (external test package) and import from `mocks`.

### 3. NATS Handler (`internal/natshandler/`)

The transport layer — bridges NATS messages to the service layer.

#### `handler.go` — Registration and NATS glue

```go
// Handler wraps the HistoryService and registers NATS subscriptions.
type Handler struct {
    svc    *service.HistoryService
    siteID string
}

func New(svc *service.HistoryService, siteID string) *Handler {
    return &Handler{svc: svc, siteID: siteID}
}

// Register wires all 4 NATS queue subscriptions. Uses existing pkg/subject
// builders for subject patterns and the service name as queue group.
func (h *Handler) Register(nc *nats.Conn) error {
    queueGroup := "history-service"
    nc.QueueSubscribe(subject.HistoryRequestAll(h.siteID), queueGroup, h.handleLoadHistory)
    nc.QueueSubscribe(subject.NextMessagesRequestAll(h.siteID), queueGroup, h.handleLoadNextMessages)
    nc.QueueSubscribe(subject.SurroundingMessagesRequestAll(h.siteID), queueGroup, h.handleLoadSurroundingMessages)
    nc.QueueSubscribe(subject.GetMessageRequestAll(h.siteID), queueGroup, h.handleGetMessageByID)
    return nil
}
```

Each `handle*` method follows the same pattern:
1. Extract `userID` and `roomID` from the NATS subject tokens using `ParseSubjectTokens`
2. Unmarshal the request payload into the typed model struct
3. Call the corresponding `HistoryService` method with parsed args
4. Reply via `natsutil.ReplyJSON` on success or `natsutil.ReplyError` with a sanitized message on failure

#### `utils.go` — Reusable NATS abstractions

```go
// ParseSubjectTokens extracts named tokens from a NATS subject given a pattern.
// Pattern uses {name} placeholders: "chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history"
// Returns a map: {"userID": "abc", "roomID": "xyz", "siteID": "site-1"}
func ParseSubjectTokens(subject, pattern string) (map[string]string, error)

// HandleRequest is a generic helper that eliminates unmarshal/reply boilerplate.
// It unmarshals the NATS message payload into Req, calls the handler func,
// and replies with the result or an error.
func HandleRequest[Req, Resp any](msg *nats.Msg, handlerFn func(ctx context.Context, req Req) (*Resp, error))
```

`HandleRequest` reduces each NATS handler to: extract subject tokens + call `HandleRequest` with a closure that invokes the service method. Keeps the individual handlers very short.

### 4. New Model Types (`pkg/model/history.go`)

The existing `HistoryRequest`/`HistoryResponse` cover `LoadHistory`. New types needed:

```go
// NextMessagesRequest is the payload for loading messages after a timestamp.
type NextMessagesRequest struct {
    RoomID string `json:"roomId" bson:"roomId"`
    After  string `json:"after"  bson:"after"`   // RFC3339Nano timestamp cursor
    Limit  int    `json:"limit"  bson:"limit"`
}

// SurroundingMessagesRequest is the payload for loading messages around an anchor.
type SurroundingMessagesRequest struct {
    RoomID    string `json:"roomId"    bson:"roomId"`
    Anchor    string `json:"anchor"    bson:"anchor"`    // RFC3339Nano timestamp
    Limit     int    `json:"limit"     bson:"limit"`     // messages per direction
}

// SurroundingMessagesResponse contains messages before and after the anchor.
type SurroundingMessagesResponse struct {
    Before []Message `json:"before" bson:"before"`
    After  []Message `json:"after"  bson:"after"`
}

// GetMessageRequest is the payload for fetching a single message by ID.
type GetMessageRequest struct {
    RoomID    string `json:"roomId"    bson:"roomId"`
    MessageID string `json:"messageId" bson:"messageId"`
}
```

`LoadNextMessages` reuses `HistoryResponse` (same shape: messages + hasMore). `GetMessageByID` returns a single `Message`.

### 5. Cassandra Pagination Toolkit (`internal/cassrepo/utils.go`)

Custom types with builder pattern for Cassandra pagination.

```go
// Page[T] is a generic paginated result.
type Page[T any] struct {
    Items     []T    `json:"items"`
    PageState []byte `json:"pageState,omitempty"`
    HasMore   bool   `json:"hasMore"`
}

// Scanner[T] scans values from a gocql iterator row into a T.
type Scanner[T any] func(iter *gocql.Iter) (T, error)

// Query wraps a CQL statement with builder-pattern configuration.
type Query struct {
    session   *gocql.Session
    stmt      string
    args      []interface{}
    pageSize  int
    pageState []byte
}

func NewQuery(session *gocql.Session, stmt string, args ...interface{}) *Query
func (q *Query) PageSize(n int) *Query
func (q *Query) WithPageState(state []byte) *Query

// ScanPage executes the query and scans results into a Page[T].
// Standalone function because Go methods cannot have type parameters.
func ScanPage[T any](q *Query, scan Scanner[T]) (*Page[T], error)
```

`Scanner[T]` takes an explicit `*gocql.Iter` parameter (not a closure) — makes the dependency on the iterator explicit and easier to understand.

`ScanPage` iterates the query result, calls the scanner for each row, captures the page state from the iterator, and sets `HasMore` based on whether a page state exists.

### 6. MongoDB Helpers (`internal/mongorepo/utils.go`)

Thin generic wrappers that eliminate decode boilerplate:

```go
// FindOne[T] finds a single document matching the filter and decodes into T.
func FindOne[T any](ctx context.Context, col *mongo.Collection, filter bson.D) (*T, error)

// FindMany[T] finds all documents matching the filter and decodes into []T.
func FindMany[T any](ctx context.Context, col *mongo.Collection, filter bson.D, opts ...options.Lister[options.FindOptions]) ([]T, error)
```

### 7. Repository Implementations

#### Cassandra (`internal/cassrepo/repository.go`)

```go
type Repository struct {
    session *gocql.Session
}

func NewRepository(session *gocql.Session) *Repository

// Implements service.MessageRepository
func (r *Repository) GetMessages(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error)
func (r *Repository) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, limit int) ([]model.Message, error)
func (r *Repository) GetSurroundingMessages(ctx context.Context, roomID string, anchor time.Time, limit int) ([]model.Message, []model.Message, error)
func (r *Repository) GetMessageByID(ctx context.Context, roomID, messageID string) (*model.Message, error)
```

All methods use `NewQuery` + `ScanPage` from the utils toolkit.

#### MongoDB (`internal/mongorepo/repository.go`)

```go
type Repository struct {
    subscriptions *mongo.Collection
}

func NewRepository(db *mongo.Database) *Repository

// Implements service.SubscriptionRepository
func (r *Repository) GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
```

Uses `FindOne[model.Subscription]` from utils.

### 8. `cmd/main.go`

Follows the established startup + graceful shutdown pattern:

1. `config.Load()` — fail fast on error
2. Connect NATS — fail fast on error
3. Connect MongoDB via `mongoutil.Connect` — fail fast on error
4. Connect Cassandra via `cassutil.Connect` — fail fast on error
5. Create `cassrepo.NewRepository(session)`, `mongorepo.NewRepository(db)`
6. Create `service.New(cassRepo, mongoRepo)`
7. Create `natshandler.New(svc, cfg.SiteID)` and call `handler.Register(nc)`
8. `shutdown.Wait()` — cleanup order: `nc.Drain()` → `mongoutil.Disconnect()` → `cassutil.Close()`

### 9. Testing Strategy

#### Unit Tests (`messages_test.go`)
- Table-driven tests for all 4 handlers
- Mock both `MessageRepository` and `SubscriptionRepository` via generated mocks
- Test cases per handler: valid request, missing subscription, store error, edge cases (empty results, boundary timestamps)
- Use `testify/assert` and `testify/require`

#### NATS Handler Utils Tests (`natshandler/utils_test.go`)
- Unit tests for `ParseSubjectTokens` — valid subjects, mismatched patterns, edge cases
- Unit tests for `HandleRequest` — valid payload, malformed JSON, handler error propagation

#### Cassandra Utils Tests (`utils_test.go`)
- Unit tests for `Query` builder (verify state after chaining)
- Tests for `Page[T]` construction

#### Cassandra Integration Tests (`integration_test.go`)
- `//go:build integration` tag
- testcontainers-go with Cassandra module
- `setupCassandra(t)` helper — starts container, creates keyspace + table, returns session, registers `t.Cleanup`
- Tests: insert test messages, verify `GetMessages`, `GetMessagesAfter`, `GetSurroundingMessages`, `GetMessageByID`

#### MongoDB Utils Tests (`utils_test.go`)
- Unit tests for `FindOne` and `FindMany` error handling

#### MongoDB Integration Tests (`integration_test.go`)
- `//go:build integration` tag
- testcontainers-go with MongoDB module
- `setupMongo(t)` helper
- Tests: insert test subscriptions, verify `GetSubscription` returns correct data and handles missing records

### 10. NATS Subjects

The 4 endpoints need subject patterns. Currently only `HistoryRequest` exists in `pkg/subject`. The new subjects follow the established naming convention:

- `chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history` (exists)
- `chat.user.{userID}.request.room.{roomID}.{siteID}.msg.next` (new)
- `chat.user.{userID}.request.room.{roomID}.{siteID}.msg.surrounding` (new)
- `chat.user.{userID}.request.room.{roomID}.{siteID}.msg.get` (new)

Corresponding wildcard subjects for `QueueSubscribe` also need to be added to `pkg/subject`.

## Files Changed

**New files:**
- `history-service/cmd/main.go`
- `history-service/internal/config/config.go`
- `history-service/internal/service/service.go`
- `history-service/internal/service/messages.go`
- `history-service/internal/service/messages_test.go`
- `history-service/internal/service/mocks/mock_store.go`
- `history-service/internal/natshandler/handler.go`
- `history-service/internal/natshandler/utils.go`
- `history-service/internal/natshandler/utils_test.go`
- `history-service/internal/cassrepo/repository.go`
- `history-service/internal/cassrepo/utils.go`
- `history-service/internal/cassrepo/utils_test.go`
- `history-service/internal/cassrepo/integration_test.go`
- `history-service/internal/mongorepo/repository.go`
- `history-service/internal/mongorepo/utils.go`
- `history-service/internal/mongorepo/utils_test.go`
- `history-service/internal/mongorepo/integration_test.go`

**Deleted files (replaced by new structure):**
- `history-service/main.go`
- `history-service/handler.go`
- `history-service/store.go`
- `history-service/store_real.go`
- `history-service/handler_test.go`
- `history-service/mock_store_test.go`
- `history-service/integration_test.go`

**Modified files:**
- `pkg/model/history.go` — add `NextMessagesRequest`, `SurroundingMessagesRequest`, `SurroundingMessagesResponse`, `GetMessageRequest`
- `pkg/subject/subject.go` — add new subject builders for `next`, `surrounding`, `get`
- `Makefile` — update history-service build target to `./history-service/cmd/`

**Unchanged:**
- `deploy/` directory stays as-is
- All other services and `pkg/` packages untouched (except `pkg/model` and `pkg/subject` additions)
