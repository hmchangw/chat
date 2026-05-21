# Changelog

All notable changes to loadgen are documented here. Format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/).

---

## Upgrade: notification-fanout real implementation (in-app channel)

The `notification-fanout` scenario, previously a SKELETON whose tick loop
recorded nothing, is now real for the in-app channel:

- The scenario subscribes to `subject.Notification(account)` for every
  unique fixture account via the long-lived `Subscribers()` registry.
- Per tick it publishes a `model.SendMessageRequest` through the standard
  frontdoor (`subject.MsgSend(account, roomID, siteID)`), with a mention
  injected at the configured `Preset.MentionRate`.
- Each notification received from the SUT (one per recipient per published
  message) observes lag into
  `loadgen_notification_lag_seconds{channel="inapp"}` — histogram
  cardinality is N × tick-count, not just tick-count.

The push/email sub-scenario (§3.4b) remains gated behind the
`notif_routing_ready` build tag — those subjects do not yet exist in
`pkg/subject`. No new operator flags or seed steps are required;
`loadgen seed --preset=<any messages preset>` already provisions the Mongo
subscriptions notification-worker needs to derive recipients.

See `docs/scenarios/notification-fanout.md` for the full subject contract
and algorithm.

---

## Post-v2 merge with origin/main (broadcast encryption + compose refactor)

A large merge from `origin/main` introduced cross-cutting changes loadgen had
to adapt to. None changes loadgen's flag surface, but every operator-facing
script and compose file is affected.

### Broadcast-worker encryption is now the default

`broadcast-worker` ships with `ENCRYPTION_ENABLED=true` by default. Loadgen
must provision per-room keypairs into Valkey so the worker can decrypt the
canonical events it fans out — otherwise every event errors and broadcasts
appear silently missing in the verdict.

- `seed` and `teardown` connect to Valkey via `connectKeyStore` and persist
  per-room keypairs through `pkg/roomkeystore`.
- `VALKEY_ADDR` is read from the environment. It is NOT marked `,required`
  so chaos / discoverability subcommands (which do not touch the keystore)
  still parse the config; the seed and teardown handlers emit an actionable
  "set VALKEY_ADDR" error and exit 2 if the address is missing.
- New exported helpers in `pkg/`-facing surface: `Fixtures.RoomKeys`,
  `SeedRoomKeys`, `TeardownRoomKeys` (see `seed.go`).
- The Makefile `run` target now depends on `seed` so a fresh stack never
  hits the "no Valkey keys present → every event errors" failure mode on
  the default `messaging-pipeline` scenario.

### Compose layout refactor: `docker-compose.loadtest.yml` is gone

The single self-contained `docker-compose.loadtest.yml` was deleted in
favour of a thin overlay (`tools/loadgen/deploy/docker-compose.yml`) that
rides ON TOP of the shared `docker-local` stack:

- `make -C ../../.. deps-up` brings up third-party deps
  (NATS / Mongo / Cassandra / Valkey / Elasticsearch / Keycloak).
- `docker-local/compose.services.yaml` brings up the microservices.
- `tools/loadgen/deploy/docker-compose.yml` adds the load-test-specific
  containers (loadgen, prometheus, grafana) on the external `chat-local`
  network exposed by docker-local.

The Makefile `up` target orchestrates all three. Every script under
`tools/loadgen/scripts/` was updated to point at the new compose file
(or to delegate to `make up` / `make down`). The chaos / federation /
auth overlay scripts now compose with `-f docker-compose.yml
-f docker-compose.{chaos,federation,auth}.yml`. The chaos / federation
/ auth overlay files were re-homed onto the shared `chat-local` network
(previously declared a dangling `loadtest` network that never existed).

### Seed-time user-room ACL bootstrap for search-sync-lag

The user-room ACL bootstrap (publishing one synthetic `OutboxMemberAdded`
per unique `(account, roomID)` fixture tuple onto the local INBOX subject,
then waiting ~35 s for the ES refresh) can now be performed at seed time
instead of on every `loadgen run --scenario=search-sync-lag`. New flags on
the `seed` subcommand:

