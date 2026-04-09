# NATS OpenTelemetry Tracing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Integrate OpenTelemetry distributed tracing into all NATS messaging across 8 services using `otelnats` and `oteljetstream` wrapper packages.

**Architecture:** Replace raw `nats.Connect` / `jetstream.New` with trace-aware wrappers from `github.com/Marz32onE/instrumentation-go/otel-nats`. Update handler signatures to propagate per-message trace context end-to-end. Wire up `otelutil.InitTracer` in every service.

**Tech Stack:** Go 1.25, NATS, JetStream, OpenTelemetry (otelnats, oteljetstream), OTLP gRPC exporter

**Spec:** `docs/superpowers/specs/2026-03-31-nats-otel-tracing-design.md`

---

### Task 1: Upgrade Go to 1.25, add external dependency, update InitTracer

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `pkg/otelutil/otel.go`
- Modify: `*/deploy/Dockerfile` (all 10)

- [x] **Step 1: Upgrade Go version in go.mod**

```bash
go mod edit -go=1.25.1
```

- [x] **Step 2: Add external otelnats/oteljetstream dependency**

```bash
GONOSUMCHECK='*' GONOSUMDB='*' GOPROXY=direct go get github.com/Marz32onE/instrumentation-go/otel-nats/otelnats github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream
```

This upgrades transitive dependencies: nats.go v1.41.1→v1.49.0, otel v1.35→v1.42, etc.

- [x] **Step 3: Update otelutil.InitTracer to register global provider**

Add to `pkg/otelutil/otel.go` imports:
```go
"go.opentelemetry.io/otel"
"go.opentelemetry.io/otel/propagation"
```

Add after `trace.NewTracerProvider(...)`:
```go
otel.SetTracerProvider(tp)
otel.SetTextMapPropagator(propagation.TraceContext{})
```

- [x] **Step 4: Update all Dockerfiles**

Change `golang:1.24-alpine` → `golang:1.25-alpine` in all 10 Dockerfiles.

- [x] **Step 5: Verify build and tests**

```bash
go build ./...
go test -race ./...
```

- [x] **Step 6: Commit**

```bash
git add go.mod go.sum pkg/otelutil/otel.go */deploy/Dockerfile tools/*/deploy/Dockerfile
git commit -m "Upgrade to Go 1.25, add external otelnats/oteljetstream, update InitTracer"
```

---

### Task 2: Integrate tracing into broadcast-worker (Category A — already accepts ctx)

**Files:**
- Modify: `broadcast-worker/handler.go`
- Modify: `broadcast-worker/handler_test.go`
- Modify: `broadcast-worker/main.go`

- [x] **Step 1: Add ctx to Publisher interface**

In `handler.go`, change:
```go
type Publisher interface {
    Publish(ctx context.Context, subject string, data []byte) error
}
```

Thread `ctx` through `publishGroupEvent` and `publishDMEvents` calls to `h.pub.Publish(ctx, ...)`.

- [x] **Step 2: Update main.go**

Replace `nats.Connect` → `otelnats.Connect`, `jetstream.New(nc)` → `oteljetstream.New(nc)`.

Change iterator loop: `msgCtx, msg, err := iter.Next()` and pass `msgCtx` to handler.

Add `otelutil.InitTracer(ctx, "broadcast-worker")` at startup. Add `tracerShutdown` to `shutdown.Wait` before `nc.Drain()`.

Change `natsPublisher` to wrap `*otelnats.Conn` with `Publish(ctx, subject, data)`.

- [x] **Step 3: Update test mock**

Change `mockPublisher.Publish` signature to `(_ context.Context, subj string, data []byte) error`.

- [x] **Step 4: Verify**

```bash
go build -o /dev/null ./broadcast-worker/
make test SERVICE=broadcast-worker
```

- [x] **Step 5: Commit**

---

### Task 3: Integrate tracing into notification-worker (Category A)

**Files:**
- Modify: `notification-worker/handler.go`
- Modify: `notification-worker/handler_test.go`
- Modify: `notification-worker/main.go`

Same pattern as broadcast-worker:

