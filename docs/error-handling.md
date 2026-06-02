# Error Handling Guide

How to produce client-facing errors in this codebase. The canonical source is
`pkg/errcode` (and its adapters `errnats` for NATS, `errhttp` for Gin); this
guide is a developer-facing walkthrough.

For the client-side view of the wire envelope (what callers see and how to
branch), see `docs/client-api.md` §6.

---

## 1. The contract

Every client-facing error is an `*errcode.Error` that marshals to:

```json
{
  "error":    "<human-readable, user-safe message>",
  "code":     "<one of 8 generic categories>",
  "reason":   "<optional, domain-specific machine code>",
  "metadata": { "<key>": "<value>" }
}
```

- `code` is **always present** and drives HTTP status.
- `reason` is **optional**; declare it only when the frontend must distinguish
  cases that the generic `code` cannot.
- `metadata` is **client-visible** structured detail (`map[string]string`).
- The cause attached via `WithCause` is **never serialized** — it is logged
  server-side once by `Classify` and reachable via `Unwrap()`/`errors.Is`/`As`.

The eight generic categories and HTTP statuses:

| Constant                       | Wire `code`         | HTTP |
|--------------------------------|---------------------|------|
| `errcode.CodeBadRequest`       | `bad_request`       | 400  |
| `errcode.CodeUnauthenticated`  | `unauthenticated`   | 401  |
| `errcode.CodeForbidden`        | `forbidden`         | 403  |
| `errcode.CodeNotFound`         | `not_found`         | 404  |
| `errcode.CodeConflict`         | `conflict`          | 409  |
| `errcode.CodeTooManyRequests`  | `too_many_requests` | 429  |
| `errcode.CodeUnavailable`      | `unavailable`       | 503  |
| `errcode.CodeInternal`         | `internal`          | 500  |

`503 vs 429`: `unavailable` is server-wide saturation (admission control,
expand-timeout); `too_many_requests` is per-caller rate limiting / quota.

---

## 2. Producing errors

### The common case — a typed client error

```go
return nil, errcode.BadRequest("name is required")
return nil, errcode.NotFound("room not found")
return nil, errcode.Forbidden("only owners can update roles")
return nil, errcode.Conflict("room is at maximum capacity",
    errcode.WithReason(errcode.RoomMaxSizeReached))
```

Use the **named constructor** (`BadRequest`, `Unauthenticated`, `Forbidden`,
`NotFound`, `Conflict`, `TooManyRequests`, `Unavailable`, `Internal`). There
are no `*f` variants on purpose — they would silently swallow trailing
`Option` args. For dynamic text, format the message at the call site:

```go
return nil, errcode.BadRequest(
    fmt.Sprintf("batch size %d exceeds limit %d", n, max))
```

`errcode.New(code, msg, opts...)` is the escape hatch for a dynamically chosen
category; semgrep warns when you pass a literal `errcode.CodeX` to it
(prefer the named constructor in that case).

### Infra / DB / third-party errors

Don't manually classify them — return the wrapped raw error and let `Classify`
collapse it to `internal`/"internal error" at the boundary (the real cause is
logged once, never sent):

```go
if err := h.store.Find(ctx, id); err != nil {
    return nil, fmt.Errorf("loading room: %w", err) // → client sees "internal error"
}
```

### Attaching a cause for server-side debugging

```go
return nil, errcode.BadRequest("invalid ensure-room-key request",
    errcode.WithCause(err))
```

`WithCause` panics if `err` already contains an `*errcode.Error` — the
invariant is **one `*errcode.Error` per chain**, propagated via a single `%w`.
Never wrap a message body, token, or any secret into a cause; the cause is
included in the server log line.

### Client-visible metadata

```go
return nil, errcode.Conflict("room is at maximum capacity",
    errcode.WithReason(errcode.RoomMaxSizeReached),
    errcode.WithMetadata("limit", strconv.Itoa(max)))
```

`WithMetadata` is **client-visible** (ships in the envelope). For server-only
attributes — request_id, account, roomID — use `WithLogValues` (next section).
Mixing them up is a leak risk.

---

## 3. Replying

You never marshal the envelope yourself; the adapter does it (and logs once):

| Transport            | Adapter                                            |
|----------------------|----------------------------------------------------|
| NATS sync reply      | `errnats.Reply(ctx, msg, err)`                     |
| NATS already-logged  | `errnats.ReplyQuiet(msg, err)` (panic backstop / `replyBusy`) |
| Gin HTTP             | `errhttp.Write(ctx, c, err)`                       |

Handlers registered via `pkg/natsrouter` are automatic: returning a typed
errcode error from the handler routes through `errnats.Reply`. JetStream
consumers / raw NATS handlers call `errnats.Reply` directly.

---

## 4. Logging contract

`errcode.Classify(ctx, err)` emits **exactly one** `slog` line per failed
request, at a **category-aware level**:

- `internal`, `unavailable` → `ERROR`
- all expected client errors (`bad_request`, `unauthenticated`, `forbidden`,
  `not_found`, `conflict`, `too_many_requests`) → `INFO`

This keeps routine 4xx validation failures out of the ERROR stream so
error-rate alerting stays meaningful. **Handlers must not log-then-reply** —
the reply path logs.

Attach domain context once at handler entry. The seam differs by handler style:

- **natsrouter handler** (`*natsrouter.Context`): use the cycle-safe method
  `c.WithLogValues("account", a, "roomID", r)`.
- **Gin or raw NATS** (plain `context.Context`): use the package func
  `ctx = errcode.WithLogValues(ctx, "request_id", id, "account", a, ...)`.

The `request_id`/`account`/`roomID` then appear in the centralized Classify
log line and any downstream slog usage in the chain.

