# loadgen

Capacity-baseline load generator for the chat platform. Single Go binary
with three subcommands, four scenarios (`messaging-pipeline`,
`history-read`, `search-read`, `room-rpc`), and a Prometheus + Grafana
reporting story.

## Quick start (fresh clone)

```
./tools/loadgen/scripts/up.sh           # build + bring up the full stack
./tools/loadgen/scripts/quickstart.sh   # 30s sanity smoke (small preset)
./tools/loadgen/scripts/run-realistic.sh # 2-min realistic-preset run
./tools/loadgen/scripts/down.sh          # tear down + drop volumes
```

For the full read-scenario sweep (Phase 1+2 — messaging + history +
search + room):

```
make -C tools/loadgen/deploy run-natsrouter
```

For live dashboards:

```
make -C tools/loadgen/deploy run-dashboards PRESET=medium
# Grafana at http://localhost:3000 (anonymous admin)
```

## Scenarios

| `--scenario`         | What it drives                           | Default preset    |
|----------------------|------------------------------------------|-------------------|
| `messaging-pipeline` | gatekeeper → canonical → workers (E2E)   | `medium`          |
| `history-read`       | history-service RPCs (Cassandra reads)   | `history-read`    |
| `search-read`        | search-service RPCs (Elasticsearch + Valkey) | `search-read` |
| `room-rpc`           | room-service RPCs (rooms.list/get/member.*) | `room-rpc`     |

## Presets

| preset         | users  | rooms | notes                                                  |
|----------------|--------|-------|--------------------------------------------------------|
| `small`        | 10     | 5     | uniform, 200-byte content                              |
| `medium`       | 1 000  | 100   | uniform, 200-byte content                              |
| `large`        | 10 000 | 1 000 | uniform, 200-byte content                              |
| `realistic`    | 1 000  | 100   | Zipf senders, mixed sizes, 50–2000 bytes, mentions     |
| `history-read` | 10     | 5     | LoadHistory 60% / GetMessageByID 20% / Surrounding 10% / Thread 10% |
| `search-read`  | 10     | 5     | SearchMessages 50% / SearchRooms 50%; 10-token query bag |
| `room-rpc`     | 1 000  | 100   | RoomsList 60% / RoomsGet 20% / MemberList 10% / Create 8% / MemberAdd 2% |

## Subcommands

- `loadgen seed --preset=<name> [--seed=42]` — idempotently populate
  MongoDB with deterministic fixtures.
- `loadgen run --preset=<name> [flags]` — open-loop publish/request at
  `--rate` for `--duration`, print a summary at the end.
- `loadgen teardown` — drop the seeded collections.

### Common `run` flags

| Flag                       | Default              | What it does                                              |
|----------------------------|----------------------|-----------------------------------------------------------|
| `--scenario=<name>`        | `messaging-pipeline` | Which scenario to run.                                    |
| `--preset=<name>`          | (required)           | Which preset to load.                                     |
| `--rate=N`                 | 500                  | Target requests / publishes per second.                   |
| `--duration=<dur>`         | `60s`                | Total run length.                                         |
| `--warmup=<dur>`           | `10s`                | Pre-measurement window (samples discarded).               |
| `--inject=frontdoor\|canonical` | `frontdoor`     | Where messaging-pipeline injects.                         |
| `--csv=<path>`             | empty                | Per-sample CSV export.                                    |

### Phase 3 harness upgrades

