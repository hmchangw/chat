# Loadgen v2 — Trustworthy, Restructured, Realistic

**Date:** 2026-05-12
**Status:** scoping
**Predecessor:** [2026-05-07-history-service-loadtest-design.md](2026-05-07-history-service-loadtest-design.md) shipped the v1 harness (Phases 1–3 of the original spec). This document covers the follow-up work surfaced by the post-ship code review.

**Review history:** the first-draft spec was reviewed by seven specialist agents (methodology, architecture, DevOps/Docker, DX, SRE, chat-platform domain, technical writing) on 2026-05-12. Each section below has been revised against the blockers and important findings; the "Review changelog" at the bottom of the document maps each amendment back to the reviewer who raised it.

## Goal

Take `tools/loadgen` from "useful internal tool" to "numbers a capacity planner can defend in a postmortem." The post-ship code review (four reviewers: bug, architecture, load-testing methodology, Go craft) identified three categories of work:

1. **Trustworthiness** — the headline numbers can lie under several detectable conditions, and the harness already has the data to know but does not surface it.
2. **Restructuring** — adding a scenario is a 6-file diff today; `main.go` is 1,071 lines with `runRun` alone at ~625; adding a 5th scenario is more expensive than it should be.
3. **Coverage** — the v1 scenarios exercise the message-create path; chat platforms live or die on presence/typing/notification fan-out, large-room broadcast, read-after-write timing, edit/delete, subscription churn, and federation, none of which are loaded today.

Six critical bugs surfaced by the same review are fixed under separate commits ahead of this spec and are not in scope here.

## Non-goals

- Replacing `tools/loadgen` with an off-the-shelf tool (k6, Vegeta, ghz). v1 is intentionally chat-shaped; v2 deepens the shaping, doesn't abandon it.
- Becoming a CI regression gate. Still operator-invoked.
- Cross-machine numerical comparisons. "Compare within one machine across changes" remains the rule.

## Phase 0 — Refactor first (pre-trustworthiness)

Goal: extract the lifecycle and flag surfaces *before* Phase 1 touches them, so Phase 1's new code lands on a clean substrate. Pure refactor, no behavior change, existing tests are the safety net.

Originally the spec ran Phase 1 before Phase 2. The architecture reviewer flagged that as inverted: §1.4 (settle phase) belongs as a `Runtime.Settle()` method, §1.3 (`RunQuality`) accumulates state across phases, §1.7 (`phase` label) needs threading through every metric site that Phase 2 then rearranges into `publisher.go` / `requester.go`. We were paying the editing cost twice.

Phase 0 lifts forward the *pure-refactor* pieces of what was Phase 2 (extract `Runtime`, extract `runFlags` struct, move transport adapters to dedicated files). The `Scenario` interface + registry stays in Phase 2 because it interacts with the new scenarios in Phase 3.

Phase 0 deliverables:

- `tools/loadgen/runtime.go` — `Runtime` struct + lifecycle methods (`Setup`, `Preflight`, `Run`, `Finalize`, `Close`). Same behavior as today's `runRun`.
- `tools/loadgen/flags.go` (expanded) — `runFlags` typed struct with sub-structs (no new flags yet; just regroup the existing 30). `ParseRunFlags(args []string)` becomes unit-testable.
- `tools/loadgen/publisher.go`, `requester.go` — move `natsCorePublisher`, `natsRequester`, `newAsyncErrHandler`, `jetstreamPublishOpts` out of `main.go`.
- `tools/loadgen/metrics_helpers.go` — move `gatheredCounterValue`, `counterValue`, `counterValueLabeled` out of `main.go`.

Phase 0 exit: `main.go` < 250 lines (down from 1,071), all existing unit + binary-exec tests still pass, no diff to printed output, no diff to flag set, no diff to Prometheus metric names/labels.

## Phase 1 — Trustworthiness

Goal: a run that is not trustworthy refuses to print numbers that look trustworthy.

### 1.1 HDR histograms replace per-(scenario, kind) sample slices

Today `Collector.requests` is a sharded map of `[]requestSample`, sorted at finalize for percentile reporting. Memory is unbounded for the duration of a run (acknowledged in README, deferred to "S6/S7"). At 5 k rps × 5 min that's ~50 MB per cell. Long sustained runs OOM the loadgen process before the summary prints.

The same slice also drives small-sample percentile garbage: a 200-sample search-read run reports the 198th sorted value as "p99" with no caveat.

**Third-party dependency ask.** CLAUDE.md §5 requires asking before adding a new third-party dependency. v2 adds `github.com/HdrHistogram/hdrhistogram-go`. **Justification:** the alternatives — t-digest (better tail accuracy, smaller memory, but harder to reason about fixed-budget reporting and less standard tooling for log-format interchange), KLL sketches (theoretical wins but no mature Go impl), Prometheus's own histogram (global buckets, can't extract per-(scenario, kind) percentiles client-side), stdlib (none) — each have a worse trade-off for this harness. HDR's `.hlog` log format is the industry-standard interchange for latency histograms, supported by external tooling (HdrHistogramVisualizer, the wrk2 ecosystem), and HDR merges across processes (which `run-mixed.sh` will exploit). The owner / operator should approve this dep before Phase 1 starts. If approval is withheld, the fallback is t-digest (`influxdata/tdigest`); the rest of the spec is unchanged.

**Design:** swap the per-cell `[]requestSample` for an HDR histogram (`HdrHistogram-go`). Mergeable across processes, accurate percentiles at any sample count.

API change inside `Collector`:

- `RecordRequest(scenario, kind, latency, errored, …)` → write into the histogram instead of appending to a slice.
- `RequestPercentiles(scenario, kind, quantiles []float64) []time.Duration` → query the histogram.
- Keep a separate sample tally for warmup-discard (a single `recordedAt int64` + `inMeasured bool` cheap counter is enough — we do not need to discard per-sample from the histogram; we either start measuring at `warmupDeadline` or keep two histograms `warmup` and `measured` and report only `measured`).

**Compatibility:** the printed report's "request latency" section keeps the same columns (`p50/p95/p99/max/count/errors`). CSV export changes: we no longer have raw per-sample latencies. Either we keep a *capped* raw-sample sidecar (for `--csv` only) or we change `--csv` semantics to "per-bucket histogram counts." Recommend the former — capped at `--csv-max-samples` (default 100 k) with a `csv truncated` warning if exceeded — since the typical `--csv` use case is offline analysis of an explicit smaller run.

