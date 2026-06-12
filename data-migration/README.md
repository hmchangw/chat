# data-migration

Migration tooling for moving off the legacy ("source") RocketChat-style MongoDB
onto the distributed chat stack. This folder groups the migration components so
they share ownership seams and a single blast radius — **the whole folder is
deletable once the source is retired.** Each component is a standard flat
`package main` service; shared code lives in the root `pkg/migration/` (there is
no nested `pkg/`/`internal/` here).

```
                    consistent cut (clusterTime T, resume token R)
                                 │
  source Mongo ─────────────────┼──────────────────────────────▶ time
                                │
   history-migrator   ◀── bulk copy of state ≤ T ──┐ captures R
   (sibling, separate owner)                       │
                                                    ▼
   oplog-connector    seed = R ──▶ startAfter(R) ──▶ live CDC ─▶ MIGRATION_OPLOG_{site}
   (this folder)                                                       │
                                                                       ▼
   oplog-transformer  ── consumes, models per collection ─▶ new stack DBs
   (sibling, separate owner)
```

## Components

| Component | Status | Role |
|-----------|--------|------|
| **oplog-connector** | implemented | "Dumb pump": tails source change streams and publishes raw, opaque CDC events to `MIGRATION_OPLOG_{site}`. Lossless, ordered per collection, resumable. |
| history-migrator | not started | Bulk DB→DB copy of state ≤ the consistent cut; captures the resume token the connector seeds from. |
| oplog-transformer | not started | Consumes `MIGRATION_OPLOG_{site}`, decodes documents per collection, models them into the new stack. |

See `docs/superpowers/specs/2026-06-08-oplog-connector-design.md` (design) and
`docs/superpowers/plans/2026-06-11-oplog-connector-implementation.md` (plan).

## oplog-connector at a glance

- **Watched collections (8):** `rocketchat_message`, `rocketchat_room`,
  `rocketchat_subscription`, `rocketchat_uploads`, `tsmc_room_members`,
  `tsmc_thread_subscriptions`, `tsmc_hr_acct_org`, `users`. All op types
  (insert/update/replace/delete) are traced for every collection, identically.
- **No lookups.** The connector forwards native oplog content only — `fullDocument`
  for insert/replace, `updateDescription` (the delta) for update, `documentKey` for
  delete. No `updateLookup`, no pre-images. All enrichment is the transformer's job.
- **Subjects:** `chat.oplog.{siteID}.{rawCollection}.{op}`; dedup via
  `Nats-Msg-Id` = change-stream `_id._data`.
- **Checkpoints:** one doc per collection in `oplog_checkpoints` (in `CHECKPOINT_DB`
  on the source RS). The resume token is the real checkpoint; saved only after a
  pub-ack (lossless).
- **Observability:** Prometheus `/metrics` + `/healthz` on `METRICS_ADDR` —
  `oplog_events_published_total`, `oplog_publish_errors_total`,
  `oplog_events_skipped_total`, `oplog_replication_lag_ms` (all by `collection`).
  For a single-replica pump, **alert on lag and sustained publish errors** — that's
  how a soft stall (retry-forever) is caught before the oplog window closes.

### Configuration (the interface)

All configuration is via environment variables. Required vars have no default and
fail-fast at startup.