| Flag                              | Default | What it does                                                       |
|-----------------------------------|---------|--------------------------------------------------------------------|
| `--auto-warmup`                   | `true`  | For `history-read` with ID-needing kinds, run a brief messaging-pipeline phase first to populate the message-ID pool. |
| `--auto-warmup-rate=N`            | 200     | Publish rate during the auto-warmup phase.                         |
| `--progress-interval=<dur>`       | `10s`   | Live progress log line cadence; `0` disables.                      |
| `--skip-readiness`                | `false` | Skip the pre-run readiness probe.                                  |
| `--readiness-timeout=<dur>`       | `30s`   | Deadline for the readiness probe.                                  |
| `--ramp-from=N`, `--ramp-to=N`    | 0       | Ramp the rate from N rps to M rps. **Mutually exclusive with `--rate`** — pass `--rate=0` together with `--ramp-from=N --ramp-to=M --ramp-duration=DUR` to ramp. |
| `--ramp-duration=<dur>`           | 0       | Time to climb across the ramp.                                     |
| `--ramp-shape=linear\|exponential`| `linear`| Ramp curve.                                                        |
| `--abort-on-p99-ms=N`             | 0       | Stop the run if the p99 of the abort window stays above N ms for `--abort-p99-sustain`. Exit code 2. |
| `--abort-p99-sustain=<dur>`       | `30s`   | Sustain window for the latency abort.                              |
| `--abort-on-error-pct=F`          | 0       | Stop if error rate stays above F (0..1) for `--abort-error-sustain`. |
| `--abort-error-sustain=<dur>`     | `10s`   | Sustain window for the error-rate abort.                           |
| `--abort-window-max-samples=N`    | 10000   | Cap on the abort/progress latency ring buffer (S3); drop-oldest when full. `0` disables the cap (legacy unbounded behavior). Bounds the per-tick percentile sort: at the default 10k, each sort is ~10k log 10k ≈ 130k comparisons (~150µs). **Coverage trade-off:** when `peak_rps × max(--abort-p99-sustain, --abort-error-sustain) > cap`, retention is compressed below the sustain interval, so the abort watcher's `covered` check returns false and it cannot fire even under a real sustained breach. The watcher emits `slog.Warn` `"abort watcher deafened by sample cap"` so a silent no-fire is detectable; raise the cap or set to `0` to keep the watcher functional. Rule of thumb: `cap ≥ peak_rps × max_sustain`. |
| `--js-async-max-pending=N`        | 4096    | Canonical-inject only: in-flight cap for async JetStream publishes (S5). `0` falls back to sync `js.PublishMsg` (legacy / bisection). Failed acks land in `loadgen_publish_errors_total{reason="async_ack"}` and the orphan messageIDs are evicted from the broadcast correlation map so MissingBroadcasts isn't inflated. Rule of thumb: `2 × peak-rps × expected-ack-latency-seconds`; the default 4096 covers ~5k rps × 400ms or ~50k rps × 40ms. Lower it (e.g. 256) to detect upstream wedging earlier; raise only if `loadgen_publish_errors_total{reason="async_ack"}` climbs due to MaxPending stalls rather than real stream failures. |

## Reading the summary

