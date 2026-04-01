# NATS Router — Design Spec

**Date:** 2026-03-27
**Status:** Approved

---

## Overview

A shared Go library at `pkg/natsrouter/` that provides pattern-based routing for NATS request/reply endpoints. Subjects use `{param}` placeholders (like REST API routes) instead of raw NATS wildcards. The router handles pattern-to-wildcard conversion, param extraction, JSON unmarshal/reply, middleware, and error sanitization.

## Goals

- Pattern-based subject routing with named params: `chat.user.{userID}.request.room.{roomID}.msg.history`
- Generic `Register[Req, Resp]` — one line to add a new endpoint
- Composable middleware: `func(next nats.MsgHandler) nats.MsgHandler`
- Built-in recovery and logging middleware
- Library-quality godoc with runnable examples
- Replace `history-service/internal/natshandler` after implementation

## Non-Goals

- JetStream consumer routing (different pattern — stream/iterator, not request/reply)
- `>` multi-token wildcard support (users can extend if needed)

---

## Package Structure

```text
pkg/natsrouter/
├── router.go         # Router struct, New(), Use()
├── register.go       # Register[Req,Resp], RegisterNoBody[Resp]
├── params.go         # Params, route, parsePattern, extractParams
├── middleware.go     # Middleware type, Recovery(), Logging()
├── router_test.go    # Integration tests with in-process NATS server
├── params_test.go    # Unit tests for pattern parsing + extraction
└── example_test.go   # Runnable godoc examples
```

---

## API Surface

### Router

```go
// Router manages NATS request/reply subscriptions with pattern-based
// routing and middleware. Create with New(), add middleware with Use(),
// then register handlers with Register or RegisterNoBody.
type Router struct {
    nc         *nats.Conn
    queue      string
    middleware []Middleware
}

// New creates a Router with the given NATS connection and queue group.
// The queue group ensures only one instance in a service cluster handles
// each request (competing consumers).
func New(nc *nats.Conn, queue string) *Router

// Use appends middleware to the router's middleware chain.
// Middleware executes in the order added: Use(A, B, C) → A → B → C → handler.
func (r *Router) Use(mw ...Middleware)
```

### Register

```go
// Register subscribes a typed handler to a subject pattern.
//
// The pattern uses {name} placeholders for named params. At registration time,
// the pattern is converted to a NATS wildcard subject for QueueSubscribe, and
// a param extraction map is built. At request time, params are extracted from
// the subject, the request body is unmarshalled into Req, the handler is called,
// and the response is marshalled back as JSON.
//
// Handler errors are logged with the subject and sanitized — callers receive
// "internal error", never raw Go error strings.
//
// Example:
//
//   natsrouter.Register[LoadHistoryRequest, LoadHistoryResponse](
//       router,
//       "chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history",
//       func(ctx context.Context, p natsrouter.Params, req LoadHistoryRequest) (*LoadHistoryResponse, error) {
//           userID := p.Get("userID")
//           roomID := p.Get("roomID")
//           return svc.LoadHistory(ctx, userID, roomID, req)
//       },
//   )
func Register[Req, Resp any](
    r *Router,
    pattern string,
    fn func(ctx context.Context, params Params, req Req) (*Resp, error),
) error

// RegisterNoBody subscribes a handler that takes no request body — only params.
// Use for endpoints where the subject contains all the information needed
// (e.g. GET-style lookups by ID).
//
// Example:
//
//   natsrouter.RegisterNoBody[Room](
//       router,
//       "chat.user.{userID}.request.rooms.get.{roomID}",
//       func(ctx context.Context, p natsrouter.Params) (*Room, error) {
//           return svc.GetRoom(ctx, p.Get("roomID"))
//       },
//   )
func RegisterNoBody[Resp any](
    r *Router,
    pattern string,
    fn func(ctx context.Context, params Params) (*Resp, error),
) error
```

### Params

```go
// Params holds named tokens extracted from a NATS subject at request time.
// Values are populated by matching the incoming subject against the registered
// pattern's {name} placeholders.
type Params struct {
    values map[string]string
}

// Get returns the value of a named param, or empty string if not found.
func (p Params) Get(key string) string

// MustGet returns the value of a named param. Panics if the key is not found.
// Use only when the param is guaranteed by the pattern — a panic here means
// the pattern doesn't contain the requested param (developer error).
func (p Params) MustGet(key string) string
```

### Middleware

```go
// Middleware wraps a NATS message handler. It receives the next handler in the
// chain and returns a new handler that can execute logic before/after calling
// next, or short-circuit by not calling next at all.
//
// Middleware sees the raw *nats.Msg — it can inspect/modify the message,
// reject requests before unmarshal, or wrap the reply.
type Middleware func(next nats.MsgHandler) nats.MsgHandler

// Recovery returns middleware that catches panics in the handler chain,
// logs the panic with stack info, and replies with a sanitized error.
func Recovery() Middleware

// Logging returns middleware that logs each request with subject and duration.
func Logging() Middleware
```

---

## Internal Types

```go
// route is created once at registration time from a pattern.
// It holds the converted NATS wildcard subject and the position-to-name
// mapping for param extraction.
type route struct {
    natsSubject string         // "chat.user.*.request.room.*.*.msg.history"
    params      map[int]string // {2: "userID", 5: "roomID", 6: "siteID"}
}

// parsePattern converts a pattern with {name} placeholders into a route.
// Each {name} token becomes a * in the NATS subject and its position is
// recorded in the params map for extraction at request time.
//
// Example:
//   parsePattern("chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history")
//   → route{
//       natsSubject: "chat.user.*.request.room.*.*.msg.history",
//       params:      map[int]string{2: "userID", 5: "roomID", 6: "siteID"},
//     }
func parsePattern(pattern string) route

// extractParams splits an incoming subject by "." and pulls values from
// the positions recorded in the route's params map.
func (r route) extractParams(subject string) Params
```

