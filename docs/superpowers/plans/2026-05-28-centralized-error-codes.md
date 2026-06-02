# Centralized Error Codes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Design spec:** `docs/superpowers/specs/2026-05-28-centralized-error-codes-design.md` — read it first for the contract, API surface, and locked decisions. This plan is the step-by-step implementation of that spec.

**Goal:** Replace the four incompatible error-reply patterns across the repo with one transport-neutral `pkg/errcode` package that produces a single wire envelope `{error, code, reason?, metadata?}`, centralizes server-side logging, and makes cause-leakage and code/reason-mixing structurally impossible.

**Architecture:** A core `pkg/errcode` package owns two distinct types — `Code` (closed set of 7 generic codes; the wire `code`) and `Reason` (open set of domain codes; the wire `reason`) — plus the `*Error` type (unexported, non-serializable `cause`), functional-option constructors, a `Classify` boundary that collapses unknown errors to `internal` and logs the full cause chain once, and a `Parse` helper for RPC clients. Two thin transport adapters — `pkg/errcode/errnats` and `pkg/errcode/errhttp` — marshal the envelope for NATS replies and Gin responses. Domain reasons live in `pkg/errcode/codes_<service>.go` as typed `Reason` constants. A `logctx` helper plus a `natsrouter.Context.WithLogValues` seam let handlers attach domain attributes to the logger without ctx-cycle hazards.

**Tech Stack:** Go 1.25, `log/slog`, `github.com/nats-io/nats.go`, `github.com/gin-gonic/gin`, `go.uber.org/mock`, `stretchr/testify`, semgrep (custom local rules).

---

## Design Decisions (locked during brainstorming + spec review)

Do not relitigate these while executing. If a task seems to contradict one, stop and ask.

1. **Wire envelope:** `{"error": "<message>", "code": "<category>", "reason": "<specific>"?, "metadata": {<string→string>}?}`. `error` = human message (existing field name). `code` = one of 7 generic categories, always present. `reason` = optional specific machine code the frontend switches on. `metadata` = optional `map[string]string`.
2. **Two distinct Go types:** `type Code string` (the 7 generics) and `type Reason string` (domain codes). The compiler rejects `New(SomeReason, …)` and `WithReason(SomeCode)`. This is the type-safety guarantee chosen over a single `Code` type.
3. **Generic categories live in core; domain reasons live in `pkg/errcode/codes_<service>.go`** as `Reason` constants (importable across `package main` services, compiler-unique, single catalog).
4. **Infra/DB/third-party errors always collapse to `internal`** with message `"internal error"`, done automatically by `Classify`.
5. **Cause never leaks:** the `cause` field is unexported → `encoding/json` cannot serialize it. Reachable only via `Unwrap()` for logging / `errors.Is`/`As`.
6. **Centralized, level-aware logging:** `Classify` emits exactly one `slog` line (request_id + domain attrs from ctx + code + reason + full cause chain). The level is **category-aware**: `internal`/`unavailable` → ERROR; all expected client errors (`bad_request`/`unauthenticated`/`forbidden`/`not_found`/`conflict`) → INFO. This keeps routine validation failures out of the ERROR stream so error-rate alerting stays meaningful. Handlers do NOT log-then-reply.
7. **One way to format:** named constructors (`errcode.BadRequest(msg, opts...)`) are the entire constructor API; there are **no `*f` variants** (they silently dropped options — a `Conflictf("…%s", id, WithReason(r))` would pass the option as a format arg and lose the reason). For dynamic text use `errcode.BadRequest(fmt.Sprintf(...), opts...)`. Fixed strings never go through `Sprintf`, so literal `%` is safe.
8. **Footgun guard:** `WithCause` panics if the cause already carries an `*errcode.Error`. semgrep flags the literal form at lint time. Invariant: **at most one `*errcode.Error` per chain via single-`%w` propagation.** (Multi-`%w` `fmt.Errorf("%w … %w", ecA, ecB)` can defeat this — `Classify` then picks the first in traversal order; semgrep rule `errcode-no-multi-wrap-errcode` flags it.)
9. **Options carry a trust boundary:** `WithMetadata` is **client-visible** (ships in the envelope); `WithLogValues` is **server-only** (never serialized). Never put server-internal detail in `WithMetadata`, never put client data you wouldn't log in `WithLogValues`.
10. **DM-already-exists is reclassified as a success response** (returns the existing room ID via `model.CreateRoomReply`), not an error.
11. **New generic category `unauthenticated` (HTTP 401)** beyond the original 6 — `auth-service` needs 401-vs-403. **PM gate before Chapter 16.**
12. **Migration uses shims, not mid-plan deletion.** `natsrouter.Err*`/`RouteError` are converted to thin delegating shims in Chapter 10 and deleted only in Chapter 17. The shims keep *production* callers compiling; natsrouter's own tests and ~40 cross-service `.Code`-as-string test assertions are migrated **in the same chapter that introduces the change** (see Ch 10) so each commit is green and bisectable.

### Open items to confirm

- **PM gate (Ch 16):** confirm the `unauthenticated` (401) category. If rejected, `auth-service` token errors fold into `forbidden` (403).
- **`unavailable` HTTP mapping:** currently 503. Admission-control "service busy" is arguably **429**. This plan keeps 503 (the NATS services don't care; only matters if admission surfaces over HTTP). Revisit if/when an HTTP service needs rate-limit semantics.
- **Double-logging is intentional but level-managed:** a failed request emits the `Logging()` middleware access line (`"nats request"`, info) AND the `Classify` line (`"request failed"`). These serve different purposes (access vs error log). With the category-aware level (Decision 6), a routine 4xx produces two INFO-ish lines, not an ERROR — so this does not pollute error alerting. Already-logged transport paths (panic backstop, `replyBusy`) use a **non-logging** marshal (`errnats.MarshalQuiet`) so they don't emit a second redundant `Classify` line; see Ch 8 / Ch 10.

---

## File Structure

**New (core):** `pkg/errcode/{category.go, reason.go, error.go, options.go, classify.go, parse.go, match.go, logctx.go, doc.go, codes_room.go, codes_message.go, codes_search.go, codes_auth.go}` + `*_test.go`.
**New (adapters):** `pkg/errcode/errnats/{reply.go,reply_test.go}`, `pkg/errcode/errhttp/{write.go,write_test.go}`.
**New (test helper):** `pkg/errcode/errtest/{assert.go,assert_test.go}` (decode-and-assert helper for the ~60 migrated test sites).
**New (lint/docs):** `.semgrep/errcode.yml`, `docs/error-handling.md`.

**Modified (foundation):** `pkg/natsrouter/{errors.go (shim then delete), register.go, router.go, context.go, middleware.go}`, `pkg/model/{event.go, error.go}`, `pkg/natsutil/reply.go`, `Makefile`.

**Modified (migrations):** `history-service/*`, `search-service/*` (incl. `metrics.go`), `mock-user-service/*`, `message-gatekeeper/*` (incl. `fetcher_history.go`), `room-service/*` (incl. `memberlist_client.go`), `room-worker/*`, `auth-service/*`, `docs/client-api.md`, `chat-frontend/*`.

---

## Chapter 0 — Core types: `Code` and `Reason`

### Task 0.1: `Code` with `HTTPStatus`

**Files:** Create `pkg/errcode/category.go`; Test `pkg/errcode/category_test.go`.

- [ ] **Step 1: Failing test**

```go
package errcode

import "testing"

func TestCode_HTTPStatus(t *testing.T) {
	cases := map[Code]int{
		CodeBadRequest:      400,
		CodeUnauthenticated: 401,
		CodeForbidden:       403,
		CodeNotFound:        404,
		CodeConflict:        409,
		CodeUnavailable:     503,
		CodeInternal:        500,
		Code("weird"):       500,
	}
	for c, want := range cases {
		if got := c.HTTPStatus(); got != want {
			t.Errorf("%s.HTTPStatus() = %d, want %d", c, got, want)
		}
	}
}
```

- [ ] **Step 2: Run, expect FAIL** — `go test ./pkg/errcode/ -run TestCode -v` → `undefined: CodeBadRequest`.

- [ ] **Step 3: Implement**

```go
// Package errcode is the single source of client-facing error envelopes for
// every transport in the chat system. See doc.go for the wrapping invariant
// and leak guarantee.
package errcode

// Code is the closed set of generic error classifications. It is the wire
// `code` field and drives HTTP status. Only the constants below are valid.
type Code string

const (
	CodeBadRequest      Code = "bad_request"
	CodeUnauthenticated Code = "unauthenticated"
	CodeForbidden       Code = "forbidden"
	CodeNotFound        Code = "not_found"
	CodeConflict        Code = "conflict"
	CodeUnavailable     Code = "unavailable"
	CodeInternal        Code = "internal"
)

// HTTPStatus maps a category to its HTTP status. Unknown values map to 500 so
// a misclassification never leaks as a 2xx.
func (c Code) HTTPStatus() int {
	switch c {
	case CodeBadRequest:
		return 400
	case CodeUnauthenticated:
		return 401
	case CodeForbidden:
		return 403
	case CodeNotFound:
		return 404
	case CodeConflict:
		return 409
	case CodeUnavailable:
		return 503
	default:
		return 500
	}
}
```

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** — `git add pkg/errcode/category.go pkg/errcode/category_test.go && git commit -m "feat(errcode): add Code type with HTTP status mapping"`

### Task 0.2: `Reason` type

**Files:** Create `pkg/errcode/reason.go`; Test `pkg/errcode/reason_test.go`.

- [ ] **Step 1: Failing test**

```go
package errcode

import "testing"

func TestReason_IsString(t *testing.T) {
	var r Reason = "max_room_size_reached"
	if string(r) != "max_room_size_reached" {
		t.Fatal("Reason must be a string-backed type")
	}
}
```

- [ ] **Step 2: Run, expect FAIL** — `undefined: Reason`.
- [ ] **Step 3: Implement**

```go
package errcode

// Reason is an open set of domain-specific machine codes the frontend switches
// on. It is the wire `reason` field. Concrete reasons are declared as typed
// constants in codes_<service>.go. Reason is deliberately distinct from
// Code so the compiler rejects passing one where the other is expected.
type Reason string
```

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** — `git add pkg/errcode/reason.go pkg/errcode/reason_test.go && git commit -m "feat(errcode): add Reason type for domain codes"`

---

## Chapter 1 — Core: `Error` type

### Task 1.1: `Error` struct, `Error()`, `Unwrap()`, `HTTPStatus()`

**Files:** Create `pkg/errcode/error.go`; Test `pkg/errcode/error_test.go`.

- [ ] **Step 1: Failing test**

```go
package errcode

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestError_Error_ReturnsMessageOnly(t *testing.T) {
	e := &Error{Code: CodeBadRequest, Message: "name is required", cause: errors.New("secret db detail")}
	if e.Error() != "name is required" {
		t.Fatalf("Error() = %q, want safe message only", e.Error())
	}
}

func TestError_Unwrap(t *testing.T) {
	root := errors.New("root")
	e := &Error{Code: CodeInternal, Message: "internal error", cause: root}
	if !errors.Is(e, root) {
		t.Fatal("errors.Is should reach the wrapped cause via Unwrap")
	}
}

func TestError_MarshalJSON_NeverLeaksCause(t *testing.T) {
	e := &Error{
		Code:     CodeBadRequest,
		Reason:   "max_room_size_reached",
		Message:  "room is full",
		Metadata: map[string]string{"limit": "500"},
		cause:    errors.New("mongo: connection refused at 10.0.0.5"),
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"bad_request","reason":"max_room_size_reached","error":"room is full","metadata":{"limit":"500"}}`
	if string(b) != want {
		t.Fatalf("marshal = %s, want %s", b, want)
	}
	if strings.Contains(string(b), "mongo") {
		t.Fatal("cause leaked into JSON")
	}
}

func TestError_MarshalJSON_OmitsEmptyOptionalFields(t *testing.T) {
	b, _ := json.Marshal(&Error{Code: CodeNotFound, Message: "not found"})
	if want := `{"code":"not_found","error":"not found"}`; string(b) != want {
		t.Fatalf("marshal = %s, want %s", b, want)
	}
}

func TestError_HTTPStatus(t *testing.T) {
	if (&Error{Code: CodeNotFound}).HTTPStatus() != 404 {
		t.Fatal("HTTPStatus should delegate to Code.HTTPStatus")
	}
}
```

- [ ] **Step 2: Run, expect FAIL** — `undefined: Error`.
- [ ] **Step 3: Implement**

```go
package errcode

// Error is the canonical client-facing error. It marshals to the wire envelope
// {error, code, reason?, metadata?}. cause is UNEXPORTED and therefore cannot
// be serialized by encoding/json — it exists only for server-side logging and
// errors.Is/As traversal. See doc.go.
type Error struct {
	Code     Code          `json:"code"`
	Reason   Reason            `json:"reason,omitempty"`
	Message  string            `json:"error"`
	Metadata map[string]string `json:"metadata,omitempty"`
	cause    error
}

// Error returns ONLY the user-safe message, never the cause.
func (e *Error) Error() string { return e.Message }

// Unwrap exposes the wrapped cause for errors.Is/As and logging. JSON
// marshalling does not call Unwrap, so this does not leak the cause to clients.
func (e *Error) Unwrap() error { return e.cause }

// HTTPStatus returns the HTTP status for this error's category.
func (e *Error) HTTPStatus() int { return e.Code.HTTPStatus() }
```

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** — `git add pkg/errcode/error.go pkg/errcode/error_test.go && git commit -m "feat(errcode): add Error type with unexported, non-serializable cause"`

---

## Chapter 2 — Core: `logctx`

### Task 2.1: logger-in-context helpers

**Files:** Create `pkg/errcode/logctx.go`; Test `pkg/errcode/logctx_test.go`.

- [ ] **Step 1: Failing test**

```go
package errcode

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestWithLogValues_AccumulatesAttrs(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)))
	ctx = WithLogValues(ctx, "account", "alice")
	ctx = WithLogValues(ctx, "roomID", "r1")
	loggerFrom(ctx).Info("hello")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["account"] != "alice" || line["roomID"] != "r1" {
		t.Fatalf("attrs not accumulated: %v", line)
	}
}

func TestLoggerFrom_DefaultsWhenAbsent(t *testing.T) {
	if loggerFrom(context.Background()) == nil {
		t.Fatal("loggerFrom must never return nil")
	}
}
```

- [ ] **Step 2: Run, expect FAIL** — `undefined: WithLogger`.
- [ ] **Step 3: Implement**

```go
package errcode

import (
	"context"
	"log/slog"
)

type loggerCtxKey struct{}

// WithLogger stores an explicit *slog.Logger in ctx (mainly for tests).
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey{}, l)
}

// WithLogValues returns ctx carrying a logger enriched with the given key/value
// pairs. Call once at the top of a handler to attach domain context; the
// centralized Classify log line then includes them.
func WithLogValues(ctx context.Context, args ...any) context.Context {
	return WithLogger(ctx, loggerFrom(ctx).With(args...))
}

// loggerFrom returns the ctx logger, or slog.Default() if none was set.
func loggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerCtxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
```

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** — `git add pkg/errcode/logctx.go pkg/errcode/logctx_test.go && git commit -m "feat(errcode): add logger-in-context helpers"`

---

## Chapter 3 — Core: options and constructors

### Task 3.1: `Option`, `New`, named constructors, `WithReason`/`WithMetadata`/`WithCause`

**Files:** Create `pkg/errcode/options.go`; Test `pkg/errcode/options_test.go`.

Constructor convention (resolves the review's naming inconsistency AND the `*f`-drops-options footgun): **named constructors are the entire API** — `errcode.BadRequest(msg, opts...)`, one per category. There are **no `*f` variants**: a `Conflictf("room %s full", id, WithReason(r))` would pass the `Option` as a `Sprintf` arg and silently lose the reason, so they are omitted. For dynamic text, call `errcode.BadRequest(fmt.Sprintf("…", x), opts...)` — the message is computed by the caller and options stay first-class. Fixed strings never touch `Sprintf`, so literal `%` is safe. `New(code, msg, opts...)` is the dynamic-category escape hatch.

- [ ] **Step 1: Failing test**

```go
package errcode

import (
	"errors"
	"fmt"
	"testing"
)

func TestNamedConstructors(t *testing.T) {
	if e := BadRequest("name is required"); e.Code != CodeBadRequest || e.Message != "name is required" {
		t.Fatalf("BadRequest: %+v", e)
	}
	if e := NotFound("gone"); e.Code != CodeNotFound {
		t.Fatal("NotFound")
	}
	for _, e := range []*Error{
		Unauthenticated("x"), Forbidden("x"), Conflict("x"), Unavailable("x"), Internal("x"),
	} {
		if e.Message != "x" {
			t.Fatalf("constructor message: %+v", e)
		}
	}
}

func TestConstructorDoesNotFormat_LiteralPercentIsSafe(t *testing.T) {
	if got := BadRequest("100% full").Message; got != "100% full" {
		t.Fatalf("constructor must not format: %q", got)
	}
}

func TestFormattingPlusOptionUsesSprintfAtCallSite(t *testing.T) {
	// The supported pattern for dynamic text + a reason: caller formats, options stay first-class.
	e := Conflict(fmt.Sprintf("room %s is full", "r1"), WithReason("max_room_size_reached"))
	if e.Message != "room r1 is full" || e.Reason != "max_room_size_reached" {
		t.Fatalf("got %+v", e)
	}
}

func TestWithReason(t *testing.T) {
	e := BadRequest("room full", WithReason("max_room_size_reached"))
	if e.Reason != "max_room_size_reached" {
		t.Fatalf("reason = %q", e.Reason)
	}
}

func TestWithMetadata_Pairs(t *testing.T) {
	e := Conflict("dm exists", WithMetadata("roomId", "r1", "kind", "dm"))
	if e.Metadata["roomId"] != "r1" || e.Metadata["kind"] != "dm" {
		t.Fatalf("meta = %v", e.Metadata)
	}
}

func TestWithMetadata_OddArgsPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("odd WithMetadata args must panic")
		}
	}()
	BadRequest("x", WithMetadata("lonely"))
}

func TestWithCause_RawError(t *testing.T) {
	root := errors.New("mongo down")
	if e := Internal("internal error", WithCause(root)); !errors.Is(e, root) {
		t.Fatal("cause not attached")
	}
}

func TestWithCause_PanicsOnNestedErrcode(t *testing.T) {
	inner := NotFound("room not found")
	defer func() {
		if recover() == nil {
			t.Fatal("WithCause(errcode.Error) must panic — invariant: one *Error per chain")
		}
	}()
	Internal("x", WithCause(inner))
}

func TestWithCause_PanicsOnWrappedNestedErrcode(t *testing.T) {
	inner := NotFound("room not found")
	wrapped := fmt.Errorf("ctx: %w", inner)
	defer func() {
		if recover() == nil {
			t.Fatal("WithCause must detect *Error even when wrapped")
		}
	}()
	Internal("x", WithCause(wrapped))
}
```

- [ ] **Step 2: Run, expect FAIL** — `undefined: BadRequest`.
- [ ] **Step 3: Implement**

```go
package errcode

import "errors"

// Option configures an *Error during construction.
type Option func(*Error)