- [x] **Step 1: Add ctx to Publisher.Publish interface and h.pub.Publish calls**
- [x] **Step 2: Update main.go** — `otelnats.Connect`, `oteljetstream.New`, `InitTracer`, pass `msgCtx` from iterator
- [x] **Step 3: Update test mock** — add `_ context.Context` to Publish
- [x] **Step 4: Verify build and tests**

---

### Task 4: Integrate tracing into inbox-worker (Category A — callback consumer)

**Files:**
- Modify: `inbox-worker/handler.go`
- Modify: `inbox-worker/handler_test.go`
- Modify: `inbox-worker/main.go`

- [x] **Step 1: Add ctx to Publisher.Publish interface and h.pub.Publish calls**

- [x] **Step 2: Update main.go with callback-based consumer**

Replace `cons.Consume(func(msg jetstream.Msg) {...})` with:
```go
cons.Consume(func(m oteljetstream.MsgWithContext) {
    handler.HandleEvent(m.Context(), m.Data())
    // ack/nak via m.Ack() / m.Nak()
})
```

Note: `oteljetstream.ConsumeContext` only has `Stop()` (not `Drain()`/`Closed()`). Shutdown changes from `cctx.Drain()` + `<-cctx.Closed()` to `cctx.Stop()`.

- [x] **Step 3: Update test mock**
- [x] **Step 4: Verify build and tests**
- [x] **Step 5: Commit (batched with notification-worker)**

---

### Task 5: Integrate tracing into message-worker (Category B — add ctx to handler)

**Files:**
- Modify: `message-worker/handler.go`
- Modify: `message-worker/main.go`

- [x] **Step 1: Add ctx parameter to HandleJetStreamMsg**

```go
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
    // remove ctx := context.Background(), use parameter instead
}
```

- [x] **Step 2: Update main.go** — `otelnats.Connect`, `oteljetstream.New`, `InitTracer`, pass `msgCtx` and `msg` from iterator
- [x] **Step 3: Verify** — existing tests call `processMessage` directly with `context.Background()`, no test changes needed
- [x] **Step 4: Commit (batched with message-gatekeeper and room-worker)**

---

### Task 6: Integrate tracing into message-gatekeeper (Category B — add ctx to handler + closures)

**Files:**
- Modify: `message-gatekeeper/handler.go`
- Modify: `message-gatekeeper/handler_test.go`
- Modify: `message-gatekeeper/main.go`

- [x] **Step 1: Add ctx to handler, publishFunc, replyFunc**

```go
type replyFunc func(ctx context.Context, subject string, data []byte) error
type publishFunc func(ctx context.Context, subject string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)

func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
    // remove ctx := context.Background()
}
```

Thread `ctx` through `sendReply(ctx, ...)`, `h.reply(ctx, ...)`, `h.publish(ctx, ...)`.

- [x] **Step 2: Update main.go** — closures now accept `ctx`:
```go
pub := func(ctx context.Context, subj string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
    return js.Publish(ctx, subj, data, opts...)
}
reply := func(ctx context.Context, subj string, data []byte) error {
    return nc.Publish(ctx, subj, data)
}
```

- [x] **Step 3: Update test** — `makePublishFunc` closure gains `_ context.Context` parameter
- [x] **Step 4: Verify**

---

### Task 7: Integrate tracing into room-worker (Category B — add ctx to handler + publish closure)

**Files:**
- Modify: `room-worker/handler.go`
- Modify: `room-worker/handler_test.go`
- Modify: `room-worker/main.go`

- [x] **Step 1: Add ctx to Handler struct's publish closure and HandleJetStreamMsg**

```go
type Handler struct {
    store   SubscriptionStore
    siteID  string
    publish func(ctx context.Context, subj string, data []byte) error
}

func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
    // pass ctx to processInvite (already accepts ctx)
}
```

Thread `ctx` through all `h.publish(ctx, ...)` calls.

- [x] **Step 2: Update main.go and test closure**
- [x] **Step 3: Verify**
- [x] **Step 4: Commit (batched with message-worker and message-gatekeeper)**

---

### Task 8: Integrate tracing into history-service (Category C — `*nats.Msg` → MsgWithContext)

**Files:**
- Modify: `history-service/handler.go`
- Modify: `history-service/handler_test.go`
- Modify: `history-service/main.go`

