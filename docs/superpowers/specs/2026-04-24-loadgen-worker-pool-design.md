# Loadgen Worker-Pool Dispatch + pprof — Design

## Purpose

The loadgen's actual publish rate falls materially below the target rate at
moderate throughput. At `--rate=1000` observed actual rate is ~775 msg/s
(~77% delivery). Root cause: the publisher runs on the `time.Ticker`'s
goroutine serially, and `time.Ticker` drops ticks that fire while a publish
is still in progress. Any per-publish stall (NATS write-lock contention,
GC pause, scheduler hiccup) above the 1 ms/tick budget silently loses a
tick.

This spec fixes that by dispatching publishes to a small worker pool and
adds opt-in pprof so future bottlenecks are diagnosable.

## Scope

### In scope

- `Generator.Run` dispatches each tick's publish to a bounded pool of
  goroutines. The ticker itself stays punctual.
- New env var `MAX_IN_FLIGHT` (default `200`) caps concurrent publishes.
  Saturation (pool full when a tick fires) is an explicit signal, not a
  silent drop: the ticker records
  `loadgen_publish_errors_total{reason="saturated"}` and moves on.
- `MAX_IN_FLIGHT=0` falls back to the current serial behavior. Useful as
  a bisection tool and a conservative default for whoever wants
  reproducible comparisons.
- On graceful shutdown / `ctx.Done()`, `Run` returns only after all
  in-flight publishes drain (bounded by a small timeout).
- New env var `PPROF_ADDR` (default `""`, meaning disabled). When set
  (e.g. `:6060`), loadgen exposes `net/http/pprof` handlers on a
  separate HTTP server. Never on by default — pprof isn't exposed in
  production-ish deployments unless the operator opts in.
- Docker-compose loadgen service documents both new env vars.

### Out of scope

- Changes to the Collector, ConsumerSampler, Report, Preset, Seed, or
  integration test — none are publish-hot-path.
- `golang.org/x/time/rate.Limiter` — the worker-pool fix addresses the
  real structural cause (ticker/publish coupling). If worker-pool
  saturation becomes the new bottleneck, re-evaluate then.
- `sync.Pool` allocation-reuse tuning — defer until pprof identifies GC
  as the next-order concern.
- Dedicated NATS connection for publishes vs. subscriptions — only
  justified if pprof identifies the NATS write lock as the bottleneck
  after the worker pool lands.
- Default-rate bump — reasoned about separately.

## Architecture

Before:

```text
ticker goroutine: [wait tick] → publishOne (JSON + NATS write + metrics) → [wait tick] → …
                                ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
                                one slow call here silently loses a tick
```

After:

```text
ticker goroutine: [wait tick] → reserve sem slot → spawn publish goroutine → [wait tick] → …
                                                                             
publish goroutine: [publishOne] → release sem slot
publish goroutine: [publishOne] → release sem slot
publish goroutine: [publishOne] → release sem slot   (up to MAX_IN_FLIGHT concurrently)
```

The ticker goroutine's per-tick work shrinks to a semaphore send + goroutine
spawn — tens of nanoseconds. It cannot overshoot the ticker interval at any
realistic rate.

## Components

### `Generator.Run` (modified)

- Read `g.cfg.MaxInFlight` from `GeneratorConfig`.
- If `MaxInFlight <= 0`: run serially as today (preserves legacy behavior
  and gives a bisection switch).
- Else: create `sem := make(chan struct{}, MaxInFlight)` and
  `var wg sync.WaitGroup`. On each tick, non-blocking `select`:
  - Slot available: take it, `wg.Add(1)`, `go func() { defer wg.Done();
    defer func() { <-sem }(); g.publishOne(ctx) }()`.
  - No slot: increment
    `loadgen_publish_errors_total{reason="saturated"}` and continue —
    the tick is dropped but at least it's observable.