- `--with-search-sync-acl` (default `false`) — opt-in: after the Mongo
  seed completes, connect to NATS, publish the ACL events, and wait for
  the ES refresh. OFF by default so the common messages-workload seed
  stays Mongo-only and doesn't require NATS connectivity.
- `--search-sync-acl-wait` (default `35s`) — post-publish wait duration;
  same default and semantics as the Run-time flag.

Recommended workflow:

```bash
loadgen seed --preset=search-read --with-search-sync-acl              # pay the 35s wait once
loadgen run --scenario=search-sync-lag --inject=canonical \
            --preset=search-read --search-sync-skip-acl-bootstrap     # subsequent runs start immediately
```

The Run-time bootstrap path remains as a fallback (default behavior of
`loadgen run` is unchanged) so existing operator workflows keep working.
Re-seeding is idempotent — search-sync-worker's painless LWW treats
redundant writes with equal timestamps as no-ops. The seed-time helper
`SeedSearchSyncACL` produces wire output identical (modulo `Timestamp`)
to the Run-time `bootstrapSearchSyncACL`.

### Search-sync scenario field rename

The search-sync-lag scenario's field/flag surface was renamed during merge
resolution. See `scenario_searchsync.go` for the current names; operator
scripts and presets reference the new names directly.

### Misc

- `MONGO_DB` default in the loadgen container's compose env is now
  `loadgen` (the prefix `guardMongoDB` requires), not `chat` (which the
  guard refuses for `seed` and `teardown` and which would silently write
  into the production DB for `run`).
- `cadvisor` is restored to the `dashboards` profile in
  `tools/loadgen/deploy/docker-compose.yml` and `prometheus.yml` scrapes
  `cadvisor:8080` again. Container-level CPU/memory panels in the
  `loadgen-system` dashboard now have data again. The cohort B
  `/var/run:ro` mount fix from the previous `docker-compose.loadtest.yml`
  is preserved in the new overlay definition.
- `newE2Handler` filters incoming events by `X-Loadgen-Run-ID` so
  concurrent runs against the same NATS no longer credit each other's
  broadcasts. Test coverage added for the runID-filter branch.

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
- **`auth-load` scenario (fully implemented)**: HTTP-benchmarks `auth-service` in two modes. Normal mode alternates `POST /auth` + `GET /healthz` at `--rate` rps, observed into `loadgen_requests_total{kind=login|validate}` + `loadgen_request_latency_seconds`. Reconnect-storm mode (`--preset=auth-reconnect-storm`) dials `AuthIdleConnections=1000` NATS conns, drops them, re-dials, and observes time-to-recovery into `loadgen_auth_reconnect_seconds` + `loadgen_auth_reconnects_completed_total`. First storm fires at T+30s; `--auth-storm-period > 0` makes the loop periodic. New flag: `--auth-url` (with `AUTH_SERVICE_URL` env fallback). Requires `DEV_MODE=true` on auth-service.
- **`first-dm` scenario real wire-up**: previously a SKELETON whose `sendFirstDM` returned an empty map and produced no histogram observations. Now records three sub-stage lags into `loadgen_first_dm_lag_seconds{stage}` per user pair: `room` (publish → `chat.room.canonical.{siteID}.create` observed), `subs` (publish → `chat.user.*.event.subscription.update` observed for **both** userA and userB), `e2e` (publish → `chat.user.{userB}.event.room` broadcast observed). Installs three wildcard observer subscriptions on the run-shared `Subscribers` registry at Run start, then per tick: pulls the next pair from `firstDMPool`, issues a real RoomCreate request via `Requester.Request(chat.user.{userA}.request.room.{siteID}.create, ...)`, publishes a synthetic DM canonical `MessageEvent`, and polls a bounded FIFO tracker (cap 16384 entries) for the three stages with a 10s per-iteration timeout. Subs stage uses on-wire `SubscriptionUpdate` events instead of a Mongo poll — room-worker publishes them from `finishCreateRoom` after the subscription doc lands, so the event is a sufficient proxy. (An earlier shape exposed a fourth `stage=persist` measurement keyed off `chat.msg.canonical.{siteID}.created`, but message-worker consumes that subject silently with no persist-complete event — the measurement was the loadgen→broker→loadgen self-loop, not Cassandra persistence, so it was removed.) Pool exhaustion in non-recycle mode exits cleanly with a `first-dm pool exhausted; exiting cleanly` log line; `--first-dm-recycle` wraps but logs a warning that recycled pairs hit the existing-DM branch so `stage=room` will be artificially short. Also tightened `augmentWithFirstDMFixtures` to populate `EngName`, `ChineseName`, and `SiteID` on the seeded `loadgen-firstdm-` users so room-service's `errInvalidUserData` validation passes.
- **`search-sync-lag` scenario**: measures canonical→ES index visibility lag end-to-end via `search.messages`. Requires `--inject=canonical`. Bootstraps the per-user user-room ACL doc by publishing synthesized `OutboxMemberAdded` events onto the local INBOX subject, then waits `--search-sync-acl-wait` (default 35s, covers ES refresh_interval + bulk-flush slack) before the publish/poll loop. New flags: `--search-sync-poll-interval` (default 250ms), `--search-sync-timeout` (default 90s), `--search-sync-acl-wait` (default 35s). New metrics: `loadgen_search_index_lag_seconds` (unlabeled histogram, buckets clustered around the 30s ES refresh_interval SLO) and `loadgen_search_index_visible_total{outcome="visible|timeout|transport_error|publish_error|dropped_inflight"}` for triaging zero-observation runs.
- **Chaos overlay** via toxiproxy + `loadgen chaos add|remove|list` subcommand + `scripts/run-chaos.sh`.
- **Discoverability**: `loadgen scenarios|presets|recommend|doctor` subcommands.
- **Diagnostic scripts**: `compare-runs.sh`, `triage.sh`, `bisect.sh`, `preflight.sh`, `new-scenario.sh`.
- **Compose v1+v2 compatibility** via `scripts/lib/compose.sh` (auto-detects `docker compose` vs `docker-compose`).

