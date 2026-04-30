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

## Task 2: Spawn handlers and reply busy on saturation

**Why:** The dispatcher goroutine currently runs the handler chain inline, which serializes everything to one in-flight handler per route. Wire the semaphore from Task 1 into `addRoute`'s callback: try a non-blocking semaphore acquire; if it succeeds, spawn a goroutine to run the chain (releasing on return); if it fails, publish an `ErrUnavailable` reply immediately and return. The dispatcher goroutine becomes a thin admission-control loop.

**Files:**
- Modify: `pkg/natsrouter/router.go` (rewrite `addRoute` callback; add `replyBusy` helper)
- Modify: `pkg/natsrouter/integration_test.go` (existing heavy-concurrency test must opt into a higher cap; add new busy-reply integration test)

- [ ] **Step 1: Write the failing busy-reply integration test**

Append to `pkg/natsrouter/integration_test.go`:

```go
// TestIntegration_BusyReplyOnSaturation verifies that requests arriving
// while the per-pod concurrency cap is exhausted receive an ErrUnavailable
// reply rather than blocking.
func TestIntegration_BusyReplyOnSaturation(t *testing.T) {
	nc := setupNATS(t)
	r := natsrouter.New(nc, "integration-busy", natsrouter.WithMaxConcurrency(1))

	gate := make(chan struct{})
	natsrouter.Register(r, "busy.{id}",
		func(c *natsrouter.Context, req echoReq) (*echoResp, error) {
			<-gate
			return &echoResp{Seq: req.Seq}, nil
		})

	// First request occupies the only slot.
	first := make(chan struct {
		resp []byte
		err  error
	}, 1)
	go func() {
		data, _ := json.Marshal(echoReq{Seq: 1})
		resp, err := nc.Request(context.Background(), "busy.1", data, 5*time.Second)
		var b []byte
		if resp != nil {
			b = resp.Data
		}
		first <- struct {
			resp []byte
			err  error
		}{b, err}
	}()

	// Wait for the first handler to be in-flight.
	require.Eventually(t, func() bool {
		// A second request should now get busy because the slot is held.
		data, _ := json.Marshal(echoReq{Seq: 2})
		resp, err := nc.Request(context.Background(), "busy.2", data, 1*time.Second)
		if err != nil {
			return false
		}
		var re natsrouter.RouteError
		if err := json.Unmarshal(resp.Data, &re); err != nil {
			return false
		}
		return re.Code == natsrouter.CodeUnavailable
	}, 5*time.Second, 50*time.Millisecond, "expected busy reply once slot is held")

	// Release the gate; first request must complete normally.
	close(gate)
	got := <-first
	require.NoError(t, got.err)
	var ok echoResp
	require.NoError(t, json.Unmarshal(got.resp, &ok))
	assert.Equal(t, 1, ok.Seq)
}
```

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test -tags=integration ./pkg/natsrouter/ -run TestIntegration_BusyReplyOnSaturation -v`
Expected: FAIL — the second request blocks rather than getting a busy reply (current code serializes inside the dispatcher).

- [ ] **Step 3: Add the `replyBusy` helper in router.go**

Add to `pkg/natsrouter/router.go`, after the `New` constructor:

```go
// replyBusy publishes an ErrUnavailable reply on m.Reply, used when the
// router's admission control rejects a message. Best-effort — if the
// caller did not set a reply subject, the publish is a no-op.
func (r *Router) replyBusy(msg *nats.Msg) {
	if msg.Reply == "" {
		return
	}
	natsutil.ReplyJSON(msg, ErrUnavailable("service busy"))
}
```

Add the import `"github.com/hmchangw/chat/pkg/natsutil"` to the file.

- [ ] **Step 4: Rewrite the `natsHandler` closure inside `addRoute`**

In `pkg/natsrouter/router.go`, replace the existing `natsHandler` closure inside `addRoute`:

```go
	natsHandler := func(m otelnats.Msg) {
		c := acquireContext(m.Context(), m.Msg, rt.extractParams(m.Msg.Subject), all)
		c.Next()
		releaseContext(c)
	}
```

with:

```go
	natsHandler := func(m otelnats.Msg) {
		select {
		case r.sem <- struct{}{}:
		default:
			r.replyBusy(m.Msg)
			return
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer func() { <-r.sem }()
			c := acquireContext(m.Context(), m.Msg, rt.extractParams(m.Msg.Subject), all)
			c.Next()
			releaseContext(c)
		}()
	}
```

- [ ] **Step 5: Update `TestIntegration_ConcurrentRequestsWithCopy` to opt into a higher cap**

The existing test fires 300 concurrent requests and asserts all succeed. With the default cap of 100, some would be busy-replied. The test's intent is heavy-concurrency stress (context safety, not admission control), so bump the cap.

In `pkg/natsrouter/integration_test.go`, replace:

```go
	r := natsrouter.New(nc, "integration-concurrent")
```

with:

```go
	r := natsrouter.New(nc, "integration-concurrent", natsrouter.WithMaxConcurrency(500))
```

- [ ] **Step 6: Run all tests to verify they pass**

Run:
```bash
go test ./pkg/natsrouter/...
go test -tags=integration ./pkg/natsrouter/...
```
Expected: PASS — unit tests still green; integration tests including the new busy-reply test pass.

- [ ] **Step 7: Run with race detector**

Run: `go test -tags=integration -race ./pkg/natsrouter/...`
Expected: PASS — no data races introduced by the spawn-per-message model.

- [ ] **Step 8: Commit**

```bash
git add pkg/natsrouter/router.go pkg/natsrouter/integration_test.go
git commit -m "feat(natsrouter): spawn handlers with semaphore admission control"
```

---