| Env | Req | Default | Purpose |
|-----|-----|---------|---------|
| `SITE_ID` | ✓ | — | site scope for subjects, stream name, checkpoint `_id` |
| `SOURCE_MONGO_URI` | ✓ | — | source replica set (change streams + checkpoint writes). **No credentials in the URI — it is logged.** |
| `SOURCE_MONGO_USERNAME` / `SOURCE_MONGO_PASSWORD` | | `""` | source auth (preferred over creds-in-URI) |
| `SOURCE_DB` | | `rocketchat` | DB the watched collections live in |
| `CHECKPOINT_DB` | | `migration` | DB holding `oplog_checkpoints` (on the source RS) |
| `NATS_URL` | ✓ | — | publish target |
| `NATS_CREDS_FILE` | | `""` | NATS credentials file |
| `WATCH_COLLECTIONS` | ✓ | — | comma-list of raw collections to tail (one watcher each; **no duplicates**) |
| `READ_PREFERENCE` | | `secondary` | source read pref (`primary`\|`primaryPreferred`\|`secondary`\|`secondaryPreferred`\|`nearest`) |
| `CHECKPOINT_EVERY` | | `100` | persist the resume token every N acked events |
| `CHECKPOINT_MAX_AGE` | | `30` | also persist it at least every N seconds (bounds replay for low-volume collections) |
| `START_MODE` | | `now` | cold-start when no checkpoint exists: `now`\|`beginning`\|`time` |
| `START_AT_TIME` | | `""` | RFC3339 or unix-ms; used by `START_MODE=time` **and** as an override (see below) |
| `START_RESUME_TOKEN` | | `""` | `_data` hex; one-off seed override (see below) |
| `BOOTSTRAP_STREAMS` | | `false` | dev-only stream creation; **keep `false` in prod** (ops/IaC owns the stream) |
| `METRICS_ADDR` | | `:9090` | bind addr for the Prometheus `/metrics` + `/healthz` listener |
| `LOG_LEVEL` | | `info` | slog level (`debug`\|`info`\|`warn`\|`error`) |

**Where the change stream starts (per collection, first match wins):**

1. **Env override** — `START_RESUME_TOKEN` (→ `startAfter`) or `START_AT_TIME`
   (→ `startAtOperationTime`). Forces a reseed; **ignores the stored checkpoint.**
2. **Stored checkpoint** — `startAfter(resumeToken)` (the normal restart path).
3. **Cold start** — `START_MODE`: `now` (default) \| `beginning` \| `time`.

### Source-side prerequisites (ops)

1. **Replica set.** Change streams require the source Mongo to be a replica set.
2. **Network egress** from the connector to the source RS (read change streams +
   write checkpoints).
3. **Seed the handoff.** Before first start, pre-insert one seed checkpoint per
   collection (`Source:"seed"`, `ResumeToken:R`) — **preferred** — so live sync
   begins exactly after the migrated cut.

No `changeStreamPreAndPostImages` / pre-image setup is required — the connector
does no lookups.

> ⚠️ **Do not leave `START_RESUME_TOKEN` / `START_AT_TIME` set in the
> environment.** They are one-off overrides that *ignore the stored checkpoint
> and reseed on every restart*. Use them for a manual one-off only, then unset;
> seed via the pre-inserted checkpoint doc instead. The connector logs a `WARN`
> at startup when either is set.
>
> ⚠️ **Do not embed credentials in `SOURCE_MONGO_URI`** — the URI is logged at
> startup. Use `SOURCE_MONGO_USERNAME` / `SOURCE_MONGO_PASSWORD` instead.

### Implementation note — synchronous publish

The repo's `oteljetstream` wrapper intentionally does not expose async publish.
The connector therefore uses **one synchronous-publish goroutine per
collection** (`Next → PublishMsg (blocks on pub-ack) → checkpoint`) rather than
the async pipeline + in-flight window in the original design. This preserves the
same guarantees — per-collection order, at-least-once, checkpoint-only-after-ack
— at the cost of per-collection pipelining throughput, which is acceptable for
live-sync volumes. Per-collection parallelism is retained (one goroutine each).

### Local dev

```
make up SERVICE=data-migration/oplog-connector
```

Stands up a single-node replica set, JetStream NATS, and
the connector with `BOOTSTRAP_STREAMS=true`.

### Tests

```
make test SERVICE=data-migration/oplog-connector              # unit (race)
make test-integration SERVICE=data-migration/oplog-connector  # store + live CDC e2e (Docker)
```
