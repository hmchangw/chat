# natsrouter

Gin-style pattern-based routing for NATS request/reply services.

Handles subject pattern matching, parameter extraction, JSON marshal/unmarshal, middleware, admission control, panic safety, and graceful shutdown — so your handlers focus on business logic.

## Quick Start

```go
nc, _ := natsutil.Connect(natsURL, credsFile)
router := natsrouter.New(nc, "my-service",
    natsrouter.WithMaxConcurrency(100),
)
router.Use(natsrouter.Recovery())
router.Use(natsrouter.HandlerTimeout(5 * time.Second))
router.Use(natsrouter.Logging())

natsrouter.Register(router, "chat.user.{account}.msg.send", svc.SendMessage)

// On shutdown:
router.Shutdown(ctx)
nc.Drain()
```

## Table of Contents

- [Installation](#installation)
- [Core Concepts](#core-concepts)
- [Concurrency Model](#concurrency-model)
- [API Reference](#api-reference)
- [Registration Functions](#registration-functions)
- [Context](#context)
- [Middleware](#middleware)
- [Error Handling](#error-handling)
- [Pattern Routing](#pattern-routing)
- [Panic Safety](#panic-safety)
- [Shutdown](#shutdown)
- [Testing](#testing)
- [Scope and Limitations](#scope-and-limitations)
- [Examples](#examples)

## Installation

```bash
go get github.com/hmchangw/chat/pkg/natsrouter
```

## Core Concepts

natsrouter is designed for **NATS request/reply** endpoints — a client sends a request to a subject and waits for a response. The router:

1. Subscribes to NATS subjects using **queue groups** (load balancing across instances)
2. Converts `{param}` patterns to NATS wildcards at registration time
3. Extracts params from incoming subjects at request time
4. Enforces a **per-process concurrency cap** with admission control (default 100, override with `WithMaxConcurrency`)
5. Spawns each accepted message into its own goroutine for parallel handler execution
6. Runs the **middleware chain** (Gin-style `c.Next()` / `c.Abort()`)
7. Unmarshals JSON request bodies into typed Go structs
8. Calls your handler with a `*Context` (implements `context.Context`)
9. Marshals the response back as JSON
10. Tracks every spawned handler in a `WaitGroup` so `Shutdown` can wait for in-flight work

## Concurrency Model

The router admits at most N concurrent handler invocations per process across all routes registered on it (default 100). Admission is enforced by a non-blocking acquire on a semaphore inside the per-subscription dispatcher callback:

- **Acquire success** → spawn a goroutine that runs the middleware chain + handler. The goroutine releases the semaphore on return.
- **Acquire failure (saturated)** → publish an `ErrUnavailable` reply (`{"error":"service busy","code":"unavailable"}`) immediately and return. Callers should retry with backoff.

```go
// Tune per environment via env var:
//   ROUTER_MAX_CONCURRENCY=200
router := natsrouter.New(nc, "my-service",
    natsrouter.WithMaxConcurrency(cfg.Router.MaxConcurrency),
)
```

### Important properties

- **Per-route overrides are not supported today.** A single router-wide semaphore covers every route. The `Registrar` interface is intentionally minimal so a future wrapper (e.g. a route group with its own admission semaphore) can be added without breaking the existing API. Route-level isolation should wait until real evidence of noisy-neighbor contention surfaces in production.

- **Per-subject FIFO ordering is NOT preserved.** Two messages that arrive on the same subscription are spawned into independent goroutines and race; whichever wins the goroutine schedule runs first. Handlers must be idempotent or use external coordination (Cassandra LWTs, Mongo conditional updates) to ensure correctness under concurrent invocation.

- **Fire-and-forget routes drop silently under saturation.** `RegisterVoid` handlers have no NATS reply subject. When a fire-and-forget message arrives while the semaphore is saturated, the router has no reply channel on which to publish `ErrUnavailable`, so the message is silently dropped (with a `slog.Warn` log line). Size `MaxConcurrency` conservatively for services that expose `RegisterVoid` endpoints, or front them with JetStream so dropped messages can be redelivered.

- **Queue-group fairness shifts under saturation.** NATS queue-group routing distributes messages among subscribers without knowing whether any individual subscriber's process-level admission control is full. A saturated pod will continue to receive (and busy-reply) its share of messages even while other pods sit idle. Monitor the per-pod busy-reply rate as a real SLI rather than assume queue-group routing alone provides load balancing.

## API Reference

### Router

```go
// Create a router with a NATS connection and queue group name.
// Queue group ensures only one instance handles each request (load balancing).
//
// Variadic options preserve backward compatibility for existing callers.
func New(nc *otelnats.Conn, queue string, opts ...Option) *Router

// Append middleware to the router's chain. Runs for ALL routes.
func (r *Router) Use(mw ...HandlerFunc)

// Drain every registered route, wait for dispatcher goroutines to exit
// (SubscriptionClosed), then wait for spawned handler goroutines to
// finish. Returns when all in-flight handlers have completed or ctx
// expires (whichever comes first). Idempotent. Call before nc.Drain()
// if you need to stop routing while keeping the NATS connection open.
func (r *Router) Shutdown(ctx context.Context) error
```

### Options

```go
// Option configures a Router on construction.
type Option func(*Router)

// WithMaxConcurrency sets the maximum number of in-flight handler
// invocations across all routes registered on this router. Defaults
// to the constructor default (100). Non-positive values are ignored;
// the constructor default applies in that case. If multiple
// WithMaxConcurrency options are supplied, the last one takes effect.
// Saturation triggers a 503-style ErrUnavailable reply.
func WithMaxConcurrency(n int) Option
```

### Registration Functions

All accept a `Registrar` (currently `*Router`).

```go
// Request body + JSON response. The standard request/reply handler.
func Register[Req, Resp any](
    r Registrar,
    pattern string,
    fn func(c *Context, req Req) (*Resp, error),
)

// No request body, JSON response. For GET-style lookups where all data is in the subject.
func RegisterNoBody[Resp any](
    r Registrar,
    pattern string,
    fn func(c *Context) (*Resp, error),
)

// Request body, no response. For fire-and-forget events.
// CAUTION: messages on saturated routers are silently dropped (see Concurrency Model).
func RegisterVoid[Req any](
    r Registrar,
    pattern string,
    fn func(c *Context, req Req) error,
)
```

All three **panic** if the NATS subscription fails. This is intentional — registration happens at startup, and a failed subscription means the service cannot function (same pattern as `http.HandleFunc`).

### Context

`*Context` implements `context.Context` and can be passed directly to database calls.

```go
// Named parameter from the subject. Shortcut for c.Params.Get(key).
func (c *Context) Param(key string) string

// Key-value store for middleware-to-handler data passing.
func (c *Context) Set(key string, value any)
func (c *Context) Get(key string) (any, bool)
func (c *Context) MustGet(key string) any

// Replace the underlying context.Context. Used by HandlerTimeout to
// install a deadline. Single-writer contract: call only from middleware
// before c.Next(); never concurrently with handler-spawned goroutines.
func (c *Context) SetContext(ctx context.Context)

// Continue the middleware chain. Must be called from middleware to proceed.
func (c *Context) Next()

// Stop the middleware chain. Subsequent handlers will not run.
func (c *Context) Abort()

// Check if the chain was aborted.
func (c *Context) IsAborted() bool

// Reply helpers.
func (c *Context) ReplyJSON(v any)
func (c *Context) ReplyError(msg string)
func (c *Context) ReplyRouteError(e *RouteError)

// The raw NATS message (for advanced use cases).
c.Msg *nats.Msg

// The full Params struct (for iteration or Require).
c.Params Params
```

### Params

```go
// Get a param value by name. Returns "" if not found.
func (p Params) Get(key string) string

// Get a param value. Panics if not found (developer error — pattern mismatch).
func (p Params) MustGet(key string) string

// Get a param value. Returns a user-facing error if missing or empty.
func (p Params) Require(key string) (string, error)

// Create params from a map (for testing).
func NewParams(values map[string]string) Params
```

### HandlerFunc

```go
// The universal function type for handlers and middleware.
type HandlerFunc func(c *Context)

// Middleware is a type alias for HandlerFunc (for documentation clarity).
type Middleware = HandlerFunc
```

### RouteError

```go
// User-facing error with optional machine-readable code.
type RouteError struct {
    Message string `json:"error"`
    Code    string `json:"code,omitempty"`
}

// Constructors.
func Err(message string) *RouteError
func Errf(format string, args ...any) *RouteError
func ErrWithCode(code, message string) *RouteError

// Convenience constructors with standard codes.
func ErrBadRequest(message string) *RouteError    // code: "bad_request"
func ErrNotFound(message string) *RouteError      // code: "not_found"
func ErrForbidden(message string) *RouteError     // code: "forbidden"
func ErrConflict(message string) *RouteError      // code: "conflict"
func ErrInternal(message string) *RouteError      // code: "internal"
func ErrUnavailable(message string) *RouteError   // code: "unavailable"

// Standard error code constants.
const (
    CodeBadRequest  = "bad_request"
    CodeNotFound    = "not_found"
    CodeForbidden   = "forbidden"
    CodeConflict    = "conflict"
    CodeInternal    = "internal"
    CodeUnavailable = "unavailable"  // emitted by admission control
)
```

`ErrUnavailable` is the structured reply emitted automatically by the router when the admission semaphore is saturated. Application code can also emit it explicitly to signal a recoverable, retry-worthy condition (e.g. mapping `context.DeadlineExceeded` from a downstream call — see `HandlerTimeout` doc).

### Built-in Middleware

```go
// Generates or extracts a request ID (from X-Request-ID header or new UUID).
// Stores it via c.Set("requestID", id). Recovery and Logging include it automatically.
func RequestID() HandlerFunc

// Catches panics, logs them with request ID, replies with "internal error".
// Recommended even though the router has its own spawn-site panic backstop —
// Recovery produces structured replies enriched with middleware-set fields
// (request ID, etc.); the spawn-site backstop is a defense-in-depth catch.
func Recovery() HandlerFunc

// Logs subject, duration, and request ID for each request.
func Logging() HandlerFunc

// Wraps the handler context with a deadline of d. Downstream calls that
// respect context (Cassandra/Mongo drivers, otelnats.Conn.Publish, etc.)
// will abort if the chain runs longer than d. The deadline is released
// when the chain returns. Place AFTER Recovery + RequestID and BEFORE
// Logging so the duration logged includes deadline-related wait.
//
// Caveat — does NOT actively interrupt non-context-aware handlers; CPU-
// bound code will run past the deadline. Recommended pattern when a
// downstream call returns context.DeadlineExceeded:
//   if errors.Is(err, context.DeadlineExceeded) {
//       return nil, natsrouter.ErrUnavailable("request timed out")
//   }
func HandlerTimeout(d time.Duration) HandlerFunc
```

### NewContext (Testing)

```go
// Create a Context for testing handlers without a live NATS connection.
func NewContext(params map[string]string) *Context
```

## Registration Functions

Three handler shapes for three use cases:

| Function | Request Body | Response | Use Case |
|----------|-------------|----------|----------|
| `Register[Req, Resp]` | Yes | Yes | Standard request/reply (most endpoints) |
| `RegisterNoBody[Resp]` | No | Yes | GET-style lookups where subject has all info |
| `RegisterVoid[Req]` | Yes | No | Fire-and-forget events (silent drop on saturation) |

```go
// Request/reply — the most common pattern.
natsrouter.Register(router, "chat.user.{account}.msg.send",
    func(c *natsrouter.Context, req SendRequest) (*SendResponse, error) {
        account := c.Param("account")
        // ... business logic ...
        return &SendResponse{ID: msg.ID}, nil
    })

// GET-style — no request body needed.
natsrouter.RegisterNoBody(router, "chat.user.{account}.rooms.get.{roomID}",
    func(c *natsrouter.Context) (*Room, error) {
        return store.FindRoom(c, c.Param("roomID"))
    })

// Fire-and-forget — no response sent. Silently dropped on router saturation.
natsrouter.RegisterVoid(router, "chat.user.{account}.event.typing",
    func(c *natsrouter.Context, req TypingEvent) error {
        return broadcast(c, c.Param("account"), req)
    })
```

## Context

Every handler receives `*Context`, which implements `context.Context`. You can pass it directly to database calls:

```go
func (s *Service) GetRoom(c *natsrouter.Context, req GetRoomReq) (*Room, error) {
    // c is a context.Context — pass it to DB calls for deadline/cancellation.
    room, err := s.store.FindByID(c, req.ID)
    if err != nil {
        return nil, fmt.Errorf("finding room: %w", err)
    }
    if room == nil {
        return nil, natsrouter.ErrNotFound("room not found")
    }
    return room, nil
}
```

If `HandlerTimeout` middleware is installed, `c` carries the timeout deadline and downstream context-aware calls abort cleanly when it expires.

### Using the Context in Goroutines

Pass `c` (or any descendant `context.Context` derived via `context.WithTimeout` etc.) freely to anything that expects a `context.Context` — `http.NewRequestWithContext`, database drivers, async goroutines. The pool only recycles the middleware chain-state; every field you can observe on `*Context` (`ctx`, `Msg`, `Params`, `keys`) is set once at request entry, so a goroutine holding `c` after the handler returns cannot race pool reuse.

```go
func MetricsMiddleware() natsrouter.HandlerFunc {
    return func(c *natsrouter.Context) {
        start := time.Now()
        c.Next()
        go func() {
            emitMetric(c.Msg.Subject, time.Since(start), c.Param("account"))
        }()
    }
}
```

Rule of thumb: treat `*Context` exactly like a `*http.Request` — safe to pass to downstream ctx consumers, safe to capture in goroutines. Do not call `Respond` on `c.Msg` from a background goroutine once the handler has already replied; publish to a fresh subject instead.

**One subtlety with `HandlerTimeout`:** the middleware's `defer cancel()` fires when the chain returns. A goroutine retaining `*Context` and reading `c.Done()` after handler return will see a cancelled context (`DeadlineExceeded` or `Canceled`). This matches `gin.Context.Request.Context()` semantics and is rarely a problem in practice — `c.Param`, `c.Get`, `c.Set` go through the params/keys map, not `c.ctx`, and remain valid.

### Middleware Data Passing

Middleware can store values that handlers read:

```go
// Auth middleware stores the user.
func AuthMiddleware() natsrouter.HandlerFunc {
    return func(c *natsrouter.Context) {
        token := c.Msg.Header.Get("Authorization")
        user, err := validateToken(token)
        if err != nil {
            c.ReplyError("unauthorized")
            c.Abort()
            return
        }
        c.Set("user", user)
        c.Next()
    }
}

// Handler reads it.
func (s *Service) CreateRoom(c *natsrouter.Context, req CreateReq) (*Room, error) {
    user := c.MustGet("user").(User)
    // ...
}
```

## Middleware

Middleware is a `HandlerFunc` that calls `c.Next()` to continue the chain:

```go
func RequestIDMiddleware() natsrouter.HandlerFunc {
    return func(c *natsrouter.Context) {
        reqID := c.Msg.Header.Get("X-Request-ID")
        if reqID == "" {
            reqID = uuid.New().String()
        }
        c.Set("requestID", reqID)
        c.Next()
    }
}
```

### Recommended Ordering

```go
router.Use(natsrouter.Recovery())                       // first — catches panics from anything below
router.Use(natsrouter.RequestID())                      // before HandlerTimeout so the timeout's logs/replies have IDs
router.Use(natsrouter.HandlerTimeout(5 * time.Second))  // before Logging so logged duration includes timeout wait
router.Use(natsrouter.Logging())                        // last — logs final duration
// custom middleware (auth, rate-limit, …) goes between RequestID and Logging
```

### Execution Order

```text
router.Use(A, B, C) + handler

A:before → B:before → C:before → handler → C:after → B:after → A:after
```

### Short-Circuiting

Don't call `c.Next()` (and optionally call `c.Abort()`) to stop the chain:

```go
func RateLimiter() natsrouter.HandlerFunc {
    return func(c *natsrouter.Context) {
        if isRateLimited(c.Param("account")) {
            c.ReplyError("rate limited")
            c.Abort()
            return
        }
        c.Next()
    }
}
```

## Error Handling

Handlers return Go errors. The router distinguishes user-facing from internal:

```go
func (s *Service) GetRoom(c *natsrouter.Context, req GetReq) (*Room, error) {
    room, err := s.store.Find(c, req.ID)
    if err != nil {
        // Internal error — logged, client sees: {"error":"internal error"}
        return nil, fmt.Errorf("finding room: %w", err)
    }
    if room == nil {
        // User-facing error — client sees: {"error":"room not found","code":"not_found"}
        return nil, natsrouter.ErrNotFound("room not found")
    }
    return room, nil
}
```

RouteErrors can be wrapped and still detected:

```go
return nil, fmt.Errorf("access check: %w", natsrouter.ErrForbidden("denied"))
// Client still receives: {"error":"denied","code":"forbidden"}
```

### Mapping Timeouts and Cancellation

When `HandlerTimeout` (or any other context source) cancels a request mid-flight, downstream calls bubble up `context.DeadlineExceeded`. The router's default error path returns a generic `internal error`; for a structured signal callers can act on, map explicitly:

```go
if errors.Is(err, context.DeadlineExceeded) {
    return nil, natsrouter.ErrUnavailable("request timed out")
}
```

## Pattern Routing

Patterns use `{name}` placeholders that convert to NATS single-token wildcards (`*`):

```text
Pattern:  "chat.user.{account}.request.room.{roomID}.{siteID}.msg.history"
NATS:     "chat.user.*.request.room.*.*.msg.history"
Params:   {account: pos 2, roomID: pos 5, siteID: pos 6}
```

At request time, the incoming subject is split by `.` and param values are extracted by position.

## Panic Safety

The router installs a process-safety backstop in every spawned handler goroutine: an unrecovered panic is caught at the spawn site, logged with stack trace, and (if the message has a Reply subject) replied to with `"internal error"`. This guarantees the process cannot be crashed by a single bad handler regardless of middleware configuration.

`Recovery()` middleware is still the recommended path because it produces structured `ErrInternal` replies enriched with request-ID and other middleware-set fields. The spawn-site backstop is strictly a defense-in-depth catch — it fires only when `Recovery` is absent or somehow bypassed, and logs at `Warn` (not `Error`) since the visible symptom is "operator should fix the middleware setup," not a production incident.

## Shutdown

`Router.Shutdown(ctx)` orchestrates a graceful stop:

1. **Drain each subscription.** New messages stop arriving; pending callbacks finish executing.
2. **Wait for `SubscriptionClosed`.** All dispatcher goroutines have exited; no more handler goroutines will be spawned.
3. **Wait on the WaitGroup.** Block until every spawned handler goroutine returns — bounded by `ctx`. If `ctx` expires mid-drain, the function still falls through to step 3 (a labeled break, not an early return) so in-flight handlers are not abandoned.

The function returns `nil` on full graceful shutdown. If `ctx` expired, the returned error is a `errors.Join` of every `context.DeadlineExceeded` (one per still-pending subscription, plus optionally one for the WG wait). `errors.Is(err, context.DeadlineExceeded)` works on the joined error.

```go
ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
defer cancel()
if err := router.Shutdown(ctx); err != nil {
    slog.Error("router shutdown", "error", err)
}
nc.Drain() // close the NATS connection after the router has stopped routing
```

Place `Shutdown` before `nc.Drain()` in the shutdown chain — the router needs the connection to publish in-flight replies.

## Testing

Use `NewContext` to create a `*Context` for unit testing handlers without a NATS connection:

```go
func TestLoadHistory(t *testing.T) {
    svc := service.New(mockMsgs, mockSubs)
    c := natsrouter.NewContext(map[string]string{
        "account": "alice",
        "roomID":  "room-1",
    })

    resp, err := svc.LoadHistory(c, models.LoadHistoryRequest{RoomID: "room-1"})
    require.NoError(t, err)
    assert.Len(t, resp.Messages, 5)
}
```

Integration tests against a real NATS server live in `integration_test.go` (build tag `integration`) and use `testcontainers-go` to spin up a NATS container. Run them with:

```bash
go test -tags=integration -race ./pkg/natsrouter/...
```

## Scope and Limitations

natsrouter is designed for **NATS request/reply** (core NATS `QueueSubscribe`). It is **not** for:

| Pattern | What to Use Instead |
|---------|-------------------|
| JetStream stream consumers (subscribe, process, ack) | Manual consumer pattern or future `pkg/natsworker` |
| Subscribe-and-publish (consume from one subject, publish to another) | JetStream consumer with explicit publish |
| Streaming / long-lived subscriptions | Direct NATS subscription |
| WebSocket / SSE push to clients | HTTP framework |

### Why not JetStream consumers?

JetStream consumers have fundamentally different semantics:
- **Acknowledgment** — messages must be acked/nacked (natsrouter has no concept of this; replies are the acknowledgment)
- **Redelivery** — failed messages are redelivered automatically (natsrouter relies on caller retry on `ErrUnavailable`)
- **Durability** — consumer state persists across restarts (natsrouter is stateless)
- **Ordering** — stream ordering is guaranteed (natsrouter explicitly does not preserve per-subject ordering — see Concurrency Model)

These concerns don't belong in a request/reply router. If you need a typed handler framework for JetStream consumers, consider a separate `pkg/natsworker` package that shares concepts (Context, middleware) but has JetStream-specific semantics.

## Examples

See `example_test.go` for runnable examples:
- `Example_basicUsage` — register a handler with params
- `Example_withMiddleware` — use Recovery and Logging
- `Example_noBodyHandler` — GET-style endpoint
- `Example_errorHandling` — user-facing vs internal errors
- `Example_fireAndForget` — RegisterVoid for events
- `Example_customMiddleware` — write your own middleware

See `history-service/internal/service/` for a production usage example.
