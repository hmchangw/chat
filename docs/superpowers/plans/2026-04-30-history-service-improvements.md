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
