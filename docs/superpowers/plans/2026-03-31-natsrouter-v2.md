# natsrouter v2 — Gin-Style Context, Groups, Error Codes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade natsrouter to a Gin-style API with request context, middleware data passing, route groups, standard error codes, and param helpers.

**Architecture:** Introduce a `Context` type (implements `context.Context`) that flows through the middleware chain and carries params, key-value store, and the raw NATS message. Middleware uses `c.Next()` / `c.Abort()` (Gin pattern). Handler signatures change from `func(ctx, params, req)` to `func(c *Context, req)`. A `Registrar` interface allows both `Router` and `Group` to register routes. The `context.Context` embedded in `Context` is created from NATS headers when available, enabling trace propagation and deadlines.

**Tech Stack:** Go 1.24, github.com/nats-io/nats.go, generics

---

## File Structure

| Action | File | Responsibility |
|--------|------|---------------|
| **Create** | `pkg/natsrouter/context.go` | `Context` type — wraps context.Context, Msg, Params, key-value store, handler chain, `Next()`/`Abort()` |
| **Modify** | `pkg/natsrouter/router.go` | Add `Registrar` interface, `Group` type, `Router.Group()` method |
| **Modify** | `pkg/natsrouter/register.go` | Change `Register`/`RegisterNoBody`/`RegisterVoid` to accept `Registrar`, use `*Context` in handler signatures |
| **Modify** | `pkg/natsrouter/middleware.go` | Change `Middleware` type alias to `HandlerFunc`, update `Recovery()`/`Logging()` to use `c.Next()` |
| **Modify** | `pkg/natsrouter/errors.go` | Add code constants (`CodeNotFound`, etc.) and convenience constructors (`ErrNotFound`, etc.) |
| **Modify** | `pkg/natsrouter/params.go` | Add `Param()` shortcut on Context, `Require()` on Params |
| **Modify** | `pkg/natsrouter/doc.go` | Update package documentation for new API |
| **Modify** | `pkg/natsrouter/router_test.go` | Update all tests for new handler signatures |
| **Modify** | `pkg/natsrouter/params_test.go` | Add `Require()` tests |
| **Modify** | `pkg/natsrouter/example_test.go` | Rewrite examples for new API (also fixes broken `natsrouter.Middleware()` call) |
| **Modify** | `history-service/internal/service/service.go` | Use `Group` for registration |
| **Modify** | `history-service/internal/service/messages.go` | Change handler signatures: `(ctx, p, req)` → `(c *Context, req)` |
| **Modify** | `history-service/internal/service/utils.go` | Update helper signatures to accept `*Context` or `context.Context` |
| **Modify** | `history-service/internal/service/messages_test.go` | Replace `NewParams` with `NewContext`, update handler calls |

---

### Task 1: Create Context type

**Files:**
- Create: `pkg/natsrouter/context.go`

- [ ] **Step 1: Create context.go**

