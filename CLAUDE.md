# Project Guidelines

## Section 1: Project Overview & Architecture

### What This Is

A distributed, multi-site chat system built in Go. Users send messages in rooms, get real-time delivery, and can be federated across independent sites. The system uses NATS JetStream for asynchronous event processing and NATS request/reply for synchronous operations.

### Tech Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.24 |
| Messaging | NATS with JetStream |
| Operational DB | MongoDB (rooms, subscriptions, messages) |
| History DB | Cassandra (message history / time-series) |
| Auth | NATS callout service with JWT + NKeys |
| Config | Environment variables via `caarlos0/env` |
| Observability | OpenTelemetry (tracing), Prometheus (metrics), `log/slog` (logging) |
| Testing | `go.uber.org/mock` (mockgen), `testcontainers-go` (integration) |
| Containers | Docker multi-stage builds, Docker Compose |

### Services & Workers

| Name | Type | Responsibility |
|------|------|----------------|
| `room-service` | NATS service | Room CRUD, member invite validation |
| `auth-service` | NATS callout | SSO token verification, JWT issuance |
| `message-worker` | JetStream consumer | Process messages, store in MongoDB + Cassandra |
| `room-worker` | JetStream consumer | Process member invites, create subscriptions |
| `broadcast-worker` | JetStream consumer | Fan out messages to room members |
| `notification-worker` | JetStream consumer | Send notifications to users |
| `inbox-worker` | JetStream consumer | Process cross-site events from other sites |

### Event Flow

1. User publishes message → **MESSAGES** stream
2. `message-worker` consumes → stores message → publishes to **FANOUT** stream
3. `broadcast-worker` consumes FANOUT → delivers to each room member's stream
4. `notification-worker` consumes FANOUT → sends notifications
5. For cross-site: `room-worker` publishes to **OUTBOX** → remote site's **INBOX** → `inbox-worker` processes

### Multi-Site Federation

Each site runs independently with its own NATS, MongoDB, and Cassandra. Cross-site events flow via the Outbox/Inbox pattern:
- **OUTBOX stream**: Local events destined for remote sites
- **INBOX stream**: Sources from remote sites' OUTBOX streams via JetStream Sources
- User subscriptions and room metadata are scoped by `siteID`

## Section 2: Project Structure

### Repository Layout

```
chat/
├── pkg/                        # Shared packages (used by all services)
│   ├── model/                  # Domain entities and events
│   ├── subject/                # NATS subject builders
│   ├── stream/                 # JetStream stream configurations
│   ├── natsutil/               # NATS reply helpers, OTel carrier
│   ├── mongoutil/              # MongoDB connect/disconnect
│   ├── cassutil/               # Cassandra connect/close
│   ├── otelutil/               # OpenTelemetry initialization
│   └── shutdown/               # Graceful shutdown signal handler
├── room-service/               # Room CRUD + invite validation
│   ├── main.go
│   ├── handler.go
│   ├── store.go                # Interface + mockgen directive
│   ├── store_mongo.go
│   ├── handler_test.go
│   ├── integration_test.go
│   ├── mock_store_test.go
│   └── deploy/
│       ├── Dockerfile
│       └── docker-compose.yml
├── auth-service/               # NATS auth callout
├── message-worker/             # Message processing worker
├── room-worker/                # Member invite worker
├── broadcast-worker/           # Message fan-out worker
├── notification-worker/        # Notification worker
├── inbox-worker/               # Cross-site event worker
├── go.mod
├── go.sum
└── CLAUDE.md
```

### Conventions

- **Monorepo**: Single `go.mod` at root. All services import `github.com/hmchangw/chat/pkg/...`.
- **Flat service directories**: Each service is a `package main` directory at the repo root. No nested `cmd/` or `internal/` — keep it flat.
- **Shared code in `pkg/`**: Only code used by 2+ services goes in `pkg/`. Service-specific logic stays in the service directory.
- **Deploy artifacts**: Each service has a `deploy/` subdirectory containing its `Dockerfile` and `docker-compose.yml`.
- **No global `cmd/` directory**: Services are top-level directories, not nested under `cmd/`.

## Section 3: Go Coding Standards

### Naming

