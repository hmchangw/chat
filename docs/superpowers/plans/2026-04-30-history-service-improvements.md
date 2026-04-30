# History-Service Production-Readiness Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply a focused set of code-quality, performance, and operational improvements to `history-service` identified during the production-readiness review, without changing externally-observable behavior.

**Architecture:** All work is scoped to `history-service/` and a small touch in `pkg/cassutil` and `pkg/model/cassandra`. No NATS subject changes, no Cassandra/Mongo schema changes, no API/response shape changes. Improvements are independent tasks committed individually so each lands as a reviewable, revertable change.

**Tech Stack:** Go 1.25, gocql, mongo-driver/v2, NATS, `caarlos0/env`, testcontainers-go, `go.uber.org/mock`, `stretchr/testify`.

**Out of scope (tracked separately):**
- `pkg/natsrouter` worker pool / handler-timeout middleware (separate spec).
- EditMessage LWT (deferred — not implementing per user decision).
- Observability (metrics, request-ID propagation) — deferred.
- HTTP `/healthz` endpoint — deferred.
- Cross-site federation of edits/deletes — out of architectural scope.

---

## Task 1: Cache `structScan` reflection per `reflect.Type`

**Why:** `structScan` (`internal/cassrepo/utils.go:109`) walks every struct field and reads its `cql` tag on every row. For 100-row pages this costs 100 × N_fields tag lookups + map allocations. Caching the column→fieldIndex slice per `reflect.Type` removes the repeated reflection work; only the per-row `Addr()` and the small `map[string]any` allocation remain (still required because `iter.MapScan` mutates map values).

**Files:**
- Modify: `history-service/internal/cassrepo/utils.go` (replace `structScan` body and add cache)
- Modify: `history-service/internal/cassrepo/utils_test.go` (add cache-correctness tests)

- [ ] **Step 1: Write the failing tests**

Append to `history-service/internal/cassrepo/utils_test.go`:

```go
func TestFieldMapFor_BuildsEntriesFromCQLTags(t *testing.T) {
	type sample struct {
		Foo      string `cql:"foo"`
		Bar      int    `cql:"bar"`
		Untagged string
	}
	entries := fieldMapFor(reflect.TypeOf(sample{}))
	require.Len(t, entries, 2)
	assert.Equal(t, "foo", entries[0].name)
	assert.Equal(t, 0, entries[0].index)
	assert.Equal(t, "bar", entries[1].name)
	assert.Equal(t, 1, entries[1].index)
}

func TestFieldMapFor_IgnoresDashTag(t *testing.T) {
	type sample struct {
		Keep string `cql:"keep"`
		Drop string `cql:"-"`
	}
	entries := fieldMapFor(reflect.TypeOf(sample{}))
	require.Len(t, entries, 1)
	assert.Equal(t, "keep", entries[0].name)
}

func TestFieldMapFor_ReturnsCachedSliceOnRepeatCall(t *testing.T) {
	type sample struct {
		Foo string `cql:"foo"`
	}
	rt := reflect.TypeOf(sample{})
	first := fieldMapFor(rt)
	second := fieldMapFor(rt)
	require.NotEmpty(t, first)
	// Same backing array — cache hit, not a fresh build.
	assert.Equal(t, &first[0], &second[0])
}
```

Add `"reflect"` to the test file's imports if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=history-service`
Expected: FAIL — `fieldMapFor` undefined.

- [ ] **Step 3: Replace `structScan` and add the cache**

Replace the existing `structScan` function (and add the cache + helper) in `history-service/internal/cassrepo/utils.go`:

```go
// fieldEntry caches a single struct field's cql tag and index.
type fieldEntry struct {
	name  string
	index int
}

// fieldMapCache memoises the cql-tag -> field-index list per struct type so
// structScan doesn't re-walk the type on every row. Keyed by reflect.Type,
// value type is []fieldEntry.
var fieldMapCache sync.Map

// fieldMapFor returns the cached cql-tag -> field-index list for the given
// struct type, computing and caching it on first request. Untagged fields and
// fields tagged `cql:"-"` are skipped.
func fieldMapFor(rt reflect.Type) []fieldEntry {
	if cached, ok := fieldMapCache.Load(rt); ok {
		return cached.([]fieldEntry)
	}
	entries := make([]fieldEntry, 0, rt.NumField())
	for i := range rt.NumField() {
		field := rt.Field(i)
		tag := field.Tag.Get("cql")
		if tag == "" || tag == "-" {
			continue
		}
		entries = append(entries, fieldEntry{name: tag, index: i})
	}
	actual, _ := fieldMapCache.LoadOrStore(rt, entries)
	return actual.([]fieldEntry)
}

// structScan scans the current row of iter into dest using cql struct tags
// for column-to-field mapping. It mirrors gocql's StructScan API which is not
// present in v1.7.0: fieldMapFor produces (and caches) the column-name ->
// struct-field-index list for dest's type, and per call a fresh
// map[string]interface{} of column-name -> field-pointer is built and passed
// to iter.MapScan.
//
// The per-row map MUST be fresh — iter.MapScan overwrites entries with bare
// values after each call, so a reused map would no longer contain
// field-pointers on the next scan.
//
// Returns true when a row was consumed, false when the iterator is exhausted
// or dest is not a pointer to a struct.
func structScan(iter *gocql.Iter, dest interface{}) bool {
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Struct {
		return false
	}
	rv = rv.Elem()
	entries := fieldMapFor(rv.Type())

	row := make(map[string]interface{}, len(entries))
	for _, e := range entries {
		row[e.name] = rv.Field(e.index).Addr().Interface()
	}
	return iter.MapScan(row)
}
```

