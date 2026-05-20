# Cache Hit-Rate Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every active cache's hit/miss/size observable via a unified Prometheus metric family (`chat_cache_hits_total`, `chat_cache_misses_total`, `chat_cache_size`), plus an opt-in periodic slog summary.

**Architecture:** A new `pkg/cachestats` package owns the metric vectors and a recorder registry. Each cache holds a `*cachestats.Recorder` and calls `Hit()` / `Miss()` on its existing hot path. Counter handles are pre-resolved at `Register` time so the hot path is one atomic increment, no label lookup. The package is instantiable (`cachestats.New(registerer)`) rather than globally stateful, so tests use isolated `prometheus.Registry` instances.

**Tech Stack:** Go 1.25, `github.com/prometheus/client_golang`, `log/slog`, `caarlos0/env/v11`.

**Spec:** `docs/superpowers/specs/2026-05-20-cache-hit-rate-visibility-design.md`.

---

## File Structure

### Created

| File | Responsibility |
|---|---|
| `pkg/cachestats/cachestats.go` | `Stats` container, `Recorder` type, `New`, `Register`, `Hit`, `Miss`, `Snapshot`, `StartLogger`. |
| `pkg/cachestats/cachestats_test.go` | Unit tests for the above. |
| `pkg/cachestats/logger_test.go` | Periodic-logger tests with an injected ticker and captured slog handler. |

### Modified

| File | Change |
|---|---|
| `pkg/roommetacache/roommetacache.go` | `New` and `WrapStore` take `*cachestats.Recorder`. Remove `loadErrs` and `LoadErrors`. `Stats()` removed. |
| `pkg/roommetacache/roommetacache_test.go` | Migrate `Stats()` assertions to `Recorder.Snapshot()`. |
| `message-gatekeeper/subcache.go` | Drop local `hits`/`misses` atomics + `Stats()`. Take `*cachestats.Recorder`. |
| `message-gatekeeper/subcache_test.go` | Migrate. |
| `message-gatekeeper/metacache.go` | Constructor accepts recorder; pass through to `roommetacache.WrapStore`. |
| `message-gatekeeper/main.go` | Construct `*cachestats.Stats`, register caches, add `/metrics` listener, start logger. New config fields. |
| `broadcast-worker/usercache.go` | Add `*cachestats.Recorder` field and `Hit`/`Miss` calls inside `FindUsersByAccounts`. |
| `broadcast-worker/usercache_test.go` | New cases for hit/miss counting. |
| `broadcast-worker/metacache.go` | Constructor accepts recorder; pass through. |
| `broadcast-worker/main.go` | Same wiring as gatekeeper. |
| `search-service/store_valkey.go` | Add recorder field; `Hit`/`Miss` in `GetRestricted`. |
| `search-service/handler_test.go` | Extend restricted-rooms tests. |
| `search-service/main.go` | Construct `*cachestats.Stats`, register `restricted_rooms`, pass recorder to `newValkeyCache`, start logger. New config fields. |

---

## Task 1: pkg/cachestats — Recorder + counters

**Files:**
- Create: `pkg/cachestats/cachestats.go`
- Test: `pkg/cachestats/cachestats_test.go`

- [ ] **Step 1.1: Write the failing test**

Create `pkg/cachestats/cachestats_test.go`:

```go
package cachestats

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStats(t *testing.T) (*Stats, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	s := New(reg)
	return s, reg
}

func TestRegister_HitMissIncrementCounters(t *testing.T) {
	s, _ := newStats(t)
	r := s.Register("subscription", nil)
	r.Hit()
	r.Hit()
	r.Miss()

	hits, misses := r.Snapshot()
	assert.Equal(t, uint64(2), hits)
	assert.Equal(t, uint64(1), misses)
}

func TestRegister_NilSafeOnRecorder(t *testing.T) {
	var r *Recorder
	// must not panic
	r.Hit()
	r.Miss()
	hits, misses := r.Snapshot()
	assert.Equal(t, uint64(0), hits)
	assert.Equal(t, uint64(0), misses)
}

func TestRegister_PreCreatesLabeledSeries(t *testing.T) {
	s, reg := newStats(t)
	_ = s.Register("subscription", nil)

	// Series must exist with value 0 before any Hit/Miss is called.
	assert.Equal(t, float64(0), testutil.ToFloat64(s.hits.WithLabelValues("subscription")))
	assert.Equal(t, float64(0), testutil.ToFloat64(s.misses.WithLabelValues("subscription")))

	got, err := testutil.GatherAndCount(reg, "chat_cache_hits_total", "chat_cache_misses_total")
	require.NoError(t, err)
	assert.Equal(t, 2, got, "expected one hits + one misses series")
}

func TestRegister_SizeGaugeRegisteredWhenSizeFnProvided(t *testing.T) {
	s, reg := newStats(t)
	size := 0
	_ = s.Register("roommeta", func() int { return size })
	size = 42

	got, err := testutil.GatherAndCount(reg, "chat_cache_size")
	require.NoError(t, err)
	assert.Equal(t, 1, got, "expected exactly one chat_cache_size series")
}

func TestRegister_SizeGaugeOmittedWhenSizeFnNil(t *testing.T) {
	s, reg := newStats(t)
	_ = s.Register("subscription", nil)

	got, err := testutil.GatherAndCount(reg, "chat_cache_size")
	require.NoError(t, err)
	assert.Equal(t, 0, got)
}

func TestRegister_PanicsOnEmptyName(t *testing.T) {
	s, _ := newStats(t)
	assert.PanicsWithValue(t, "cachestats: empty cache name", func() {
		s.Register("", nil)
	})
}

func TestRegister_PanicsOnDuplicateName(t *testing.T) {
	s, _ := newStats(t)
	_ = s.Register("subscription", nil)
	defer func() {
		r := recover()
		require.NotNil(t, r)
		assert.True(t, strings.Contains(r.(string), "already registered"))
	}()
	s.Register("subscription", nil)
}

func TestNew_NilRegistererFallsBackToDefault(t *testing.T) {
	// Construct with nil; should not panic, and the returned Stats
	// must accept Register calls. Use a unique cache name so this
	// test does not collide with the default registry across runs.
	s := New(nil)
	require.NotNil(t, s)
	name := "test_default_registerer_" + t.Name()
	r := s.Register(name, nil)
	r.Hit()
	hits, _ := r.Snapshot()
	assert.Equal(t, uint64(1), hits)
}
```

- [ ] **Step 1.2: Run tests to verify they fail**

Run: `go test ./pkg/cachestats/... -run . -v`
Expected: build error — package does not exist.

- [ ] **Step 1.3: Implement `pkg/cachestats/cachestats.go`**

