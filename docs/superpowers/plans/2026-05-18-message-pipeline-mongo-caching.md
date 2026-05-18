# Message Pipeline MongoDB Caching Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate per-message MongoDB read amplification in `message-gatekeeper` and `broadcast-worker` by introducing a shared room-metadata cache package and a gatekeeper-private subscription cache, and by removing the wasted read-after-write in broadcast-worker's room update.

**Architecture:** Process-local LRU+TTL caches via `hashicorp/golang-lru/v2` Expirable, with `golang.org/x/sync/singleflight` to dedupe concurrent misses. Caches wrap the existing Mongo stores transparently (the store interface is the boundary, the cache is an implementation choice at wiring time). The hot-path `rooms.FindOneAndUpdate` becomes a plain `rooms.UpdateOne` (no read-back), with the formerly-returned fields served from the cache.

**Tech Stack:** Go 1.25, MongoDB v2 driver, `hashicorp/golang-lru/v2`, `golang.org/x/sync/singleflight`, `go.uber.org/mock` (mockgen), `stretchr/testify`, `testcontainers-go`.

**Spec:** `docs/superpowers/specs/2026-05-18-message-pipeline-mongo-caching-design.md`

**Branch:** `claude/investigate-mongodb-cpu-zQG01`

---

## File Structure

**New files:**
- `pkg/roommetacache/roommetacache.go` — Cache type + Stats + singleflight loader. Shared between gatekeeper and broadcast-worker.
- `pkg/roommetacache/roommetacache_test.go` — Unit tests for the package.
- `message-gatekeeper/subcache.go` — Subscription cache wrapper (`cachedSubscriptionStore`). Private to gatekeeper.
- `message-gatekeeper/subcache_test.go` — Unit tests for the subscription cache wrapper.
- `message-gatekeeper/metacache.go` — Room-metadata cache wrapper (`cachedMetaStore`). Bridges the gatekeeper Store interface to `pkg/roommetacache`.
- `message-gatekeeper/metacache_test.go` — Unit tests for the metacache wrapper.
- `broadcast-worker/metacache.go` — Room-metadata cache wrapper for broadcast-worker.
- `broadcast-worker/metacache_test.go` — Unit tests for the metacache wrapper.

**Modified files:**
- `go.mod`, `go.sum` — add `github.com/hashicorp/golang-lru/v2`.
- `broadcast-worker/store.go` — replace `FetchAndUpdateRoom` with `UpdateRoomLastMessage` + `GetRoomMeta`.
- `broadcast-worker/store_mongo.go` — implement the two new methods, remove `FetchAndUpdateRoom`.
- `broadcast-worker/handler.go` — use the new methods; `buildRoomEvent` takes `roommetacache.Meta`.
- `broadcast-worker/handler_test.go` — update mock expectations for the new methods.
- `broadcast-worker/main.go` — wire the cache.
- `broadcast-worker/integration_test.go` — verify the new path persists `lastMsgAt` etc. correctly.
- `broadcast-worker/mock_store_test.go` — regenerated.
- `message-gatekeeper/store.go` — add `GetRoomMeta` method to interface, remove `GetRoomUserCount`.
- `message-gatekeeper/store_mongo.go` — implement `GetRoomMeta`, remove `GetRoomUserCount`.
- `message-gatekeeper/handler.go` — call `GetRoomMeta` instead of `GetRoomUserCount`.
- `message-gatekeeper/handler_test.go` — update mock expectations.
- `message-gatekeeper/main.go` — wire the caches.
- `message-gatekeeper/mock_store_test.go` — regenerated.

---

## Phase 1: New shared package `pkg/roommetacache`

### Task 1: Add `hashicorp/golang-lru/v2` dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the dependency**

Run:
```bash
go get github.com/hashicorp/golang-lru/v2@v2.0.7
```

Expected: `go.mod` gets `github.com/hashicorp/golang-lru/v2 v2.0.7` as a direct dependency; `go.sum` updated.

- [ ] **Step 2: Verify it builds**

Run:
```bash
go build ./...
```

Expected: success, no compile errors.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add hashicorp/golang-lru/v2 for in-process LRU+TTL caches"
```

### Task 2: Scaffold the package skeleton

**Files:**
- Create: `pkg/roommetacache/roommetacache.go`

- [ ] **Step 1: Create the file with types and constructor**

Write `pkg/roommetacache/roommetacache.go`:

```go
// Package roommetacache provides a process-local LRU+TTL cache for room
// metadata that is read on the per-message hot path of multiple services.
//
// The cached fields (Type, Name, SiteID, UserCount) change rarely; reading
// them from MongoDB on every published message produces measurable wasted
// load. This package centralizes the cache so message-gatekeeper and
// broadcast-worker share a uniform shape and behavior.
//
// Freshness is TTL-bounded. There is no active invalidation in v1 — see
// the spec at
// docs/superpowers/specs/2026-05-18-message-pipeline-mongo-caching-design.md
// for rationale.
package roommetacache

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"

	"github.com/hmchangw/chat/pkg/model"
)

// Meta is the cached projection of a room document. Both consumers
// (gatekeeper and broadcast-worker) use these four fields and nothing
// else from the room.
type Meta struct {
	ID        string
	Type      model.RoomType
	Name      string
	SiteID    string
	UserCount int
}

// Loader fetches a fresh Meta for the given roomID. The cache calls
// Loader on miss; a non-nil error short-circuits the cache (the error
// is returned to the caller and nothing is cached).
type Loader func(ctx context.Context, roomID string) (Meta, error)

// Cache is an LRU+TTL cache of room Meta values, deduped via
// singleflight on miss.
type Cache struct {
	lru    *lru.LRU[string, Meta]
	loader Loader
	sf     singleflight.Group

	hits     atomic.Uint64
	misses   atomic.Uint64
	loadErrs atomic.Uint64
}

// Stats is a snapshot of the cache's hit/miss counters.
type Stats struct {
	Hits, Misses, LoadErrors uint64
	Size                     int
}

// New constructs a Cache with the given capacity, TTL, and loader.
// size and ttl must both be positive; loader must be non-nil.
func New(size int, ttl time.Duration, loader Loader) (*Cache, error) {
	if size <= 0 {
		return nil, fmt.Errorf("roommetacache: size must be positive, got %d", size)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("roommetacache: ttl must be positive, got %v", ttl)
	}
	if loader == nil {
		return nil, fmt.Errorf("roommetacache: loader must not be nil")
	}
	return &Cache{
		lru:    lru.NewLRU[string, Meta](size, nil, ttl),
		loader: loader,
	}, nil
}
```

- [ ] **Step 2: Verify it compiles**

Run:
```bash
go build ./pkg/roommetacache/...
```

Expected: success.

- [ ] **Step 3: Commit**

```bash
git add pkg/roommetacache/roommetacache.go
git commit -m "feat(roommetacache): add package skeleton with Meta, Loader, Cache types"
```

### Task 3: TDD basic Get behavior (hit/miss)

**Files:**
- Create: `pkg/roommetacache/roommetacache_test.go`
- Modify: `pkg/roommetacache/roommetacache.go`

- [ ] **Step 1: Write failing test**

Write `pkg/roommetacache/roommetacache_test.go`:

```go
package roommetacache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roommetacache"
)

func makeMeta(id string) roommetacache.Meta {
	return roommetacache.Meta{
		ID:        id,
		Type:      model.RoomTypeChannel,
		Name:      "room " + id,
		SiteID:    "site-a",
		UserCount: 7,
	}
}

