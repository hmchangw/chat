# Runbook: search-sync-lag dashboard is silent / zero observations

When a `--scenario=search-sync-lag` run produces an empty
`loadgen_search_index_lag_seconds` histogram, the
`loadgen_search_index_visible_total{outcome=…}` counter is the triage
anchor — it has one bucket per terminal outcome and tells you whether the
problem is the scenario, the publish path, the SUT, or pure capacity.

For background on what the scenario does and why it has an ACL warm-up
step, see the [scenario doc](../scenarios/search-sync-lag.md).

## Quick triage

1. Locate the run's artifact bundle:
   ```
   ls runs/<run_id>/
   ```
2. Inspect the outcome counter:
   ```bash
   cat runs/<run_id>/metrics.txt \
     | grep loadgen_search_index_visible_total
   ```
   (Or query Prometheus directly:
   `sum by (outcome) (loadgen_search_index_visible_total)`.)
3. Match the dominant outcome to the section below.

## Triage tree

### 1. No metric at all (everything is zero)

**Symptom.** `loadgen_search_index_visible_total` has no series; the
histogram has no observations; the scenario produced no signal.

**Likely causes** (ordered by frequency):

1. The scenario didn't run — `--scenario` flag was wrong or omitted.
2. The ACL bootstrap failed at `Run` start and the scenario exited before
   any tick fired. The bootstrap is the **only** code path that can return
   an error from `Run` before metrics are recorded — see
   [`scenario_searchsync.go:168-170`](../../scenario_searchsync.go).
3. The run was shorter than `--search-sync-acl-wait` (default `35s`) and
   the operator cancelled before the publish/poll loop started.

**Confirm.**

```bash
# Verify the scenario was selected
grep -- '--scenario' runs/<run_id>/run.log

# Verify the bootstrap completed (or failed)
grep -E 'bootstrap search-sync ACL|search-sync-lag requires' runs/<run_id>/run.log

# Verify total run duration vs the ACL wait
grep -E 'duration|started|exited' runs/<run_id>/run.log
```

**Resolution.**

- If `--scenario=search-sync-lag` wasn't passed, re-run with it. The
  scenario does not run by default in the messaging-pipeline preset.
- If the bootstrap failed, look at the error message. The most common
  bootstrap failures are NATS connection (creds/URL) or
  `--inject=frontdoor` (see section 2 below — the bootstrap also publishes
  via the loadgen publisher).
- If the run exited too early, set `--duration` ≥ `--search-sync-acl-wait
  + 2 minutes` minimum.

### 1b. `bootstrap_error` dominant

**Symptom.**

```
loadgen_search_index_visible_total{outcome="bootstrap_error"} 1
loadgen_search_index_visible_total{outcome="visible"} 0
loadgen_search_index_visible_total{outcome="timeout"} 0
```

`bootstrap_error` is the only outcome present, and it appears exactly
**once** per failed run — the ACL bootstrap is a one-shot step before the
publish/poll loop, so a single increment is the canonical signature of a
run that exited before any tick could fire.

This counter exists specifically to disambiguate "scenario never ran" from
"scenario ran with zero observations" on an otherwise-silent dashboard:
section 1 above is the same root cause but reads as "no metric at all",
whereas the `bootstrap_error` increment surfaces the failure on the outcome
breakdown.

**Likely causes** (ordered by frequency):

1. NATS unreachable or wrong credentials. The bootstrap publish uses the
   loadgen publisher; if the connection itself is broken the bootstrap is
   the first thing to fail.
2. `INBOX_{siteID}` stream not configured for the seed-time publish
   subject. The bootstrap publishes to
   `subject.InboxMemberAdded(siteID)` =
   `chat.inbox.{siteID}.member_added`
   ([`pkg/subject/subject.go:122-126`](../../../../pkg/subject/subject.go)).
   The owning service is `inbox-worker`; the bootstrap itself does NOT go
   through inbox-worker — it publishes directly onto the INBOX subject.
   If the stream is missing or doesn't accept that subject, the publish
   errors immediately.
3. JetStream not enabled on the local NATS (the bootstrap requires
   JetStream — the local INBOX stream is JetStream-backed).
4. Loadgen's NATS credentials lack publish permission on
   `chat.inbox.{siteID}.>`.

**Confirm.**

```bash
# Loadgen log: the bootstrap emits a structured error on failure
grep -E 'bootstrap search-sync ACL|inbox.*member_added' runs/<run_id>/run.log

# INBOX stream exists and accepts the member_added subject
nats stream info INBOX_site-local
nats stream subjects INBOX_site-local | grep member_added

# JetStream is up
nats server check jetstream

# Manual smoke publish onto the INBOX subject (substitute siteID)
nats pub chat.inbox.site-local.member_added '{}' --jetstream
```

**Resolution.**

- If NATS is unreachable, fix connectivity (creds path, NATS URL) and
  rerun. `loadgen doctor` covers the common cases.
- If `INBOX_{siteID}` is missing, have ops/IaC provision it via
  `pkg/stream.Inbox(siteID)` (see CLAUDE.md §6 Stream-bootstrap ownership
  rules — `inbox-worker` owns this stream). For dev environments, bring
  up the SUT with `BOOTSTRAP_STREAMS=true` so `inbox-worker` creates it.