// New builds an *Error with a generic category and message, applying options.
// Prefer the named constructors below; use New only for a dynamically chosen
// category.
func New(code Code, message string, opts ...Option) *Error {
	e := &Error{Code: code, Message: message}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Named constructors are the entire constructor API: one per category, each
// taking a fixed message and options. They never format — for dynamic text the
// caller passes fmt.Sprintf(...) as msg, keeping options first-class. There are
// deliberately no *f variants (they would swallow trailing Option args).
func BadRequest(msg string, opts ...Option) *Error      { return New(CodeBadRequest, msg, opts...) }
func Unauthenticated(msg string, opts ...Option) *Error { return New(CodeUnauthenticated, msg, opts...) }
func Forbidden(msg string, opts ...Option) *Error       { return New(CodeForbidden, msg, opts...) }
func NotFound(msg string, opts ...Option) *Error        { return New(CodeNotFound, msg, opts...) }
func Conflict(msg string, opts ...Option) *Error        { return New(CodeConflict, msg, opts...) }
func Unavailable(msg string, opts ...Option) *Error     { return New(CodeUnavailable, msg, opts...) }
func Internal(msg string, opts ...Option) *Error        { return New(CodeInternal, msg, opts...) }

// WithReason attaches the specific machine code the frontend switches on.
// Accepts only Reason — the compiler rejects a Code here.
func WithReason(r Reason) Option { return func(e *Error) { e.Reason = r } }

// WithMetadata attaches CLIENT-VISIBLE string key/value metadata (it ships in
// the wire envelope). Never put server-internal detail here — use WithLogValues
// for that. Args must be even; an odd count is a programmer error and panics.
func WithMetadata(kv ...string) Option {
	return func(e *Error) {
		if len(kv)%2 != 0 {
			panic("errcode: WithMetadata requires an even number of args (key/value pairs)")
		}
		if e.Metadata == nil {
			e.Metadata = make(map[string]string, len(kv)/2)
		}
		for i := 0; i < len(kv); i += 2 {
			e.Metadata[kv[i]] = kv[i+1]
		}
	}
}

// WithCause attaches an underlying error for server-side logging. The cause is
// NEVER serialized. It PANICS if the cause already carries an *errcode.Error,
// preserving the "at most one *Error per chain" invariant. Pass only raw
// infra/third-party errors. See doc.go.
func WithCause(err error) Option {
	return func(e *Error) {
		var nested *Error
		if errors.As(err, &nested) {
			panic("errcode: WithCause must not wrap another *errcode.Error; " +
				`propagate it with "return err" or fmt.Errorf("...: %w", err) instead`)
		}
		e.cause = err
	}
}
```

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** — `git add pkg/errcode/options.go pkg/errcode/options_test.go && git commit -m "feat(errcode): add constructors and options with WithCause panic guard"`

---

## Chapter 4 — Core: `Classify`

### Task 4.1: boundary classifier + centralized log

**Files:** Create `pkg/errcode/classify.go`; Test `pkg/errcode/classify_test.go`.

- [ ] **Step 1: Failing test**

```go
package errcode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func newCapture() (context.Context, *bytes.Buffer) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return WithLogger(context.Background(), l), &buf
}

func TestClassify_NilReturnsNil(t *testing.T) {
	ctx, _ := newCapture()
	if Classify(ctx, nil) != nil {
		t.Fatal("nil → nil")
	}
}

func TestClassify_UnknownBecomesInternalAndLogsCause(t *testing.T) {
	ctx, buf := newCapture()
	raw := fmt.Errorf("load room: %w", errors.New("mongo: connection refused 10.0.0.5"))
	e := Classify(ctx, raw)
	if e.Code != CodeInternal || e.Message != "internal error" {
		t.Fatalf("got %+v", e)
	}
	if !strings.Contains(buf.String(), "mongo: connection refused") {
		t.Fatalf("cause not logged: %s", buf.String())
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "mongo") {
		t.Fatalf("cause leaked into reply: %s", b)
	}
}

func TestClassify_TypedErrorPreservedThroughWrapping(t *testing.T) {
	ctx, _ := newCapture()
	typed := NotFound("room not found", WithReason("room_not_found"))
	e := Classify(ctx, fmt.Errorf("checking room: %w", typed))
	if e.Code != CodeNotFound || e.Reason != "room_not_found" {
		t.Fatalf("typed lost: %+v", e)
	}
}

func TestClassify_LogsCtxValues(t *testing.T) {
	ctx, buf := newCapture()
	ctx = WithLogValues(ctx, "request_id", "req-123", "account", "alice")
	Classify(ctx, errors.New("boom"))
	if l := buf.String(); !strings.Contains(l, "req-123") || !strings.Contains(l, "alice") {
		t.Fatalf("ctx values missing: %s", l)
	}
}

func TestClassify_LevelIsCategoryAware(t *testing.T) {
	level := func(err error) string {
		ctx, buf := newCapture()
		Classify(ctx, err)
		var line map[string]any
		_ = json.Unmarshal(buf.Bytes(), &line)
		return line["level"].(string)
	}
	// Expected client errors must NOT log at ERROR (would pollute alerting).
	if got := level(BadRequest("name is required")); got != "INFO" {
		t.Fatalf("4xx level = %s, want INFO", got)
	}
	if got := level(NotFound("gone")); got != "INFO" {
		t.Fatalf("not_found level = %s, want INFO", got)
	}
	// Server/infra errors log at ERROR.
	if got := level(errors.New("mongo down")); got != "ERROR" {
		t.Fatalf("internal level = %s, want ERROR", got)
	}
	if got := level(Unavailable("service busy")); got != "ERROR" {
		t.Fatalf("unavailable level = %s, want ERROR", got)
	}
}
```

- [ ] **Step 2: Run, expect FAIL** — `undefined: Classify`.
- [ ] **Step 3: Implement**

```go
package errcode

import (
	"context"
	"errors"
	"log/slog"
)

// Classify converts any error into a client-safe *Error and logs it exactly
// once on the server side. It is the single boundary every transport adapter
// calls before replying.
//
//   - nil → nil.
//   - *errcode.Error in the chain (via errors.As) → that error.
//   - anything else → Internal "internal error", original chain as cause.
//
// The log line carries request_id and domain attrs stashed via WithLogValues,
// plus the full cause chain, and its LEVEL is category-aware: server faults
// (internal/unavailable) at ERROR, expected client errors at INFO — so routine
// 4xx validation failures don't pollute the ERROR stream / break alerting.
// The cause is never part of the returned *Error's serialized form. The cause
// chain is logged via err.Error(); callers MUST NOT wrap raw message bodies or
// tokens into a cause (see doc.go logging contract).
func Classify(ctx context.Context, err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Code: CodeInternal, Message: "internal error", cause: err}
	}
	loggerFrom(ctx).Log(ctx, e.logLevel(), "request failed",
		"code", string(e.Code),
		"reason", string(e.Reason),
		"cause", err.Error(),
	)
	return e
}

// logLevel maps a category to a server-log level: server faults are ERROR,
// expected client errors are INFO.
func (e *Error) logLevel() slog.Level {
	switch e.Code {
	case CodeInternal, CodeUnavailable:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
```

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** — `git add pkg/errcode/classify.go pkg/errcode/classify_test.go && git commit -m "feat(errcode): add Classify boundary with centralized logging"`

---

## Chapter 5 — Core: `Parse` (for RPC clients)

Needed by `message-gatekeeper/fetcher_history.go` and `room-service/memberlist_client.go`, which decode *remote* error replies. Replaces `natsutil.TryParseError`.

### Task 5.1: `Parse`

**Files:** Create `pkg/errcode/parse.go`; Test `pkg/errcode/parse_test.go`.

- [ ] **Step 1: Failing test**

```go
package errcode

import "testing"

func TestParse_ErrorEnvelope(t *testing.T) {
	e, ok := Parse([]byte(`{"code":"forbidden","reason":"not_room_member","error":"only room members can list members"}`))
	if !ok || e.Code != CodeForbidden || e.Reason != "not_room_member" {
		t.Fatalf("parse failed: %+v ok=%v", e, ok)
	}
}

func TestParse_NonErrorJSON(t *testing.T) {
	if _, ok := Parse([]byte(`{"roomId":"r1","status":"accepted"}`)); ok {
		t.Fatal("payload without non-empty error must not parse as error")
	}
}

func TestParse_Malformed(t *testing.T) {
	if _, ok := Parse([]byte(`not json`)); ok {
		t.Fatal("malformed must not parse")
	}
}
```

- [ ] **Step 2: Run, expect FAIL** — `undefined: Parse`.
- [ ] **Step 3: Implement**

```go
package errcode

import "encoding/json"

// Parse decodes a reply payload into an *Error iff it is an error envelope
// (non-empty "error" field). Used by RPC clients to detect remote failures and
// branch on code/reason. Returns (nil, false) for success payloads or garbage.
func Parse(data []byte) (*Error, bool) {
	var e Error
	if err := json.Unmarshal(data, &e); err != nil || e.Message == "" {
		return nil, false
	}
	return &e, true
}
```

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** — `git add pkg/errcode/parse.go pkg/errcode/parse_test.go && git commit -m "feat(errcode): add Parse for RPC-client error detection"`

### Task 5.2: in-process reason matching (`match.go`)

In-process callers (e.g. `room-service/memberlist_client.go` Ch 14.2) need to branch on the reason of an error without hand-rolling `errors.As`. Provide one helper each for the value and the boolean test.

**Files:** Create `pkg/errcode/match.go`, `match_test.go`.

- [ ] **Step 1: Failing test**
```go
package errcode

import (
	"errors"
	"fmt"
	"testing"
)

func TestReasonOf(t *testing.T) {
	err := fmt.Errorf("ctx: %w", NotFound("x", WithReason(RoomNotMember)))
	if ReasonOf(err) != RoomNotMember {
		t.Fatalf("ReasonOf = %q", ReasonOf(err))
	}
	if ReasonOf(errors.New("plain")) != "" {
		t.Fatal("non-errcode error must yield empty reason")
	}
}

func TestHasReason(t *testing.T) {
	if !HasReason(NotFound("x", WithReason(RoomNotMember)), RoomNotMember) {
		t.Fatal("HasReason should match")
	}
	if HasReason(NotFound("x"), RoomNotMember) {
		t.Fatal("HasReason must not match an absent reason")
	}
}
```

- [ ] **Step 2: Run, expect FAIL** — `undefined: ReasonOf`.
- [ ] **Step 3: Implement**
```go
package errcode

import "errors"

// ReasonOf returns the Reason of the first *Error in err's chain, or "" if
// there is none.
func ReasonOf(err error) Reason {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ""
}

// HasReason reports whether err's chain carries an *Error with reason r.
func HasReason(err error, r Reason) bool { return ReasonOf(err) == r }
```

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** — `git add pkg/errcode/match.go pkg/errcode/match_test.go && git commit -m "feat(errcode): add ReasonOf/HasReason matchers"`

### Task 5.3: test helper (`errtest`)

~60 test sites across services migrate from `RouteError.Code == "..."` to decoding the reply envelope. A tiny shared helper avoids hand-rolling JSON-decode-and-assert in each, and keeps the migration chapters mechanical.

**Files:** Create `pkg/errcode/errtest/assert.go`, `assert_test.go`.

- [ ] **Step 1: Failing test**
```go
package errtest

import (
	"encoding/json"
	"testing"

	"github.com/hmchangw/chat/pkg/errcode"
)

func TestAssertEnvelope(t *testing.T) {
	data, _ := json.Marshal(errcode.NotFound("room not found", errcode.WithReason(errcode.RoomNotMember)))
	AssertCode(t, data, errcode.CodeNotFound)
	AssertReason(t, data, errcode.RoomNotMember)
}
```

- [ ] **Step 2: Run, expect FAIL** — `undefined: AssertCode`.
- [ ] **Step 3: Implement** (helpers call `t.Helper()`; live in a non-`_test.go` file so other packages' tests can import them — allowed by CLAUDE.md "shared test utilities used by multiple packages may live in a dedicated package", and this package is import-only-by-tests):
```go
// Package errtest provides assertions for errcode wire envelopes in tests.
package errtest

import (
	"testing"

	"github.com/hmchangw/chat/pkg/errcode"
)

// Decode parses an error envelope from a reply payload, failing the test if it
// is not one.
func Decode(t *testing.T, data []byte) *errcode.Error {
	t.Helper()
	e, ok := errcode.Parse(data)
	if !ok {
		t.Fatalf("payload is not an error envelope: %s", data)
	}
	return e
}

// AssertCode fails unless data is an error envelope with the given code.
func AssertCode(t *testing.T, data []byte, want errcode.Code) {
	t.Helper()
	if got := Decode(t, data).Code; got != want {
		t.Fatalf("code = %q, want %q (payload %s)", got, want, data)
	}
}

// AssertReason fails unless data is an error envelope with the given reason.
func AssertReason(t *testing.T, data []byte, want errcode.Reason) {
	t.Helper()
	if got := Decode(t, data).Reason; got != want {
		t.Fatalf("reason = %q, want %q (payload %s)", got, want, data)
	}
}
```

- [ ] **Step 4: Run, expect PASS.** Migration chapters (11–16) SHOULD use `errtest.AssertCode`/`AssertReason` instead of bespoke decoding.
- [ ] **Step 5: Commit** — `git add pkg/errcode/errtest/ && git commit -m "feat(errcode/errtest): add envelope assertion helpers for tests"`

---

## Chapter 6 — Core: `doc.go`

### Task 6.1: package documentation

**Files:** Create `pkg/errcode/doc.go`.

- [ ] **Step 1: Write the doc**

```go
// Package errcode is the single source of client-facing error envelopes for
// every transport (NATS request/reply, JetStream replies, Gin HTTP).
//
// # Wire envelope
//
//	{"error":"<message>","code":"<category>","reason":"<specific>"?,"metadata":{…}?}
//
//   - error    — human-readable, user-safe message.
//   - code     — one Code (bad_request, unauthenticated, forbidden,
//     not_found, conflict, unavailable, internal). Always present.
//   - reason   — optional Reason (domain code, e.g. "max_room_size_reached"),
//     declared in codes_<service>.go. Frontend logic: trigger = reason ?? code.
//   - metadata — optional map[string]string for structured detail.
//
// # Two types, by design
//
// Code (the 7 generics) and Reason (open domain set) are distinct types so
// the compiler rejects New(SomeReason, …) and WithReason(SomeCode).
//
// # Leak guarantee
//
// Error.cause is unexported; encoding/json cannot serialize it. The cause is
// reachable only server-side via Unwrap()/errors.Is/As and is logged exactly
// once by Classify.
//
// # Wrapping invariant: at most one *errcode.Error per chain
//
// Allowed:
//
//	return errcode.BadRequest("name is required")
//	return errcode.NotFound("x", errcode.WithReason(RoomNotMember))
//	return errcode.Internal("x", errcode.WithCause(rawDBErr)) // RAW cause only
//	return fmt.Errorf("checking room: %w", typedErr)          // typed survives
//	return typedErr
//
// Forbidden (WithCause panics; semgrep-flagged):
//
//	return errcode.Internal("x", errcode.WithCause(anotherErrcodeErr))
//
// Also forbidden (defeats the invariant; semgrep-flagged): putting two errcode
// errors in one chain via multi-verb fmt.Errorf —
//
//	return fmt.Errorf("%w and %w", errcodeA, errcodeB) // Classify picks the first
//
// Propagate with a single %w only.
//
// # Logging
//
// Classify logs each error exactly once, at a category-aware level (server
// faults ERROR, expected client errors INFO). Handlers must NOT log-then-reply.
//
// Attach domain context once at handler entry, choosing by handler style:
//   - natsrouter handler (has *Context):  c.WithLogValues("account", a)
//   - Gin / raw NATS (has context.Context): ctx = errcode.WithLogValues(ctx, …)
//
// Never call the package func errcode.WithLogValues with a *natsrouter.Context
// as parent — use the method, which derives from the inner ctx and avoids the
// Value-delegation cycle.
//
// Trust boundary: WithLogValues attributes are SERVER-ONLY (never serialized);
// WithMetadata is CLIENT-VISIBLE (ships in the envelope). A cause attached via
// WithCause is logged through err.Error() — never wrap raw message bodies,
// tokens, or secrets into a cause, or the central log becomes a leak vector.
package errcode
```

- [ ] **Step 2: Run** `go vet ./pkg/errcode/` → clean.
- [ ] **Step 3: Commit** — `git add pkg/errcode/doc.go && git commit -m "docs(errcode): document envelope, type split, leak guarantee, invariant"`

---

## Chapter 7 — Domain reason catalogs

### Task 7.1: per-service `Reason` catalogs

**Files:** Create `pkg/errcode/codes_room.go`, `codes_message.go`, `codes_search.go`, `codes_auth.go`; Test `pkg/errcode/codes_test.go`.

- [ ] **Step 1: Failing test (uniqueness + snake_case)**

```go
package errcode

import (
	"regexp"
	"testing"
)

var allReasons = []Reason{
	RoomMaxSizeReached, RoomDMAlreadyExists, RoomNotMember, RoomNotOwner,
	RoomLastOwnerCannotLeave, RoomBotInChannel, RoomBotNotAvailable,
	MessageLargeRoomPostRestricted, MessageNotSubscribed,
	AuthTokenExpired, AuthInvalidToken,
}

func TestReasons_SnakeCase(t *testing.T) {
	re := regexp.MustCompile(`^[a-z][a-z0-9_]*[a-z0-9]$`)
	for _, r := range allReasons {
		if !re.MatchString(string(r)) {
			t.Errorf("reason %q is not flat snake_case", r)
		}
	}
}

func TestReasons_Unique(t *testing.T) {
	seen := map[Reason]bool{}
	for _, r := range allReasons {
		if seen[r] {
			t.Errorf("duplicate reason: %q", r)
		}
		seen[r] = true
	}
}
```

- [ ] **Step 2: Run, expect FAIL** — `undefined: RoomMaxSizeReached`.
- [ ] **Step 3: Create catalogs**

`pkg/errcode/codes_room.go`:
```go
package errcode

// Reasons emitted by room-service and room-worker.
const (
	RoomMaxSizeReached       Reason = "max_room_size_reached"
	RoomDMAlreadyExists      Reason = "dm_already_exists"
	RoomNotMember            Reason = "not_room_member"
	RoomNotOwner             Reason = "not_room_owner"
	RoomLastOwnerCannotLeave Reason = "last_owner_cannot_leave"
	RoomBotInChannel         Reason = "bot_in_channel"
	RoomBotNotAvailable      Reason = "bot_not_available"
)
```

`pkg/errcode/codes_message.go`:
```go
package errcode

// Reasons emitted by message-gatekeeper.
const (
	MessageLargeRoomPostRestricted Reason = "large_room_post_restricted"
	MessageNotSubscribed           Reason = "not_subscribed"
)
```

`pkg/errcode/codes_search.go`:
```go
package errcode

// Reasons emitted by search-service. None require frontend branching today;
// this file is the per-service home for future search reasons.
```

`pkg/errcode/codes_auth.go`:
```go
package errcode

// Reasons emitted by auth-service.
const (
	AuthTokenExpired Reason = "sso_token_expired"
	AuthInvalidToken Reason = "invalid_sso_token"
)
```

- [ ] **Step 4: Run** `go test ./pkg/errcode/ -run TestReasons -v` → PASS.
- [ ] **Step 5: Full package gate** — `make test SERVICE=pkg/errcode && go vet ./pkg/errcode/...` → PASS.
- [ ] **Step 6: Commit** — `git add pkg/errcode/codes_*.go pkg/errcode/codes_test.go && git commit -m "feat(errcode): add per-service Reason catalogs"`

---

## Chapter 8 — Adapter: `errnats`

### Task 8.1: `errnats.Marshal` and `errnats.Reply`

**Files:** Create `pkg/errcode/errnats/reply.go`, `reply_test.go`.

- [ ] **Step 1: Failing test**

```go
package errnats

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/hmchangw/chat/pkg/errcode"
)

func ctxQuiet() context.Context {
	return errcode.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
}

func TestMarshal_TypedError(t *testing.T) {
	data := Marshal(ctxQuiet(), errcode.NotFound("room not found", errcode.WithReason(errcode.RoomNotMember)))
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if got["code"] != "not_found" || got["reason"] != "not_room_member" || got["error"] != "room not found" {
		t.Fatalf("envelope = %v", got)
	}
}

func TestMarshal_UnknownCollapsesToInternal(t *testing.T) {
	data := Marshal(ctxQuiet(), errors.New("mongo down"))
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if got["code"] != "internal" || got["error"] != "internal error" {
		t.Fatalf("envelope = %v", got)
	}
	if _, leaked := got["reason"]; leaked {
		t.Fatal("reason should be absent")
	}
}

func TestMarshalQuiet_DoesNotLogButStillCollapses(t *testing.T) {
	var buf bytes.Buffer
	// Default logger must not receive a line from MarshalQuiet.
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)

	data := MarshalQuiet(errors.New("mongo down"))
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if got["code"] != "internal" || got["error"] != "internal error" {
		t.Fatalf("envelope = %v", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("MarshalQuiet must not log; got %s", buf.String())
	}
}
```

(`Reply`/`ReplyQuiet` call `msg.Respond`; covered by service integration tests. Unit-test `Marshal`/`MarshalQuiet`, which hold the logic.)

- [ ] **Step 2: Run, expect FAIL** — `undefined: Marshal`.
- [ ] **Step 3: Implement**

```go
// Package errnats adapts errcode.Error to NATS request/reply responses.
package errnats

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/errcode"
)

const fallback = `{"code":"internal","error":"internal error"}`