func TestCache_GetMissThenHit(t *testing.T) {
	var loaderCalls atomic.Int32
	loader := func(_ context.Context, roomID string) (roommetacache.Meta, error) {
		loaderCalls.Add(1)
		return makeMeta(roomID), nil
	}
	c, err := roommetacache.New(10, time.Minute, loader)
	require.NoError(t, err)

	// First call: miss, loader runs.
	got, err := c.Get(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, makeMeta("r1"), got)
	assert.Equal(t, int32(1), loaderCalls.Load(), "loader should run on miss")

	// Second call: hit, loader does NOT run again.
	got2, err := c.Get(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, makeMeta("r1"), got2)
	assert.Equal(t, int32(1), loaderCalls.Load(), "loader should not run on hit")

	stats := c.Stats()
	assert.Equal(t, uint64(1), stats.Hits)
	assert.Equal(t, uint64(1), stats.Misses)
	assert.Equal(t, uint64(0), stats.LoadErrors)
}
```

- [ ] **Step 2: Run test, confirm it fails**

Run:
```bash
go test ./pkg/roommetacache/...
```

Expected: FAIL — `c.Get` / `c.Stats` undefined.

- [ ] **Step 3: Implement Get and Stats**

Append to `pkg/roommetacache/roommetacache.go`:

```go
// Get returns the cached Meta for roomID. On miss it calls the configured
// loader (deduped via singleflight) and caches the result. Loader errors
// are returned to the caller and not cached.
func (c *Cache) Get(ctx context.Context, roomID string) (Meta, error) {
	if v, ok := c.lru.Get(roomID); ok {
		c.hits.Add(1)
		return v, nil
	}
	c.misses.Add(1)

	v, err, _ := c.sf.Do(roomID, func() (interface{}, error) {
		// Recheck the cache inside singleflight in case a sibling caller
		// populated it while we were waiting for the lock.
		if cached, ok := c.lru.Get(roomID); ok {
			return cached, nil
		}
		loaded, err := c.loader(ctx, roomID)
		if err != nil {
			return Meta{}, err
		}
		c.lru.Add(roomID, loaded)
		return loaded, nil
	})
	if err != nil {
		c.loadErrs.Add(1)
		return Meta{}, err
	}
	return v.(Meta), nil
}