Add `"sync"` to the file's imports if not already present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=history-service`
Expected: PASS — all unit tests including the three new ones.

- [ ] **Step 5: Run cassrepo integration tests to confirm no regression**

Run: `make test-integration SERVICE=history-service`
Expected: PASS — Cassandra round-trips still produce correct results.

- [ ] **Step 6: Commit**

```bash
git add history-service/internal/cassrepo/utils.go history-service/internal/cassrepo/utils_test.go
git commit -m "perf(history-service): cache structScan field map per type"
```

---

## Task 2: Migrate remaining `for i := 0; i < n; i++` loops to `for i := range n`

**Why:** Project-wide consistency on the Go 1.22+ idiomatic counted-loop. After Task 1 only three c-style loops remain in `history-service/`, all in integration tests. Converting them now keeps the codebase consistent with the loop in `fieldMapFor` introduced in Task 1.

**Files:**
- Modify: `history-service/internal/cassrepo/messages_by_room_integration_test.go:21`
- Modify: `history-service/internal/cassrepo/thread_messages_integration_test.go:21,100`

- [ ] **Step 1: Verify the exact set of remaining loops**

Run:
```bash
grep -rEn "for [a-zA-Z]+ ?:= ?0; [a-zA-Z]+ ?<" history-service/ | grep -v /mocks/
```
Expected output: exactly the three integration-test loops listed above. If any new ones appear, convert them too.

- [ ] **Step 2: Convert `messages_by_room_integration_test.go:21`**

Replace:
```go
	for i := 0; i < count; i++ {
```
with:
```go
	for i := range count {
```

- [ ] **Step 3: Convert `thread_messages_integration_test.go:21`**

Replace:
```go
	for i := 0; i < count; i++ {
```
with:
```go
	for i := range count {
```

- [ ] **Step 4: Convert `thread_messages_integration_test.go:100`**

Replace:
```go
	for i := 0; i < len(page.Data)-1; i++ {
```
with:
```go
	for i := range len(page.Data) - 1 {
```

- [ ] **Step 5: Run integration tests to verify behavior is unchanged**

Run: `make test-integration SERVICE=history-service`
Expected: PASS — same tests as before, just spelled idiomatically.

- [ ] **Step 6: Re-run the grep to confirm zero remaining matches**

Run:
```bash
grep -rEn "for [a-zA-Z]+ ?:= ?0; [a-zA-Z]+ ?<" history-service/ | grep -v /mocks/
```
Expected output: no matches.

- [ ] **Step 7: Commit**

```bash
git add history-service/internal/cassrepo/messages_by_room_integration_test.go history-service/internal/cassrepo/thread_messages_integration_test.go
git commit -m "style(history-service): use idiomatic for-range counted loops"
```

---

## Task 3: Split `internal/service/utils.go` into focused files

**Why:** `utils.go` is a grab-bag (access checks, redaction, time helpers, mutation authorization). Split into two focused files (`access.go`, `redaction.go`) and absorb the small helpers into the handler file that already uses them. Wraps `SubscriptionRepository` in a tiny `accessChecker` type so the access policy has a name and a clear surface (the "small types" version of the original B+E recommendation).

**Files:**
- Create: `history-service/internal/service/access.go`
- Create: `history-service/internal/service/redaction.go`
- Modify: `history-service/internal/service/service.go` (field rename + constructor)
- Modify: `history-service/internal/service/messages.go` (absorb `parsePageRequest`, `millisToTime`, `derefTime`, `timeMax`; rename `s.getAccessSince(...)` → `s.access.Check(...)`)
- Modify: `history-service/internal/service/threads.go` (rename `s.getAccessSince(...)` → `s.access.Check(...)`)
- Delete: `history-service/internal/service/utils.go`
- Modify: `history-service/internal/service/utils_test.go` → rename file to `access_test.go` (TestCanModify covers `canModify` which now lives in `access.go`)

- [ ] **Step 1: Create `access.go`**

Create `history-service/internal/service/access.go` with this content:

```go
package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// accessChecker enforces room-membership and history-window policy. Wraps a
// SubscriptionRepository so the policy has a single named home rather than
// being inlined into each handler.
type accessChecker struct {
	subs SubscriptionRepository
}

func newAccessChecker(subs SubscriptionRepository) *accessChecker {
	return &accessChecker{subs: subs}
}

// Check returns the historySharedSince lower bound for the user in this room.
// nil means full access; a non-nil time means the user only sees messages at
// or after that timestamp. Returns ErrForbidden when the user is not
// subscribed to the room and ErrInternal when the subscription store fails.
func (a *accessChecker) Check(ctx context.Context, account, roomID string) (*time.Time, error) {
	accessSince, subscribed, err := a.subs.GetHistorySharedSince(ctx, account, roomID)
	if err != nil {
		slog.Error("checking subscription", "error", err, "account", account, "roomID", roomID)
		return nil, natsrouter.ErrInternal("unable to verify room access")
	}
	if !subscribed {
		return nil, natsrouter.ErrForbidden("not subscribed to room")
	}
	return accessSince, nil
}

// findMessage fetches a message by ID and validates room ownership. Returns
// ErrNotFound both for missing messages and for messages that belong to a
// different room (same error to prevent cross-room ID probing).
func (s *HistoryService) findMessage(ctx context.Context, roomID, messageID string) (*models.Message, error) {
	if messageID == "" {
		return nil, natsrouter.ErrBadRequest("messageId is required")
	}
	msg, err := s.msgReader.GetMessageByID(ctx, messageID)
	if err != nil {
		slog.Error("finding message", "error", err, "messageID", messageID)
		return nil, natsrouter.ErrInternal("failed to retrieve message")
	}
	if msg == nil {
		return nil, natsrouter.ErrNotFound("message not found")
	}
	if msg.RoomID != roomID {
		return nil, natsrouter.ErrNotFound("message not found")
	}
	return msg, nil
}

// canModify reports whether account is authorized to edit or soft-delete msg.
// Sender-only authorization: the caller must be the message's original
// sender. Empty account on either side is treated as unauthorized so messages
// with missing sender data cannot match.
func canModify(msg *models.Message, account string) bool {
	if msg == nil {
		return false
	}
	if account == "" {
		return false
	}
	if msg.Sender.Account == "" {
		return false
	}
	return msg.Sender.Account == account
}

// derefTime returns *t when t != nil, otherwise the zero time.
func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// timeMax returns the later of two timestamps; zero values are ignored.
func timeMax(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}
	if a.After(b) {
		return a
	}
	return b
}
```

- [ ] **Step 2: Create `redaction.go`**

Create `history-service/internal/service/redaction.go` with this content:

```go
package service

import (
	"time"

	"github.com/hmchangw/chat/history-service/internal/models"
)

// UnavailableQuoteMsg replaces QuotedParentMessage.Msg when the quoted
// message falls outside the user's access window.
const UnavailableQuoteMsg = "This message is unavailable"

// quoteInaccessible reports whether a quoted message is outside the access
// window. For TShow replies, ThreadParentCreatedAt is checked; nil parent
// time on a TShow quote is treated as inaccessible (conservative — prevents
// leaks via legacy rows that lack the captured parent timestamp).
func quoteInaccessible(m *models.Message, q *models.QuotedParentMessage, accessSince time.Time) bool {
	tshowParentInaccessible := m.TShow && q.ThreadParentID != "" && q.ThreadParentCreatedAt != nil && q.ThreadParentCreatedAt.Before(accessSince)
	legacyTShowMissingParentTime := m.TShow && q.ThreadParentID != "" && q.ThreadParentCreatedAt == nil
	return q.CreatedAt.Before(accessSince) || tshowParentInaccessible || legacyTShowMissingParentTime
}

// redactUnavailableQuote replaces an inaccessible quote on a single message.
func redactUnavailableQuote(m *models.Message, accessSince *time.Time) {
	if m == nil || accessSince == nil {
		return
	}
	q := m.QuotedParentMessage
	if q == nil {
		return
	}
	if quoteInaccessible(m, q, *accessSince) {
		m.QuotedParentMessage = &models.QuotedParentMessage{Msg: UnavailableQuoteMsg}
	}
}

// redactUnavailableQuotes replaces inaccessible quotes across a slice in place.
func redactUnavailableQuotes(msgs []models.Message, accessSince *time.Time) {
	if accessSince == nil {
		return
	}
	for i := range msgs {
		q := msgs[i].QuotedParentMessage
		if q == nil {
			continue
		}
		if quoteInaccessible(&msgs[i], q, *accessSince) {
			msgs[i].QuotedParentMessage = &models.QuotedParentMessage{Msg: UnavailableQuoteMsg}
		}
	}
}
```

- [ ] **Step 3: Update `service.go` to wrap subscriptions in accessChecker**

In `history-service/internal/service/service.go`:

Replace the `HistoryService` struct definition:
```go
// HistoryService handles message history queries and mutations. Transport-agnostic.
type HistoryService struct {
	msgReader     MessageReader
	msgWriter     MessageWriter
	subscriptions SubscriptionRepository
	publisher     EventPublisher
	threadRooms   ThreadRoomRepository
}
```
with:
```go
// HistoryService handles message history queries and mutations. Transport-agnostic.
type HistoryService struct {
	msgReader   MessageReader
	msgWriter   MessageWriter
	access      *accessChecker
	publisher   EventPublisher
	threadRooms ThreadRoomRepository
}
```

Replace the constructor:
```go
func New(msgs MessageRepository, subs SubscriptionRepository, pub EventPublisher, threadRooms ThreadRoomRepository) *HistoryService {
	return &HistoryService{msgReader: msgs, msgWriter: msgs, subscriptions: subs, publisher: pub, threadRooms: threadRooms}
}
```
with:
```go
func New(msgs MessageRepository, subs SubscriptionRepository, pub EventPublisher, threadRooms ThreadRoomRepository) *HistoryService {
	return &HistoryService{
		msgReader:   msgs,
		msgWriter:   msgs,
		access:      newAccessChecker(subs),
		publisher:   pub,
		threadRooms: threadRooms,
	}
}
```

