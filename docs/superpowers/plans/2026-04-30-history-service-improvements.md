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