- **Packages**: Short, lowercase, single-word. No underscores or mixedCaps (e.g., `natsutil`, `mongoutil`).
- **Interfaces**: Name by what they do, not what they are. Use the `-er` suffix for single-method interfaces. For store interfaces, use `<Domain>Store` (e.g., `RoomStore`, `MessageStore`, `HistoryStore`).
- **Constructors**: Use `New<Type>` (e.g., `NewMongoStore`, `NewHandler`).
- **Exported vs unexported**: Export types and functions that other packages consume. Keep handler/store implementations unexported when only used within the service.

### Error Handling

- **Always wrap with context**: Use `fmt.Errorf("short description: %w", err)`. The description should say what the current function was trying to do, not what failed underneath.
  ```go
  // Good
  return fmt.Errorf("create room: %w", err)

  // Bad
  return err
  return fmt.Errorf("error: %w", err)
  ```
- **Never ignore errors silently**. If you intentionally discard an error, add a comment explaining why.
- **Error responses**: Use `model.ErrorResponse` (`{"error": "message"}`) for all NATS reply errors via `natsutil.ReplyError`.

### Interfaces & Dependency Injection

- **Define interfaces in the consumer**, not the implementer. Each service defines its own store interface in `store.go` with only the methods it needs.
- **Accept interfaces, return structs**. Constructors return concrete types; callers assign to interface variables.
- **Handler structs hold dependencies** injected via constructor:
  ```go
  type Handler struct {
      store RoomStore
      nc    *nats.Conn
      site  string
  }
  ```

### Struct Tags

- **All model structs** get both `json` and `bson` tags for JSON serialization and MongoDB storage.
- **Use `_id` for MongoDB primary keys**: `bson:"_id"` mapped to the `ID` field.
  ```go
  type Room struct {
      ID   string `json:"id" bson:"_id"`
      Name string `json:"name" bson:"name"`
  }
  ```

### Logging

- **Structured logging**: Always use `log/slog` with JSON format for structured logging. Never use `fmt.Println`, `log.Println`, or text-format loggers.

### Code Organization (Per Service)

- `main.go` — Config parsing, dependency wiring, startup, graceful shutdown
- `handler.go` — Request/message handling logic
- `store.go` — Store interface definition + `//go:generate mockgen` directive
- `store_mongo.go` / `store_real.go` — Store implementation
- `handler_test.go` — Unit tests with mocked store
- `integration_test.go` — Integration tests with testcontainers (tagged `//go:build integration`)
- `mock_store_test.go` — Generated mocks (do not edit manually)

## Section 4: Testing Standards

### Unit Tests

- **Framework**: Use the standard `testing` package. No third-party test frameworks (no testify, no ginkgo).
- **Mocking**: Use `go.uber.org/mock` (mockgen) for generating interface mocks. Add `//go:generate mockgen` directives in `store.go`. Generated mocks go in `mock_store_test.go` — never edit them manually.
- **Pattern**: Create the mock controller, set expectations, inject into the handler, call the method, assert results:
  ```go
  ctrl := gomock.NewController(t)
  store := NewMockRoomStore(ctrl)
  store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
  h := &Handler{store: store, siteID: "site-a"}
  ```
- **Test file location**: `handler_test.go` in the same package as the handler (`package main`). This allows testing unexported types directly.
- **Naming**: Use `Test<Type>_<Method>` or `Test<Type>_<Method>_<Scenario>` (e.g., `TestHandler_CreateRoom`, `TestHandler_InviteOwner_Success`).
- **No real dependencies**: Unit tests never connect to databases, NATS, or external services. Mock everything.
- **Function injection for publishers**: When a handler publishes to JetStream, inject the publish function as a field (e.g., `publishToStream func(data []byte) error`) so tests can capture published data without a real NATS connection.

### Integration Tests

- **Build tag**: All integration tests use `//go:build integration` to exclude from `go test ./...`.
- **Containers**: Use `testcontainers-go` with official modules (`modules/mongodb`, `modules/cassandra`, `modules/nats`) to spin up real dependencies in Docker.
- **Setup helpers**: Write `setup<Dep>(t *testing.T)` functions that start a container, register cleanup via `t.Cleanup`, and return a connected client:
  ```go
  func setupMongo(t *testing.T) *mongo.Database {
      t.Helper()
      ctx := context.Background()
      container, err := mongodb.Run(ctx, "mongo:8")
      // ... connect, t.Cleanup(terminate), return db
  }
  ```