- **messaging-pipeline:** `final_pending == 0` on every durable + zero errors → pipeline sustaining target rate. `final_pending` climbing or errors > 0 → over capacity or upstream regression.
- **history-read / search-read / room-rpc:** the "request latency" section shows per-(scenario, kind) p50/p95/p99/max + count + errors. p99 climbing while count drops → service-side saturation.
- **Async JetStream publishes (S5, `--inject canonical` only):** when `--js-async-max-pending>0`, `loadgen_published_total` counts publishes *queued* into the JetStream async ring, not acked. The ack happens off-loop; failures land in `loadgen_publish_errors_total{reason="async_ack"}`. To get the acked count, subtract `async_ack` errors from `Published`. The orphan messageID is evicted from the broadcast correlation map, so `MissingBroadcasts` is NOT inflated by async-ack failures (R1 BLOCKER #5/#8 fix). If the shutdown drain logs `"jetstream async publish drain timed out"`, the printed report's `Published` count is a lower-bound — late acks landing after the drain timeout still bump `loadgen_publish_errors_total{reason="async_ack"}` on the metrics endpoint, but the printed summary won't reflect them.
- **Exit codes** (precedence: liveness > saturation > clean-fail > clean-pass):
    - `0` clean pass
    - `1` errors above tolerance
    - `2` aborted by the saturation watcher (SUT got slow — `--abort-on-p99-ms` / `--abort-on-error-pct`)
    - `3` aborted by the liveness watcher (SUT became unreachable — `--liveness-interval`)

### Saturation metric layout

Loadgen-side saturation (when `MaxInFlight` is reached) is recorded into
two different counters by intentional scenario asymmetry:

- **messaging-pipeline:** `loadgen_publish_errors_total{reason="saturated"}` — saturation is a publish-side event.
- **history-read / search-read / room-rpc:** `loadgen_request_errors_total{scenario, kind="*", reason="saturated"}` — saturation is a request-side event.

The Grafana dashboard's "Saturation events/sec (all scenarios)" panel sums
both so operators see the loadgen-side pressure in one place regardless
of scenario. Alerts that need to fire on saturation should query the same
sum:

```promql
sum(rate(loadgen_publish_errors_total{reason="saturated"}[1m]))
+ sum(rate(loadgen_request_errors_total{reason="saturated"}[1m]))
```

## Workflow notes

- `history-read` and `search-read` need data in their backing stores. The
  default `--auto-warmup=true` runs a brief messaging-pipeline phase
  before each `history-read` run to populate Cassandra (and indirectly,
  via search-sync-worker, Elasticsearch). For longer / more realistic
  data shapes, run `messaging-pipeline` separately for a few minutes
  first, then run the read scenarios with `--auto-warmup=false`.
- `room-rpc` mutates Mongo state (10% of the mix is room/member
  creates). Run against the loadgen-owned `MONGO_DB=loadgen` and call
  `make teardown` between runs to keep the rooms collection bounded.
- **Mongo isolation guard:** `loadgen seed` and `loadgen teardown`
  refuse to operate unless `MONGO_DB` carries the `loadgen` prefix.
  Protects against the footgun of accidentally seeding into a
  production-shaped database. For one-off recovery / migration
  workflows where you genuinely need to touch a non-`loadgen` DB,
  `loadgen seed --i-know-what-i-am-doing` bypasses the check.
  `teardown` has no such bypass — drop a non-loadgen DB directly
  via `mongosh` if you really need to.
- **Per-user NATS credentials (preview):** `--nats-creds-dir=DIR`
  rotates `*.creds` files from `DIR` across the data connections in
  the pool, so each data connection dials with a different user
  credential. This is the harness-side plumbing for the eventual
  "per-fixture-user JWT/NKey" auth path. Full SUT-side validation
  requires `auth-service` in the compose stack, which today is
  deferred — until then, the rotation runs but the SUT doesn't
  validate against auth-service. Set `NATS_CREDS_FILE` (single
  shared creds, today's default) OR `--nats-creds-dir`, not both.
- **Mid-run liveness probe:** the harness probes the SUT every
  `--liveness-interval` (default 30s). After
  `--liveness-failures` (default 3) consecutive failures, the run
  aborts with exit code 3 ("SUT unreachable"), distinct from exit
  code 2 ("SUT slow / saturation watcher fired"). Counted by the
  `loadgen_liveness_probes_total{result=ok|fail}` metric.
- The compose stack is substantial (NATS + Mongo + Cassandra +
  Elasticsearch + Valkey + 7 services). Plan for ~3GB of memory on
  Cassandra + Elasticsearch alone.
- **Phase S scope notes.** S1 was originally framed as "streaming
  aggregates" (per-bucket HDR-histograms replacing the per-sample
  slices) but shipped as **per-key mutex sharding** — a 1-day change
  that preserves exact-percentile reporting contracts instead of a
  3-day rewrite. The full streaming-aggregate path is deferred to a
  follow-up (S6/S7). Memory under sustained load is still bounded
  by `--abort-window-max-samples` (which caps the watcher's slice)
  but the Collector's per-(scenario,kind) request slices remain
  unbounded for the duration of a single run. See commit `e7b7fe4`.

## Troubleshooting

First-run gotchas observed when standing up the loadtest compose stack
on a fresh machine. Listed in rough order of likelihood.

- **`apk add --no-cache ca-certificates` fails during `docker compose
  build` with `certificate verify failed`.** Hits restricted-network
  sandboxes where the Alpine CDN's TLS chain isn't validated by the
  bare alpine image. Workaround: pre-build an `alpine-precerts:3.21`
  base image with the host CA bundle baked in (`COPY
  /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/`), then sed the
  service Dockerfiles to use it. Long-term: switch the runtime stage
  to `gcr.io/distroless/static-debian12` which ships with ca-certs
  preinstalled. Tracked as a follow-up.

- **`cassandra-init` exits with code 1 on first `up`.** Cassandra
  4.1 occasionally races its own healthcheck on cold start — the
  `nodetool status` check passes but the CQL listener isn't yet
  accepting connections. The compose retry budget was bumped to
  60 × 10s but you may still hit it on slow hosts. Recovery:
  `docker compose up cassandra-init` once Cassandra is steadily
  healthy.

- **`search-sync-worker` error-loops with `INBOX_site-local stream
  not found`.** The INBOX stream is owned by `inbox-worker` per
  CLAUDE.md §6 ("Stream bootstrap ownership"). The loadtest compose
  stack now includes `inbox-worker` to satisfy this dependency even
  for single-site runs.

- **`--ramp-from`/`--ramp-to` rejected with "`--rate` and
  `--ramp-from/--ramp-to` cannot both be set".** `--rate` defaults
  to 500. To use a ramp, pass `--rate=0 --ramp-from=N --ramp-to=M
  --ramp-duration=DUR` together.

- **`curl localhost:9099/metrics` returns empty / connection
  refused.** The metrics HTTP server is started and stopped per
  `loadgen run` invocation — it does NOT outlive the run. To capture
  metrics during a run, bring up the `dashboards` profile so
  Prometheus scrapes live: `make run-dashboards PRESET=...`.

- **High `redelivered` count on `message-worker` in the consumer-lag
  table.** This is a SUT signal (the worker can't ack at the offered
  rate), not a loadgen bug. The S3 sample cap diagnostic (`abort
  watcher deafened by sample cap`) is separate; check whether either
  fires when interpreting a run.

## Non-goals

- Not a CI regression gate. Invoked manually.
- Not an auth benchmark. Connects unauthenticated.
- Not a cross-site benchmark — single-site only. (`inbox-worker`
  federation deferred to a follow-up spec.)
- Not a `auth-service` HTTP benchmark — deferred.
- Not an absolute-number tool. Numbers vary by host — compare within
  one machine across changes, don't compare across machines.