// Stats returns a snapshot of the cache's counters.
func (c *Cache) Stats() Stats {
	return Stats{
		Hits:       c.hits.Load(),
		Misses:     c.misses.Load(),
		LoadErrors: c.loadErrs.Load(),
		Size:       c.lru.Len(),
	}
}
```

- [ ] **Step 4: Run test, confirm it passes**

Run:
```bash
go test ./pkg/roommetacache/... -run TestCache_GetMissThenHit -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/roommetacache/
git commit -m "feat(roommetacache): implement Get with singleflight and Stats"
```

### Task 4: TDD loader error not cached

**Files:**
- Modify: `pkg/roommetacache/roommetacache_test.go`

- [ ] **Step 1: Add failing test**

Append to `pkg/roommetacache/roommetacache_test.go`:

```go
func TestCache_LoaderErrorNotCached(t *testing.T) {
	var calls atomic.Int32
	wantErr := errors.New("boom")
	loader := func(_ context.Context, _ string) (roommetacache.Meta, error) {
		calls.Add(1)
		return roommetacache.Meta{}, wantErr
	}
	c, err := roommetacache.New(10, time.Minute, loader)
	require.NoError(t, err)

	_, err = c.Get(context.Background(), "r1")
	assert.ErrorIs(t, err, wantErr)
	_, err = c.Get(context.Background(), "r1")
	assert.ErrorIs(t, err, wantErr)

	assert.Equal(t, int32(2), calls.Load(), "errors should not be cached; loader must run again")

	stats := c.Stats()
	assert.Equal(t, uint64(2), stats.Misses)
	assert.Equal(t, uint64(2), stats.LoadErrors)
}
```

- [ ] **Step 2: Run test, confirm it passes immediately**

Run:
```bash
go test ./pkg/roommetacache/... -run TestCache_LoaderErrorNotCached -v
```

Expected: PASS (the implementation from Task 3 already handles this).

- [ ] **Step 3: Commit**

```bash
git add pkg/roommetacache/roommetacache_test.go
git commit -m "test(roommetacache): assert loader errors are not cached"
```

### Task 5: TDD TTL eviction

**Files:**
- Modify: `pkg/roommetacache/roommetacache_test.go`

- [ ] **Step 1: Add failing test**

Append to `pkg/roommetacache/roommetacache_test.go`:

```go
func TestCache_TTLExpires(t *testing.T) {
	var calls atomic.Int32
	loader := func(_ context.Context, roomID string) (roommetacache.Meta, error) {
		calls.Add(1)
		return makeMeta(roomID), nil
	}
	c, err := roommetacache.New(10, 50*time.Millisecond, loader)
	require.NoError(t, err)

	_, _ = c.Get(context.Background(), "r1")
	_, _ = c.Get(context.Background(), "r1")
	require.Equal(t, int32(1), calls.Load(), "second call within TTL should be a hit")

	time.Sleep(75 * time.Millisecond)
	_, _ = c.Get(context.Background(), "r1")
	assert.Equal(t, int32(2), calls.Load(), "after TTL expiry, loader runs again")
}
```

- [ ] **Step 2: Run test, confirm it passes**

Run:
```bash
go test ./pkg/roommetacache/... -run TestCache_TTLExpires -v
```

Expected: PASS — the underlying `lru.NewLRU` constructor takes a TTL parameter that handles eviction automatically.

- [ ] **Step 3: Commit**

```bash
git add pkg/roommetacache/roommetacache_test.go
git commit -m "test(roommetacache): assert TTL eviction works as configured"
```

### Task 6: TDD capacity eviction (LRU)

**Files:**
- Modify: `pkg/roommetacache/roommetacache_test.go`

- [ ] **Step 1: Add failing test**

Append to `pkg/roommetacache/roommetacache_test.go`:

```go
func TestCache_CapacityEviction(t *testing.T) {
	var calls atomic.Int32
	loader := func(_ context.Context, roomID string) (roommetacache.Meta, error) {
		calls.Add(1)
		return makeMeta(roomID), nil
	}
	c, err := roommetacache.New(2, time.Minute, loader)
	require.NoError(t, err)

	_, _ = c.Get(context.Background(), "r1") // miss
	_, _ = c.Get(context.Background(), "r2") // miss
	_, _ = c.Get(context.Background(), "r3") // miss; evicts r1 (LRU)
	require.Equal(t, int32(3), calls.Load())

	// r1 should be evicted; re-loading produces another miss.
	_, _ = c.Get(context.Background(), "r1")
	assert.Equal(t, int32(4), calls.Load(), "r1 should have been evicted by capacity")

	// r3 should still be cached.
	_, _ = c.Get(context.Background(), "r3")
	assert.Equal(t, int32(4), calls.Load(), "r3 should still be a hit")
}
```

- [ ] **Step 2: Run test, confirm it passes**

Run:
```bash
go test ./pkg/roommetacache/... -run TestCache_CapacityEviction -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/roommetacache/roommetacache_test.go
git commit -m "test(roommetacache): assert LRU capacity eviction"
```

### Task 7: TDD Invalidate

**Files:**
- Modify: `pkg/roommetacache/roommetacache.go`, `pkg/roommetacache/roommetacache_test.go`

- [ ] **Step 1: Add failing test**

Append to `pkg/roommetacache/roommetacache_test.go`:

```go
func TestCache_Invalidate(t *testing.T) {
	var calls atomic.Int32
	loader := func(_ context.Context, roomID string) (roommetacache.Meta, error) {
		calls.Add(1)
		return makeMeta(roomID), nil
	}
	c, err := roommetacache.New(10, time.Minute, loader)
	require.NoError(t, err)

	_, _ = c.Get(context.Background(), "r1")
	_, _ = c.Get(context.Background(), "r1")
	require.Equal(t, int32(1), calls.Load())

	c.Invalidate("r1")

	_, _ = c.Get(context.Background(), "r1")
	assert.Equal(t, int32(2), calls.Load(), "Invalidate should cause next Get to miss")
}
```

- [ ] **Step 2: Run test, confirm it fails**

Run:
```bash
go test ./pkg/roommetacache/... -run TestCache_Invalidate -v
```

Expected: FAIL — `c.Invalidate` undefined.

- [ ] **Step 3: Implement Invalidate**

Append to `pkg/roommetacache/roommetacache.go`:

```go
// Invalidate removes any cached entry for roomID. Safe to call when
// no entry exists; in that case it is a no-op. Included from v1 even
// though no caller uses it, so future event-driven invalidation work
// plugs in without an interface change.
func (c *Cache) Invalidate(roomID string) {
	c.lru.Remove(roomID)
}
```

- [ ] **Step 4: Run test, confirm it passes**

Run:
```bash
go test ./pkg/roommetacache/... -run TestCache_Invalidate -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/roommetacache/
git commit -m "feat(roommetacache): add Invalidate for future event-driven freshness"
```

### Task 8: TDD singleflight dedup of concurrent misses

**Files:**
- Modify: `pkg/roommetacache/roommetacache_test.go`

- [ ] **Step 1: Add failing test**

Append to `pkg/roommetacache/roommetacache_test.go`:

```go
func TestCache_SingleflightDedupsMisses(t *testing.T) {
	var calls atomic.Int32
	gate := make(chan struct{})
	loader := func(_ context.Context, roomID string) (roommetacache.Meta, error) {
		calls.Add(1)
		<-gate // hold so concurrent callers pile up
		return makeMeta(roomID), nil
	}
	c, err := roommetacache.New(10, time.Minute, loader)
	require.NoError(t, err)

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, err := c.Get(context.Background(), "r1")
			assert.NoError(t, err)
		}()
	}

	// Give all callers a moment to enter Get and block on singleflight.
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()

	assert.Equal(t, int32(1), calls.Load(),
		"singleflight should collapse N concurrent misses on the same key to 1 loader call")
}
```

- [ ] **Step 2: Run test, confirm it passes**

Run:
```bash
go test ./pkg/roommetacache/... -run TestCache_SingleflightDedupsMisses -race -v
```

Expected: PASS (singleflight is already in the Get implementation from Task 3).

- [ ] **Step 3: Commit**

```bash
git add pkg/roommetacache/roommetacache_test.go
git commit -m "test(roommetacache): assert singleflight dedups concurrent misses"
```

### Task 9: TDD constructor argument validation

**Files:**
- Modify: `pkg/roommetacache/roommetacache_test.go`

- [ ] **Step 1: Add failing test**

Append to `pkg/roommetacache/roommetacache_test.go`:

```go
func TestNew_RejectsInvalidArgs(t *testing.T) {
	okLoader := func(_ context.Context, _ string) (roommetacache.Meta, error) { return roommetacache.Meta{}, nil }
	tests := []struct {
		name    string
		size    int
		ttl     time.Duration
		loader  roommetacache.Loader
		wantErr string
	}{
		{"zero size", 0, time.Minute, okLoader, "size must be positive"},
		{"negative size", -1, time.Minute, okLoader, "size must be positive"},
		{"zero ttl", 10, 0, okLoader, "ttl must be positive"},
		{"negative ttl", 10, -1, okLoader, "ttl must be positive"},
		{"nil loader", 10, time.Minute, nil, "loader must not be nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := roommetacache.New(tc.size, tc.ttl, tc.loader)
			assert.Nil(t, c)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
```

- [ ] **Step 2: Run test, confirm it passes**

Run:
```bash
go test ./pkg/roommetacache/... -run TestNew_RejectsInvalidArgs -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/roommetacache/roommetacache_test.go
git commit -m "test(roommetacache): assert New validates arguments"
```

### Task 10: Run full coverage check on the package

- [ ] **Step 1: Run with coverage**

Run:
```bash
go test ./pkg/roommetacache/... -coverprofile=/tmp/roommetacache.out
go tool cover -func=/tmp/roommetacache.out | tail -5
```

Expected: `total: (statements)` line at or above 80%. The whole package is ~120 lines; coverage should be 90%+ given the tests written.

- [ ] **Step 2: If coverage is below 80%, add tests for the uncovered branches and commit**

Likely uncovered branches: the inner re-check inside singleflight `Do`. Add this test if needed:

```go
func TestCache_SingleflightInnerRecheck(t *testing.T) {
	// Two callers; first finishes and populates cache;
	// second's singleflight callback runs the re-check and
	// returns the cached value without calling loader.
	// (LRU semantics make this hard to force deterministically;
	// acceptable to skip if 80% coverage is already met.)
}
```

If coverage is fine, skip this step.

---

## Phase 2: broadcast-worker — store interface + implementation

### Task 11: Update broadcast-worker Store interface

**Files:**
- Modify: `broadcast-worker/store.go`

- [ ] **Step 1: Replace `FetchAndUpdateRoom` with `UpdateRoomLastMessage` and `GetRoomMeta`**

Edit `broadcast-worker/store.go`. Replace the entire interface block:

```go
package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roommetacache"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store
//go:generate mockgen -destination=mock_userstore_test.go -package=main github.com/hmchangw/chat/pkg/userstore UserStore
//go:generate mockgen -destination=mock_keystore_test.go -package=main . RoomKeyProvider

// Store defines data access operations for the broadcast worker.
type Store interface {
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error)
	ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
	UpdateRoomLastMessage(ctx context.Context, roomID, msgID string, msgAt time.Time, mentionAll bool) error
	SetSubscriptionMentions(ctx context.Context, roomID string, accounts []string) error
}
```

- [ ] **Step 2: Verify it does not yet compile**

Run:
```bash
go build ./broadcast-worker/...
```

Expected: FAIL — `store_mongo.go` still implements `FetchAndUpdateRoom`, missing the two new methods.

This is the expected RED state; proceed to the next task.

### Task 12: Implement new methods in `store_mongo.go`

**Files:**
- Modify: `broadcast-worker/store_mongo.go`

- [ ] **Step 1: Replace `FetchAndUpdateRoom` with the two new methods**

Edit `broadcast-worker/store_mongo.go`. Replace the `FetchAndUpdateRoom` function (lines 47–65) and add `GetRoomMeta`:

```go
func (m *mongoStore) GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error) {
	filter := bson.M{"_id": roomID}
	opts := options.FindOne().SetProjection(bson.M{
		"type":      1,
		"name":      1,
		"siteId":    1,
		"userCount": 1,
	})
	var doc struct {
		ID        string         `bson:"_id"`
		Type      model.RoomType `bson:"type"`
		Name      string         `bson:"name"`
		SiteID    string         `bson:"siteId"`
		UserCount int            `bson:"userCount"`
	}
	if err := m.roomCol.FindOne(ctx, filter, opts).Decode(&doc); err != nil {
		return roommetacache.Meta{}, fmt.Errorf("get room meta %s: %w", roomID, err)
	}
	return roommetacache.Meta{
		ID:        doc.ID,
		Type:      doc.Type,
		Name:      doc.Name,
		SiteID:    doc.SiteID,
		UserCount: doc.UserCount,
	}, nil
}

func (m *mongoStore) UpdateRoomLastMessage(ctx context.Context, roomID, msgID string, msgAt time.Time, mentionAll bool) error {
	fields := bson.M{
		"lastMsgAt": msgAt,
		"lastMsgId": msgID,
		"updatedAt": msgAt,
	}
	if mentionAll {
		fields["lastMentionAllAt"] = msgAt
	}
	filter := bson.M{"_id": roomID}
	update := bson.M{"$set": fields}

	res, err := m.roomCol.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update room last message %s: %w", roomID, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("update room last message %s: %w", roomID, mongo.ErrNoDocuments)
	}
	return nil
}
```

Then add the import for `roommetacache` at the top of the file:

```go
import (
    ...
    "github.com/hmchangw/chat/pkg/roommetacache"
)
```

- [ ] **Step 2: Verify it compiles**

Run:
```bash
go build ./broadcast-worker/...
```

Expected: FAIL — `handler.go` still calls `store.FetchAndUpdateRoom`. This is expected; the handler edit comes next.

### Task 13: Update broadcast-worker handler to use new methods

**Files:**
- Modify: `broadcast-worker/handler.go`

- [ ] **Step 1: Replace `FetchAndUpdateRoom` call in `handleCreated`**

In `broadcast-worker/handler.go`, lines 75–78 currently read:

```go
room, err := h.store.FetchAndUpdateRoom(ctx, msg.RoomID, msg.ID, msg.CreatedAt, resolved.MentionAll)
if err != nil {
    return fmt.Errorf("fetch and update room %s: %w", msg.RoomID, err)
}
```

Replace with:

```go
if err := h.store.UpdateRoomLastMessage(ctx, msg.RoomID, msg.ID, msg.CreatedAt, resolved.MentionAll); err != nil {
    return fmt.Errorf("update room last message %s: %w", msg.RoomID, err)
}
meta, err := h.store.GetRoomMeta(ctx, msg.RoomID)
if err != nil {
    return fmt.Errorf("get room meta %s: %w", msg.RoomID, err)
}
```

- [ ] **Step 2: Update all downstream uses of `room` to use `meta`**

In the same function, `room` is passed to `publishChannelEvent` and `publishDMEvents`, and `room.Type` is switched on. Update by changing the type and field accesses:

Change:
```go
switch room.Type {
case model.RoomTypeChannel:
    return h.publishChannelEvent(ctx, room, clientMsg, resolved.MentionAll, resolved.Participants)
case model.RoomTypeDM:
    return h.publishDMEvents(ctx, room, clientMsg, resolved.Accounts)
default:
    slog.Warn("unknown room type, skipping fan-out", "type", room.Type, "roomID", room.ID)
    return nil
}
```

to:
```go
switch meta.Type {
case model.RoomTypeChannel:
    return h.publishChannelEvent(ctx, meta, clientMsg, resolved.MentionAll, resolved.Participants)
case model.RoomTypeDM:
    return h.publishDMEvents(ctx, meta, clientMsg, resolved.Accounts)
default:
    slog.Warn("unknown room type, skipping fan-out", "type", meta.Type, "roomID", meta.ID)
    return nil
}
```

- [ ] **Step 3: Change `publishChannelEvent` and `publishDMEvents` signatures**

Find `publishChannelEvent` (line 234) and `publishDMEvents` (line 275). Change their `room *model.Room` parameter to `meta roommetacache.Meta`:

```go
func (h *Handler) publishChannelEvent(ctx context.Context, meta roommetacache.Meta, clientMsg *model.ClientMessage, mentionAll bool, mentions []model.Participant) error {
    evt := buildRoomEvent(meta, clientMsg)
    evt.MentionAll = mentionAll
    if len(mentions) > 0 {
        evt.Mentions = mentions
    }

    if h.encrypt {
        msgJSON, err := json.Marshal(clientMsg)
        if err != nil {
            return fmt.Errorf("marshal client message: %w", err)
        }

        key, err := h.currentRoomKey(ctx, meta.ID)
        if err != nil {
            return err
        }

        encrypted, err := roomcrypto.Encode(string(msgJSON), key.KeyPair.PublicKey, key.Version)
        if err != nil {
            return fmt.Errorf("encrypt message for room %s: %w", meta.ID, err)
        }

        encJSON, err := json.Marshal(encrypted)
        if err != nil {
            return fmt.Errorf("marshal encrypted message: %w", err)
        }

        evt.EncryptedMessage = json.RawMessage(encJSON)
        evt.Message = nil
    }

    payload, err := json.Marshal(evt)
    if err != nil {
        return fmt.Errorf("marshal channel event: %w", err)
    }

    return h.pub.Publish(ctx, subject.RoomEvent(meta.ID), payload)
}
```

For `publishDMEvents`, the body calls `h.store.ListSubscriptions(ctx, room.ID)` — change that to `meta.ID`:

```go
func (h *Handler) publishDMEvents(ctx context.Context, meta roommetacache.Meta, clientMsg *model.ClientMessage, mentionedAccounts []string) error {
    subs, err := h.store.ListSubscriptions(ctx, meta.ID)
    if err != nil {
        return fmt.Errorf("list subscriptions for DM room %s: %w", meta.ID, err)
    }

    mentionSet := make(map[string]struct{}, len(mentionedAccounts))
    for _, name := range mentionedAccounts {
        mentionSet[name] = struct{}{}
    }

    for i := range subs {
        _, hasMention := mentionSet[subs[i].User.Account]

        evt := buildRoomEvent(meta, clientMsg)
        evt.HasMention = hasMention

        payload, err := json.Marshal(evt)
        if err != nil {
            return fmt.Errorf("marshal DM event for user %s: %w", subs[i].User.Account, err)
        }
        if err := h.pub.Publish(ctx, subject.UserRoomEvent(subs[i].User.Account), payload); err != nil {
            slog.Error("publish DM event failed", "error", err, "account", subs[i].User.Account)
        }
    }
    return nil
}
```

- [ ] **Step 4: Update `buildRoomEvent` signature**

In `broadcast-worker/handler.go` around line 303, change:

```go
func buildRoomEvent(room *model.Room, clientMsg *model.ClientMessage) model.RoomEvent {
    return model.RoomEvent{
        Type:      model.RoomEventNewMessage,
        RoomID:    room.ID,
        Timestamp: time.Now().UTC().UnixMilli(),
        RoomName:  room.Name,
        RoomType:  room.Type,
        SiteID:    room.SiteID,
        UserCount: room.UserCount,
        LastMsgAt: clientMsg.CreatedAt,
        LastMsgID: clientMsg.ID,
        Message:   clientMsg,
    }
}
```

to:

```go
func buildRoomEvent(meta roommetacache.Meta, clientMsg *model.ClientMessage) model.RoomEvent {
    return model.RoomEvent{
        Type:      model.RoomEventNewMessage,
        RoomID:    meta.ID,
        Timestamp: time.Now().UTC().UnixMilli(),
        RoomName:  meta.Name,
        RoomType:  meta.Type,
        SiteID:    meta.SiteID,
        UserCount: meta.UserCount,
        LastMsgAt: clientMsg.CreatedAt,
        LastMsgID: clientMsg.ID,
        Message:   clientMsg,
    }
}
```

- [ ] **Step 5: Add `roommetacache` import in handler.go**

Add to the imports block:

```go
"github.com/hmchangw/chat/pkg/roommetacache"
```

- [ ] **Step 6: Update `fanOutMutationEvent`**

The function at lines 138–201 uses `h.store.GetRoom` for edit/delete events. Edit/delete events do not need the cache (they are far rarer than creates), so leave `GetRoom` untouched. The function continues to use `*model.Room` for these paths. No change needed there.

- [ ] **Step 7: Verify the package builds**

Run:
```bash
go build ./broadcast-worker/...
```

Expected: success (handler tests will still fail to compile but the production code builds).

### Task 14: Regenerate mocks

**Files:**
- Modify: `broadcast-worker/mock_store_test.go` (regenerated)

- [ ] **Step 1: Regenerate**

Run:
```bash
make generate SERVICE=broadcast-worker
```

Expected: `mock_store_test.go` updated to mock the new Store interface (with `UpdateRoomLastMessage` and `GetRoomMeta`).

- [ ] **Step 2: Commit the store + handler + mock changes together**

```bash
git add broadcast-worker/store.go broadcast-worker/store_mongo.go broadcast-worker/handler.go broadcast-worker/mock_store_test.go
git commit -m "refactor(broadcast-worker): split FetchAndUpdateRoom into UpdateRoomLastMessage + GetRoomMeta"
```

### Task 15: Update broadcast-worker handler tests

**Files:**
- Modify: `broadcast-worker/handler_test.go`

- [ ] **Step 1: Update mock expectations**

In `broadcast-worker/handler_test.go`, every `store.EXPECT().FetchAndUpdateRoom(...)` call must become:

```go
store.EXPECT().UpdateRoomLastMessage(gomock.Any(), <roomID>, <msgID>, <msgTime>, <mentionAll>).Return(nil)
store.EXPECT().GetRoomMeta(gomock.Any(), <roomID>).Return(roommetacache.Meta{
    ID:        <roomID>,
    Type:      <type>,
    Name:      <name>,
    SiteID:    <siteID>,
    UserCount: <userCount>,
}, nil)
```

Find each of the 15 occurrences (the grep result from earlier) and rewrite them. For each one:

- The `testChannelRoom`/`testDMRoom` test fixture (a `*model.Room`) must be replaced with a `roommetacache.Meta` for the `GetRoomMeta` return value. Define a helper near the top of the test file:

```go
func metaOf(r *model.Room) roommetacache.Meta {
    return roommetacache.Meta{
        ID: r.ID, Type: r.Type, Name: r.Name, SiteID: r.SiteID, UserCount: r.UserCount,
    }
}
```

- Error cases that previously had `store.EXPECT().FetchAndUpdateRoom(...).Return(nil, errors.New(...))` split into:

  ```go
  store.EXPECT().UpdateRoomLastMessage(...).Return(errors.New("db error"))
  ```

  (No `GetRoomMeta` expectation in the error case — the handler returns before calling it.)

- The `mongo.ErrNoDocuments` test (line 681) becomes:

  ```go
  store.EXPECT().UpdateRoomLastMessage(...).Return(fmt.Errorf("update room last message ghost-room: %w", mongo.ErrNoDocuments))
  ```

Apply this pattern across all 15 expectations.

- [ ] **Step 2: Add `roommetacache` import**

```go
"github.com/hmchangw/chat/pkg/roommetacache"
```

- [ ] **Step 3: Run tests**

Run:
```bash
go test ./broadcast-worker/... -run "TestHandler|TestBuildRoomEvent" -v
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add broadcast-worker/handler_test.go
git commit -m "test(broadcast-worker): update handler test expectations for split store methods"
```

---

## Phase 3: broadcast-worker — wire the room metadata cache

### Task 16: Add `cachedMetaStore` wrapper

**Files:**
- Create: `broadcast-worker/metacache.go`
- Create: `broadcast-worker/metacache_test.go`

- [ ] **Step 1: Write the failing test**

Create `broadcast-worker/metacache_test.go`:

```go
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roommetacache"
)

