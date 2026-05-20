# Cache Hit-Rate Visibility

## Summary

Make the hit rate of every active in-process and Valkey-backed cache in the
system observable through a unified Prometheus metric family and, optionally,
through a periodic structured log line. Today three caches expose hit/miss
counters only via their in-memory `Stats()` method (read by tests, by nothing
in production); two more caches keep no counters at all. After this change,
operators can answer "what is the hit rate of cache X right now" from any
Prometheus dashboard, and in environments without a metrics stack they can
opt into a periodic `slog` summary.

## Motivation

Caches are now load-bearing on the message-publish hot path
(`pkg/roommetacache`, gatekeeper subscription cache) and on search request
routing (`search-service` restricted-rooms Valkey cache). Their effectiveness
is currently invisible in production. A miss-rate regression — say, a TTL
that's too short, or a cache that's too small for a working set — can only
be diagnosed by reading code and inferring from Mongo / ES load. We want a
direct, low-cost signal.

The change is intentionally narrow: instrument, do not redesign. No cache
gains or loses behavior. No cache changes its TTL, size, or invalidation
story.

## Scope

### In scope

Five active caches:

| Cache | Location | Today |
|---|---|---|
| Subscription LRU | `message-gatekeeper/subcache.go` | Has `Hits`/`Misses` atomics, read only by tests |
| Room-meta LRU (gatekeeper) | `pkg/roommetacache` wrapped in `message-gatekeeper/metacache.go` | Has `Hits`/`Misses`/`LoadErrors`/`Size`, read only by tests |
| Room-meta LRU (broadcast) | `pkg/roommetacache` wrapped in `broadcast-worker/metacache.go` | Same as above |
| User LRU | `broadcast-worker/usercache.go` | No counters |
| Restricted-rooms (Valkey) | `search-service/store_valkey.go` | No counters |

### Out of scope

- `pkg/roomsubcache` — built but not yet wired into any production caller; instrument when wired.
- `/metrics` for services that own no cache (`message-worker`, `notification-worker`, `room-worker`, `search-sync-worker`, `inbox-worker`, `auth-service`).
- Counting cache transport / load errors as a separate metric series.
- Dashboards, alerting rules, or runbooks.
- Active invalidation, TTL changes, capacity tuning.

## Design

### Overview

A new package `pkg/cachestats` owns three responsibilities:

1. A small `Recorder` type that each cache calls on its hit/miss path.
2. The unified Prometheus collectors (one `CounterVec` for hits, one for misses, one `GaugeFunc`-style registration for size).
3. An optional periodic `slog` summary goroutine.

```
cache implementation  →  *cachestats.Recorder  →  Prometheus CounterVec + Gauge
                                              ↘
                                                process-local recorder registry
                                                              ↓
                                               cachestats.StartLogger() goroutine
                                                              ↓
                                                       slog.Info per cache
```

Unified metric names across the system:

- `chat_cache_hits_total{cache="<name>"}` — counter
- `chat_cache_misses_total{cache="<name>"}` — counter
- `chat_cache_size{cache="<name>"}` — gauge (only for caches that can cheaply report length; the Valkey caches omit it)

Allowed `cache` label values: `subscription`, `roommeta`, `user`, `restricted_rooms`. Each Prometheus registry lives in its own service process, so a `service` label is not needed — Prometheus scrape config supplies that.

### Components

#### `pkg/cachestats/cachestats.go` (new)

```go
type Recorder struct {
    name   string
    hits   prometheus.Counter // pre-resolved label handle
    misses prometheus.Counter // pre-resolved label handle
    sizeFn func() int         // may be nil
}

func (r *Recorder) Hit()
func (r *Recorder) Miss()

// Snapshot returns the current counter values. Implemented by
// reading the Prom counters via prometheus.Metric.Write — slow path
// (allocates a dto.Metric), intentionally off the cache hot path.
// Used by tests and by StartLogger only.
func (r *Recorder) Snapshot() (hits, misses uint64)

// Register returns a Recorder for the named cache. sizeFn may be nil
// when the cache cannot cheaply report its length (Valkey-backed
// caches). Panics if called twice with the same name in the same
// process — caches register exactly once at startup.
func Register(name string, sizeFn func() int) *Recorder

// StartLogger emits one slog.Info line per registered cache every
// interval, until ctx is cancelled. Returns immediately with a no-op
// goroutine when interval <= 0. The owning service decides whether
// to call it based on its own env-var toggle; this package does not
// read environment variables.
func StartLogger(ctx context.Context, interval time.Duration, logger *slog.Logger)
```