- [ ] **Step 4: Move `parsePageRequest` and `millisToTime` into `messages.go`**

Append to `history-service/internal/service/messages.go` (after the existing handler functions, before EOF):

```go
// millisToTime converts an optional unix-millis timestamp to a UTC time.Time;
// nil yields the zero time.
func millisToTime(millis *int64) time.Time {
	if millis == nil {
		return time.Time{}
	}
	return time.UnixMilli(*millis).UTC()
}

// parsePageRequest validates a cursor + limit and maps decode errors to a
// user-facing ErrBadRequest. The original error is logged for debugging.
func parsePageRequest(cursor string, limit int) (cassrepo.PageRequest, error) {
	q, err := cassrepo.ParsePageRequest(cursor, limit)
	if err != nil {
		slog.Error("invalid pagination cursor", "error", err, "cursor", cursor)
		return cassrepo.PageRequest{}, natsrouter.ErrBadRequest("invalid pagination cursor")
	}
	return q, nil
}
```

- [ ] **Step 5: Replace `s.getAccessSince(...)` calls in `messages.go`**

In `history-service/internal/service/messages.go`, replace every occurrence of `s.getAccessSince(c, account, roomID)` with `s.access.Check(c, account, roomID)`. Six call sites: `LoadHistory`, `LoadNextMessages`, `LoadSurroundingMessages`, `GetMessageByID`, `EditMessage`, `DeleteMessage`.

Run to verify no occurrences remain:
```bash
grep -n "s\.getAccessSince" history-service/internal/service/messages.go
```
Expected: no matches.

- [ ] **Step 6: Replace `s.getAccessSince(...)` calls in `threads.go`**

In `history-service/internal/service/threads.go`, replace both occurrences of `s.getAccessSince(c, account, roomID)` with `s.access.Check(c, account, roomID)` (in `GetThreadMessages` and `GetThreadParentMessages`).

Run to verify:
```bash
grep -n "s\.getAccessSince\|s\.subscriptions" history-service/internal/service/threads.go
```
Expected: no matches.

- [ ] **Step 7: Delete `utils.go`**

```bash
rm history-service/internal/service/utils.go
```

- [ ] **Step 8: Rename `utils_test.go` → `access_test.go`**

```bash
git mv history-service/internal/service/utils_test.go history-service/internal/service/access_test.go
```

The file's only test (`TestCanModify`) now lives in the same file-pair as the function it tests.

- [ ] **Step 9: Run all unit tests**

Run: `make test SERVICE=history-service`
Expected: PASS — all existing tests still green. Compilation must succeed across `messages.go`, `threads.go`, `service.go`, `access.go`, `redaction.go`.

- [ ] **Step 10: Run lint**

Run: `make lint`
Expected: PASS — no `goimports` / `staticcheck` complaints. If unused-import warnings appear in `messages.go` or `threads.go`, remove them.

- [ ] **Step 11: Commit**

```bash
git add history-service/internal/service/
git commit -m "refactor(history-service): split utils.go into access.go and redaction.go"
```

---

## Task 4: Document Cassandra mirror-table consistency model on the writers

**Why:** Edits and deletes touch up to four Cassandra tables sequentially with no cross-partition transaction. A crash between the canonical row update (`messages_by_id`) and the mirror updates leaves mirrors stale. The current behavior (eventual reconvergence on the next mutation) is acceptable, but the trade-off must be visible to anyone reading the writer code. Per the brainstorming decision: doc-comment only, no separate doc file.

**Files:**
- Modify: `history-service/internal/cassrepo/write.go` (extend doc comments on `UpdateMessageContent` and `SoftDeleteMessage`)

- [ ] **Step 1: Replace the doc comment on `UpdateMessageContent`**

In `history-service/internal/cassrepo/write.go`, the function currently has no doc comment. Add this comment immediately above `func (r *Repository) UpdateMessageContent(...)` (around line 82):

```go
// UpdateMessageContent applies an edit across all Cassandra tables that hold
// the message: messages_by_id (canonical), and depending on the message type
// either messages_by_room (top-level) or thread_messages_by_room (thread
// reply); plus pinned_messages_by_room when PinnedAt is set.
//
// Consistency model: messages_by_id is the source of truth. The mirror tables
// are eventually consistent — sequential UPDATEs across separate partitions
// have no atomic guarantee, so a crash mid-write leaves messages_by_id
// updated and one or more mirrors stale. The next successful edit/delete on
// the same message reconverges the mirrors. Readers that hit a stale mirror
// (e.g. LoadHistory via messages_by_room while messages_by_id has been
// updated) will see the pre-edit content for a brief window. This trade-off
// is intentional given Cassandra's lack of multi-partition transactions.
```

- [ ] **Step 2: Extend the doc comment on `SoftDeleteMessage`**

Replace the existing comment (lines 110–112):

```go
// SoftDeleteMessage uses a Cassandra LWT on messages_by_id as a one-shot gate so only
// the winning goroutine runs mirror-table updates and tcount decrement, preventing double-decrement.
// `IF deleted != true` matches NULL (message-worker never writes deleted) and false, excluding true.
```