func TestCachedMetaStore_GetRoomMeta_CachesResult(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	want := roommetacache.Meta{ID: "r1", Type: model.RoomTypeChannel, Name: "r1", SiteID: "site-a", UserCount: 3}
	inner.EXPECT().GetRoomMeta(gomock.Any(), "r1").Return(want, nil).Times(1)

	cached, err := newCachedMetaStore(inner, 10, time.Minute)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		got, err := cached.GetRoomMeta(context.Background(), "r1")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestCachedMetaStore_OtherMethodsPassThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	inner.EXPECT().UpdateRoomLastMessage(gomock.Any(), "r1", "m1", gomock.Any(), false).Return(nil).Times(1)
	inner.EXPECT().ListSubscriptions(gomock.Any(), "r1").Return(nil, nil).Times(1)
	inner.EXPECT().SetSubscriptionMentions(gomock.Any(), "r1", []string{"alice"}).Return(nil).Times(1)
	inner.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1"}, nil).Times(1)

	cached, err := newCachedMetaStore(inner, 10, time.Minute)
	require.NoError(t, err)

	_ = cached.UpdateRoomLastMessage(context.Background(), "r1", "m1", time.Now(), false)
	_, _ = cached.ListSubscriptions(context.Background(), "r1")
	_ = cached.SetSubscriptionMentions(context.Background(), "r1", []string{"alice"})
	_, _ = cached.GetRoom(context.Background(), "r1")
}