Counters register with the default Prometheus registry via `promauto.NewCounterVec`, matching the existing pattern in `search-service/metrics.go`. The `chat_cache_size` gauge is implemented as a labeled `GaugeFunc` per registered name so the value is sampled only at scrape time, never on the hot path.

**Hot-path constraint (load-bearing).** `CounterVec.WithLabelValues("subscription").Inc()` performs a map lookup on every call, which would be measurable at gatekeeper message rates. `Register` resolves the labeled `prometheus.Counter` once and stores the handle on the `Recorder`. The hot path then calls `r.hits.Inc()` directly with no map lookup. This is the single design constraint that keeps overhead nominal; see Performance below.

Per-name zero-valued series are pre-created at registration time so dashboards don't see "missing series" for a cache that has not yet served a request.

#### Per-cache changes

**`message-gatekeeper/subcache.go`** — drop the local `hits`/`misses` `atomic.Uint64` fields and the `SubStats`/`Stats()` method. Constructor takes a `*cachestats.Recorder`; the existing hit/miss branches call `r.Hit()` / `r.Miss()`. Tests read counter values through a `Recorder.Snapshot() (hits, misses uint64)` helper exposed by `pkg/cachestats` specifically for tests and the periodic logger (single canonical read path; no `testutil.ToFloat64` plumbing in cache packages).

**`pkg/roommetacache/roommetacache.go`** — `New` and `WrapStore` gain a `recorder *cachestats.Recorder` parameter. The package itself does **not** call `cachestats.Register` — that's the consuming service's job, so the same package can in principle be registered under different names by different services (today both use `roommeta`, but the registration ownership stays at the service boundary, where the choice of name is correct to make). The existing `Stats()` method is removed; tests migrate to `Recorder.Snapshot()`. The `loadErrs` atomic is **removed** — it's not part of the hit-rate signal, no production code reads it, and the existing test assertion is dropped along with it (error-counting is explicitly out of scope).

**`broadcast-worker/usercache.go`** — add a `recorder *cachestats.Recorder` field. Inside `FindUsersByAccounts`, increment `Hit()` per account served from the LRU and `Miss()` per account dropped into the `missing` slice (so the counters match the actual lookup unit, not the request — a 50-account lookup with 40 hits records 40 hits and 10 misses, which is what operators want).

**`search-service/store_valkey.go`** — `newValkeyCache` accepts a `*cachestats.Recorder`. `GetRestricted` calls `Hit()` on a returned value, `Miss()` on `ErrCacheMiss`. **Transport errors do not move either counter** — they are already logged at the call site, and counting them as misses would skew the hit-rate signal during Valkey outages. A separate error counter is out of scope.

#### Service wiring

**`message-gatekeeper/main.go`** — add a metrics listener mirroring the `search-service` pattern: separate `net.Listen` + `http.Server` with all four timeouts (Read, ReadHeader, Write, Idle) on its own goroutine, listener acquired before the goroutine starts so a bind failure fails fast. Config gains:

```go
MetricsAddr string `env:"METRICS_ADDR" envDefault:":9090"`
```

The new caches are constructed with their recorders:

```go
subRec := cachestats.Register("subscription", nil)
metaRec := cachestats.Register("roommeta", func() int { return roomMetaCache.Size() })
withMeta, _ := newCachedMetaStore(mongoStore, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL, metaRec)
store, _   := newCachedSubStore(withMeta, cfg.SubCacheSize, cfg.SubCacheTTL, subRec)
```

