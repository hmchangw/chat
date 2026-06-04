# Centralized Error Codes — Design Spec

**Date:** 2026-05-28  
**Updated:** 2026-06-02 (post-implementation amendments — see §Amendment Log)  
**Status:** Implemented

---

## Overview

A shared Go package at `pkg/errcode/` that is the single source of client-facing error envelopes for every transport in the chat system (NATS request/reply, JetStream replies, Gin HTTP). It replaces four incompatible error-reply patterns that exist today:

1. `pkg/natsrouter` — `RouteError` + `Err*` constructors + `Code*` string consts.
2. `pkg/natsutil` — `MarshalError` / `MarshalErrorWithCode` / `ReplyError` / `TryParseError`.
3. `pkg/model.ErrorResponse` — hand-built reply struct.
4. `auth-service` — ad-hoc `gin.H{"error": ...}`.

The package produces one wire envelope, `{error, code, reason?, metadata?}`, centralizes server-side logging at a single classification boundary, and makes two classes of bug structurally impossible: leaking an internal cause to a client, and mixing the generic code with the specific reason.

## Goals

- One transport-neutral error type marshalling to a stable envelope `{error, code, reason?, metadata?}`.
- A closed set of generic categories (`code`) that map to standard REST/HTTP status, plus an open set of domain `reason`s the frontend switches on.
- Compile-time separation of `code` and `reason` (distinct Go types).
- Infra/DB/third-party errors always collapse to `internal` with a safe message — never leak a cause.
- Exactly one server-side log line per failed request, at a category-appropriate level.
- Thin transport adapters for NATS (`errnats`) and Gin (`errhttp`); core stays transport-neutral.
- Lint-enforced invariants (semgrep) so the guarantees survive future code.
- A clean migration that keeps every intermediate commit compiling and bisectable.

## Non-Goals

- Changing the JetStream Ack/Nak retry semantics of any worker (the envelope is independent of the retry decision; permanence stays an explicit, separate signal).
- i18n / localized error messages (messages remain English, server-authored).
- Error codes for purely internal (non-client-facing) errors — those stay raw `fmt.Errorf` and collapse to `internal` at the boundary.
- A registry/enum of every possible reason across services in one file — reasons live per-service.

---

## Wire Envelope

```json
{
  "error": "room is full",
  "code": "conflict",
  "reason": "max_room_size_reached",
  "metadata": { "limit": "500" }
}
```

| Field | Required | Meaning |
|-------|----------|---------|
| `error` | yes | Human-readable, user-safe message. Existing field name, preserved. |
| `code` | yes | One of 7 generic categories. Drives HTTP status. Always present. |
| `reason` | no | Domain-specific machine code the frontend branches on. Omitted when absent. |
| `metadata` | no | `map[string]string` of structured, client-visible detail. Omitted when empty. |

**Frontend rule:** the trigger a client switches on is `reason ?? code` — specific when present, generic otherwise.

The 8 categories and their HTTP status:

| `code` | HTTP | Use |
|--------|------|-----|
| `bad_request` | 400 | Malformed/invalid input |
| `unauthenticated` | 401 | Missing/expired/invalid credentials |
| `forbidden` | 403 | Authenticated but not permitted |
| `not_found` | 404 | Target does not exist |
| `conflict` | 409 | State conflict (duplicate, capacity, last-owner) |
| `too_many_requests` | 429 | Per-caller rate limiting / quota exceeded |
| `unavailable` | 503 | Transient server saturation/timeout (admission, expand timeout) |
| `internal` | 500 | Anything unclassified; the default collapse target |

