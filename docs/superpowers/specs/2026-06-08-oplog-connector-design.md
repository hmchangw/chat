# oplog-connector — live-sync CDC pump (Design)

*source Mongo → `MIGRATION_OPLOG_{site}`*

> **Status:** DESIGN — not yet implemented. This document is the agreed design record produced via brainstorming; the implementer follows the TDD cycle in CLAUDE.md §4 against the contract below.

*A small, single-replica, per-site service whose only job is to tail the legacy ("source") MongoDB via change streams and pump **raw, uninterpreted** change events into a single JetStream stream. It does no enrichment, no lookups, no per-collection schema knowledge — a downstream **transformer** (out of scope here) consumes `MIGRATION_OPLOG_{site}` and does the actual modelling. This is the "dumb pump" half of a two-stage migration.*

---

## 0. Context — where this sits in the migration

We are migrating off a legacy RocketChat-style Mongo onto the new distributed chat stack. The migration has **two independent halves**:

1. **History migration (separate service, out of scope).** A bulk DB→DB copy of everything already in the source at a consistent cut. It captures a **resume token / clusterTime at its snapshot point** and hands that off.
2. **Live-sync CDC (this service).** Picks up **exactly after the migrated cut** and streams every subsequent change. The handoff is the **init checkpoint**: the connector seeds its change stream from the token the history migration captured, so live sync begins precisely where the bulk copy ended — **zero gap**, and any overlap collapses on `Nats-Msg-Id` dedup.

```
                    consistent cut (clusterTime T, resume token R)
                                 │
  source Mongo ─────────────────┼──────────────────────────────▶ time
                                │
   history-migration  ◀── bulk copy of state ≤ T ──┐
   (separate service)                              │ captures R
                                                    ▼
   oplog-connector    seed init checkpoint = R ──▶ startAfter(R) ──▶ live CDC ─▶ MIGRATION_OPLOG_{site}
   (this service)
```

So the connector's job reduces to: **resume from a given point, and never lose or reorder an event after it.**

---

## 1. Goal & non-goals

**Goal.** Reliably mirror source Mongo change events into `MIGRATION_OPLOG_{site}` such that, per collection:
- every event after the init checkpoint is delivered **at least once** (loss is unacceptable; duplicates are fine — dedup collapses them),
- events are delivered **in oplog order** end to end,
- the connector can **crash/restart losslessly** by resuming from a persisted checkpoint,
- an operator can **seed the start point** (the migration handoff) and, in recovery, **re-seed** it.

**Non-goals.**
- No interpretation/transformation of documents — opaque pass-through only (the transformer owns modelling).
- No global cross-collection total order (see §6 — inherent to per-collection watchers).
- No bulk/history backfill — that is the separate history-migration service.
- No client-facing request/reply surface — this service has no `errcode` boundary.

---

## 2. The B1↔B2 contract (what the transformer consumes)

### 2.1 Stream

- **Stream:** `MIGRATION_OPLOG_{siteID}` (added to `pkg/stream/stream.go` as `MigrationOplog(siteID)`).
- **Subjects:** `["chat.oplog.{siteID}.>"]`.
- **Retention:** soak / time-based, sized over the **worst-case transformer outage** so the transformer can be down and replay without data loss. (Exact window is an ops/IaC decision; the connector does not depend on it.)
- **Ownership:** the connector **owns** this stream and bootstraps it in dev only (§5).

### 2.2 Subject

```
chat.oplog.{siteID}.{rawCollection}.{op}      op ∈ insert | update | replace | delete
```

`rawCollection` is the **raw source collection name** (e.g. `rocketchat_messages`) — the connector does not rename. Built via a new `pkg/subject` builder, never `fmt.Sprintf`.

### 2.3 Dedup key

`Nats-Msg-Id` header = the change-stream event id (`_id._data`). JetStream message-dedup collapses the migration-handoff overlap and any redelivery after a crash.

### 2.4 Published envelope (`pkg/model/oplog_event.go`)

Typed envelope, but documents stay **opaque** (`json.RawMessage`) so the dumb connector remains collection-agnostic — the transformer decodes per collection. (This is deferred decoding, not `map[string]interface{}`; it complies with the "typed structs" rule.)