```go
// Package cachestats publishes hit/miss/size metrics for every cache
// in the owning service to a single unified Prometheus metric family
// (chat_cache_hits_total, chat_cache_misses_total, chat_cache_size),
// and optionally emits a periodic slog summary line per cache.
//
// The package exposes a Stats container rather than package-level
// state so each service owns its own registry and tests can use
// isolated *prometheus.Registry instances without cross-test bleed.
package cachestats

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Stats owns the unified hit/miss CounterVecs and the per-cache
// Recorder registry for one process. Construct one per service.
type Stats struct {
	hits   *prometheus.CounterVec
	misses *prometheus.CounterVec

	reg prometheus.Registerer

	mu        sync.Mutex
	recorders map[string]*Recorder
}

// New constructs a Stats and registers the hit/miss vectors with reg.
// Pass prometheus.DefaultRegisterer in production; pass an isolated
// prometheus.NewRegistry() in tests. A nil reg falls back to the
// default registerer.
func New(reg prometheus.Registerer) *Stats {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	s := &Stats{
		hits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "chat_cache_hits_total",
			Help: "Total cache hits, partitioned by cache name.",
		}, []string{"cache"}),
		misses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "chat_cache_misses_total",
			Help: "Total cache misses, partitioned by cache name.",
		}, []string{"cache"}),
		reg:       reg,
		recorders: map[string]*Recorder{},
	}
	reg.MustRegister(s.hits, s.misses)
	return s
}

// Recorder is the per-cache instrumentation handle. Hit and Miss each
// perform a single atomic increment on a pre-resolved Prometheus
// counter — no map lookup, no label resolution on the hot path.
type Recorder struct {
	name   string
	hits   prometheus.Counter
	misses prometheus.Counter
	sizeFn func() int
}

// Register returns a Recorder for the named cache. sizeFn may be nil
// for caches that cannot cheaply report length (e.g. Valkey-backed);
// when non-nil, a chat_cache_size{cache=name} GaugeFunc is registered
// and sampled only at scrape time.
//
// Panics on empty name or duplicate registration — both are
// programmer errors caught at startup.
func (s *Stats) Register(name string, sizeFn func() int) *Recorder {
	if name == "" {
		panic("cachestats: empty cache name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.recorders[name]; exists {
		panic("cachestats: cache already registered: " + name)
	}
	r := &Recorder{
		name:   name,
		hits:   s.hits.WithLabelValues(name),
		misses: s.misses.WithLabelValues(name),
		sizeFn: sizeFn,
	}
	// Touch both counters so the labeled series exist at scrape time
	// even before traffic arrives. Dashboards see 0, not "no data".
	r.hits.Add(0)
	r.misses.Add(0)
	if sizeFn != nil {
		gauge := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name:        "chat_cache_size",
			Help:        "Current size of an in-process cache, sampled at scrape time.",
			ConstLabels: prometheus.Labels{"cache": name},
		}, func() float64 { return float64(sizeFn()) })
		s.reg.MustRegister(gauge)
	}
	s.recorders[name] = r
	return r
}

// Hit increments the cache's hit counter. Safe for concurrent use.
// No-op on a nil receiver so callers and tests can omit a recorder
// without branching at the call site.
func (r *Recorder) Hit() {
	if r == nil {
		return
	}
	r.hits.Inc()
}

// Miss increments the cache's miss counter. See Hit for nil semantics.
func (r *Recorder) Miss() {
	if r == nil {
		return
	}
	r.misses.Inc()
}

// Snapshot returns the current counter values. Implemented by reading
// the Prometheus counters via prometheus.Metric.Write — allocates a
// dto.Metric, intentionally off the cache hot path. Used by tests and
// the periodic logger only.
func (r *Recorder) Snapshot() (hits, misses uint64) {
	if r == nil {
		return 0, 0
	}
	return readCounter(r.hits), readCounter(r.misses)
}

func readCounter(c prometheus.Counter) uint64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return 0
	}
	return uint64(m.GetCounter().GetValue())
}

// snapshotAll returns a stable slice of recorders. Used by the
// periodic logger.
func (s *Stats) snapshotAll() []*Recorder {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Recorder, 0, len(s.recorders))
	for _, r := range s.recorders {
		out = append(out, r)
	}
	return out
}

// StartLogger emits one slog.Info line per registered cache every
// interval, until ctx is cancelled OR the returned stop function is
// called. A non-positive interval starts no goroutine and returns a
// no-op stop. A nil logger defaults to slog.Default().
//
// Callers wire stop into their shutdown chain so the goroutine exits
// before the process terminates.
func (s *Stats) StartLogger(ctx context.Context, interval time.Duration, logger *slog.Logger) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	if logger == nil {
		logger = slog.Default()
	}
	loggerCtx, cancel := context.WithCancel(ctx)
	go s.runLogger(loggerCtx, interval, logger)
	return cancel
}

func (s *Stats) runLogger(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.emitLogLines(logger)
		}
	}
}

func (s *Stats) emitLogLines(logger *slog.Logger) {
	for _, r := range s.snapshotAll() {
		hits, misses := r.Snapshot()
		total := hits + misses
		var rate float64
		if total > 0 {
			rate = float64(hits) / float64(total)
		}
		attrs := []any{
			"cache", r.name,
			"hits", hits,
			"misses", misses,
			"hit_rate", rate,
		}
		if r.sizeFn != nil {
			attrs = append(attrs, "size", r.sizeFn())
		}
		logger.Info("cache stats", attrs...)
	}
}
```

- [ ] **Step 1.4: Run tests to verify they pass**

Run: `go test ./pkg/cachestats/... -race -v`
Expected: all tests PASS.

- [ ] **Step 1.5: Write logger test**

Create `pkg/cachestats/logger_test.go`:

```go
package cachestats

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureHandler stores each slog record it sees as a JSON line so
// tests can decode and assert without coupling to slog's text format.
type captureHandler struct {
	mu    sync.Mutex
	lines []map[string]any
	inner slog.Handler
}

func newCaptureHandler() (*captureHandler, *slog.Logger) {
	var buf bytes.Buffer
	jh := slog.NewJSONHandler(&buf, nil)
	h := &captureHandler{inner: jh}
	return h, slog.New(h)
}

func (h *captureHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *captureHandler) Handle(ctx context.Context, r slog.Record) error {
	var buf bytes.Buffer
	if err := slog.NewJSONHandler(&buf, nil).Handle(ctx, r); err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		return err
	}
	h.mu.Lock()
	h.lines = append(h.lines, m)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	return &captureHandler{inner: h.inner.WithGroup(name)}
}

func (h *captureHandler) snapshot() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]map[string]any, len(h.lines))
	copy(out, h.lines)
	return out
}

func TestStartLogger_EmitsLinePerCachePerTick(t *testing.T) {
	s, _ := newStats(t)
	r1 := s.Register("subscription", nil)
	r2 := s.Register("roommeta", func() int { return 7 })

	r1.Hit()
	r1.Hit()
	r1.Miss()
	r2.Hit()

	cap, logger := newCaptureHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := s.StartLogger(ctx, 10*time.Millisecond, logger)
	defer stop()

	// Wait for at least one tick to fire.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(cap.snapshot()) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()

	lines := cap.snapshot()
	require.GreaterOrEqual(t, len(lines), 2, "expected at least one line per cache")

	// Find the most recent line for each cache.
	byCache := map[string]map[string]any{}
	for _, l := range lines {
		byCache[l["cache"].(string)] = l
	}

	sub := byCache["subscription"]
	require.NotNil(t, sub)
	assert.Equal(t, "cache stats", sub["msg"])
	assert.EqualValues(t, 2, sub["hits"])
	assert.EqualValues(t, 1, sub["misses"])
	assert.InDelta(t, 0.6666, sub["hit_rate"].(float64), 0.001)
	_, hasSize := sub["size"]
	assert.False(t, hasSize, "subscription has nil sizeFn; should not include size field")

	meta := byCache["roommeta"]
	require.NotNil(t, meta)
	assert.EqualValues(t, 1, meta["hits"])
	assert.EqualValues(t, 0, meta["misses"])
	assert.EqualValues(t, 1, meta["hit_rate"])
	assert.EqualValues(t, 7, meta["size"])
}

func TestStartLogger_NonPositiveIntervalIsNoop(t *testing.T) {
	s, _ := newStats(t)
	_ = s.Register("subscription", nil)

	cap, logger := newCaptureHandler()
	stop := s.StartLogger(context.Background(), 0, logger)
	defer stop()

	time.Sleep(20 * time.Millisecond)
	assert.Empty(t, cap.snapshot())
}

func TestStartLogger_StopFunctionCancelsGoroutine(t *testing.T) {
	s, _ := newStats(t)
	_ = s.Register("subscription", nil)

	cap, logger := newCaptureHandler()
	stop := s.StartLogger(context.Background(), 5*time.Millisecond, logger)

	// Let it tick at least once.
	time.Sleep(30 * time.Millisecond)
	stop()

	// Record count after cancellation.
	before := len(cap.snapshot())

	// Wait longer than the tick interval; count must not grow.
	time.Sleep(50 * time.Millisecond)
	after := len(cap.snapshot())
	assert.Equal(t, before, after, "stop should halt further log lines")
}

func TestStartLogger_NilLoggerUsesSlogDefault(t *testing.T) {
	// Smoke test: nil logger must not panic and must start the goroutine.
	s, _ := newStats(t)
	_ = s.Register("subscription", nil)

	stop := s.StartLogger(context.Background(), 1*time.Hour, nil)
	require.NotNil(t, stop)
	stop()
}

func TestStartLogger_ZeroDenominatorYieldsZeroRate(t *testing.T) {
	s, _ := newStats(t)
	_ = s.Register("subscription", nil)

	cap, logger := newCaptureHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := s.StartLogger(ctx, 5*time.Millisecond, logger)
	defer stop()

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && len(cap.snapshot()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	stop()

	lines := cap.snapshot()
	require.NotEmpty(t, lines)
	last := lines[len(lines)-1]
	assert.Contains(t, []any{0, float64(0), int64(0)}, last["hit_rate"], "rate should be 0 when no traffic")
	_ = strings.TrimSpace // keep imports honest
}
```

