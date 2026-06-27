# Alerts for silent degradations

The audit in [`incident-scenarios.md`](incident-scenarios.md) found that the
dominant failure shape in this system is **silent degradation** — an operation
reports success (HTTP 200 / JetStream Ack / empty result set) while the result
is incomplete, with no signal that anything went wrong. The fix for each is the
same: emit a metric so the failure becomes observable, then alert on it.

**Ownership.** Services emit OpenTelemetry metrics on `/metrics` (via
`pkg/otelutil.InitMeter` + `MetricsServer`). The alert *rules* themselves live
in ops/IaC, exactly like JetStream stream provisioning — this repo does not
carry PrometheusRule/alertmanager manifests. This file is the contract between
the two: for each silent gap it names the metric the code emits (or must emit)
and the PromQL ops should alert on.

Status legend: **emitted** = the metric exists in code today; **proposed** =
the metric still needs to be wired (the service often needs `InitMeter` +
`MetricsServer` first — noted inline).

---

## Emitted

### `message_bucket_hours` — MESSAGE_BUCKET_HOURS drift (§1.1, highest risk)

The single highest-aggregate risk. The Cassandra bucket key is
`floor(createdAt / window) * window`, computed independently by each service
from its own `MESSAGE_BUCKET_HOURS`. If the writer and reader disagree (e.g. a
staggered deploy leaves `message-worker` at 72h and `history-service` at 48h),
writes and reads target different partitions and range reads silently return
incomplete or empty results — Cassandra reports no error.

- **Type:** Int64 observable gauge, re-observed every scrape. Value = configured window in hours.
- **Emitted by:** `message-worker`, `history-service` (any service touching `messages_by_room` should `bucketmetrics.Register` at startup).
- **Source:** `pkg/bucketmetrics`, wired in each service's `main` after `otelutil.InitMeter`.

```promql
# FIRES when any two scrape targets report different bucket windows.
max(message_bucket_hours) != min(message_bucket_hours)
```

```yaml
# Suggested rule for ops/IaC (illustrative — not provisioned from this repo):
- alert: MessageBucketHoursDrift
  expr: max(message_bucket_hours) != min(message_bucket_hours)
  for: 5m
  labels: { severity: critical }
  annotations:
    summary: "MESSAGE_BUCKET_HOURS differs across services — message history reads are silently incomplete"
    runbook: "All services reading/writing messages_by_room must share one window. Re-align config; a changed window requires a re-bucketing migration."
```

A complementary cheap guard (not a metric): make `MESSAGE_BUCKET_HOURS`
`required` with no default so a service can never silently fall back to 72h.

---

## Proposed

Each entry names the metric to add and the PromQL to alert on. Where a service
has no meter today, it first needs `otelutil.InitMeter(<name>)` + an
`otelutil.MetricsServer()` bound on `METRICS_ADDR` (mirror `message-worker`).

### `inbox_member_add_skipped_total` — federated member dropped (§1.2, confirmed)

`inbox-worker handleMemberAdded` skips any account missing from the local
`users` collection (`slog.Warn` + `continue`) and Acks as success — a permanent,
silent cross-site membership divergence. Needs a counter (and `inbox-worker`
needs meter plumbing — it currently inits only the tracer).

```promql
# FIRES on any sustained skip rate — every skip is a user silently missing a room.
sum(rate(inbox_member_add_skipped_total[5m])) > 0
```

Pair with the behavioural fix the audit recommends: Nak (retry) instead of
silent skip, so a sync race self-heals.

### `presence_peer_query_failures_total` — cross-site presence offline-fallback (§2.3, confirmed)

`user-presence-service QueryBatch` returns `nil` on a peer-site timeout, so those
users silently default to **offline** with a 200 OK. Needs a counter labelled by
peer site (and `user-presence-service` needs meter plumbing).

```promql
sum by (peer_site) (rate(presence_peer_query_failures_total[5m])) > 0
```

### `search_ccs_skipped_clusters_total` — cross-cluster search degradation (§2.4)

`search-service` queries `*:messages-*` with `ignore_unavailable=true`, so an
unreachable remote ES cluster yields 200 + empty hits — search silently degrades
to local-only. `search-service` already exposes `/metrics`; inspect the
`_clusters` block of the ES response and count `skipped`.

```promql
sum by (cluster) (rate(search_ccs_skipped_clusters_total[5m])) > 0
```

### `notification_membership_cache_stale_total` — removed member notified (§3.2)

`notification-worker` reads a TTL cache of room members; a member removed just
before a publish can still receive a push (info leak). `notification-worker`
already exposes `/metrics`. Emit a counter when delivery-time revalidation finds
a candidate that is no longer subscribed (this also requires adding that
revalidation).

```promql
sum(rate(notification_membership_cache_stale_total[5m])) > 0
```

### `message_ack_failures_total` — Ack failure after canonical publish (§1.6, §2.7)

The message hot-path workers log `failed to ack message` but emit no counter, so
duplicate-reprocessing pressure (and the JetStream redelivery it causes) is
invisible. `message-gatekeeper`, `message-worker`, `broadcast-worker` all expose
`/metrics`. Add a counter at each Ack-failure site.

```promql
sum by (service) (rate(message_ack_failures_total[5m])) > 0
```

### Federation reachability — INBOX missing at destination (§2.1, confirmed)

A remote site whose `INBOX` stream is not provisioned silently drops every
cross-site publish (the publisher sees success). This is best caught by an
active probe rather than a passive counter: a periodic federation health check
that verifies each remote `INBOX_<site>` exists and a consumer is processing,
exporting `federation_remote_inbox_up{peer_site}` (1/0).

```promql
min by (peer_site) (federation_remote_inbox_up) == 0
```

---

## Adding a new degradation metric

1. Define the instrument in a small `pkg/<domain>metrics` package or a
   service-local `metrics.go`, following the `pkg/cachemetrics` constructor +
   `ManualReader` test idiom (TDD: assert the increment via a collected datapoint).
2. If the service has no meter, add `otelutil.InitMeter(<service>)` and bind
   `otelutil.MetricsServer()` on `METRICS_ADDR`, stopping it last during drain
   so the final scrape lands.
3. Record at the exact silent site — the `continue`/`return nil`/empty-result
   branch the audit flagged — so the metric counts the real degradation.
4. Document the metric and its PromQL here, cross-referencing the
   `incident-scenarios.md` section.
