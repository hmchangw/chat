# History-Service Refactor Design

## Overview

Refactor `history-service` from its current flat, pre-generated structure into a well-organized Go service with `cmd/` + `internal/` layout. The service handles 4 NATS request/reply endpoints for message history queries, backed by Cassandra (message storage) and MongoDB (subscription metadata).

## Goals

- Clean folder structure with clear separation of concerns
- Proper dependency injection via interfaces defined in the consumer
- Reusable Cassandra pagination toolkit (generic `Page[T]`, query builder)
- Generic MongoDB `Collection[T]` abstraction with type-safe methods
- Comprehensive tests: unit tests with mocks for service layer, integration tests with testcontainers for repositories
- Solid foundation for future development

## Non-Goals

- Implementing full business logic for all 4 handlers (placeholders/scaffolds are fine)
- Promoting utils to `pkg/` (stays in `internal/` until reviewed)
- Changing other services or shared packages
- End-to-end integration tests (NATS + Cassandra + MongoDB wired together)

## Known Follow-ups

- `model.Message` in `pkg/model/message.go` is missing `bson` tags (CLAUDE.md requires both `json` and `bson`). Will address separately.

## Notes

- **Auth change**: NATS auth is moving from callout service to SSO token → auth service → JWT with permissions built-in. This has no impact on history-service — it receives messages on the wire after authentication is already complete. UserID comes from subject tokens, subscription validation gates access.

## Folder Structure

> **Note:** This service intentionally uses `cmd/` + `internal/` subdirectories instead of the
> standard flat `package main` layout. This deviation is approved because the history-service has
> multiple independent repository packages (cassrepo, mongorepo) and a clean service layer that
> benefit from internal package boundaries and separate testability.