- [ ] **Step 1.6: Run logger tests**

Run: `go test ./pkg/cachestats/... -race -v`
Expected: all tests PASS.

- [ ] **Step 1.7: Lint**

Run: `make lint`
Expected: no errors.

- [ ] **Step 1.8: Commit**

```bash
git add pkg/cachestats/
git commit -m "feat(cachestats): add unified Prometheus cache hit/miss/size metrics + slog summary"
```

---

## Task 2: pkg/roommetacache — accept Recorder, remove LoadErrors

**Files:**
- Modify: `pkg/roommetacache/roommetacache.go`
- Modify: `pkg/roommetacache/roommetacache_test.go`
- Modify: `message-gatekeeper/metacache.go` (constructor signature ripple)
- Modify: `broadcast-worker/metacache.go` (constructor signature ripple)

This task changes the public API of `roommetacache.New` and `WrapStore` to accept a `*cachestats.Recorder`. Both consumer wrappers are updated in the same commit so the build stays green. The consuming services are updated in later tasks; for now they pass `nil` (recorder is nil-safe).

- [ ] **Step 2.1: Update the failing test**

Edit `pkg/roommetacache/roommetacache_test.go`. Find tests that call `c.Stats()` and rewrite them to use a recorder. Read the file first to locate the assertions.

Replace the existing `Stats`-based tests with:

```go
func TestCache_RecorderCountsHitsAndMisses(t *testing.T) {
	stats := cachestats.New(prometheus.NewRegistry())
	rec := stats.Register("test_roommeta", nil)

	calls := 0
	loader := func(ctx context.Context, roomID string) (Meta, error) {
		calls++
		return Meta{ID: roomID}, nil
	}
	c, err := New(10, time.Minute, loader, rec)
	require.NoError(t, err)

	_, err = c.Get(context.Background(), "r1") // miss
	require.NoError(t, err)
	_, err = c.Get(context.Background(), "r1") // hit
	require.NoError(t, err)
	_, err = c.Get(context.Background(), "r1") // hit
	require.NoError(t, err)

	hits, misses := rec.Snapshot()
	assert.Equal(t, uint64(2), hits)
	assert.Equal(t, uint64(1), misses)
	assert.Equal(t, 1, calls)
}

func TestCache_NilRecorderIsSafe(t *testing.T) {
	loader := func(ctx context.Context, roomID string) (Meta, error) {
		return Meta{ID: roomID}, nil
	}
	c, err := New(10, time.Minute, loader, nil)
	require.NoError(t, err)
	_, err = c.Get(context.Background(), "r1")
	assert.NoError(t, err)
}
```

Add these imports to the test file if missing:

```go
"github.com/prometheus/client_golang/prometheus"
"github.com/hmchangw/chat/pkg/cachestats"
```

Remove any test that asserted on `LoadErrors` or `Stats()` directly.

- [ ] **Step 2.2: Run tests to verify they fail**

Run: `go test ./pkg/roommetacache/... -run TestCache_Recorder -v`
Expected: build error — `New` does not take a fourth argument, `cachestats` import unused.

- [ ] **Step 2.3: Update `pkg/roommetacache/roommetacache.go`**

Apply these edits:

Drop the `loadErrs` field and `LoadErrors`/`Hits`/`Misses` from `Stats`. Drop the `Stats` type entirely (and the `Stats()` method).

Replace the struct definition:

```go
type Cache struct {
	lru    *lru.LRU[string, Meta]
	loader Loader
	sf     singleflight.Group
	rec    *cachestats.Recorder
}
```

Update `New`:

```go
func New(size int, ttl time.Duration, loader Loader, rec *cachestats.Recorder) (*Cache, error) {
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
		rec:    rec,
	}, nil
}
```

Update `Get`:

```go
func (c *Cache) Get(ctx context.Context, roomID string) (Meta, error) {
	if v, ok := c.lru.Get(roomID); ok {
		c.rec.Hit()
		return v, nil
	}
	c.rec.Miss()

	v, err, _ := c.sf.Do(roomID, func() (interface{}, error) {
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
		return Meta{}, fmt.Errorf("get room meta for %q: %w", roomID, err)
	}
	return v.(Meta), nil
}
```

Delete the `Stats` struct and `Stats()` method. Delete the `loadErrs` atomic field. Add `Len()` so callers can supply a `sizeFn`:

```go
// Len returns the current entry count. Used by callers that want to
// register a chat_cache_size gauge via cachestats.
func (c *Cache) Len() int { return c.lru.Len() }
```

Update `WrapStore`:

```go
func WrapStore[S MetaProvider](inner S, size int, ttl time.Duration, rec *cachestats.Recorder) (*Wrapper[S], error) {
	loader := func(ctx context.Context, roomID string) (Meta, error) {
		return inner.GetRoomMeta(ctx, roomID)
	}
	cache, err := New(size, ttl, loader, rec)
	if err != nil {
		return nil, err
	}
	return &Wrapper[S]{S: inner, cache: cache}, nil
}
```

Expose `Len` on the Wrapper:

```go
// Len returns the current entry count of the underlying Cache.
func (w *Wrapper[S]) Len() int { return w.cache.Len() }
```