with:

```go
// SoftDeleteMessage uses a Cassandra LWT on messages_by_id as a one-shot gate
// so only the winning goroutine runs mirror-table updates and the parent
// tcount decrement, preventing double-counting under concurrent retries.
// `IF deleted != true` matches NULL (message-worker never writes deleted) and
// false, excluding true.
//
// Consistency model: messages_by_id is the source of truth. After the LWT
// applies, the mirror updates (messages_by_room or thread_messages_by_room,
// optionally pinned_messages_by_room) and the parent tcount decrement run
// sequentially with no atomic guarantee across partitions. A crash mid-write
// leaves messages_by_id flagged deleted and one or more mirrors not yet
// flagged; the next successful delete on the same message is short-circuited
// upstream by the already-deleted check, so the mirrors remain stale until a
// future operation touches the row. Readers that hit a stale mirror will see
// the pre-deletion content for a brief window. This trade-off is intentional
// given Cassandra's lack of multi-partition transactions.
```

- [ ] **Step 3: Verify nothing else changed**

Run:
```bash
git diff history-service/internal/cassrepo/write.go
```
Expected: only the comment additions. No code changes.

- [ ] **Step 4: Run unit + integration tests to confirm no functional drift**

Run: `make test SERVICE=history-service && make test-integration SERVICE=history-service`
Expected: PASS — comment-only change.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/write.go
git commit -m "docs(history-service): document mirror-table consistency model on writers"
```

---

## Task 5: Bound startup with a context deadline

**Why:** `cmd/main.go` uses `context.Background()` for tracer init, Mongo connect, and `EnsureIndexes`. If any of those hangs (network partition, mongo primary stepdown, OTel collector down), the pod blocks indefinitely and never reaches Ready, denying the orchestrator the chance to restart and retry. A bounded context makes the failure surface as a non-zero exit, which the orchestrator can act on.

**Files:**
- Modify: `history-service/cmd/main.go`

- [ ] **Step 1: Add the constant and bounded context to `main()`**

In `history-service/cmd/main.go`, immediately after the existing `import` block, add the constant declaration:

```go
// startupTimeout bounds the time spent on tracer init, Mongo connect, and
// index creation at startup. Hardcoded — startup time is not an
// environment-tunable concern.
const startupTimeout = 30 * time.Second
```

Then replace this section (around line 31):

```go
	ctx := context.Background()

	tracerShutdown, err := otelutil.InitTracer(ctx, "history-service")
```

with:

```go
	ctx := context.Background()
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()

	tracerShutdown, err := otelutil.InitTracer(startupCtx, "history-service")
```

- [ ] **Step 2: Use `startupCtx` for the Mongo connect**

In the same file, replace:
```go
	mongoClient, err := mongoutil.Connect(ctx, cfg.Mongo.URI, cfg.Mongo.Username, cfg.Mongo.Password)
```
with:
```go
	mongoClient, err := mongoutil.Connect(startupCtx, cfg.Mongo.URI, cfg.Mongo.Username, cfg.Mongo.Password)
```

- [ ] **Step 3: Use `startupCtx` for `EnsureIndexes` and release it after startup completes**

Replace the existing index-ensure block:
```go
	if err := threadRoomRepo.EnsureIndexes(ctx); err != nil {
		slog.Error("ensure thread_rooms indexes failed", "error", err)
		os.Exit(1)
	}
```
with:
```go
	if err := threadRoomRepo.EnsureIndexes(startupCtx); err != nil {
		slog.Error("ensure thread_rooms indexes failed", "error", err)
		os.Exit(1)
	}
	cancelStartup()