The same swap applies to `LatencyWindow` (the abort watcher's ring buffer). HDR's `SubtractWindow` operation lets us implement the rolling sustain check natively without the existing "drop oldest sample" cap-masking failure mode — though we keep the cap as a memory ceiling for the watcher's per-tick query.

**Known risk — abort-watcher semantic change.** Replacing `LatencyWindow`'s slice-based percentile with HDR's `SubtractWindow` is *not* a transparent refactor. It will move the precise rps at which a real run aborts (HDR percentile at 3 sig digits differs from sorted-slice percentile by 1–2 ms at boundary conditions; the windowing semantics differ subtly between "sample within last N seconds" and "snapshot subtract snapshot from N seconds ago"). **Before Phase 1 lands, re-run the existing abort-watcher canary suite at three rps points (well-below, at, well-above the canonical abort threshold) and compare trip latency under the old and new implementations. Acceptance criterion: trip-time drift ≤ 10% at each point. If not, retune defaults before committing.**

### 1.2 Coordinated-omission tracking

Today the latency clock starts inside the dispatched goroutine, after `sem <- struct{}{}` and after `go func(){}` schedules. Under saturation, generator-side queueing latency hides under "SUT smoothness," and dropped ticks (when the pool is full) leave no trace beyond a `saturated` counter increment.

**Design:** capture `intendedAt = time.Now()` at the tick site *before* the `sem` send. Pass it to the dispatched closure. The closure measures `actualStart - intendedAt` as **dispatch deficit** and records it in:

- A new `loadgen_omission_deficit_seconds{dropped="false|true"}` histogram (Prometheus + HDR-internal). The `dropped` label keeps serviced-tick deficit and dropped-tick deficit queryable separately — the methodology reviewer flagged that conflating them into one histogram corrupts p99 reasoning.
- The end-of-run summary as two lines: `omission p99 (serviced): Xms` and `omission p99 (dropped): Yms` so operators can distinguish "the generator queued briefly under load" from "the generator dropped this many ticks entirely."

For dropped ticks (sem full), record `intendedAt` and the moment the tick was dropped (i.e. `time.Now()` at the `default:` branch of the sem-send select) into the same histogram with `dropped=true`.

The latency-of-interest reported in the existing histograms remains `actualStart → reply`; we do NOT shift it to `intendedAt → reply` (that's wrk2-style correction, which would change every reported number on every chart and break "compare within a machine across changes"). Operators get *both* numbers and choose the one that matches their question.

Exit-code policy unchanged.

### 1.3 RUN QUALITY verdict

Today the harness detects but does not surface:

- Abort watcher deafened by sample cap (logged `slog.Warn`, otherwise invisible)
- Async JetStream drain timed out (logged `slog.Warn`, summary still prints `Published` count)
- Warmup ended with ≥X% errors (no detection at all)
- Coordinated omission p99 above threshold (new, from §1.2)
- Liveness probe degraded (some failures, but not enough to trip the watcher)
- Measured window shorter than `2 × abort-p99-sustain` (so the watcher couldn't have fired even on a real breach)

**Design:** add a `RunQuality` struct accumulated through the run, evaluated at finalize:

```go
type RunQuality struct {
    Verdict string   // TRUSTED | DEGRADED | UNTRUSTED
    Issues  []string // human-readable lines, e.g. "abort watcher deafened (cap=10000, rate=500, sustain=30s)"
}
```

Rules (refined against methodology-reviewer feedback — original first-draft thresholds were too lax):

- **UNTRUSTED:** drain timed out **or** measured window < `abort-p99-sustain` **or** warmup error rate > 20% **or** settle phase incomplete (§1.4) **or** abort watcher deafened with `peak_rps × sustain > 2 × cap` (deaf for ≥50% of window).
- **DEGRADED:** warmup error rate 5–20% **or** abort watcher deafened with `peak_rps × sustain ≤ 2 × cap` (deaf for <50% of window) **or** omission p99 > 25% of measured p99 (budget auto-scales by default; `--omission-budget-ms=N` overrides with a fixed budget; the previous default 100ms was wrong for tight scenarios like `room-rpc` targeting 20ms p99) **or** ≥1 liveness probe failed but watcher didn't trip.
- **TRUSTED:** otherwise.

Counters used in verdict rules are computed over the **measured window only**; warmup samples are filtered out per §1.7 phase labels. Window definitions: warmup error rate is `failed_in_warmup / total_in_warmup`; the measured-window predicate uses Collector counters scoped to `phase="measured"`.

Verdict interaction with exit codes: the verdict is always printed and emitted as `loadgen_run_quality{verdict="..."} 1`. Exit codes 2 (saturation abort) and 3 (liveness abort) still take precedence over the verdict-derived exit code 4 (UNTRUSTED); a saturated+UNTRUSTED run exits 2 *and* prints UNTRUSTED in the summary.

Print at the top of the summary, color-coded if stdout is a TTY:

```
=== RUN QUALITY: DEGRADED ===
  - abort watcher deafened by sample cap (peak_rps×sustain > cap)
  - omission p99 above budget: 240ms (budget 100ms)
=============================
```

UNTRUSTED runs additionally exit with code 4 (new) so CI / automation does not consume the numbers. TRUSTED + DEGRADED still exit with the existing 0/1/2/3 policy.

### 1.4 Settle phase between auto-warmup and read scenarios

Today `runAutoWarmup` ends at a wall-clock deadline and the read scenario starts immediately. Cassandra commit-log flushes and search-sync-worker → Elasticsearch indexing have not necessarily caught up. First `GetMessageByID` requests can return "not found" or hit a cold path.

**Design:** after the warmup deadline and before the read run starts, run a **settle poll**:

- Sample N=20 message IDs from the warmup pool.
- Issue `GetMessageByID` for each via the same Requester the read scenario will use.
- Loop until all N succeed (200 OK + non-empty body) or `--settle-timeout` (default 30s) elapses.
- For `search-read` scenarios, additionally issue a `SearchMessages` for a known token from the seed corpus until it returns ≥1 hit (Elasticsearch index visibility).

If `--settle-timeout` expires with unmet probes, run continues but RunQuality records `settle phase incomplete: N/M probes succeeded` and downgrades the verdict to UNTRUSTED.

### 1.5 Async JetStream — `published_queued` vs `published_acked` in summary

Today the printed summary shows `Published: N` where `N` is the async-publish queue depth count. Acked count is only visible by subtracting `loadgen_publish_errors_total{reason="async_ack"}` from `loadgen_published_total` on the metrics endpoint — which doesn't outlive the run.

**Design:** the printed summary, for `--inject=canonical` runs, prints two numbers and a status flag:

```
Published (queued): 30021
Published (acked):  30019
Async ack errors:   2 (drain_timeout=false)
```

Drain-timeout status comes from `js.PublishAsyncComplete()` already being awaited at shutdown; we just need to thread the outcome back into the summary.

### 1.6 App-level error responses in read scenarios

Today `read_generator.go:110, 233` and `room_generator.go:97` observe only the transport `err` from `nc.RequestMsg`. The SUT signals app errors via `model.ErrorResponse` in the reply payload — these are counted as success.

**Design:** read scenarios decode the reply payload (or its first 32 bytes) and check for the canonical error envelope. Add a `RequestError` reason label to `loadgen_request_errors_total`. The Collector's `errored` counter becomes accurate.

### 1.7 Warmup-error exclusion from headline counters

Today `Sent`, `PublishErrors`, and a few derived numbers in the summary are pulled from Prometheus counter totals that include the warmup window. A noisy warmup leaks into the verdict.

**Design:** every counter that participates in the printed summary OR the abort-watcher decision must be scoped to the measured window. Two ways:

- Add a `phase` label (`warmup|measured`) to all such counters. Already exists on `loadgen_published_total`; extend to errors, requests, reply correlations. Sum `{phase="measured"}` for the summary. (Recommended — Prometheus-side filtering is already how the dashboards work.)
- Snapshot counters at `warmupDeadline` and subtract at finalize. (Avoid — fragile, hides the warmup numbers entirely.)

### 1.8 Run artifacts persisted to a bundle

The SRE reviewer flagged that today, after `loadgen run` exits, everything that produced the verdict is gone (HDR bin counts in-process, Prometheus scrape ends with the metrics server, `--csv` is opt-in and capped at 100k samples). "Why was that run DEGRADED at 14:02" has no answer.

**Design:** every run writes to `tools/loadgen/runs/<run_id>/` (configurable via `--runs-dir`). The bundle contains:

- `summary.json` — verdict, settle outcomes, ack-vs-queued counts, omission p99, exit reason, full flag set, git SHA of loadgen, SUT image digests (read from `docker inspect`).
- `histograms.hlog` — HDR log file in the native HdrHistogram log format. One block per (scenario, kind, phase) cell. Mergeable across runs via `hdrhistogramlogprocessor` and the planned `scripts/compare-runs.sh`.
- `timeseries.parquet` (or jsonl if parquet adds dep weight) — per-second snapshot of rate, p99, errors. Source: Collector ticker, not Prometheus scrape, so it survives metrics-server shutdown.
- `settle.json` — N/M probe outcomes per RAW path.
- `flags.txt`, `env.txt` (secrets redacted), `stdout.log`, `stderr.log`.
- `metrics.prom` — final scrape snapshot, written at finalize *before* the metrics server stops (closes the gap the DX reviewer also flagged: today `/metrics` dies with the run).

Storage: local filesystem default. `--runs-s3=s3://bucket/prefix/` opts into S3 / MinIO upload (the MinIO container is already in the compose stack via `pkg/minioutil`). Spec does not mandate Mongo — wrong shape for blobs.

Retention: bundle directories survive `loadgen teardown`. A `loadgen runs prune --older-than=14d` subcommand handles housekeeping.

### 1.9 Run isolation for concurrent operators

The SRE reviewer flagged that two engineers running loadgen against the same SUT today collide on: same Mongo `loadgen` DB, same deterministic fixture IDs (`--seed=42`), same JetStream durable consumer names, same correlation tracker.

**Design:** every loadgen-owned resource is prefixed by a short `run_id` (7-char prefix of the UUIDv7):

- `MONGO_DB=loadgen_<short_run_id>` (the existing `loadgen` DB-name isolation guard widens to accept any `loadgen_*` prefix).
- Durable consumer names `loadgen_<short_run_id>_<purpose>` (e.g. `loadgen_a1b2c3d_e1obs`).
- Correlation tracker filters incoming canonical/broadcast messages by their `X-Loadgen-Run-ID` header — messages from another concurrent run are ignored.
- `--allow-concurrent=false` default: at startup, the harness writes a row into a shared `loadgen_runs` Mongo collection (`{run_id, started_at, host, scenario}`). Refuses to proceed if another row is active for the same SUT URL within the last `--run-ttl=2h`. `--allow-concurrent=true` overrides for two-machine campaigns.

Phase 1 exit: every run prints a verdict, every untrustworthy run is marked, no run silently reports clean numbers it cannot defend; every run persists a bundle; two operators on the same machine can run concurrently without stomping each other.

### 1.10 Teardown --force for orphan recovery

A crashed loadgen (OOM, network partition, host reboot) leaves: seeded Mongo collections, JetStream durable consumers (no current cleanup), `loadgen_*` rooms in shared Mongo, pending async-publish acks.

**Design:** `loadgen teardown --force [--older-than=24h] [--run-id=X]`:

1. Enumerates Mongo databases matching `loadgen_*` and JetStream durable consumers matching `loadgen_*` via NATS JSAPI.
2. Cross-references against `loadgen_runs` Mongo collection (§1.9) to identify orphans (no live process for `run_id`, or `started_at + --run-ttl < now`).
3. Drops each idempotently, logs what it removed, exits 0.

`teardown --force --run-id=X` skips heuristics and drops a specific run's resources by ID.

### 1.11 Alert rules shipped with v2

The SRE reviewer flagged that `prometheus.yml` has no rule files and the dashboard has no alert blocks. v2 ships `deploy/prometheus/rules/loadgen.yml` with the rules below, enabled by default in the dashboards profile:

```promql
# UNTRUSTED run in progress (gauge set at finalize, scraped before metrics-server stops)
- alert: LoadgenUntrustedRunActive
  expr: loadgen_run_quality{verdict="UNTRUSTED"} == 1
  for: 30s

# Coordinated omission exceeding budget
- alert: LoadgenOmissionBudgetExceeded
  expr: histogram_quantile(0.99, sum(rate(loadgen_omission_deficit_seconds_bucket{dropped="false"}[1m])) by (le)) > 0.1
  for: 2m

# Async ack drain wedging
- alert: LoadgenAsyncAckBacklog
  expr: rate(loadgen_publish_errors_total{reason="async_ack"}[1m]) > 1
  for: 2m

# RAW lag regression (Phase 3.1)
- alert: LoadgenRAWHistoryLagP99
  expr: histogram_quantile(0.99, sum(rate(loadgen_raw_lag_seconds_bucket{path="history"}[2m])) by (le)) > 1
  for: 3m

# Loadgen self-saturation (back-pressure on the harness)
- alert: LoadgenSelfSaturated
  expr: |
    sum(rate(loadgen_publish_errors_total{reason="saturated"}[1m]))
    + sum(rate(loadgen_request_errors_total{reason="saturated"}[1m])) > 10
  for: 1m
```

### 1.12 v2 Grafana dashboards as Phase 1 deliverables

The SRE reviewer flagged that today's dashboard is a scalar grid; an operator under fire wants heatmaps, omission-vs-throughput correlation, RAW-lag panels, RUN QUALITY annotations.

**Design:** `tools/loadgen/deploy/grafana/dashboards/v2/` ships:

- `loadgen-overview.json` — RUN QUALITY annotation strip, per-scenario rate, p99 heatmap (`loadgen_request_latency_seconds_bucket`), omission p99 (split by `dropped` label).
- `loadgen-raw.json` — per-path RAW lag heatmaps, visibility-window distributions.
- `loadgen-federation.json` — four sub-stage histograms, end-to-end lag.
- `loadgen-system.json` — cAdvisor scrape (added to `prometheus.yml` as a new job), NATS JetStream consumer lag via `prometheus-nats-exporter` (now a hard dep, not a TODO), Cassandra commit-log latency via `cassandra-exporter`.

These are part of **Phase 1 exit**, not Phase 3 — without them v2 is operationally regressing from v1.

### 1.13 Security and credentials

Phase 3.8 (`auth-load`) and 3.9 (`federation-lag`) introduce credential-management requirements. v2 commits to:

- **Per-fixture-user JWTs**: `loadgen seed --with-jwts` mints them into `tools/loadgen/runs/<run_id>/creds/` (mode 0600, gitignored). The directory is removed by `teardown`. Auth-service's signer is invoked over its admin RPC; no signing keys ship in the loadgen process.
- **Federation peer NKey**: `seed --with-federation` provisions ephemeral peer NKeys, written to the same per-run creds dir, registered with both sites' auth-services, revoked at teardown.
- **.gitignore additions** as a Phase-1 deliverable: `tools/loadgen/runs/`, `tools/loadgen/creds/`.
- **No secrets in `summary.json` or stdout.log.** A redaction allow-list (`AUTH_TOKEN`, `NATS_CREDS_FILE`, `MONGO_URI` password) is applied at finalize. CLAUDE.md §5 forbids committing `.env`; this just extends the rule to artifact bundles.

### 1.14 Soak campaign script

`scripts/run-soak.sh` (default DURATION=8h, rotates artifact bundles hourly, re-evaluates RUN QUALITY every hour and logs trajectory). `scripts/run-campaign.sh` orchestrates baseline → ramp → 60-min sustained → soak as four phases of one campaign with a single rolled-up verdict. Without this, "trustworthy" stops at minute 5.

## Phase 2 — Restructure (post-Phase-0)

Goal: Phase 0 already extracted `Runtime`, `runFlags`, and the transport adapters. Phase 2 introduces the `Scenario` interface + registry (the *behavior* refactor that interacts with new scenarios in Phase 3), and migrates per-scenario string switches to the registry.

Phase 2 deliverables:

- `scenario.go` — interface family (§2.2).
- `scenario_messaging.go`, `scenario_history.go`, `scenario_search.go`, `scenario_room.go` — one file per existing scenario, registered via `init()`.
- Removal of every `switch *scenario` in `runRun`, `readiness.go`, `auto_warmup.go`, `flags.go` validators.
- `runtime.Subscribers()` accessor for §3.2 large-room-broadcast.
- `Runtime` parameterized over `[]SiteDeps` (default len 1).

`runRun` < 100 lines.

### 2.1 Runtime lifecycle struct

`runRun` does 4 distinct phases (setup → preflight → run → finalize) collapsed into one function. Extract a `Runtime` struct in `tools/loadgen/runtime.go`:

```go
type Runtime struct {
    cfg         config
    runID       string
    nc          *nats.Conn
    js          jetstream.JetStream
    pool        *ConnPool
    metrics     *Metrics
    collector   *Collector
    metricsSrv  *http.Server
    pprofSrv    *http.Server
    samplers    []sampler
}

func NewRuntime(ctx context.Context, cfg config, runID string) (*Runtime, error)
func (r *Runtime) Preflight(ctx context.Context, sc Scenario) error
func (r *Runtime) Run(ctx context.Context, sc Scenario, gen Runner, rf runFlags) (Summary, ExitReason, error)
func (r *Runtime) Finalize(ctx context.Context) error
func (r *Runtime) Close() error
```

`runRun` becomes ~80 lines: parse flags → construct runtime → preflight → run → finalize → emit exit code.

The four watchers (progress / abort / liveness / async-drain) become methods on `Runtime`, each owning their goroutine + cancel channel. They register themselves with a `watcherGroup` that the runtime tears down in `Close()`. No more inline closures with captured-variable spaghetti.

### 2.2 Scenario interface + registry

Today the string `"history-read" | "search-read" | "room-rpc" | "messaging-pipeline"` switches in six files. Adding a fifth scenario is a 6-file diff. Replace with a **split** interface family (the architecture reviewer flagged a single 7-method interface as a god-interface in waiting):

```go
// Identity — every scenario implements this.
type Scenario interface {
    Name() string
    DefaultPreset() string
}

// Optional probe contracts; scenarios opt in by also implementing them.
type ReadinessProber interface {
    BuildReadinessProbe(deps ScenarioDeps) func(context.Context) error
}
type LivenessProber interface {
    BuildLivenessProbe(deps ScenarioDeps) func(context.Context) error
}
type AutoWarmer interface {
    NeedsAutoWarmup(p *Preset) bool
}

// Required: the generator factory.
type GeneratorFactory interface {
    NewGenerator(deps ScenarioDeps, rf runFlags) (Runner, error)
}

var scenarios = map[string]Scenario{}
func RegisterScenario(s Scenario) { scenarios[s.Name()] = s }
```

Runtime probes for interfaces at call time:

```go
if pr, ok := sc.(ReadinessProber); ok && pr != nil { ... }
```

This keeps the surface honest — a federation scenario does not pretend to have a single readiness probe; an auth scenario does not pretend to use auto-warmup. Each new scenario lives in its own file (`scenario_messaging.go`, `scenario_history.go`, `scenario_search.go`, `scenario_room.go`, plus new ones from Phase 3) and registers in `init()`.

`ScenarioDeps` is **also split** rather than a single fat struct — Phase 3.9 federation needs two NATS handles and two fixture sets, which a single struct cannot represent. Define narrow accessor interfaces (`Publisher()`, `Requester()`, `Collector()`, `Fixtures()`, `Sites() []SiteDeps` for federated scenarios) and let scenarios narrow to what they need.

**Scenarios that extend Runtime.** The registry covers §3.1, §3.3, §3.4, §3.5, §3.6, §3.7, §3.8. It does *not* cover:

- **§3.2 large-room-broadcast** needs a long-lived subscription manager spanning the whole run (10 000 wildcard subs across the pool). That's Runtime-shaped state; expose via `runtime.Subscribers()` accessor.
- **§3.9 federation-lag** needs two sites. Either `Runtime` becomes parameterized over a slice of sites, or federation gets a `FederatedRuntime` wrapper. Pick the former (parameterized slice with `len==1` default).
- **§3.11 chaos overlay** is correctly not a generator — a compose overlay plus a script. No `Scenario` impl.

### 2.3 Move transport adapters out of main.go

`natsCorePublisher`, `natsRequester`, `newAsyncErrHandler`, `jetstreamPublishOpts`, `gatheredCounterValue`, `counterValue`, `counterValueLabeled` are not orchestration — they are transport / observability adapters. Move to:

- `publisher.go` — `natsCorePublisher`, `newAsyncErrHandler`, `jetstreamPublishOpts`
- `requester.go` — `natsRequester`
- `metrics_helpers.go` (or fold into `metrics.go`) — counter-value helpers

### 2.4 runFlags struct

The 30 inline `flag.*Var` calls in `runRun` become a typed struct:

```go
type runFlags struct {
    Scenario     string
    Preset       string
    Rate         int
    Duration     time.Duration
    Warmup       time.Duration
    Inject       string
    CSV          string
    AutoWarmup   AutoWarmupFlags
    Ramp         RampFlags
    Abort        AbortFlags
    Liveness     LivenessFlags
    Conn         ConnFlags
    Progress     ProgressFlags
    Settle       SettleFlags // new in Phase 1
    Quality      QualityFlags // new in Phase 1
}

func ParseRunFlags(args []string) (runFlags, error)
```

Each sub-struct's `*Var` registration lives next to the sub-struct definition. `ParseRunFlags` becomes testable without exec'ing the binary.

### 2.5 Shared `headers.go`

`X-Loadgen-Run-ID` is a string literal in `main.go`. The wire-stub types in `request_builder.go` (for cross-service request shapes that aren't importable from history-service's `internal/`) are duplicated.

**Design:** new `tools/loadgen/headers.go` with `HeaderRunID = "X-Loadgen-Run-ID"` and any other shared constants. If SUT services ever want to import this, the constant lives in a single source of truth.

Phase 2 exit: `main.go` < 200 lines; adding a 5th scenario is `scenario_X.go` + `preset.go` mix-field addition + done.

## Phase 3 — New scenarios

Goal: every README "Non-goals" line is either implemented or has a justified deferral note.

Each scenario gets its own file (per §2.2), its own preset (or shares an existing one), and its own per-scenario script under `scripts/`. Each scenario lists: subjects it publishes/requests, fixtures it consumes/creates, success metric, abort metric.

### 3.1 Read-after-write consistency timing

**Scenario name:** `raw-consistency`. New file `scenario_raw.go`.

Mechanism: publish a message via the frontdoor (gatekeeper) and immediately enter a poll loop calling `LoadHistory` for that room until the message appears. Record `publishedAt → firstVisibleAt` lag in a histogram. Same for `SearchMessages` (separate histogram) and `GetMessageByID` (separate histogram).

**Poll-interval bias — must be controlled.** The naive poll loop has uniform [0, `poll_interval`] additive bias: observed p50 is `real_p50 + poll_interval/2`. The methodology reviewer flagged this as a blocker for the scenario's defensibility. v2 ships both mitigations:

1. **Tight default poll interval.** Default `--raw-poll-interval=10ms`. The expected p50 for in-process history reads is ≥5ms, so the bias is ≤2ms — small relative to the measurement.
2. **Visibility-window upper bound, reported alongside the lag.** Each RAW probe records two numbers: `firstVisibleAt - publishedAt` (the reported lag) AND `firstVisibleAt - lastNotVisibleAt` (the upper-bound width). Summary prints both: `RAW lag p99: 145ms (visibility-window p99: 25ms)`. Operators see the uncertainty explicitly.
3. **Hard guardrail.** If `poll_interval > 0.2 × measured_p50` at run end, RunQuality records `raw poll-interval too coarse` and downgrades to DEGRADED.

Backpressure: bounded concurrent in-flight RAW probes via the existing sem pattern.

Metric: `loadgen_raw_lag_seconds{path="history|search|getbyid"}` histogram + `loadgen_raw_visibility_window_seconds{path=...}` companion histogram.

Preset: shares `messaging-pipeline` fixtures plus a new `RAWConfig` block (poll interval, timeout, max in-flight).

### 3.2 Large-room fan-out

**Scenario name:** `large-room-broadcast`. New file `scenario_largeroom.go`. The chat-domain reviewer flagged that a single "10k announce room" shape misses three other large-room archetypes that stress different bottlenecks. v2 ships **three presets** sharing the scenario:

- `announce-room` — 10 000 members, write-rate ~1/min, read-rate huge on each post. Stresses fan-out tables and subscription-collection read amplification.
- `firehose-room` — 1 000 members, write-rate 50/sec sustained, read-rate moderate. Stresses broadcast-worker per-room semaphore and JetStream MaxAckPending.
- `bot-room` — 100 members, write-rate 200/sec from 3 bot accounts. Stresses Cassandra hot-partition (`messages_by_room` writes), message-worker throughput.

Mechanism: subscribe to all room members' inboxes via the `ConnPool` (10k subscriptions overflow a single NATS connection; spread across `--connections=N`). Measure publish → per-member-delivery latency.

**Fan-out completion math (pinned per methodology reviewer).** Two metrics, both reported:

- **Per-message completion ratio at the message's p99 mark.** For each broadcast, compute `delivered_count_at_msg_p99 / total_members`. Report the distribution: "by p99 of fan-out time, N% of members had received it." Histogram bucket: 0.0–1.0 in 0.01 steps.
- **Per-recipient receive-latency p99.** For each (message, recipient) pair, record receive-latency. Report standard p50/p99/max histogram.

Subscription churn during a run is *frozen* (members can't be added/removed mid-run for this scenario) or the denominator drifts; spec requires the scenario to refuse mutations on its target rooms.

Output: `loadgen_largeroom_completion_ratio{quantile="p99",preset=...}` (Prometheus summary) + `loadgen_largeroom_receive_latency_seconds{preset=...}` histogram.

### 3.3 Presence / typing churn

**Scenario name:** `presence-typing`. New file `scenario_presence.go`.

**SUT readiness gate.** Phase 3 inventory: if `pkg/subject` does not yet define presence/typing subject builders, this scenario is **DEFERRED-UNTIL-SUT-READY** and tracked in `tools/loadgen/CHANGES.md` as a known gap. The plan must check this on entry and skip §3.3 if subjects are missing, not invent stub subjects.

Mechanism (when shippable): per-user typing-indicator emit, **rate-limited at the generator to model client-side throttling**. The chat-domain reviewer flagged that real clients throttle to ~1 typing event per (user, room) per 3 seconds (Slack/Discord). Default: 1 event / 3s per (user, room) for users-currently-typing. `--typing-unthrottled` knob disables the client-side limit for finding the SUT ceiling.

Metric: emit-rate vs delivered-rate ratio (single number per second + per-second histogram), observer-side fan-out p99. Pin the definition explicitly: `emit_at_loadgen → received_by_loadgen_observer_subscribed_to_room`.

Output: `loadgen_presence_delivered_ratio` gauge, `loadgen_presence_fanout_seconds{preset=...}` histogram.

### 3.4 Notification fan-out

**Scenario name:** `notification-fanout`. New file `scenario_notif.go`.

Mechanism: publish messages with mentions at a configurable rate (extending the existing `MentionRate` preset field). Subscribe to the notification-worker's output. Measure publish → notification-emit lag, **per delivery channel**.

The chat-domain reviewer flagged that single-path measurement is insufficient: production notifications fork into in-app event (always), mobile push (only if user offline + not DND), email digest (only if unread > N minutes). v2 ships:

- **Phase 3.4a (immediate):** in-app notification lag — `publishedAt → loadgen_subscriber_received_at` on the notification subject. Pin: `subject.Notification(account)`.
- **Phase 3.4b (later in Phase 3):** preset varies `offline_user_fraction` (default 0.3) and `dnd_user_fraction` (default 0.1) so the SUT's routing branches are exercised. Loadgen does *not* try to receive a real mobile push — instead measures notification-worker's branch-emit latency by subscribing to the per-channel routing subjects (if they exist; gate per §3.3 SUT-readiness pattern).

Metric: `loadgen_notification_lag_seconds{channel="inapp|push|email"}` histogram, per-(scenario, mention_count_bucket).

### 3.5 Thread fan-out

Not a new scenario — fixes the existing realism gap (`ThreadRate` is set in presets but never read by `Generator.publishOne`). Wire `ThreadRate` into the publish path so a fraction of messages target an existing thread (or become thread parents on first emit). Add `loadgen_thread_messages_total` counter and measure thread-targeted publish latency as a separate kind label.

### 3.6 Message edit/delete

**Scenario name:** `message-mutate`. New file `scenario_mutate.go`.

Mechanism: maintain a rolling set of recently-published message IDs from a co-running messaging-pipeline phase. At configurable rate, emit edit and delete requests on the appropriate subjects (`chat.msg.canonical.{siteID}.edited` / `.deleted` per CLAUDE.md §6). Measure latency to canonical-event observation.

Preset: extends `messaging-pipeline` preset with `EditRate` and `DeleteRate` mix.

### 3.7 Subscription churn

**Scenario name:** `subscription-churn`. New file `scenario_subschurn.go`.

Mechanism: at configurable rate, emit subscription joins (member-invite path) and leaves (member-remove path) against the seeded room/user population. Measure per-(action) latency and target the MongoDB `subscriptions` collection write amplification.

Note: this scenario must NOT consume the seeded fixture pool destructively — it operates on a separate `loadgen-churn-` prefixed subset of rooms/users created by `seed --include-churn-fixtures` so steady-state numbers across runs are comparable.

### 3.8 Auth load

**Scenario name:** `auth-load`. New file `scenario_auth.go`.

Mechanism: HTTP benchmark against `auth-service` (`/login`, `/refresh`, `/validate` per actual API surface — read `auth-service/main.go` for endpoints). Resty-driven open-loop ticker. Add reconnect-storm preset: spin up M idle connections, drop them all, measure recovery.

Stack: requires `auth-service` + its DB in the compose file. Adds it.

### 3.9 Cross-site federation

**Scenario name:** `federation-lag`. New file `scenario_federation.go`.

Mechanism: bring up *two* sites in compose (site-a, site-b) with OUTBOX/INBOX wiring. The methodology reviewer flagged "one number summing four sub-latencies" as a blocker — the reported "federation lag is 800 ms" tells operators nothing about whether the bottleneck is outbox queueing, source-lag, remote-persist, or read-visibility. v2 measures all four sub-paths, in addition to the end-to-end:

1. `publishedAt_site_a → outbox_queued_on_a` (local OUTBOX append visible — subscribe to `outbox.{siteA}.>` on site-a's NATS).
2. `outbox_queued_on_a → inbox_received_on_b` (cross-site sourcing — subscribe to `INBOX_{siteB}` stream on site-b).
3. `inbox_received_on_b → persisted_on_b` (remote write — observe via the `chat.msg.canonical.{siteB}.created` subject on site-b).
4. `persisted_on_b → visible_via_read_on_b` (read-after-write fence on the remote — poll `LoadHistory` like §3.1).

Reported as four separate histograms `loadgen_federation_lag_seconds{stage="outbox|inbox|persist|visible"}`, plus an end-to-end `loadgen_federation_e2e_lag_seconds`. Summary prints all five lines.

**Federation flap.** A second mode (`--federation-flap`) cycles site-b down/up every N seconds for the duration of the run, measures INBOX backlog drain time when site-b returns, and reports `loadgen_federation_drain_seconds`.

**Cross-site read of remote room.** A third sub-scenario (separate run) — a user on site-a reads history of a room homed on site-b. Exercises a different code path than local read; reported as `loadgen_federation_cross_read_seconds`.

Stack: doubles the compose footprint. Add `docker-compose.federation.yml` overlay (default-off, gated by Compose `profiles:`) and a `--federation` flag on the run-* scripts.

### 3.10 WebSocket gateway

**Status:** DEFERRED if no WS gateway exists in the SUT yet. If/when one ships, scenario `ws-fanout` measures WS-connect storm + per-connection broadcast-delivery latency.

### 3.12 DM-vs-channel traffic split (chat-domain blocker)

The chat-domain reviewer flagged that v1's "realistic" preset is channel-heavy (Zipf senders across a fixture of mostly channel rooms), while production chat is 50–80% DM-shaped. DMs are 2-member rooms (no fan-out cost) with 100% read-receipt + notification pressure per write — a fundamentally different SUT shape.

**Design:**

- Add `DMRatio float64` to `Preset` (e.g. `realistic` gets 0.6; `channel-heavy` 0.1; `dm-heavy` 0.85).
- `messaging-pipeline` generator picks a room type per publish according to `DMRatio`, then picks within the type. DM rooms use `idgen.BuildDMRoomID(a, b)` ID semantics; channel rooms keep base62 IDs.
- New preset `dm-heavy` (85% DM, mention rate proportional, no large rooms) for DM-tail capacity tests.
- Headline summary line breaks down counters by room type: `Sent: 30000 (channel=12000, dm=18000)`.

### 3.13 First-DM hot path (chat-domain blocker)

Per CLAUDE.md §6, `idgen.BuildDMRoomID(a, b)` is deterministic but the room is created lazily on first send. "First message between A and B ever" goes through room-create + subscription-create + canonical publish in one tick — the #1 reported p99 outlier in DM-heavy products.

**Scenario name:** `first-dm`. New file `scenario_firstdm.go`. New preset `first-dm` with a dedicated user pool consumed monotonically (so each (A, B) pair fires exactly once per run).

Mechanism: pick (A, B) from the unconsumed pool, send the first message, measure `publishedAt → roomCreated, → subscriptionsCreated, → canonicalPersisted` as separate sub-lags.

Metric: `loadgen_first_dm_lag_seconds{stage="room|subs|persist|e2e"}`.

### 3.14 Room-open composite read amplification (chat-domain blocker)

Real clients opening a room fire a correlated burst: `MsgHistory` + `RoomsGet` (or `RoomsInfoBatch`) + presence subscribe + `MessageRead` write + restricted-rooms cache lookup. v1 ran each as an independent open-loop ticker — the SUT never saw the correlated burst that drives Mongo `subscriptions` connect-storm and Valkey cache miss amplification.

**Scenario name:** `room-open`. New file `scenario_roomopen.go`.

Mechanism: per tick, simulate one client opening one room → issue the bundle (5 requests, fired in parallel from the same goroutine via `errgroup`). Report **slowest-leg p99** as the headline (because that's what the user experiences as "room loaded") plus per-leg p99 as a diagnostic table.

Metric: `loadgen_room_open_seconds{leg="history|rooms_get|presence|read|restricted"}` histogram + `loadgen_room_open_e2e_seconds` for the slowest-leg.

### 3.15 Bursty publish patterns

The chat-domain reviewer flagged that p99 regressions hide under bursts, not steady load. v2 adds a **burst envelope** to the generator:

- `Preset.BurstPeriod time.Duration` (e.g. 30s)
- `Preset.BurstRatio float64` (e.g. 5.0 — peak rate is 5× baseline)
- `Preset.BurstDuration time.Duration` (e.g. 10s of high rate)

Generator's tick scheduler reads the burst envelope and rebuilds the ticker at boundaries. New preset `incident-burst` (5 rps baseline → 200 rps for 10s every 30s). Metric: existing `loadgen_rate_bucket` label already covers the rate split; no new metric needed.

### 3.16 Read receipts

The chat-domain reviewer flagged read receipts (1 read RPC per message-viewed per user) as the silent killer in chat. v2 adds:

**Scenario name:** `read-receipts`. New file `scenario_readreceipts.go`.

Mechanism: shares `messaging-pipeline` fixtures. Maintains a rolling set of recent message IDs from a co-running pipeline. Fires `MessageRead` for a configurable subset of recipients per message (default 60% — models the fraction of readers who actually see the message before notification mutes).

Metric: `loadgen_message_read_seconds{room_type="channel|dm"}` histogram. Federation note: per `SubscriptionReadEvent` (OUTBOX type `subscription_read`), a `MessageRead` on a remote-homed room generates cross-site traffic. When `--federation` is on, the federation-lag scenario should pick up this load too.

### 3.11 Chaos / failure injection

**Scenario:** not a new generator — an overlay. New file `tools/loadgen/deploy/docker-compose.chaos.yml` adding `pumba` or `toxiproxy` between services. Add `scripts/run-chaos.sh` driving a baseline `messaging-pipeline` with periodic network partitions / packet loss / slow links applied to a chosen service. Measure SUT graceful-degradation behavior.

Phase 3 exit: README's "Non-goals" section is updated. Every disclaimed item is either implemented or has a follow-up issue link.

## Cross-phase concerns

### Backwards compatibility

- Scripts in `scripts/` keep their names and env-var contracts. Internals can change.
- Printed-summary table layout: existing columns stay, new lines are *additions* (RUN QUALITY block at the top, published_acked beside published_queued, omission block in a new section). Operator tooling that greps `^P99` / `^MissingBroadcasts` keeps working.
- CSV column set: unchanged for existing columns; if HDR sidecar drops raw samples by default, `--csv-raw=true` opts back into the old behavior.

### Docker Compose v1 and v2 compatibility

The harness must run unmodified on **both** `docker-compose` (v1, the Python tool, hyphenated CLI; v1.29.2 floor) and `docker compose` (v2, the Go plugin, space-delimited CLI; v2.0 floor). Many operator environments still ship v1 (some Ubuntu LTS, some RHEL with the legacy package), and some only have v2.

**Design (revised against DevOps-reviewer findings):**

- Drop the first-draft "Compose spec ≤ 3.8" ceiling — it's obsolete. The Compose Specification superseded versioned schemas; the file **omits `version:` entirely**. Both v1.27+ and v2 accept this.
- **Centralize compose detection.** Ship `tools/loadgen/scripts/lib/compose.sh` (sourced once by every other script and by `loadgen doctor`):

```sh
# lib/compose.sh — source me, don't exec me
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  v=$(docker compose version --short 2>/dev/null || echo 0)
  case "$v" in 2.*|[3-9].*) export DC="docker compose"; export DC_KIND="v2" ;; *) ;; esac
fi
if [ -z "${DC:-}" ] && command -v docker-compose >/dev/null 2>&1; then
  v=$(docker-compose version --short 2>/dev/null || echo 0)
  case "$v" in 1.29.*|1.[3-9]*|[2-9].*) export DC="docker-compose"; export DC_KIND="v1" ;; *) ;; esac
fi
if [ -z "${DC:-}" ]; then
  echo "ERROR: need docker compose (v2.0+) or docker-compose (v1.29.2+)" >&2; exit 2
fi
dc() { $DC "$@"; }
export COMPOSE_PROJECT_NAME=loadgen
```

  Scripts source via `. "$(dirname "$0")/lib/compose.sh"`. The Makefile uses `DC ?= $(shell scripts/lib/compose.sh print-cmd)` (the same script when run with `print-cmd` arg, prints DC and exits).
- **Compose file fixes (Blocker).** Current `deploy/docker-compose.loadtest.yml` has top-level `name: loadgen` — **v2-only, must be removed**. Project naming moves to `COMPOSE_PROJECT_NAME=loadgen` exported from `lib/compose.sh`.
- **Floor enforcement.** `lib/compose.sh` rejects v1 below 1.29.2 (`profiles:` and `service_completed_successfully` require it) and v2 below 2.0 (some distros backport "v1.29.x" as the plugin name).
- **`profiles:` works in both.** `dashboards`, `federation` (default-off), `auth` (default-off) profiles gate optional services.
- **Healthcheck quirks.** Add `start_period: 30s` to Cassandra/Elasticsearch healthchecks (no-op on too-old v1, helps on supported versions). `condition: service_completed_successfully` (already in use) requires v1.29+ — covered by the floor.
- **Nested-invocation env propagation.** `export COMPOSE_PROJECT_NAME=loadgen` in `lib/compose.sh` ensures child processes (umbrella scripts spawning per-scenario scripts) address the same network. `COMPOSE_PROFILES` does NOT stack across nested calls — child scripts re-pass `--profile` explicitly.
- **CI validation.** `make -C tools/loadgen/deploy validate-compose` runs `dc config -q` under whichever CLI is present, plus (if both CLIs are installed in the CI image) a v1-and-v2 cross-check. A `make smoke` target additionally runs `dc up -d --wait && dc down` with a 60s timeout under v2 only (v1 install in CI is fragile).
- **Operator UX for too-old plugin.** `loadgen doctor` prints the floor requirement and the install link if detection fails.

### Operator UX — discovery and diagnostics

The DX reviewer flagged that ship-and-document-later is too late for a tool that prints TRUSTED / DEGRADED / UNTRUSTED verdicts; operators need self-service paths the moment v2 lands. v2 ships:

**Subcommands (extending the `loadgen` binary, not new scripts):**

- `loadgen scenarios` — prints `name | preset | what it loads | typical duration | SUT deps` for every registered scenario. Reads the registry from §2.2 — trivial post-Phase-2.
- `loadgen presets` — same shape for presets.
- `loadgen recommend --target-rps=N --duration=5m` — picks a preset, suggests `--abort-window-max-samples`, warns if host doesn't have enough RAM.
- `loadgen doctor` — runs the host preflight (Docker version, free RAM, free disk, port collisions, compose v1/v2 detection) and prints a remediation checklist.

**Diagnostic scripts (under `scripts/`):**

- `compare-runs.sh OLD_RUN_ID NEW_RUN_ID` — diffs two artifact bundles (§1.8). Prints per-(scenario, kind) Δp50/p95/p99, verdict transition, omission delta, RAW-lag delta. Leverages HDR's merge story.
- `triage.sh RUN_ID` — auto-collects diagnostics for an UNTRUSTED run: `docker logs --since=Nm` for each SUT service, NATS `varz`/`connz`/`jsz`, the loadgen metrics endpoint snapshot, the abort-watcher deafened-by-cap log lines. Writes to `runs/<run_id>/diagnostics.tgz`.
- `bisect.sh GIT_SHA_OLD GIT_SHA_NEW SCENARIO` — operator hands two SHAs; script checks out each, rebuilds the loadgen-targeted services, runs the same scenario, prints a Δ against `compare-runs.sh` math.
- `preflight.sh` — host check (invoked from `up.sh` and `loadgen doctor`).
- `new-scenario.sh NAME` — scaffolder that writes `scenario_NAME.go` (with `Scenario` interface stubbed against the §2.2 registry), adds a preset entry, copies `run-history.sh` as `run-NAME.sh`, prints next steps. Makes "≤2-file change" a real promise rather than a footnote.

**Remediation pointers on every UNTRUSTED Issues line.** When the summary prints `"abort watcher deafened by sample cap"`, it appends `→ see USAGE.md#abort-watcher-deafened`. Same for every Issues string; the pointer is part of the string template.

**`--diagnose` flag.** When a run ends UNTRUSTED, `--diagnose` auto-re-runs at half the rate with `--progress-interval=2s --liveness-interval=5s --csv=runs/<run_id>/diagnose.csv` and writes a markdown comparison vs the original run.

**`--metrics-linger=30s` (default).** Keeps the `/metrics` server alive past `runFinalize()` so post-hoc curl-scrapes work without bringing up the dashboards profile. Closes the v1 "metrics endpoint dies with the run" gap.

**`--legacy-summary` flag** for one v2.x release: prints the v1-shape summary (no verdict block, no omission line, no queued/acked split) for teams whose tooling can't be updated in lockstep. Removed in v3.

**`scripts/README.md`** — categorized index of every script (Lifecycle, Per-scenario, Umbrella, Diagnostic, Dev). Generated from each script's `--help` output via `make docs-scripts`.

### Operator UX — scripts and presets

`tools/loadgen/scripts/` ships 13 helper scripts today. v2 keeps every existing script working (per the backwards-compatibility rule) and adds one per new scenario. Audit and update them as part of each phase:

- **Phase 1 deliverable:** every existing script gains a `--quality-budget` env var (default sane) and the run output is filtered through a `summary-with-verdict` wrapper that surfaces the new RUN QUALITY block. Scripts gain `set -euo pipefail` if any are missing it, and a `--help` flag printing usage.
- **Phase 2 deliverable:** scripts switch to the `dc()` shell function so they work under both compose v1 and v2. No interface changes for operators.
- **Phase 3 deliverable:** one new `scripts/run-<scenario>.sh` for each new scenario from §3.1–§3.11. Naming pattern matches existing: `run-raw.sh`, `run-largeroom.sh`, `run-presence.sh`, `run-notif.sh`, `run-mutate.sh`, `run-subschurn.sh`, `run-auth.sh`, `run-federation.sh`, `run-chaos.sh`. Each follows the existing template (env-var knobs with defaults, sensible 30–120s duration, prints final summary + verdict).

Two new umbrella scripts:

- `scripts/run-trustworthy-baseline.sh` — runs the canonical "is this build sane?" set: messaging-pipeline + history-read + search-read + room-rpc at conservative rates, asserting RUN QUALITY=TRUSTED for each. Single-command full sanity check. Used by operators after a deploy.
- `scripts/run-saturation-sweep.sh` — runs ramp scenarios for each read service to find each one's saturation knee, prints a per-service table at the end.

### Documentation — README split, USAGE.md, CHANGES.md

The current `tools/loadgen/README.md` (233 lines) bundles operator usage, scenario reference, internal architecture, troubleshooting, and non-goals. The tech-writing reviewer flagged that v2 expands this further (RUN QUALITY, new scenarios, glossary), and that a recipes-after-reference order is wrong for first-time readers — recipes should come *before* the reference encyclopedia.

**Design:** split into three documents in the same folder. Each has a strict budget; per-scenario long-form discussion (if it exceeds the budget) spills into `tools/loadgen/docs/scenarios/<scenario>.md` rather than bloating USAGE.md.

#### `tools/loadgen/README.md` (≤80 lines)

- One-paragraph statement of what loadgen is.
- The three commands of quick-start (up / quickstart / down).
- A pointer table: "For X, see Y" (Recipes / Concepts / Scenarios / Reference / Migration).
- Non-goals (operators don't open issues asking for k6 parity).
- **Does NOT contain:** per-scenario tables (lives in USAGE.md), the v2 changelog (lives in CHANGES.md), the migration note (lives in USAGE.md's Migrating-from-v1 subsection).

#### `tools/loadgen/USAGE.md` (≤700 lines; spilling per-scenario content to `docs/scenarios/` if tight)

Ordered for a first-time reader (recipes before reference):

1. **Prerequisites** — Docker (v1 ≥ 1.29.2 OR v2 ≥ 2.0), 3 GB min RAM (8 GB for `realistic`+ presets), open ports, host arch caveats.
2. **Concepts** — single page on: open-loop vs closed-loop, coordinated omission, warmup vs measured window, verdict tiers, abort vs liveness watcher, settle phase, frontdoor vs canonical injection. Vocabulary the rest of the doc references.
3. **5-minute tour** — single-screen copy-paste flow, annotated expected output, what TRUSTED means in the next line.
4. **Recipes** — 5–10 worked end-to-end flows ("baseline against new SUT build", "find the saturation knee for history-service", "compare two builds on one machine", "investigate a p99 regression", "run a soak campaign overnight", "set up live dashboards", "triage an UNTRUSTED run"). Recipes-first matches Terraform/kubectl/gh doc shape and is what an operator under time pressure reaches for.
5. **Pick a scenario** — decision tree (regression-testing? → run-trustworthy-baseline. Saturation knee? → run-saturation-sweep. New scenario? → contributor guide).
6. **Scenarios** — encyclopedia: per-scenario page (what it loads, what the SUT must support, how to interpret the summary, common failure modes, recommended starting rates). Ordered by frequency of operator use, not by ship order. Per-scenario sections target ~25 lines; spillover to `docs/scenarios/<name>.md`.
7. **Presets** — table: what each preset is for, how big, how long it takes, how much RAM. Includes `dm-heavy`, `incident-burst`, large-room shapes from §3.
8. **Reading the summary** — line-by-line. RUN QUALITY block (with both empty and populated Issues examples). p99 vs omission p99 worked example. Published-queued vs acked. Saturation events table.
9. **Tuning knobs** — every `--abort-*`, `--ramp-*`, `--auto-warmup-*`, `--settle-*`, `--quality-*`, `--raw-poll-interval`, `--omission-budget-ms` with worked examples.
10. **Live dashboards** — Grafana + Prometheus setup, per-panel guide, alert rules from §1.11, "Ports and what's on each" table.
11. **Per-script reference** — every `scripts/run-*.sh` with a paragraph; generated from `--help` via `make docs-scripts`.
12. **Migrating from v1** — exit-code 4 added (was 0–3), `--csv` semantics change, `--legacy-summary` for one release.
13. **Pitfalls / troubleshooting** — UNTRUSTED verdict actions, sample-cap auto-sizing, settle timeouts, compose v1/v2 detection failures, dashboard port collisions. Every entry's anchor matches the `→ see USAGE.md#anchor` pointers emitted by the harness (§Operator UX — discovery and diagnostics).
14. **Numbers you can defend / numbers you can't** — one page on what comparisons are valid (within machine, within run-id) and what aren't (across hosts, across compose-stack versions).
15. **Glossary** — HdrHistogram, coordinated omission, dispatch deficit, p50/p95/p99, JetStream async ack, OUTBOX/INBOX stream, settle phase, RUN QUALITY tiers, open-loop vs closed-loop, saturation knee, sample cap, drop-oldest, sustain window, frontdoor vs canonical injection. One paragraph each. Cross-linked from every section that uses the term.

#### `tools/loadgen/CHANGES.md` (Keep-a-Changelog 1.1.0 format)

- Per-flag deprecation / replacement table at the top (a `--csv` user can grep their runbook and find the v2 mapping in 10 seconds).
- Sections per release: Added / Changed / Deprecated / Removed / Fixed / Security.
- v2.0 entry includes: exit-code 4 breaking change, RUN QUALITY block at top of summary, `--csv` default change with `--csv-raw` bridge, sample-cap default auto-sizes, new metrics (`loadgen_omission_deficit_seconds`, `loadgen_run_quality`, etc.), removed (none planned).

#### Diagrams (Mermaid, in-tree)

The spec is pure prose; the tech-writing reviewer asked for diagrams where they pay for themselves. Each phase's exit criterion includes shipping the relevant diagram:

- **Phase 0 exit:** component dependency graph showing `Runtime → ScenarioDeps → {publisher, requester, collector, ...}`. Lives in USAGE.md or `docs/architecture.md`.
- **Phase 1 exit:** state diagram for RUN QUALITY verdict transitions (accumulator → terminal node). Sequence diagram for settle-phase timeouts.
- **Phase 3 exit:** component diagram for federation (two-site, OUTBOX→INBOX flow with the four sub-latencies from §3.9).

USAGE.md is part of every phase's exit criterion: a phase is not done until the operator-visible surface it changed is documented. README.md is updated only when the orientation paragraph or quick-start commands change.

### Test strategy

- Phase 1: every new RunQuality rule and every new histogram gets a unit test. HDR migration is a refactor — existing collector tests should pass unchanged after adapter wiring.
- Phase 2: refactor — existing tests are the safety net. Add Runtime + Scenario-registry unit tests.
- Phase 3: each new scenario gets a unit test (generator behavior with mocked publisher/requester) and an integration test (testcontainers, single happy path) per CLAUDE.md §4.

Coverage floor (80%) and target (90% for `pkg/`) per CLAUDE.md §4 remain. Loadgen lives under `tools/`, so it follows the same conventions but is not in `pkg/`.

### Documentation

- README.md gets a `## v2 changes` section with the verdict semantics, new scenarios, and a `## migration` note for operators on a v1 invocation that now sees verdict output.
- Each new scenario lands a section in README's "Scenarios" table.
- Non-goals list shrinks: items moved to "Implemented" or "Deferred — see <follow-up>".

## Open questions

1. **HDR histogram resolution.** Need to pick a sensible `lowestDiscernibleValue` / `highestTrackableValue` / `significantValueDigits`. The methodology reviewer caught the first-draft memory estimate ("~2 KB per cell" with `(1µs, 1min, 3 digits)`) as off by ~3 orders of magnitude — that combination is ~3 MB per cell, not 2 KB, and with ~40 cells (scenarios × kinds × {warmup, measured}) that's 120 MB just for histograms before sub-process merge. The defensible choices for this SUT (p50 1–5 ms, want sub-ms resolution there):
   - `(100µs, 60s, 3 sig digits)` → ~30 KB/cell, ~1.2 MB total. Lower bound loses sub-100µs resolution, which is fine for this SUT.
   - `(1µs, 10s, 2 sig digits)` → ~150 KB/cell, ~6 MB total. Sub-µs floor, but `>10s` clamps (we cap at 10s anyway for liveness).
   - `(1µs, 60s, 3 sig digits)` → ~3 MB/cell, ~120 MB total. Highest fidelity, highest cost.

   **Recommendation: `(100µs, 60s, 3 sig digits)` for per-(scenario, kind, phase) cells; `(10µs, 60s, 3 sig digits)` for the abort-watcher window (tighter floor on the safety-critical path).** Verify on a representative run before locking in.
2. **Drop or keep raw `--csv` samples?** Recommend default off in v2 (HDR-only), `--csv-raw=true` to opt in. Open for operator review.
3. **Phase 3 ordering.** Recommend the order listed (RAW → large-room → presence → notification → thread → edit/delete → subs-churn → auth → federation → chaos). Open to reorder by operator priority.
4. **Federation compose footprint** doubles RAM. Mark as a "two-machine-friendly" scenario in docs, not the default.

## Out of scope (explicitly)

- Replacing NATS / Mongo / Cassandra with mocks. The harness drives the real services.
- Cross-machine numeric comparability. Same rule as v1.
- Becoming a CI gate.
- Generating production-grade traffic. v2 closes the gap with realistic shapes; it does not become a production replay tool.

## Rollout, migration, and rollback

Each phase ships as an independent unit. v1 invocations continue to work through v2.0.x via the `--legacy-summary` flag; v3 removes it.

| Phase | Ships behind | Breaking? | Migration | Rollback |
|-------|--------------|-----------|-----------|----------|
| Phase 0 | nothing — pure refactor | No, behavior-identical | None | Revert PR |
| Phase 1 | `--legacy-summary=true` for v1 output | Yes — new exit code 4, summary block added at top, CSV default changes | CHANGES.md per-flag table; one v2.x release supports both summaries | Set `--legacy-summary=true` per-invocation; revert PR if widespread |
| Phase 2 | nothing | No (internal refactor; tests pin behavior) | None | Revert PR |
| Phase 3 | per-scenario `profiles:` in compose; new scenarios are opt-in via `--scenario=<new>` | No (new surface; existing scenarios unchanged) | Operators add `--profile federation` / `--profile auth` to opt in | Disable the relevant profile; the new scenarios stay un-shipped |

**v2 rollout discipline:**

1. Phase 0 lands first, before any new tests, so the substrate is clean.
2. Phase 1 ships with `--legacy-summary` defaulting to **false** (v2 verdict by default) but operators with v1 tooling pin `LOADGEN_LEGACY_SUMMARY=1` in their runbooks for one v2.x release. v2.0 release notes call out the grace window.
3. v2.1 deprecates `--legacy-summary` with a runtime `slog.Warn`. v3.0 removes it.
4. Phase 2 lands silently — tests are the safety net (the test-strategy addendum below lists which existing tests are expected to need updates).
5. Phase 3 ships per scenario; each new scenario is a separate PR with its own integration test.

## Success criteria

A phase is "done" only when its measurable criteria pass:

| Phase | Criterion |
|-------|-----------|
| Phase 0 | `cloc tools/loadgen/main.go` ≤ 250 lines; `wc -l tools/loadgen/runRun` ≤ 100; existing test suite + binary-exec smoke tests green; zero diff to printed output, flag set, or Prometheus metric names/labels. |
| Phase 1 | Canonical messaging-pipeline run on a known-good build prints `RUN QUALITY: TRUSTED`. An artificially-throttled build prints `DEGRADED`. A stopped-SUT run prints `UNTRUSTED` with exit code 4. Abort-watcher canary suite at three rps points shows ≤10% trip-time drift vs v1. Memory under sustained 5krps × 30min run is bounded (no growth past ~50 MB harness RSS). Every run produces a complete `runs/<run_id>/` bundle. Two operators on one machine can run concurrently without resource collisions. v2 Grafana dashboards render. Alert rules fire and clear correctly in a synthetic test. |
| Phase 2 | Adding a new scenario touches ≤2 files in the loadgen package. `main.go` ≤ 200 lines. No remaining `switch scenarioName` in non-scenario files. |
| Phase 3 | Every scenario has a unit test + an integration test (testcontainers). Every scenario has a `run-<name>.sh`. README "Non-goals" section shrinks; every previously-disclaimed item is implemented or has a follow-up issue. `loadgen scenarios` lists everything. |

## Telemetry plan

New Prometheus metrics shipped by v2 (cardinality bounded):

| Metric | Type | Labels | Purpose | Phase |
|--------|------|--------|---------|-------|
| `loadgen_omission_deficit_seconds` | histogram | `dropped` ({false,true}), `scenario` | Coordinated-omission deficit | 1 |
| `loadgen_run_quality` | gauge | `verdict` ({TRUSTED,DEGRADED,UNTRUSTED}) | Verdict — gauge so it survives the scrape window | 1 |
| `loadgen_settle_probes_total` | counter | `path` ({history,search,getbyid}), `result` ({ok,fail}) | Settle phase outcomes | 1 |
| `loadgen_raw_lag_seconds` | histogram | `path` ({history,search,getbyid}) | RAW timing | 3.1 |
| `loadgen_raw_visibility_window_seconds` | histogram | `path` | RAW poll-interval upper bound | 3.1 |
| `loadgen_largeroom_completion_ratio` | summary | `preset`, `quantile` | Large-room fan-out completion | 3.2 |
| `loadgen_largeroom_receive_latency_seconds` | histogram | `preset` | Per-recipient latency | 3.2 |
| `loadgen_presence_delivered_ratio` | gauge | — | Presence emit/deliver ratio | 3.3 |
| `loadgen_presence_fanout_seconds` | histogram | `preset` | Presence fan-out | 3.3 |
| `loadgen_notification_lag_seconds` | histogram | `channel` ({inapp,push,email}) | Notification fan-out per route | 3.4 |
| `loadgen_first_dm_lag_seconds` | histogram | `stage` ({room,subs,persist,e2e}) | First-DM sub-lags | 3.13 |
| `loadgen_room_open_seconds` | histogram | `leg` ({history,rooms_get,presence,read,restricted}) | Room-open composite | 3.14 |
| `loadgen_room_open_e2e_seconds` | histogram | — | Room-open slowest-leg | 3.14 |
| `loadgen_message_read_seconds` | histogram | `room_type` ({channel,dm}) | Read receipts | 3.16 |
| `loadgen_federation_lag_seconds` | histogram | `stage` ({outbox,inbox,persist,visible}) | Federation sub-lags | 3.9 |
| `loadgen_federation_e2e_lag_seconds` | histogram | — | Federation end-to-end | 3.9 |
| `loadgen_federation_drain_seconds` | histogram | — | Federation flap drain | 3.9 |
| `loadgen_federation_cross_read_seconds` | histogram | — | Cross-site remote-room read | 3.9 |

Existing metrics gain a `phase` label (`{warmup, measured}`) per §1.7. **Existing dashboards must sum across `phase` to preserve their pre-v2 meaning** — covered in CHANGES.md.

## On-call considerations

The DX reviewer flagged that an on-call engineer at 2am seeing `RUN QUALITY: UNTRUSTED` needs an action, not vocabulary. v2 commits to:

- **Runbook stub:** `docs/runbooks/loadgen-untrusted.md` — what UNTRUSTED means, decision tree (drain timeout → check SUT; settle incomplete → wait + retry; warmup error → fix SUT; watcher deafened with cap-blame → raise the cap and re-run). Phase 1 deliverable.
- **Every UNTRUSTED Issues line carries a usage.md#anchor pointer** (§Operator UX — discovery and diagnostics).
- **`--diagnose` flag** auto-re-runs and produces a diff vs original — operator gets a delta in one command (§Operator UX — discovery and diagnostics).
- **Alerts route to the loadgen channel,** not the SUT channel — they describe loadgen health, not SUT health. Operators must understand the distinction.

## API stability

`Scenario`, `Runtime`, `ScenarioDeps` interfaces are `tools/loadgen/`-internal in v2.0. **Not exported for third-party scenarios** — that's a v3 consideration after the registry pattern has worn in. If a third-party scenario is desired before then, the implementer copies a scenario file into the loadgen tree and contributes upstream.

## Test strategy (addendum)

The test-architecture reviewer asked for a concrete plan:

| Phase | Tests requiring update | New tests | Coverage gate |
|-------|------------------------|-----------|---------------|
| Phase 0 | `main_test.go` binary-exec smoke (string anchors may shift); `flags_test.go` (runFlags struct introduces parse path) | `runtime_test.go` (lifecycle), `flags_test.go` expansion | ≥81.4% (no regression) |
| Phase 1 | `collector_test.go` exact-percentile asserts → range asserts; `window_test.go` ditto; binary-exec smoke for new summary block; integration tests assert `summary.json` + `histograms.hlog` written | Settle, RunQuality, omission, run-isolation, artifacts | ≥85% |
| Phase 2 | `readiness_test.go`, `auto_warmup_test.go` migrate from string-switch to registry; integration tests pin scenario-registration | Scenario interface unit tests | ≥85% |
| Phase 3 | Per scenario: scenario unit test + integration test under `//go:build integration`; large-room and federation tests need 2-node testcontainer setups | Per scenario | ≥80% per new file, 90% on shared `pkg/` if any |

Integration test budget: 10 new testcontainer-driven tests added by Phase 3 is a real CI runtime hit. Plan for ~10 min added to CI. If it overshoots, gate the heavier scenarios (federation, large-room) behind `make test-integration-heavy` invoked nightly, not per-PR.

## Review changelog

The first-draft spec (commit b91b85f) was reviewed on 2026-05-12 by seven specialist agents. Material amendments below:

| Source | Severity | Amendment |
|--------|----------|-----------|
| Methodology | Blocker | HDR memory math corrected (~3 OOM); recommended params `(100µs, 60s, 3 digits)` |
| Methodology | Blocker | RAW poll-bias mitigation (visibility-window upper bound + 10ms default + DEGRADED guardrail) |
| Methodology | Blocker | Federation expanded to 4 stage histograms + flap + cross-site-read |
| Methodology | Blocker | RUN QUALITY thresholds tightened (warmup 5/20%; omission auto-scales to 25% of measured p99) |
| Methodology | Important | Dispatch-deficit histogram split on `dropped` label |
| Architecture | Blocker | Added Phase 0 (refactor first) so Phase 1 lands on clean substrate |
| Architecture | Blocker | HDR third-party dep ask added explicitly per CLAUDE.md §5; t-digest fallback documented |
| Architecture | Blocker | Abort-watcher semantic change called out as a known risk with acceptance criterion |
| Architecture | Important | `Scenario` interface split into family (identity + optional probers + required factory) |
| Architecture | Important | Carved out which scenarios extend Runtime (large-room, federation) vs use registry |
| DevOps | Blocker | Dropped Compose spec ≤ 3.8 ceiling; removed top-level `name:`; floor v1≥1.29.2 / v2≥2.0 |
| DevOps | Important | Centralized `lib/compose.sh` (sourced) instead of inlined `dc()` per script |
| DevOps | Important | Healthcheck `start_period` added; nested-invocation env propagation pinned |
| DX | Blocker | TRUSTED/DEGRADED/UNTRUSTED meaning communicated in-flow, not buried |
| DX | Important | Subcommands: `loadgen scenarios|presets|recommend|doctor` |
| DX | Important | Diagnostic scripts: `compare-runs.sh`, `triage.sh`, `bisect.sh`, `preflight.sh`, `new-scenario.sh` |
| DX | Important | Remediation pointers on every UNTRUSTED Issues line; `--diagnose` flag; `--metrics-linger` |
| SRE | Blocker | §1.8 Run artifacts bundle (summary.json, histograms.hlog, timeseries, settle, metrics) |
| SRE | Blocker | §1.9 Run isolation for concurrent operators (per-run-id resource prefixing) |
| SRE | Blocker | §1.11 Alert rules shipped with v2; §1.12 v2 dashboards as Phase 1 exit, not Phase 3 |
| SRE | Important | §1.10 `teardown --force` for orphan recovery; §1.13 credentials story; §1.14 soak script |
| Chat-domain | Blocker | §3.12 DM/channel split via `DMRatio`; `dm-heavy` preset |
| Chat-domain | Blocker | §3.13 First-DM hot path scenario |
| Chat-domain | Blocker | §3.14 Room-open composite (correlated 5-request burst) |
| Chat-domain | Important | §3.15 Bursty publish (incident-burst preset); §3.16 read receipts; §3.2 expanded to 3 large-room shapes; §3.3 typing throttling; §3.4 multi-channel notification |
| Tech-writing | Blocker | Rollout/migration/rollback table; Success criteria per phase; Telemetry plan; On-call section |
| Tech-writing | Blocker | Documentation section contradiction resolved (README ≤80, USAGE per-scenario lives in `docs/scenarios/`, migration in USAGE) |
| Tech-writing | Important | USAGE.md reordered: Recipes before Reference; Concepts page; Glossary last |
| Tech-writing | Important | CHANGES.md formalized as Keep-a-Changelog 1.1.0 with per-flag deprecation table |
| Tech-writing | Important | Diagrams committed as phase-exit deliverables (state, sequence, component graphs) |