// Marshal classifies err (logging it once) and returns the JSON envelope.
func Marshal(ctx context.Context, err error) []byte {
	data, mErr := json.Marshal(errcode.Classify(ctx, err))
	if mErr != nil {
		return []byte(fallback)
	}
	return data
}

// MarshalQuiet returns the envelope WITHOUT logging. Use only on paths that have
// already logged the failure (panic backstop, admission/replyBusy) to avoid a
// redundant second log line. Unknown errors still collapse to internal and the
// cause is never serialized.
func MarshalQuiet(err error) []byte {
	var e *errcode.Error
	if !errors.As(err, &e) {
		e = errcode.Internal("internal error")
	}
	data, mErr := json.Marshal(e)
	if mErr != nil {
		return []byte(fallback)
	}
	return data
}

// Reply classifies err (logging once) and sends the envelope on msg's reply subject.
func Reply(ctx context.Context, msg *nats.Msg, err error) {
	if rErr := msg.Respond(Marshal(ctx, err)); rErr != nil {
		slog.ErrorContext(ctx, "error reply failed", "error", rErr, "subject", msg.Subject)
	}
}

// ReplyQuiet sends the envelope WITHOUT logging the failure (see MarshalQuiet).
func ReplyQuiet(msg *nats.Msg, err error) {
	if rErr := msg.Respond(MarshalQuiet(err)); rErr != nil {
		slog.Error("error reply failed", "error", rErr, "subject", msg.Subject)
	}
}
```

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** — `git add pkg/errcode/errnats/ && git commit -m "feat(errcode/errnats): add NATS reply adapter"`

---

## Chapter 9 — Adapter: `errhttp`

### Task 9.1: `errhttp.Write` for Gin

**Files:** Create `pkg/errcode/errhttp/write.go`, `write_test.go`.

- [ ] **Step 1: Failing test**

```go
package errhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/errcode"
)