```text
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
│       ├── utils.go               # Generic Collection[T] with FindOne, FindByID, FindMany, Raw
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

No env prefix on embedded structs — env var names stay consistent with the rest of the repo. Connection strings (`NATS_URL`, `MONGO_URI`, `CASSANDRA_HOSTS`) are required with no defaults — fail fast if missing. **Note:** Existing services use `envDefault` for connection strings (e.g., `envDefault:"nats://localhost:4222"`). This spec follows CLAUDE.md's rule ("never default connection strings") as an intentional improvement. For local dev, use `docker-compose.yml` or a `.env` file.

### 2. Service Layer (`internal/service/`)

#### `service.go` — Interfaces, struct, wiring

```go
// MessageRepository defines Cassandra-backed message operations.
type MessageRepository interface {
    // GetMessagesBefore returns messages in a room before `before` and after `since`, ordered newest-first.
    // Used by LoadHistory. Fetches limit+1 to determine HasMore.
    GetMessagesBefore(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error)
    // GetMessagesAfter returns messages in a room after `after`, ordered oldest-first.
    // Used by LoadNextMessages. If `after` is zero, returns the latest messages.
    GetMessagesAfter(ctx context.Context, roomID string, after time.Time, limit int) ([]model.Message, error)
    // GetSurroundingMessages fetches the message by ID within the given room,
    // then returns messages before and after its createdAt. Central message included in `after` slice.
    // roomID is required because Cassandra's partition key is room_id.
    GetSurroundingMessages(ctx context.Context, roomID, messageID string, limit int) (before []model.Message, after []model.Message, err error)
    // GetMessageByID returns a single message by its ID within a room.
    // roomID is required because Cassandra's partition key is room_id.
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

**Timestamp parsing:** Request structs use `string` for timestamp fields (`Before`, `After`, `LastSeen`). Each handler method parses these into `time.Time` via `time.Parse(time.RFC3339Nano, ...)` with proper error handling. Empty string means "no cursor" (e.g., empty `Before` defaults to `time.Now().UTC()`, empty `After` means "get latest messages").

**Default limit:** All endpoints use a default limit of **50** when `limit <= 0` (consistent with existing behavior).

**`HistorySharedSince` enforcement:** Every endpoint must respect the subscription's `HistorySharedSince` timestamp — users must never see messages from before their access window. This is enforced at the **service layer** (not the repo). The service fetches the subscription from MongoDB, extracts `HistorySharedSince`, and passes it as the `since` lower bound to Cassandra queries. For single-message lookups, the service checks `message.CreatedAt >= sub.HistorySharedSince` after fetching. No caching of subscriptions currently — every request hits MongoDB. (Caching is a future optimization.)

1. **`LoadHistory(ctx, userID, req) (*LoadHistoryResponse, error)`** — Messages before a cursor timestamp, paginated backwards.
   - Parses `req.Before` → `before time.Time` (defaults to `time.Now().UTC()` if empty)
   - Parses `req.LastSeen` → `lastSeen time.Time` (zero if empty — means all messages are "read")
   - Fetches subscription → extracts `HistorySharedSince` as `since` lower bound
   - Calls `GetMessagesBefore(ctx, roomID, since, before, limit+1)`
   - Determines `hasMore` from limit+1 trick (fetched limit+1, return limit)
   - Scans loaded batch for `firstUnread`: first message where `createdAt > lastSeen`
   - Request params: `roomID`, `before` (timestamp cursor), `limit` (default 50), `lastSeen` (last seen timestamp)
   - Returns: `messages`, `firstUnread` (nil if none in batch), `hasMore`

2. **`LoadNextMessages(ctx, userID, req) (*LoadNextMessagesResponse, error)`** — Messages after a cursor timestamp, paginated forwards.
   - Parses `req.After` → `after time.Time` (if empty, returns latest messages — repo handles zero time)
   - Fetches subscription → extracts `HistorySharedSince`
   - If `after` is before `HistorySharedSince`, clamps it to `HistorySharedSince` (prevents requesting messages outside access window)
   - Calls `GetMessagesAfter(ctx, roomID, after, limit+1)` — uses limit+1 trick for `hasMore`
   - Request params: `roomID`, `after` (timestamp cursor, empty for latest), `limit` (default 50)
   - Returns: `messages` array, `hasMore`

3. **`LoadSurroundingMessages(ctx, userID, req) (*LoadSurroundingMessagesResponse, error)`** — Messages around a central message.
   - Fetches subscription using `userID` + `req.RoomID`
   - Calls repo `GetSurroundingMessages(ctx, roomID, messageID, limit)` — repo fetches the message by ID, gets `createdAt`, queries before/after within the room
   - Validates `centralMessage.CreatedAt >= sub.HistorySharedSince` — denies access if the central message is outside the access window
   - Filters out any returned messages where `createdAt < HistorySharedSince` (edge case: surrounding query may return messages from before the user's access window)
   - Request params: `roomID`, `messageID`, `limit` (total including central message)
   - Returns: `before` and `after` message arrays (central message included in `after`)

4. **`GetMessageByID(ctx, userID, req) (*Message, error)`** — Single message lookup.
   - Fetches subscription using `userID` + `req.RoomID`
   - Calls repo `GetMessageByID(ctx, roomID, messageID)`
   - Validates `message.CreatedAt >= sub.HistorySharedSince` — returns error if outside access window
   - Request params: `roomID`, `messageID`
   - Returns: single `*Message`

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
// Returns an error if any subscription fails.
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
```

Each `handle*` method follows the same pattern:
1. Extract `userID` and `roomID` from the NATS subject using `subject.ParseUserRoomSubject` (already exists in `pkg/subject`)
2. Unmarshal the request payload into the typed model struct
3. Call the corresponding `HistoryService` method with parsed args
4. Reply via `natsutil.ReplyJSON` on success or `natsutil.ReplyError` with a sanitized error message on failure

#### `utils.go` — Reusable NATS abstractions

```go
// HandleRequest is a generic helper that eliminates unmarshal/reply boilerplate.
// It unmarshals the NATS message payload into Req, calls the handler func,
// and replies with the result via natsutil.ReplyJSON or natsutil.ReplyError.
func HandleRequest[Req, Resp any](msg *nats.Msg, handlerFn func(ctx context.Context, req Req) (*Resp, error))
```

`HandleRequest` reduces each NATS handler to: parse subject via `subject.ParseUserRoomSubject` + call `HandleRequest` with a closure that invokes the service method. Keeps the individual handlers very short. Uses existing `pkg/subject` and `pkg/natsutil` — no custom subject parsing needed.

### 4. New Model Types (`pkg/model/history.go`)

The existing `HistoryRequest`/`HistoryResponse` will be replaced with more precise types. (Verified: only used within history-service files being deleted — no cross-service impact.) All new model types:

```go
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
    After  string `json:"after"  bson:"after"`  // RFC3339Nano cursor — fetch messages after this (empty for latest)
    Limit  int    `json:"limit"  bson:"limit"`  // default 50
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

`GetMessageByID` returns a single `*model.Message` directly.

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

### 6. MongoDB Collection Abstraction (`internal/mongorepo/utils.go`)

A generic `Collection[T]` type that wraps `*mongo.Collection` with type-safe, ergonomic methods. Lives in `internal/mongorepo` for now — can be promoted to `pkg/mongoutil` after review.

```go
// Collection[T] is a type-safe wrapper around *mongo.Collection.
// It handles decoding, ErrNoDocuments normalization, and consistent error wrapping.
type Collection[T any] struct {
    col  *mongo.Collection
    name string // collection name, used for error context
}

// NewCollection creates a typed collection wrapper.
func NewCollection[T any](col *mongo.Collection) *Collection[T] {
    return &Collection[T]{col: col, name: col.Name()}
}

// FindOne returns the first document matching the filter decoded into *T.
// Returns (nil, nil) when no document matches — not an error.
// Only returns a non-nil error for actual failures (network, decode, etc.).
// This eliminates the need for callers to check mongo.ErrNoDocuments.
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

// Raw returns the underlying *mongo.Collection for escape-hatch scenarios
// (aggregation pipelines, bulk writes, etc.) where the typed wrapper is insufficient.
func (c *Collection[T]) Raw() *mongo.Collection { return c.col }
```

**Key design decisions:**
- **`FindOne` returns `(nil, nil)` on not-found** — callers check `if result == nil` instead of `errors.Is(err, mongo.ErrNoDocuments)` everywhere. This is the biggest ergonomic win.
- **Error wrapping includes collection name** — `"finding subscriptions: connection refused"` instead of a generic mongo error. Follows CLAUDE.md's "wrap with context" rule.
- **`FindMany` returns `[]T{}` not `nil`** — consistent for JSON marshaling (empty array, not null).
- **`Raw()` escape hatch** — for when you need raw mongo driver access without fighting the abstraction.
- **No write methods yet** — history-service is read-only. `InsertOne`, `UpdateOne`, etc. can be added when another service adopts the pattern. YAGNI.

### 7. Repository Implementations

#### Cassandra (`internal/cassrepo/repository.go`)

```go
type Repository struct {
    session *gocql.Session
}

func NewRepository(session *gocql.Session) *Repository

// Implements service.MessageRepository
func (r *Repository) GetMessagesBefore(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error)
func (r *Repository) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, limit int) ([]model.Message, error)
func (r *Repository) GetSurroundingMessages(ctx context.Context, roomID, messageID string, limit int) (before []model.Message, after []model.Message, err error)
func (r *Repository) GetMessageByID(ctx context.Context, roomID, messageID string) (*model.Message, error)
```

All methods use `NewQuery` + `ScanPage` from the utils toolkit.

#### MongoDB (`internal/mongorepo/repository.go`)

```go
type Repository struct {
    subscriptions *Collection[model.Subscription]
}

func NewRepository(db *mongo.Database) *Repository {
    return &Repository{
        subscriptions: NewCollection[model.Subscription](db.Collection("subscriptions")),
    }
}

// Implements service.SubscriptionRepository.
// Returns (nil, nil) when the user is not subscribed to the room.
func (r *Repository) GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error) {
    return r.subscriptions.FindOne(ctx, bson.D{
        {Key: "userId", Value: userID},
        {Key: "roomId", Value: roomID},
    })
}
```

The repository is a thin delegation layer — `Collection[T]` handles all the decoding and error wrapping. Adding new query methods is one line each.

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
- Unit tests for `HandleRequest` — valid payload, malformed JSON, handler error propagation, nil response handling

#### Cassandra Utils Tests (`utils_test.go`)
- Unit tests for `Query` builder (verify state after chaining)
- Tests for `Page[T]` construction

#### Cassandra Integration Tests (`integration_test.go`)
- `//go:build integration` tag
- testcontainers-go with Cassandra module
- `setupCassandra(t)` helper — starts container, creates keyspace + `messages` table (schema below), returns session, registers `t.Cleanup`
- Tests: insert test messages, verify `GetMessagesBefore`, `GetMessagesAfter`, `GetSurroundingMessages`, `GetMessageByID`

**Expected Cassandra schema** (used by integration tests, subject to adjustment when real schema is provided):
```cql
CREATE TABLE messages (
    room_id text,
    created_at timestamp,
    id text,
    user_id text,
    content text,
    PRIMARY KEY (room_id, created_at)
) WITH CLUSTERING ORDER BY (created_at DESC);
```

#### MongoDB Utils Tests (`utils_test.go`)
- Unit tests for `FindOne` and `FindMany` error handling

#### MongoDB Integration Tests (`integration_test.go`)
- `//go:build integration` tag
- testcontainers-go with MongoDB module
- `setupMongo(t)` helper
- Tests: insert test subscriptions, verify `GetSubscription` returns correct data and handles missing records

### 10. NATS Subjects

The 4 endpoints need subject patterns. Currently only `HistoryRequest` exists in `pkg/subject`. The new subjects follow the established naming convention:

- `chat.user.{account}.request.room.{roomID}.{siteID}.msg.history` (exists)
- `chat.user.{account}.request.room.{roomID}.{siteID}.msg.next` (new)
- `chat.user.{account}.request.room.{roomID}.{siteID}.msg.surrounding` (new)
- `chat.user.{account}.request.room.{roomID}.{siteID}.msg.get` (new)

New subject builder functions follow the existing `*Wildcard` naming convention:
- `MsgHistoryWildcard(siteID)` — already exists
- `MsgNextWildcard(siteID)` — new
- `MsgSurroundingWildcard(siteID)` — new
- `MsgGetWildcard(siteID)` — new

Corresponding per-user builder functions (for client-side publishing):
- `MsgNext(userID, roomID, siteID)` — new
- `MsgSurrounding(userID, roomID, siteID)` — new
- `MsgGet(userID, roomID, siteID)` — new

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
- `pkg/model/history.go` — replace `HistoryRequest`/`HistoryResponse` with `LoadHistoryRequest`, `LoadHistoryResponse`, `LoadNextMessagesRequest`, `LoadNextMessagesResponse`, `LoadSurroundingMessagesRequest`, `LoadSurroundingMessagesResponse`, `GetMessageByIDRequest`
- `pkg/subject/subject.go` — add new subject builders for `next`, `surrounding`, `get`
- `Makefile` — add per-service build path override for history-service: `./history-service/cmd/` instead of `./history-service/`. `make test SERVICE=history-service` uses `./$(SERVICE)/...` which recurses into `cmd/` and `internal/` correctly — no change needed for test/generate targets.
- `history-service/deploy/Dockerfile` — update build path from `./history-service/` to `./history-service/cmd/`

**Unchanged:**
- `deploy/` directory stays as-is
- All other services and `pkg/` packages untouched (except `pkg/model` and `pkg/subject` additions)