---

## Pattern → Wildcard Conversion

```text
Input:  "chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history"
Output: "chat.user.*.request.room.*.*.msg.history"

Input:  "chat.user.{userID}.request.rooms.create"
Output: "chat.user.*.request.rooms.create"

Input:  "fanout.{siteID}.{roomID}"
Output: "fanout.*.*"
```

Rules:
- Any token matching `{...}` is replaced with `*`
- All other tokens pass through unchanged
- The param name (without braces) is mapped to its position index

---

## Middleware Chain

Given `router.Use(A, B, C)` and a handler `H`, the execution order is:

```text
A.before → B.before → C.before → H → C.after → B.after → A.after
```

Middleware can short-circuit by not calling `next(msg)`:

```go
func AuthMiddleware(next nats.MsgHandler) nats.MsgHandler {
    return func(msg *nats.Msg) {
        if !isAuthorized(msg) {
            natsutil.ReplyError(msg, "unauthorized")
            return // short-circuit — handler never executes
        }
        next(msg) // continue chain
    }
}
```

The middleware chain is built at registration time by wrapping the handler from inside out: `A(B(C(handler)))`.

---

## Error Handling

- `Register` returns an error if `QueueSubscribe` fails (bad connection, invalid subject)
- Handler errors are **logged** with subject context and **sanitized** — callers receive `"internal error"` via `natsutil.ReplyError`, never raw Go error strings
- JSON unmarshal errors reply with `"invalid request payload"`
- `Recovery()` middleware catches panics, logs with stack, replies with `"internal error"`
- `Logging()` middleware logs subject and duration for every request

---

## Built-in Middleware Implementation

### Recovery

```go
func Recovery() Middleware {
    return func(next nats.MsgHandler) nats.MsgHandler {
        return func(msg *nats.Msg) {
            defer func() {
                if r := recover(); r != nil {
                    slog.Error("panic recovered", "panic", r, "subject", msg.Subject)
                    natsutil.ReplyError(msg, "internal error")
                }
            }()
            next(msg)
        }
    }
}
```

### Logging

```go
func Logging() Middleware {
    return func(next nats.MsgHandler) nats.MsgHandler {
        return func(msg *nats.Msg) {
            start := time.Now()
            next(msg)
            slog.Info("nats request", "subject", msg.Subject, "duration", time.Since(start))
        }
    }
}
```

---

## Testing Strategy

### params_test.go — Pure unit tests (no NATS)
- `TestParsePattern_SingleParam` — one `{param}` replaced with `*`
- `TestParsePattern_MultipleParams` — multiple params, correct positions
- `TestParsePattern_NoParams` — literal subject, empty params map
- `TestParsePattern_AdjacentParams` — `{a}.{b}` side by side
- `TestExtractParams` — correct values at correct positions
- `TestExtractParams_NoParams` — empty params, no panic

### router_test.go — Integration tests with in-process NATS server
- `TestRegister_Success` — request/response round-trip with params
- `TestRegister_InvalidJSON` — error reply
- `TestRegister_HandlerError` — sanitized error reply
- `TestRegister_ParamsExtraction` — verify correct param values in handler
- `TestRegisterNoBody_Success` — handler with no request body
- `TestMiddleware_ExecutionOrder` — verify A → B → C order
- `TestMiddleware_ShortCircuit` — middleware rejects without calling handler
- `TestRecovery_CatchesPanic` — panic → error reply, no crash
- `TestLogging_LogsRequest` — verify log output (subject + duration)

### example_test.go — Runnable godoc examples
- `Example_basicUsage` — register + request
- `Example_withMiddleware` — logging + recovery
- `Example_paramsExtraction` — using Params in handler
- `Example_noBodyHandler` — RegisterNoBody usage

---

## Migration Plan

After `pkg/natsrouter` is implemented:

1. Update `history-service/internal/service/service.go`:
   - Replace `natshandler.Handler` → `natsrouter.Router`
   - Handler signatures change from `func(ctx, userID, req)` to `func(ctx, params, req)`
   - `RegisterHandlers` uses `natsrouter.Register` with pattern strings

2. Update `history-service/internal/service/messages.go`:
   - Each handler takes `params natsrouter.Params` instead of `userID string`
   - Extract userID via `params.Get("userID")`

3. Update `history-service/cmd/main.go`:
   - `natsrouter.New(nc, "history-service")` replaces `natshandler.New(nc, "history-service")`
   - Add `router.Use(natsrouter.Recovery(), natsrouter.Logging())`

4. Delete `history-service/internal/natshandler/` entirely

5. Update tests in `messages_test.go` to pass `natsrouter.Params` instead of `userID`

---

## Files Changed

**New files:**
- `pkg/natsrouter/router.go`
- `pkg/natsrouter/register.go`
- `pkg/natsrouter/params.go`
- `pkg/natsrouter/middleware.go`
- `pkg/natsrouter/router_test.go`
- `pkg/natsrouter/params_test.go`
- `pkg/natsrouter/example_test.go`

**Modified files (migration):**
- `history-service/internal/service/service.go` — use `natsrouter.Router`
- `history-service/internal/service/messages.go` — `Params` instead of `userID`
- `history-service/internal/service/messages_test.go` — update mock expectations
- `history-service/cmd/main.go` — use `natsrouter.New`

**Deleted files:**
- `history-service/internal/natshandler/handler.go`
- `history-service/internal/natshandler/utils_test.go`