```go
type OplogEvent struct {
    EventID      string          `json:"eventId"`      // change-stream _id._data; also Nats-Msg-Id
    Op           string          `json:"op"`           // insert | update | replace | delete
    DB           string          `json:"db"`
    Collection   string          `json:"coll"`         // raw source name
    DocumentKey  json.RawMessage `json:"documentKey"`  // { _id: ... }
    ClusterTime  int64           `json:"clusterTime"`  // source op time, unix ms
    FullDocument json.RawMessage `json:"fullDocument,omitempty"` // post-image (insert/update/replace)
    PreImage     json.RawMessage `json:"preImage,omitempty"`     // pre-image; messages deletes only
    SiteID       string          `json:"siteId"`
    Timestamp    int64           `json:"timestamp"`    // publish time, unix ms (event-level, per CLAUDE.md)
}
```

The opaque **resume token** is kept **internally** for checkpointing and is *not* in the payload.

---

## 3. Architecture & components

Flat per-service layout (CLAUDE.md §"per-service file organization"):

```
oplog-connector/
  main.go            config parse, connect source Mongo + NATS, bootstrap, wire, start watchers, shutdown.Wait
  config.go          typed Config (caarlos0/env)
  handler.go         the watcher engine (read → channel → per-collection sequential publisher → frontier)
  bootstrap.go       bootstrapStreams (owns MIGRATION_OPLOG_{site}, gated by BOOTSTRAP_STREAMS)
  store.go           CheckpointStore interface + //go:generate mockgen
  store_mongo.go     Mongo impl over the `migration` DB on the source RS
  handler_test.go    unit: mocked store + injected publish fn
  integration_test.go //go:build integration — testcontainers Mongo + NATS
  mock_store_test.go  generated
  deploy/{Dockerfile,docker-compose.yml,azure-pipelines.yml}

pkg/model/oplog_event.go     OplogEvent (+ pkg/model round-trip test entry)
pkg/stream/stream.go         MigrationOplog(siteID)
pkg/subject/...              oplog subject builder
```

### 3.1 Watcher engine (per configured collection)

One change stream per collection. For each:

```
 change stream cursor ──▶ reader goroutine ──▶ buffered channel ──▶ ONE sequential publisher goroutine ──▶ PublishAsync
                                                                            │
                                                                  contiguous ack frontier
                                                                            │
                                                                  persist token (post-ack)
```

- **Read options:** post-image via `updateLookup`; **pre-image** (`fullDocumentBeforeChange`) only for collections in `PREIMAGE_COLLECTIONS` (default `rocketchat_messages`); `majority` read concern; read from **secondary**.
- **Single active reader per collection** — guaranteed by `replicas=1` (see §7 HA). No leader election.

### 3.2 Checkpoint store

Interface in `store.go` (consumer-defined, minimal):

```go
type CheckpointStore interface {
    Load(ctx context.Context, collection string) (*Checkpoint, error) // nil, nil when absent
    Save(ctx context.Context, cp *Checkpoint) error                   // upsert by _id
}
```

Mongo impl writes to the `oplog_checkpoints` collection in the `migration` DB **on the source replica set** (reuses the connection the connector already has; no new cluster; checkpoints die with the source when migration ends). `EnsureIndexes` (just `_id`, which is implicit) in the constructor.

---

## 4. Checkpoints & start-point resolution

### 4.1 Checkpoint document (`oplog_checkpoints`)

One doc per collection, `_id = "{siteID}:{collection}"`:

```go
type Checkpoint struct {
    ID          string   `bson:"_id"`          // "{siteID}:{collection}"
    SiteID      string   `bson:"siteId"`
    Collection  string   `bson:"collection"`   // raw source name
    ResumeToken bson.Raw `bson:"resumeToken"`  // change-stream token {_data:"..."} — fed back verbatim
    ClusterTime int64    `bson:"clusterTime"`  // op time of last acked event, unix s — fallback + observability
    EventID     string   `bson:"eventId"`      // _id._data of last acked event
    Source      string   `bson:"source"`       // "seed" | "runtime" — provenance
    UpdatedAt   int64    `bson:"updatedAt"`    // last persist time, unix ms
}
```

The **resume token is the real checkpoint** (opaque, raw BSON so it round-trips exactly). `ClusterTime` is a coarse fallback (feeds `startAtOperationTime` if a token is ever absent) and lets ops eyeball lag without decoding the token.