### Changed

- Existing counters now carry a `phase={warmup,measured}` label. Dashboards must sum across `phase=` to preserve pre-v2 semantics (see "Dashboard-breaking changes" above).
- Summary block layout adds a RUN QUALITY header, omission lines, and a queued/acked breakdown.
- Docker Compose top-level `name:` directive: project naming comes from `COMPOSE_PROJECT_NAME=loadgen` exported by `scripts/lib/compose.sh` for v1 callers; the post-merge `docker-compose.yml` overlay does declare `name: loadgen` for v2 callers. (Originally introduced when the `name:` directive was removed from the now-deleted `docker-compose.loadtest.yml` for v1 compatibility — see "Post-v2 merge with origin/main" section above for the compose refactor that obsoleted that file.)
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

---

## Known follow-ups for v2.x

Tracked here so future operators / contributors know what's deliberately
deferred rather than missed:

- **Build-tag-gated scenarios** (`presence-typing`, `notif_routing` for
  push/email). Both wait for the SUT to expose subjects that don't exist
  yet (`subject.PresenceWildcard`, `subject.NotificationPushPattern`).
  When the SUT adds those builders, removing the build tag and finishing
  the scenarios is the work.
- **ACL doc auto-detection in `search-sync-lag` Run**. Currently the
  Run-time bootstrap publishes its events regardless of whether the seed
  step already populated the ACL doc; operators are expected to pass
  `--search-sync-skip-acl-bootstrap` after a `--with-search-sync-acl`
  seed. A probe against `search.messages` at Run start could
  auto-detect the doc and skip the redundant publishes. Marginal value
  vs. operator awareness; not implemented.
- **Painless-guard wording in commit `a7e74d2`**. The commit message
  said equal-timestamp writes are a no-op; the actual guard is strictly
  `>`, so equal-timestamp writes ARE applied (idempotent in practice
  because the doc content is identical). Wording is slightly wrong;
  history not rewritten to avoid force-push churn.
- **bootstrap_error observability**. On bootstrap failure the scenario
  now holds the process open for 20s (one Prometheus scrape cycle) so
  the metric is observable before exit. A more robust pattern (write
  the failure to `runs/<run_id>/` for offline triage) is a follow-up.