func TestWrite_StatusAndEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth", nil)
	Write(c.Request.Context(), c, errcode.Unauthenticated("token expired", errcode.WithReason(errcode.AuthTokenExpired)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "unauthenticated" || got["reason"] != "sso_token_expired" {
		t.Fatalf("envelope = %v", got)
	}
}

func TestWrite_UnknownIs500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	Write(c.Request.Context(), c, errors.New("db exploded"))
	if w.Code != http.StatusInternalServerError || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 2: Run, expect FAIL** — `undefined: Write`.
- [ ] **Step 3: Implement**

```go
// Package errhttp adapts errcode.Error to Gin HTTP responses.
package errhttp

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/errcode"
)

// Write classifies err (logging it once) and writes the envelope with the
// category's HTTP status.
func Write(ctx context.Context, c *gin.Context, err error) {
	e := errcode.Classify(ctx, err)
	c.JSON(e.HTTPStatus(), e)
}
```

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** — `git add pkg/errcode/errhttp/ && git commit -m "feat(errcode/errhttp): add Gin response adapter"`

---

## Chapter 10 — natsrouter integration (shims, not deletion)

This wires natsrouter to `errcode`/`errnats`, adds the logging seam, and converts `RouteError`/`Err*` to **thin shims** so all existing callers keep compiling until Chapter 17. After this, `pkg/natsrouter` depends on `errcode` + `errnats` (never Gin).

### Task 10.1: Add `Context.WithLogValues` seam (fixes the ctx-cycle bug)

**Files:** Modify `pkg/natsrouter/context.go`; Test `pkg/natsrouter/context_test.go`.

- [ ] **Step 1: Failing test**

```go
func TestContext_WithLogValues_NoCycleAndEnriches(t *testing.T) {
	var buf bytes.Buffer
	c := NewContext(map[string]string{})
	c.SetContext(errcode.WithLogger(c.ctx, slog.New(slog.NewJSONHandler(&buf, nil))))

	c.WithLogValues("account", "alice") // must not hang (no ctx cycle)

	// A value lookup must terminate (would loop forever on a cycle):
	_ = c.Value("anything")

	errcode.Classify(c, errors.New("boom"))
	if !strings.Contains(buf.String(), "alice") {
		t.Fatalf("log values not applied: %s", buf.String())
	}
}
```

- [ ] **Step 2: Run, expect FAIL** — `c.WithLogValues undefined`.
- [ ] **Step 3: Implement the seam**

Add to `pkg/natsrouter/context.go`:
```go
// WithLogValues enriches the context logger with key/value pairs for the
// centralized errcode log line (account, roomID, …). It derives from c.ctx
// (the unexported underlying context), never from c itself, avoiding the
// Value-delegation cycle documented on SetContext. Call once at handler entry.
func (c *Context) WithLogValues(args ...any) {
	c.SetContext(errcode.WithLogValues(c.ctx, args...))
}
```
Add import `"github.com/hmchangw/chat/pkg/errcode"`.

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** — `git add pkg/natsrouter/context.go pkg/natsrouter/context_test.go && git commit -m "feat(natsrouter): add Context.WithLogValues seam (cycle-safe)"`

### Task 10.2: Route handler errors through `errnats`; convert `RouteError`/`Err*` to shims

**Files:** Modify `pkg/natsrouter/register.go`, `errors.go`, `context.go`, `router.go`, `middleware.go`, `params.go`; AND the package's own tests `errors_test.go`, `router_test.go`, `example_test.go`, `integration_test.go` (they assert on `.Code` and must be migrated in THIS task — see Step 1).

> **Why the tests change here, not later:** `type RouteError = errcode.Error` makes `RouteError.Code` an `errcode.Code`, not a `string`. natsrouter's own tests compare it to string literals (`assert.Equal(t, "not_found", result.Code)`); with testify those *compile* but **fail at runtime** (`errcode.Code("not_found") != "not_found"`). And `TestRouteError_Error` asserts the old `"not_found: room not found"` format, but `errcode.Error.Error()` returns message-only. So these tests MUST move with the shim, in this commit.

- [ ] **Step 1: Update/add tests (use `errtest` helpers from Task 5.3)**

  - `register_test.go`: a handler returning `errcode.NotFound("x", errcode.WithReason(errcode.RoomNotMember))` replies `{code:"not_found",reason:"not_room_member"}` (`errtest.AssertCode`/`AssertReason`); an unknown error replies `{code:"internal"}`; the deserialize-failure path (`register.go:20`, see Step 3) replies `{code:"bad_request"}`.
  - `router_test.go`: admission-saturation test asserts `code=="unavailable"`; panic-backstop test asserts `code=="internal"`. **Delete/rewrite `TestRouteError_Error`** — the `code: message` string format no longer exists; if a message-only check is still wanted, assert `err.Error()=="room not found"`. Rewrite the three `result.Code == "..."` string assertions (router_test.go:257,276,361) to `errtest.AssertCode` on the reply bytes, or compare to `errcode.Code*`.
  - `errors_test.go`: rewrite the two `err.Code == "..."`/`CodeUnavailable` assertions to compare against `errcode.Code*` (the shim consts are now typed via `string(errcode.Code*)`).
  - `example_test.go` (line ~93) and `integration_test.go` (line ~247): rewrite `.Code == "..."` runtime comparisons to `errtest.AssertCode`.

- [ ] **Step 2: Run, expect FAIL.**

- [ ] **Step 3: Apply changes**

`register.go` — replace `replyErr`:
```go
func replyErr(c *Context, err error) {
	errnats.Reply(c, c.Msg, err)
}
```
Drop now-unused `errors`/`slog` imports only if `RegisterVoid` no longer needs them (it still uses `slog` — keep). Add `errnats` import.

`errors.go` — convert `RouteError` and constructors to shims over `errcode` (do NOT delete yet):
```go
package natsrouter

import (
	"fmt"

	"github.com/hmchangw/chat/pkg/errcode"
)

// Deprecated: use pkg/errcode directly. Retained as a shim during migration;
// removed in the error-codes cleanup chapter.
type RouteError = errcode.Error

func Err(message string) *RouteError  { return errcode.BadRequest(message) }
func Errf(f string, a ...any) *RouteError { return errcode.BadRequest(fmt.Sprintf(f, a...)) }
func ErrWithCode(code, message string) *RouteError { return errcode.New(errcode.Code(code), message) }

const (
	CodeBadRequest  = string(errcode.CodeBadRequest)
	CodeNotFound    = string(errcode.CodeNotFound)
	CodeForbidden   = string(errcode.CodeForbidden)
	CodeConflict    = string(errcode.CodeConflict)
	CodeInternal    = string(errcode.CodeInternal)
	CodeUnavailable = string(errcode.CodeUnavailable)
)

func ErrBadRequest(m string) *RouteError  { return errcode.BadRequest(m) }
func ErrNotFound(m string) *RouteError    { return errcode.NotFound(m) }
func ErrForbidden(m string) *RouteError   { return errcode.Forbidden(m) }
func ErrConflict(m string) *RouteError    { return errcode.Conflict(m) }
func ErrInternal(m string) *RouteError    { return errcode.Internal(m) }
func ErrUnavailable(m string) *RouteError { return errcode.Unavailable(m) }
```
Note: `RouteError = errcode.Error` is a type alias, so `var rerr *RouteError; errors.As(err,&rerr); return rerr` keeps compiling. But `rerr.Code` is now `errcode.Code`, not `string`, so **production** code that assigns/compares `.Code` to a string breaks the build. There is exactly one such production site — `search-service/metrics.go` (`status = rerr.Code`) — fixed in Step 5 below so `go build` stays green. (`search-service/handler.go:148` only does `errors.As` + `return rerr`, which the alias keeps valid.) Test-only `.Code` comparisons are migrated in Chapters 11–12.

`context.go` — BOTH `Context` error-reply methods become errcode-backed (the plan previously missed `ReplyError`, which is called at `register.go:20` and by Recovery middleware; leaving it on `natsutil.ReplyError` would emit a `code`-less envelope AND break compilation when Ch 17 deletes `natsutil.ReplyError`):
```go
func (c *Context) ReplyRouteError(e *RouteError) { errnats.Reply(c, c.Msg, e) }

// ReplyError replies with a bad_request envelope for the given message. (Kept
// for the deserialize-failure path; new code should return a typed errcode.)
func (c *Context) ReplyError(message string) { errnats.Reply(c, c.Msg, errcode.BadRequest(message)) }
```
Note `register.go:20` (`c.ReplyError("invalid request payload")`, the payload-deserialize path) now routes through errcode and yields `{code:"bad_request","error":"invalid request payload"}` — assert this in Step 1.

`router.go` — `replyBusy` and the panic backstop have ALREADY logged (saturation warn / panic recovery), so they use the **non-logging** `errnats.ReplyQuiet` to avoid a redundant `Classify` line (and to keep per-event saturation replies out of the ERROR stream):
```go
func (r *Router) replyBusy(msg *nats.Msg) {
	if msg.Reply == "" {
		slog.Warn("natsrouter: dropped fire-and-forget message under saturation", "subject", msg.Subject)
		return
	}
	errnats.ReplyQuiet(msg, errcode.Unavailable("service busy"))
}
```
Panic backstop (router.go:~204) — `recover()` already logged the panic:
```go
if m.Msg.Reply != "" {
	errnats.ReplyQuiet(m.Msg, errcode.Internal("internal error"))
}
```

`middleware.go` Recovery (~54): the recovery handler already logs `"panic recovered"`, so use the quiet reply: `c.ReplyError(...)` → `errnats.ReplyQuiet(c.Msg, errcode.Internal("internal error"))`.

`params.go:41` — `ErrBadRequest("missing required param ...")` keeps working via the shim; no change needed, but verify it compiles.

Add `errcode`/`errnats` imports where used; drop `natsutil`/`errors` where now unused (goimports via `make fmt` will catch leftovers in `register.go`).

- [ ] **Step 4: Run** `make test SERVICE=pkg/natsrouter` → PASS (this runs the package's own tests with `-race`; they were migrated in Step 1, so the package is fully green here — NOT deferred).
- [ ] **Step 5: Fold the one production-code break, then build.** Before building, fix the single production site that assigns `errcode.Code` to a string — `search-service/metrics.go` (~:100-104). Minimal in-place change (full handler migration still happens in Ch 12; this is just to keep `go build` green now):
  ```go
  status := string(errcode.CodeInternal)
  var ee *errcode.Error
  if errors.As(err, &ee) { status = string(ee.Code) }
  ```
  (Drop the `natsrouter.CodeInternal`/`RouteError` references in that block; add `errcode` import.) Then:
  ```bash
  go build ./...                              # expect: clean
  ```
  Now enumerate the test-only breakage (compiles, fails at runtime — `go build` skips other packages' `_test.go`) so each chapter's scope is exact:
  ```bash
  grep -rn '\.Code ==\|== natsrouter.Code\|RouteError\|natsrouter.Err' --include='*_test.go' \
    search-service history-service mock-user-service
  ```
  Expected hits (migrated in their chapters): `search-service` handler_test ×18, query_rooms_test ×2, integration_*_test ×6 (incl. one `CodeInternal` at `integration_users_test.go:100`); `history-service/internal/service` messages_test ×15, threads_test ×1; `mock-user-service` handler_test ×1. Record the list; do not fix here.
- [ ] **Step 6: Update CLAUDE.md NOW (not in Ch 18).** The Section 3 Error-Handling bullet "Use `model.ErrorResponse` via `natsutil.ReplyError` for all NATS reply errors" is contradicted from the next chapter onward; leaving it until Ch 18 means every intermediate commit violates a current OVERRIDING rule. Replace that bullet with: "Use `pkg/errcode` for all client-facing errors; reply via `errnats.Reply` (NATS) / `errhttp.Write` (Gin). Never hand-build `model.ErrorResponse` or call `natsutil.ReplyError`." (Full `docs/error-handling.md` still lands in Ch 18.)
- [ ] **Step 7: Commit** — `git add pkg/natsrouter/ search-service/metrics.go CLAUDE.md && git commit -m "refactor(natsrouter): emit errcode envelopes; RouteError/Err* now shims over errcode"`

---

## Chapter 11 — history-service (reference migration)

### Task 11.1: Swap `natsrouter.Err*` for `errcode`

**Files:** Modify `history-service/*.go`, `history-service/**/*_test.go`, `docs/client-api.md`.

- [ ] **Step 1: Enumerate** — `grep -rn "natsrouter.Err\|ErrWithCode\|RouteError\|ReplyRouteError\|natsrouter.Code" history-service/` (include subpackages, e.g. `internal/service/`). Record each site + current code/message. Audit found **30 client-facing `natsrouter.Err*` reply sites** (messages.go ×11, threads.go ×7, utils.go ×6, room_times.go ×2, …) and **16 test `.Code` asserts** (`messages_test.go` ×15 + `threads_test.go:452` ×1 — not "~22"). All use BadRequest/Forbidden/NotFound/Internal; Conflict/Unavailable/Unauthenticated are unused here.

- [ ] **Step 2: Update one handler test** to decode the reply and assert `code` (and `reason` if applicable) instead of `RouteError{Code:...}`.

- [ ] **Step 3: Run, expect FAIL.**

- [ ] **Step 4: Migrate call sites** using this table (apply to every site from Step 1):

| Old | New |
|-----|-----|
| `natsrouter.ErrBadRequest(m)` / `natsrouter.Err(m)` | `errcode.BadRequest(m)` |
| `natsrouter.Errf(f, a...)` | `errcode.BadRequest(fmt.Sprintf(f, a...))` |
| `natsrouter.ErrNotFound(m)` | `errcode.NotFound(m)` |
| `natsrouter.ErrForbidden(m)` | `errcode.Forbidden(m)` |
| `natsrouter.ErrConflict(m)` | `errcode.Conflict(m)` |
| `natsrouter.ErrInternal(m)` | prefer returning the wrapped raw error: `fmt.Errorf("…: %w", err)` (Classify → internal). Use `errcode.Internal(m)` only when there is no underlying error. |

Add `reason`s only where history-service has a case the frontend must distinguish (most are generic — confirm against the endpoints). Update imports.

- [ ] **Step 5: Enrich log context** at each handler entry: `c.WithLogValues("request_id", natsutil.RequestIDFromContext(c), "account", account, "roomID", c.Param("roomID"))`. Every history handler has `roomID` from the route param, so include it unconditionally (not "where available"). The reply path (`errnats.Reply(c, …)`) then logs with these attrs.

- [ ] **Step 6: Update all remaining old-shape test assertions** found in Step 1 (the full `messages_test.go`/`threads_test.go` set), decoding to `code`/`reason`.

- [ ] **Step 7: Run** `make test SERVICE=history-service` → PASS.

- [ ] **Step 8: Update `docs/client-api.md`** — per history-service endpoint, add/refresh the "Possible errors" table (`code`, `reason`, when).

- [ ] **Step 9: Commit** — `git add history-service/ docs/client-api.md && git commit -m "refactor(history-service): adopt errcode envelopes"`

---

## Chapter 12 — search-service (incl. metrics.go) and mock-user-service

### Task 12.1: search-service handlers AND the Prometheus label path

**Files:** Modify `search-service/handler.go`, `query_rooms.go`, **`metrics.go`**, `*_test.go`, `integration_*_test.go`, `docs/client-api.md`.

- [ ] **Step 1: Enumerate** — `grep -rn "natsrouter.Err\|RouteError\|natsrouter.Code\|rerr.Code" search-service/`. Sites: handler.go (`natsrouter.Err*` constructors); `handler.go:148-150` (`var rerr *natsrouter.RouteError` passthrough); `query_rooms.go:74` — the **exported** signature `func roomTypeFilterClause(...) (map[string]any, *natsrouter.RouteError)` plus its doc comment (`query_rooms.go:20`); `metrics.go:100-104` (already minimally fixed in Ch 10 Step 5 — finalize here). Plus the test sites enumerated in Ch 10 Step 5.

- [ ] **Step 2: Update a representative handler test** (`searchMessages` "query is required") to assert `errtest.AssertCode(t, reply, errcode.CodeBadRequest)`.

- [ ] **Step 3: Run, expect FAIL.**

- [ ] **Step 4: Migrate handlers** per Task 11.1's table. `natsrouter.ErrInternal("unable to build search query")` → `fmt.Errorf("building search query: %w", err)`.

- [ ] **Step 5: Change `query_rooms.go:74` return type** `*natsrouter.RouteError` → `*errcode.Error` (and update the `query_rooms.go:20` doc comment + the caller in `handler.go`). This is required because Ch 17 deletes the `RouteError` alias; the alias only masks it until then. Update `query_rooms_test.go` (×2 `.Code` asserts) to `errtest`.

- [ ] **Step 6: Finalize `metrics.go`** — Ch 10 left the minimal `errors.As`-based status label. Keep that form (it deliberately avoids a second `Classify` log line, since the reply path already logs); just confirm it reads cleanly and drop any leftover `natsrouter` import.

- [ ] **Step 7: Migrate `handler.go:148-150`** passthrough: replace the `*natsrouter.RouteError` assertion with `*errcode.Error`.

- [ ] **Step 8: Enrich log ctx** at handler entry (as Task 11.1 Step 5). Migrate ALL test `.Code` assertions to `errtest`: `handler_test.go` has **18** (not "~17"), `query_rooms_test.go` ×2, and integration tests asserting `model.ErrorResponse.Code` against **both** `natsrouter.CodeBadRequest` AND `natsrouter.CodeInternal` (esp. `integration_users_test.go:100`, the one internal assertion) — = 26 total. The internal one must migrate too, or it fails at runtime and breaks compile when Ch 17 removes `model.ErrorResponse`.

- [ ] **Step 9: Run + docs + commit**
```bash
make test SERVICE=search-service
git add search-service/ docs/client-api.md
git commit -m "refactor(search-service): adopt errcode envelopes incl. metrics label"
```

### Task 12.2: mock-user-service

**Files:** Modify `mock-user-service/handler.go`, `*_test.go`.

- [ ] **Step 1:** `grep -n "natsrouter.Err" mock-user-service/handler.go` → `checkSite` returns `natsrouter.ErrNotFound("unknown site")`.
- [ ] **Step 2:** Update the `checkSite` test (`handler_test.go:28-30`) to assert decoded `code=="not_found"`.
- [ ] **Step 3:** Run, expect FAIL.
- [ ] **Step 4:** Replace with `errcode.NotFound("unknown site")`; update imports; enrich log ctx.
- [ ] **Step 5:** `make test SERVICE=mock-user-service && git add mock-user-service/ && git commit -m "refactor(mock-user-service): adopt errcode envelopes"`

---

## Chapter 13 — message-gatekeeper (incl. fetcher_history.go)

Preserve the infra-vs-validation **ack/nak** decision (a JetStream retry concern, independent of the envelope). Only the reply payload and the remote-error parse change.

### Task 13.1: Replace `codedError`; resolve category to `forbidden` + reason

**Files:** Modify `message-gatekeeper/store.go`, `handler.go`, `*_test.go`, `docs/client-api.md`.

Resolve the spec contradiction: both gatekeeper validation errors are **`forbidden`** (the user is not permitted to post), with the specific case in `reason`.

- [ ] **Step 1: Fix the tests that reference the deleted reply path.** The plan previously cited the wrong range. The actual sites:
  - `handler_test.go:1159-1188 TestHandler_marshalErrorReply` — 3 subtests calling the soon-deleted `marshalErrorReply` and reading `errLargeRoomPostRestricted.Code`. **Delete/rewrite** this whole test (the method is gone).
  - `handler_test.go:686-688` — the real large-room reply assertion inside the `processMessage` table. Update to decode the reply and assert `code=="forbidden"`, `reason=="large_room_post_restricted"` via `errtest`.
  - `handler_test.go:477,563,687` — `assert.ErrorIs(t, err, errLargeRoomPostRestricted)`: these match by identity and KEEP working (the sentinel is still returned directly at handler.go:220). Leave them.

- [ ] **Step 2: Run, expect FAIL.**

- [ ] **Step 3: Replace coded errors in `store.go`** (delete `codedError`, `codeLargeRoomPostRestricted`, the old `errLargeRoomPostRestricted`, and `errNotSubscribed`'s `errors.New` form):
```go
var (
	errNotSubscribed = errcode.Forbidden("not subscribed", errcode.WithReason(errcode.MessageNotSubscribed))
	errLargeRoomPostRestricted = errcode.Forbidden(
		"posting is restricted to owners and admins in this room",
		errcode.WithReason(errcode.MessageLargeRoomPostRestricted))
)
```
`errors.Is(err, errNotSubscribed)` (handler.go:194) still works by identity. The `infraError` detection (`errors.As(err,&ie)`) is independent of these values, so ack/nak is unchanged.

- [ ] **Step 4: CRITICAL — re-home the inline validation errors so they don't collapse to `internal`.** Step 5 routes ALL validation replies through `errnats.Marshal`→`Classify`, which collapses any non-`*errcode.Error` to `{"code":"internal","error":"internal error"}`. Today these reply with their real message (documented at `docs/client-api.md:1886-1894`). Convert each plain `fmt.Errorf` validation return to a typed errcode:
  - `handler.go:149,155,164,169,173,178,183,188,277,281,283` (missing/malformed fields, bad subject, invalid payload) → `errcode.BadRequest(...)` (or `errcode.NotFound(...)` for the quote/message-missing case — check each message). Preserve the exact user message text.
  - `handler.go:195` — the `not_subscribed` reply path returns a FRESH `fmt.Errorf("user %s is not subscribed…")` that does NOT wrap the sentinel, so it would collapse to internal and lose the reason. Change to `return nil, errNotSubscribed` (or `fmt.Errorf("…: %w", errNotSubscribed)` if the dynamic account text matters — but the sentinel message is preferred so the reason survives).
  Audit the whole handler: any error path that REPLIES to the client (validation/Ack branch) must be an `*errcode.Error`; only infra errors (Nak branch) stay raw.

- [ ] **Step 5: Replace `marshalErrorReply`** in `handler.go` — delete it; build the reply bytes with `errnats.Marshal`:
```go
h.sendReply(ctx, account, msg.Data(), errnats.Marshal(ctx, err))
```
The `"invalid message subject"` path → `errnats.Marshal(ctx, errcode.BadRequest("invalid message subject"))`. Keep the infra→Nak / validation→Ack+reply branch exactly as-is. **Enrich ctx AFTER parsing** (not "at entry" — `account`/`roomID` come from `ParseUserRoomSiteSubject` and `reqID` from the payload `req.RequestID`, parsed inside `HandleJetStreamMsg`, not at the `main.go:143` call site): once subject+payload are parsed, `ctx = errcode.WithLogValues(ctx, "request_id", req.RequestID, "account", account, "roomID", roomID)`.

- [ ] **Step 6: Migrate `fetcher_history.go:61`** — it decodes a remote history-service reply via `model.ErrorResponse` to detect upstream errors. Replace with `errcode.Parse`:
```go
if ee, ok := errcode.Parse(replyData); ok {
	return nil, fmt.Errorf("history fetch: %s", ee.Message)
}
```
Update `fetcher_history_test.go:88` accordingly (it currently builds a `model.ErrorResponse` payload — switch to an errcode envelope; the `Contains("message not found")` assertion still holds since `ee.Message` is the `error` field).

- [ ] **Step 7: Run** `make test SERVICE=message-gatekeeper` → PASS.
- [ ] **Step 8: docs + commit** — Update `docs/client-api.md:1886-1894`: fill the `code` column (currently `—`) for every now-`bad_request`/`forbidden`/`not_found` validation row, and add the `reason` for the two forbidden cases.
```bash
git add message-gatekeeper/ docs/client-api.md
git commit -m "refactor(message-gatekeeper): errcode envelopes + errcode.Parse for remote errors"
```

---

## Chapter 14 — room-service (delete `sanitizeError`, incl. memberlist_client.go + DM-exists)

### Task 14.1: Convert sentinels to errcode; delete `sanitizeError`

**Files:** Modify `room-service/helper.go`, `handler.go`, `*_test.go`, `docs/client-api.md`.

Mapping (message text from the current sentinels):

| Sentinel | Code | Reason |
|----------|----------|--------|
| `errInvalidRole` | BadRequest | — |
| `errOnlyOwners` | Forbidden | — |
| `errAlreadyOwner` | Conflict | — |
| `errNotOwner` | Forbidden | `RoomNotOwner` |
| `errCannotDemoteLast` | Conflict | — |
| `errRoomTypeGuard` | BadRequest | — |
| `errTargetNotMember` | BadRequest | — |
| `errNotRoomMember` | Forbidden | `RoomNotMember` |
| `errInvalidOrg` | BadRequest | — |
| `errInvalidThreadID` | BadRequest | — |
| `errThreadSubNotFound` | NotFound | — |
| `errPromoteRequiresIndividual` | BadRequest | — |
| `errEmptyCreateRequest` | BadRequest | — |
| `errSelfDM` | BadRequest | — |
| `errBotInChannel` | BadRequest | `RoomBotInChannel` |
| `errBotNotAvailable` | NotFound | `RoomBotNotAvailable` |
| `errInvalidUserData` | BadRequest | — |
| `errMissingRequestID` | BadRequest | — |
| `errInvalidRequestID` | BadRequest | — |
| `errChannelNameRequired` | BadRequest | — |
| `errChannelNameTooLong` | BadRequest | — |
| `errUserNotFound` | NotFound | — |
| `errMessageNotFound` | NotFound | — |
| `errMessageRoomMismatch` | BadRequest | — |
| `errNotMessageSender` | Forbidden | — |
| `errRemoveTargetAmbiguous` | BadRequest | — |
| `errCannotRemoveLastMember` | Conflict | — |
| `errLastOwnerCannotLeave` | Conflict | `RoomLastOwnerCannotLeave` |
| `errOrgMemberCannotLeaveSolo` | Forbidden | — |
| `errRoomIDMismatch` | BadRequest | — |
| `errRemoveChannelOnly` | BadRequest | — |
| `errListLimitInvalid` | BadRequest | — |
| `errListOffsetInvalid` | BadRequest | — |
| room-capacity ("at maximum capacity"/"exceeds maximum capacity") | Conflict | `RoomMaxSizeReached` |
| `channelExpandTimeoutError` | Unavailable | — |
| `dmExistsError` | → **success reply** (Task 14.3) | — |

- [ ] **Step 1: Convert sentinel defs in `helper.go`** (each `errors.New` → `errcode.<Code>(msg[, WithReason(...)])`). Example:
```go
var (
	errInvalidRole   = errcode.BadRequest("invalid role: must be owner or member")
	errOnlyOwners    = errcode.Forbidden("only owners can update roles")
	errNotRoomMember = errcode.Forbidden("only room members can list members", errcode.WithReason(errcode.RoomNotMember))
	errNotOwner      = errcode.Forbidden("user is not an owner", errcode.WithReason(errcode.RoomNotOwner))
	// … all sentinels per the table
)
```

- [ ] **Step 2: CRITICAL — re-home the inline allowlist-passthrough errors BEFORE deleting `sanitizeError`.** `sanitizeError`'s substring allowlist (`helper.go:227`) does not just sanitize the named sentinels — it passes through ~6 classes of **inline `fmt.Errorf`** errors that are NOT in the sentinel table. Deleting the allowlist (this step) makes every one of them collapse to `internal error` at `errnats.Reply`. The load-bearing passthrough is proven by `helper_test.go:63-69`. Convert each inline site to a typed errcode AT THE SOURCE (preserve the user message text):
  - `handler.go:502,522` `"only owners can remove members"`, `:660` `"only owners can add members to a restricted room"` → `errcode.Forbidden(...)` (allowlist prefix `"only owners can"`).
  - `handler.go:657` `"cannot add members to a non-channel room"` → `errcode.BadRequest(...)` (prefix `"cannot add members"`).
  - `handler.go:648` `"requester not in room: %w"` → `errcode.Forbidden(...)` (prefix `"requester not in room"`).
  - `handler.go:155,437,462,561,564,666,669,889,1139,1142` — the `"invalid request…"` family incl. `"room ID mismatch"`, `"messageId is required"` (prefix `"invalid request"`, the widest leak — 10 sites) → `errcode.BadRequest(...)`.
  - `handler.go:1408` `"invalid mute-toggle subject"` → `errcode.BadRequest(...)`.
  Grep `sanitizeError`'s allowlist for the exact prefixes and verify every passthrough string has a re-homed errcode; the test at `helper_test.go:63-69` enumerates them — use it as the checklist.
- [ ] **Step 3: Delete `sanitizeError` (helper.go:176-234)** including the substring allowlist. Where the capacity strings originate (`handler.go:320,717`), return `errcode.Conflict("room is at maximum capacity", errcode.WithReason(errcode.RoomMaxSizeReached))` at the source. Where `channelExpandTimeoutError` is constructed, return `errcode.Unavailable(fmt.Sprintf("expanding channels timed out for room %s on site %s", roomID, siteID))`; delete the custom type if unreferenced.

- [ ] **Step 4: Update reply sites in `handler.go`** — `natsutil.ReplyError(m.Msg, sanitizeError(err))` → `errnats.Reply(ctx, m.Msg, err)`. room-service uses raw `nc.QueueSubscribe`/`otelnats.Msg` and an existing `wrappedCtx(m)` helper used at **11 call sites** (`handler.go:108,371,384,…`). **Fold the log enrichment into `wrappedCtx`** rather than hand-rolling per handler, so all 11 sites are consistent:
```go
// inside wrappedCtx(m), after extracting requestID/account/roomID:
return errcode.WithLogValues(ctx, "request_id", requestID, "account", account, "roomID", roomID)
```
(If `account`/`roomID` aren't known inside `wrappedCtx`, add them at each handler via `c`-less `errcode.WithLogValues(ctx, …)`; do NOT use `c.WithLogValues` — this is not natsrouter.)

- [ ] **Step 5: Delete/rewrite the `sanitizeError` test suite** (`helper_test.go:38-234` — the suite STARTS at 38, not 75; it includes `RemoteMemberListPrefix`/`WithContext`/`TransportFailureStillOpaque` at 131-146 and `NewSentinelErrorsExist`/`DMExistsErrorWraps` at 149-174, all referencing deleted symbols — remove `TestDMExistsErrorWrapsCorrectly` explicitly). Rewrite handler/integration assertions (`handler_test.go:1437-1458,2651-2701`, `integration_test.go:1334-1339`) to decode `code`/`reason` via `errtest`.

- [ ] **Step 6: Run** `make test SERVICE=room-service` (DM-exists + memberlist tests temporarily expected to fail until 14.2/14.3).

### Task 14.2: Migrate `memberlist_client.go` to `errcode.Parse` + reason matching

**Files:** Modify `room-service/memberlist_client.go`, `memberlist_client_test.go`.

The current code (`memberlist_client.go:65-70`) uses `natsutil.TryParseError` + `errResp.Error == errNotRoomMember.Error()` (brittle message-string equality across sites) to remap a remote not-member error.

- [ ] **Step 1: Update the test** (`memberlist_client_test.go:68-94`) so the simulated remote reply is an errcode envelope `{code:"forbidden",reason:"not_room_member",error:"…"}`, and assert the client remaps it to the local `errNotRoomMember` (or returns an `*errcode.Error` with `reason==RoomNotMember`).

- [ ] **Step 2: Run, expect FAIL.**

- [ ] **Step 3: Rewrite the decode**:
```go
if ee, ok := errcode.Parse(replyData); ok {
	if ee.Reason == errcode.RoomNotMember {
		return errNotRoomMember
	}
	// Preserve remote message for other remote errors (replaces the old
	// "remote member.list:" allowlist passthrough).
	return errcode.New(ee.Code, ee.Message, errcode.WithReason(ee.Reason))
}
```
Remove the `errResp.Error == errNotRoomMember.Error()` string comparison and the dependence on the deleted `sanitizeError` "remote member.list:" prefix. **Rollout note:** during a mixed-version window a legacy remote room-service still replies via `natsutil.MarshalError` (no `code`). `errcode.Parse` still succeeds (it only requires a non-empty `error`) but yields `Code==""` → `HTTPStatus()` 500 and no reason match. That's an acceptable degradation (the not-member remap simply doesn't fire until both sides are upgraded); call it out so it isn't mistaken for a bug.

- [ ] **Step 4: Run** the memberlist tests → PASS.

### Task 14.3: DM-already-exists → success reply

**Files:** Modify `room-service/handler.go` (DM-exists block, ~125-137), `pkg/model/event.go` (add status const), `handler_test.go`, `integration_test.go`, `pkg/model/model_test.go`, `docs/client-api.md`.

- [ ] **Step 1: Add the status constant** in `pkg/model/event.go` near `CreateRoomReply`:
```go
// CreateRoomStatusExists indicates the requested DM already existed; RoomID is
// the existing room. Clients treat it as success and open the room.
const CreateRoomStatusExists = "exists"
```

- [ ] **Step 2: Update the DM-exists handler test** to expect a SUCCESS reply `model.CreateRoomReply{Status: "exists", RoomID: existing}` (decode the reply; assert `status=="exists"` and `roomId==existing`), not an error envelope.

- [ ] **Step 3: Run, expect FAIL.**

- [ ] **Step 4: Replace the error reply** — instead of marshalling `model.ErrorResponse{Error:"dm already exists", RoomID: existingRoomID}`:
```go
natsutil.ReplyJSON(m.Msg, model.CreateRoomReply{Status: model.CreateRoomStatusExists, RoomID: existingRoomID})
return
```
Remove the `dmExistsError` type, `newDMExistsError` (handler.go:258), and ALL `errors.As/Is(err, *dmExistsError)` checks — `handleCreateRoom` must stop returning it. **Confirm `model.CreateRoomReply.RoomType`** (`pkg/model/event.go:316`) — the success-exists reply leaves it `""` (the old error path didn't send it either); verify the frontend does not read `roomType` on the exists branch, else populate it.

- [ ] **Step 5: Rewrite the `*dmExistsError` routing tests** — the type is gone, so these no longer compile: `integration_test.go:1588-1594` (`errors.As(err, *dmExistsError)`), `handler_test.go:2333,2427-2442,2662-2681`. Rewrite each to assert the SUCCESS reply (`status=="exists"`, `roomId==existing`) instead of an error. (Step 2's test covers the happy path; these are the routing/`errors.As` sites the plan previously missed.)

- [ ] **Step 6: Remove the now-obsolete model test** `TestErrorResponseRoomIDOmitempty` (`model_test.go:2002`) since `model.ErrorResponse.RoomID` is removed in Chapter 17; if Chapter 17 hasn't run yet, leave the field but stop using it here. (Field removal happens in Ch 17.)

- [ ] **Step 7: Update `docs/client-api.md:235-238`** — rewrite the DM-exists block from the old `{"error":"dm already exists","roomId":…}` (documented-as-success-error) to the new success shape `{"status":"exists","roomId":…}`. Note the semantics flip in the changelog section.

- [ ] **Step 8: BREAKING-CONTRACT GATE (must be checked before merge/deploy).** DM-exists flips from an error the old client treats as success (keys on `.error` + `.roomId`) to an explicit `{status:"exists"}` success with NO `.error`. An old frontend would mis-handle the new reply. Therefore:
  - The frontend create-DM change (Ch 18.2 Step 3) MUST land in the **same release** as this commit. Do not merge room-service ahead of the frontend.
  - Record this coupling in the PR description and the `docs/client-api.md` changelog (not just here).
  - If the team cannot co-release, STOP and ask: the fallback is to keep emitting the legacy `{error,roomId}` shape behind a temporary flag until the frontend ships — but the default plan is co-release.

- [ ] **Step 9: Run + commit**
```bash
make test SERVICE=room-service
git add room-service/ pkg/model/ docs/client-api.md
git commit -m "refactor(room-service): replace sanitizeError with errcode; DM-exists now returns success; remote member-list via errcode.Parse"
```

---

## Chapter 15 — room-worker (explicit permanence + AsyncJobResult)

### Task 15.1: Add `Code`/`Reason` to `AsyncJobResult`

**Files:** Modify `pkg/model/event.go:280-287`, `pkg/model/model_test.go`.

- [ ] **Step 1: Update `TestAsyncJobResultShape`** to set `Code`/`Reason` on the error case and assert round-trip, AND assert the success case (`Status:"ok"`) marshals WITHOUT `code`/`reason` (omitempty).

- [ ] **Step 2: Run, expect FAIL.**

- [ ] **Step 3: Add fields**
```go
type AsyncJobResult struct {
	RequestID string `json:"requestId"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
	RoomID    string `json:"roomId,omitempty"`
	Error     string `json:"error,omitempty"`
	Code      string `json:"code,omitempty"`   // string, not errcode.Code: pkg/model must not import errcode
	Reason    string `json:"reason,omitempty"`
	Timestamp int64  `json:"timestamp"`
}
```

- [ ] **Step 4: Run + commit**
```bash
go test ./pkg/model/ -run TestAsyncJobResult -v
git add pkg/model/event.go pkg/model/model_test.go
git commit -m "feat(model): add code/reason to AsyncJobResult"
```

### Task 15.2: Convert sanitizers; make permanence EXPLICIT (not category-inferred)

**Files:** Modify `room-worker/handler.go`, `*_test.go`, `docs/client-api.md`.

**Critical correction from review:** do NOT infer permanence from "non-internal category". Many real permanent errors (room-ID collision, user-not-found, unknown room type, invalid request ID, room key absent) naturally classify to `internal` and would be Nak'd/redelivered forever. Keep an **explicit permanent marker** alongside the errcode value.

- [ ] **Step 1: Define a permanent wrapper that carries an errcode payload**
```go
// permanentError marks a job failure as non-retryable (Ack, don't Nak) AND
// carries the client-facing errcode payload. The errcode value may be any
// category, including internal — permanence is explicit, never inferred.
type permanentError struct{ ec *errcode.Error }

func (e *permanentError) Error() string { return e.ec.Error() }
func (e *permanentError) Unwrap() error { return e.ec } // so errors.As finds the *errcode.Error

func permanent(ec *errcode.Error) error { return &permanentError{ec: ec} }
```
Migrate EVERY current explicit-permanent site — there are **21, not "≈10"** (enumerate via `grep -n "newPermanent\|newPermanentAbsent" room-worker/handler.go`): `newPermanent` at lines 182, 309, 469, 674, 747, 750, 766, 839, 847, 965, 1214, 1217, 1222, 1241, 1267, 1269, 1310, 1351; `newPermanentAbsent` at 1235, 1846. Each becomes `permanent(errcode.<Code>("…"[, WithReason(...)]))`. Suggested categories: collision(182)→`Internal`; wrong-room-type(309,766,1310)→`BadRequest`/`Internal`; user-not-found(469,674,839,847,965,1241,1267,1269,1351)→`NotFound` or `Internal`; request-ID(747,750,1214,1217)→`BadRequest`; unmarshal(1222)→`BadRequest`; key-absent(1235,1846)→`Internal`+`WithCause` (below). **All 21 must stay wrapped in `permanent(...)`** — several (collision, key-absent, unknown-room-type) classify to `internal`, so if permanence were category-inferred they'd be Nak'd forever; the explicit marker is exactly what prevents that.

**Locked design for `newPermanentAbsent`** (resolving the earlier "OR"): attach the alert sentinel as the errcode cause —
```go
permanent(errcode.Internal("room key absent", errcode.WithCause(errRoomKeyAbsent)))
```
This yields the chain `permanentError →(Unwrap)→ *errcode.Error →(Unwrap)→ errRoomKeyAbsent`. Note: the production `KeyAbsentErrors` alert metric is incremented **inline at the call site** (handler.go:1234,1845), BEFORE the error is built — so the alert does not actually depend on this chain. The `errors.Is(err, errRoomKeyAbsent)` resolution is relied on only by tests (handler_test.go:3478,3558). Still, TEST in Step 2 that BOTH resolve in one pass:
- `errors.As(err, &ee)` finds the `*errcode.Error` (for the reply payload), AND
- `errors.Is(err, errRoomKeyAbsent)` still matches, because `errcode.Error.Unwrap()` returns its cause.

`errRoomKeyAbsent` is confirmed a raw `errors.New(...)` (handler.go:34), not an `*errcode.Error`, so `WithCause` does NOT panic.

- [ ] **Step 1b: Migrate the two paths the plan previously missed.**
  - **`errRoomIDCollision` + sync-DM reconcile branch** (`handler.go:1593`, and the `errors.Is(reconcileErr, errPermanent)` branch at `~1689-1696`). Once `errPermanent`/`permanentError` semantics change, rewrite that branch to `errors.As(reconcileErr, &pe *permanentError)`, and convert `errRoomIDCollision` → `errcode.Conflict("room id collision", …)`. Without this, the sync-DM collision path either won't compile or silently returns raw internal.
  - **`processRoleUpdate` (handler.go:224-299) has NO AsyncJobResult and returns 6+ bare `fmt.Errorf`** (lines 228,237,242,245,248,254,266,269,275,289,295). Line 248 (`"unsupported role"`) is a real permanent error currently **Nak'd forever — a pre-existing bug**. Wrap the validation returns as typed errcode, and wrap the permanent ones (esp. :248) in `permanent(errcode.BadRequest("unsupported role"))` so they Ack. If role-update is supposed to publish an AsyncJobResult like its siblings, add it; if not, confirm that's intentional (docs:476 says it doesn't today).

- [ ] **Step 2: Write the failing test** — cover:
  - permanent `errcode.Forbidden(..., WithReason(RoomNotMember))` → `AsyncJobResult{Status:"error", Error:"…", Code:"forbidden", Reason:"not_room_member"}` AND Ack'd (not Nak'd);
  - raw infra error → `Code:"internal", Error:"internal error"` AND Nak'd;
  - permanent `errcode.Internal("collision")` → Ack'd (permanence is explicit, not category-inferred) with `Code:"internal"`;
  - **the `newPermanentAbsent` chain**: assert `errors.Is(err, errRoomKeyAbsent)` is true AND `errors.As(err, &ee)` yields `ee.Code=="internal"` on the SAME error value (guards the locked Step-1 design);
  - **`processRoleUpdate` unsupported-role** (handler.go:248) is now Ack'd, not Nak'd forever;
  - **rewrite the existing suites that reference deleted functions**: `handler_test.go:2644-2657` (`sanitizeAsyncJobError`) and the `sanitizeSyncDMError` tables at `2713-2723,3008-3151` must move to `fillAsyncError`/`errnats`, not merely be added to.

- [ ] **Step 3: Run, expect FAIL.**

- [ ] **Step 4: Replace `sanitizeAsyncJobError`** with a populator:
```go
func (h *Handler) fillAsyncError(ctx context.Context, result *model.AsyncJobResult, jobErr error) {
	e := errcode.Classify(ctx, jobErr)
	result.Status = model.AsyncJobStatusError
	result.Error, result.Code, result.Reason = e.Message, string(e.Code), string(e.Reason)
}
```
The Ack/Nak decision stays keyed on `errors.As(jobErr, &pe *permanentError)` (Ack) vs not (Nak) — independent of category. Where the result was built with `result.Error = sanitizeAsyncJobError(jobErr)`, call `h.fillAsyncError(ctx, &result, jobErr)`.

- [ ] **Step 5: Replace `sanitizeSyncDMError`** — `natsutil.ReplyError(m.Msg, sanitizeSyncDMError(err))` → `errnats.Reply(ctx, m.Msg, err)`. Convert sync-DM sentinels (`errMissingRequestID`, `errInvalidRequestID`, `errInvalidSyncDMRequest`, `errUserLookupFailed`, `errCrossSiteRequester`) to errcode: mostly `errcode.BadRequest(...)`; `errUserLookupFailed` → return the wrapped raw error (Classify → internal). Enrich ctx at entry.

- [ ] **Step 6: REQUIRED — add panic recovery to the async consumer goroutine.** The async path runs in a JetStream consumer goroutine NOT under natsrouter's recovery middleware. A stray panic crashes the worker. The surface is broader than `WithCause`: `WithMetadata` with odd args also panics. Verified absent today — `room-worker/main.go` (~:156-163) only defers semaphore release + `wg.Done()`. Add recovery inside that goroutine (write the failing test first — a handler that panics must Nak + not crash):
  ```go
  go func() {
      defer func() {
          if r := recover(); r != nil {
              slog.Error("panic in async job handler", "panic", r, "subject", msg.Subject())
              _ = msg.Nak()
          }
          <-sem
          wg.Done()
      }()
      // ... existing job processing ...
  }()
  ```
  (Match the existing semaphore/`wg` mechanics exactly; the recover must run before the semaphore release so ordering is preserved. Follow the reference worker pattern if one already has recovery.)

- [ ] **Step 7: Run + docs + commit.** Docs updates (`docs/client-api.md`): add `code`/`reason` rows to the `AsyncJobResult` schema table (~:338) and refresh the example JSON; update §5 (~:2045-2057) which currently says "Absent for plain `natsutil.ReplyError`" — the sync-DM reply now emits `code`; update the create.dm error section accordingly.
```bash
make test SERVICE=room-worker
git add room-worker/ docs/client-api.md
git commit -m "refactor(room-worker): errcode async results + sync DM; explicit permanence marker"
```

---

## Chapter 16 — auth-service (HTTP)

**GATED on PM confirmation of the `unauthenticated` (401) category.**

### Task 16.1: Replace `gin.H{"error":...}` with `errhttp.Write`

**Files:** Modify `auth-service/handler.go:79-148`, `handler_test.go`, `docs/client-api.md`.

| Site | New |
|------|-----|
| :79 | `errhttp.Write(ctx, c, errcode.BadRequest("ssoToken and natsPublicKey are required"))` |
| :84,:141 | `errcode.BadRequest("invalid natsPublicKey format")` |
| :92 | `errcode.Unauthenticated("SSO token has expired, please re-login", errcode.WithReason(errcode.AuthTokenExpired))` |
| :96 | `errcode.Unauthenticated("invalid SSO token", errcode.WithReason(errcode.AuthInvalidToken))` |
| :108,:148 | `errhttp.Write(ctx, c, fmt.Errorf("generating NATS token: %w", err))` (→ internal; real error logged) |
| :136 | `errcode.BadRequest("account and natsPublicKey are required")` |

Audit confirmed all **8 error sites** above are the complete set (no `c.String`/`fmt.Errorf`/`errors.New` response sites), the `errors.Is(err, pkgoidc.ErrTokenExpired)` discriminator at :90 is preserved, and `middleware.go:21` does set `c.Set("request_id", …)` (36-char UUIDv7, CLAUDE.md-compliant). The two `authResponse` 200s and healthz are untouched.

- [ ] **Step 1: Update the error tests — all five, not just one.** Besides the token-expired test (`handler_test.go:135-150` → expect 401, `code=="unauthenticated"`, `reason=="sso_token_expired"` via `errtest`), update: `:166` (invalid SSO token → 401, `reason=="invalid_sso_token"`), `:182`/`:305` (invalid natsPublicKey → 400 `bad_request`), `:289` (missing account → 400). **Add a 500-path test** — there is none today, and the 500 message changes (see Step 3), so it MUST be covered.
- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Apply the table.** Enrich ctx at entry: `ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"), "account", account)`. Replace each `c.JSON(status, gin.H{"error":...})` with `errhttp.Write(ctx, c, <err>)`. **NOTE the visible behavior change:** the 500 sites (:108,:148) currently return `{"error":"failed to generate NATS token"}`; after Classify they return `{"code":"internal","error":"internal error"}` — the real cause is logged, not sent. This is intended (no internal leak) but is a client-visible message change to document.
- [ ] **Step 4: Verify** healthz (`:199`), the CORS-preflight 204 (`middleware.go:39`), and the two success `c.JSON(http.StatusOK, authResponse{...})` paths are untouched.
- [ ] **Step 5: Run + docs + commit.** Docs (`docs/client-api.md:165-169`): rewrite every auth error row to the envelope shape — 401 rows show `"code":"unauthenticated"` + the right `reason`; 400 rows `"code":"bad_request"`; the 500 row's body changes from `"failed to generate NATS token"` to `{"code":"internal","error":"internal error"}` (currently documented as the old message at :169 — must be edited).
```bash
make test SERVICE=auth-service
git add auth-service/ docs/client-api.md
git commit -m "refactor(auth-service): adopt errcode HTTP envelopes"
```

> **PM-gate fallback:** if PM rejects the `unauthenticated`/401 category, the :92/:96 rows fold to `errcode.Forbidden(...)` (403) with the same reasons, and `codes_auth.go` + the errhttp test must use 403. The spec carries no separate 403 mapping table — derive it from this one by swapping Unauthenticated→Forbidden if the gate fails.

---

## Chapter 17 — Cleanup: delete shims and legacy helpers

Now that all callers use `errcode`, remove the natsrouter shims, `model.ErrorResponse`, and the legacy `natsutil` helpers.

### Task 17.1: Delete natsrouter `RouteError`/`Err*` shims

**Files:** Modify/delete `pkg/natsrouter/errors.go`, `pkg/natsrouter/context.go` (`ReplyRouteError`).

- [ ] **Step 1:** `grep -rn "natsrouter.Err\|RouteError\|ReplyRouteError\|natsrouter.Code" --include="*.go" .` → expect only `pkg/natsrouter/errors.go`, `context.go`, and natsrouter's own tests/examples. If any service still references them, migrate it now (it was missed).
- [ ] **Step 2:** Delete the shim contents of `errors.go` (the whole `RouteError` alias, constructors, `Code*` consts) and `ReplyRouteError`. Update `example_test.go`/`router_test.go` to construct errors via `errcode` directly.
- [ ] **Step 3:** `go build ./... && go test ./pkg/natsrouter/ -v` → PASS.
- [ ] **Step 4:** Commit — `git add pkg/natsrouter/ && git commit -m "refactor(natsrouter): remove RouteError shims"`

### Task 17.2: Retire `model.ErrorResponse` and legacy `natsutil` helpers

**Files:** Modify `pkg/model/error.go`, `pkg/natsutil/reply.go`, callers.

- [ ] **Step 1:** `grep -rn "model.ErrorResponse\|MarshalError\|MarshalErrorWithCode\|natsutil.ReplyError\|TryParseError" --include="*.go" .` (exclude tests last). Confirm all production callers are migrated (`fetcher_history.go`, `memberlist_client.go` already moved to `errcode.Parse` in Ch 13/14).
- [ ] **Step 2:** Delete `TryParseError`, `MarshalError`, `MarshalErrorWithCode`, `ReplyError` from `pkg/natsutil/reply.go` (keep `MarshalResponse`/`ReplyJSON`). Delete `model.ErrorResponse` (or, if any non-migrated test still needs it, migrate that test first). Remove `RoomID` reference sites.
- [ ] **Step 3:** `go build ./... && make test` → PASS.
- [ ] **Step 4:** Commit — `git add -A && git commit -m "refactor: retire model.ErrorResponse and legacy natsutil error helpers"`

---

## Chapter 18 — semgrep, docs, final verification (backend)

> **Frontend cutover split out:** the TypeScript work, browser verification, and the breaking DM-exists co-release coupling now live in Chapter 19 as a separate release task — different toolchain (npm), different release gate. Ch 18 covers only the Go/lint/docs side; Ch 19 ships with the frontend release.

### Task 18.1: semgrep rules

**Files:** Create `.semgrep/errcode.yml`; Modify `Makefile:44`.

- [ ] **Step 1: Write rules**
```yaml
rules:
  - id: errcode-no-reason-literal-outside-catalog
    languages: [go]
    severity: ERROR
    message: >
      Declare Reason codes as typed constants in pkg/errcode/codes_<service>.go,
      not inline. Use an existing errcode.Reason constant.
    paths:
      exclude:
        - "pkg/errcode/codes_*.go"
        - "**/*_test.go"
    patterns:
      - pattern: errcode.Reason("...")

  - id: errcode-withcause-must-not-wrap-errcode
    languages: [go]
    severity: ERROR
    message: >
      WithCause must wrap a raw error, never another errcode error. Propagate a
      typed error with `return err` or `fmt.Errorf("...: %w", err)`.
    patterns:
      - pattern: errcode.WithCause(errcode.$F(...))

  - id: errcode-no-multi-wrap-errcode
    languages: [go]
    severity: ERROR
    message: >
      Multiple %w verbs can place two errcode errors in one chain, defeating the
      "one *Error per chain" invariant (Classify picks the first). Use a single %w.
    patterns:
      - pattern-regex: 'fmt\.Errorf\([^)]*%w[^)]*%w'

  - id: errcode-prefer-named-constructor
    languages: [go]
    severity: WARNING
    message: >
      Prefer the named constructor (errcode.NotFound(msg)) over
      errcode.New(errcode.CodeX, msg) for a literal category. Reserve New for a
      category chosen at runtime.
    paths:
      exclude:
        - "pkg/errcode/**"
        - "**/*_test.go"
    patterns:
      - pattern: errcode.New(errcode.$CODE, ...)
      - metavariable-regex:
          metavariable: $CODE
          regex: '^Code[A-Z].*'
```

- [ ] **Step 2: Wire into Makefile** — append `--config=.semgrep/errcode.yml` to `SEMGREP_FLAGS` (line ~44).
- [ ] **Step 3: Verify each rule** — plant, run `make sast-semgrep`, confirm the expected rule fires, remove the plant, confirm clean. Cover: (a) `_ = errcode.Reason("oops")` in a non-catalog file → `errcode-no-reason-literal-outside-catalog`; (b) `errcode.WithCause(errcode.NotFound("x"))` → `errcode-withcause-must-not-wrap-errcode`; (c) `fmt.Errorf("%w %w", a, b)` → `errcode-no-multi-wrap-errcode`; (d) `errcode.New(errcode.CodeNotFound, "x")` → `errcode-prefer-named-constructor` (WARNING). Confirm the real codebase is clean against all four.
- [ ] **Step 4: Commit** — `git add .semgrep/errcode.yml Makefile && git commit -m "ci(semgrep): enforce Reason location, WithCause + single-%w invariants, named constructors"`

### Task 18.2: docs/client-api.md per-service envelope rows (consolidated docs pass)

The per-service migration chapters intentionally deferred the `docs/client-api.md` updates to a single pass here, so the doc edits are coherent across endpoints. CLAUDE.md requires `docs/client-api.md` updates in the same PR as client-facing handler changes — this pass discharges that obligation for Chapters 11–16.

- [ ] **Step 1: Inventory the deferred rows.** For each migrated service, list the affected endpoints and what the envelope shape now is. Per-service notes (cross-check against the per-service audit findings):
  - **history-service:** add a "Possible errors" table per endpoint (history/next/surrounding/get/edit/delete/thread/thread.parent) with `code` (the generic) and `when`. No reasons today.
  - **search-service:** error tables for search endpoints; `code` only (codes_search.go is the empty placeholder, no reasons).
  - **mock-user-service:** the `checkSite` 404 envelope.
  - **message-gatekeeper:** the doc table around `docs/client-api.md:1886-1894` — fill the `code` column (currently `—`) for every now-`bad_request`/`forbidden`/`not_found` row, and add `reason` for the two forbidden cases (`large_room_post_restricted`, `not_subscribed`).
  - **room-service:** rewrite the DM-exists block (~`docs/client-api.md:235-238`) from the old documented-as-success-error `{"error":"dm already exists","roomId":…}` to the new success shape `{"status":"exists","roomId":…}`; add a changelog entry noting the semantics flip and the **Ch 19 co-release coupling**. For other room-service endpoints, fill in code + reason where applicable (RoomNotMember/RoomNotOwner/RoomLastOwnerCannotLeave/RoomBotInChannel/RoomBotNotAvailable/RoomMaxSizeReached).
  - **room-worker:** AsyncJobResult schema table (~`docs/client-api.md:338`) gains `code`/`reason` rows; example JSON refreshed. §5 (~`:2045-2057`) currently says "Absent for plain `natsutil.ReplyError`" — update: sync-DM reply now emits `code`. Update the create.dm error section accordingly.
  - **auth-service:** `docs/client-api.md:165-169` — rewrite the 4 auth error rows to envelope shape (401 rows show `"code":"unauthenticated"` + the right `reason`; 400 rows `"code":"bad_request"`); the 500 row body changes from `{"error":"failed to generate NATS token"}` to `{"code":"internal","error":"internal error"}`.

- [ ] **Step 2: Apply the edits** in one pass through `docs/client-api.md`. Add a top-of-file changelog entry summarizing: envelope shape is now `{error, code, reason?, metadata?}`; the DM-exists contract flip (co-releases with the frontend in Ch 19); the 500 message homogenization to `"internal error"`.

- [ ] **Step 3: Commit** — `git add docs/client-api.md && git commit -m "docs(client-api): refresh error envelopes for the errcode migration"`

### Task 18.3: Repo-wide gates + error-handling guide

- [ ] **Step 1:** `go build ./...` → clean.
- [ ] **Step 2:** `make lint` → clean.
- [ ] **Step 3:** `make test` → all unit tests pass.
- [ ] **Step 4:** `make sast` → clean (incl. errcode rules).
- [ ] **Step 5:** `grep -rn "sanitizeError\|RouteError\|codedError\|MarshalErrorWithCode\|model.ErrorResponse\|TryParseError" --include="*.go" .` → no production references.
- [ ] **Step 6:** Write `docs/error-handling.md` (envelope, categories + HTTP map incl. the 503-vs-429 note + the new `too_many_requests`/429 category, how to add a Reason, the wrapping invariant + allowed/forbidden table mirroring `doc.go`, the logging contract incl. `Context.WithLogValues` vs `errcode.WithLogValues`, the semgrep rules). Link from `CLAUDE.md` Section 3 "Error Handling".
- [ ] **Step 7:** Commit — `git add docs/error-handling.md CLAUDE.md && git commit -m "docs: add repo-wide error-handling guide"`
- [ ] **Step 8:** Push — `git push -u origin claude/sharp-hopper-qzm6W` (retry with backoff on network errors).

### Task 18.4: Retire the last stale references + adjacent worker bugs

Final repo-wide consistency sweep. A focused audit (one reviewer per non-migrated service + a cross-cutting `pkg/` scan) confirmed the 5 remaining JetStream-only worker services (`broadcast-`, `message-`, `notification-`, `inbox-`, `search-sync-worker`) have NO client-facing error surface — they correctly stay `pkg/errcode`-free. But 4 doc sites still reference deleted symbols, and 3 adjacent worker bugs were surfaced (out of scope for the migration, but small enough to fold into the same PR).

- [ ] **Step 1: Retire the 4 stale doc references.** Each currently points at deleted symbols; fix in place.
  - `CLAUDE.md:224` — bullet still reads "Use `natsutil.ReplyJSON` for success responses, `natsutil.ReplyError` for errors". Replace the second half: "use `errnats.Reply` for errors (typed errcode envelope; see `docs/error-handling.md`)".
  - `docs/client-api.md:1806` — "…per `pkg/model.ErrorResponse` (see §5)" — `ErrorResponse` is deleted; rewrite to point at §6 (Error envelope reference) and drop the type reference.
  - `docs/superpowers/spec.md:233` — `**ErrorResponse**: error (string)` type entry — `ErrorResponse` is deleted; remove or replace with a note that error envelopes are owned by `pkg/errcode` (see `docs/error-handling.md`).
  - `docs/superpowers/spec.md:439` — package table row `pkg/natsutil | ReplyJSON, ReplyError, MarshalResponse, MarshalError, HeaderCarrier` — `ReplyError`/`MarshalError` deleted; trim to the surviving helpers and add a `pkg/errcode` row referencing the canonical owner.

> **Historical implementation plans** under `docs/superpowers/plans/2026-04-*`, `2026-05-07-*`, `2026-05-13-*` etc. still mention `RouteError`/`model.ErrorResponse`/`natsutil.ReplyError` — these are point-in-time SNAPSHOT records from earlier PRs (the cross-cutting reviewer explicitly flagged them as "out of scope; acceptable to leave"). Do NOT touch them; they document state at the time the prior plan landed.

- [ ] **Step 2: Adjacent worker bug fixes** (flagged during the audit; small enough to bundle):
  - `notification-worker/main.go:48,54` — `mongoMemberLookup.ListSubscriptions` returns bare `err` twice. CLAUDE.md §3 violation. Wrap with `fmt.Errorf("find subscriptions for room %s: %w", roomID, err)` and `fmt.Errorf("decode subscriptions: %w", err)` respectively.
  - `inbox-worker/handler.go:187` — `return fmt.Errorf("role_updated event has empty roles")` on a permanently-malformed event causes infinite NAK redelivery of a poison message. Change to `slog.Warn(...) + return nil` (silent-drop pattern, consistent with the `default:` branch at `handler.go:73`).
  - `notification-worker/handler.go:67` — `slog.Error("publish notification failed", "error", err, "account", subs[i].User.Account)` logs the recipient account (PII-adjacent). Operators need the identifier to debug "why didn't user X get a notification", so don't drop it — add a one-line comment acknowledging the trade-off so the choice is explicit.

- [ ] **Step 3: Verify** — `go build ./...` clean; `go test -race ./notification-worker/ ./inbox-worker/` green; `golangci-lint` clean.

- [ ] **Step 4: Repo-wide grep gate** (extends Task 18.3 Step 5 to docs):
  ```bash
  grep -rn "natsutil\.\(ReplyError\|MarshalError\|MarshalErrorWithCode\|TryParseError\)\|model\.ErrorResponse\|natsrouter\.\(RouteError\|Err[A-Z]\|Code[A-Z]\|ReplyRouteError\)" \
    --include="*.md" --include="*.go" \
    docs/ CLAUDE.md pkg/ auth-service/ history-service/ search-service/ mock-user-service/ \
    message-gatekeeper/ room-service/ room-worker/ broadcast-worker/ message-worker/ \
    notification-worker/ inbox-worker/ search-sync-worker/
  ```
  Expect: zero hits outside (a) the `docs/error-handling.md` "Migration history" tombstone list, (b) the `pkg/natsutil/reply.go` package-doc tombstone comment, and (c) the historical implementation plans under `docs/superpowers/plans/2026-04-*`/`2026-05-07-*`/`2026-05-13-*`/`2026-03-*`/`2026-04-*`.

- [ ] **Step 5: Commit** — `git add CLAUDE.md docs/client-api.md docs/superpowers/spec.md notification-worker/ inbox-worker/ && git commit -m "docs+chore: retire last stale references; fix notification/inbox worker bugs"`
- [ ] **Step 6: Push.**

---

## Chapter 19 — Follow-up: Frontend Cutover (separate release task)

> **Why split from Ch 18:** different toolchain (TypeScript / npm vs Go), and it ships under a **release coupling** — the room-service DM-exists reply has flipped from `{error,"dm already exists",roomId}` to `{status:"exists",roomId}` (a contract change the old frontend would mishandle), so this chapter's commit MUST land in the same release as the backend. Until it ships, the backend is paused at the gate (or the frontend rolls out FIRST so it tolerates both shapes — see Step 0).

**Pre-merge gate (must be checked before merging the backend release):**
- [ ] **Step 0: Confirm rollout order.**
  - **Preferred:** ship frontend + backend in the same release; deploy frontend first (it already tolerates the new shape — backward-compatible by Steps 2-3), then deploy room-service.
  - **Fallback:** if co-release is impossible, the room-service DM-exists change must be reverted behind a temporary flag emitting the legacy shape until the frontend ships. STOP and ask if you reach this case.

**Files:** the canonical seams (verified by a focused frontend-section review):
- `chat-frontend/src/api/_transport/asyncJob.ts` — `SyncReplyEnvelope`/`AsyncReplyEnvelope` types (~lines 88-99); the `AsyncJobError` class (~40-48); the sync-envelope error path (~169-177); the `'operation failed'` fallback (~204).
- **`chat-frontend/src/context/NatsContext/NatsContext.jsx`** — **the CANONICAL sync error decoder** (`NatsContext.request`, ~line 92: `if (parsed.error) throw new Error(parsed.error)`). Every sync RPC (search, member.list, getRoom, etc.) flows through here; without rewriting this, NO sync error sees `reason`. ALSO the auth HTTP error path (~:51-53 reading `errBody.error`) for the new `errhttp.Write` envelope (Ch 16).
- **`chat-frontend/src/lib/constants.js`** — the `isDMExistsReply` predicate (~line 21: `reply.error === 'dm already exists'`). MUST be rewritten or DM-exists silently 30s-timeouts after the contract flip.
- `chat-frontend/src/api/types.ts` — the TS mirror of `pkg/model.AsyncJobResult` (~278-282); strict mirror rule requires adding `code?` / `reason?` fields.
- `chat-frontend/src/api/createRoom/index.ts` + `src/components/MainApp/Sidebar/CreateRoomDialog/CreateRoomDialog.jsx` — the create-DM call site + dialog driver.
- `chat-frontend/src/components/MainApp/ChatPage/ManageMembersDialog/{MemberRoster/MemberRoster.jsx, AddMembersForm/AddMembersForm.jsx}` — member-management surfaces currently rendering raw error strings.
- Tests: `asyncJob.test.js`, `CreateRoomDialog.test.jsx` (currently substring-matches `/exceeds maximum capacity/i` — must move to `reason === 'max_room_size_reached'`).

- [ ] **Step 1: Enumerate seams + reason inventory.** `grep -rn "\.code\|\.reason\|\.error\|AsyncError\|ErrorResponse\|dm already exists\|roomId" chat-frontend/src/` (whole `src/`, not just `api/`). Build an inventory mapping each emitted Reason to its UI driver: `max_room_size_reached`→CreateRoomDialog; `sso_token_expired`/`invalid_sso_token`→redirect to re-login (currently falls through ErrorBoundary); `not_subscribed`→send/edit "join the room first" copy; `not_room_member`/`not_room_owner`/`last_owner_cannot_leave`→member-management dialogs (replace raw strings); `large_room_post_restricted`→send-failed toast; generic `forbidden`/`not_found`/`bad_request`→default copy. Anything in this inventory without an explicit step below is a gap to close in Step 6.

- [ ] **Step 2: Extend the transport types + rewrite both decoders.**
  - Extend `SyncReplyEnvelope`/`AsyncReplyEnvelope` (`asyncJob.ts:88-99`) with `code?: string`, `reason?: string`, `metadata?: Record<string,string>`.
  - Extend `AsyncJobError` (`asyncJob.ts:40-48`) with `code?` and `reason?` so consumers can branch without re-parsing `.message`.
  - Rewrite `NatsContext.request` (`NatsContext.jsx:~92`): instead of throwing `new Error(parsed.error)`, throw a structured `RequestError`/`AsyncJobError` carrying `{message, code, reason, metadata}`. Backward-compatible: an `error`-only legacy payload still throws (with `code=undefined`), so the frontend can deploy first.
  - Rewrite the auth HTTP error path in `NatsContext.jsx:~51-53` to read `errBody.code`/`errBody.reason` and surface `sso_token_expired`/`invalid_sso_token` as the re-login redirect, not a raw login-UI string.
  - UI branches must use `reason ?? code` for trigger logic; keep generic copy keyed on `code`.

- [ ] **Step 3: Fix DM-exists (CRITICAL) — `isDMExistsReply` accepts BOTH shapes.** In `src/lib/constants.js:~21`, rewrite the predicate to:
  ```js
  export const isDMExistsReply = (reply) =>
    (reply?.status === 'exists' && reply?.roomId) ||         // new backend
    (reply?.error === 'dm already exists' && reply?.roomId); // legacy backend during rollout
  ```
  Without this, the new `{status:"exists", roomId}` falls through to the sync error branch (which also doesn't match since there's no `.error`), and the request hangs until the 30s `AsyncTimeout`. The create-DM call site already routes through `isDMExistsReply`; no per-call changes needed once the predicate is fixed. Remove the legacy branch in a follow-up release once the backend is everywhere.

- [ ] **Step 4: AsyncJobResult — mirror the Go fields in TS AND decode them.** Update `chat-frontend/src/api/types.ts` (~:278-282) to add `code?: string` and `reason?: string` to the AsyncJobResult mirror (CLAUDE.md strict-mirror rule). Then add `code`/`reason` handling to `asyncJob.ts`'s async decoder so a failed async job throws an `AsyncJobError` carrying them; replace the hard-coded `'operation failed'` fallback (`asyncJob.ts:~204`) with a humanized lookup keyed off `reason` (or `code` when reason is absent). Regenerate any test fixtures (`api/_transport/__fixtures__/*` if applicable) per the frontend CLAUDE.md fixture rule.

- [ ] **Step 5: Frontend gates** — actual `chat-frontend/package.json` scripts: `npm run typecheck`, `npm test`, `npm run build`, `npm run smoke`, `npm run smoke:asyncjob`, `npm run smoke:livestack`. Run ALL of them until green — the three `smoke*` scripts exercise the wire contract end-to-end against a real stack and are the **required regression net** for a contract-flip, not optional. Document anything skipped + why. Add explicit unit tests: (a) `isDMExistsReply({status:'exists', roomId:'r1'})===true`; (b) legacy `{error:'dm already exists', roomId:'r1'}` STILL true (rollout window); (c) real-failure `{error:'something else'}` falls through to SyncError; (d) `AsyncJobError` exposes the new `.code`/`.reason` fields.

- [ ] **Step 6: Rewrite reason-driven UX from message substrings to reason matches.** Walk the Step-1 inventory and replace every "english-string-substring" branch with a `reason === '<value>'` branch. Specifically: `CreateRoomDialog.test.jsx:~265` `/exceeds maximum capacity/i` → match `reason === 'max_room_size_reached'`; member-management dialogs rendering raw strings → use mapped copy per reason; auth re-login redirect on `sso_token_expired`/`invalid_sso_token`; send/edit "join the room first" copy on `not_subscribed`. Browser verification (CLAUDE.md UI rule): start the dev server; exercise (a) generic validation, (b) `max_room_size_reached` dialog, (c) DM-exists navigates to the existing room, (d) async-failed `{Code:"forbidden", Reason:"not_room_member"}` surfaces correctly, (e) expired SSO redirects to re-login. If browser unavailable, state so and rely on unit tests + staging.

- [ ] **Step 7: Commit + push** — `git add chat-frontend/ && git commit -m "feat(frontend): consume errcode reason + DM-exists success reply"` then push for review.

- [ ] **Step 8: Release coordination** — PR description records: backend release SHA this targets; rollout order (frontend first); when the legacy DM-exists fallback (Step 3) + legacy sync-error-only path (Step 2) are removed (follow-up release once backend is everywhere). Note: `ErrorBoundary` render-time errors are intentionally left generic; only `formatAsyncJobError`/`NatsContext.request` consumers gain reason-aware copy.

---

## Chapter 20 — Migration consequences (in-PR completion sweep)

> **Scope rule.** This chapter captures EVERY adjustment that exists ONLY because the errcode migration changed how errors are produced, shaped, logged, or consumed. After this PR ships, no future engineer should have to "go back and finish errcode". The one pre-existing bug surfaced during audits that is **NOT** errcode-driven and stays on its backlog is the gatekeeper `fakeJSMsg` Ack/Nak test gap (JetStream consumer semantics). The other three originally-listed items — notification-worker bare-`err` returns, notification-worker account PII log, inbox-worker NAK-forever — were already addressed by Task 18.4 (commit `a619863`).

> **Locked execution policy.** All in-PR tasks (20.1 → 20.8 plus 20.11 → 20.20) execute in a single batch in one focused session — verify after each (build + per-package test + lint), then a single end-of-batch verification gate (Task 20.10) before one consolidated push. 20.9 stays as a planned follow-up release (gated on backend rollout). Task 20.1 takes the "drop sentinels, add Reasons, use `HasReason` for identity matches" approach (the new identity primitive — aligns with the migration's design). Tasks 20.11–20.20 are the branch_review findings folded in (per user decision 2026-06-01: all 3 HIGH+ + all 4 aesthetic + 3 medium items land in this PR).

### Task 20.1: room-service — restore wrapped name in client envelope (REGRESSION FIX)

**The migration broke client UX.** Pre-migration, `sanitizeError`'s substring allowlist passed `fmt.Errorf("user %q: %w", a, errUserNotFound).Error()` verbatim to the client (user saw `user "alice": user not found`). Post-migration, `Classify` walks `errors.As`, finds the sentinel, and returns only `e.Message` (`"user not found"`) — the account/org name is gone. Sites: `room-service/handler.go:758, :785`; `room-service/store_mongo.go:730`. The reviewer's room-service report rated this **medium** but it IS a real client-visible regression caused by this migration.

- [ ] **Step 1: Add reasons** in `pkg/errcode/codes_room.go`:
  ```go
  RoomUserNotFound Reason = "user_not_found"
  RoomInvalidOrg   Reason = "invalid_org"
  ```
  **Also update `pkg/errcode/codes_test.go:8` `allReasons` list manually** — it's a hand-maintained slice (not auto-discovered); the snake-case + uniqueness tests run over it.
- [ ] **Step 2: Emit fresh errcode values at EVERY producer site** (do this **BEFORE** dropping sentinels in Step 3 so the build never breaks). Plan-review found 6 producers, not 3:
  - `room-service/handler.go:151` → `return nil, errcode.NotFound("user not found", errcode.WithReason(errcode.RoomUserNotFound))` (requester-check; no account is in scope to format into the message — the bare reason carries the intent).
  - `room-service/handler.go:223` → same shape for the counterpart-check.
  - `room-service/handler.go:404` (the re-emit after the `errors.Is` at `:403`) → `errcode.BadRequest("invalid org", errcode.WithReason(errcode.RoomInvalidOrg))`.
  - `room-service/handler.go:758` → `errcode.NotFound(fmt.Sprintf("user %q not found", a), errcode.WithReason(errcode.RoomUserNotFound))`.
  - `room-service/handler.go:785` → `errcode.BadRequest(fmt.Sprintf("invalid org %q", id), errcode.WithReason(errcode.RoomInvalidOrg))`.
  - `room-service/store_mongo.go:730` → `errcode.BadRequest(fmt.Sprintf("list org members for %q", orgID), errcode.WithReason(errcode.RoomInvalidOrg))`.
- [ ] **Step 3: Drop the package-level sentinels** in `room-service/helper.go:26,45` — `errInvalidOrg`, `errUserNotFound`. After Step 2 they have no producer; this step makes them disappear.
- [ ] **Step 4: Convert identity matches to reason matches.** Production: `room-service/handler.go:403` does `errors.Is(err, errInvalidOrg)` today — rewrite to `errcode.HasReason(err, errcode.RoomInvalidOrg)`. Tests (the plan previously listed only `:2262` + 2 integration sites — that's incomplete):
  - `room-service/handler_test.go:1121, 1130, 1149, 1227, 1236` — `wantErrSentinel: errInvalidOrg/errUserNotFound` table-cells (asserted via `errors.Is` at `:1198, :1275`). Rename the column to `wantReason errcode.Reason` (keep a separate `wantStoreFailure` column if `errStoreFailure` is still in play).
  - `room-service/handler_test.go:1825, 1828, 1830` — `wantErrSentinel: errInvalidOrg/errUserNotFound`, plus the mock-return at `:1828` (`store.EXPECT().ListOrgMembers(...).Return(nil, errInvalidOrg)`) needs to call the same fresh constructor as production.
  - `room-service/handler_test.go:2262` (the single previously-listed site).
  - `room-service/helper_test.go:95, 108, 139` — identity assertions over the sentinel values; rewrite or delete (the symbols vanish in Step 3).
  - `room-service/integration_test.go:805, 820` — `errors.Is` over the sentinels → `errcode.HasReason`.
- [ ] **Step 5: Refresh doc comments** that reference the dropped sentinels:
  - `room-service/store.go:66` — "returns a `RoomInvalidOrg`-reason errcode" (was: "returns `errInvalidOrg`").
  - `room-service/store_mongo.go:705` — same.
- [ ] **Step 6: Docs.**
  - `docs/client-api.md` §6 reason catalog — add `user_not_found` (not_found, room-service) and `invalid_org` (bad_request, room-service).
  - `chat-frontend/CLAUDE.md:119-122` reason-catalog list — add the two new reasons under the room-service bullet so other frontend Claudes can branch on them.
- [ ] **Step 7: Verify** `make test SERVICE=room-service`, `make test SERVICE=pkg/errcode`, `golangci-lint run ./room-service/... ./pkg/errcode/...` all clean.

### Task 20.2: `errnats.Reply` + `errnats.ReplyQuiet` direct unit tests

`pkg/errcode/errnats/reply.go:43, :50` are exported but 0% direct coverage — only indirectly exercised via `pkg/natsrouter/router_test.go`. CLAUDE.md §4: "Every exported function in `pkg/` must have corresponding test cases".

**Plan-review finding:** the original "fake `*nats.Msg`" approach **does not work** — `nats.Msg` is a concrete struct (not an interface) and `Respond(data)` requires `m.Sub` to be a non-nil `*nats.Subscription` (also concrete). You cannot subclass the struct or interpose `Respond`. The fix: use the same in-memory NATS server pattern as `pkg/natsrouter/router_test.go` (`natsserver.RunRandClientPortServer`), subscribe to a reply subject, and capture the published bytes.

- [ ] **Step 1: Test scaffolding** in `pkg/errcode/errnats/reply_test.go`:
  - Reuse the `startTestNATS(t *testing.T) *nats.Conn` helper pattern from `pkg/natsrouter/helpers_test.go` (or duplicate it locally; if duplicated, file an internal follow-up note to extract to `pkg/testutil`). Spin up the server in `t.Cleanup`.
  - Helper: `requestAndCaptureReply(t, nc, replyTo, fn func(*nats.Msg))` — open a `nats.Sub` on `replyTo`, call `fn(msg)` with a real `*nats.Msg` whose `Reply` is set, capture the responded bytes.
- [ ] **Step 2: Cases.**
  - `TestReply_RespondsWithEnvelopeAndLogsOnce` — pass an `errcode.Forbidden("x", WithReason(RoomNotMember))`, ctx with a capturing `slog.Handler` (JSON to `bytes.Buffer`); assert: (a) captured reply bytes decode to `{code:"forbidden", reason:"not_room_member", error:"x"}`, (b) exactly one `"request failed"` log line at `INFO` level.
  - `TestReply_LogsAtErrorLevelOnInternal` — pass `errcode.Internal("x")`; assert the log line is at `ERROR` level (category-aware level pin).
  - `TestReply_UnknownErrorCollapsesToInternal` — pass `errors.New("mongo down")`; assert wire bytes carry `code:"internal", error:"internal error"` and the raw cause appears in the LOG but NOT the wire.
  - `TestReplyQuiet_RespondsButEmitsNoClassifyLine` — pass an `errcode.Unavailable(...)`; assert envelope sent, zero `"request failed"` lines (`ReplyQuiet` is for already-logged paths).
  - `TestReply_LogsTransportFailure` — drop the subscriber (subscribe + immediately `Drain()`), call `Reply`, assert the `"error reply failed"` operational slog line fires when `msg.Respond` errors.
- [ ] **Step 3:** verify `go test -race -cover ./pkg/errcode/errnats/` shows ≥80% line coverage on `reply.go` (the existing `Marshal`/`MarshalQuiet` tests already pad some of it; these add the `Reply`/`ReplyQuiet` paths).

### Task 20.3: `errtest` negative-branch coverage

`pkg/errcode/errtest/assert.go` lines 16, 25, 33 (`t.Fatalf` failure branches) are uncovered (66.7%). The migration introduced `errtest`; close the gap so future regressions in the failure-message format are caught.

- [ ] Add tests in `pkg/errcode/errtest/assert_test.go` using a `testing.T` capture (a thin `mockT` that records `Fatalf` calls — pattern used in stdlib `testing/iotest`):
  - `TestDecode_FailsOnNonEnvelope` — non-error JSON triggers `t.Fatalf` with the documented message.
  - `TestAssertCode_FailsOnMismatch` — wrong code triggers `t.Fatalf`.
  - `TestAssertReason_FailsOnMismatch` — wrong reason triggers `t.Fatalf`.

### Task 20.4: `request_id` slog-key standardization

Migration's `WithLogValues` introduced `"request_id"` (snake_case, slog-canonical) as the convention. Pre-existing call sites still use `"requestID"` (camelCase) at multiple spots; same JSON log now mixes both styles and observability dashboards keyed on `request_id` miss the legacy emitters.

**Plan-review correction:** the original "enumerate workers" framing was wrong. Verified scope:
- **`pkg/natsrouter/middleware.go:44`** — THE central request-attr emitter; the migration's `WithLogValues` convention applies repo-wide, and skipping the middleware leaves the very emitter that's supposed to be canonical inconsistent.
- **`room-worker/handler.go`** — 8 sites (lines 119, 569, 1051, 1069, 1463, 1675, 1740, 1745).
- **All other worker dirs** (`message-`, `inbox-`, `broadcast-`, `notification-`, `search-sync-`) — ZERO `"requestID"` slog sites; they're already clean.

**CRITICAL distinction — string literal vs Go identifier vs Gin-context-key:** at `pkg/natsrouter/middleware.go:19` there is `const requestIDKey = "requestID"` — this constant is the **Gin context key** (read by `pkg/natsrouter/router_test.go:417, 437, 540, 551, 571` via `c.Get("requestID")` and used by service handlers via `c.Get(requestIDKey)`). Changing it would break every caller. The slog literal at `:44` is a SEPARATE string that happens to share the value. The plan must NOT touch the const or anything reading it; it changes only the slog literal at `:44` to `"request_id"`.

- [ ] **Step 1:** verify the exact scope with one grep — should match the audit (~9 sites total):
  ```bash
  grep -rnE '(slog\.[A-Z][a-zA-Z]+(Context)?\(.*"requestID"|WithLogValues\(.*"requestID")' --include="*.go" .
  ```
  Anything matching `c.Get("requestID")`, `c.Set("requestID", ...)`, `c.MustGet("requestID")`, or `requestIDKey` (the const) is **NOT in scope** — those are Gin context keys.
- [ ] **Step 2: `pkg/natsrouter/middleware.go:44`** — change ONLY the slog literal (`"requestID"` → `"request_id"`). The const at `:19` stays as-is.
- [ ] **Step 3: `room-worker/handler.go`** — replace the 8 `"requestID"` slog literals with `"request_id"`. Identifier `requestID` (Go variable) stays.
- [ ] **Step 4: Tests.**
  - `pkg/natsrouter/router_test.go:417, 437, 540, 551, 571` use `c.Get("requestID")` (Gin context key) — **do NOT change**.
  - Any test that asserts on the slog literal — search `"requestID"` across `*_test.go` after Step 2/3 land and confirm only context-key sites remain.
- [ ] **Step 5:** verify `go build ./...`, the changed packages' tests, and `golangci-lint run ./pkg/natsrouter/... ./room-worker/...`.

### Task 20.5: `errcode.New` code-set validation

`pkg/errcode/options.go` `New(code Code, msg string, opts ...Option)` is exported but doesn't validate `code` is one of the 8 canonical constants. The "named constructors are the only validated entry points" claim in `parse.go` is loose; the only in-tree caller (`room-service/memberlist_client.go:78`) feeds it a `Code` parsed from a remote envelope — a foreign Code would silently pass through.

- [ ] **Step 1:** in `New`, validate the input is in the closed set; **panic with a clear message** if not. This matches the `WithCause` invariant-guard style (Decision 8). Rationale: `New` is the dynamic escape hatch; passing a non-canonical Code is a programmer error that should fail loudly.
- [ ] **Step 2:** add a test asserting the panic (`TestNew_PanicsOnUnknownCategory`).
- [ ] **Step 3: `memberlist_client.go:71-78`** — when the remote `Code` isn't canonical (legacy site during rollout), fall back to `errcode.Internal(ee.Message)` AND log a single `slog.Warn("legacy peer emitted non-canonical errcode", "code", ee.Code, "message", ee.Message)` so SREs can see legacy peers explicitly (instead of silently collapsing). **Update the existing comment block at `:71-74`** to describe the new fallback behavior — do not just delete the comment.
- [ ] **Step 4:** document the runtime contract in two places:
  - `docs/error-handling.md` near where `New` is described (around line 72) — add a sentence: "Passing a non-canonical `Code` to `errcode.New` panics — `New` is for dynamic but well-known categories, not for arbitrary strings."
  - `docs/error-handling.md` §7 (semgrep rules table) — note that the existing `errcode-prefer-named-constructor` WARNING is now backed at runtime by the panic so unknown-Code bugs surface fast.

### Task 20.6: Frontend UI — convert english-text branching to reason branching

The transport now carries `code`/`reason` (Ch 19 done). Several UI sites still substring-match the English error text; convert them so the contract becomes text-agnostic. This is the second half of Ch 19's Step 6 that I deferred — undeferring per the "every change in this PR" scope rule.

- [ ] **Step 1:** enumerate UI sites — `grep -rnE "toMatch\(.*(?:capacity|exists|owner|subscribed|forbidden|exceeds|requires)|err\.message\.(includes|match|startsWith)|setError\(err.*\.message" chat-frontend/src/` (incl. tests). Known sites the plan-review verified: `CreateRoomDialog.jsx:112` + `.test.jsx:254,265`, `LeaveRoomButton.jsx:14` + `.test.jsx:61,65`, `MemberRoster.jsx:61` (raw `err.message`) + `:102, :126` (via `formatAsyncJobError`) + `.test.jsx:266, :352`, `AddMembersForm.jsx:57` + `.test.jsx:93, :98`, plus `MessageActionMenu.jsx:69` and `OidcCallback.jsx:41` (the latter folds into Task 20.7).
- [ ] **Step 2: `CreateRoomDialog`** — branch on `err instanceof AsyncJobError && err.reason === 'max_room_size_reached'`. Surface humanized copy ("This room is at capacity — owners can raise the limit"). **Update `CreateRoomDialog.test.jsx:254`** to construct the mock rejection as `new AsyncJobError('exceeds maximum capacity (50)', 'sync-error', { reason: 'max_room_size_reached' })` instead of `new Error(...)` — otherwise `REASON_COPY[err.reason]` falls back to `err.message` and the assertion at `:265` passes on the legacy text, masking the change. Update `:265` to assert on the humanized reason copy.
- [ ] **Step 3: `LeaveRoomButton`** — same shape; branch on `err.reason === 'last_owner_cannot_leave'`. Surface "You're the last owner — promote someone else first or delete the room." Update `.test.jsx:61` mock to `new AsyncJobError('cannot leave: you are the last owner', 'sync-error', { reason: 'last_owner_cannot_leave' })` and `:65` assertion to the humanized copy.
- [ ] **Step 4: `MemberRoster.jsx`** — three sites:
  - `:61` currently does `setError(err.message)` — swap to `setError(formatAsyncJobError(err))` (consistency with `:102` / `:126`; otherwise the roster-load failure path stays inconsistent and bypasses 20.8's lookup).
  - `:102` and `:126` already use `formatAsyncJobError` — verify they pick up the humanized copy from Task 20.8 once tests' mocks carry `.reason`.
  - `.test.jsx:266, :352` — update the mock rejections to `AsyncJobError` with `.reason: 'not_room_member'` (or 'not_room_owner', whichever matches the scenario); assertions follow the humanized REASON_COPY copy from 20.8.
- [ ] **Step 5: `AddMembersForm.jsx:57`** — already calls `formatAsyncJobError`. Update `.test.jsx:93` mock to `AsyncJobError({ reason: 'not_room_owner' })` and `:98` to assert on the humanized copy.
- [ ] **Step 6: `MessageActionMenu.jsx:69`** (minor uniformity fix) — `setError(err?.message || 'Failed to load read receipts')` → `setError(formatAsyncJobError(err) || 'Failed to load read receipts')`. Read-receipts likely never carry an actionable reason today, but routing through `formatAsyncJobError` keeps the contract uniform.
- [ ] **Step 7:** verify `npm run typecheck && npm test && npm run build` clean. (End-of-batch smoke runs are in Task 20.10.)

### Task 20.7: Frontend — auth re-login redirect on token-expired

`sso_token_expired` / `invalid_sso_token` reasons are now carried into the `AsyncJobError` thrown by `NatsContext.connect` (Ch 19 done). Today they fall through to the surface that catches them with raw text — the migration's enabling change isn't yet realized as UX.

**Plan-review correction:** there is **no App-level catch handler.** `App.jsx` only mounts `<ErrorBoundary>`, which by design catches render errors only (per `chat-frontend/CLAUDE.md`: "boundary does NOT catch event-handler errors"). The real catch sites are the components/contexts that call `useNats()`. There are two trigger surfaces — initial login and mid-session token expiry — and both need handling.

- [ ] **Step 1: Initial-login surface** — `chat-frontend/src/pages/LoginPage/LoginPage.jsx:28` (`handleDevSubmit`) and `:44` (`handleKeycloakLogin`). Both `catch` and just call `setError(err.message)`. Add:
  ```js
  if (err instanceof AsyncJobError &&
      (err.reason === 'sso_token_expired' || err.reason === 'invalid_sso_token')) {
    // Clear any partial session, then redirect to the OIDC sign-in flow
    // (sso) or surface a "session expired, please log in again" prompt (dev).
    clearSession()
    if (mode === 'sso') getOidcManager().signinRedirect()
    return
  }
  setError(formatAsyncJobError(err))
  ```
- [ ] **Step 2: OIDC callback surface** — `chat-frontend/src/pages/OidcCallback/OidcCallback.jsx:41` currently does `setError(err.message || String(err))`. Apply the same `reason`-aware branch (an expired-token error from auth-service during callback should redirect to re-login, not display).
- [ ] **Step 3: Mid-session surface** — `NatsContext.request` (`chat-frontend/src/context/NatsContext/NatsContext.jsx:100`) is the throw site for sync RPCs that hit an expired token mid-session. The cleanest fix is centralizing the redirect in `NatsContext` itself: if a thrown error's `.reason` is `sso_token_expired` / `invalid_sso_token`, trigger the redirect side-effect AND continue to throw (so callers don't see a phantom-success). Alternatively each consumer (e.g. `MemberRoster.jsx:61`) checks; the centralized approach is preferred for a single source of truth.
- [ ] **Step 4: Tests.**
  - Add a `LoginPage.test.jsx` case asserting that an `AsyncJobError({ reason: 'sso_token_expired' })` thrown by `connectToNats` triggers `clearSession()` + redirect (mock the OIDC manager).
  - Same for `invalid_sso_token`.
  - If Step 3 centralizes in `NatsContext`, add a `NatsContext.test.jsx` case mocking a request whose reply carries `reason: 'sso_token_expired'`; assert the redirect side-effect fires.

### Task 20.8: Frontend — `formatAsyncJobError` reason-keyed humanization

`formatAsyncJobError` (`chat-frontend/src/api/_transport/asyncJob.ts:59`) currently returns `err.message` verbatim for `SyncError`/`AsyncError`. Now that `reason` is carried, prefer a humanized copy lookup keyed off reason; fall back to `err.message` for unmapped cases (and bare `Error` callers).

- [ ] **Step 1:** add a `REASON_COPY: Record<string, string>` map next to `formatAsyncJobError`. Seed with the catalog reasons (each from `pkg/errcode/codes_*.go`):
  ```ts
  const REASON_COPY: Record<string, string> = {
    max_room_size_reached: 'This room is at capacity.',
    not_room_member: "You're not a member of this room.",
    not_room_owner: 'Only owners can do that.',
    last_owner_cannot_leave: "You're the last owner — promote someone else first.",
    bot_in_channel: "Bots can't join channels.",
    bot_not_available: "This bot isn't available right now.",
    large_room_post_restricted: 'Only owners and admins can post here.',
    not_subscribed: 'You need to join this room first.',
    // sso_token_expired / invalid_sso_token are intentionally absent — they
    // drive a redirect (Task 20.7), not a user-facing message.
  }
  ```
- [ ] **Step 2:** `formatAsyncJobError` returns `REASON_COPY[err.reason] ?? err.message` for `SyncError`/`AsyncError` (only — wire-level kinds keep their existing copy at lines 69-72 of `asyncJob.ts`).
- [ ] **Step 3: Tests.** The existing tests at `chat-frontend/src/api/_transport/asyncJob.test.js:223-230` ("returns the raw message for SyncError" + "exceeds maximum capacity") stay green **only because** their mock errors construct `new Error(...)` with no `.reason` — the lookup falls back to message text. Plan-review explicitly flagged that this masks the change. Action:
  - Keep the existing two tests as the fall-back-path proof (rename them to `..._fallsBackToMessageWhenNoReason` so the contract is explicit).
  - **Add new test cases** that construct `new AsyncJobError(rawMessage, 'sync-error', { reason: '<catalog>' })` and assert `formatAsyncJobError(err)` returns the humanized REASON_COPY copy. Cover at least: `max_room_size_reached`, `not_room_member`, `not_subscribed`, `large_room_post_restricted` (the highest-traffic reasons).
- [ ] **Step 4:** update `chat-frontend/CLAUDE.md` "Error envelope" section — under the reasons-emitted-today list (around line 119), append a short note that `formatAsyncJobError` is now the reason-keyed lookup so consumers don't need to map themselves. Cross-link to the REASON_COPY constant.

### Task 20.9: Frontend — remove `isDMExistsReply` legacy fallback (FOLLOW-UP RELEASE)

Once the room-service-with-the-flip is deployed everywhere, the legacy `error: 'dm already exists'` branch in `isDMExistsReply` is dead. Plan-review found the original task missed several sites that exercise/document the legacy shape — these all retire together.

- [ ] `chat-frontend/src/lib/constants.js` — drop the legacy `error: ERR_DM_ALREADY_EXISTS && roomId` branch in `isDMExistsReply`; drop the `ERR_DM_ALREADY_EXISTS` constant (and the "Legacy" comment on `:30`).
- [ ] `chat-frontend/src/lib/constants.test.js` — drop the "legacy shape true" case (the new-shape case stays).
- [ ] `chat-frontend/src/api/_transport/asyncJob.test.js:104, :107, :109` — `treatAsSuccess: (reply) => reply.error === 'dm already exists' && !!reply.roomId` uses the legacy predicate inline. Switch to `treatAsSuccess: (reply) => reply.status === 'exists' && !!reply.roomId` (or import `isDMExistsReply`).
- [ ] `chat-frontend/src/api/_transport/asyncJob.ts:217` — code comment references the legacy shape ("DM-exists and similar `200-with-error+roomId` replies…"); update to describe only the new success-envelope shape.
- [ ] `chat-frontend/src/components/MainApp/Sidebar/CreateRoomDialog/CreateRoomDialog.test.jsx:145` — mock sync reply uses `sync: { error: 'dm already exists', roomId: 'r-existing' }`. Switch to `sync: { status: 'exists', roomId: 'r-existing' }`.
- [ ] `chat-frontend/scripts/asyncJob.smoke.mjs:110, :130` — produces / asserts the legacy shape; switch to the new shape.
- [ ] `chat-frontend/scripts/liveStack.smoke.mjs:141, :148-149` — same.
- [ ] `chat-frontend/CLAUDE.md` — drop the legacy-fallback paragraph (the long sentence noting `isDMExistsReply` accepts both shapes during the rollout window). Keep the canonical `{status:"exists", roomId}` description.

**Release gate:** schedule for ONE release AFTER the backend has been deployed to every site. Not for this PR; lives in the plan so it isn't forgotten.

### Task 20.10: End-of-batch verification gate (must pass before push)

Locked execution policy is "one batch → one push". Mirror Ch 18.3's repo-wide gate, updated for the new Ch 20 sites. **Run from a clean checkout of the post-20.1-through-20.8 state.**

- [ ] **Backend gates.**
  - `go build ./...` → clean.
  - `make lint` → clean (no SA1019 from any leftover legacy reference).
  - `make test` → all unit tests pass (`-race`).
  - `make sast` → clean (the 4 errcode semgrep rules + the new 20.5 unknown-Code surface).
- [ ] **Frontend gates.**
  - `cd chat-frontend && npm run typecheck` → clean.
  - `npm test` → green.
  - `npm run build` → succeeds (pre-existing chunk-size warning OK).
  - `npm run smoke && npm run smoke:asyncjob && npm run smoke:livestack` → green. **These are the wire-contract regression net for the reason-driven branching changes in 20.6–20.8** (per Ch 19 reviewer); even though the contract flip itself shipped in Ch 19, the consumer-side reshaping in 20.6–20.8 should re-prove the round-trip.
- [ ] **Repo-wide grep — no leftover legacy references.**
  ```bash
  grep -rnE 'errUserNotFound|errInvalidOrg' --include="*.go" room-service/ pkg/
  # expect: 0 hits (sentinels are deleted)

  grep -rnE '(slog\.[A-Z][a-zA-Z]+(Context)?\(.*"requestID"|WithLogValues\(.*"requestID")' --include="*.go" .
  # expect: 0 hits (slog literals all switched; Gin-context-key sites remain — only the slog literals were in scope)

  grep -rn "fakeJSMsg\|errcode\.Reason(" --include="*.go" .
  # expect: same as the established Ch 18.3 baseline (errtest helpers don't tip the second one)
  ```
- [ ] **Docs consistency.** Confirm the four documents that name the reason catalog all agree:
  - `pkg/errcode/codes_room.go` lists the two new reasons.
  - `docs/client-api.md` §6 reason catalog lists them.
  - `docs/error-handling.md` reason-catalog reference, if any, is accurate.
  - `chat-frontend/CLAUDE.md` "Error envelope" reasons list includes them.
- [ ] **Commit + push.** Single consolidated commit covering 20.1–20.8 + 20.11–20.20 + the gate proof, followed by `git push origin claude/sharp-hopper-qzm6W`. Use a structured commit message that lists each task by number so reviewers can navigate the diff.

### Task 20.11: message-gatekeeper — collapse triple-unmarshal to a single decode (CRITICAL perf)

Branch-review CRITICAL finding (performance lens). `message-gatekeeper/handler.go` currently unmarshals the inbound JetStream message body three times per request: once for routing, once for the validation context, once for the outbox publish. At `MAX_WORKERS=100` on MESSAGES_CANONICAL this triples GC pressure on the hottest path in the system.

- [ ] **Step 1: Identify the three decode sites** — grep `json.Unmarshal` in `message-gatekeeper/handler.go`. Expected: lines ~74 (initial routing decode), ~129 (validation-context decode), ~168 (outbox publish decode). Confirm each decodes the same `model.Message` (or equivalent) struct.
- [ ] **Step 2: Decode once at entry.** At the first decode site, store the decoded struct in a local (or pass through the per-message handler scope). Delete the second and third `json.Unmarshal` calls — refer to the in-scope variable instead.
- [ ] **Step 3: Outbox publish reuse.** The outbox publish site needs the original raw bytes (it republishes), not a re-decode. If the function consumes `[]byte`, pass `msg.Data` directly (already in scope from JetStream); if it consumes the struct, pass the struct decoded in Step 2. Either way, no second `json.Unmarshal` call.
- [ ] **Step 4: Verify** `make build SERVICE=message-gatekeeper` + `make test SERVICE=message-gatekeeper` — no test fixture changes expected (the behavior is unchanged; only the number of allocations drops).

### Task 20.12: WithCause audit — drop payload-shape leaks (HIGH leak)

Branch-review HIGH finding (observability lens). `room-service/handler.go:1353` introduced `WithCause(json.Unmarshal err)` in commit `d8ef62b`. The unmarshal error's text includes a byte-offset and a payload prefix; via `WithCause` that string lands in the server log. The same shape was deliberately dropped from `message-gatekeeper:169` in the same review-fix batch — this is the symmetric regression.

- [ ] **Step 1: Repo-wide audit.** Grep all `WithCause` call sites:
  ```bash
  grep -rnE 'errcode\.With(Cause|cause)\(' --include="*.go" .
  ```
  For each match, verify the wrapped error is NOT one of: `json.Unmarshal` result, `proto.Unmarshal` result, raw `msg.Data` slice, OIDC token string, anything containing user input bytes.
- [ ] **Step 2: Fix `room-service/handler.go:1353`** — drop `errcode.WithCause(err)` from the chain. Keep the typed `errcode.BadRequest("invalid payload", errcode.WithReason(...))`. The Unmarshal error already gets logged by the caller via `slog.Error("decode failed", "err", err)` at a sanitized level — no need to second-channel it through `WithCause`.
- [ ] **Step 3: Any other site found in Step 1** — apply the same surgery. If a site legitimately needs the cause for ops debugging (e.g. wrapping a Mongo driver error), keep it — the audit is about user-input payloads only.
- [ ] **Step 4: Verify** `make test SERVICE=room-service` + `make lint` clean. Confirm via `grep` that no `WithCause(json.Unmarshal` patterns remain.

### Task 20.13: message-gatekeeper — hoist duplicate WithLogValues (HIGH perf)

Branch-review HIGH finding (performance lens). `message-gatekeeper/handler.go:73` calls `errcode.WithLogValues(ctx, ...)` early; `:140` calls it again with the same room/user fields. The second call re-allocates a fresh inner ctx and a new `logValues` map for fields already in scope. Round-2 already removed one redundancy; this is the other.

- [ ] **Step 1: Confirm the two sites.** Grep `WithLogValues` in `message-gatekeeper/handler.go`. Expected: 2 sites (or more — find all). Read each and identify which fields are passed.
- [ ] **Step 2: Hoist into one early call.** Move all field assignments to the first call at :73 (or wherever the earliest scope-fully-known site is). Delete subsequent calls that add only already-present fields.
- [ ] **Step 3: Edge case** — if a downstream call adds a field that's only known later in the flow (e.g. a derived msgID after dedup), keep that one but assert (via grep) it's not re-adding fields from the earlier call.
- [ ] **Step 4: Verify** `make test SERVICE=message-gatekeeper` clean.

### Task 20.14: search-service — pin metrics status-label cardinality (medium observability)

Branch-review MEDIUM finding (observability + per-service lens). `search-service/metrics.go:96-105` status-label cardinality widened from {ok, internal, bad_request, not_found, forbidden, conflict} (5-6 values) to "any non-empty `errcode.Code`" (up to 9). Bounded today, but future Code additions auto-create new series and Prometheus has no allowlist guard.

- [ ] **Step 1: Pin the allowed label set.** In `search-service/metrics.go`, define a package-level `var allowedStatusLabels = map[string]struct{}{ "ok": {}, "bad_request": {}, "not_found": {}, "forbidden": {}, "conflict": {}, "unauthenticated": {}, "too_many_requests": {}, "unavailable": {}, "internal": {} }` (the 8 errcode Codes + "ok").
- [ ] **Step 2: Guard the labeling site.** Before passing the status string to `.WithLabelValues(...)`, check `if _, ok := allowedStatusLabels[status]; !ok { status = "internal" }`. This forces unexpected labels into the "internal" bucket rather than creating a new time series.
- [ ] **Step 3: Refresh doc comments.** The doc comments at `:93-94` currently enumerate only 4 codes — stale. Rewrite the comment to enumerate all 9 allowed values (or reference the `allowedStatusLabels` var).
- [ ] **Step 4: Test.** Add `TestStatusLabel_RejectsUnknown` in `search-service/metrics_test.go` (or extend the existing metrics test) that calls the labeling helper with a synthetic `"made_up_code"` and asserts the counter increments on `"internal"`, not `"made_up_code"`.
- [ ] **Step 5: Verify** `make test SERVICE=search-service` + `make lint` clean.

### Task 20.15: pkg/errcode — consolidate room-worker permanentError marker + add nil-guard (medium)

Branch-review MEDIUM finding (Go lens). `room-worker/handler.go:129` `permanentError` is a single-field marker (`ec *errcode.Error`) with a dereference in `Error()` that has no nil-guard. The marker is room-worker-private but the pattern (explicit permanence for JetStream Nak/Ack discrimination) is general — consolidate into `pkg/errcode` so other workers can reuse it.

- [ ] **Step 1: Create `pkg/errcode/permanent.go`** with:
  ```go
  // PermanentError marks an *Error as non-retryable: the JetStream consumer
  // should Ack (drop) rather than Nak (redeliver). Workers wrap a classified
  // errcode in Permanent() at the call site to make the policy explicit.
  type PermanentError struct{ ec *Error }

  func Permanent(ec *Error) *PermanentError {
      if ec == nil { panic("errcode.Permanent: nil *Error") }
      return &PermanentError{ec: ec}
  }
  func (p *PermanentError) Error() string { return p.ec.Error() }
  func (p *PermanentError) Unwrap() error { return p.ec }
  ```
  The nil-guard panics at construction (not deref) — same invariant style as `WithCause`.
- [ ] **Step 2: Add `IsPermanent(err error) (*Error, bool)`** convenience: `var p *PermanentError; if errors.As(err, &p) { return p.ec, true }; return nil, false`. Tests for both.
- [ ] **Step 3: Tests** in `pkg/errcode/permanent_test.go`:
  - `TestPermanent_PanicsOnNil`
  - `TestPermanent_UnwrapReachesErrcode` (verify `errors.As(p, &*Error)` works)
  - `TestIsPermanent_DetectsWrapper`
  - `TestIsPermanent_FalseOnPlainErrcode`
- [ ] **Step 4: Migrate room-worker.** Replace `room-worker/handler.go`'s local `permanentError` + `permanent()` with `errcode.PermanentError` + `errcode.Permanent`. Update the `errors.As(&pe)` consumer (search the worker for `*permanentError` and `permanent(`) to use `errcode.IsPermanent`. All 21 `permanent(errcode...)` call sites swap to `errcode.Permanent(errcode...)`.
- [ ] **Step 5: Delete the local marker** from `room-worker/handler.go:129` once Step 4 makes it unused. Verify with `grep "permanentError\b\|\bpermanent(" room-worker/`.
- [ ] **Step 6: Verify** `make test SERVICE=room-worker` + `make test SERVICE=pkg/errcode` + `make lint` clean.

### Task 20.16: pkg/errcode — validate non-empty Message at construction (medium)

Branch-review MEDIUM finding (Go lens). `errcode.New(code, msg, opts...)` accepts empty `msg`. The wire payload then carries `"error": ""` — a contract violation (`error` is documented as always-populated, user-safe text). The named constructors (`NotFound`, `Forbidden`, …) all call `New` internally; an empty message at the constructor leaks through.

- [ ] **Step 1: Add the guard** at the top of `New` in `pkg/errcode/options.go`: `if msg == "" { panic("errcode: empty message — every constructor requires user-safe text") }`. Panic, not return-error: same invariant-guard style as the existing `WithCause` panic on `*Error` (Decision 8).
- [ ] **Step 2: Audit existing constructors.** Grep `errcode\.(NotFound|Forbidden|BadRequest|Conflict|Unauthenticated|TooManyRequests|Unavailable|Internal)\(""` — should be zero matches in tree. If any match exists, fix the caller (the panic would crash the service at first request).
- [ ] **Step 3: Test.** `TestNew_PanicsOnEmptyMessage` in `pkg/errcode/options_test.go`.
- [ ] **Step 4: Doc.** `docs/error-handling.md` near the `New` description — add "Every `*Error` must carry a non-empty user-safe message. Passing `""` panics at construction time. This is enforced because the wire envelope's `error` field is documented as always-populated."
- [ ] **Step 5: Verify** `make test SERVICE=pkg/errcode` + `make lint` clean.

### Task 20.17: room-service/helper.go — document sentinel non-mutation contract (aesthetic)

Branch-review LOW finding (Go lens). After Task 20.1 retires `errUserNotFound` / `errInvalidOrg`, the only remaining sentinels in `room-service/helper.go` are package-level singletons. Today's Options return fresh `*Error` values (mutation-safe), but a future option that mutates in place would silently alias state across callers.

- [ ] **Step 1: Add doc-comment** on the var block (after 20.1 leaves the remaining sentinels, e.g. `errStoreFailure`):
  ```go
  // Package-level errcode sentinels. SHARED across all goroutines.
  // Callers MUST NOT mutate; use errors.Is for identity, errcode.HasReason
  // for reason matching, and construct fresh *Error values via the named
  // constructors when callers need a wrapped message or extra metadata.
  ```
- [ ] **Step 2: Verify** `golangci-lint run ./room-service/...` clean (doc-comment-only change; no behavior change).

### Task 20.18: pkg/errcode/parse.go — guard empty error text (aesthetic)

Branch-review LOW finding (Go lens). `Parse` accepts a struct with empty `error` text. Strictly per the wire contract, `error` is always populated server-side. If a malformed envelope arrives, returning a synthetic placeholder is safer than silently round-tripping `""`.

- [ ] **Step 1: Add the guard** in `Parse`: after JSON-decoding, if `env.Error == ""`, set `env.Error = "(no message)"`. This keeps the returned `*Error` invariant-compliant with Task 20.16's New-panics-on-empty rule.
- [ ] **Step 2: Test.** `TestParse_FillsPlaceholderOnEmptyMessage` — assert the returned `*Error.Message == "(no message)"` when the input envelope has `"error": ""`.
- [ ] **Step 3: Doc.** Inline comment on the guard: `// Defensive: if a peer ships an empty error string (contract violation), fill a placeholder so the resulting *Error satisfies the non-empty-message invariant enforced by New (Task 20.16).`
- [ ] **Step 4: Verify** `make test SERVICE=pkg/errcode` clean.

### Task 20.19: pkg/errcode/match.go — delete dead Is shim (aesthetic)

Branch-review MEDIUM finding (Go lens). `*Error.Is(target error) bool` is dead in-tree — `errors.Is(*Error, ...)` is only ever used against sentinels via the `cause` chain, which the default unwrap handles. Custom `Is` increases the maintenance surface (future maintainers must wonder "does Is collapse code+reason or only code?").

- [ ] **Step 1: Verify dead.** `grep -rn 'errors\.Is.*\*errcode\.Error\|errors\.Is(.*&\?errcode\.Error{' --include="*.go" .` — confirm no caller relies on the custom semantic (only sentinel-identity comparisons, handled by default unwrap).
- [ ] **Step 2: Delete the method** in `pkg/errcode/match.go`.
- [ ] **Step 3: Delete any test** in `pkg/errcode/match_test.go` that pinned the custom semantic (the sentinel-identity tests stay — they exercise default unwrap).
- [ ] **Step 4: Verify** `make test SERVICE=pkg/errcode` clean (any test that broke means the method WAS load-bearing — back out the delete and document instead).

### Task 20.20: pkg/errcode/classify.go — reduce cause-string concat allocation (aesthetic perf)

Branch-review MEDIUM finding (performance lens). `Classify` builds the log-line cause via `cause := cause + ": " + e.cause.Error()`. On every classified-error path. Two intermediate strings allocated per call. Replace with separate slog fields — zero string concat, more queryable in log aggregators.

- [ ] **Step 1: Refactor `Classify`** to log the cause as two distinct slog fields:
  ```go
  attrs := []any{"code", string(e.Code), "reason", string(e.Reason), "cause", cause}
  if e.cause != nil {
      attrs = append(attrs, "underlying", e.cause.Error())
  }
  loggerFrom(ctx).Log(ctx, e.logLevel(), "request failed", attrs...)
  ```
  The `cause` field now carries the outer-error's text (`err.Error()` minus any wrapped `*Error.Message`); `underlying` carries the unexported-cause's text. Both are independently queryable.
- [ ] **Step 2: Update tests.** Any existing classify_test that asserts on `cause` field shape needs the assertion split between `cause` and `underlying`. Add a regression test that pins both fields land in the log when `WithCause` was used.
- [ ] **Step 3: Update doc** in `pkg/errcode/doc.go` (or `docs/error-handling.md`) describing the two-field log shape — log aggregators that pivot on `cause` see the same data; those that want the unexported underlying separately query `underlying`.
- [ ] **Step 4: Verify** `make test SERVICE=pkg/errcode` clean. Spot-check one downstream service (e.g. `make test SERVICE=room-service`) — no test should depend on the old single-field shape.

---

## Self-Review Notes

- **Spec coverage:** wire format (Ch 0–1), infra→internal (Ch 4), remove sanitizeError (Ch 14), extract to pkg (Ch 0–9), server log before reply (Ch 4 + per-service ctx enrichment), specific-vs-general via Code+Reason (Ch 0, 3, 7). Self-found items: natsrouter decoupling + cycle-safe seam (Ch 10), full caller sweep incl. `params.go`/`metrics.go`/`fetcher_history.go`/`memberlist_client.go` (Ch 10/12/13/14), AsyncJobResult (Ch 15), explicit permanence (Ch 15), auth HTTP (Ch 16), shim-then-delete ordering (Ch 10/17), legacy cleanup (Ch 17), semgrep (Ch 18), consolidated docs/client-api.md pass (Ch 18.2), error-handling guide (Ch 18.3), frontend cutover split out (Ch 19 — separate release task, gated on co-release with the room-service DM-exists flip).
- **Type consistency:** `Code`/`Reason`, `New`, named constructors (no `*f`), `WithReason(Reason)`, `WithMetadata`, `WithCause`, `Classify`/`logLevel`, `Parse`, `ReasonOf`/`HasReason`, `errnats.Reply/Marshal/ReplyQuiet/MarshalQuiet`, `errhttp.Write`, `errtest.AssertCode/AssertReason/Decode`, `WithLogger`/`WithLogValues`/`loggerFrom`, `Context.WithLogValues`, `permanent`/`permanentError` used consistently.
- **Confirm-before-execution:** (a) PM gate on `unauthenticated` (Ch 16); (b) frontend test/script names (Ch 18); (c) DM-exists co-release gate (Ch 14.3 Step 7).
- **Review round 1 fixes applied:** ctx-cycle seam, `natsutil.ReplyJSON` for DM-exists, complete caller sweep, shim ordering, explicit permanence, Code/Reason split, constructor naming, Ch 13 contradiction resolved (forbidden+reason), 503/429 noted, AsyncJobResult string fields + omitempty test.
- **Review round 2 fixes applied:** (Critical) natsrouter's own tests + ~40 cross-service `.Code` test asserts migrated in-chapter (Ch 10 Step 1/5); `Context.ReplyError` + `register.go:20` covered (Ch 10 Step 3); `query_rooms.go:74` return-type change (Ch 12 Step 5); one production `metrics.go` break folded into Ch 10 to keep `go build` green; room-worker async `recover()` now a REQUIRED task (Ch 15 Step 6). (High) category-aware `Classify` log level (Ch 4); `*f` constructors removed (Ch 3); CLAUDE.md rule updated in Ch 10 not Ch 18. (Medium) `WithMetadata`/`WithLogValues` trust-boundary documented (Decision 9, doc.go); `errnats.MarshalQuiet`/`ReplyQuiet` for already-logged paths (Ch 8/10); single-`%w` invariant + semgrep rule; `newPermanentAbsent` chain locked + tested (Ch 15); DM-exists co-release gate (Ch 14.3). (Low) `errtest` helper (Ch 5.3); `ReasonOf`/`HasReason` (Ch 5.2); semgrep `prefer-named-constructor`.
- **Review round 3 (per-service exhaustive audits) fixes applied:** (Critical) message-gatekeeper — ~10 inline validation `fmt.Errorf` re-homed so they don't collapse to internal, and the `not_subscribed` reply now returns the sentinel not a fresh error (Ch 13 Step 4); room-service — the `sanitizeError` allowlist's ~14 inline passthrough sites (`"only owners can"`, `"invalid request"`, `"cannot add members"`, `"requester not in room"`, mute-toggle) re-homed at source BEFORE deleting the allowlist (Ch 14 Step 2); room-worker — 21 (not "≈10") `newPermanent` sites enumerated, `errRoomIDCollision`/sync-DM reconcile branch + `processRoleUpdate`'s Nak-forever bug covered (Ch 15 Step 1/1b). (High) gatekeeper test line-range corrected (`TestHandler_marshalErrorReply` 1159-1188 vs real assertion 686-688); room-service `*dmExistsError` routing tests (`integration_test.go:1588`, `handler_test.go:2333/2427/2662`) added (Ch 14.3 Step 5); search-service test count 18 (not ~17) + the `CodeInternal` integration assert (Ch 12 Step 8). (Medium) `wrappedCtx` log-enrichment fold (Ch 14 Step 4); gatekeeper ctx-enrich placed after parse (Ch 13 Step 5); memberlist legacy-remote empty-code rollout note (Ch 14.2); auth 500-message change + missing 500 test + all five error tests + docs rows (Ch 16); per-service docs row specifics. (Low) history test count 16 (not ~22) + mandatory roomID (Ch 11); CreateRoomReply.RoomType confirm (Ch 14.3); errRoomKeyAbsent alert is call-site-driven, framing corrected (Ch 15). mock-user-service: audited clean, no change.

---

## Post-Plan Amendments (implemented after plan was written)

### PA-1 — `Code.Valid()` + `New()` panic on bad code/empty message

`Code.Valid()` added to `pkg/errcode/category.go`. `New()` in `options.go` now panics on a non-canonical `Code` or empty `Message` (programmer errors surfaced at init time rather than producing silent broken envelopes). `Parse` uses `Code.Valid()` and message-emptiness to detect legacy/non-canonical remote envelopes (see PA-4).

### PA-2 — `TooManyRequests` (429) added

Resolved the spec open-item: `CodeTooManyRequests Code = "too_many_requests"` (HTTP 429) and the named constructor `TooManyRequests(msg, opts...)` were added alongside the other 7 categories. `Code.Valid()` covers it. `too_many_requests` = per-caller quota/rate-limit; `unavailable` = server-wide saturation. Both are INFO-level in `logLevel()`.

### PA-3 — Request-ID policy split: `StampRequestID` vs `RequireRequestID`

The uniform "mint-on-missing" approach in the original plan was discovered to break dedup for room-service and room-worker handlers that derive `Nats-Msg-Id` / message IDs from the inbound request ID. Two policies were implemented:

**`natsutil.StampRequestID(ctx, headers, subject) (ctx, id)`** — default; mints on missing, warns on malformed. Applied by all other handlers and the `pkg/natsrouter` RequestID middleware.

**`natsutil.RequireRequestID(ctx, headers, subject) (ctx, id, error)`** — strict; returns `errcode.BadRequest` on missing or malformed. Applied by:
- All room-service NATS handlers via `wrappedCtx(m otelnats.Msg) (context.Context, error)` (signature changed from `context.Context` to `(context.Context, error)`)
- `room-worker.natsServerCreateDM` via `requireDedupRequestID` wrapper

The room-worker JetStream consume loop keeps `StampRequestID` defensively but logs `slog.Error` on a forced mint.

Tests added: `TestRequireRequestID` (pkg/natsutil), `TestWrappedCtx_*` (room-service), `TestRequireDedupRequestID` (room-worker).

Documented in `docs/error-handling.md` §3a.

### PA-4 — Cross-site memberlist: propagate X-Request-ID

`room-service/memberlist_client.go` constructed a bare `nats.Msg` with empty headers, so `X-Request-ID` was never forwarded to the remote site. Since remote room-service uses `RequireRequestID` (strict — PA-3), this caused integration tests to fail with `bad_request`. Fixed by using `natsutil.NewMsg(reqCtx, subject, body)`.

Integration tests `TestRoomsInfoBatchRPC` and `TestAddMembers_TwoSiteEndToEnd` in `room-service/integration_test.go` were updated to stamp valid UUIDs on the request context before calling handlers.

### PA-5 — Legacy remote peer handling in `memberlist_client`

When `errcode.Parse` returns an envelope with a non-canonical `Code` or empty `Message` (old peer), `memberlist_client` now falls back to `errcode.Internal("remote site returned an error")` and emits `slog.Warn("legacy peer emitted non-canonical errcode", ...)` instead of panicking in `errcode.New`. This implements safe mixed-version rollout.

### PA-6 — `errRoomKeyAbsent` (room-service) converted to typed errcode

The `errRoomKeyAbsent` sentinel in `room-service/helper.go` (introduced by main's room-key-fetch RPC feature) was converted from `errors.New(...)` to `errcode.NotFound("room key not available")`. The `handleGetRoomKey` inline `fmt.Errorf("invalid request: %w", err)` was also converted to `errcode.BadRequest("invalid request")`. `natsGetRoomKey` handler updated to use `errnats.Reply` + the new two-return `wrappedCtx`.

Note: room-worker's own `errRoomKeyAbsent = errors.New(...)` is intentionally a raw sentinel — it is wrapped via `errcode.WithCause(errRoomKeyAbsent)` so both `errors.Is` (alert path) and `errors.As` (errcode classification) resolve in the same chain.