- **Test file**: `integration_test.go` in the service directory.
- **Database naming**: Use `<service>_test` as the database name in integration tests to avoid collision.

### Model Tests

- **JSON round-trip**: `pkg/model/model_test.go` verifies all domain types marshal and unmarshal correctly via a generic `roundTrip` helper.

### Running Tests

- **Unit tests**: `go test ./...` (excludes integration tests by default).
- **Integration tests**: `go test -tags integration ./service-name/` (requires Docker).
- **Generate mocks**: `go generate ./...` before running tests if store interfaces changed.

## Section 5: NATS & Messaging

### Connection & Lifecycle

- **Library**: Use `github.com/nats-io/nats.go` for NATS core and `github.com/nats-io/nats.go/jetstream` for JetStream.
- **Connection**: Connect in `main.go`. On failure, log and exit immediately — don't retry at startup.
- **Shutdown**: Use `nc.Drain()` for graceful shutdown. This flushes pending messages before closing the connection. Register drain in `shutdown.Wait`.

### Subject Naming

- **Convention**: Dot-delimited hierarchical subjects. Use `pkg/subject` builders — never construct subjects with raw `fmt.Sprintf` in service code.
- **Structure**:
  - User-scoped: `chat.user.{userID}.…`
  - Room-scoped: `chat.room.{roomID}.…`
  - Fanout: `fanout.{siteID}.{roomID}.{msgID}`
  - Outbox: `outbox.{siteID}.to.{destSiteID}.{eventType}`
- **Wildcards**: Use `*` for single-token wildcards, `>` for multi-token tail wildcards. Define wildcard patterns in `pkg/subject` (e.g., `MsgSendWildcard`, `FanoutWildcard`).

### JetStream Streams

- **Configuration**: Define all stream configs in `pkg/stream/stream.go`. Each stream config includes a name pattern (`<STREAM>_<siteID>`) and subject filters.
- **Streams**:
  - `MESSAGES_{siteID}` — User message submissions
  - `FANOUT_{siteID}` — Broadcast messages for fan-out
  - `ROOMS_{siteID}` — Member invite requests
  - `OUTBOX_{siteID}` — Cross-site outbound events
  - `INBOX_{siteID}` — Cross-site inbound events (sourced from remote OUTBOX)
- **Stream creation**: Use `js.CreateOrUpdateStream` in `main.go` at startup. This is idempotent.
- **Consumers**: Use durable consumers named after the service (e.g., `message-worker`, `room-worker`). This ensures messages are not lost on restart.

### Request/Reply

- **Pattern**: Use NATS request/reply for synchronous operations (e.g., room CRUD). The service subscribes with `nc.QueueSubscribe` for load balancing.
- **Response format**: Always use `natsutil.ReplyJSON` for success and `natsutil.ReplyError` for errors. Both produce JSON payloads.
- **Queue groups**: Use service name as the queue group to distribute requests across instances.

### Message Serialization

- **Format**: All NATS message payloads are JSON. Use `encoding/json` for marshal/unmarshal.
- **Types**: Define request/response types in `pkg/model`. Never use `map[string]interface{}`.

## Section 6: Database Standards

### MongoDB

- **Driver**: Use `go.mongodb.org/mongo-driver/v2`.
- **Connection**: Use `mongoutil.Connect` from `pkg/mongoutil`. It connects, pings, and returns the client.
- **Database naming**: Use `MONGO_DB` env var (default: `chat`). Integration tests use `chat_test`.
- **Collections**: Name collections after the domain entity in lowercase plural (e.g., `rooms`, `subscriptions`, `messages`).
- **Primary keys**: Use `_id` field with application-generated UUIDs (`github.com/google/uuid`). Map to `bson:"_id"` on the struct's `ID` field.
- **Struct tags**: All model structs have both `json` and `bson` tags. Use `camelCase` for `json` tags, `camelCase` for `bson` tags, except `_id` for the primary key.
- **Queries**: Use the driver's typed filter builders where possible. For simple filters, `bson.M{"field": value}` is acceptable.
- **Error handling**: Wrap all MongoDB errors with context (`fmt.Errorf("create room: %w", err)`). Check for `mongo.ErrNoDocuments` explicitly when a missing record is expected.
- **Indexes**: Create indexes in the store constructor or a dedicated `EnsureIndexes` method called at startup.