```go
package natsrouter

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// HandlerFunc is the function type for handlers and middleware.
// Middleware calls c.Next() to continue the chain. Handlers are the last in the chain.
type HandlerFunc func(c *Context)

// Context carries request state through the middleware chain.
// It implements context.Context so it can be passed directly to database calls.
type Context struct {
	ctx      context.Context
	Msg      *nats.Msg
	Params   Params
	keys     map[string]any
	handlers []HandlerFunc
	index    int
}

// newContext creates a Context for an incoming NATS message.
func newContext(msg *nats.Msg, params Params, handlers []HandlerFunc) *Context {
	return &Context{
		ctx:      context.Background(),
		Msg:      msg,
		Params:   params,
		keys:     make(map[string]any),
		handlers: handlers,
		index:    -1,
	}
}

// NewContext creates a Context for testing handlers outside of a live NATS connection.
func NewContext(params map[string]string) *Context {
	return &Context{
		ctx:    context.Background(),
		Params: NewParams(params),
		keys:   make(map[string]any),
		index:  -1,
	}
}

// --- context.Context implementation ---

func (c *Context) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c *Context) Done() <-chan struct{}        { return c.ctx.Done() }
func (c *Context) Err() error                  { return c.ctx.Err() }
func (c *Context) Value(key any) any           { return c.ctx.Value(key) }

// --- Middleware chain ---

// Next executes the next handler in the chain. Call this from middleware
// to continue processing. If not called, the chain is short-circuited.
func (c *Context) Next() {
	c.index++
	for c.index < len(c.handlers) {
		c.handlers[c.index](c)
		c.index++
	}
}

// Abort stops the middleware chain. Subsequent handlers will not run.
func (c *Context) Abort() {
	c.index = len(c.handlers)
}

// IsAborted returns true if the chain was aborted.
func (c *Context) IsAborted() bool {
	return c.index >= len(c.handlers)
}

// --- Key-value store ---

// Set stores a key-value pair on the context for downstream handlers.
func (c *Context) Set(key string, value any) {
	c.keys[key] = value
}

// Get returns the value for a key, and whether it was found.
func (c *Context) Get(key string) (any, bool) {
	val, ok := c.keys[key]
	return val, ok
}

// MustGet returns the value for a key. Panics if not found.
func (c *Context) MustGet(key string) any {
	val, ok := c.keys[key]
	if !ok {
		panic("natsrouter: key " + key + " not found in context")
	}
	return val
}

// --- Param shortcut ---

// Param returns a named parameter from the subject. Shortcut for c.Params.Get(key).
func (c *Context) Param(key string) string {
	return c.Params.Get(key)
}

// --- Reply helpers ---

// ReplyJSON marshals v as JSON and sends it as the reply.
func (c *Context) ReplyJSON(v any) {
	natsutil.ReplyJSON(c.Msg, v)
}

// ReplyError sends an error response to the client.
func (c *Context) ReplyError(msg string) {
	natsutil.ReplyError(c.Msg, msg)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./pkg/natsrouter/...`
Expected: May fail (other files still reference old types). That's fine — just verify context.go has no syntax errors by checking the error doesn't come from context.go.

- [ ] **Step 3: Commit**

```bash
git add pkg/natsrouter/context.go
git commit -m "feat(natsrouter): add Gin-style Context type"
```

---

### Task 2: Update middleware type and built-ins

**Files:**
- Modify: `pkg/natsrouter/middleware.go`

- [ ] **Step 1: Rewrite middleware.go**

Replace the entire file. The `Middleware` type becomes an alias for `HandlerFunc`. `Recovery()` and `Logging()` use `c.Next()` instead of `next(msg)`.

```go
package natsrouter

import (
	"log/slog"
	"time"
)

// Middleware is a handler that participates in the middleware chain.
// Call c.Next() to pass control to the next handler, or return without
// calling it to short-circuit the chain.
//
// Example:
//
//	func AuthMiddleware() natsrouter.HandlerFunc {
//	    return func(c *natsrouter.Context) {
//	        token := c.Msg.Header.Get("Authorization")
//	        user, err := validateToken(token)
//	        if err != nil {
//	            c.ReplyError("unauthorized")
//	            c.Abort()
//	            return
//	        }
//	        c.Set("user", user)
//	        c.Next()
//	    }
//	}
type Middleware = HandlerFunc

// Recovery returns middleware that catches panics in the handler chain,
// logs the panic with the subject, and replies with a sanitized error.
// Place this first in the middleware chain to catch panics from all handlers.
func Recovery() HandlerFunc {
	return func(c *Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic recovered", "panic", r, "subject", c.Msg.Subject)
				c.ReplyError("internal error")
				c.Abort()
			}
		}()
		c.Next()
	}
}

// Logging returns middleware that logs each request with subject and duration.
func Logging() HandlerFunc {
	return func(c *Context) {
		start := time.Now()
		c.Next()
		slog.Info("nats request", "subject", c.Msg.Subject, "duration", time.Since(start))
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add pkg/natsrouter/middleware.go
git commit -m "feat(natsrouter): update middleware to Gin-style HandlerFunc"
```

---

### Task 3: Update router with Registrar interface and Group support

