# Changelog

All notable changes to loadgen are documented here. Format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/).

---

## Flag changes (v1 → v2)

| Old (v1) | New (v2) | Behavior change |
|----------|----------|-----------------|
| `--abort-window-max-samples=10000` (fixed) | `--abort-window-max-samples` with auto-sizing recommendation in `loadgen recommend` | Default no longer deafens watcher; operators should size to `2 × peak_rps × max_sustain` |
| *(none)* | `--settle-timeout`, `--settle-interval`, `--settle-probes` | New "settle" phase between auto-warmup and measured |
| *(none)* | `--allow-concurrent`, `--run-ttl` | Run isolation |
| *(none)* | `--raw-poll-interval`, `--raw-timeout` | RAW timing scenario |
| *(none)* | `--receipt-coverage` | Read-receipts scenario |
| *(none)* | `--mutate-rate`, `--edit-age-distribution` | Message-mutate scenario |
| *(none)* | `--churn-rate` | Subscription-churn |
| *(none)* | `--first-dm-recycle` | First-DM scenario |
| *(none)* | `--federation-flap`, `--federation-cross-read`, `--flap-period`, `--flap-down`, `--federation-secondary-nats-url` | Federation-lag |
| *(none)* | `--auth-storm-period` | Auth-load reconnect storm |

---

## Dashboard-breaking changes

**`phase` label added to `loadgen_publish_errors_total` and `loadgen_request_errors_total`** (Phase 1a §1.7).

Existing dashboards that sum these counters must now aggregate across `phase=` to preserve their pre-v2 meaning. PromQL example:

```promql
# Before (v1):
sum(rate(loadgen_publish_errors_total[1m])) by (preset, reason)

# After (v2) — works unchanged; sums implicitly across phase=:
sum(rate(loadgen_publish_errors_total[1m])) by (preset, reason)

# To filter to just measured-phase errors:
sum(rate(loadgen_publish_errors_total{phase="measured"}[1m])) by (preset, reason)
```

`loadgen_published_total` and `loadgen_requests_total` already carried `phase` in v1; no migration needed for those.

---

## [v2.0.0] - Unreleased

### Added

- **Trust core**: RUN QUALITY verdict (TRUSTED / DEGRADED / UNTRUSTED) printed at the top of every summary. Exit code 4 for UNTRUSTED runs.
- HDR histograms replace per-sample slices throughout (Collector, LatencyWindow, OmissionTracker) for bounded memory under sustained load.
- Coordinated-omission deficit tracking (`loadgen_omission_deficit_seconds{dropped}`).
- Settle phase between auto-warmup and measured-window phases.
- Run artifact bundles under `runs/<run_id>/` with full per-run data (summary, histograms, settle outcome, flags, env, logs, metrics, timeseries).
- Run isolation via run-id-prefixed Mongo / JetStream consumer resources + the `loadgen_runs` lock collection.
- `teardown --force` for orphan-resource recovery.
- 5 Prometheus alert rules + `promtool test rules` synthetic test suite.
- 4 v2 Grafana dashboards (overview, RAW, federation, system) + 3 exporters (NATS, Cassandra, cAdvisor) under the `dashboards` Compose profile.
- Per-run JWT + NKey provisioning with a 26-pattern redaction allow-list for env.txt.
- `scripts/run-soak.sh` and `scripts/run-campaign.sh` for long-running test regimes.
- **Phase 2** Scenario interface + registry: adding a new scenario takes ≤2 files. 14 scenarios registered in the default build.
- **Phase 3 scenarios**: raw-consistency, large-room-broadcast, notification-fanout, message-mutate, subscription-churn, auth-load, federation-lag, first-dm, room-open, read-receipts. Plus 2 build-tagged skeletons (presence-typing, push/email notification routing) deferred until SUT exposes the needed subjects.
- **Chaos overlay** via toxiproxy + `loadgen chaos add|remove|list` subcommand + `scripts/run-chaos.sh`.
- **Discoverability**: `loadgen scenarios|presets|recommend|doctor` subcommands.
- **Diagnostic scripts**: `compare-runs.sh`, `triage.sh`, `bisect.sh`, `preflight.sh`, `new-scenario.sh`.
- **Compose v1+v2 compatibility** via `scripts/lib/compose.sh` (auto-detects `docker compose` vs `docker-compose`).

### Changed

- Existing counters now carry a `phase={warmup,measured}` label. Dashboards must sum across `phase=` to preserve pre-v2 semantics (see "Dashboard-breaking changes" above).
- Summary block layout adds a RUN QUALITY header, omission lines, and a queued/acked breakdown.
- Docker Compose top-level `name:` directive removed from `docker-compose.loadtest.yml` (v1 doesn't support it). Project naming now comes from `COMPOSE_PROJECT_NAME=loadgen` exported by `scripts/lib/compose.sh`.
- `Collector.RecordRequest` signature: added `phase` parameter, removed `publishedAt time.Time` parameter (phase label supersedes timestamp-based filtering).
- `executeRun` return type: `int` → `(int, Summary)` so artifact bundle writer can persist the full summary.

### Deprecated

None.

### Removed

None (v1's flag set is preserved; v2 only adds).

### Fixed

- 6 critical bugs from the post-v1 review (read-scenario ramps, actualRate accounting, auto-warmup inject mismatch, Zipf+ThreadRate realism, run-mixed.sh metrics-port collision, abort-window default cap).
- LatencyWindow: cap-sizing matched to production's `resolveAbortWindowMaxSamples` formula (`2 × rps × sustainS`) — the prior fixed `10000` cap silently deafened the watcher at common rates.
- OmissionTracker: data race on `*hdr.Histogram` fixed by adding `sync.Mutex` (Task 1a.4). The HDR library is not goroutine-safe.
- DMRatio runtime wiring: prior commit had the helper but never wired into `publishOne`, so DMRatio affected seed-time room counts but not the actual runtime publish distribution (Task 3.12 BLOCKING fix).
- LoadgenAsyncAckBacklog alert: prior expression was logically inverted (fired on healthy throughput); corrected to measure rate of `async_ack` errors directly (Task 1b.4 fix).
- LoadgenOmissionBudgetExceeded alert: prior expression had disjoint label sets between numerator and denominator → division always empty. Fixed with `sum(...) by (le)` aggregation (Task 1b.4 fix).
- Run-isolation lock: prior implementation stored the `loadgen_runs` collection in the per-run Mongo DB, so concurrent runs in different per-run DBs never saw each other's lock rows. Fixed by introducing `SharedLockDBName = "loadgen_shared"` (Task 1b.2 critical fix).

### Security

- Per-fixture-user JWTs and federation peer NKeys live in `runs/<run_id>/creds/` (mode 0600, gitignored).
- 26-pattern redaction allow-list applied to `env.txt` before bundling (9 exact + 11 suffix + 6 prefix patterns covering AUTH_TOKEN, MONGO_URI, NATS_URL, *_TOKEN, *_KEY, *_SECRET, etc.).