func TestCachedMetaStore_LoaderErrorReturned(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	wantErr := errors.New("boom")
	inner.EXPECT().GetRoomMeta(gomock.Any(), "r1").Return(roommetacache.Meta{}, wantErr).Times(2)

	cached, err := newCachedMetaStore(inner, 10, time.Minute)
	require.NoError(t, err)

	_, err = cached.GetRoomMeta(context.Background(), "r1")
	assert.ErrorIs(t, err, wantErr)
	_, err = cached.GetRoomMeta(context.Background(), "r1")
	assert.ErrorIs(t, err, wantErr)
}
```

- [ ] **Step 2: Run test, confirm it fails**

Run:
```bash
go test ./broadcast-worker/... -run TestCachedMetaStore -v
```

Expected: FAIL — `newCachedMetaStore` undefined.

- [ ] **Step 3: Implement the wrapper**

Create `broadcast-worker/metacache.go`:

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/roommetacache"
)

// cachedMetaStore wraps a Store with a roommetacache.Cache in front of
// GetRoomMeta. All other Store methods pass through unchanged.
type cachedMetaStore struct {
	Store
	cache *roommetacache.Cache
}

func newCachedMetaStore(inner Store, size int, ttl time.Duration) (*cachedMetaStore, error) {
	loader := func(ctx context.Context, roomID string) (roommetacache.Meta, error) {
		return inner.GetRoomMeta(ctx, roomID)
	}
	cache, err := roommetacache.New(size, ttl, loader)
	if err != nil {
		return nil, fmt.Errorf("build room meta cache: %w", err)
	}
	return &cachedMetaStore{Store: inner, cache: cache}, nil
}

// GetRoomMeta serves from the cache, falling through to the inner
// store on miss.
func (c *cachedMetaStore) GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error) {
	return c.cache.Get(ctx, roomID)
}
```

- [ ] **Step 4: Run test, confirm pass**

Run:
```bash
go test ./broadcast-worker/... -run TestCachedMetaStore -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add broadcast-worker/metacache.go broadcast-worker/metacache_test.go
git commit -m "feat(broadcast-worker): add cachedMetaStore wrapper around Store"
```

### Task 17: Wire the cache in broadcast-worker `main.go`

**Files:**
- Modify: `broadcast-worker/main.go`

- [ ] **Step 1: Add config fields**

In `broadcast-worker/main.go`, add to the `config` struct (after `UserCacheTTL`):

```go
RoomMetaCacheSize int           `env:"ROOM_META_CACHE_SIZE"      envDefault:"10000"`
RoomMetaCacheTTL  time.Duration `env:"ROOM_META_CACHE_TTL"       envDefault:"2m"`
```

- [ ] **Step 2: Wrap the store in `main()`**

Locate the line:

```go
store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
```

Below it, add:

```go
cachedStore, err := newCachedMetaStore(store, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL)
if err != nil {
    slog.Error("init room meta cache failed", "error", err)
    os.Exit(1)
}
slog.Info("room-meta-cache enabled", "size", cfg.RoomMetaCacheSize, "ttl", cfg.RoomMetaCacheTTL)
```