- [x] **Step 1: Change NatsHandleHistory signature**

```go
func (h *Handler) NatsHandleHistory(m otelnats.MsgWithContext) {
    // use m.Msg for the message, m.Context() for trace context
}
```

Change `handleHistory` to accept `ctx context.Context` as first parameter (remove internal `context.Background()`).

- [x] **Step 2: Update main.go** — `otelnats.Connect`, `nc.QueueSubscribe` now passes handler that receives `MsgWithContext`
- [x] **Step 3: Update tests** — add `context.Background()` to `handleHistory` calls
- [x] **Step 4: Verify**
- [x] **Step 5: Commit (batched with room-service)**

---

### Task 9: Integrate tracing into room-service (Category C — multiple handlers + RegisterCRUD)

**Files:**
- Modify: `room-service/handler.go`
- Modify: `room-service/handler_test.go`
- Modify: `room-service/main.go`

- [x] **Step 1: Change all handler signatures**

```go
func (h *Handler) RegisterCRUD(nc *otelnats.Conn) error { ... }
func (h *Handler) natsCreateRoom(m otelnats.MsgWithContext) { ... }
func (h *Handler) natsListRooms(m otelnats.MsgWithContext) { ... }
func (h *Handler) natsGetRoom(m otelnats.MsgWithContext) { ... }
func (h *Handler) NatsHandleInvite(m otelnats.MsgWithContext) { ... }
```

Inside each handler, use `m.Msg` for the message and `m.Context()` for trace context.

Change `publishToStream` to `func(ctx context.Context, data []byte) error`.

- [x] **Step 2: Update main.go** — `otelnats.Connect`, `oteljetstream.New`, `InitTracer`
- [x] **Step 3: Update test closures** — add `_ context.Context` to `publishToStream`
- [x] **Step 4: Verify**

---

### Task 10: Full verification — lint, test, format

- [x] **Step 1: Run goimports formatting**

```bash
make fmt
```

This fixes import grouping (external `Marz32onE` package gets its own group).

- [x] **Step 2: Run linter**

```bash
make lint
```
Expected: 0 issues.

- [x] **Step 3: Run full test suite fresh**

```bash
go test -race -count=1 ./...
```
Expected: All 18 test suites pass.

- [x] **Step 4: Push all commits**

```bash
git push -u origin claude/add-nats-tracing-ch72M
```

---

### Task 11: Upgrade Go from 1.25.1 to 1.25.8

**Files:**
- Modify: `go.mod`
- Modify: `go.sum` (via `go mod tidy`)

- [x] **Step 1: Update go directive**

```bash
go mod edit -go=1.25.8
```

- [x] **Step 2: Run go mod tidy to prune stale go.sum entries**

```bash
go mod tidy
```

- [x] **Step 3: Verify build, tests, and lint**

```bash
go build ./...
go test -race -count=1 ./...
make lint
```
Expected: All 18 test suites pass, 0 lint issues.

- [x] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "Upgrade Go from 1.25.1 to 1.25.8"
```

---

## Commit History

| Commit | Description |
|--------|-------------|
| 1 | Upgrade Go 1.25, add otelnats/oteljetstream dep, update InitTracer, Dockerfiles |
| 2 | broadcast-worker tracing integration |
| 3 | notification-worker + inbox-worker tracing integration |
| 4 | message-worker + message-gatekeeper + room-worker tracing integration |
| 5 | history-service + room-service tracing integration |
| 6 | Upgrade Go from 1.25.1 to 1.25.8 |

## Files Changed Summary

- `go.mod`, `go.sum` — Go 1.25.8 + new dependency + transitive upgrades
- `pkg/otelutil/otel.go` — global TracerProvider + propagator registration
- 10 Dockerfiles — `golang:1.24-alpine` → `golang:1.25-alpine`
- 8 `main.go` files — `otelnats.Connect`, `oteljetstream.New`, `InitTracer`, traced iterators
- 6 `handler.go` files — `ctx` param added, `MsgWithContext` adoption
- 5 `handler_test.go` files — mock publisher/closure signatures updated

**Total: ~25 files modified, 0 new files created.**
