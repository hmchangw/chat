# Loadgen v2 — Trustworthy, Restructured, Realistic

**Date:** 2026-05-12
**Status:** scoping
**Predecessor:** [2026-05-07-history-service-loadtest-design.md](2026-05-07-history-service-loadtest-design.md) shipped the v1 harness (Phases 1–3 of the original spec). This document covers the follow-up work surfaced by the post-ship code review.

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

## Phase 1 — Trustworthiness

Goal: a run that is not trustworthy refuses to print numbers that look trustworthy.

### 1.1 HDR histograms replace per-(scenario, kind) sample slices

Today `Collector.requests` is a sharded map of `[]requestSample`, sorted at finalize for percentile reporting. Memory is unbounded for the duration of a run (acknowledged in README, deferred to "S6/S7"). At 5 k rps × 5 min that's ~50 MB per cell. Long sustained runs OOM the loadgen process before the summary prints.

The same slice also drives small-sample percentile garbage: a 200-sample search-read run reports the 198th sorted value as "p99" with no caveat.

**Design:** swap the per-cell `[]requestSample` for an HDR histogram (`HdrHistogram-go`, the Go port of HdrHistogram). Bounded memory (~2 KB per cell at the resolution we need), accurate percentiles at any sample count, mergeable across processes (which `run-mixed.sh` will exploit).

API change inside `Collector`:

- `RecordRequest(scenario, kind, latency, errored, …)` → write into the histogram instead of appending to a slice.
- `RequestPercentiles(scenario, kind, quantiles []float64) []time.Duration` → query the histogram.
- Keep a separate sample tally for warmup-discard (a single `recordedAt int64` + `inMeasured bool` cheap counter is enough — we do not need to discard per-sample from the histogram; we either start measuring at `warmupDeadline` or keep two histograms `warmup` and `measured` and report only `measured`).

**Compatibility:** the printed report's "request latency" section keeps the same columns (`p50/p95/p99/max/count/errors`). CSV export changes: we no longer have raw per-sample latencies. Either we keep a *capped* raw-sample sidecar (for `--csv` only) or we change `--csv` semantics to "per-bucket histogram counts." Recommend the former — capped at `--csv-max-samples` (default 100 k) with a `csv truncated` warning if exceeded — since the typical `--csv` use case is offline analysis of an explicit smaller run.