Then change the `NewHandler` call from `NewHandler(store, ...)` to `NewHandler(cachedStore, ...)`.

- [ ] **Step 3: Verify it builds**

Run:
```bash
go build ./broadcast-worker/...
```

Expected: success.

- [ ] **Step 4: Run all broadcast-worker unit tests**

Run:
```bash
go test ./broadcast-worker/... -race
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add broadcast-worker/main.go
git commit -m "feat(broadcast-worker): wire cachedMetaStore in main"
```

### Task 18: Update broadcast-worker integration test

**Files:**
- Modify: `broadcast-worker/integration_test.go`

- [ ] **Step 1: Add a new test asserting `lastMsgAt`/`lastMsgId` are persisted**

Append to `broadcast-worker/integration_test.go`, after the existing `TestBroadcastWorker_ChannelRoom_Integration`:

```go
func TestBroadcastWorker_PersistsLastMessage_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r-last", Name: "general", Type: model.RoomTypeChannel, UserCount: 2, SiteID: "site-a",
	})
	require.NoError(t, err)
	seedUsers(t, db)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
	cached, err := newCachedMetaStore(store, 10, time.Minute)
	require.NoError(t, err)

	pub := &recordingPublisher{}
	h := NewHandler(cached, userstore.NewMongoStore(db.Collection("users")), pub, &fakeRoomKeyProvider{}, false)

	msgTime := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	evt := model.MessageEvent{
		Event:     model.EventCreated,
		Timestamp: msgTime.UnixMilli(),
		Message: model.Message{
			ID:          "msg-last",
			RoomID:      "r-last",
			UserID:      "u-alice",
			UserAccount: "alice",
			Content:     "hi",
			CreatedAt:   msgTime,
		},
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	require.NoError(t, h.HandleMessage(ctx, data))

	// Verify the room doc now has lastMsgAt/lastMsgId persisted.
	var got struct {
		LastMsgAt time.Time `bson:"lastMsgAt"`
		LastMsgID string    `bson:"lastMsgId"`
	}
	err = db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r-last"}).Decode(&got)
	require.NoError(t, err)
	assert.Equal(t, "msg-last", got.LastMsgID)
	assert.WithinDuration(t, msgTime, got.LastMsgAt, time.Millisecond)
}
```

- [ ] **Step 2: Run integration test**

Run:
```bash
make test-integration SERVICE=broadcast-worker
```

Expected: all integration tests pass, including the new one.

- [ ] **Step 3: Commit**

```bash
git add broadcast-worker/integration_test.go
git commit -m "test(broadcast-worker): assert lastMessage update persists via new path"
```

---

## Phase 4: message-gatekeeper — store interface + cache wrappers

### Task 19: Update message-gatekeeper Store interface and Mongo impl

**Files:**
- Modify: `message-gatekeeper/store.go`
- Modify: `message-gatekeeper/store_mongo.go`

- [ ] **Step 1: Replace `GetRoomUserCount` with `GetRoomMeta`**

In `message-gatekeeper/store.go`, change the `Store` interface:

```go
type Store interface {
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error)
}
```

Add the import:

```go
"github.com/hmchangw/chat/pkg/roommetacache"
```

- [ ] **Step 2: Implement `GetRoomMeta` in `store_mongo.go`**

In `message-gatekeeper/store_mongo.go`, replace the `GetRoomUserCount` method:

```go
func (s *MongoStore) GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error) {
	opts := options.FindOne().SetProjection(bson.M{
		"type":      1,
		"name":      1,
		"siteId":    1,
		"userCount": 1,
	})
	var doc struct {
		ID        string         `bson:"_id"`
		Type      model.RoomType `bson:"type"`
		Name      string         `bson:"name"`
		SiteID    string         `bson:"siteId"`
		UserCount int            `bson:"userCount"`
	}
	if err := s.rooms.FindOne(ctx, bson.M{"_id": roomID}, opts).Decode(&doc); err != nil {
		return roommetacache.Meta{}, fmt.Errorf("get room meta %q: %w", roomID, err)
	}
	return roommetacache.Meta{
		ID:        doc.ID,
		Type:      doc.Type,
		Name:      doc.Name,
		SiteID:    doc.SiteID,
		UserCount: doc.UserCount,
	}, nil
}
```

Add the import:

```go
"github.com/hmchangw/chat/pkg/roommetacache"
```

- [ ] **Step 3: Update handler.go**

In `message-gatekeeper/handler.go`, lines 174–187, replace:

```go
if !isThreadReply && !canBypassLargeRoomCap(sub) {
    userCount, err := h.store.GetRoomUserCount(ctx, roomID)
    if err != nil {
        return nil, &infraError{cause: fmt.Errorf("get user count for room %s: %w", roomID, err)}
    }
    if userCount > h.largeRoomThreshold {
        ...
```

with:

```go
if !isThreadReply && !canBypassLargeRoomCap(sub) {
    meta, err := h.store.GetRoomMeta(ctx, roomID)
    if err != nil {
        return nil, &infraError{cause: fmt.Errorf("get room meta for %s: %w", roomID, err)}
    }
    if meta.UserCount > h.largeRoomThreshold {
        slog.Info("send blocked",
            "reason", codeLargeRoomPostRestricted,
            "account", account,
            "roomID", roomID,
            "userCount", meta.UserCount,
            "threshold", h.largeRoomThreshold,
        )
        return nil, errLargeRoomPostRestricted
    }
}
```

- [ ] **Step 4: Regenerate mocks**

Run:
```bash
make generate SERVICE=message-gatekeeper
```

- [ ] **Step 5: Update handler test expectations**

Find every occurrence of `GetRoomUserCount` in `message-gatekeeper/handler_test.go`:

```bash
grep -n "GetRoomUserCount" message-gatekeeper/handler_test.go
```

For each match, replace:

```go
store.EXPECT().GetRoomUserCount(gomock.Any(), "<roomID>").Return(<count>, nil)
```

with:

```go
store.EXPECT().GetRoomMeta(gomock.Any(), "<roomID>").Return(roommetacache.Meta{ID: "<roomID>", UserCount: <count>}, nil)
```

Error cases: `Return(0, err)` → `Return(roommetacache.Meta{}, err)`.

Add the import to the test file:

```go
"github.com/hmchangw/chat/pkg/roommetacache"
```

- [ ] **Step 6: Run gatekeeper tests**

Run:
```bash
go test ./message-gatekeeper/... -race
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add message-gatekeeper/store.go message-gatekeeper/store_mongo.go message-gatekeeper/handler.go message-gatekeeper/handler_test.go message-gatekeeper/mock_store_test.go
git commit -m "refactor(message-gatekeeper): replace GetRoomUserCount with GetRoomMeta"
```

### Task 20: Add `cachedMetaStore` wrapper in gatekeeper

**Files:**
- Create: `message-gatekeeper/metacache.go`
- Create: `message-gatekeeper/metacache_test.go`

- [ ] **Step 1: Write the failing test**

Create `message-gatekeeper/metacache_test.go`:

```go
package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roommetacache"
)

func TestCachedMetaStore_GetRoomMetaCaches(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	want := roommetacache.Meta{ID: "r1", UserCount: 5}
	inner.EXPECT().GetRoomMeta(gomock.Any(), "r1").Return(want, nil).Times(1)

	cached, err := newCachedMetaStore(inner, 10, time.Minute)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		got, err := cached.GetRoomMeta(context.Background(), "r1")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestCachedMetaStore_GetSubscriptionPassesThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	want := &model.Subscription{ID: "s1", RoomID: "r1"}
	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(want, nil).Times(1)

	cached, err := newCachedMetaStore(inner, 10, time.Minute)
	require.NoError(t, err)

	got, err := cached.GetSubscription(context.Background(), "alice", "r1")
	require.NoError(t, err)
	assert.Same(t, want, got)
}
```