**Files:**
- Modify: `pkg/natsrouter/router.go`

- [ ] **Step 1: Rewrite router.go**

```go
package natsrouter

import (
	"fmt"

	"github.com/nats-io/nats.go"
)

// Registrar is the interface for registering route handlers.
// Both Router and Group implement it.
type Registrar interface {
	addRoute(pattern string, handlers []HandlerFunc)
}

// Router manages NATS request/reply subscriptions with pattern-based
// routing and middleware. Create with New(), add middleware with Use(),
// then register handlers with Register, RegisterNoBody, or RegisterVoid.
type Router struct {
	nc         *nats.Conn
	queue      string
	middleware []HandlerFunc
}

// New creates a Router with the given NATS connection and queue group.
func New(nc *nats.Conn, queue string) *Router {
	return &Router{nc: nc, queue: queue}
}

// Use appends middleware to the router's chain.
// Middleware executes in the order added: Use(A, B, C) → A → B → C → handler.
func (r *Router) Use(mw ...HandlerFunc) {
	r.middleware = append(r.middleware, mw...)
}

// Group creates a sub-router with a subject prefix and optional middleware.
// The prefix is prepended to all patterns registered on the group.
// Group middleware runs after router middleware and before the handler.
func (r *Router) Group(prefix string, mw ...HandlerFunc) *Group {
	return &Group{parent: r, prefix: prefix, middleware: mw}
}

func (r *Router) addRoute(pattern string, handlers []HandlerFunc) {
	rt := parsePattern(pattern)
	all := make([]HandlerFunc, 0, len(r.middleware)+len(handlers))
	all = append(all, r.middleware...)
	all = append(all, handlers...)

	natsHandler := func(msg *nats.Msg) {
		c := newContext(msg, rt.extractParams(msg.Subject), all)
		c.Next()
	}

	if _, err := r.nc.QueueSubscribe(rt.natsSubject, r.queue, natsHandler); err != nil {
		panic(fmt.Sprintf("natsrouter: subscribing to %s: %v", rt.natsSubject, err))
	}
}

// Group is a sub-router with a subject prefix and scoped middleware.
type Group struct {
	parent     Registrar
	prefix     string
	middleware []HandlerFunc
}

// Use appends middleware scoped to this group.
func (g *Group) Use(mw ...HandlerFunc) {
	g.middleware = append(g.middleware, mw...)
}

// Group creates a nested sub-group with an additional prefix and middleware.
func (g *Group) Group(prefix string, mw ...HandlerFunc) *Group {
	return &Group{
		parent:     g,
		prefix:     g.prefix + "." + prefix,
		middleware: append(append([]HandlerFunc{}, g.middleware...), mw...),
	}
}

func (g *Group) addRoute(pattern string, handlers []HandlerFunc) {
	fullPattern := g.prefix + "." + pattern
	all := make([]HandlerFunc, 0, len(g.middleware)+len(handlers))
	all = append(all, g.middleware...)
	all = append(all, handlers...)
	g.parent.addRoute(fullPattern, all)
}
```

- [ ] **Step 2: Commit**

```bash
git add pkg/natsrouter/router.go
git commit -m "feat(natsrouter): add Registrar interface and Group support"
```

---

### Task 4: Update register functions for new Context-based signatures

**Files:**
- Modify: `pkg/natsrouter/register.go`

- [ ] **Step 1: Rewrite register.go**

Handler signatures change from `func(ctx, params, req)` to `func(c *Context, req)`. The Register functions accept `Registrar` instead of `*Router`.