Update the package import block to add:

```go
"github.com/hmchangw/chat/pkg/cachestats"
```

Remove the `sync/atomic` import if no longer used.

- [ ] **Step 2.4: Update consumer wrappers**

Edit `message-gatekeeper/metacache.go`:

```go
package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/cachestats"
	"github.com/hmchangw/chat/pkg/roommetacache"
)

type cachedMetaStore struct {
	Store
	cache *roommetacache.Wrapper[Store]
}

func newCachedMetaStore(inner Store, size int, ttl time.Duration, rec *cachestats.Recorder) (*cachedMetaStore, error) {
	w, err := roommetacache.WrapStore(inner, size, ttl, rec)
	if err != nil {
		return nil, err
	}
	return &cachedMetaStore{Store: w.S, cache: w}, nil
}

func (c *cachedMetaStore) GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error) {
	return c.cache.GetRoomMeta(ctx, roomID)
}

// Len exposes the cache size for the cachestats sizeFn.
func (c *cachedMetaStore) Len() int { return c.cache.Len() }
```

Edit `broadcast-worker/metacache.go` to match (same body — both wrappers are identical except for the surrounding Store interface they embed).

Update the existing call sites in `message-gatekeeper/main.go` and `broadcast-worker/main.go` to pass `nil` for the recorder argument (Task 4 and 6 will replace this with a real recorder):

In `message-gatekeeper/main.go`, change:
```go
withMeta, err := newCachedMetaStore(mongoStore, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL)
```
to:
```go
withMeta, err := newCachedMetaStore(mongoStore, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL, nil)
```

In `broadcast-worker/main.go`, change:
```go
cachedStore, err := newCachedMetaStore(store, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL)
```
to:
```go
cachedStore, err := newCachedMetaStore(store, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL, nil)
```

- [ ] **Step 2.5: Run tests**

Run: `go test ./pkg/roommetacache/... -race -v && go build ./...`
Expected: all tests PASS; full build succeeds.

- [ ] **Step 2.6: Commit**

```bash
git add pkg/roommetacache/ message-gatekeeper/metacache.go broadcast-worker/metacache.go message-gatekeeper/main.go broadcast-worker/main.go
git commit -m "refactor(roommetacache): take cachestats.Recorder; drop LoadErrors and Stats"
```

---

## Task 3: message-gatekeeper subcache — Recorder migration

**Files:**
- Modify: `message-gatekeeper/subcache.go`
- Modify: `message-gatekeeper/subcache_test.go`
- Modify: `message-gatekeeper/main.go` (constructor signature ripple — still nil)

- [ ] **Step 3.1: Update the failing test**

Edit `message-gatekeeper/subcache_test.go`. Replace `TestCachedSubStore_StatsTrackHitsAndMisses` with:

```go
func TestCachedSubStore_RecorderCountsHitsAndMisses(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)
	want := &model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}}
	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(want, nil).Times(1)

	stats := cachestats.New(prometheus.NewRegistry())
	rec := stats.Register("subscription", nil)

	cached, err := newCachedSubStore(inner, 10, time.Minute, rec)
	require.NoError(t, err)

	_, _ = cached.GetSubscription(context.Background(), "alice", "r1") // miss
	_, _ = cached.GetSubscription(context.Background(), "alice", "r1") // hit
	_, _ = cached.GetSubscription(context.Background(), "alice", "r1") // hit

	hits, misses := rec.Snapshot()
	assert.Equal(t, uint64(2), hits)
	assert.Equal(t, uint64(1), misses)
}

func TestCachedSubStore_NilRecorderIsSafe(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)
	want := &model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}}
	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(want, nil).Times(1)

	cached, err := newCachedSubStore(inner, 10, time.Minute, nil)
	require.NoError(t, err)
	_, err = cached.GetSubscription(context.Background(), "alice", "r1")
	assert.NoError(t, err)
}
```

Also locate every other call to `newCachedSubStore(inner, size, ttl)` in this file and add a fourth `nil` argument.

Add imports:

```go
"github.com/prometheus/client_golang/prometheus"
"github.com/hmchangw/chat/pkg/cachestats"
```

- [ ] **Step 3.2: Run test to verify it fails**

Run: `go test ./message-gatekeeper/... -run TestCachedSubStore_Recorder -v`
Expected: build error — `newCachedSubStore` does not take a fourth argument.

- [ ] **Step 3.3: Update `message-gatekeeper/subcache.go`**

Remove the `hits` and `misses` atomic fields, the `SubStats` type, and the `Stats()` method. Add a `rec` field and use it in `GetSubscription`.

Replace the struct and constructor:

```go
type cachedSubStore struct {
	Store
	lru *lru.LRU[subKey, cachedSubscription]
	sf  singleflight.Group
	rec *cachestats.Recorder
}

func newCachedSubStore(inner Store, size int, ttl time.Duration, rec *cachestats.Recorder) (*cachedSubStore, error) {
	if size <= 0 {
		return nil, fmt.Errorf("subcache: size must be positive, got %d", size)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("subcache: ttl must be positive, got %v", ttl)
	}
	return &cachedSubStore{
		Store: inner,
		lru:   lru.NewLRU[subKey, cachedSubscription](size, nil, ttl),
		rec:   rec,
	}, nil
}
```

Update `GetSubscription`:

```go
func (c *cachedSubStore) GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	key := subKey{roomID: roomID, account: account}
	if v, ok := c.lru.Get(key); ok {
		c.rec.Hit()
		return fromCached(v), nil
	}
	c.rec.Miss()

	sfKey := roomID + "\x00" + account
	v, err, _ := c.sf.Do(sfKey, func() (interface{}, error) {
		if cached, ok := c.lru.Get(key); ok {
			return cached, nil
		}
		sub, err := c.Store.GetSubscription(ctx, account, roomID)
		if err != nil {
			return nil, err
		}
		projected := cachedSubscription{
			ID:      sub.User.ID,
			Account: sub.User.Account,
			Roles:   append([]model.Role(nil), sub.Roles...),
		}
		c.lru.Add(key, projected)
		return projected, nil
	})
	if err != nil {
		return nil, err
	}
	return fromCached(v.(cachedSubscription)), nil
}

// Len exposes the cache size for the cachestats sizeFn.
func (c *cachedSubStore) Len() int { return c.lru.Len() }
```

Delete the `SubStats` type and the `Stats()` method entirely.

Remove the `sync/atomic` import. Add the cachestats import:

```go
"github.com/hmchangw/chat/pkg/cachestats"
```

- [ ] **Step 3.4: Update `message-gatekeeper/main.go`**

Find the line:
```go
store, err := newCachedSubStore(withMeta, cfg.SubCacheSize, cfg.SubCacheTTL)
```
and change it to:
```go
store, err := newCachedSubStore(withMeta, cfg.SubCacheSize, cfg.SubCacheTTL, nil)
```

- [ ] **Step 3.5: Run tests**

Run: `go test ./message-gatekeeper/... -race -v`
Expected: all tests PASS.

- [ ] **Step 3.6: Commit**

```bash
git add message-gatekeeper/subcache.go message-gatekeeper/subcache_test.go message-gatekeeper/main.go
git commit -m "refactor(gatekeeper): subscription cache takes cachestats.Recorder"
```

---

## Task 4: message-gatekeeper main.go — wire metrics listener + registrations + logger