- [ ] **Step 2: Run test, confirm it fails**

Run:
```bash
go test ./message-gatekeeper/... -run TestCachedMetaStore -v
```

Expected: FAIL — `newCachedMetaStore` undefined.

- [ ] **Step 3: Implement the wrapper**

Create `message-gatekeeper/metacache.go`:

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/roommetacache"
)

// cachedMetaStore wraps a Store with a roommetacache.Cache for room
// metadata. GetSubscription passes through to the inner store.
type cachedMetaStore struct {
	Store
	cache *roommetacache.Cache
}

func newCachedMetaStore(inner Store, size int, ttl time.Duration) (*cachedMetaStore, error) {
	loader := func(ctx context.Context, roomID string) (roommetacache.Meta, error) {
		return inner.GetRoomMeta(ctx, roomID)
	}
	cache, err := roommetacache.New(size, ttl, loader)
	if err != nil {
		return nil, fmt.Errorf("build room meta cache: %w", err)
	}
	return &cachedMetaStore{Store: inner, cache: cache}, nil
}

func (c *cachedMetaStore) GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error) {
	return c.cache.Get(ctx, roomID)
}
```

- [ ] **Step 4: Run test, confirm pass**

Run:
```bash
go test ./message-gatekeeper/... -run TestCachedMetaStore -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add message-gatekeeper/metacache.go message-gatekeeper/metacache_test.go
git commit -m "feat(message-gatekeeper): add cachedMetaStore wrapper"
```

### Task 21: Add subscription cache wrapper in gatekeeper

**Files:**
- Create: `message-gatekeeper/subcache.go`
- Create: `message-gatekeeper/subcache_test.go`

- [ ] **Step 1: Write the failing test**

Create `message-gatekeeper/subcache_test.go`:

```go
package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func TestCachedSubStore_HitMiss(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	want := &model.Subscription{
		User:  model.SubscriptionUser{Account: "alice"},
		Roles: []model.Role{model.RoleMember},
	}
	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(want, nil).Times(1)

	cached, err := newCachedSubStore(inner, 10, time.Minute)
	require.NoError(t, err)

	got, err := cached.GetSubscription(context.Background(), "alice", "r1")
	require.NoError(t, err)
	assert.Equal(t, "alice", got.User.Account)
	assert.Equal(t, []model.Role{model.RoleMember}, got.Roles)

	// Second call: cache hit, inner not called again.
	got2, err := cached.GetSubscription(context.Background(), "alice", "r1")
	require.NoError(t, err)
	assert.Equal(t, "alice", got2.User.Account)
	assert.Equal(t, []model.Role{model.RoleMember}, got2.Roles)
}

func TestCachedSubStore_NotSubscribedNotCached(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(nil, errNotSubscribed).Times(2)

	cached, err := newCachedSubStore(inner, 10, time.Minute)
	require.NoError(t, err)

	_, err = cached.GetSubscription(context.Background(), "alice", "r1")
	assert.ErrorIs(t, err, errNotSubscribed)
	_, err = cached.GetSubscription(context.Background(), "alice", "r1")
	assert.ErrorIs(t, err, errNotSubscribed)
}

func TestCachedSubStore_TransientErrorNotCached(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	boom := errors.New("transient")
	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(nil, boom).Times(2)

	cached, err := newCachedSubStore(inner, 10, time.Minute)
	require.NoError(t, err)

	_, err = cached.GetSubscription(context.Background(), "alice", "r1")
	assert.ErrorIs(t, err, boom)
	_, err = cached.GetSubscription(context.Background(), "alice", "r1")
	assert.ErrorIs(t, err, boom)
}

func TestCachedSubStore_TTLExpires(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	want := &model.Subscription{User: model.SubscriptionUser{Account: "alice"}, Roles: []model.Role{model.RoleMember}}
	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(want, nil).Times(2)

	cached, err := newCachedSubStore(inner, 10, 50*time.Millisecond)
	require.NoError(t, err)

	_, _ = cached.GetSubscription(context.Background(), "alice", "r1")
	_, _ = cached.GetSubscription(context.Background(), "alice", "r1")
	time.Sleep(75 * time.Millisecond)
	_, _ = cached.GetSubscription(context.Background(), "alice", "r1")
}

func TestCachedSubStore_SingleflightDedupsConcurrentMisses(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	var calls atomic.Int32
	gate := make(chan struct{})
	want := &model.Subscription{User: model.SubscriptionUser{Account: "alice"}, Roles: []model.Role{model.RoleMember}}
	inner.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		DoAndReturn(func(_ context.Context, _, _ string) (*model.Subscription, error) {
			calls.Add(1)
			<-gate
			return want, nil
		}).
		Times(1)

	cached, err := newCachedSubStore(inner, 10, time.Minute)
	require.NoError(t, err)

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = cached.GetSubscription(context.Background(), "alice", "r1")
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	assert.Equal(t, int32(1), calls.Load())
}

func TestCachedSubStore_GetRoomMetaPassesThrough(t *testing.T) {
	// GetRoomMeta is not cached by the sub cache; it passes through.
	// (Caching of GetRoomMeta is the metacache wrapper's job.)
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	inner.EXPECT().GetRoomMeta(gomock.Any(), "r1").Return(roommetacache.Meta{ID: "r1", UserCount: 1}, nil).Times(2)

	cached, err := newCachedSubStore(inner, 10, time.Minute)
	require.NoError(t, err)

	_, _ = cached.GetRoomMeta(context.Background(), "r1")
	_, _ = cached.GetRoomMeta(context.Background(), "r1")
}
```

Add the import:

```go
"github.com/hmchangw/chat/pkg/roommetacache"
```

- [ ] **Step 2: Run test, confirm it fails**

Run:
```bash
go test ./message-gatekeeper/... -run TestCachedSubStore -v
```

Expected: FAIL — `newCachedSubStore` undefined.

- [ ] **Step 3: Implement the wrapper**

Create `message-gatekeeper/subcache.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"

	"github.com/hmchangw/chat/pkg/model"
)

// cachedSubscription is the projection of model.Subscription that
// gatekeeper actually reads on the hot path. Caching only these fields
// (vs the full doc) keeps the cache tight and makes the contract
// explicit at the cache boundary.
type cachedSubscription struct {
	Account string
	Roles   []model.Role
}

type subKey struct {
	roomID  string
	account string
}

// cachedSubStore wraps a Store with an LRU+TTL cache of subscription
// lookups. Negative results (errNotSubscribed) and transient errors
// are NOT cached — see the spec for rationale. GetRoomMeta passes
// through unchanged.
type cachedSubStore struct {
	Store
	lru *lru.LRU[subKey, cachedSubscription]
	sf  singleflight.Group

	hits   atomic.Uint64
	misses atomic.Uint64
}

func newCachedSubStore(inner Store, size int, ttl time.Duration) (*cachedSubStore, error) {
	if size <= 0 {
		return nil, fmt.Errorf("subcache: size must be positive, got %d", size)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("subcache: ttl must be positive, got %v", ttl)
	}
	return &cachedSubStore{
		Store: inner,
		lru:   lru.NewLRU[subKey, cachedSubscription](size, nil, ttl),
	}, nil
}

