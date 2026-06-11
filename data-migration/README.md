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

- **Watched collections (8):** `rocketchat_message` (pre-image), `rocketchat_room`,
  `rocketchat_subscription`, `rocketchat_uploads`, `tsmc_room_members`,
  `tsmc_thread_subscriptions`, `tsmc_hr_acct_org`, `users`. All op types
  (insert/update/replace/delete) are traced for every collection.
- **Subjects:** `chat.oplog.{siteID}.{rawCollection}.{op}`; dedup via
  `Nats-Msg-Id` = change-stream `_id._data`.
- **Checkpoints:** one doc per collection in `oplog_checkpoints` (in `CHECKPOINT_DB`
  on the source RS). The resume token is the real checkpoint; saved only after a
  pub-ack (lossless).

### Source-side prerequisites (ops)

1. **Replica set.** Change streams require the source Mongo to be a replica set.
2. **Pre-images.** Each `PREIMAGE_COLLECTIONS` collection must be created with
   `changeStreamPreAndPostImages: { enabled: true }` so deletes carry the prior
   document.
3. **Network egress** from the connector to the source RS (read change streams +
   write checkpoints).
4. **Seed the handoff.** Before first start, pre-insert one seed checkpoint per
   collection (`Source:"seed"`, `ResumeToken:R`) — or set `START_RESUME_TOKEN` —
   so live sync begins exactly after the migrated cut.

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

Stands up a single-node replica set (pre-images enabled), JetStream NATS, and
the connector with `BOOTSTRAP_STREAMS=true`.

### Tests

```
make test SERVICE=data-migration/oplog-connector              # unit (race)
make test-integration SERVICE=data-migration/oplog-connector  # store + live CDC e2e (Docker)
```
