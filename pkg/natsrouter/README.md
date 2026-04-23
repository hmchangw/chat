# natsrouter

Gin-style pattern-based routing for NATS request/reply services.

Handles subject pattern matching, parameter extraction, JSON marshal/unmarshal, middleware, and error handling — so your handlers focus on business logic.

## Quick Start

```go
nc, _ := nats.Connect(nats.DefaultURL)
router := natsrouter.New(nc, "my-service")
router.Use(natsrouter.Recovery())
router.Use(natsrouter.Logging())

natsrouter.Register(router, "chat.user.{account}.msg.send", svc.SendMessage)
```

## Table of Contents

- [Installation](#installation)
- [Core Concepts](#core-concepts)
- [API Reference](#api-reference)
- [Registration Functions](#registration-functions)
- [Context](#context)
- [Middleware](#middleware)
- [Error Handling](#error-handling)
- [Pattern Routing](#pattern-routing)
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
4. Runs the **middleware chain** (Gin-style `c.Next()` / `c.Abort()`)
5. Unmarshals JSON request bodies into typed Go structs
6. Calls your handler with a `*Context` (implements `context.Context`)
7. Marshals the response back as JSON

## API Reference

### Router

```go
// Create a router with a NATS connection and queue group name.
// Queue group ensures only one instance handles each request (load balancing).
func New(nc *nats.Conn, queue string) *Router

// Append middleware to the router's chain. Runs for ALL routes.
func (r *Router) Use(mw ...HandlerFunc)

// Drain every registered route and wait for in-flight handlers to finish
// or ctx to expire. Idempotent. Call before nc.Drain() if you need to stop
// routing while keeping the NATS connection open.
func (r *Router) Shutdown(ctx context.Context) error
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

// Detach the context for use in goroutines / after the handler returns.
// Returns a pool-safe deep copy with its own keys/params; Next() is a no-op.
func (c *Context) Copy() *Context

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
func ErrBadRequest(message string) *RouteError   // code: "bad_request"
func ErrNotFound(message string) *RouteError      // code: "not_found"
func ErrForbidden(message string) *RouteError     // code: "forbidden"
func ErrConflict(message string) *RouteError      // code: "conflict"

// Standard error code constants.
const (
    CodeBadRequest = "bad_request"
    CodeNotFound   = "not_found"
    CodeForbidden  = "forbidden"
    CodeConflict   = "conflict"
)
```

### Built-in Middleware

```go
// Generates or extracts a request ID (from X-Request-ID header or new UUID).
// Stores it via c.Set("requestID", id). Recovery and Logging include it automatically.
func RequestID() HandlerFunc

// Catches panics, logs them with request ID, replies with "internal error".
func Recovery() HandlerFunc

// Logs subject, duration, and request ID for each request.
func Logging() HandlerFunc
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
| `RegisterVoid[Req]` | Yes | No | Fire-and-forget events (typing indicators, etc.) |

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

// Fire-and-forget — no response sent.
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

### Using the Context in Goroutines

The `*Context` is pooled — its underlying struct is reused across requests. **Never** hold a direct reference to `c` in a goroutine or after the handler returns; the pool will hand it to another request and your goroutine will race with the next handler's writes.

For async work (metrics, deferred logging, fire-and-forget outbound events), call `c.Copy()`:

```go
func MetricsMiddleware() natsrouter.HandlerFunc {
    return func(c *natsrouter.Context) {
        start := time.Now()
        c.Next()
        cp := c.Copy() // detach from the pool before handing off
        go func() {
            emitMetric(cp.Msg.Subject, time.Since(start), cp.Param("account"))
        }()
    }
}
```

The copy deep-copies `Keys` and `Params`, shares the read-only `Msg` pointer (don't `Respond` on it from a goroutine — publish instead), and is marked aborted so its middleware chain won't execute.

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

## Pattern Routing

Patterns use `{name}` placeholders that convert to NATS single-token wildcards (`*`):

```text
Pattern:  "chat.user.{account}.request.room.{roomID}.{siteID}.msg.history"
NATS:     "chat.user.*.request.room.*.*.msg.history"
Params:   {account: pos 2, roomID: pos 5, siteID: pos 6}
```

At request time, the incoming subject is split by `.` and param values are extracted by position.

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
- **Acknowledgment** — messages must be acked/nacked (natsrouter has no concept of this)
- **Redelivery** — failed messages are redelivered automatically
- **Durability** — consumer state persists across restarts
- **Ordering** — stream ordering is guaranteed
- **Concurrency** — uses semaphore + worker pool patterns

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