func (c *cachedSubStore) GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	key := subKey{roomID: roomID, account: account}
	if v, ok := c.lru.Get(key); ok {
		c.hits.Add(1)
		return fromCached(v), nil
	}
	c.misses.Add(1)

	sfKey := roomID + "\x00" + account
	v, err, _ := c.sf.Do(sfKey, func() (interface{}, error) {
		if cached, ok := c.lru.Get(key); ok {
			return cached, nil
		}
		sub, err := c.Store.GetSubscription(ctx, account, roomID)
		if err != nil {
			// Do not cache errNotSubscribed or transient errors — see spec.
			return nil, err
		}
		projected := cachedSubscription{
			Account: sub.User.Account,
			Roles:   append([]model.Role(nil), sub.Roles...),
		}
		c.lru.Add(key, projected)
		return projected, nil
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, errors.New("subcache: nil value from loader") // unreachable
	}
	return fromCached(v.(cachedSubscription)), nil
}

// fromCached builds a partial *model.Subscription from the cached
// projection. Only the fields gatekeeper reads (User.Account, Roles)
// are populated; other fields are zero. This is intentional — the
// cache contract is the projection, not the full doc.
func fromCached(c cachedSubscription) *model.Subscription {
	return &model.Subscription{
		User:  model.SubscriptionUser{Account: c.Account},
		Roles: append([]model.Role(nil), c.Roles...),
	}
}
```

- [ ] **Step 4: Run test, confirm pass**

Run:
```bash
go test ./message-gatekeeper/... -run TestCachedSubStore -race -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add message-gatekeeper/subcache.go message-gatekeeper/subcache_test.go
git commit -m "feat(message-gatekeeper): add cachedSubStore wrapper with no-negative-caching policy"
```

### Task 22: Wire both caches in message-gatekeeper `main.go`

**Files:**
- Modify: `message-gatekeeper/main.go`

- [ ] **Step 1: Add config fields**

In `message-gatekeeper/main.go`, add to the `config` struct (after `LargeRoomThreshold`):

```go
SubCacheSize      int           `env:"GATEKEEPER_SUB_CACHE_SIZE"  envDefault:"100000"`
SubCacheTTL       time.Duration `env:"GATEKEEPER_SUB_CACHE_TTL"   envDefault:"2m"`
RoomMetaCacheSize int           `env:"ROOM_META_CACHE_SIZE"       envDefault:"10000"`
RoomMetaCacheTTL  time.Duration `env:"ROOM_META_CACHE_TTL"        envDefault:"2m"`
```

- [ ] **Step 2: Wrap the store in `main()`**

Replace the line:

```go
store := NewMongoStore(db)
```

with:

```go
mongoStore := NewMongoStore(db)
withMeta, err := newCachedMetaStore(mongoStore, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL)
if err != nil {
    slog.Error("init room meta cache failed", "error", err)
    os.Exit(1)
}
store, err := newCachedSubStore(withMeta, cfg.SubCacheSize, cfg.SubCacheTTL)
if err != nil {
    slog.Error("init subscription cache failed", "error", err)
    os.Exit(1)
}
slog.Info("gatekeeper caches enabled",
    "sub_cache_size", cfg.SubCacheSize, "sub_cache_ttl", cfg.SubCacheTTL,
    "room_meta_cache_size", cfg.RoomMetaCacheSize, "room_meta_cache_ttl", cfg.RoomMetaCacheTTL,
)
```

- [ ] **Step 3: Verify it builds**

Run:
```bash
go build ./message-gatekeeper/...
```

Expected: success.

- [ ] **Step 4: Run all gatekeeper tests**

Run:
```bash
go test ./message-gatekeeper/... -race
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add message-gatekeeper/main.go
git commit -m "feat(message-gatekeeper): wire subscription + room meta caches in main"
```

---

## Phase 5: Full verification

### Task 23: Run full test suite

- [ ] **Step 1: Lint**

Run:
```bash
make lint
```

Expected: zero issues.

- [ ] **Step 2: Format**

Run:
```bash
make fmt
```

If changes are made, commit them:
```bash
git add -A
git commit -m "chore: gofmt + goimports"
```

- [ ] **Step 3: Full unit test suite with race detector**

Run:
```bash
make test
```

Expected: all tests pass.

- [ ] **Step 4: Coverage check for the new files**

Run:
```bash
go test ./pkg/roommetacache/... -coverprofile=/tmp/cover-rmc.out
go tool cover -func=/tmp/cover-rmc.out | tail -3
```

Expected: ≥80% coverage.

```bash
go test ./broadcast-worker/... -coverprofile=/tmp/cover-bw.out
go tool cover -func=/tmp/cover-bw.out | grep -E "metacache.go" | head
```

Expected: ≥80% coverage on `metacache.go`.

```bash
go test ./message-gatekeeper/... -coverprofile=/tmp/cover-gk.out
go tool cover -func=/tmp/cover-gk.out | grep -E "subcache.go|metacache.go"
```

Expected: ≥80% coverage on both files.

- [ ] **Step 5: Full integration test suite**

Run:
```bash
make test-integration
```

Expected: all integration tests pass. (Requires Docker; may take several minutes.)

- [ ] **Step 6: Commit any final cleanup**

If any cleanup was needed in earlier steps and is not yet committed, commit it now with a focused message.

### Task 24: Loadgen verification (manual, before-after measurement)

**Files:** none modified; this is a measurement step.

- [ ] **Step 1: Capture baseline (before changes)**

Check out the parent commit (the one immediately before Task 1 was committed) on a separate worktree or branch. Run:

```bash
cd tools/loadgen/deploy
make up
make seed PRESET=large
make run PRESET=large RATE=500 DURATION=60s
```

In another shell during the run:
```bash
docker stats --no-stream loadgen-mongodb-1
docker exec loadgen-mongodb-1 mongosh chat --eval 'JSON.stringify(db.serverStatus().opcounters)'
```

Record:
- MongoDB CPU % at steady state
- `opcounters` delta over the 60s window
- loadgen summary `final_pending` and error counts

Run `make down` when done.

- [ ] **Step 2: Capture post-change numbers**

Check out the branch with all changes applied. Re-run the same workflow:

```bash
cd tools/loadgen/deploy
make up
make seed PRESET=large
make run PRESET=large RATE=500 DURATION=60s
```

Repeat the same observations.

- [ ] **Step 3: Document numbers in a PR-ready comment**

Write a short summary capturing:
- MongoDB CPU% before vs. after
- `opcounters.query` count before vs. after (should drop by ~95%)
- `opcounters.update` count before vs. after (should be roughly unchanged — same number of writes, just via `UpdateOne` instead of `FindOneAndUpdate`)
- loadgen latency and pending-count comparison

Save this as a note for the PR description. Do not commit it to the repo — it is PR metadata.

---

## Final Push

- [ ] **Push the branch**

Run:
```bash
git push origin claude/investigate-mongodb-cpu-zQG01
```

Do NOT create a pull request unless explicitly requested by the user.

---

## Notes for the implementer

- **Existing in-house cache precedent:** `broadcast-worker/usercache.go` uses a hand-rolled `container/list` LRU with TTL. This plan intentionally uses `hashicorp/golang-lru/v2` for the new caches per the spec. Do not unify with `usercache.go` as part of this work — that is a separate refactor.
- **Prometheus metrics in spec but deferred:** the spec's Rollout section mentions Prometheus counters. Broadcast-worker and message-gatekeeper do not currently expose a metrics endpoint; wiring one is out of scope here. The `Stats()` method on `pkg/roommetacache.Cache` and the `hits`/`misses` atomic counters on `cachedSubStore` are the foundation for a future Prometheus PR.
- **No active invalidation in v1:** the `Invalidate(roomID)` method on the cache is present but never called. The spec explains why; a future PR adding NATS-driven invalidation will plug in here without an interface change.
- **TDD discipline:** every implementation task starts with a failing test. If you find yourself writing implementation before the test, stop and rewind to the test step. The Red phase is non-negotiable per CLAUDE.md.
- **Commits are deliberately small:** each task ends with a focused commit. Do not squash; small commits make `git bisect` useful if a regression slips through.