- If the subject is configured but the publish still errors, check the
  builder in `pkg/subject/subject.go::InboxMemberAdded` against the
  stream's configured subjects — a rename or drift would manifest here.
- The narrative discussion of bootstrap failure in section 1 above
  applies — `bootstrap_error` is the outcome-counter signature of the
  same failure mode.

### 2. `publish_error` dominant

**Symptom.**

```
loadgen_search_index_visible_total{outcome="publish_error"} 1234
loadgen_search_index_visible_total{outcome="visible"} 0
loadgen_search_index_visible_total{outcome="timeout"} 0
```

**Likely causes** (ordered by frequency):

1. `--inject=frontdoor` (the default). With frontdoor injection the
   publisher uses core NATS, and the canonical subject has no core
   subscriber — every publish silently dead-letters. `NewGenerator`
   *should* reject this configuration up front
   ([`scenario_searchsync.go:96-103`](../../scenario_searchsync.go)); if
   you see `publish_error` anyway, check that the binary is up to date.
2. `MESSAGES_CANONICAL_{siteID}` stream does not exist (ops/IaC didn't
   bootstrap it, or `BOOTSTRAP_STREAMS=false` in a dev environment
   without manual provisioning).
3. Loadgen's NATS credentials don't have publish permission on the
   canonical subject.

**Confirm.**

```bash
# Stream exists, is on the expected subject, has the expected consumer
nats stream info MESSAGES_CANONICAL_site-local
nats stream subjects MESSAGES_CANONICAL_site-local

# Inject mode actually used
grep -- '--inject' runs/<run_id>/run.log

# Manual publish smoke-test (substitute siteID for your environment)
nats pub chat.msg.canonical.site-local.created '{}' --jetstream
```

**Resolution.**

- Add `--inject=canonical` to the `loadgen run` command. This is the
  single most common cause.
- If the stream is missing, have ops provision it via IaC, or — in a dev
  environment — bring up the SUT with `BOOTSTRAP_STREAMS=true` so the
  owning service creates it.
- If NATS creds are wrong, regenerate the loadgen creds via the path
  documented in [USAGE.md](../../USAGE.md).

### 3. `dropped_inflight` dominant

**Symptom.**

```
loadgen_search_index_visible_total{outcome="dropped_inflight"} 5421
loadgen_search_index_visible_total{outcome="visible"} 12
```