### Cassandra

- **Driver**: Use `github.com/gocql/gocql`.
- **Connection**: Use `cassutil.Connect` from `pkg/cassutil`. It configures `LocalQuorum` consistency and a 10-second timeout.
- **Use case**: Cassandra is for message history (time-series queries). MongoDB handles everything else.
- **Consistency**: Default to `LocalQuorum` for reads and writes.
- **Schema**: Design tables around query patterns (partition key = room ID, clustering key = timestamp). Avoid secondary indexes.

### General Database Rules

- **No ORM**: Use the native drivers directly. No GORM, no ent.
- **Store pattern**: Each service defines a store interface in `store.go` with only the methods it needs. The implementation goes in `store_mongo.go` (or `store_cassandra.go`).
- **Context propagation**: All store methods accept `context.Context` as the first parameter for timeout and cancellation support.
- **Connection lifecycle**: Connect in `main.go`, pass the client/session to the store constructor, disconnect in `shutdown.Wait`.

## Section 7: API & HTTP Standards

### HTTP Server

- **Framework**: Use the [Gin](https://github.com/gin-gonic/gin) library for all HTTP servers. Do not use `net/http` mux directly.
- **Router**: Register all routes in a dedicated `routes.go` file — never in `main.go`. The `routes.go` file should expose a function (e.g., `SetupRoutes(r *gin.Engine)`) that `main.go` calls.
- **Request/Response format**: All APIs use JSON. Define request and response structs explicitly — no `map[string]interface{}`.
- **Validation**: Validate all incoming request bodies at the handler level using Gin's binding/validation before passing to the service layer. Return `400 Bad Request` with a descriptive error message for invalid input.
- **Error responses**: Use a consistent error response format across all services: `{"error": "human readable message"}`. Map domain errors to appropriate HTTP status codes in the handler — don't leak internal details.
- **Middleware**: Chain middleware where the code is cleanest and most maintainable. For global middleware (logging, panic recovery, CORS), attach in `routes.go` alongside route registration. For route-group-specific middleware (auth, rate limiting), attach on the relevant `gin.RouterGroup`. Keep `main.go` focused on server lifecycle, not middleware wiring.
- **Health checks**: Every HTTP service exposes `GET /healthz` that returns `200 OK` when the service is ready to accept traffic.
- **Timeouts**: Set read/write timeouts on `http.Server` wrapping the Gin engine. Use `context.WithTimeout` for downstream calls (DB, NATS, external APIs) within handlers.
- **Context propagation**: Extract request-scoped values (user ID, trace ID) into `context.Context` in middleware and pass through to service and store layers.

### Outbound HTTP Calls

- **Library**: Use [Resty](https://github.com/go-resty/resty) for all outbound HTTP calls to third-party dependencies. Do not use `net/http` client directly for external calls.
- **Timeouts**: Always set a request timeout on the Resty client to protect against long waits. Example:
  ```go
  client := resty.New().SetTimeout(10 * time.Second)
  ```
- **Testing**: Use Resty's recommended testing framework (`httpmock` via `jarcoal/httpmock` or Resty's built-in test utilities) for unit testing outbound HTTP calls. Mock external dependencies — never make real HTTP calls in unit tests.
- **Error handling**: Always check both the error return and the HTTP status code from Resty responses. Wrap errors with context before returning.

## Section 8: Logging & Observability

### Logging

- **Library**: Use `log/slog` (Go standard library structured logging). No third-party logging libraries.
- **Initialization**: Initialize the default logger in `main.go` using `slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))`. Other files use `slog` directly (e.g., `slog.Info(...)`) — no need to pass the logger via dependency injection.
- **Format**: Always use JSON output. Never use text format, even in development.
- **Log levels**: Use `slog.Info` for normal operations, `slog.Error` for failures that need attention, `slog.Debug` for development diagnostics. Never log at `Warn` unless there's a degraded-but-recoverable situation.
- **Structured fields**: Always attach context as key-value pairs, not interpolated strings. Example: `slog.Error("failed to send message", "room_id", roomID, "err", err)` — not `slog.Error(fmt.Sprintf("failed to send message to room %s: %v", roomID, err))`.
- **Sensitive data**: Never log tokens, passwords, or full message bodies. Log IDs and metadata only.

### Request Logging & Tracing

- **Request logging**: For HTTP services (Gin), use a middleware that logs method, path, status code, latency, and request ID on every request.
- **Trace/Request ID**: Generate or extract a unique request/correlation ID at the entry point (HTTP middleware or NATS message handler) and propagate it through `context.Context`. Include it in all log lines.

### Metrics

- **Library**: If metrics are needed, use Prometheus client library (`prometheus/client_golang`). Expose metrics on a separate port via `/metrics` endpoint.

## Section 9: Configuration & Environment

- **Source**: All configuration comes from environment variables. No config files (YAML, TOML, JSON) unless there's a strong reason.
- **Loading**: Read environment variables in `main.go` at startup. Parse them into a typed `Config` struct. Pass the struct (or its fields) to constructors — services and handlers never read `os.Getenv` directly.
- **Validation**: Validate all required config at startup. If a required variable is missing or invalid, log the error and exit immediately with a non-zero exit code. Fail fast — don't let the service start in a broken state.
- **Naming**: Use `SCREAMING_SNAKE_CASE` for environment variable names. Prefix with the service name for service-specific vars (e.g., `AUTH_SERVICE_PORT`, `HISTORY_SERVICE_CASSANDRA_HOSTS`). Use unprefixed names for shared vars (e.g., `NATS_URL`, `MONGO_URI`).
- **Defaults**: Provide sensible defaults for non-critical config (e.g., port, log level). Never default secrets or connection strings — require them explicitly.
- **Secrets**: Secrets (tokens, keys, passwords) come from environment variables. Never hardcode them. Never log them. In production, inject via the orchestrator's secret management (e.g., Kubernetes Secrets).
- **`.env` files**: Use `.env` files for local development only. Add `.env` to `.gitignore`. Never commit `.env` files.

## Section 10: Deployment & Docker

### Dockerfiles

- **Multi-stage builds**: Every service uses a two-stage Dockerfile:
  1. **Builder stage** (`golang:1.24-alpine`): Copy `go.mod`, `go.sum`, download deps, copy `pkg/` and the service directory, build with `CGO_ENABLED=0`.
  2. **Runtime stage** (`alpine:3.21`): Install `ca-certificates`, copy the binary, set `ENTRYPOINT`.
  ```dockerfile
  FROM golang:1.24-alpine AS builder
  WORKDIR /app
  COPY go.mod go.sum ./
  RUN go mod download
  COPY pkg/ pkg/
  COPY <service>/ <service>/
  RUN CGO_ENABLED=0 go build -o /<service> ./<service>/

  FROM alpine:3.21
  RUN apk add --no-cache ca-certificates
  COPY --from=builder /<service> /<service>
  ENTRYPOINT ["/<service>"]
  ```
- **Location**: `<service>/deploy/Dockerfile`.
- **Build context**: Always set to the repo root (`../..` from the deploy directory) so `pkg/` and `go.mod` are accessible.
- **No `.dockerignore` tricks**: Keep builds simple — copy only what's needed explicitly.

### Docker Compose

- **Purpose**: Local development and testing only. Not for production.
- **Location**: `<service>/deploy/docker-compose.yml`.
- **Infrastructure services**: Include only the dependencies the service needs (e.g., NATS with JetStream, MongoDB, Cassandra).
- **NATS config**: Always enable JetStream (`--jetstream`) and the HTTP monitoring port (`--http_port 8222`).
- **Environment variables**: Define all required env vars in the `environment` block. Use service names as hostnames (e.g., `nats://nats:4222`, `mongodb://mongodb:27017`).
- **`depends_on`**: List infrastructure dependencies so services start in order.

### Graceful Shutdown

- **Pattern**: Use `pkg/shutdown.Wait` in every service's `main.go`. It blocks on `SIGINT`/`SIGTERM`, then calls cleanup functions sequentially.
- **Cleanup order**: Drain NATS first (stop receiving, flush pending), then disconnect databases.
  ```go
  shutdown.Wait(ctx,
      func(ctx context.Context) error { nc.Drain(); return nil },
      func(ctx context.Context) error { mongoutil.Disconnect(ctx, client); return nil },
  )
  ```
