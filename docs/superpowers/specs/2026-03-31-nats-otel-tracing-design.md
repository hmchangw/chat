# NATS OpenTelemetry Tracing Integration

**Date:** 2026-03-31
**Status:** Approved
**Approach:** Direct wrapper swap using `otelnats` / `oteljetstream`

## Summary

Integrate OpenTelemetry distributed tracing into all NATS messaging across 8 services (excluding `auth-service`). Replace raw `nats.Connect` / `jetstream.New` with trace-aware wrappers from `github.com/Marz32onE/instrumentation-go/otel-nats`. Wire up `otelutil.InitTracer` in every service. Update handler signatures to propagate per-message trace context end-to-end.

## Decisions

| Decision | Choice |
|----------|--------|
| Scope | 8 services (exclude `auth-service`) |
| Existing `pkg/natsutil/carrier.go` | Keep as-is |
| TracerProvider setup | Wire up `InitTracer` only (not `InitMeter`) |
| Context propagation depth | Full end-to-end (pass per-message `ctx` into handlers) |
| Infrastructure trace events | Skip (`WithTraceDestination` / `SubscribeTraceEvents` not used) |
| Approach | Direct wrapper swap (Approach 1) |

## New Dependency

```
github.com/Marz32onE/instrumentation-go/otel-nats
```

Provides two packages:
- `otelnats` — trace-aware wrapper around `*nats.Conn` (Publish, Subscribe, QueueSubscribe, Request)
- `oteljetstream` — trace-aware wrapper around `jetstream.JetStream` (Publish, Consumer, Stream)

## Section 1: TracerProvider Setup

### `pkg/otelutil/otel.go` Change

Add global provider registration inside `InitTracer`:

```go
otel.SetTracerProvider(tp)
otel.SetTextMapPropagator(propagation.TraceContext{})
```

This ensures the `otelnats`/`oteljetstream` packages pick up the provider via `otel.GetTracerProvider()`.

### Per-Service `main.go` Pattern

Each service adds this block early in `main()`, before NATS connection:

```go
tracerShutdown, err := otelutil.InitTracer(ctx, "<service-name>")
if err != nil {
    slog.Error("init tracer failed", "error", err)
    os.Exit(1)
}
```

`tracerShutdown` is added to `shutdown.Wait` after worker drain completes but before `nc.Drain()`, so in-flight spans flush before the NATS connection drains. The shutdown order becomes: stop iterator -> wait for workers -> flush tracer -> drain NATS -> disconnect databases.

## Section 2: NATS Connection Changes

### Core NATS

All 8 services change from:
```go
nc, err := nats.Connect(cfg.NatsURL)
```
To:
```go
nc, err := otelnats.Connect(cfg.NatsURL)
```

Returns `*otelnats.Conn` instead of `*nats.Conn`.

### JetStream (6 services)

Services using JetStream change from:
```go
js, err := jetstream.New(nc)
```
To:
```go
js, err := oteljetstream.New(nc)
```

Returns `oteljetstream.JetStream` instead of `jetstream.JetStream`.

### Connection table

| Service | Connection | JetStream |
|---------|-----------|-----------|
| broadcast-worker | `otelnats.Connect` | `oteljetstream.New` |
| history-service | `otelnats.Connect` | -- |
| inbox-worker | `otelnats.Connect` | `oteljetstream.New` |
| message-gatekeeper | `otelnats.Connect` | `oteljetstream.New` |
| message-worker | `otelnats.Connect` | `oteljetstream.New` |
| notification-worker | `otelnats.Connect` | `oteljetstream.New` |
| room-service | `otelnats.Connect` | `oteljetstream.New` |
| room-worker | `otelnats.Connect` | `oteljetstream.New` |

## Section 3: Handler Signature Changes

### Category A: Handlers already accepting `ctx` + `[]byte`

**Services:** broadcast-worker, inbox-worker, notification-worker

Handler methods (e.g., `HandleMessage(ctx context.Context, data []byte)`) keep their signature. The change is in `main.go` — pass the real per-message `ctx` from the traced iterator instead of `context.Background()`.

**Publisher interface** gains `ctx`:
```go
// Before:
type Publisher interface {
    Publish(subject string, data []byte) error
}

// After:
type Publisher interface {
    Publish(ctx context.Context, subject string, data []byte) error
}
```

The `natsPublisher` adapter wraps `otelnats.Conn.Publish(ctx, subj, data)`.

### Category B: Handlers accepting `jetstream.Msg` directly

**Services:** message-gatekeeper, message-worker, room-worker

Add `ctx` parameter:
```go
// Before:
func (h *Handler) HandleJetStreamMsg(msg jetstream.Msg)

// After:
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg)
```

For **message-gatekeeper**, closures also gain `ctx`:
- `publishFunc` becomes `func(ctx context.Context, subj string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)`
- `replyFunc` becomes `func(ctx context.Context, subj string, data []byte) error`

For **room-worker**, the publish closure gains `ctx`:
- `func(ctx context.Context, subj string, data []byte) error`

### Category C: Request/reply handlers accepting `*nats.Msg`

**Services:** history-service, room-service

Handler signatures change from `func(*nats.Msg)` to `func(otelnats.MsgWithContext)`:
```go
// Before:
func (h *Handler) NatsHandleHistory(msg *nats.Msg)

// After:
func (h *Handler) NatsHandleHistory(m otelnats.MsgWithContext)
```