### 4.2 Start-point resolution (per collection, precedence top-down)

```
1. ENV override (forces a reseed; ignores any stored checkpoint)
     START_RESUME_TOKEN=<_data hex>     → startAfter(token)
     START_AT_TIME=<RFC3339|unix-ms>    → startAtOperationTime(ts)
2. Persisted checkpoint
     startAfter(cp.ResumeToken)         (or startAtOperationTime(cp.ClusterTime) if token absent)
3. Cold start default (no checkpoint, no override)
     START_MODE = now (default) | beginning | time(+START_AT_TIME)
```

### 4.3 The two operator inputs

- **Init checkpoint (the migration handoff) — "provide a checkpoint".** The history-migration service captures the resume token `R` at its consistent cut. Operationally, **pre-insert one seed doc per collection** into `oplog_checkpoints` (`Source:"seed"`, `ResumeToken:R`) before first start — per-collection, no env juggling. For a one-off, the global `START_RESUME_TOKEN` env does the same. The connector then `startAfter(R)` → live sync begins exactly after the migrated cut.
- **Initial start point — "init start point".** `START_MODE` / `START_AT_TIME` cover cold start when **no** checkpoint exists (e.g. a brand-new collection with no migration handoff).

### 4.4 `startAfter`, not `resumeAfter`

Tokens are fed back with **`startAfter`**: it survives invalidate events (collection drop/rename mid-migration) where `resumeAfter` hard-fails. Same token format, strictly safer for this workload.

---

## 5. Bootstrap & config

### 5.1 Stream bootstrap (CLAUDE.md §JetStream)

`bootstrap.go` defines `bootstrapStreams(ctx, js, siteID, enabled) error`, gated by `BOOTSTRAP_STREAMS` (env `STREAMS`, default `false`), no-op when disabled. When enabled (dev), creates **only** `MIGRATION_OPLOG_{site}`'s schema (`Name + Subjects` from `pkg/stream.MigrationOplog(siteID)`) via `js.CreateOrUpdateStream`. No federation `Sources`/`SubjectTransforms` — those are ops/IaC owned.

### 5.2 Config (`caarlos0/env`, fail-fast on required)

| Env | Req? | Default | Purpose |
|-----|------|---------|---------|
| `SITE_ID` | ✓ | — | site scope for subjects/stream/checkpoint `_id` |
| `SOURCE_MONGO_URI` | ✓ | — | source RS connection (read change streams + write checkpoints) |
| `CHECKPOINT_DB` | | `migration` | DB on source RS holding `oplog_checkpoints` |
| `NATS_URL` | ✓ | — | publish target |
| `WATCH_COLLECTIONS` | ✓ | — | comma-list of raw collections to tail |
| `PREIMAGE_COLLECTIONS` | | `rocketchat_messages` | subset needing pre-images |
| `READ_PREFERENCE` | | `secondary` | source read preference |
| `PUBLISH_CHANNEL_BUFFER` | | `1024` | per-watcher reader→publisher buffer |
| `MAX_INFLIGHT_PUBLISHES` | | `256` | async pub-ack window before backpressure |
| `START_MODE` | | `now` | cold-start default: `now` \| `beginning` \| `time` |
| `START_AT_TIME` | | — | RFC3339 or unix-ms; used with `START_MODE=time` or as override |
| `START_RESUME_TOKEN` | | — | one-off global seed override (`startAfter`) |
| `BOOTSTRAP_STREAMS` | | `false` | dev-only stream creation |
| `LOG_LEVEL` | | `info` | slog level |

> `WATCH_COLLECTIONS` / `PREIMAGE_COLLECTIONS` are **tentative** — final collection set is confirmed during implementation against the source schema.

---

## 6. Ordering invariants

Three monotonic positions answer "what we init / what we pushed / what's next / what comes after":

| Question | Tracked by |
|---|---|
| What we init from | seed / `START_*` start point (resume token or clusterTime) |
| What we've pushed | `Checkpoint.ResumeToken` = the **contiguous ack frontier** (every event ≤ here is pub-acked + durable) |
| What's next to push | the next event the cursor yields after the frontier / head of the channel |
| What comes after | the strictly oplog-ordered tail |