```go
package natsrouter

import (
	"encoding/json"
	"errors"
	"log/slog"
)

// Register subscribes a typed handler to a subject pattern.
//
// The handler receives a *Context (which implements context.Context) and the
// unmarshalled request body. It returns a response pointer and an error.
//
//   - *RouteError → sent to client as-is
//   - any other error → logged, client receives "internal error"
//   - JSON unmarshal fails → client receives "invalid request payload"
//
// Register panics if the subscription fails (startup-only, fatal if broken).
func Register[Req, Resp any](
	r Registrar,
	pattern string,
	fn func(c *Context, req Req) (*Resp, error),
) {
	handler := HandlerFunc(func(c *Context) {
		var req Req
		if err := json.Unmarshal(c.Msg.Data, &req); err != nil {
			c.ReplyError("invalid request payload")
			return
		}

		resp, err := fn(c, req)
		if err != nil {
			replyErr(c, err)
			return
		}

		c.ReplyJSON(resp)
	})

	r.addRoute(pattern, []HandlerFunc{handler})
}

// RegisterNoBody subscribes a handler that takes no request body — only params.
func RegisterNoBody[Resp any](
	r Registrar,
	pattern string,
	fn func(c *Context) (*Resp, error),
) {
	handler := HandlerFunc(func(c *Context) {
		resp, err := fn(c)
		if err != nil {
			replyErr(c, err)
			return
		}

		c.ReplyJSON(resp)
	})

	r.addRoute(pattern, []HandlerFunc{handler})
}

// RegisterVoid subscribes a handler that processes a request without replying.
func RegisterVoid[Req any](
	r Registrar,
	pattern string,
	fn func(c *Context, req Req) error,
) {
	handler := HandlerFunc(func(c *Context) {
		var req Req
		if err := json.Unmarshal(c.Msg.Data, &req); err != nil {
			slog.Error("invalid payload in void handler", "error", err, "subject", c.Msg.Subject)
			return
		}

		if err := fn(c, req); err != nil {
			slog.Error("void handler error", "error", err, "subject", c.Msg.Subject)
		}
	})

	r.addRoute(pattern, []HandlerFunc{handler})
}

// replyErr handles error replies.
func replyErr(c *Context, err error) {
	var routeErr *RouteError
	if errors.As(err, &routeErr) {
		c.ReplyJSON(routeErr)
		return
	}
	slog.Error("handler error", "error", err, "subject", c.Msg.Subject)
	c.ReplyError("internal error")
}
```

- [ ] **Step 2: Verify natsrouter compiles**

Run: `go build ./pkg/natsrouter/...`
Expected: Compile errors only from test files (they still use old signatures). Core package should compile.

- [ ] **Step 3: Commit**

```bash
git add pkg/natsrouter/register.go
git commit -m "feat(natsrouter): update Register functions for Context-based handlers"
```

---

### Task 5: Add error code constants and param helpers

**Files:**
- Modify: `pkg/natsrouter/errors.go`
- Modify: `pkg/natsrouter/params.go`

- [ ] **Step 1: Add error constants to errors.go**

Append after the existing `ErrWithCode` function:

```go
// Standard error codes for consistent cross-service error handling.
const (
	CodeBadRequest = "bad_request"
	CodeNotFound   = "not_found"
	CodeForbidden  = "forbidden"
	CodeConflict   = "conflict"
)

// ErrBadRequest creates a user-facing bad request error.
func ErrBadRequest(message string) *RouteError { return ErrWithCode(CodeBadRequest, message) }

// ErrNotFound creates a user-facing not found error.
func ErrNotFound(message string) *RouteError { return ErrWithCode(CodeNotFound, message) }

// ErrForbidden creates a user-facing forbidden error.
func ErrForbidden(message string) *RouteError { return ErrWithCode(CodeForbidden, message) }

// ErrConflict creates a user-facing conflict error.
func ErrConflict(message string) *RouteError { return ErrWithCode(CodeConflict, message) }
```

- [ ] **Step 2: Add Require to params.go**

Append after `MustGet`:

```go
// Require returns the value of a named param or an error if not found/empty.
// Use when a missing param should return a user-facing error, not a panic.
func (p Params) Require(key string) (string, error) {
	v, ok := p.values[key]
	if !ok || v == "" {
		return "", ErrBadRequest("missing required param: " + key)
	}
	return v, nil
}
```

- [ ] **Step 3: Verify natsrouter compiles**

Run: `go build ./pkg/natsrouter/...`

- [ ] **Step 4: Commit**