**`broadcast-worker/main.go`** — same metrics listener; registers `roommeta` and `user`. The size function for `user` reads the cache's internal length under its mutex (acceptable because it's only invoked on scrape).

**`search-service/main.go`** — already has a metrics listener. Just register `restricted_rooms` (size omitted, `nil`) and pass the recorder to `newValkeyCache`.

All three services accept the slog-logger toggle:

```go
CacheStatsLogEnabled  bool          `env:"CACHE_STATS_LOG_ENABLED"  envDefault:"false"`
CacheStatsLogInterval time.Duration `env:"CACHE_STATS_LOG_INTERVAL" envDefault:"60s"`
```

When `CACHE_STATS_LOG_ENABLED=true`, the service calls
`cachestats.StartLogger(ctx, cfg.CacheStatsLogInterval, slog.Default())`
after caches are registered. The goroutine's context is the same one wired
into `pkg/shutdown.Wait`, so it exits cleanly on graceful shutdown.

Log line shape (one per cache per tick):

```
level=INFO msg="cache stats" cache=subscription hits=12873 misses=441 hit_rate=0.967 size=4096
```

`hit_rate` is computed as `hits / (hits + misses)` with the obvious zero-denominator guard. The `size` field is omitted from the log line when the cache didn't register a `sizeFn`.

### Data flow (gatekeeper hot path)

```
NATS msg.send → handler → store.GetSubscription(account, roomID)
                            ↓
                          cachedSubStore.GetSubscription
                            ├── LRU hit  → recorder.Hit()   → atomic CAS on Prom counter
                            └── LRU miss → recorder.Miss()  → atomic CAS on Prom counter
                                          ↓ singleflight load → inner Store
```

Per-call instrumentation cost is one atomic increment on the resolved
`prometheus.Counter` handle — see Performance.

### Error handling

- `cachestats.Register` panics on duplicate registration. This is a programmer error — caches register exactly once at startup. The panic at startup is preferable to a silent double-count.
- `StartLogger` with `interval <= 0` is a no-op; non-fatal so a misconfigured deployment still serves traffic.
- Metrics-listener bind failure is fatal in `main.go`, matching `search-service`: a worker quietly serving traffic without `/metrics` is worse than a hard restart loop that surfaces the misconfig.
- Valkey transport errors don't move the hit/miss counters (see component note above).

### Performance

Instrumentation overhead per cache call:

| Cache | Today | After |
|---|---|---|
| gatekeeper subcache | 1 `atomic.Uint64.Add` | 1 `prometheus.Counter.Inc` (replaces the existing atomic) |
| gatekeeper / broadcast roommetacache | 1 `atomic.Uint64.Add` | 1 `prometheus.Counter.Inc` (replaces the existing atomic) |
| broadcast usercache | 0 | 1 `prometheus.Counter.Inc` per account |
| search-service restricted_rooms | 0 | 1 `prometheus.Counter.Inc` per request |

A `prometheus.Counter.Inc()` from `client_golang` is a single atomic float64 CAS — roughly 10–20 ns on x86. The LRU lookup already in front of it costs ~100–500 ns (hash, map probe, TTL check, mutex). The miss path costs microseconds to milliseconds (Mongo / ES / Valkey round-trip). Instrumentation is well under 1% of the work the cache is already doing.

The single design constraint that keeps this true is **pre-resolving the labeled counter handle at `Register` time**, never calling `WithLabelValues(...)` on the hot path. This is enforced by the `Recorder` API: callers cannot reach the underlying `CounterVec`, only the pre-resolved `prometheus.Counter`.

Things that cost nothing on the hot path:

- `chat_cache_size` is a `GaugeFunc`, sampled only on scrape.
- The slog summary goroutine is gated by `CACHE_STATS_LOG_ENABLED`; when false, the goroutine isn't started.
- When the logger is enabled, it ticks every 60s by default, iterates five recorders, and emits five `slog.Info` lines — entirely off the cache hot path.
- The `/metrics` HTTP listener runs on its own port and goroutine; it does not share the worker's NATS consumer or Gin request goroutine.

