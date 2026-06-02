# Unified Worker Error Logging — Design Spec

**Date:** 2026-06-02
**Status:** **Considered & deferred (YAGNI)** — see "Decision: deferred" below. The design is recorded so the trade-off doesn't have to be re-discovered. Only the one concrete bug (message-worker missing `request_id`) was fixed directly.
**Follows on from:** `docs/superpowers/specs/2026-05-28-centralized-error-codes-design.md` (this would extend the errcode boundary to JetStream worker paths)

---

## Decision: deferred

Before implementing, we measured what the four candidate workers actually return:

| Worker | `errcode.*` returns | `WithCause` | raw `fmt.Errorf` |
|---|---|---|---|
| broadcast-worker | 0 | 0 | 32 |
| notification-worker | 0 | 0 | 9 |
| inbox-worker | 2 (Permanent markers) | 0 | 32 |
| message-worker | 0 | 0 | 58 |

This collapses the benefit:

- **Cause-chain preservation** (the strongest argument) only matters when a worker returns a *typed* `errcode.Error` carrying `WithCause` and logs it raw. **Zero** of these workers use `WithCause` — nothing is being dropped today.
- **`code`/`reason` queryability** is moot: everything is raw `fmt.Errorf`, so `LogJobError` would stamp a constant `code:"internal"` on all of them — a field that never varies doesn't help filtering.
- **Level discipline** barely differs: only `inbox-worker` has permanent failures, and it already logs them at WARN correctly.

The only concrete, non-theoretical gap was **`message-worker` omitting `request_id`** — fixed directly as a one-liner (`slog.Error("process message failed", "error", err, "request_id", natsutil.RequestIDFromContext(ctx))`), no new abstraction needed.

**Revisit this design if/when workers start adopting typed `errcode` errors (especially `WithCause`).** At that point the cause-loss bug and cross-service `code`/`reason` filtering become real and `LogJobError` pays for itself. Until then, building it would standardize things that don't actually differ — classic YAGNI.

The remainder of this document is the (still valid) design, retained for that future revisit.

---

---

## Problem

The centralized `pkg/errcode` work standardized the **client-facing** error pipeline: every NATS request/reply and Gin HTTP handler routes errors through `errcode.Classify`, which logs exactly one structured line (`code`/`reason`/`cause`/`underlying` + ctx attrs) at a category-aware level and returns a leak-safe envelope.

JetStream **worker** paths never go through that boundary — there is no client to reply to, only `Ack`/`Nak`. As a result, worker error logging drifted into **four** incompatible shapes:

1. **Inline in the consume loop** (`broadcast-worker`, `notification-worker`): `slog.Error("handle message failed", "error", err, "request_id", …)` — always ERROR, no `code`/`reason`.
2. **Permanence-branched inline** (`inbox-worker`): permanent → `slog.Warn("permanent event failure — dropping (Ack)", …)`; else `slog.Error("handle event failed", …)`. The only worker that varies level.
3. **Inside the handler** (`message-worker`): `slog.Error("process message failed", "error", err)` — always ERROR **and no `request_id`**.
4. **Reason-string model** (`search-sync-worker`): routes through `natsutil.Ack/Nak(msg, reason)`, which log only `"nak failed"` *if the ack/nak call itself errors*. The actual handler error is reduced to a human reason string (`"bulk action failed"`) — **no `code`/`reason`/`cause` line is ever emitted**.

`message-gatekeeper` is a hybrid: its client-reply path correctly uses `errnats.Marshal` (→ `Classify`), but its **infra** path logs `slog.ErrorContext("process message failed (infra)", …)` — a fifth shape.

### Consequences

- **Log shape / queryability:** worker errors carry no `code`/`reason`, so they cannot be filtered or alerted on uniformly alongside req/rep errors.
- **Level discipline:** the same conceptual condition (a permanent, non-retryable failure that will be dropped) logs at WARN in `inbox-worker` but ERROR everywhere else.
- **Cause-chain loss:** a typed `*errcode.Error` hides its `WithCause` in an unexported field. Raw `slog.Error("…", "error", err)` prints only `err.Error()` (the message) and **silently drops the underlying cause** — only `Classify`'s log core reaches into it via the `underlying` field. So worker logs can be strictly *less* informative than req/rep logs for the same error.
- **Missing `request_id`** in `message-worker`.