> **Open item (`unavailable` mapping):** admission-control "service busy" is arguably HTTP 429. This design keeps 503 (the NATS services don't surface HTTP; only matters if an HTTP service later needs rate-limit semantics). Revisit then.

---

## Design Decisions (locked)

1. **Two distinct Go types** — `type Code string` (the 7 generics; the wire `code`) and `type Reason string` (open domain set; the wire `reason`). The compiler rejects `New(SomeReason, …)` and `WithReason(SomeCode)`. Chosen over a single `Code` type for compile-time safety.
2. **Generic categories live in core; domain reasons live in `codes_<service>.go`** as `Reason` constants — importable across the flat `package main` services, compiler-unique, one catalog per service.
3. **Cause never leaks** — `Error.cause` is unexported, so `encoding/json` cannot serialize it. Reachable only via `Unwrap()` for logging and `errors.Is`/`As`.
4. **Infra errors collapse to `internal`** — `Classify` maps any non-`*errcode.Error` to `internal` with message `"internal error"`, keeping the original chain as the (unserialized) cause.
5. **Centralized, level-aware logging** — `Classify` emits exactly one `slog` line; level is category-aware (`internal`/`unavailable` → ERROR, expected client errors → INFO) so routine 4xx don't pollute error alerting. Handlers never log-then-reply.
6. **One way to construct** — named constructors (`errcode.BadRequest(msg, opts...)`), one per category; no `*f` variants (they silently swallow trailing options). Dynamic text uses `errcode.BadRequest(fmt.Sprintf(...), opts...)`.
7. **Functional options** — `WithReason`, `WithMetadata`, `WithCause`.
8. **Single `*errcode.Error` per chain** — `WithCause` panics if the cause already carries an `*errcode.Error` (semgrep also flags the literal form). Propagate typed errors with a single `%w` or bare `return`. Multi-`%w` mixing is forbidden and semgrep-flagged.
9. **Trust boundary on options** — `WithMetadata` is client-visible (ships in the envelope); `WithLogValues` is server-only (never serialized). Causes attached via `WithCause` are logged via `err.Error()`, so they must never wrap raw message bodies, tokens, or secrets.
10. **DM-already-exists is a success, not an error** — room-service returns `model.CreateRoomReply{Status:"exists", RoomID:…}` instead of an error envelope the client treats as success. (Breaking contract change; co-released with the frontend.)
11. **Migration uses shims, not mid-plan deletion** — `natsrouter.RouteError`/`Err*` become thin delegating shims, deleted only in the cleanup chapter, so every commit compiles.

---

## Package Structure

```text
pkg/errcode/
├── category.go        # type Code + 7 Code constants + HTTPStatus()
├── reason.go          # type Reason
├── error.go           # type Error{Code,Reason,Message,Metadata,cause} + Error/Unwrap/HTTPStatus
├── options.go         # Option, New, named constructors, WithReason/WithMetadata/WithCause
├── classify.go        # Classify(ctx,err) + category-aware logLevel
├── parse.go           # Parse([]byte) for RPC clients
├── match.go           # ReasonOf / HasReason
├── logctx.go          # WithLogger / WithLogValues / loggerFrom
├── doc.go             # package contract + invariants
├── codes_room.go      # Reason consts for room-service / room-worker
├── codes_message.go   # Reason consts for message-gatekeeper
├── codes_search.go    # (placeholder; none needed today)
├── codes_auth.go      # Reason consts for auth-service
├── *_test.go
├── errnats/           # NATS adapter: Marshal/Reply + MarshalQuiet/ReplyQuiet
├── errhttp/           # Gin adapter: Write
└── errtest/           # test helper: Decode/AssertCode/AssertReason
```

Dependency direction: `errnats`/`errhttp`/`errtest` → `errcode` → stdlib only. `pkg/natsrouter` → `errcode` + `errnats` (never Gin). `pkg/model` must NOT import `errcode` (it stores `code`/`reason` as plain strings where needed).

---

## API Surface

### Code and Reason

```go
// Code is the closed set of generic classifications; the wire `code` field.
type Code string

const (
    CodeBadRequest      Code = "bad_request"
    CodeUnauthenticated Code = "unauthenticated"
    CodeForbidden       Code = "forbidden"
    CodeNotFound        Code = "not_found"
    CodeConflict        Code = "conflict"
    CodeTooManyRequests Code = "too_many_requests"
    CodeUnavailable     Code = "unavailable"
    CodeInternal        Code = "internal"
)

// HTTPStatus maps a Code to its HTTP status; unknown → 500.
func (c Code) HTTPStatus() int

// Reason is an open set of domain machine codes; the wire `reason` field.
// Concrete values are declared per-service in codes_<service>.go.
type Reason string
```

### Error

```go
// Error is the canonical client-facing error. It marshals to
// {code, reason?, error, metadata?}. cause is UNEXPORTED — encoding/json
// cannot serialize it; it exists only for server-side logging and
// errors.Is/As traversal.
type Error struct {
    Code     Code              `json:"code"`
    Reason   Reason            `json:"reason,omitempty"`
    Message  string            `json:"error"`
    Metadata map[string]string `json:"metadata,omitempty"`
    cause    error
}

func (e *Error) Error() string  // message only, never the cause
func (e *Error) Unwrap() error  // the cause (not serialized)
func (e *Error) HTTPStatus() int
```

### Constructors and options

```go
// New builds an Error with a dynamic Code. Prefer the named constructors.
func New(code Code, message string, opts ...Option) *Error

// Named constructors — the primary API. One per category.
func BadRequest(msg string, opts ...Option) *Error
func Unauthenticated(msg string, opts ...Option) *Error
func Forbidden(msg string, opts ...Option) *Error
func NotFound(msg string, opts ...Option) *Error
func Conflict(msg string, opts ...Option) *Error
func Unavailable(msg string, opts ...Option) *Error
func Internal(msg string, opts ...Option) *Error

type Option func(*Error)

// WithReason attaches the domain code (accepts only Reason).
func WithReason(r Reason) Option
// WithMetadata attaches CLIENT-VISIBLE key/value pairs (even count; panics on odd).
func WithMetadata(kv ...string) Option
// WithCause attaches a RAW underlying error for logging. Panics if the cause
// already carries an *errcode.Error (one-Error-per-chain invariant).
func WithCause(err error) Option
```

### Classification, parsing, matching

```go
// Classify converts any error to a client-safe *Error and logs it exactly once
// (category-aware level). nil→nil; *errcode.Error in chain→that; else→internal.
// The single boundary every adapter calls before replying.
func Classify(ctx context.Context, err error) *Error

// Parse decodes a reply payload into an *Error iff it is an error envelope
// (non-empty "error"). Used by RPC clients to detect remote failures.
func Parse(data []byte) (*Error, bool)

// ReasonOf returns the Reason of the first *Error in err's chain, or "".
func ReasonOf(err error) Reason
// HasReason reports whether err's chain carries an *Error with reason r.
func HasReason(err error, r Reason) bool
```

### Logging context

```go
// WithLogger stores an explicit logger (mainly tests).
func WithLogger(ctx context.Context, l *slog.Logger) context.Context
// WithLogValues returns ctx whose logger carries the given SERVER-ONLY attrs.
// Call once at handler entry; Classify's log line then includes them.
func WithLogValues(ctx context.Context, args ...any) context.Context
```

natsrouter handlers use the cycle-safe method instead of the package func:

```go
// (*natsrouter.Context).WithLogValues enriches the handler logger, deriving
// from the inner ctx (never from the Context itself, which would cycle).
func (c *Context) WithLogValues(args ...any)
```

### Transport adapters

```go
// errnats
func Marshal(ctx context.Context, err error) []byte         // classify+log, return envelope
func Reply(ctx context.Context, msg *nats.Msg, err error)    // classify+log, respond
func MarshalQuiet(err error) []byte                          // NO log (already-logged paths)
func ReplyQuiet(msg *nats.Msg, err error)                    // NO log

// errhttp
func Write(ctx context.Context, c *gin.Context, err error)   // classify+log, c.JSON(status, env)

// errtest
func Decode(t *testing.T, data []byte) *errcode.Error
func AssertCode(t *testing.T, data []byte, want errcode.Code)
func AssertReason(t *testing.T, data []byte, want errcode.Reason)
```

---

## Behavioral Specifications

### Classification and logging

- `Classify(ctx, nil)` returns `nil`.
- If any `*errcode.Error` is in the chain (`errors.As`), it is returned verbatim — category, reason, metadata preserved through `fmt.Errorf("…: %w", typed)` wrapping.
- Otherwise the error becomes `internal` / `"internal error"`, with the original chain kept as the unserialized cause.
- Exactly one `slog` line per call, keyed `code`, `reason`, `cause` (the full chain via `err.Error()`), plus any `WithLogValues` attrs (request_id, account, roomID, …).
- Level: `internal`/`unavailable` → ERROR; `bad_request`/`unauthenticated`/`forbidden`/`not_found`/`conflict` → INFO.
- Already-logged transport paths (natsrouter panic backstop, `replyBusy`) use `MarshalQuiet`/`ReplyQuiet` to avoid a redundant second line.

### Leak guarantee

`Error.cause` is unexported ⇒ `json.Marshal` omits it. A round-trip test asserts a marshalled envelope never contains the cause string. `Error.Error()` returns the message only.

### Wrapping invariant

At most one `*errcode.Error` per chain, propagated by a single `%w` or bare `return`. `WithCause` panics on a nested `*errcode.Error`; semgrep flags both the literal `WithCause(errcode.X(...))` and multi-`%w` mixing.

---

## Per-Service Error Contract

This is the externally observable contract each migrated service emits. (The implementation plan carries the exhaustive per-site mapping; this is the summary.)

### room-service (33 sentinels + inline errors)

All `helper.go` sentinels map to a category, a subset carry reasons:

| Reason | Category | Condition |
|--------|----------|-----------|
| `not_room_member` | forbidden | actor not a member |
| `not_room_owner` | forbidden | actor not an owner |
| `last_owner_cannot_leave` | conflict | last owner leaving |
| `bot_in_channel` | bad_request | bot in a channel room |
| `bot_not_available` | not_found | bot lookup miss |
| `max_room_size_reached` | conflict | capacity exceeded |
| `dm_already_exists` | *(removed — now a success reply, see below)* |

Sentinels without a reason map to generic categories (invalid input → bad_request; permission → forbidden; duplicate/last-member → conflict; missing → not_found; channel-expand timeout → unavailable). **Critical:** the deleted `sanitizeError` allowlist currently passes through ~14 *inline* `fmt.Errorf` sites ("only owners can…", the "invalid request" family, "cannot add members", "requester not in room", mute-toggle) that are NOT sentinels — these are re-homed to typed errcodes at the source before the allowlist is deleted, or they would silently collapse to `internal`.

**DM-already-exists:** returns `model.CreateRoomReply{Status:"exists", RoomID:…}` (success), not an error.

**Cross-service:** `memberlist_client.go` decodes the remote room-service reply via `errcode.Parse` and remaps `reason==not_room_member` to the local sentinel (replacing brittle message-string equality). Mixed-version rollout: a legacy remote without `code` degrades to `internal`/no-reason — acceptable.

### message-gatekeeper

| Reason | Category | Condition |
|--------|----------|-----------|
| `large_room_post_restricted` | forbidden | non-owner/admin posting in a large room |
| `not_subscribed` | forbidden | sender not subscribed to the room |

All other client-facing validation errors (missing/malformed fields, bad subject, invalid payload) become explicit `bad_request`/`not_found` — they must be typed errcodes, not raw `fmt.Errorf`, or they collapse to `internal`. The infra-vs-validation **Ack/Nak** decision is unchanged (keyed on `infraError`/sentinel identity, independent of the envelope).

### auth-service (HTTP)

| Reason | Category (HTTP) | Condition |
|--------|-----------------|-----------|
| `sso_token_expired` | unauthenticated (401) | expired SSO token |
| `invalid_sso_token` | unauthenticated (401) | invalid SSO token |
| — | bad_request (400) | missing/invalid params |
| — | internal (500) | NATS-token generation failure |

The 500 body changes from `"failed to generate NATS token"` to `"internal error"` (cause logged, not sent). Success and `/healthz` responses are untouched. **Gated** on PM confirmation of the new `unauthenticated` category; fallback folds 401→403 (`forbidden`) with the same reasons.

### room-worker (async + sync-DM)

- `AsyncJobResult` gains `Code string` / `Reason string` (json, omitempty; `pkg/model` stays errcode-free).
- **Permanence is explicit, never inferred from category.** A `permanentError` wrapper carries the `*errcode.Error` and drives Ack (permanent) vs Nak (retryable). Many genuinely permanent errors (collision, key-absent, unknown room type) classify to `internal`, so category-inference would Nak them forever — the explicit marker prevents that.
- The room-key-absent alert sentinel is attached via `WithCause(errRoomKeyAbsent)`, so both `errors.As` (find the errcode) and `errors.Is` (alert) resolve in one chain.
- The async consumer goroutine gains a `recover()` (it runs outside natsrouter's recovery; a `WithCause`/`WithMetadata` misuse would otherwise crash the worker).

### history-service, search-service, mock-user-service

Straight mechanical mapping of `natsrouter.Err*` → `errcode` named constructors; no domain reasons required today (codes_search.go is a placeholder). search-service's Prometheus status-label path reads the code via `errors.As` (no second log); `query_rooms.go`'s exported `*natsrouter.RouteError` return type changes to `*errcode.Error`.

---

## natsrouter Decoupling

All error semantics move out of `pkg/natsrouter` into `pkg/errcode`. natsrouter becomes a transport that calls `errnats.Reply`. During migration `RouteError` is a type alias (`= errcode.Error`) and `Err*`/`Code*` are delegating shims, so production callers keep compiling; the shims (and `ReplyRouteError`) are deleted in the cleanup chapter. A new cycle-safe `Context.WithLogValues` seam lets natsrouter handlers attach domain attrs. `Context.ReplyError` and the deserialize-failure path are migrated to errcode too (previously they emitted a `code`-less body).

---

## Enforcement (semgrep)

Custom rules at `.semgrep/errcode.yml`, wired into `make sast` (blocking CI gate):

- `errcode-no-reason-literal-outside-catalog` — `errcode.Reason("...")` only in `codes_*.go`.
- `errcode-withcause-must-not-wrap-errcode` — `WithCause(errcode.X(...))` forbidden.
- `errcode-no-multi-wrap-errcode` — `fmt.Errorf("…%w…%w…")` mixing forbidden.
- `errcode-prefer-named-constructor` (warning) — steer `New(CodeX, …)` literals to the named constructor.

---

## Frontend Contract

- The transport error type gains `reason?: string` and `metadata?: Record<string,string>`.
- UI logic branches on `reason ?? code`; generic handling keys on `code`.
- Create-DM handles the new `{status:"exists", roomId}` success (navigate to the room) — **must ship in the same release as room-service** (the old client keyed on `.error`).
- The `AsyncJobResult` decoder reads `code`/`reason`.

---

## Testing Strategy

- **Core (`pkg/errcode`):** unit tests for `HTTPStatus`, leak guarantee (marshalled envelope never contains the cause), `Unwrap`/`errors.Is`, constructor + option behavior (incl. `WithCause`/`WithMetadata` panics), `Classify` (nil, unknown→internal, typed-through-wrapping, ctx values, category-aware level), `Parse`, `ReasonOf`/`HasReason`. ≥80% coverage; ≥90% for the core logic.
- **Adapters:** `errnats.Marshal`/`MarshalQuiet` and `errhttp.Write` unit-tested for status + envelope + (non-)logging; `Reply` paths covered by service integration tests.
- **Per-service migration:** every handler test that asserted `RouteError.Code` (string) moves to `errtest.AssertCode`/`AssertReason` on the decoded reply. The plan enumerates the exact counts (e.g. search ×26, history ×16) so none are missed.
- **TDD throughout** (Red-Green-Refactor), per repo CLAUDE.md.

---

## Migration Overview

Sequenced as a clean DAG (full step-by-step in the plan):

1. **Core (Ch 0–9):** types, `Error`, logctx, constructors/options, `Classify`, `Parse`, `match`, `doc`, reason catalogs, `errnats`, `errhttp`, `errtest`.
2. **natsrouter (Ch 10):** route errors through `errnats`, add the `WithLogValues` seam, convert `Err*`/`RouteError` to shims, update CLAUDE.md's error-handling rule.
3. **Per-service (Ch 11–16):** history, search + mock-user, message-gatekeeper, room-service, room-worker, auth-service — each with reply-path migration, reason assignment, test migration, and `docs/client-api.md` updates.
4. **Cleanup (Ch 17):** delete natsrouter shims, retire `model.ErrorResponse` and the legacy `natsutil` error helpers.
5. **Enforcement + frontend + docs (Ch 18):** semgrep rules, frontend cutover, repo-wide gates, `docs/error-handling.md`.

---

## Files Changed

**New (core + adapters + helper):**
- `pkg/errcode/{category,reason,error,options,classify,parse,match,logctx,doc}.go` + tests
- `pkg/errcode/codes_{room,message,search,auth}.go` + tests
- `pkg/errcode/errnats/{reply.go,reply_test.go}`
- `pkg/errcode/errhttp/{write.go,write_test.go}`
- `pkg/errcode/errtest/{assert.go,assert_test.go}`

**New (lint/docs):**
- `.semgrep/errcode.yml`
- `docs/error-handling.md`

**Modified (foundation):**
- `pkg/natsrouter/{errors.go (shim→delete), register.go, router.go, context.go, middleware.go, params.go}` + tests
- `pkg/model/{event.go (AsyncJobResult code/reason; CreateRoomStatusExists), error.go (ErrorResponse removed)}`
- `pkg/natsutil/reply.go` (legacy error helpers removed)
- `Makefile` (semgrep wiring)
- `CLAUDE.md` (error-handling rule)

**Modified (service migrations):**
- `history-service/*`, `search-service/*` (incl. `metrics.go`, `query_rooms.go`), `mock-user-service/*`
- `message-gatekeeper/*` (incl. `fetcher_history.go`)
- `room-service/*` (incl. `helper.go`, `memberlist_client.go`)
- `room-worker/*`, `auth-service/*`
- `docs/client-api.md`, `chat-frontend/*`

**Deleted (cleanup chapter):**
- `pkg/natsrouter` `RouteError`/`Err*`/`Code*`/`ReplyRouteError`
- `pkg/model.ErrorResponse`
- `pkg/natsutil` `MarshalError`/`MarshalErrorWithCode`/`ReplyError`/`TryParseError`
- room-service `sanitizeError` + allowlist; message-gatekeeper `codedError`/`marshalErrorReply`; room-worker `sanitizeAsyncJobError`/`sanitizeSyncDMError`

---

## Amendment Log (post-implementation decisions)

### A1 — `Code.Valid()` and `New()` panic guards (implemented)

`Code.Valid()` was added to `pkg/errcode/category.go` — it reports whether a value is one of the canonical `Code*` constants (necessary for the `Parse` path to detect non-canonical remote envelopes). `New()` in `options.go` now panics on both a non-canonical `Code` and an empty `Message`; these are programmer errors and fail-fast is preferable to silently producing a broken envelope. `Parse` treats a non-canonical code or empty message in a remote reply as a legacy/non-canonical envelope (see A4 below).

### A2 — `TooManyRequests` constructor and HTTP 429 (implemented)

`CodeTooManyRequests` (`too_many_requests`, HTTP 429) and its named constructor `TooManyRequests(msg, opts...)` were added alongside the other 7 categories, resolving the "open item" in the original spec. The distinction from `unavailable` (503) is preserved: `too_many_requests` is per-caller quota/rate-limiting; `unavailable` is server-wide saturation.

### A3 — Request-ID policy split: StampRequestID vs RequireRequestID (implemented)

Implementation revealed that a uniform "mint-on-missing" policy breaks client-retry deduplication for handlers that derive JetStream `Nats-Msg-Id` keys and canonical message IDs from the inbound request ID. Two policies now coexist (documented in `docs/error-handling.md` §3a):

**Default (mint-on-missing):** `natsutil.StampRequestID(ctx, headers, subject) (ctx, id)` — if the header is absent, mint a fresh UUIDv7 silently; if malformed, mint and emit a single `slog.Warn`. Used by all handlers where the request ID is logging/tracing only.

**Strict (reject-on-missing):** `natsutil.RequireRequestID(ctx, headers, subject) (ctx, id, error)` — returns `errcode.BadRequest` on missing or malformed `X-Request-ID`. Used by:
- All room-service handlers (via the `wrappedCtx(m otelnats.Msg) (context.Context, error)` helper)
- `room-worker.natsServerCreateDM` (sync DM endpoint)

Rationale: room-service fans out to JetStream publishes whose `Nats-Msg-Id` (via `OutboxDedupID`, `CanonicalDedupID`, `messageDedupSeed`) and message IDs (`idgen.MessageIDFromRequestID`) are derived from the request ID. A silently-minted server-side ID across client retries produces a different dedup key each attempt, silently duplicating outbox events and system messages.

The room-worker JetStream consume loop keeps the default mint policy defensively (messages arrive from room-service which already validated the header) but logs `slog.Error` if forced to mint, signalling an upstream contract violation.

**Client contract:** callers targeting room-service or room-worker MUST send a stable `X-Request-ID` header (valid hyphenated UUIDv4 or v7) and reuse it across retries.

### A4 — Cross-site memberlist: propagate X-Request-ID to remote handler (implemented)

`room-service/memberlist_client.go` previously constructed a bare `nats.Msg` with an empty header, so the inbound `X-Request-ID` was never forwarded when making cross-site `member.list` NATS requests. Because remote room-service uses `RequireRequestID` (strict mode per A3), the remote handler rejected with `bad_request`. Fixed by using `natsutil.NewMsg(reqCtx, subject, body)` which copies the `X-Request-ID` from the context into the outbound message header. Integration tests (`TestAddMembers_TwoSiteEndToEnd`, `TestRoomsInfoBatchRPC`) were updated correspondingly.

### A5 — Legacy peer handling in `Parse` / `memberlist_client` (implemented)

`errcode.Parse` returns `(*Error, bool)`. When `memberlist_client` receives a remote envelope with a non-canonical `Code` (old peer) or empty `Message`, it falls back to `errcode.Internal("remote site returned an error")` and emits a single `slog.Warn("legacy peer emitted non-canonical errcode", ...)`. This ensures graceful mixed-version rollout without a hard dependency on the remote being up-to-date.

### A6 — `errRoomKeyAbsent` converted to typed errcode (implemented)

The `errRoomKeyAbsent` sentinel in `room-service/helper.go` (introduced by the room-key-fetch RPC) was converted from `errors.New(...)` to `errcode.NotFound("room key not available")` so it flows through `errnats.Reply` without requiring `sanitizeError`. The room-worker package retains its own `errRoomKeyAbsent = errors.New(...)` sentinel specifically so `errors.Is(err, errRoomKeyAbsent)` can trigger an operational alert path (wrapped via `errcode.Internal(..., errcode.WithCause(errRoomKeyAbsent))`).

### A7 — `history-service` infra errors intentionally use `fmt.Errorf` (clarification)

Several `fmt.Errorf("...: %w", err)` calls in `history-service/internal/service/messages.go` are correct by design. They wrap Cassandra read/write errors (infra tier) and collapse to `internal error` at the handler boundary via `Classify`. Client-visible logic (access window, not-found, forbidden) correctly uses `errcode.*` constructors. This two-tier pattern is the intended usage per CLAUDE.md §3 and `docs/error-handling.md` §2.

### A8 — Worker-side logging boundary (follow-on)

The errcode boundary (`Classify` → one structured log line + leak-safe envelope) covers only client-facing paths (NATS req/rep, Gin HTTP). JetStream worker paths log errors in several ad-hoc shapes. A follow-on `errcode.LogJobError` helper to unify them was designed but **considered and deferred (YAGNI)**: measurement showed the candidate workers are ~100% raw `fmt.Errorf` with no `WithCause`, so the unification would standardize things that don't actually differ. Only the one concrete gap (`message-worker` missing `request_id`) was fixed directly. See `docs/superpowers/specs/2026-06-02-unified-worker-error-logging-design.md` for the design and the deferral rationale; revisit if workers adopt typed errcode errors.