Memory: a few hundred bytes for the `CounterVec` metadata plus ~80 bytes per labeled series. Negligible.

Contention: the existing atomic counters on gatekeeper / roommetacache already exist on the hot path. Replacing them with the Prom counter is net-neutral. For the two previously uninstrumented caches (`usercache`, `restricted_rooms`), the added atomic lives on its own cache line and does not contend with anything else.

## Configuration

| Env var | Service(s) | Default | Notes |
|---|---|---|---|
| `METRICS_ADDR` | `message-gatekeeper`, `broadcast-worker` | `:9090` | Bind address for the new `/metrics` HTTP listener. Already present in `search-service`. |
| `CACHE_STATS_LOG_ENABLED` | all three | `false` | Toggle the periodic slog summary line. |
| `CACHE_STATS_LOG_INTERVAL` | all three | `60s` | Tick interval for the summary goroutine. Ignored when disabled. |

No secrets, no required fields. All non-critical config keeps an `envDefault`.

## Testing

TDD per CLAUDE.md §4 — tests precede implementation for every unit.

### `pkg/cachestats/cachestats_test.go` (new)

- `Register` returns a `Recorder` that increments the labeled Prom counter for its name.
- `Register` panics on duplicate name in the same process.
- `Register` with `sizeFn == nil` registers no size gauge for that name; with a non-nil `sizeFn`, the gauge returns the value of the function at scrape time.
- `Hit` / `Miss` increment the corresponding counter exactly once per call.
- `StartLogger` with `interval > 0` emits one record per registered cache per tick, using an injected `slog.Handler` that captures records, until `ctx` is cancelled.
- `StartLogger` with `interval <= 0` returns without starting a goroutine (no records emitted).
- `StartLogger` exits cleanly on `ctx.Done()` within a tight test timeout.

### `message-gatekeeper/subcache_test.go` (update)

- Existing singleflight / TTL / capacity behavior tests stay.
- Existing `Stats()` test is replaced with a test that constructs a recorder, exercises hits/misses, and reads the underlying Prom counters.

### `pkg/roommetacache/roommetacache_test.go` (update)

- Same migration: `Stats()` removed, tests assert against an injected recorder.

### `broadcast-worker/usercache_test.go` (update)

- New cases: hits-only, all-miss, mixed-batch, stale-TTL-counts-as-miss, duplicated-input-still-counts-once-per-unique-account.

### `search-service/handler_test.go` (extend)

- Extend the existing restricted-rooms cache tests:
  - Cache return → `Hit` recorded, no `Miss`.
  - `ErrCacheMiss` → `Miss` recorded, no `Hit`.
  - Transport error → neither counter moves.

### Per-service `/metrics` scrape tests

- For each of `message-gatekeeper`, `broadcast-worker`, `search-service`: an integration-style test that exercises a few hit/miss operations, GETs the service's `/metrics` endpoint, and asserts that the expected `chat_cache_hits_total{cache="..."}` and `chat_cache_misses_total{cache="..."}` series exist with the correct values.

### Coverage

`pkg/cachestats` is a shared package, so per CLAUDE.md it targets 90%+ coverage. Per-cache instrumentation paths are exercised by the existing handler / store tests after the migration.

## Migration

No data migration. No config migration required — the new env vars are opt-in with safe defaults, and `METRICS_ADDR` is additive (services that don't set it pick up `:9090`, matching `search-service`).

Rollout order, single PR:

1. Land `pkg/cachestats` with full unit tests.
2. Migrate each cache (subcache, roommetacache, usercache, restricted-rooms) to take a recorder; remove the old `Stats()` methods. Per-cache tests migrated alongside.
3. Wire `cachestats.Register` and `cachestats.StartLogger` into each service's `main.go`. Add the metrics listener to gatekeeper and broadcast-worker.
4. Verify `/metrics` shape against the per-service scrape test.

## Open questions

None. All design choices were confirmed in brainstorming.