**Files:**
- Modify: `message-gatekeeper/main.go`
- Create: `message-gatekeeper/metrics_test.go`

- [ ] **Step 4.1: Write a failing /metrics scrape test**

Create `message-gatekeeper/metrics_test.go`:

```go
package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/cachestats"
)

// TestMetricsHandler_ExposesCacheCountersAndSize verifies that a
// Prometheus scrape of the metrics handler returns the unified cache
// series with the names the gatekeeper registers.
func TestMetricsHandler_ExposesCacheCountersAndSize(t *testing.T) {
	reg := prometheus.NewRegistry()
	stats := cachestats.New(reg)

	subSize := 0
	subRec := stats.Register("subscription", func() int { return subSize })
	metaRec := stats.Register("roommeta", func() int { return 7 })

	subRec.Hit()
	subRec.Hit()
	subRec.Miss()
	subSize = 4096
	metaRec.Hit()

	server := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer server.Close()

	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := string(body)

	assert.Contains(t, out, `chat_cache_hits_total{cache="subscription"} 2`)
	assert.Contains(t, out, `chat_cache_misses_total{cache="subscription"} 1`)
	assert.Contains(t, out, `chat_cache_hits_total{cache="roommeta"} 1`)
	assert.Contains(t, out, `chat_cache_size{cache="subscription"} 4096`)
	assert.Contains(t, out, `chat_cache_size{cache="roommeta"} 7`)
	// Sanity: only the names we registered appear.
	assert.NotContains(t, strings.ToLower(out), `cache="user"`)
}
```

- [ ] **Step 4.2: Run test to verify it fails**

Run: `go test ./message-gatekeeper/... -run TestMetricsHandler -v`
Expected: build error — `cachestats` import path resolves but the test compiles only after `pkg/cachestats` exists (it does after Task 1). The test will PASS without main-package changes since it exercises `cachestats` + `promhttp` directly. **That is intentional** — it is a contract test for what `/metrics` will return once main.go registers the same names. If it already passes, move to Step 4.3.

- [ ] **Step 4.3: Wire metrics listener + recorders + logger in `message-gatekeeper/main.go`**

Add to the config struct:

```go
MetricsAddr           string        `env:"METRICS_ADDR"                envDefault:":9090"`
CacheStatsLogEnabled  bool          `env:"CACHE_STATS_LOG_ENABLED"     envDefault:"false"`
CacheStatsLogInterval time.Duration `env:"CACHE_STATS_LOG_INTERVAL"    envDefault:"60s"`
```

Add imports:

```go
"errors"
"net"
"net/http"

"github.com/prometheus/client_golang/prometheus"
"github.com/prometheus/client_golang/prometheus/promhttp"

"github.com/hmchangw/chat/pkg/cachestats"
```

In `main()`, after parsing config and before constructing caches, build the stats container and register the two recorders. `sizeFn` for the subscription cache is supplied via a deferred binding because the cache doesn't exist yet — register first with a stub that will be rewritten once the cache instance exists.

Actually simpler: construct the caches first (passing nil), then build a small post-construction helper. Cleaner: pre-declare the size closures.

Use this pattern (replace the cache-construction block):

```go
stats := cachestats.New(prometheus.DefaultRegisterer)

// Forward declarations so we can register sizeFn against the caches
// before the caches exist.
var subStoreRef *cachedSubStore
var metaStoreRef *cachedMetaStore

subRec := stats.Register("subscription", func() int {
	if subStoreRef == nil {
		return 0
	}
	return subStoreRef.Len()
})
metaRec := stats.Register("roommeta", func() int {
	if metaStoreRef == nil {
		return 0
	}
	return metaStoreRef.Len()
})

withMeta, err := newCachedMetaStore(mongoStore, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL, metaRec)
if err != nil {
	slog.Error("init room meta cache failed", "error", err)
	os.Exit(1)
}
metaStoreRef = withMeta

store, err := newCachedSubStore(withMeta, cfg.SubCacheSize, cfg.SubCacheTTL, subRec)
if err != nil {
	slog.Error("init subscription cache failed", "error", err)
	os.Exit(1)
}
subStoreRef = store
```

After the `slog.Info("gatekeeper caches enabled", ...)` line, optionally start the logger:

```go
var stopCacheLogger func()
if cfg.CacheStatsLogEnabled {
	stopCacheLogger = stats.StartLogger(ctx, cfg.CacheStatsLogInterval, slog.Default())
	slog.Info("cache stats logger enabled", "interval", cfg.CacheStatsLogInterval)
} else {
	stopCacheLogger = func() {}
}
```

After `handler := NewHandler(...)` and before `bootstrapStreams`, add the metrics listener block (model on `search-service/main.go:172-200`):

```go
// /metrics-only listener. All four timeouts guard against hung
// scrapers tying up a goroutine indefinitely on an operator-exposed
// port. Bind synchronously so a port conflict fails startup loudly.
metricsMux := http.NewServeMux()
metricsMux.Handle("/metrics", promhttp.Handler())
metricsServer := &http.Server{
	Handler:           metricsMux,
	ReadHeaderTimeout: 5 * time.Second,
	ReadTimeout:       10 * time.Second,
	WriteTimeout:      10 * time.Second,
	IdleTimeout:       60 * time.Second,
}
metricsListener, err := net.Listen("tcp", cfg.MetricsAddr)
if err != nil {
	slog.Error("metrics server listen failed", "addr", cfg.MetricsAddr, "error", err)
	os.Exit(1)
}
go func() {
	slog.Info("metrics server listening", "addr", cfg.MetricsAddr)
	if err := metricsServer.Serve(metricsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("metrics server failed", "error", err)
	}
}()
```

Extend the `shutdown.Wait` call. The current sequence is:
```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error { iter.Stop(); return nil },
	func(ctx context.Context) error { /* wg.Wait */ ... },
	func(ctx context.Context) error { return tracerShutdown(ctx) },
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
)
```

Add a hook that stops the cache logger first (before drain so any final log line emits) and one that shuts the metrics server last (so the final-window observations are scrape-able):

```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error { stopCacheLogger(); return nil },
	func(ctx context.Context) error { iter.Stop(); return nil },
	func(ctx context.Context) error { /* wg.Wait — unchanged */ ... },
	func(ctx context.Context) error { return tracerShutdown(ctx) },
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	func(ctx context.Context) error { return metricsServer.Shutdown(ctx) },
)
```

- [ ] **Step 4.4: Run tests + build**

Run: `go test ./message-gatekeeper/... -race -v && go build ./...`
Expected: all tests PASS; build succeeds.

- [ ] **Step 4.5: Lint**

Run: `make lint`
Expected: no errors.

- [ ] **Step 4.6: Commit**

```bash
git add message-gatekeeper/main.go message-gatekeeper/metrics_test.go
git commit -m "feat(gatekeeper): expose /metrics with chat_cache_* series + optional cache-stats slog"
```

---

## Task 5: broadcast-worker usercache — Recorder migration

**Files:**
- Modify: `broadcast-worker/usercache.go`
- Modify: `broadcast-worker/usercache_test.go`

- [ ] **Step 5.1: Write failing tests**

Edit `broadcast-worker/usercache_test.go`. Read it first to see existing structure, then add these cases at the end of the file:

```go
func TestCachedUserStore_RecorderCountsHitsAndMisses_Mixed(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockUserStore(ctrl) // use whatever the existing tests use
	inner.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).
		Return([]model.User{{Account: "bob"}, {Account: "carol"}}, nil).Times(2)

	stats := cachestats.New(prometheus.NewRegistry())
	rec := stats.Register("user", nil)
	c := NewCachedUserStore(inner, 10, time.Minute, rec)

	// Pre-warm bob and carol.
	_, err := c.FindUsersByAccounts(context.Background(), []string{"bob", "carol"})
	require.NoError(t, err)
	// 2 misses recorded above.

	// Now alice is a miss; bob and carol are hits.
	_, err = c.FindUsersByAccounts(context.Background(), []string{"alice", "bob", "carol"})
	require.NoError(t, err)

	hits, misses := rec.Snapshot()
	assert.Equal(t, uint64(2), hits, "bob + carol")
	assert.Equal(t, uint64(3), misses, "first call: 2 misses; second call: 1 miss for alice")
}

func TestCachedUserStore_RecorderCountsAllMiss_StaleEntries(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockUserStore(ctrl)
	inner.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob"}).
		Return([]model.User{{Account: "bob"}}, nil).Times(1)
	inner.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob"}).
		Return([]model.User{{Account: "bob"}}, nil).Times(1)

	stats := cachestats.New(prometheus.NewRegistry())
	rec := stats.Register("user", nil)
	c := NewCachedUserStore(inner, 10, time.Millisecond, rec)

	_, err := c.FindUsersByAccounts(context.Background(), []string{"bob"})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond) // expire bob

	_, err = c.FindUsersByAccounts(context.Background(), []string{"bob"})
	require.NoError(t, err)

	hits, misses := rec.Snapshot()
	assert.Equal(t, uint64(0), hits)
	assert.Equal(t, uint64(2), misses, "first call miss; second call stale-as-miss")
}

func TestCachedUserStore_RecorderCountsOnceForDuplicates(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockUserStore(ctrl)
	inner.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob"}).
		Return([]model.User{{Account: "bob"}}, nil).Times(1)

	stats := cachestats.New(prometheus.NewRegistry())
	rec := stats.Register("user", nil)
	c := NewCachedUserStore(inner, 10, time.Minute, rec)

	// One unique account passed twice. Counts as one miss (the
	// implementation dedups before counting).
	_, err := c.FindUsersByAccounts(context.Background(), []string{"bob", "bob"})
	require.NoError(t, err)

	hits, misses := rec.Snapshot()
	assert.Equal(t, uint64(0), hits)
	assert.Equal(t, uint64(1), misses)
}

func TestCachedUserStore_NilRecorderIsSafe(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockUserStore(ctrl)
	inner.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob"}).
		Return([]model.User{{Account: "bob"}}, nil).Times(1)

	c := NewCachedUserStore(inner, 10, time.Minute, nil)
	_, err := c.FindUsersByAccounts(context.Background(), []string{"bob"})
	require.NoError(t, err)
}
```

Add imports (if missing): `"github.com/prometheus/client_golang/prometheus"`, `"github.com/hmchangw/chat/pkg/cachestats"`.

Update every existing call to `NewCachedUserStore(inner, size, ttl)` in this test file to add a fourth `nil` argument.

- [ ] **Step 5.2: Run tests to verify they fail**

Run: `go test ./broadcast-worker/... -run TestCachedUserStore -v`
Expected: build error — `NewCachedUserStore` does not take a fourth argument.

- [ ] **Step 5.3: Update `broadcast-worker/usercache.go`**

Add the `rec` field and constructor argument:

```go
type CachedUserStore struct {
	inner   userstore.UserStore
	ttl     time.Duration
	maxSize int

	mu    sync.Mutex
	lru   *list.List
	index map[string]*list.Element
	now   func() time.Time
	rec   *cachestats.Recorder
}

func NewCachedUserStore(inner userstore.UserStore, maxSize int, ttl time.Duration, rec *cachestats.Recorder) *CachedUserStore {
	return &CachedUserStore{
		inner:   inner,
		ttl:     ttl,
		maxSize: maxSize,
		lru:     list.New(),
		index:   make(map[string]*list.Element, maxSize),
		now:     time.Now,
		rec:     rec,
	}
}
```

Add the import:

```go
"github.com/hmchangw/chat/pkg/cachestats"
```

In `FindUsersByAccounts`, after the dedup loop and inside the per-account `for _, account := range accounts` loop, count once per outcome. Replace the loop body:

```go
for _, account := range accounts {
	elem, ok := c.index[account]
	if !ok {
		c.rec.Miss()
		missing = append(missing, account)
		continue
	}
	entry := elem.Value.(*userCacheEntry)
	if now.Sub(entry.inserted) >= c.ttl {
		c.lru.Remove(elem)
		delete(c.index, account)
		c.rec.Miss()
		missing = append(missing, account)
		continue
	}
	if elem != c.lru.Front() {
		c.lru.MoveToFront(elem)
	}
	c.rec.Hit()
	hits = append(hits, entry.user)
}
```

Add a `Len` method for sizeFn use:

```go
// Len returns the current cache size. Used by the cachestats sizeFn
// in main.go. Takes the same mutex as the hot path; called only on
// Prometheus scrape, so contention is irrelevant.
func (c *CachedUserStore) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}
```

- [ ] **Step 5.4: Update existing call site in main.go**

In `broadcast-worker/main.go`, find:
```go
us = NewCachedUserStore(us, cfg.UserCacheSize, cfg.UserCacheTTL)
```
and change to:
```go
us = NewCachedUserStore(us, cfg.UserCacheSize, cfg.UserCacheTTL, nil)
```

- [ ] **Step 5.5: Run tests**

Run: `go test ./broadcast-worker/... -race -v`
Expected: all tests PASS.

- [ ] **Step 5.6: Commit**

```bash
git add broadcast-worker/usercache.go broadcast-worker/usercache_test.go broadcast-worker/main.go
git commit -m "refactor(broadcast-worker): user cache takes cachestats.Recorder"
```

---

## Task 6: broadcast-worker main.go — wire metrics listener + registrations + logger

**Files:**
- Modify: `broadcast-worker/main.go`

The shape mirrors Task 4. The worker has an additional cache (`user`) but no listener exists today, so add one.

- [ ] **Step 6.1: Add config fields and imports**

Append to the `config` struct:

```go
MetricsAddr           string        `env:"METRICS_ADDR"                envDefault:":9090"`
CacheStatsLogEnabled  bool          `env:"CACHE_STATS_LOG_ENABLED"     envDefault:"false"`
CacheStatsLogInterval time.Duration `env:"CACHE_STATS_LOG_INTERVAL"    envDefault:"60s"`
```

Add imports:

```go
"errors"
"net"
"net/http"

"github.com/prometheus/client_golang/prometheus"
"github.com/prometheus/client_golang/prometheus/promhttp"

"github.com/hmchangw/chat/pkg/cachestats"
```

- [ ] **Step 6.2: Construct stats container and wire recorders**

Replace the cache-construction block. The current block:

```go
cachedStore, err := newCachedMetaStore(store, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL, nil)
if err != nil {
	slog.Error("init room meta cache failed", "error", err)
	os.Exit(1)
}
slog.Info("room-meta-cache enabled", "size", cfg.RoomMetaCacheSize, "ttl", cfg.RoomMetaCacheTTL)
us := userstore.NewMongoStore(db.Collection("users"))
if cfg.UserCacheSize > 0 && cfg.UserCacheTTL > 0 {
	us = NewCachedUserStore(us, cfg.UserCacheSize, cfg.UserCacheTTL, nil)
	slog.Info("user-cache enabled", "size", cfg.UserCacheSize, "ttl", cfg.UserCacheTTL)
} else {
	slog.Info("user-cache disabled")
}
```

becomes:

```go
stats := cachestats.New(prometheus.DefaultRegisterer)

var metaStoreRef *cachedMetaStore
metaRec := stats.Register("roommeta", func() int {
	if metaStoreRef == nil {
		return 0
	}
	return metaStoreRef.Len()
})

cachedStore, err := newCachedMetaStore(store, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL, metaRec)
if err != nil {
	slog.Error("init room meta cache failed", "error", err)
	os.Exit(1)
}
metaStoreRef = cachedStore
slog.Info("room-meta-cache enabled", "size", cfg.RoomMetaCacheSize, "ttl", cfg.RoomMetaCacheTTL)

us := userstore.NewMongoStore(db.Collection("users"))
if cfg.UserCacheSize > 0 && cfg.UserCacheTTL > 0 {
	var userStoreRef *CachedUserStore
	userRec := stats.Register("user", func() int {
		if userStoreRef == nil {
			return 0
		}
		return userStoreRef.Len()
	})
	cached := NewCachedUserStore(us, cfg.UserCacheSize, cfg.UserCacheTTL, userRec)
	userStoreRef = cached
	us = cached
	slog.Info("user-cache enabled", "size", cfg.UserCacheSize, "ttl", cfg.UserCacheTTL)
} else {
	slog.Info("user-cache disabled")
}
```

- [ ] **Step 6.3: Start the optional slog logger**

After the cache block, add:

```go
var stopCacheLogger func()
if cfg.CacheStatsLogEnabled {
	stopCacheLogger = stats.StartLogger(ctx, cfg.CacheStatsLogInterval, slog.Default())
	slog.Info("cache stats logger enabled", "interval", cfg.CacheStatsLogInterval)
} else {
	stopCacheLogger = func() {}
}
```

- [ ] **Step 6.4: Wire the /metrics listener**

After the existing NATS / JetStream setup and before `iter, err := cons.Messages(...)`, insert the listener block:

```go
metricsMux := http.NewServeMux()
metricsMux.Handle("/metrics", promhttp.Handler())
metricsServer := &http.Server{
	Handler:           metricsMux,
	ReadHeaderTimeout: 5 * time.Second,
	ReadTimeout:       10 * time.Second,
	WriteTimeout:      10 * time.Second,
	IdleTimeout:       60 * time.Second,
}
metricsListener, err := net.Listen("tcp", cfg.MetricsAddr)
if err != nil {
	slog.Error("metrics server listen failed", "addr", cfg.MetricsAddr, "error", err)
	os.Exit(1)
}
go func() {
	slog.Info("metrics server listening", "addr", cfg.MetricsAddr)
	if err := metricsServer.Serve(metricsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("metrics server failed", "error", err)
	}
}()
```

- [ ] **Step 6.5: Extend shutdown hooks**