```

The explicit `cancelStartup()` releases the timer goroutine immediately once startup is complete; the `defer` from Step 1 still guards against early returns.

- [ ] **Step 4: Verify the runtime context (`ctx`) is still used by the rest of `main`**

Run:
```bash
grep -n "context\.\|ctx\b\|startupCtx" history-service/cmd/main.go
```
Expected: `shutdown.Wait` and any other long-lived calls still use `ctx` (the unbounded background context); only tracer/mongo/EnsureIndexes use `startupCtx`. NATS and Cassandra connects don't take a context — they keep their existing call signatures.

- [ ] **Step 5: Build and run unit tests**

Run: `make build SERVICE=history-service && make test SERVICE=history-service`
Expected: PASS — main.go still compiles, unit tests unaffected (they don't touch main).

- [ ] **Step 6: Commit**

```bash
git add history-service/cmd/main.go
git commit -m "fix(history-service): bound startup with 30s context deadline"
```

---

## Task 6: Hydrate thread parents from `messages_by_room` via `MessageKey`

**Why:** Today `GetThreadParentMessages` calls `GetMessagesByIDs(ctx, parentIDs)` which queries `messages_by_id` with `IN (?, ?, ...)`. Each ID lives on its own partition (partition key = `message_id`), so a 100-ID batch hits 100 partitions and pressures the coordinator. The thread-rooms MongoDB documents already store `ThreadParentCreatedAt` and `RoomID`, so the hydration query can target `messages_by_room` instead — `(room_id, (created_at, message_id) IN (...))` collapses to a single partition with multi-column-IN over clustering keys.

**Trade-off:** `messages_by_room` lacks `pinned_at` / `pinned_by` columns (only present in `messages_by_id` and `pinned_messages_by_room`). Hydrated thread-parent messages will not include pin metadata. Acceptable — the thread-parent UI does not display pin status.

**Files:**
- Modify: `pkg/model/cassandra/message.go` (add `MessageKey` type)
- Modify: `history-service/internal/cassrepo/messages_by_room.go` (add `GetMessagesByRoomAndKeys`)
- Modify: `history-service/internal/cassrepo/messages_by_id.go` (delete `GetMessagesByIDs`)
- Modify: `history-service/internal/cassrepo/messages_by_id_integration_test.go` (delete `GetMessagesByIDs` tests)
- Create: integration test for `GetMessagesByRoomAndKeys` (append to `messages_by_room_integration_test.go`)
- Modify: `history-service/internal/service/service.go` (`MessageReader` interface)
- Modify: `history-service/internal/service/mocks/mock_repository.go` (regenerated)
- Modify: `history-service/internal/service/threads.go` (call new method)
- Modify: `history-service/internal/service/threads_test.go` (mock expectations)

- [ ] **Step 1: Add `MessageKey` to `pkg/model/cassandra/message.go`**

Append to `pkg/model/cassandra/message.go` (after the `Message` struct, before EOF):

```go
// MessageKey identifies a message within a known room — the clustering keys
// of messages_by_room (and pinned_messages_by_room). Used by bulk-fetch
// queries that look up N specific messages in one room with a single
// partition-bound query.
type MessageKey struct {
	CreatedAt time.Time
	MessageID string
}
```

- [ ] **Step 2: Write the failing integration test for `GetMessagesByRoomAndKeys`**

Append to `history-service/internal/cassrepo/messages_by_room_integration_test.go`:

```go
func TestRepository_GetMessagesByRoomAndKeys(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	ts1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
	ts3 := time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)

	for _, fields := range []struct {
		id string
		ts time.Time
	}{
		{"m-key-1", ts1},
		{"m-key-2", ts2},
		{"m-key-3", ts3},
	} {
		require.NoError(t, session.Query(
			`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg) VALUES (?, ?, ?, ?, ?)`,
			"r1", fields.ts, fields.id, sender, "x",
		).Exec())
	}

	keys := []models.MessageKey{
		{CreatedAt: ts1, MessageID: "m-key-1"},
		{CreatedAt: ts3, MessageID: "m-key-3"},
	}
	got, err := repo.GetMessagesByRoomAndKeys(ctx, "r1", keys)
	require.NoError(t, err)
	require.Len(t, got, 2)
	ids := []string{got[0].MessageID, got[1].MessageID}
	assert.ElementsMatch(t, []string{"m-key-1", "m-key-3"}, ids)
}