```bash
git add pkg/natsrouter/errors.go pkg/natsrouter/params.go
git commit -m "feat(natsrouter): add error code constants and param Require helper"
```

---

### Task 6: Update natsrouter tests

**Files:**
- Modify: `pkg/natsrouter/router_test.go`
- Modify: `pkg/natsrouter/params_test.go`
- Modify: `pkg/natsrouter/example_test.go`

- [ ] **Step 1: Update router_test.go**

All handler lambdas change from `func(ctx context.Context, p Params, req testReq) (*testResp, error)` to `func(c *Context, req testReq) (*testResp, error)`. Middleware lambdas change from `func(next nats.MsgHandler) nats.MsgHandler` to `func(c *Context)` with `c.Next()`.

Key changes across all tests:
- `func(ctx context.Context, p Params, req testReq)` → `func(c *Context, req testReq)`
- `func(ctx context.Context, p Params)` → `func(c *Context)`
- `func(ctx context.Context, p Params, req testReq) error` → `func(c *Context, req testReq) error`
- `p.Get("userID")` → `c.Param("userID")`
- `captured = p` → `captured = c.Params`
- Middleware: `func(next nats.MsgHandler) nats.MsgHandler { return func(msg *nats.Msg) { ... next(msg) ... } }` → `func(c *Context) { ... c.Next() ... }`
- Short-circuit middleware: instead of `msg.Respond(...)`, use `c.ReplyJSON(...)` and don't call `c.Next()`
- Remove `"context"` and `nats.go` imports if no longer needed
- Keep `model` import for ErrorResponse checks

Also add new tests:
- `TestGroup_Registration` — register via group, verify handler works
- `TestGroup_ScopedMiddleware` — group middleware runs only for group routes
- `TestContext_SetGet` — verify key-value store
- `TestContext_Abort` — verify abort stops chain
- `TestErrBadRequest` etc. — verify error code constants
- `TestParams_Require` — verify Require returns error for missing params

- [ ] **Step 2: Update params_test.go**

Add tests for `Require`:

```go
func TestParams_Require_Success(t *testing.T) {
	p := Params{values: map[string]string{"userID": "abc"}}
	v, err := p.Require("userID")
	require.NoError(t, err)
	assert.Equal(t, "abc", v)
}

func TestParams_Require_Missing(t *testing.T) {
	p := Params{values: map[string]string{}}
	_, err := p.Require("userID")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required param")
}

func TestParams_Require_Empty(t *testing.T) {
	p := Params{values: map[string]string{"userID": ""}}
	_, err := p.Require("userID")
	require.Error(t, err)
}
```

- [ ] **Step 3: Rewrite example_test.go**

Fix the broken `natsrouter.Middleware(requireBody)` call (line 140) and update all examples to use the new API. All handler signatures change. The custom middleware example uses `c.Next()` instead of `next(msg)`.

- [ ] **Step 4: Run all natsrouter tests**

Run: `go test -race ./pkg/natsrouter/...`
Expected: All tests PASS.

- [ ] **Step 5: Run linter**

Run: `make lint`
Expected: 0 issues.

- [ ] **Step 6: Commit**

```bash
git add pkg/natsrouter/router_test.go pkg/natsrouter/params_test.go pkg/natsrouter/example_test.go
git commit -m "test(natsrouter): update all tests for v2 Context-based API"
```

---

### Task 7: Update doc.go

**Files:**
- Modify: `pkg/natsrouter/doc.go`

- [ ] **Step 1: Update package documentation**

Update the package doc to reflect the new API:
- Handler signatures now use `*Context`
- Middleware uses `c.Next()` / `c.Abort()`
- Route groups with `router.Group(prefix)`
- Error constants
- Context implements `context.Context`

- [ ] **Step 2: Commit**

```bash
git add pkg/natsrouter/doc.go
git commit -m "docs(natsrouter): update package documentation for v2 API"
```

---

### Task 8: Update history-service handlers

**Files:**
- Modify: `history-service/internal/service/service.go`
- Modify: `history-service/internal/service/messages.go`
- Modify: `history-service/internal/service/utils.go`