Inside handlers, use `m.Msg` for the message and `m.Context()` for trace context.

For **room-service**, `RegisterCRUD(nc *nats.Conn)` becomes `RegisterCRUD(nc *otelnats.Conn)`, and all CRUD handlers adopt the same `MsgWithContext` pattern. The `publishToStream` closure gains `ctx`:
- `func(ctx context.Context, data []byte) error`

## Section 4: Consumer Message Loop Changes

### Pull-based iterator (5 services)

```go
// Before:
msg, err := iter.Next()
handler.HandleMessage(ctx, msg.Data())  // ctx = context.Background()

// After:
msgCtx, msg, err := iter.Next()  // oteljetstream.MessagesContext.Next()
handler.HandleMessage(msgCtx, msg.Data())
```

### Callback-based consumer (inbox-worker)

```go
// Before:
cons.Consume(func(msg jetstream.Msg) {
    handler.HandleEvent(ctx, msg.Data())
})

// After:
cons.Consume(func(m oteljetstream.MsgWithContext) {
    handler.HandleEvent(m.Context(), m.Data())
})
```

### Request/reply (history-service, room-service)

No message loop — `otelnats.Conn.QueueSubscribe` wraps handler calls automatically with span creation. Handlers receive `MsgWithContext`.

## Section 5: Test Impact

### No new test dependencies

Tests use `context.Background()` for trace context. The global no-op tracer is sufficient — no `TracerProvider` setup needed in unit tests.

### Changes per category

**Category A** (broadcast-worker, inbox-worker, notification-worker): Update `Publisher` mock to accept `ctx`. Handler test calls unchanged.

**Category B** (message-gatekeeper, message-worker, room-worker): Add `context.Background()` as first arg to handler calls in tests. Update closure mocks for `publishFunc`/`replyFunc`/publish closure.

**Category C** (history-service, room-service): Wrap test `*nats.Msg` in `otelnats.MsgWithContext{Msg: msg, Ctx: context.Background()}`.

### Mock regeneration

Run `make generate SERVICE=<name>` for any service whose store interface or publisher interface changed (if mocked via mockgen).

## Section 6: File Change Summary

### Shared packages

| File | Change |
|------|--------|
| `pkg/otelutil/otel.go` | Add `otel.SetTracerProvider` + `otel.SetTextMapPropagator` |
| `go.mod` / `go.sum` | Add `instrumentation-go/otel-nats` dependency |

### Per-service changes

| Service | `main.go` | `handler.go` | `handler_test.go` |
|---------|-----------|-------------|-------------------|
| broadcast-worker | `otelnats.Connect`, `oteljetstream.New`, `InitTracer`, pass `msgCtx` | Add `ctx` to `Publisher.Publish` | Update publisher mock |
| history-service | `otelnats.Connect`, `InitTracer` | `*nats.Msg` -> `MsgWithContext` | Wrap messages in `MsgWithContext` |
| inbox-worker | `otelnats.Connect`, `oteljetstream.New`, `InitTracer`, callback `MsgWithContext` | Add `ctx` to `Publisher.Publish` | Update publisher mock |
| message-gatekeeper | `otelnats.Connect`, `oteljetstream.New`, `InitTracer`, pass `msgCtx` | Add `ctx` to handler + closures | Add `ctx` to calls and mocks |
| message-worker | `otelnats.Connect`, `oteljetstream.New`, `InitTracer`, pass `msgCtx` | Add `ctx` param | Add `ctx` to calls |
| notification-worker | `otelnats.Connect`, `oteljetstream.New`, `InitTracer`, pass `msgCtx` | Add `ctx` to `Publisher.Publish` | Update publisher mock |
| room-service | `otelnats.Connect`, `oteljetstream.New`, `InitTracer` | `*nats.Msg` -> `MsgWithContext`, `RegisterCRUD(*otelnats.Conn)`, `ctx` to closure | Wrap messages, update mocks |
| room-worker | `otelnats.Connect`, `oteljetstream.New`, `InitTracer`, pass `msgCtx` | Add `ctx` to handler + closure | Add `ctx` to calls and mock |

### Files NOT changed

- `auth-service/*` — out of scope
- `pkg/natsutil/carrier.go` — kept as-is
- `pkg/stream/stream.go` — unaffected
- `pkg/subject/subject.go` — unaffected
- `pkg/model/*` — unaffected
- `mock_store_test.go` files — regenerated via `make generate`, not manually edited

### Totals

~20 files modified, 0 new files created.

---

## Addendum: Go 1.25.1 → 1.25.8 Upgrade

**Date:** 2026-04-02
**Status:** Approved

### Summary

Upgrade Go from 1.25.1 to 1.25.8 to pick up security and bug fixes from patch releases 1.25.2–1.25.8. Patch releases are backwards compatible per Go's release policy — no behavioral changes.

### Decision

| Decision | Choice |
|----------|--------|
| Approach | Bump go directive only (Approach 1) |
| Dockerfiles | No change — `golang:1.25-alpine` auto-resolves to latest 1.25.x |

### Files Changed

| File | Change |
|------|--------|
| `go.mod` | `go 1.25.1` → `go 1.25.8` |
| `go.sum` | Pruned by `go mod tidy` (74 stale entries removed, 4 added) |

### Verification

- All 18 test suites pass with `go test -race -count=1 ./...`
- Lint clean with `make lint`