func TestRepository_GetMessagesByRoomAndKeys_Empty(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	got, err := repo.GetMessagesByRoomAndKeys(ctx, "r1", nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestRepository_GetMessagesByRoomAndKeys_PartialMatch(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg) VALUES (?, ?, ?, ?, ?)`,
		"r1", ts, "m-exists", sender, "hi",
	).Exec())

	keys := []models.MessageKey{
		{CreatedAt: ts, MessageID: "m-exists"},
		{CreatedAt: ts, MessageID: "m-missing"},
	}
	got, err := repo.GetMessagesByRoomAndKeys(ctx, "r1", keys)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "m-exists", got[0].MessageID)
}
```

If `models` is not already aliased to `pkg/model/cassandra` in the test file's imports, check the existing imports — `messages_by_room_integration_test.go` already imports models from history-service's models (which re-exports cassandra types). Verify the `models.MessageKey` reference resolves; if not, add the import alias.

- [ ] **Step 3: Run the new tests to verify they fail**

Run: `make test-integration SERVICE=history-service`
Expected: FAIL — `GetMessagesByRoomAndKeys` undefined.

- [ ] **Step 4: Implement `GetMessagesByRoomAndKeys`**

Append to `history-service/internal/cassrepo/messages_by_room.go`:

```go
// GetMessagesByRoomAndKeys fetches the messages identified by keys (clustering
// coordinates within roomID) using a single multi-column-IN query against
// messages_by_room. Empty keys returns an empty slice without hitting
// Cassandra. Missing rows are silently omitted; result order is not
// guaranteed (callers should re-sort using their own ordering).
func (r *Repository) GetMessagesByRoomAndKeys(ctx context.Context, roomID string, keys []models.MessageKey) ([]models.Message, error) {
	if len(keys) == 0 {
		return []models.Message{}, nil
	}

	var sb strings.Builder
	sb.WriteString(messageByRoomQuery)
	sb.WriteString(` WHERE room_id = ? AND (created_at, message_id) IN (`)
	args := make([]interface{}, 0, 1+2*len(keys))
	args = append(args, roomID)
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(?, ?)")
		args = append(args, k.CreatedAt, k.MessageID)
	}
	sb.WriteString(")")

	iter := r.session.Query(sb.String(), args...).WithContext(ctx).Iter()
	messages := scanMsgsFromIter(iter)
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("querying messages by room and keys: %w", err)
	}
	return messages, nil
}
```

Add `"strings"` to the file's import block if not already present.

- [ ] **Step 5: Run the integration tests to verify they pass**

Run: `make test-integration SERVICE=history-service`
Expected: PASS — three new tests green; all existing tests still green.

- [ ] **Step 6: Update `MessageReader` interface in `service.go`**

In `history-service/internal/service/service.go`, replace the line:
```go
	GetMessagesByIDs(ctx context.Context, messageIDs []string) ([]models.Message, error)
```
with:
```go
	GetMessagesByRoomAndKeys(ctx context.Context, roomID string, keys []models.MessageKey) ([]models.Message, error)
```

(The line is in the `MessageReader` interface near line 24.)

- [ ] **Step 7: Regenerate the mock**

Run: `make generate SERVICE=history-service`
Expected: `internal/service/mocks/mock_repository.go` regenerated. The `GetMessagesByIDs` method is removed; `GetMessagesByRoomAndKeys` is added.

- [ ] **Step 8: Update `GetThreadParentMessages` to use the new method**

In `history-service/internal/service/threads.go`, replace this block (around lines 121–137):

```go
	seenIDs := make(map[string]struct{}, len(threadPage.Data))
	parentIDs := make([]string, 0, len(threadPage.Data))
	for i := range threadPage.Data {
		id := threadPage.Data[i].ParentMessageID
		if _, dup := seenIDs[id]; dup {
			continue
		}
		seenIDs[id] = struct{}{}
		parentIDs = append(parentIDs, id)
	}

	cassMessages, err := s.msgReader.GetMessagesByIDs(c, parentIDs)
	if err != nil {
		slog.Error("hydrating thread parent messages from Cassandra", "error", err, "roomID", roomID)
		return nil, natsrouter.ErrInternal("failed to load thread parent messages")
	}
```

with:

```go
	seenIDs := make(map[string]struct{}, len(threadPage.Data))
	parentIDs := make([]string, 0, len(threadPage.Data))
	keys := make([]cassmodel.MessageKey, 0, len(threadPage.Data))
	for i := range threadPage.Data {
		id := threadPage.Data[i].ParentMessageID
		if _, dup := seenIDs[id]; dup {
			continue
		}
		seenIDs[id] = struct{}{}
		parentIDs = append(parentIDs, id)
		keys = append(keys, cassmodel.MessageKey{
			CreatedAt: threadPage.Data[i].ThreadParentCreatedAt,
			MessageID: id,
		})
	}

	cassMessages, err := s.msgReader.GetMessagesByRoomAndKeys(c, roomID, keys)
	if err != nil {
		slog.Error("hydrating thread parent messages from Cassandra", "error", err, "roomID", roomID)
		return nil, natsrouter.ErrInternal("failed to load thread parent messages")
	}
```

Add the import `cassmodel "github.com/hmchangw/chat/pkg/model/cassandra"` to `threads.go`'s import block. The `models` package alias in this file points to `history-service/internal/models` which re-exports many cassandra types; using a `cassmodel` alias keeps the new type's origin explicit.

- [ ] **Step 9: Update `threads_test.go` mock expectations**

In `history-service/internal/service/threads_test.go`, replace every occurrence of:
```go
msgs.EXPECT().GetMessagesByIDs(gomock.Any(), gomock.Any())
```
with:
```go
msgs.EXPECT().GetMessagesByRoomAndKeys(gomock.Any(), gomock.Any(), gomock.Any())
```

Two occurrences are stricter (lines 432 and 623): they assert on a specific argument value:
```go
msgs.EXPECT().GetMessagesByIDs(gomock.Any(), []string{"p1"}).Return(...)
msgs.EXPECT().GetMessagesByIDs(gomock.Any(), []string{"p-early"}).Return(...)
```
Update those to match by message ID inside the keys slice. Replace each with:
```go
msgs.EXPECT().GetMessagesByRoomAndKeys(gomock.Any(), gomock.Any(), gomock.Cond(func(v any) bool {
    keys, ok := v.([]cassmodel.MessageKey)
    return ok && len(keys) == 1 && keys[0].MessageID == "p1"
})).Return(...)
```
(Use `"p-early"` for the other site.) Add the import `cassmodel "github.com/hmchangw/chat/pkg/model/cassandra"` to the test file.

- [ ] **Step 10: Remove `GetMessagesByIDs` implementation**

In `history-service/internal/cassrepo/messages_by_id.go`, delete the `GetMessagesByIDs` function and any unused imports it leaves behind. The file should retain only `GetMessageByID`.

- [ ] **Step 11: Remove `GetMessagesByIDs` integration tests**

In `history-service/internal/cassrepo/messages_by_id_integration_test.go`, delete the three tests `TestRepository_GetMessagesByIDs`, `TestRepository_GetMessagesByIDs_Empty`, `TestRepository_GetMessagesByIDs_MissingID` (lines 186–236).

- [ ] **Step 12: Run full unit + integration test suite**

Run: `make test SERVICE=history-service && make test-integration SERVICE=history-service`
Expected: PASS — all existing thread tests still pass with the new mock expectations; new integration tests pass; removed tests no longer present.

- [ ] **Step 13: Run lint to catch any unused imports or symbol drift**

Run: `make lint`
Expected: PASS.

- [ ] **Step 14: Commit**

```bash
git add pkg/model/cassandra/message.go history-service/internal/cassrepo/ history-service/internal/service/
git commit -m "perf(history-service): hydrate thread parents from messages_by_room"
```

---

## Task 7: Make `casMaxRetries` configurable on `cassrepo.Repository`

**Why:** The 16-retry CAS bound is hardcoded in `cassrepo/write.go`. Under realistic concurrent-delete bursts on the same parent message, 16 may be tight or generous depending on cluster size and contention pattern. Making it a constructor option (with default 16) gives operators a tuning knob without exposing yet another env var. Per the brainstorming decision (option E), no design change — just lift the constant onto the struct.

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Modify: `history-service/internal/cassrepo/write.go` (use `r.casRetries` instead of the constant)

- [ ] **Step 1: Replace `Repository` and `NewRepository` with a configurable shape**

In `history-service/internal/cassrepo/repository.go`, replace the entire file content with:

```go
package cassrepo

import (
	"github.com/gocql/gocql"
)

// defaultCASMaxRetries bounds the CAS loop in casDecrement; 16 retries cover
// realistic burst concurrency. Tunable per Repository via WithCASMaxRetries.
const defaultCASMaxRetries = 16

// Repository wraps a gocql session with the small set of message-table
// operations history-service needs.
type Repository struct {
	session     *gocql.Session
	casRetries  int
}

// Option configures a Repository on construction.
type Option func(*Repository)

// WithCASMaxRetries overrides the CAS-loop retry bound used by tcount
// decrement. Useful for tuning under high concurrent-delete contention.
func WithCASMaxRetries(n int) Option {
	return func(r *Repository) {
		if n > 0 {
			r.casRetries = n
		}
	}
}

func NewRepository(session *gocql.Session, opts ...Option) *Repository {
	r := &Repository{session: session, casRetries: defaultCASMaxRetries}
	for _, opt := range opts {
		opt(r)
	}
	return r
}
```

- [ ] **Step 2: Replace the constant in `write.go` with the per-instance field**

In `history-service/internal/cassrepo/write.go`:

Delete the existing `casMaxRetries` constant declaration:
```go
// casMaxRetries bounds the CAS loop; 16 retries cover realistic burst concurrency.
const casMaxRetries = 16
```

Replace both occurrences of `casMaxRetries` (in `decrementParentTcount`, around lines 187 and 208) with `r.casRetries`:

```go
if err := casDecrement(r.casRetries, tcount, func(newVal int, expected *int) (bool, *int, error) {
```

- [ ] **Step 3: Run unit tests to verify the package still compiles**

Run: `make test SERVICE=history-service`
Expected: PASS — `cassrepo` compiles; existing tests unchanged because the default still resolves to 16.

- [ ] **Step 4: Run integration tests**

Run: `make test-integration SERVICE=history-service`
Expected: PASS — CAS-decrement integration tests still verify the same behavior with the default retry count.

- [ ] **Step 5: Verify `cmd/main.go` still compiles unchanged**

Run: `make build SERVICE=history-service`
Expected: PASS — `NewRepository(cassSession)` is still valid (variadic options means zero opts is fine).

- [ ] **Step 6: Commit**

```bash
git add history-service/internal/cassrepo/repository.go history-service/internal/cassrepo/write.go
git commit -m "refactor(history-service): make casMaxRetries configurable per repository"
```

---

## Task 8: TODO comment on the pinned-table edit/delete helpers

**Why:** The pinned-table UPDATE in edit/delete uses `*msg.PinnedAt` as the clustering-key value. Per the project docs, no pin/unpin operation exists in the codebase today — these branches are dead code retained for future correctness. When a real pin/unpin ships, `msg.PinnedAt` (read from `messages_by_id`) could be stale relative to the actual `pinned_messages_by_room` row's clustering key (an unpin-then-repin would leave a row at a different timestamp), and the UPDATE would silently no-op. Flag this now while the affected code is in front of us, so a future implementer of pin/unpin sees the constraint.

**Files:**
- Modify: `history-service/internal/cassrepo/write.go` (add TODO comment on the pinned helpers)

- [ ] **Step 1: Add the TODO comment above `editInPinnedMessagesByRoom`**

In `history-service/internal/cassrepo/write.go`, immediately above `func (r *Repository) editInPinnedMessagesByRoom(...)` (around line 66), insert:

```go
// TODO(pin-feature): When a real pin/unpin operation ships, this helper's
// invariant — msg.PinnedAt matches the clustering key of the live
// pinned_messages_by_room row — must be re-verified. An unpin-then-repin
// flow would create a new row at a later pinned_at while messages_by_id
// still references the older value (until the pin op updates it),
// causing this UPDATE to silently no-op. The same constraint applies to
// deleteInPinnedMessagesByRoom below.
```

- [ ] **Step 2: Verify only that comment was added**

Run:
```bash
git diff history-service/internal/cassrepo/write.go
```
Expected: a single insertion of the comment block — no other changes.

- [ ] **Step 3: Run lint and tests to confirm comment-only**

Run: `make lint && make test SERVICE=history-service`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add history-service/internal/cassrepo/write.go
git commit -m "docs(history-service): TODO on pinned-table helpers for future pin/unpin"
```

---