- On `ctx.Done()`: stop the ticker, then `wg.Wait()` with a bounded grace
  period (5 s). If the grace expires, log and return — in-flight
  goroutines complete on their own after NATS drain in main.

### `GeneratorConfig` (modified)

Add one field:

```go
type GeneratorConfig struct {
    … existing fields …
    MaxInFlight int
}
```

### `main.go` (modified)

Add to `config`:

```go
type config struct {
    … existing fields …
    MaxInFlight int    `env:"MAX_IN_FLIGHT" envDefault:"200"`
    PProfAddr   string `env:"PPROF_ADDR"    envDefault:""`
}
```

Pass `cfg.MaxInFlight` into `GeneratorConfig` when constructing the generator.

On startup, if `PProfAddr != ""`: register `net/http/pprof` handlers on a
new `http.ServeMux` and start a separate `http.Server` listening on that
addr. Log the resulting URL. The server doesn't share the metrics mux —
pprof is genuinely separate, opt-in infrastructure, and keeping it off the
metrics port avoids accidental exposure when the metrics mux is scraped
by Prometheus.

On `ctx.Done()`: gracefully shut down the pprof server with a 2 s timeout.

### Metrics

No new metrics. The existing `loadgen_publish_errors_total` counter with
`reason="saturated"` is the single new label value for pool saturation.
This keeps the Grafana dashboard's "Publish errors/sec by reason" panel
working out of the box.

## Error handling

- `sem <- struct{}{}` is never blocking because we use non-blocking
  `select` — if the pool is full, we record saturation and move on. No
  unbounded goroutine growth under sustained overload.
- Inside each publish goroutine, `publishOne` already handles its own
  errors (counters for marshal/publish failures, `RecordPublishFailed`
  on the Collector).
- Graceful shutdown: the `Run` method returns only after in-flight
  publishes drain or the bounded grace period elapses. The caller
  (`main.go runRun`) already calls `collector.DiscardBefore` and
  `collector.Finalize` after `Run` returns, so late-arriving publishes
  correctly integrate with the summary.

## Testing

### New unit test

`TestGenerator_MaxInFlightZeroRunsSerially` — with `MaxInFlight=0`, the
generator's behavior is unchanged from today. Reuses the existing
`TestGenerator_SendsExpectedCount` assertion style.

### Adjusted unit test

`TestGenerator_SendsExpectedCount` — still valid with `MaxInFlight > 0`,
but the count may be closer to the theoretical target since the ticker
is no longer blocked.

### New unit test

`TestGenerator_PoolSaturationCountedAsError` — artificially slow the
publisher via an injected blocking `Publisher`. Run at a rate that
exceeds the pool's capacity. Assert the `saturated` counter increments.

### Integration test

No change. The existing `tools/loadgen/integration_test.go` exercises
`Generator.Run` with a fake gatekeeper + broadcast-worker and makes no
assumptions about ticker coupling.

### Coverage target

`generator.go` to stay at ≥ 90% for `Run`, `publishOne`, `content` per
the existing plan.

## Dependencies

No new third-party dependencies. All new code uses stdlib: `net/http`,
`net/http/pprof`, `sync`.

## Rollout

- Both env vars have safe defaults (`MAX_IN_FLIGHT=200`, `PPROF_ADDR=""`).
- Existing deployments pick up the worker pool automatically with
  improved actual-rate fidelity at moderate throughput. Operators
  concerned about the behavior change can set `MAX_IN_FLIGHT=0` to
  get the legacy serial path.
- pprof stays off unless explicitly enabled via `PPROF_ADDR`.
- Internal-only to the loadgen service; no cross-service contract
  change.

## Future work (deferred)

- Dedicated publish-side `*nats.Conn` — only if profiling identifies the
  NATS connection write lock as the remaining bottleneck.
- `sync.Pool` for `SendMessageRequest` / `MessageEvent` / byte buffers
  to reduce per-publish GC pressure — only if GC shows up in a
  profile.
- Background UUID generation — only if `crypto/rand` shows up
  prominently.