> **Why two APIs?** `*natsrouter.Context` implements `context.Context` and
> delegates `Value(key)` lookups to an inner `ctx` field. Calling
> `errcode.WithLogValues(c, …)` would derive a new ctx whose parent is `c` —
> any subsequent `c.Value(otherKey)` would loop. The method (`c.WithLogValues`)
> derives from the inner field, avoiding the cycle.

---

## 5. Adding a new `reason`

Reasons are **per-service catalogs** in `pkg/errcode/codes_<service>.go`
(declared as `Reason` constants — never `errcode.Reason("...")` inline; semgrep
will reject it).

1. Pick a `flat_snake_case` machine code (e.g. `bot_rate_limited`).
2. Add it to the right catalog:
   ```go
   // pkg/errcode/codes_room.go
   RoomBotRateLimited Reason = "bot_rate_limited"
   ```
3. Add the constant to `allReasons` in `pkg/errcode/codes_test.go` (the
   snake-case + uniqueness tests pick it up automatically).
4. Use it: `errcode.TooManyRequests("bot quota exceeded",
   errcode.WithReason(errcode.RoomBotRateLimited))`.
5. Update `docs/client-api.md` §6 reason catalog AND the relevant endpoint
   error table in the SAME PR (CLAUDE.md client-API rule).

Only add a reason when the frontend genuinely needs to distinguish it from
other errors of the same category. Most cases are generic.

---

## 6. Wrapping invariant — allowed vs forbidden

**Invariant:** at most one `*errcode.Error` per error chain, propagated via a
single `%w`.

**Allowed:**

```go
return errcode.BadRequest("name is required")
return errcode.NotFound("x", errcode.WithReason(RoomNotMember))
return errcode.Internal("x", errcode.WithCause(rawDBErr))      // RAW cause only
return fmt.Errorf("checking room: %w", typedErr)               // typed survives
return typedErr                                                 // bare propagation
```

**Forbidden (semgrep-flagged + panics at runtime):**

```go
return errcode.Internal("x", errcode.WithCause(anotherErrcodeErr)) // PANIC
return fmt.Errorf("%w and %w", errcodeA, errcodeB)                 // Classify picks one
```

---

## 7. Lint enforcement

`.semgrep/errcode.yml` (wired into `make sast`) enforces:

| Rule                                       | Severity | What it catches |
|--------------------------------------------|----------|-----------------|
| `errcode-no-reason-literal-outside-catalog`| ERROR    | Inline `errcode.Reason("...")` outside `codes_*.go` |
| `errcode-withcause-must-not-wrap-errcode`  | ERROR    | `errcode.WithCause(errcode.X(...))` literal |
| `errcode-no-multi-wrap-errcode`            | ERROR    | `fmt.Errorf("%w … %w")` mixing typed errors |
| `errcode-prefer-named-constructor`         | WARNING  | `errcode.New(errcode.CodeX, msg)` literal |

CI runs `make sast` on every PR.

---

## 8. Testing

Use `pkg/errcode/errtest` to assert on a decoded reply payload:

```go
import "github.com/hmchangw/chat/pkg/errcode/errtest"

errtest.AssertCode(t, replyBytes, errcode.CodeNotFound)
errtest.AssertReason(t, replyBytes, errcode.RoomNotMember)
e := errtest.Decode(t, replyBytes) // for ad-hoc checks
```

For in-process matching on chained errors:

```go
if errcode.HasReason(err, errcode.RoomNotMember) { /* … */ }
r := errcode.ReasonOf(err) // "" if no errcode error in chain
```

---

## 9. JetStream consumers — `errcode.Permanent`

JetStream handlers face a different question than request/reply handlers: on
failure, do we **Ack** (drop the message) or **Nak** (let JetStream redeliver)?
The category alone can't answer it — an `Internal` from a deterministic bug
should drop, while a transient infra `Internal` should retry. The marker is
**explicit**:

```go
if err := json.Unmarshal(data, &req); err != nil {
    // Malformed payload: redelivery won't help. Ack via Permanent.
    return errcode.Permanent(errcode.BadRequest("unmarshal X"))
}
// Transient infra failure: bare error → consumer Naks for redelivery.
if err := h.store.Save(ctx, &row); err != nil {
    return fmt.Errorf("save row: %w", err)
}
```

The consume loop in `main.go` reads the marker:

```go
if _, ok := errcode.IsPermanent(err); ok {
    msg.Ack() // poison-pill drop; client already got the AsyncJobResult.
    return
}
msg.Nak() // transient — retry.
```

`Permanent` wraps an `*errcode.Error` so `fillAsyncError` can still extract
`Code` / `Reason` for the `AsyncJobResult` envelope; the wrapper is invisible
to clients (it isn't serialized). `errors.Is(err, errcode.ErrPermanent)` is
the sentinel-style match if you don't need the wrapped `*Error`.

**Don't** infer permanence from `Code`: an `Internal` can be either a poison-
pill (bad payload classified to internal by Classify) or a retryable
infra-down condition. Wrap explicitly at the call site.

---

## 10. Migration history

This package replaced four legacy patterns (all removed in `pkg/natsrouter`
cleanup):

- `pkg/natsrouter`'s `RouteError` + `Err*` constructors + `Code*` consts
- `pkg/natsutil`'s `MarshalError` / `MarshalErrorWithCode` / `ReplyError` /
  `TryParseError`
- `pkg/model.ErrorResponse`
- `auth-service`'s ad-hoc `gin.H{"error": ...}`

See `docs/superpowers/specs/2026-05-28-centralized-error-codes-design.md` for
the design rationale and the per-service error contract.
