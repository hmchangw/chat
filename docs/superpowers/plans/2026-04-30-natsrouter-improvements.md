# natsrouter Concurrency Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `pkg/natsrouter`'s implicit per-subscription serialization (one in-flight handler per route per pod) with a Gin-style admission-controlled concurrency model: spawn-per-message guarded by a semaphore, with a 503-busy reply when the semaphore is saturated.

**Architecture:** Each `*nats.Subscription` still has one dispatcher goroutine inside nats.go. The router's callback no longer runs the handler chain inline — it acquires a router-level semaphore (non-blocking), and on success spawns a goroutine that runs the chain. On semaphore-full, the callback publishes a `{"error":"service busy","code":"unavailable"}` reply and returns. Per-route override (`WithConcurrency`) gives a route its own semaphore so it can be bulkheaded from the shared pool. A new `HandlerTimeout(d)` middleware enforces a per-handler context deadline.

**Tech Stack:** Go 1.25, `nats-io/nats.go`, `Marz32onE/instrumentation-go/otel-nats`, `stretchr/testify`.

**Out of scope:**
- Replacing the router's NATS transport. Subjects, queue groups, and subscription semantics are unchanged.
- Any service-level wiring (history-service, message-worker, etc.) — this plan only changes `pkg/natsrouter`. Service-level adoption is tracked in each service's own plan (e.g. history-service Task 11 wires `WithMaxConcurrency`).
- Per-route ordering guarantees. The new model loses per-subject FIFO; documented in Task 7. Routes that need ordering opt in via `WithConcurrency(1)`.

---

## Task 1: Add `WithMaxConcurrency` constructor option and `ErrUnavailable` error

**Why:** Establish the public API and types the rest of the plan builds on. Add a router-level semaphore (sized by `WithMaxConcurrency(N)`, default 100), plus a new `ErrUnavailable` error type for the saturation-reply path. No behavior change yet — Task 2 wires the semaphore into the dispatch path.

**Files:**
- Modify: `pkg/natsrouter/errors.go` (add `CodeUnavailable`, `ErrUnavailable`)
- Modify: `pkg/natsrouter/router.go` (add `Option`, `WithMaxConcurrency`, semaphore + WaitGroup fields, default constant)

- [ ] **Step 1: Write the failing tests**

Append to `pkg/natsrouter/errors_test.go` (create the file if missing — the existing `errors.go` has no test pair):

```go
package natsrouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrUnavailable_HasCodeAndMessage(t *testing.T) {
	err := ErrUnavailable("service busy")
	assert.Equal(t, "unavailable", err.Code)
	assert.Equal(t, "service busy", err.Message)
}

func TestCodeUnavailable_Constant(t *testing.T) {
	assert.Equal(t, "unavailable", CodeUnavailable)
}
```

Append to `pkg/natsrouter/router_test.go`:

```go
func TestRouter_DefaultMaxConcurrency(t *testing.T) {
	r := New(nil, "test")
	assert.Equal(t, defaultMaxConcurrency, cap(r.sem))
}

func TestRouter_WithMaxConcurrency_Overrides(t *testing.T) {
	r := New(nil, "test", WithMaxConcurrency(7))
	assert.Equal(t, 7, cap(r.sem))
}

func TestRouter_WithMaxConcurrency_IgnoresNonPositive(t *testing.T) {
	r := New(nil, "test", WithMaxConcurrency(0))
	assert.Equal(t, defaultMaxConcurrency, cap(r.sem))
	r2 := New(nil, "test", WithMaxConcurrency(-1))
	assert.Equal(t, defaultMaxConcurrency, cap(r2.sem))
}
```

If `router_test.go` doesn't import `assert` already, add `"github.com/stretchr/testify/assert"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/natsrouter/...`
Expected: FAIL — `ErrUnavailable`, `CodeUnavailable`, `Option`, `WithMaxConcurrency`, `defaultMaxConcurrency`, and `r.sem` all undefined.

- [ ] **Step 3: Add `CodeUnavailable` and `ErrUnavailable` to errors.go**

In `pkg/natsrouter/errors.go`, after the existing `CodeInternal` constant, append:

```go
	// CodeUnavailable signals the service is temporarily over capacity and
	// the caller should retry. Used by the router's admission control when
	// the per-pod handler concurrency cap is reached.
	CodeUnavailable = "unavailable"
```

The block becomes:
```go
const (
	CodeBadRequest  = "bad_request"
	CodeNotFound    = "not_found"
	CodeForbidden   = "forbidden"
	CodeConflict    = "conflict"
	CodeInternal    = "internal"
	CodeUnavailable = "unavailable"
)
```

After the existing `ErrInternal` helper, add:

```go
// ErrUnavailable creates a user-facing service-busy error. Returned by the
// router's admission control when the per-pod handler concurrency cap is
// reached. Callers should retry with backoff.
func ErrUnavailable(message string) *RouteError { return ErrWithCode(CodeUnavailable, message) }
```

- [ ] **Step 4: Add semaphore + WaitGroup fields and `WithMaxConcurrency` to router.go**

In `pkg/natsrouter/router.go`, add an import for `sync` if not already present (it is — used by `sync.Mutex`).

Add this constant near the top (after the `package` line and imports):

```go
// defaultMaxConcurrency is the default per-pod handler concurrency cap. Sized
// to match the project-wide MAX_WORKERS convention used by JetStream worker
// services. Override with WithMaxConcurrency.
const defaultMaxConcurrency = 100
```

Replace the `Router` struct:

```go
type Router struct {
	nc         *otelnats.Conn
	queue      string
	middleware []HandlerFunc

	mu   sync.Mutex
	subs []*nats.Subscription
}
```

with:

```go
type Router struct {
	nc         *otelnats.Conn
	queue      string
	middleware []HandlerFunc

	// sem gates handler concurrency: every handler invocation acquires a
	// slot before running and releases it on return. cap(sem) is the
	// per-pod concurrency ceiling. Configured by WithMaxConcurrency.
	sem chan struct{}
	// wg tracks in-flight handler goroutines so Shutdown can wait for
	// them to finish.
	wg sync.WaitGroup

	mu   sync.Mutex
	subs []*nats.Subscription
}

// Option configures a Router on construction.
type Option func(*Router)

// WithMaxConcurrency sets the maximum number of in-flight handler
// invocations across all routes registered on this router. Defaults to
// defaultMaxConcurrency. Non-positive values are ignored. Saturation
// triggers a 503-style ErrUnavailable reply.
func WithMaxConcurrency(n int) Option {
	return func(r *Router) {
		if n > 0 {
			r.sem = make(chan struct{}, n)
		}
	}
}
```

Replace the existing `New` function:

```go
func New(nc *otelnats.Conn, queue string) *Router {
	return &Router{nc: nc, queue: queue}
}
```

with:

```go
func New(nc *otelnats.Conn, queue string, opts ...Option) *Router {
	r := &Router{
		nc:    nc,
		queue: queue,
		sem:   make(chan struct{}, defaultMaxConcurrency),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/natsrouter/...`
Expected: PASS — three new router tests, two new errors tests, all existing tests still green.

- [ ] **Step 6: Verify no service breaks (variadic options means existing call sites still compile)**

Run: `go build ./...` from repo root.
Expected: PASS — every existing `natsrouter.New(nc, queue)` call still typechecks.

- [ ] **Step 7: Commit**

```bash
git add pkg/natsrouter/errors.go pkg/natsrouter/errors_test.go pkg/natsrouter/router.go pkg/natsrouter/router_test.go
git commit -m "feat(natsrouter): add WithMaxConcurrency option and ErrUnavailable error"
```

---