Find the `hooks := []func(context.Context) error{...}` slice and prepend the cache-logger stop hook (so it's first), then append the metrics-server shutdown hook last:

```go
hooks := []func(context.Context) error{
	func(ctx context.Context) error { stopCacheLogger(); return nil },
	func(ctx context.Context) error { iter.Stop(); return nil },
	// ... existing wg/tracer/nc.Drain hooks unchanged
}
// ... existing keyStore append unchanged
hooks = append(hooks, func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil })
hooks = append(hooks, func(ctx context.Context) error { return metricsServer.Shutdown(ctx) })
```

- [ ] **Step 6.6: Build + test**

Run: `go test ./broadcast-worker/... -race -v && go build ./...`
Expected: all tests PASS; build succeeds.

- [ ] **Step 6.7: Lint**

Run: `make lint`
Expected: no errors.

- [ ] **Step 6.8: Commit**

```bash
git add broadcast-worker/main.go
git commit -m "feat(broadcast-worker): expose /metrics with chat_cache_* series + optional cache-stats slog"
```

---

## Task 7: search-service valkey cache + main.go wiring

**Files:**
- Modify: `search-service/store_valkey.go`
- Modify: `search-service/store_valkey_test.go`
- Modify: `search-service/main.go`
- Modify: `search-service/handler_test.go` (existing tests need the new constructor signature)

`search-service` already exposes `/metrics` and registers via `promauto` to the default registry. `cachestats.New(prometheus.DefaultRegisterer)` will register the new vectors on the same registry — no listener changes needed.

- [ ] **Step 7.1: Write failing tests**

Edit `search-service/store_valkey_test.go`. The file already defines a `stubValkey` in-memory helper used by the existing tests; reuse it. Add these tests at the end of the file:

```go
func TestValkeyCache_GetRestricted_RecordsHitOnReturn(t *testing.T) {
	ctx := context.Background()
	stats := cachestats.New(prometheus.NewRegistry())
	rec := stats.Register("restricted_rooms", nil)
	c := newValkeyCache(newStubValkey(), rec)

	require.NoError(t, c.SetRestricted(ctx, "alice", map[string]int64{"r1": 1}, time.Minute))
	_, hit, err := c.GetRestricted(ctx, "alice")
	require.NoError(t, err)
	require.True(t, hit)

	hits, misses := rec.Snapshot()
	assert.Equal(t, uint64(1), hits)
	assert.Equal(t, uint64(0), misses)
}

func TestValkeyCache_GetRestricted_RecordsMissOnCacheMiss(t *testing.T) {
	stats := cachestats.New(prometheus.NewRegistry())
	rec := stats.Register("restricted_rooms", nil)
	c := newValkeyCache(newStubValkey(), rec)

	_, hit, err := c.GetRestricted(context.Background(), "nobody")
	require.NoError(t, err)
	require.False(t, hit)

	hits, misses := rec.Snapshot()
	assert.Equal(t, uint64(0), hits)
	assert.Equal(t, uint64(1), misses)
}

func TestValkeyCache_GetRestricted_TransportErrorRecordsNothing(t *testing.T) {
	stub := newStubValkey()
	stub.getErr = errors.New("conn refused")

	stats := cachestats.New(prometheus.NewRegistry())
	rec := stats.Register("restricted_rooms", nil)
	c := newValkeyCache(stub, rec)

	_, hit, err := c.GetRestricted(context.Background(), "alice")
	require.Error(t, err)
	require.False(t, hit)

	hits, misses := rec.Snapshot()
	assert.Equal(t, uint64(0), hits, "transport errors do not count as hits")
	assert.Equal(t, uint64(0), misses, "transport errors do not count as misses")
}
```

Also update every existing `newValkeyCache(...)` call in this file (the three existing tests `TestValkeyCache_SetThenGet`, `TestValkeyCache_GetMiss`, `TestValkeyCache_GetTransportError`, plus any others) to pass `nil` as the second argument.

Add imports: `"github.com/prometheus/client_golang/prometheus"`, `"github.com/hmchangw/chat/pkg/cachestats"`.

- [ ] **Step 7.2: Run tests to verify they fail**

Run: `go test ./search-service/... -run TestValkeyCache_GetRestricted -v`
Expected: build error — `newValkeyCache` does not take a second argument.

- [ ] **Step 7.3: Update `search-service/store_valkey.go`**

```go
type valkeyCache struct {
	client valkeyutil.Client
	rec    *cachestats.Recorder
}

func newValkeyCache(client valkeyutil.Client, rec *cachestats.Recorder) *valkeyCache {
	return &valkeyCache{client: client, rec: rec}
}
```

Update `GetRestricted`:

```go
func (c *valkeyCache) GetRestricted(ctx context.Context, account string) (map[string]int64, bool, error) {
	var rooms map[string]int64
	err := valkeyutil.GetJSON(ctx, c.client, restrictedKey(account), &rooms)
	if errors.Is(err, valkeyutil.ErrCacheMiss) {
		c.rec.Miss()
		return nil, false, nil
	}
	if err != nil {
		// Transport errors are logged at the call site; we deliberately
		// do not move either counter so hit-rate is not skewed during
		// Valkey outages.
		return nil, false, fmt.Errorf("cache get restricted: %w", err)
	}
	if rooms == nil {
		rooms = map[string]int64{}
	}
	c.rec.Hit()
	return rooms, true, nil
}
```

Add the import:

```go
"github.com/hmchangw/chat/pkg/cachestats"
```

- [ ] **Step 7.4: Update existing call sites**

In `search-service/main.go`, find:
```go
cache := newValkeyCache(valkey)
```
and change to:
```go
cache := newValkeyCache(valkey, restrictedRec)
```
(the `restrictedRec` variable will be declared in Step 7.5)

In `search-service/handler_test.go`, find every `newValkeyCache(...)` call (and any test fixture builders that construct one) and add `nil` as the second argument.

- [ ] **Step 7.5: Wire stats container + recorder + logger in `search-service/main.go`**

Add to the existing `config.Search` struct (or wherever `MetricsAddr` lives — same struct):

```go
CacheStatsLogEnabled  bool          `env:"CACHE_STATS_LOG_ENABLED"  envDefault:"false"`
CacheStatsLogInterval time.Duration `env:"CACHE_STATS_LOG_INTERVAL" envDefault:"60s"`
```

Add imports:

```go
"github.com/prometheus/client_golang/prometheus"

"github.com/hmchangw/chat/pkg/cachestats"
```

In `main()`, before the line `cache := newValkeyCache(valkey, ...)`, insert:

```go
stats := cachestats.New(prometheus.DefaultRegisterer)
restrictedRec := stats.Register("restricted_rooms", nil)
```

After the `handler.Register(router)` line and before the existing metrics-listener block, start the optional logger:

```go
var stopCacheLogger func()
if cfg.Search.CacheStatsLogEnabled {
	stopCacheLogger = stats.StartLogger(ctx, cfg.Search.CacheStatsLogInterval, slog.Default())
	slog.Info("cache stats logger enabled", "interval", cfg.Search.CacheStatsLogInterval)
} else {
	stopCacheLogger = func() {}
}
```

Extend `shutdown.Wait`. The current call is:

```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error { return router.Shutdown(ctx) },
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { return tracerShutdown(ctx) },
	func(_ context.Context) error { valkeyutil.Disconnect(valkey); return nil },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	func(ctx context.Context) error { return metricsServer.Shutdown(ctx) },
)
```

Insert the cache-logger stop hook as the first hook:

```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error { stopCacheLogger(); return nil },
	func(ctx context.Context) error { return router.Shutdown(ctx) },
	// ... rest unchanged
)
```

- [ ] **Step 7.6: Build + test**

Run: `go test ./search-service/... -race -v && go build ./...`
Expected: all tests PASS; build succeeds.

- [ ] **Step 7.7: Lint**

Run: `make lint`
Expected: no errors.

- [ ] **Step 7.8: Commit**

```bash
git add search-service/
git commit -m "feat(search-service): instrument restricted-rooms cache with cachestats"
```

---

## Task 8: Final verification

**Files:** none (verification only).

- [ ] **Step 8.1: Regenerate mocks if any store interfaces changed**

The store interfaces themselves are unchanged in this PR (only constructors changed). Run anyway to be safe:

Run: `make generate`
Expected: no diff in `mock_*` files. If a diff appears, commit it as `chore: regenerate mocks`.

- [ ] **Step 8.2: Full unit test suite with race detector**

Run: `make test`
Expected: all packages PASS, no race warnings.

- [ ] **Step 8.3: Full lint**

Run: `make lint`
Expected: no errors.

- [ ] **Step 8.4: SAST gate**

Run: `make sast`
Expected: no medium-or-higher findings.

- [ ] **Step 8.5: Manual sanity check of /metrics**

This is a documentation step, not a test. The plan does not require running the services locally — the contract test in Task 4 (`metrics_test.go`) already verifies the output shape. If a reviewer wants to see live output, document the command:

```
GATEKEEPER:  curl localhost:9090/metrics | grep chat_cache_
BROADCAST:   curl localhost:9090/metrics | grep chat_cache_
SEARCH:      curl <search-metrics-addr>/metrics | grep chat_cache_
```

Expected series after a few publishes:

```
chat_cache_hits_total{cache="subscription"} N
chat_cache_misses_total{cache="subscription"} N
chat_cache_hits_total{cache="roommeta"} N
chat_cache_misses_total{cache="roommeta"} N
chat_cache_hits_total{cache="user"} N
chat_cache_misses_total{cache="user"} N
chat_cache_hits_total{cache="restricted_rooms"} N
chat_cache_misses_total{cache="restricted_rooms"} N
chat_cache_size{cache="subscription"} N
chat_cache_size{cache="roommeta"} N
chat_cache_size{cache="user"} N
```

- [ ] **Step 8.6: Push branch**

```bash
git push -u origin claude/cache-hit-rate-visibility-fgG6c
```

---

## Spec coverage check

| Spec section | Task(s) |
|---|---|
| `pkg/cachestats` package, `Recorder`, `Register`, `Hit`, `Miss`, `Snapshot`, `StartLogger` | Task 1 |
| Pre-resolved counter handle at Register time | Task 1 (step 1.3) |
| Pre-created zero-valued labeled series | Task 1 (step 1.3, `r.hits.Add(0)`) |
| `chat_cache_size` GaugeFunc only when sizeFn != nil | Task 1 (step 1.3 + test) |
| Panic on duplicate / empty name | Task 1 (tests + impl) |
| Nil-safe Hit / Miss | Task 1 (test + impl) |
| Migrate `pkg/roommetacache`; remove `LoadErrors` | Task 2 |
| Migrate gatekeeper subcache | Task 3 |
| Migrate broadcast usercache | Task 5 |
| Migrate search-service valkey cache; transport-error neutrality | Task 7 |
| `METRICS_ADDR` listener added to gatekeeper + broadcast-worker | Tasks 4, 6 |
| Search-service uses existing listener | Task 7 |
| `CACHE_STATS_LOG_ENABLED` + `CACHE_STATS_LOG_INTERVAL` config | Tasks 4, 6, 7 |
| Periodic slog summary goroutine wired into graceful shutdown | Tasks 4, 6, 7 (step 6.5, 4.3, 7.5) |
| `/metrics` scrape contract test | Task 4 (`metrics_test.go`) |
| `make lint`, `make test`, `make sast` clean | Task 8 |