The same swap applies to `LatencyWindow` (the abort watcher's ring buffer). HDR's `SubtractWindow` operation lets us implement the rolling sustain check natively without the existing "drop oldest sample" cap-masking failure mode — though we keep the cap as a memory ceiling for the watcher's per-tick query.

### 1.2 Coordinated-omission tracking

Today the latency clock starts inside the dispatched goroutine, after `sem <- struct{}{}` and after `go func(){}` schedules. Under saturation, generator-side queueing latency hides under "SUT smoothness," and dropped ticks (when the pool is full) leave no trace beyond a `saturated` counter increment.

**Design:** capture `intendedAt = time.Now()` at the tick site *before* the `sem` send. Pass it to the dispatched closure. The closure measures `actualStart - intendedAt` as **dispatch deficit** and records it in:

- A new `loadgen_omission_deficit_seconds` histogram (Prometheus + HDR-internal).
- The end-of-run summary as `omission p99: Xms (Y% of measured wall time)`.

For dropped ticks (sem full), record `intendedAt` and the moment the tick was dropped into the same deficit histogram with `dropped=true`. The deficit is then `now - intendedAt`.

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

Rules:

- **UNTRUSTED:** drain timed out **or** measured window < `abort-p99-sustain` **or** warmup error rate > 50%.
- **DEGRADED:** abort watcher deafened **or** omission p99 > 100ms (configurable via `--omission-budget-ms`) **or** ≥1 liveness probe failed but watcher didn't trip.
- **TRUSTED:** otherwise.

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

Phase 1 exit: every run prints a verdict, every untrustworthy run is marked, no run silently reports clean numbers it cannot defend.

## Phase 2 — Restructure

Goal: adding a new scenario touches ≤2 files. `main.go` < 200 lines. `runRun` < 100 lines.

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

Today the string `"history-read" | "search-read" | "room-rpc" | "messaging-pipeline"` switches in six files. Adding a fifth scenario is a 6-file diff. Replace with:

```go
type Scenario interface {
    Name() string
    DefaultPreset() string
    NeedsReadiness() bool
    BuildReadinessProbe(deps ScenarioDeps) func(context.Context) error
    NeedsAutoWarmup(*Preset) bool
    BuildLivenessProbe(deps ScenarioDeps) func(context.Context) error
    NewGenerator(deps ScenarioDeps, rf runFlags) (Runner, error)
}

var scenarios = map[string]Scenario{}
func RegisterScenario(s Scenario) { scenarios[s.Name()] = s }
```

Each scenario lives in its own file (`scenario_messaging.go`, `scenario_history.go`, `scenario_search.go`, `scenario_room.go`, plus new ones from Phase 3). Each file registers in `init()`. `main.go` looks up the scenario by name from `*scenario` flag.

`ScenarioDeps` is a struct of injected dependencies (nc, js, pool, collector, metrics, fixtures, publisher, requester) so scenarios don't reach into `Runtime` internals.

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

Backpressure: bounded concurrent in-flight RAW probes via the existing sem pattern.

Metric: `loadgen_raw_lag_seconds{path="history|search|getbyid"}` histogram. Summary table: median, p95, p99, max, count, timeouts.

Preset: shares `messaging-pipeline` fixtures plus a new `RAWConfig` block (poll interval, timeout, max in-flight).

### 3.2 Large-room fan-out

**Scenario name:** `large-room-broadcast`. New file `scenario_largeroom.go`. New preset `large-room` with one announce-style room containing 10 000 members (and 100 background rooms of normal size to keep the harness exercising realistic-ish member-list pressure).

Mechanism: 1 publish per second into the announce room. Subscribe to all 10 000 member inboxes (via a small connection pool with wildcard subjects). Measure publish → per-member-delivery latency distributions.

Output: per-message broadcast fan-out time histogram (p50/p99/max + completion-count distribution: "by p99 N% of members had received it").

Memory note: 10 000 inbox subscriptions on a single connection saturates NATS subscription tables; reuse the existing `ConnPool` with `--connections=N` to spread.

### 3.3 Presence / typing churn

**Scenario name:** `presence-typing`. New file `scenario_presence.go`.

Mechanism: per-user typing-indicator emit on a configurable cadence (e.g. one event per simulated keystroke at 5–15 events/second/user over a small subset of users-currently-typing). Subjects per `pkg/subject` (find whichever presence/typing subjects exist or stub them in if the SUT doesn't yet — this scenario may surface SUT-side missing implementation, which is signal not bug).

Metric: emit rate vs delivery rate, observer-side fan-out latency.

Open question: if the SUT doesn't currently implement a presence/typing subject family, this scenario is gated on that work. Mark as **DEFERRED-UNTIL-SUT-READY** in the plan and add a `docs/` note.

### 3.4 Notification fan-out

**Scenario name:** `notification-fanout`. New file `scenario_notif.go`.

Mechanism: publish messages with mentions at a configurable rate (extending the existing `MentionRate` preset field — already plumbed for `pipeline` but not measured as a separate latency path). Subscribe to the notification-worker's output (per `pkg/subject` notification subjects). Measure publish → notification-emit lag.

Metric: `loadgen_notification_lag_seconds` histogram, per-(scenario, mention_count_bucket).

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

Mechanism: bring up *two* sites in compose (site-a, site-b) with OUTBOX/INBOX wiring. Publish a message on site-a, measure time until it appears in site-b's history.

Stack: doubles the compose footprint. Add `docker-compose.federation.yml` overlay and a `--federation` flag on the run-* scripts.

### 3.10 WebSocket gateway

**Status:** DEFERRED if no WS gateway exists in the SUT yet. If/when one ships, scenario `ws-fanout` measures WS-connect storm + per-connection broadcast-delivery latency.

### 3.11 Chaos / failure injection

**Scenario:** not a new generator — an overlay. New file `tools/loadgen/deploy/docker-compose.chaos.yml` adding `pumba` or `toxiproxy` between services. Add `scripts/run-chaos.sh` driving a baseline `messaging-pipeline` with periodic network partitions / packet loss / slow links applied to a chosen service. Measure SUT graceful-degradation behavior.

Phase 3 exit: README's "Non-goals" section is updated. Every disclaimed item is either implemented or has a follow-up issue link.

## Cross-phase concerns

### Backwards compatibility

- Scripts in `scripts/` keep their names and env-var contracts. Internals can change.
- Printed-summary table layout: existing columns stay, new lines are *additions* (RUN QUALITY block at the top, published_acked beside published_queued, omission block in a new section). Operator tooling that greps `^P99` / `^MissingBroadcasts` keeps working.
- CSV column set: unchanged for existing columns; if HDR sidecar drops raw samples by default, `--csv-raw=true` opts back into the old behavior.

### Docker Compose v1 and v2 compatibility

The harness must run unmodified on **both** `docker-compose` (v1, the Python tool, hyphenated CLI) and `docker compose` (v2, the Go plugin, space-delimited CLI). Many operator environments still ship v1 (some Ubuntu LTS, some RHEL with the legacy package), and some only have v2.

**Design:**

- `tools/loadgen/scripts/*.sh` must auto-detect: define a `dc()` shell function near the top of each script that tries `docker compose version` first, falls back to `docker-compose --version`, then sets `DC=...` accordingly. All `docker-compose` / `docker compose` invocations in scripts go through `dc`.
- The `Makefile` under `tools/loadgen/deploy/` does the same. Add a `DC ?= $(shell ./scripts/detect-compose.sh)` line that runs a tiny helper script.
- The `docker-compose.loadtest.yml` file uses only directives supported by BOTH v1 (Compose spec ≤ 3.8) and v2. Forbid: top-level `name:` (v2-only), `profiles:` (works in both ≥ recent versions, OK), `develop:` block (v2-only), `extends:` outside the spec.
- A new `scripts/detect-compose.sh` helper exits 0 with the correct CLI on stdout, 1 with a clear error if neither is installed. It is sourced by every other script.
- README documents that BOTH v1 and v2 work. The "Known issues" section calls out the few directives that differ (we avoid them; if a contributor adds one, CI catches it).
- CI: add a single `make -C tools/loadgen/deploy validate-compose` target that runs `docker compose -f docker-compose.loadtest.yml config -q` AND (if `docker-compose` v1 is present) `docker-compose -f docker-compose.loadtest.yml config -q`. Fails the build if either rejects the file.

### Operator UX — scripts and presets

`tools/loadgen/scripts/` ships 13 helper scripts today. v2 keeps every existing script working (per the backwards-compatibility rule) and adds one per new scenario. Audit and update them as part of each phase:

- **Phase 1 deliverable:** every existing script gains a `--quality-budget` env var (default sane) and the run output is filtered through a `summary-with-verdict` wrapper that surfaces the new RUN QUALITY block. Scripts gain `set -euo pipefail` if any are missing it, and a `--help` flag printing usage.
- **Phase 2 deliverable:** scripts switch to the `dc()` shell function so they work under both compose v1 and v2. No interface changes for operators.
- **Phase 3 deliverable:** one new `scripts/run-<scenario>.sh` for each new scenario from §3.1–§3.11. Naming pattern matches existing: `run-raw.sh`, `run-largeroom.sh`, `run-presence.sh`, `run-notif.sh`, `run-mutate.sh`, `run-subschurn.sh`, `run-auth.sh`, `run-federation.sh`, `run-chaos.sh`. Each follows the existing template (env-var knobs with defaults, sensible 30–120s duration, prints final summary + verdict).

Two new umbrella scripts:

- `scripts/run-trustworthy-baseline.sh` — runs the canonical "is this build sane?" set: messaging-pipeline + history-read + search-read + room-rpc at conservative rates, asserting RUN QUALITY=TRUSTED for each. Single-command full sanity check. Used by operators after a deploy.
- `scripts/run-saturation-sweep.sh` — runs ramp scenarios for each read service to find each one's saturation knee, prints a per-service table at the end.

### Documentation — `USAGE.md`

The current `tools/loadgen/README.md` (233 lines) bundles operator usage, scenario reference, internal architecture, troubleshooting, and non-goals. It's grown too dense for a first-time reader.

**Design:** split into two documents in the same folder.

- `tools/loadgen/README.md` — short orientation (≤80 lines). What is loadgen, when to use it, where to find more, link to USAGE.md and to the design spec. Quick-start (5 commands). Single "Choose your scenario" table.
- `tools/loadgen/USAGE.md` — comprehensive operator guide (the new doc, target ≤500 lines). Covers:
  - **Prerequisites**: Docker (v1 OR v2), disk, RAM (3 GB minimum, 8 GB for `realistic`+ presets), open ports, host architecture caveats.
  - **First run**: copy-paste flow from clone to a TRUSTED messaging-pipeline run. Verbatim commands, expected output.
  - **The 4 + N scenarios**: per-scenario page with: what it loads, what the SUT must support, how to interpret the summary, common failure modes, recommended starting rates.
  - **Presets** table: what each preset is for, how big, how long it takes, how much RAM.
  - **Reading the summary**: the new RUN QUALITY block, every section line-by-line, what each column means, what TRUSTED / DEGRADED / UNTRUSTED imply for operator action.
  - **Tuning knobs**: every `--abort-*`, `--ramp-*`, `--auto-warmup-*`, `--settle-*`, `--quality-*` flag with worked examples.
  - **Live dashboards**: Grafana + Prometheus setup, the per-panel guide, what to alert on.
  - **Common pitfalls**: a rewrite of the existing "Troubleshooting" section, plus new entries for v2 (`UNTRUSTED` verdict, sample-cap auto-sizing, settle timeouts, Docker compose v1/v2 detection failures).
  - **Per-script reference**: every `scripts/run-*.sh` with a paragraph on what it does, what env vars it accepts, expected runtime, exit codes.
  - **Recipes**: 5–10 worked end-to-end recipes for the most common operator workflows ("baseline against new SUT build," "find the saturation knee for history-service," "compare two builds across one machine," "investigate a p99 regression," etc.).
- `tools/loadgen/CHANGES.md` — v2 changelog with migration notes for v1 invocations.

USAGE.md is part of every phase's exit criterion: a phase is not done until the operator-visible surface it changed is documented.

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

1. **HDR histogram resolution.** Need to pick a sensible `lowestDiscernibleValue` / `highestTrackableValue` / `significantValueDigits`. Default to `(1µs, 1min, 3 digits)` matching wrk2's choice and verify on a representative run.
2. **Drop or keep raw `--csv` samples?** Recommend default off in v2 (HDR-only), `--csv-raw=true` to opt in. Open for operator review.
3. **Phase 3 ordering.** Recommend the order listed (RAW → large-room → presence → notification → thread → edit/delete → subs-churn → auth → federation → chaos). Open to reorder by operator priority.
4. **Federation compose footprint** doubles RAM. Mark as a "two-machine-friendly" scenario in docs, not the default.

## Out of scope (explicitly)

- Replacing NATS / Mongo / Cassandra with mocks. The harness drives the real services.
- Cross-machine numeric comparability. Same rule as v1.
- Becoming a CI gate.
- Generating production-grade traffic. v2 closes the gap with realistic shapes; it does not become a production replay tool.