- [ ] **Step 1: Update service.go — use Group**

```go
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, siteID string) {
	msg := r.Group("chat.user.{username}.request.room.{roomID}." + siteID + ".msg")
	natsrouter.Register(msg, "history", s.LoadHistory)
	natsrouter.Register(msg, "next", s.LoadNextMessages)
	natsrouter.Register(msg, "surrounding", s.LoadSurroundingMessages)
	natsrouter.Register(msg, "get", s.GetMessageByID)
}
```

Remove `"fmt"` import (no longer needed for `fmt.Sprintf`).

- [ ] **Step 2: Update messages.go — change all handler signatures**

Every handler changes from:
```go
func (s *HistoryService) LoadHistory(ctx context.Context, p natsrouter.Params, req ...) (*..., error) {
```
To:
```go
func (s *HistoryService) LoadHistory(c *natsrouter.Context, req ...) (*..., error) {
```

Inside each handler:
- `p.Get("username")` → `c.Param("username")`
- `resolveRoomID(p, req.RoomID)` → `resolveRoomID(c, req.RoomID)`
- `s.getAccessSince(ctx, ...)` → `s.getAccessSince(c, ...)`
- All `ctx` passed to repo calls → `c` (since Context implements context.Context)
- `natsrouter.ErrWithCode("not_found", ...)` → `natsrouter.ErrNotFound(...)`
- `natsrouter.ErrWithCode("forbidden", ...)` → `natsrouter.ErrForbidden(...)`
- `natsrouter.ErrWithCode("bad_request", ...)` → `natsrouter.ErrBadRequest(...)`

Remove the `"context"` import (no longer directly used).

- [ ] **Step 3: Update utils.go**

- `getAccessSince`: change `ctx context.Context` → `ctx context.Context` (keep as context.Context for interface compatibility with repos)
- `resolveRoomID`: change `p natsrouter.Params` → `c *natsrouter.Context`, use `c.Param("roomID")`
- Replace `natsrouter.ErrWithCode(...)` with convenience constructors

- [ ] **Step 4: Verify compilation**

Run: `go build ./history-service/...`
Expected: Clean build. Tests won't pass yet (next task).

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/service.go history-service/internal/service/messages.go history-service/internal/service/utils.go
git commit -m "feat(history): migrate handlers to natsrouter v2 Context API"
```

---

### Task 9: Update history-service tests

**Files:**
- Modify: `history-service/internal/service/messages_test.go`

- [ ] **Step 1: Update test setup and all handler calls**

Key changes:
- `testParams = natsrouter.NewParams(map[string]string{...})` → `testCtx = natsrouter.NewContext(map[string]string{...})` (but note: create a fresh context per test since Context is mutable)
- Replace `testParams` usage in each test with `natsrouter.NewContext(map[string]string{"username": "u1", "roomID": "r1"})`
- All handler calls: `svc.LoadHistory(ctx, testParams, req)` → `svc.LoadHistory(c, req)` where `c` is the new Context
- Remove `ctx := context.Background()` lines — the Context carries the context
- Update mock expectations: `ctx` parameter in mock calls changes from `context.Background()` to the `*natsrouter.Context` (use `gomock.Any()` for the context parameter since it's a different object each test)

**Important:** Since `*natsrouter.Context` implements `context.Context`, mock expectations that match on `ctx` need to use `gomock.Any()` instead of a specific `context.Background()` value, because the context is now the `*natsrouter.Context` instance which differs per test.

- [ ] **Step 2: Run tests**

Run: `make test SERVICE=history-service`
Expected: All tests PASS.

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/service/messages_test.go
git commit -m "test(history): update tests for natsrouter v2 Context API"
```

---

### Task 10: Full verification

- [ ] **Step 1: Run linter**

Run: `make lint`
Expected: 0 issues.

- [ ] **Step 2: Run all tests**

Run: `make test`
Expected: All packages PASS.

- [ ] **Step 3: Push**

```bash
git push -u origin claude/review-history-service-4IpAp
```