The publish is succeeding (so it isn't a `publish_error`) but the
in-flight semaphore is saturated and most polls are being skipped.

**Cause.** The scenario is over-driving itself relative to
`MAX_IN_FLIGHT`. Each poll goroutine lives up to `--search-sync-timeout`
seconds; sustained `dropped_inflight=0` requires
`rate × timeout ≲ MAX_IN_FLIGHT`. With defaults
(`MAX_IN_FLIGHT=200`, `--search-sync-timeout=90s`), that's `rate ≲ 2/s`.

The default `--rate=500` (set for the messaging-pipeline scenario) will
saturate within the first second of a search-sync-lag run.

**Confirm.**

```bash
grep -E '--rate|MAX_IN_FLIGHT' runs/<run_id>/run.log
```

**Resolution.**

- Lower `--rate` to `1` and raise gradually while watching the counter.
- For a sustained higher rate, raise `MAX_IN_FLIGHT` (env var) to at least
  `rate × timeout`. Be aware this raises the worst-case RPC fan-out
  against `search-service` — make sure the SUT can absorb it before
  raising past a few hundred.
- See [scenario doc → Sizing the run](../scenarios/search-sync-lag.md#sizing-the-run).

### 4. `transport_error` dominant

**Symptom.**

```
loadgen_search_index_visible_total{outcome="transport_error"} 800
loadgen_search_index_visible_total{outcome="visible"} 4
loadgen_search_index_visible_total{outcome="timeout"} 12
```

The publish succeeded but the `search.messages` NATS request itself is
failing.

**Likely causes** (ordered by frequency):

1. `search-service` is down or unhealthy — no subscriber on
   `chat.user.{account}.request.searchMessages`.
2. The request-reply timeout (`--request-timeout`, default `2s`) is too
   tight and `search-service` is responding slowly under load.
3. NATS is partitioned between loadgen and `search-service`.

**Confirm.**

```bash
# search-service running and reachable
docker compose ps search-service
docker compose logs --tail=50 search-service

# Is anyone subscribed?
nats sub 'chat.user.*.request.searchMessages' --count=1 &
nats req chat.user.alice.request.searchMessages '{"query":"x"}' --timeout=5s

# loadgen's NATS connectivity
grep -E 'nats|connect' runs/<run_id>/run.log
```

**Resolution.**

- If `search-service` is down, restart it and verify the healthz
  endpoint.
- If the service is up but slow, raise `--request-timeout` (default `2s`)
  to give per-request RPC more headroom.
- If NATS is partitioned, fix that first — every NATS-using scenario will
  be broken.

### 5. `timeout` dominant

**Symptom.**

```
loadgen_search_index_visible_total{outcome="timeout"} 200
loadgen_search_index_visible_total{outcome="visible"} 0
loadgen_search_index_visible_total{outcome="transport_error"} 0
loadgen_search_index_visible_total{outcome="publish_error"} 0
```

The publish landed, `search-service` is answering, but the document
never shows up within `--search-sync-timeout`. **This is the most common
"silent dashboard" pattern and almost always means the ACL doc is
missing.**

**Likely causes** (ordered by frequency):

1. **ACL doc missing.** `search-service` is AND'ing the query against an
   empty allowed-rooms set, so every query returns 0 hits even when the
   message *is* in ES.
2. ES `refresh_interval` is configured longer than
   `--search-sync-timeout` (rare; defaults are `30s` vs `90s`).
3. `search-sync-worker` is behind on the
   `MESSAGES_CANONICAL` consumer and the document isn't being indexed
   at all.
4. ES is under capacity / refusing bulk writes (look for 429s in
   sync-worker logs).

**Confirm — ACL doc (most common):**

```bash
# Did the bootstrap log line fire at run start?
grep 'bootstrap search-sync ACL' runs/<run_id>/run.log

# Is the user-room-sync consumer making progress?
nats consumer info INBOX_site-local user-room-sync
# Look at: Num Pending, Num Ack Pending, Last Delivered Sequence

# Does an ACL doc exist for one of the fixture accounts?
# (substitute the actual user-room index name from search-sync-worker config)
curl -s 'http://localhost:9200/user-room/_doc/alice' | jq '.found, ._source.rooms'

# Direct search bypass — does the message exist in ES at all?
curl -s 'http://localhost:9200/messages-*/_search?q=lgst*' | jq '.hits.total'
```

**Confirm — sync-worker behind:**

```bash
# Consumer pending for the messages collection (the consumer name follows
# search-sync-worker's collection naming; check its config)
nats consumer ls MESSAGES_CANONICAL_site-local
nats consumer info MESSAGES_CANONICAL_site-local <consumer-name>

# Sync-worker logs for ES errors / 429s
docker compose logs --tail=200 search-sync-worker | grep -iE 'error|429|reject'
```

**Confirm — ES refresh:**

```bash
# Current refresh_interval on the messages index
curl -s 'http://localhost:9200/messages-*/_settings' \
  | jq '.[] .settings.index.refresh_interval'
```

**Resolution.**

- **ACL doc missing**: raise `--search-sync-acl-wait` (default `35s`) to
  cover a slower bulk-flush — try `60s` or `90s`. If the bootstrap log
  line did not fire at all, the bootstrap publish itself errored before
  recording a metric; check the loadgen run log for
  `bootstrap search-sync ACL:` errors.
- **Sync-worker behind**: scale up `search-sync-worker`, raise its
  consumer's `MAX_AC_PENDING`, or lower scenario `--rate`.
- **ES misconfigured / under capacity**: confirm `refresh_interval` is
  not set to `-1` (refreshes disabled); confirm ES has memory/disk;
  examine `_cluster/health`.

### 6. `visible` is rare but non-zero (SLO violation)

**Symptom.**

```
loadgen_search_index_visible_total{outcome="visible"} 7
loadgen_search_index_visible_total{outcome="timeout"} 1893
```

The pipeline works, but most messages are not visible within
`--search-sync-timeout`. This is an **SLO investigation**, not a
configuration bug.

**Confirm.**

```bash
# Histogram p99 — what's the actual tail?
curl -s http://localhost:9090/api/v1/query \
  --data-urlencode 'query=histogram_quantile(0.99, sum(rate(loadgen_search_index_lag_seconds_bucket[5m])) by (le))' \
  | jq -r '.data.result[0].value[1]'

# Sync-worker consumer lag — is the bulk-batch the bottleneck?
nats consumer info MESSAGES_CANONICAL_site-local <messages-consumer>

# ES refresh + write health
curl -s 'http://localhost:9200/_cluster/health' | jq
curl -s 'http://localhost:9200/_cat/indices/messages-*?v'
```

**Resolution.**

- If p99 is just over `--search-sync-timeout`, raise the flag to capture
  the tail. The histogram has buckets up to 300 s for a reason.
- If sync-worker consumer has steady pending count > 0, it's the
  bottleneck — investigate bulk-batch sizing and ES bulk-write throughput.
- If ES refresh is slower than `30s` (operator-tuned), set the
  `--search-sync-timeout` to ≥ `3 × refresh_interval`.

## See also

- [Scenario doc — search-sync-lag](../scenarios/search-sync-lag.md)
- [USAGE.md → search-sync-lag](../../USAGE.md#search-sync-lag)
- [USAGE.md → Pitfalls](../../USAGE.md#pitfalls--troubleshooting)
- [`scripts/triage.sh`](../../scripts/triage.sh) for diagnostic data collection
- [`scenario_searchsync.go`](../../scenario_searchsync.go) — outcome taxonomy is canonicalized here
- [`metrics.go:58-74`](../../metrics.go) — metric definitions and bucket layout
