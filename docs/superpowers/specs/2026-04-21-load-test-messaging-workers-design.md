# Messaging Workers Load Test Harness — Design

## Purpose

A capacity-baseline load test for the single-site messaging pipeline
(`message-gatekeeper` → `MESSAGES_CANONICAL` → `message-worker` +
`broadcast-worker`).

The harness answers one question: **how many messages per second can one
site sustain, and at what latency?** It produces a repeatable terminal
summary, an optional CSV dump, and an opt-in Grafana dashboard.

## Scope

### In scope

- A Go-based CLI load generator at `tools/loadgen/` (flat service, standard
  file layout per the repo's conventions).
- A docker-compose harness at `tools/loadgen/deploy/docker-compose.loadtest.yml`
  bringing up one NATS (JetStream), one MongoDB, one Cassandra, one
  `message-gatekeeper`, one `message-worker`, one `broadcast-worker`, and
  the loadgen container.
- Programmatic seeding of users, rooms, and subscriptions into MongoDB
  based on a named preset + RNG seed.
- Open-loop rate generation with named presets: `small`, `medium`,
  `large`, `realistic`.
- Front-door injection (via `chat.user.{account}.room.{roomID}.{siteID}.msg.send`)
  by default, with a flag to inject directly at `MESSAGES_CANONICAL` for
  isolating downstream-worker capacity.
- End-of-run terminal summary and optional CSV export.
- Optional Prometheus + Grafana compose profile with a pre-baked
  dashboard JSON.

### Out of scope (v1)

- Multi-site / supercluster topology. The harness stays single-site;
  topology is left pluggable for later.
- Per-user NATS credentials. The loadgen authenticates with the shared
  `backend.creds` from `docker-local/` and impersonates users via subject
  tokens.
- Persistence-read latency measurement from Cassandra. Replaced by
  JetStream consumer-lag sampling (see measurement section).
- CI regression gating / pass-fail thresholds. The baseline run returns a
  summary; CI gating is a later phase.
- Soak / long-duration stability runs. Different use case; different
  tool settings; revisit later.

## Topology

Single-site stack, defined in `tools/loadgen/deploy/docker-compose.loadtest.yml`:

```
loadgen ──▶ nats (JetStream) ──▶ message-gatekeeper ──▶ MESSAGES_CANONICAL ──┬──▶ message-worker ──▶ cassandra
                │                       │                                    └──▶ broadcast-worker ──▶ mongodb
                │                       └──▶ mongodb (subscriptions lookup)
                └──◀─ reply subject (chat.user.*.response.>)
                └──◀─ broadcast subject (chat.room.*.event)
                └──◀─ consumer info (JetStream API)

            optional profile "dashboards":
                prometheus ──▶ grafana (pre-baked dashboard JSON)
```

- One NATS server with JetStream enabled, client port `4222`,
  monitoring `8222`.
- One MongoDB, one Cassandra. Site scoping is handled by the `SITE_ID`
  environment variable shared by all services in the stack
  (`site-local`).
- One instance each of `message-gatekeeper`, `message-worker`,
  `broadcast-worker`, all built from their existing `deploy/Dockerfile`
  images with build context at the repo root.
- The `loadgen` container joins the same compose network and reaches
  services by name (`nats`, `mongodb`, `cassandra`). Its host-side
  port `9099` is exposed for Prometheus scraping.
- The `dashboards` profile adds `prometheus` and `grafana` containers
  with file-provisioned scrape config and dashboard JSON.

## File layout

Following the repo's flat-service convention. All loadgen code lives in
`tools/loadgen/`:

```
tools/loadgen/
├── README.md
├── main.go                      # config parsing, wiring, subcommand dispatch
├── seed.go                      # programmatic seeding of users/rooms/subs
├── preset.go                    # preset definitions + RNG-based workload spec
├── generator.go                 # open-loop publisher, rate-limited
├── collector.go                 # reply + broadcast subscribers, latency samples
├── consumerlag.go               # polls JetStream ConsumerInfo every 1s
├── report.go                    # terminal summary, CSV export, Prometheus gauges
├── preset_test.go
├── generator_test.go
├── collector_test.go
├── report_test.go
├── integration_test.go          # //go:build integration
└── deploy/
    ├── Dockerfile
    ├── Makefile                 # scoped make targets
    ├── docker-compose.loadtest.yml
    ├── grafana/
    │   ├── dashboards/loadtest.json
    │   └── provisioning/
    │       ├── dashboards/loadtest.yaml
    │       └── datasources/prometheus.yaml
    └── prometheus/
        └── prometheus.yml
```

The loadgen has no dedicated `Store` interface — seeding writes directly
through `mongoutil.Connect` and the raw collection API. This keeps the
component focused and avoids mock generation for code that exists only
to populate fixtures.

## CLI surface

The loadgen is one binary with three subcommands:

```
loadgen seed     --preset=<name> [--seed=<int>]
loadgen run      --preset=<name> [--seed=<int>] [--duration=60s] [--rate=500]
                 [--warmup=10s] [--inject=frontdoor|canonical] [--csv=path]
loadgen teardown
```

- `seed` is idempotent. It drops and recreates the `users`, `rooms`,
  and `subscriptions` collections for the given preset, deterministically
  populated from `(preset name, seed)`. Default seed is `42`.
- `run` assumes `seed` has been applied. It opens NATS and MongoDB
  connections, subscribes to reply and broadcast subjects, starts a
  publisher at the configured rate for `duration`, and prints a summary
  at the end. `--warmup` discards samples from the first N seconds to
  avoid cold-start skew. `--inject=canonical` bypasses the gatekeeper
  and publishes `model.MessageEvent` directly on
  `chat.msg.canonical.{siteID}.created`, for isolating downstream-worker
  capacity.
- `teardown` drops the three seeded collections so a different preset
  can be seeded cleanly without lingering state.

### Environment config

All values are parsed via `caarlos0/env` into a typed `config` struct in
`main.go`. Flags take precedence for run-specific knobs; everything else
is env.

| Env Var            | Default      | Description                                         |
|--------------------|--------------|-----------------------------------------------------|
| `NATS_URL`         | *required*   | NATS server URL                                     |
| `NATS_CREDS_FILE`  | *empty*      | Shared backend creds; empty disables auth           |
| `SITE_ID`          | `site-local` | Must match gatekeeper / worker `SITE_ID`            |
| `MONGO_URI`        | *required*   | MongoDB URI                                         |
| `MONGO_DB`         | `chat`       | MongoDB database name                               |
| `METRICS_ADDR`     | `:9099`      | Prometheus `/metrics` listen address                |

### Preset structure

Presets are declared as a `map[string]Preset` in `preset.go`. Adding a
new preset is one map entry; no CLI plumbing changes.

```go
type Preset struct {
    Name         string
    Users        int
    Rooms        int
    RoomSizeDist Distribution   // uniform | mixed
    SenderDist   Distribution   // uniform | zipf
    ContentBytes Range          // min/max content size
    MentionRate  float64        // 0.0 for uniform presets, 0.10 for realistic
    ThreadRate   float64        // 0.0 for uniform presets, 0.05 for realistic
}
```

Built-in presets:

| preset      | users | rooms | room sizes   | sender dist | content bytes | mentions | threads |
|-------------|-------|-------|--------------|-------------|---------------|----------|---------|
| `small`     | 10    | 5     | uniform      | uniform     | 200           | 0%       | 0%      |
| `medium`    | 1 000 | 100   | uniform      | uniform     | 200           | 0%       | 0%      |
| `large`     | 10 000| 1 000 | uniform      | uniform     | 200           | 0%       | 0%      |
| `realistic` | 1 000 | 100   | mixed        | Zipf(s=1.1) | 50–2000       | 10%      | 5%      |

Every run prints the preset name and RNG seed in the summary, making
results reproducible on any machine.

### Makefile targets

Scoped under `tools/loadgen/deploy/Makefile`. The root Makefile is
untouched, per the precedent set by the broadcast-worker test harness.

```make
COMPOSE ?= docker compose -f docker-compose.loadtest.yml

up:
	$(COMPOSE) up -d --build

seed:
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen seed --preset=$(PRESET)

run:
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen run \
	    --preset=$(PRESET) \
	    --rate=$(or $(RATE),500) \
	    --duration=$(or $(DURATION),60s)

run-dashboards:
	$(COMPOSE) --profile dashboards up -d
	$(MAKE) run PRESET=$(PRESET) RATE=$(RATE) DURATION=$(DURATION)

down:
	$(COMPOSE) --profile dashboards down -v
```

## Seeding

`loadgen seed` is responsible for producing a deterministic fixture
from `(preset name, seed)` and writing it to MongoDB. The algorithm:

1. Open a MongoDB connection via `mongoutil.Connect`.
2. Drop `users`, `rooms`, and `subscriptions` collections (idempotent
   reset so reruns are clean).
3. Seed a `math/rand.New(rand.NewSource(seed))` generator.
4. Generate `preset.Users` user documents. Each user has a stable ID
   (`u-<zero-padded-index>`) and account name (`user-<index>`). English
   and Chinese display names are drawn from a small fixed list cycled
   by index so enrichment paths in `broadcast-worker` exercise populated
   values.
5. Generate `preset.Rooms` room documents. Room IDs are
   `room-<zero-padded-index>`. Room type is `group` for uniform
   presets; `realistic` mixes `group` and `dm` with a 9:1 ratio.
6. For each room, assign members according to the preset's
   `RoomSizeDist`:
   - **uniform**: each room has `ceil(Users / Rooms)` distinct members
     drawn round-robin from the user pool (every user ends up in at
     least one room; some users are in more).
   - **mixed**: a small fraction of rooms (10%) get up to 500 members
     sampled without replacement; the remainder get 2–20 members. DM
     rooms always have exactly 2 members.
7. Write `Subscription` documents for each `(user, room)` membership,
   with `siteId = SITE_ID`.
8. Create indexes that match the worker services' expectations
   (`subscriptions.roomId`, `subscriptions.u.account`).

Seed data is never large enough to need bulk-write batching beyond
MongoDB's default batch size; `InsertMany` is used directly. At the
`large` preset (10k users, ~100k subscriptions) this completes in a
few seconds on a developer laptop.

Because generation is a pure function of `(preset, seed)`, running
`loadgen seed --preset=large --seed=42` twice produces byte-identical
data. The same `(preset, seed)` passed to `loadgen run` produces the
same stream of publishes.

## Generator and measurement

### Open-loop publishing

A single goroutine owns a `time.Ticker` at `1s / rate`. On each tick
it selects a `(user, room)` pair according to the preset's
distributions (deterministic from the same RNG seed used in `seed`)
and publishes a `model.SendMessageRequest` with:

- `ID`: a freshly allocated UUID, used as the JetStream message-ID for
  deduplication and as the `Message.ID` after gatekeeper validation.
- `RequestID`: a freshly allocated UUID, used to correlate the
  gatekeeper reply back to the originating publish.
- `Content`: a random-length string drawn from `preset.ContentBytes`.
  Content is a benign filler — no PII, no tokens. For `realistic`,
  a mention token (`@user-<index>`) is prefixed with probability
  `MentionRate`; thread-reply fields reference a prior message with
  probability `ThreadRate`.

The publish subject is built via `pkg/subject` helpers (never hand
`fmt.Sprintf`) and, by default, is
`chat.user.{account}.room.{roomID}.{siteID}.msg.send`. With
`--inject=canonical`, the generator instead publishes a pre-built
`model.MessageEvent` on `chat.msg.canonical.{siteID}.created` — this
bypasses the gatekeeper entirely and is used to isolate downstream
worker capacity.

Publishing is non-blocking. If the pipeline slows, messages accumulate
in JetStream and the consumer-lag signal grows — which is exactly the
backpressure signal a capacity baseline wants to reveal.

The rate limiter is `time.Ticker`. `golang.org/x/time/rate.Limiter`
would also work, but a ticker is sufficient for a fixed target rate
and keeps the dependency footprint minimal.

### Metrics measured

| ID  | Name                   | How it's measured                                                                                                 |
|-----|------------------------|-------------------------------------------------------------------------------------------------------------------|
| E1  | Gatekeeper ack latency | Publish time → gatekeeper reply on `chat.user.{account}.response.{requestID}`. Correlated by `requestID`.          |
| E2  | Broadcast visibility   | Publish time → appearance of matching `RoomEvent` on `chat.room.{roomID}.event`. Correlated by `message.id`.        |
| E4  | Consumer backlog       | Polled via `js.Consumer(stream, durable).Info(ctx)` every 1s for both `message-worker` and `broadcast-worker`.      |

E3 (persistence-read latency from Cassandra) is deliberately not
measured. The E4 consumer-backlog curves give the relevant answer —
"is the message-worker keeping up with canonical publishes?" — without
requiring a Cassandra probe.

### Reply correlation

Before the generator begins publishing, two wildcard subscriptions are
opened:

- `chat.user.*.response.>` for gatekeeper replies (E1).
- `chat.room.*.event` for broadcast events (E2).

Every outbound publish records the publish timestamp in **two separate**
`sync.Map`s:

- `pendingByRequestID[requestID] = publishNanos` — consumed by E1.
- `pendingByMessageID[messageID] = publishNanos` — consumed by E2.

Keeping E1 and E2 bookkeeping independent means recording an E1 sample
does not affect E2 correlation (and vice versa), and each map can be
scanned at end-of-run to count its own "missing" class.

When a reply arrives on the response subject, the collector parses
`requestID` from the last subject token, looks it up in
`pendingByRequestID`, appends `now - publishNanos` to the E1 sample
buffer, and deletes the entry. When a `RoomEvent` arrives on the
broadcast subject, the collector extracts `message.id`, looks it up
in `pendingByMessageID`, appends the delta to the E2 sample buffer,
and deletes the entry.

At end-of-run, any remaining entries in `pendingByRequestID` are
counted as "missing replies"; any remaining in `pendingByMessageID`
are counted as "missing broadcasts". Neither contributes to percentiles.

### Consumer-lag sampling

A dedicated goroutine polls both durable consumers on
`MESSAGES_CANONICAL_{SITE_ID}` every 1 second using
`js.Consumer(ctx, stream, durable).Info(ctx)`. Fields recorded per
sample:

- `num_pending` — messages in the stream that haven't been delivered.
- `num_ack_pending` — messages delivered but not yet acked.
- `num_redelivered` — accumulator of retry deliveries; delta per
  sample is logged.
- `num_waiting` — pull requests in flight (worker health).

Samples are appended to per-durable time-series buffers and exported
live as Prometheus gauges. The terminal summary reports min, peak,
and final values.

Little's Law gives a rough latency estimate if needed:
`avg_wait ≈ num_pending / actual_throughput`. This is not reported by
default — the headline metrics are already E1 and E2 — but the raw
data supports it.

### Sample storage

Latency samples are `int64` nanosecond deltas appended to per-metric
slices guarded by a mutex. A 60-second run at 1000 msg/s produces
120k samples (E1 + E2 combined) consuming about 1 MB — trivial. At
end of run, the collector sorts each slice and computes P50, P95, P99,
and max.

Should we ever need multi-hour runs, HDR histogram
(`github.com/HdrHistogram/hdrhistogram-go`) would replace the slice.
v1 does not add that dependency.

### Warmup

The first `--warmup` seconds (default 10s) of publishing and sampling
happens normally but the samples collected during that window are
discarded at the warmup boundary. This prevents first-connection,
JIT, and cache-cold effects from skewing the headline percentiles.

### Error accounting

Each of these is counted separately and surfaced explicitly in the
summary; a run is never silently "successful" if any occurred:

- Publish failures (JetStream `PublishAsync` returned an error).
- Gatekeeper error replies (reply payload has a non-empty `error` field).
- Missing replies (requestID never received a reply by end of run).
- Missing broadcasts (message.id never received a broadcast by end of run).
- Reply-subject JSON parse failures (malformed reply payload).

## Reporting

### Terminal summary

Printed to stdout at end of run via `text/tabwriter`. Always produced,
regardless of whether Prometheus/Grafana are running. Structured so a
human can eyeball it and a grep-based tool can parse it.

```
=== loadgen run complete ===
preset: medium    seed: 42    site: site-local
duration: 60s (warmup: 10s, measured: 50s)    inject: frontdoor
target rate: 500 msg/s    actual rate: 499.8 msg/s

publish results
  sent:             25000
  publish errors:     0
  gatekeeper errors:  0
  missing replies:    0
  missing broadcasts: 0

latency (measured window only)
  metric            count   p50     p95      p99     max
  E1 gatekeeper     25000   2.1ms   6.3ms   11.4ms   24ms
  E2 broadcast      25000   8.7ms   24.1ms  41.0ms   88ms

consumer lag (MESSAGES_CANONICAL_site-local)
  durable             min_pending   peak_pending   final_pending   peak_ack_pending   redelivered
  message-worker           0             42              0                 18                0
  broadcast-worker         0             57              0                 22                0
```

The capacity signal is `final_pending == 0` with `peak_pending`
bounded: the system drained its queue within the run, so it is
sustaining the target rate. `final_pending` climbing is the signal
for "over capacity".

### CSV export

Opt-in with `--csv=path`. One file, one row per sample:

```
timestamp_ns,request_id,metric,latency_ns
1713600000000000000,9f…,E1,2100000
1713600000000000000,9f…,E2,8700000
…
```

Intended for ad-hoc analysis in a notebook or spreadsheet. Not
produced unless the flag is set.

### Prometheus metrics

Always exposed on `METRICS_ADDR` (default `:9099`), using
`prometheus/client_golang` (already an approved repo dependency).

| Metric                              | Type      | Labels              |
|-------------------------------------|-----------|---------------------|
| `loadgen_published_total`           | counter   | `preset`            |
| `loadgen_publish_errors_total`      | counter   | `preset`, `reason`  |
| `loadgen_e1_latency_seconds`        | histogram | `preset`            |
| `loadgen_e2_latency_seconds`        | histogram | `preset`            |
| `loadgen_consumer_pending`          | gauge     | `stream`, `durable` |
| `loadgen_consumer_ack_pending`      | gauge     | `stream`, `durable` |
| `loadgen_consumer_redelivered`      | gauge     | `stream`, `durable` |

### Grafana dashboard (opt-in)

Activated with `docker compose --profile dashboards up` (or
`make run-dashboards`). Prometheus is provisioned to scrape:

- The loadgen's `/metrics` endpoint.
- The NATS server's monitoring endpoint (`/varz` and `/jsz`) via the
  community `prometheus-nats-exporter`, or directly via NATS's own
  Prometheus output if configured.

A pre-baked dashboard JSON at
`tools/loadgen/deploy/grafana/dashboards/loadtest.json` is
provisioned via Grafana's file provisioner and includes these panels:

1. **Throughput** — `rate(loadgen_published_total[10s])` vs target rate.
2. **E1 gatekeeper ack latency** — P50/P95/P99 histogram quantiles over time.
3. **E2 broadcast latency** — P50/P95/P99 histogram quantiles over time.
4. **Consumer pending** — `loadgen_consumer_pending` stacked by durable.
5. **Ack pending** — `loadgen_consumer_ack_pending` by durable.
6. **Error rate** — `rate(loadgen_publish_errors_total[10s])` by reason.
7. **NATS health** — connections, slow consumers, JetStream bytes.

The default compose stack (without the profile) does not bring up
Prometheus or Grafana, keeping the fast path lightweight.

### Exit code

- `0` — run completed and error counts were within tolerance
  (hardcoded 0.1% of `sent` for v1).
- `1` — startup failure, publish-error rate exceeded tolerance, or
  missing-reply rate exceeded tolerance.

This establishes a foundation for CI gating later without committing
to it in v1.

## Testing

### Unit tests

Standard in-package tests, `package main`, following the repo's
conventions (`stretchr/testify` assertions, `go.uber.org/mock` where
mocks are useful, table-driven where applicable).

- `preset_test.go` — same `(preset, seed)` produces the same users,
  rooms, and subscriptions byte-for-byte; same `(preset, seed)`
  produces the same `(user, room, content)` publish sequence. Table-
  driven across all four presets.
- `generator_test.go` — rate pacing (given rate R and duration D,
  exactly R·D messages are produced ±1); user/room selection honors
  the preset's distributions; injects a stub publish function that
  records calls (per the repo's "inject publish function as a field"
  rule for testability).
- `collector_test.go` — reply correlation: given a set of fake publish
  records and a stream of synthesized replies, samples land in the
  right metric buffer; missing replies are counted; unknown
  `requestID`s are ignored.
- `report_test.go` — percentile math over fixed sample sets; CSV
  export format; exit-code logic at the error-tolerance boundary
  (just below, at, and just above).

All unit tests run via `make test SERVICE=tools/loadgen` with the
race detector enabled (handled by the root Makefile).

### Integration test

`integration_test.go` with build tag `//go:build integration`. Uses
`testcontainers-go` to bring up NATS, MongoDB, Cassandra,
`message-gatekeeper`, `message-worker`, and `broadcast-worker`
containers. The test then runs
`loadgen seed --preset=small` and
`loadgen run --preset=small --duration=10s --rate=50` and asserts:

- Exit code is `0`.
- E1 sample count equals published count (no missing replies).
- E2 sample count equals published count (no missing broadcasts).
- Final `num_pending` on both durable consumers is `0`.
- `rooms.lastMsgId` in MongoDB for a sampled room matches the last
  published message's ID.

The test verifies end-to-end wiring — it does not assert on
performance numbers, which depend on the test host and are not the
point of a CI-runnable test.

### Coverage target

≥80% per the project rule (`CLAUDE.md`), with `generator.go`,
`collector.go`, and `preset.go` aiming for 90%+ as core logic.

## Error handling

All errors follow the repo's rules (`CLAUDE.md`):

- Errors wrapped with context: `fmt.Errorf("seed users: %w", err)`.
  Never bare `err`, never `fmt.Errorf("error: %w", err)`.
- NATS connect / MongoDB connect failures at startup log and
  `os.Exit(1)` — the same pattern the workers use.
- Publish errors during a run are counted and logged at DEBUG; the
  run continues so the overall shape of the failure is visible.
- Reply-subject JSON parse failures are counted under
  `reason="bad_reply"` and the offending sample is discarded.
- Graceful shutdown on `SIGTERM` / `SIGINT` via `pkg/shutdown.Wait`:
  stop the publish ticker, drain in-flight publishes with a 5-second
  bound, unsubscribe from reply and broadcast subjects, `nc.Drain()`,
  disconnect MongoDB, then print a partial summary before exit.

## Logging

`log/slog` with the JSON handler. Lifecycle events at INFO (startup,
seed complete, run started, run complete). Per-error detail at DEBUG
(publish errors, bad replies). Never log message content
(`CLAUDE.md`: "never log tokens, passwords, or full message bodies").

## Documentation

- `tools/loadgen/README.md` — reference for the operator: what the
  tool is, how to run each preset, how to read the terminal summary,
  how to turn on the Grafana dashboard, what each metric means,
  example output. Not a tutorial.
- This design document at
  `docs/superpowers/specs/2026-04-21-load-test-messaging-workers-design.md`.

The `README.md` explicitly documents what the harness does **not** do,
so future contributors don't silently retrofit responsibilities onto
it:

- Does not run in CI by default.
- Does not test auth / NATS callout capacity.
- Does not test cross-site behavior or the OUTBOX / INBOX path.
- Does not assert on absolute performance numbers — those are
  host-dependent; the pass signal is `final_pending == 0` with error
  counts at zero.

## Dependencies

No new third-party Go dependencies are added for v1. Everything needed
is already present in `go.mod`:

- `github.com/nats-io/nats.go` and `.../jetstream` — publish, subscribe,
  consumer info.
- `go.mongodb.org/mongo-driver/v2` — seeding (via `pkg/mongoutil`).
- `github.com/caarlos0/env/v11` — config parsing.
- `github.com/google/uuid` — request/message IDs.
- `github.com/prometheus/client_golang` — metrics endpoint.
- `github.com/stretchr/testify` — test assertions.
- `go.uber.org/mock` — where mocks are useful (unlikely in loadgen,
  but available).
- `github.com/testcontainers/testcontainers-go` — integration test.

Shared packages consumed from the repo:

- `pkg/model` — typed NATS payloads (`SendMessageRequest`,
  `MessageEvent`, `RoomEvent`).
- `pkg/subject` — subject builders (never hand-construct subject
  strings).
- `pkg/stream` — stream/consumer config helpers.
- `pkg/natsutil` — NATS connection helper.
- `pkg/mongoutil` — MongoDB connection helper.
- `pkg/shutdown` — graceful shutdown orchestration.

## Future work (explicitly deferred)

- Multi-site / supercluster topology to measure gateway cost.
- Per-user NATS creds to measure auth-callout capacity.
- HDR histogram sample storage for multi-hour soak runs.
- k6-based harness variant if HTML reports or CI threshold gating
  become a priority.
- CI integration with a baseline-comparison workflow.
- Realistic workload extensions (message edits, deletes, reactions
  once those features land).