- **Resume tokens are monotonic** (encode oplog position) — comparable; the frontier only moves forward along the *contiguous* acked prefix, never jumping a gap.
- **JetStream stream sequence is monotonic** — a second independent ordering the transformer reads off the consumer side.

**Invariant — per-collection order preserved end to end.** Per collection there is exactly **one sequential publisher** draining the channel in oplog order on one connection, so wire order = stream-sequence order. `MAX_INFLIGHT_PUBLISHES` only bounds un-acked throughput; on a NAK/timeout the publisher **stalls and retries** — it never advances the frontier past a gap. This is what makes "what's next" truly the next event, not a hole.

**Caveat — no global cross-collection total order.** Watchers are independent change streams, so order is strict *within* a collection but **not** between collections. `ClusterTime` gives a coarse cross-collection sort (ties possible), not a strict total order. A transformer needing cross-entity causal order must tolerate reordering rather than assume one global sequence. This is inherent to per-collection watchers; the alternative (one whole-DB stream) trades away per-collection parallelism and the clean per-collection checkpoint, and is rejected.

---

## 7. HA, error handling & lifecycle

### 7.1 HA — single replica + resume

`replicas=1`. No leader election. Failover = pod restart; losslessness comes from the persisted checkpoint + the soak/dedup window, not from a hot standby.

### 7.2 Error handling

- **Invalid / expired resume token** (`ChangeStreamHistoryLost`, code 286): loud `slog.Error`, **exit non-zero**. The connector does **not** silently reseed-from-now — that would drop events. Recovery is operator-driven and uses the same seeding model as §4.3: re-snapshot via the history-migration service, update the seed doc / `START_RESUME_TOKEN`, restart.
- **Publish failure (no pub-ack):** retry with backoff. The contiguous frontier does **not** advance past an un-acked event, so the token is never persisted ahead of durably-stored data. Sustained failure → the bounded channel applies backpressure, the reader stalls, the lag metric climbs, an alert fires.
- **Checkpoint persistence:** the token for collection C is `Save`d only after **every event ≤ that position** has a pub-ack — at-least-once, never loss. Crash → resume `startAfter` the last persisted token → duplicates collapse on `Nats-Msg-Id`.
- **No client-facing `errcode`:** no request/reply handlers; all errors are internal/operational (wrapped with `fmt.Errorf("...: %w", err)` per CLAUDE.md §3).

### 7.3 Graceful shutdown (`pkg/shutdown.Wait`, ≤25s)

stop readers → close change-stream cursors → drain channels / await in-flight pub-acks (bounded timeout) → persist final frontier per collection → `nc.Drain()` → disconnect Mongo.

### 7.4 Observability

`log/slog` JSON. Correlation field per event = `EventID` (the resume-token data). Metrics: replication lag (now − `ClusterTime`), events/sec per collection, publish errors, in-flight depth, frontier position.

---

## 8. Testing (CLAUDE.md §4 — TDD, 80% floor / 90% target on handler + store)

### 8.1 Unit (`handler_test.go`) — mocked `CheckpointStore` + injected publish fn capturing payloads

Table-driven over:
- op types (insert/update/replace/delete) → correct subject + envelope fields,
- pre-image present only for `PREIMAGE_COLLECTIONS`,
- **frontier advances only along the contiguous acked prefix**,
- **publish failure stalls the frontier** (token not persisted past a gap),
- **token persisted only after ack**,
- start-point resolution precedence (override > checkpoint > cold-start default),
- envelope marshal/unmarshal.

### 8.2 Integration (`integration_test.go`, `//go:build integration`)

`testutil.MongoDB` + `testutil.NATS`; `TestMain` → `testutil.RunTests`. Insert/update/delete in source → assert envelope lands on `MIGRATION_OPLOG_{site}` with right subject/headers; **resume-after-restart** from a persisted token (no gap); **dedup** on redelivery (same `Nats-Msg-Id`); seed-checkpoint start (`startAfter`) begins exactly after the seeded point.

### 8.3 Model (`pkg/model`)

`OplogEvent` round-trips via the existing generic `roundTrip` helper.

---

## 9. Open / deferred

- Final `WATCH_COLLECTIONS` / `PREIMAGE_COLLECTIONS` set — confirm against source schema at implementation time.
- Soak retention window sizing — ops/IaC decision.
- The downstream **transformer** (consumes `MIGRATION_OPLOG_{site}`) is a separate spec.