This is not a violation of the errcode plan (worker logging was out of its scope) but it is a real observability inconsistency.

## Goal

One worker-side logging helper, `errcode.LogJobError`, that is the JetStream counterpart to `Classify`: it emits the **identical canonical field set**, reuses `Classify`'s cause-chain logic (so `WithCause` is never lost), and picks a **permanence-driven** level. All five workers + `message-gatekeeper`'s infra path call it.

## Non-Goals

- **No change to Ack/Nak semantics.** This is logging-only. Each worker keeps its own retry/drop control flow exactly as today (including `inbox-worker`'s `IsPermanent`-driven Ack-vs-Nak branch — only its *logging* lines collapse into the helper).
- **No change to the client-facing pipeline.** `Classify`, `errnats`, `errhttp`, and `gatekeeper`'s client-reply path are untouched except for the internal extraction of a shared log core (a pure refactor with no behavior change).
- **No change to `natsutil.Ack/Nak`.** Those log only on an ack/nak *call* failure (orthogonal concern) and stay as-is.

---

## Design Decisions (locked)

1. **Scope is logging-only.** The helper logs and returns nothing. Ack/Nak stays in the caller.
2. **Approach: extract `Classify`'s log core inside `errcode`.** `Classify` is split into a pure `classify(err) *Error` + a shared `logErr(...)` emit core. A new exported `LogJobError(ctx, err)` reuses both. This keeps a single source of truth for log shape and stays **stdlib-only** (no new package, no `nats.go` import in core — consistent with the errcode dependency rule: `errnats`/`errhttp` are the only transport adapters).
3. **Level policy is permanence-driven.** `IsPermanent(err)` true → **WARN** (the message will be Ack'd/dropped — an expected, handled outcome); otherwise → **ERROR** (will be Nak'd/retried — actionable if it persists). This matches `inbox-worker`'s existing behavior and unifies the other four onto it.
4. **Field parity with `Classify`.** Both entry points emit the same keys via `logErr`; the only differences are the `msg` string (`"request failed"` vs `"job failed"`) and a `permanent` bool flag on the worker line.
5. **Raw `fmt.Errorf` remains the deliberate internal-error signal.** `LogJobError`, like `Classify`, treats any non-`*errcode.Error` as `internal` and logs its full cause chain. This implicitness is intentional (safe-by-default; one boundary owns logging) — but to close the discoverability gap, the convention "raw `fmt.Errorf("…: %w", err)` = 'this is internal, collapse and log it at the boundary'; `errcode.Internal(msg, WithCause(err))` = the explicit form when you want to be loud" is documented in `docs/error-handling.md`.

---

## API

All additions live in `pkg/errcode` (stdlib-only).

```go
// classify (private, NEW): the pure classifier extracted from Classify.
//   nil → nil; *errcode.Error in chain → that; else → Internal "internal error".
// Does NOT log.
func classify(err error) *Error

// logErr (private, NEW): the single emit core extracted from Classify.
// Computes the cause/underlying strings from err+e and emits ONE slog line
// (code, reason, cause, underlying?, + ctx attrs + any extra) at the given level.
func logErr(ctx context.Context, err error, e *Error, msg string, level slog.Level, extra ...any)

// Classify (UNCHANGED behavior): req/rep + HTTP boundary.
//   = classify + logErr(ctx, err, e, "request failed", e.logLevel())
func Classify(ctx context.Context, err error) *Error

// LogJobError (NEW): JetStream worker boundary. Logs once, returns nothing.
// nil-safe (no-op on nil), never panics (read-only). Ack/Nak stays in the caller.
//   e := classify(err); _, perm := IsPermanent(err)
//   level := ERROR; if perm { level = WARN }
//   logErr(ctx, err, e, "job failed", level, "permanent", perm)
func LogJobError(ctx context.Context, err error)
```

### Level matrix

| Condition | Level |
|---|---|
| `IsPermanent(err)` true (will be Ack'd / dropped) | `WARN` |
| otherwise (will be Nak'd / retried) | `ERROR` |

### Log shape

```jsonc
// req/rep (Classify) — UNCHANGED
{"level":"INFO","msg":"request failed","code":"forbidden","reason":"not_room_member","cause":"…","request_id":"…"}

// worker (LogJobError) — NEW; identical keys + permanent flag
{"level":"WARN","msg":"job failed","code":"internal","reason":"","cause":"unknown room type: …","permanent":true,"request_id":"…"}
{"level":"ERROR","msg":"job failed","code":"internal","reason":"","cause":"persist message: cassandra: timeout","permanent":false,"request_id":"…"}
```

`request_id` rides along automatically because workers already stamp it on `handlerCtx` via `natsutil.StampRequestID` and `logErr` uses `loggerFrom(ctx)` (the same mechanism `Classify` uses).

---

## Migration Map (per service, per line)

The consume loops stamp `handlerCtx`/`ctx` with `request_id` today; `LogJobError` takes that same ctx. The `Ack`/`Nak` calls and the `"failed to ack/nak"` logs are **unchanged**.

| Service | File:line (today) | Today | Change |
|---|---|---|---|
| **errcode (core)** | `pkg/errcode/classify.go` | `Classify` does classify+compute+emit inline | Extract `classify(err)` + `logErr(...)`; `Classify` delegates to them (no behavior change) |
| **errcode (core)** | `pkg/errcode/logjob.go` (NEW) | — | Add `LogJobError(ctx, err)` |
| **broadcast-worker** | `broadcast-worker/main.go:172` | `slog.Error("handle message failed", "error", err, "request_id", natsutil.RequestIDFromContext(handlerCtx))` | → `errcode.LogJobError(handlerCtx, err)` |
| **notification-worker** | `notification-worker/main.go:136` | `slog.Error("handle message failed", "error", err, "request_id", …)` | → `errcode.LogJobError(handlerCtx, err)` |
| **inbox-worker** | `inbox-worker/main.go:305` | `slog.Warn("permanent event failure — dropping (Ack)", "error", err, "request_id", …)` | → `errcode.LogJobError(handlerCtx, err)` (level now auto-WARN from permanence). **Keep** the surrounding `IsPermanent` branch — it still drives `Ack` here |
| **inbox-worker** | `inbox-worker/main.go:311` | `slog.Error("handle event failed", "error", err, "request_id", …)` | → `errcode.LogJobError(handlerCtx, err)` (auto-ERROR). **Keep** the surrounding `Nak` |
| **message-worker** | `message-worker/handler.go:44` | `slog.Error("process message failed", "error", err)` (no request_id) | → `errcode.LogJobError(ctx, err)` (gains request_id + standard shape). Confirm the consume loop stamps `ctx` before `HandleJetStreamMsg`; if not, stamp it |
| **message-gatekeeper** | `message-gatekeeper/handler.go:108` | `slog.ErrorContext(ctx, "process message failed (infra)", "error", err, "account", account, "room_id", roomID)` | → `errcode.LogJobError(ctx, err)`. `account`/`room_id`/`request_id` are **already** on `ctx` via `WithLogValues` (handler.go:66-82), so the explicit fields are dropped and picked up automatically. **Client-reply path via `errnats.Marshal` is untouched** |

**Notes:**
- `message-worker` is a clean one-line swap that *gains* `request_id` for free: the consume loop already stamps `handlerCtx` (main.go:156) and passes it as `ctx`, so `LogJobError(ctx, err)` picks up the request_id the current `slog.Error("process message failed", "error", err)` omits.
- `inbox-worker` is the one place where the `IsPermanent` check is *also* a control-flow branch (Ack vs Nak). The refactor must collapse only the two `slog` lines into `LogJobError` and leave the Ack/Nak branch intact.

### Excluded: `search-sync-worker`

`search-sync-worker` is deliberately **out of scope**. Its error model does not fit a per-message errcode boundary:
- It flushes a **batch** of buffered messages at once (`Flush`), so there is no single per-message `err` or `request_id` at the failure site — the errcode boundary is per-message.
- Its failures are **Elasticsearch bulk-result shaped**, not Go-error-with-classification shaped: per-item `status`/`docID`/`index`/`ErrorType` (handler.go:133) and a batch-level count-mismatch (handler.go:121). Only one site (handler.go:104, the `h.store.Bulk` Go error) is a real `error`, and it is already a well-structured `slog.Error("bulk request failed", "error", err, "actions", …)` whose useful fields (`actions` count) `LogJobError` would not carry.

Forcing it through `LogJobError` would be square-peg-round-hole: it would strip ES-specific diagnostics and attach a meaningless `code:"internal"` with no `request_id`. Its existing structured `slog` lines already satisfy the CLAUDE.md logging rules (JSON, no secrets). It is left as-is.

---

## Testing Strategy

### Core (`pkg/errcode`) — where the behavior lives

TDD unit tests for `LogJobError` (capture a `slog` JSON buffer, assert fields):
- raw infra error (`fmt.Errorf("…: %w", errors.New("…"))`) → `ERROR`, `code:"internal"`, `cause` contains the full chain, `permanent:false`.
- typed `*errcode.Error` with `WithCause` → `ERROR`, `code`/`reason` set, `underlying` field present (proves cause-chain is not lost).
- `errcode.Permanent(errcode.NotFound("…", WithReason(…)))` → **`WARN`**, `permanent:true`, `reason` preserved.
- `errcode.Permanent` wrapping an `internal` → `WARN`, `code:"internal"`.
- `nil` err → no log line, no panic.
- `request_id`/domain attrs from `WithLogValues(ctx, …)` appear on the line.
- **Field-parity test:** assert `LogJobError` and `Classify` emit the same key set (minus `msg`, plus `permanent`) for the same underlying error.

Refactor safety: existing `Classify` tests must remain green unchanged (proves the `classify`/`logErr` extraction is behavior-preserving).

### Services

Worker migrations are logging-only, so existing handler/integration tests cover correctness (no behavior to assert). Where a worker test currently asserts a specific log message string, update it to the new `"job failed"` shape. No new service-level tests required beyond those string updates.

---

## Docs

- **`docs/error-handling.md` §4 (Logging contract):** add `LogJobError` as the worker-side counterpart to `Classify`, with the permanence→level table and the "Ack/Nak stays in the caller" note. Add the discoverability sentence from Decision 5 (raw `fmt.Errorf` = deliberate internal signal; `errcode.Internal(WithCause)` = the explicit form).
- **`pkg/errcode/doc.go`:** one line in the logging-contract section noting the two boundary entry points (`Classify` for req/rep+HTTP, `LogJobError` for JetStream workers).

---

## Files Changed

**Modified (core):**
- `pkg/errcode/classify.go` — extract `classify` + `logErr`; `Classify` delegates.
- `pkg/errcode/doc.go` — logging-contract note.

**New (core):**
- `pkg/errcode/logjob.go` + `logjob_test.go` — `LogJobError` + tests.

**Modified (services):**
- `broadcast-worker/main.go`, `notification-worker/main.go`, `inbox-worker/main.go`, `message-worker/handler.go`, `message-gatekeeper/handler.go` — migrate to `LogJobError`.
- Any worker `*_test.go` that pins a log-message string.
- (`search-sync-worker` is excluded — see "Excluded" above.)

**Modified (docs):**
- `docs/error-handling.md`.

---

## Rollout / Risk

- Logging-only change ⇒ no wire-contract, no Ack/Nak, no retry-semantics impact. Safe to ship behind no flag.
- The single behavioral *observable* change: `broadcast`/`notification`/`message-worker`/`gatekeeper-infra` permanent failures (if any are wrapped via `errcode.Permanent`) now log at WARN instead of ERROR. This is the intended unification; note it in the PR so dashboards/alerts keyed on those services' ERROR volume are reviewed.
