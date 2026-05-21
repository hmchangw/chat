# first-dm scenario (Phase 3 §3.13)

## Status

**Implemented.** The scenario records three sub-stage lags into
`loadgen_first_dm_lag_seconds{stage="room|subs|e2e"}` by running a real
RoomCreate-then-publish loop against room-service per user pair pulled
from the seeded `loadgen-firstdm-` pool. An earlier shape included a
`stage=persist` measurement keyed off `chat.msg.canonical.{siteID}.created`,
but that subject is consumed silently by message-worker (no
persist-complete event), so the lag was the loadgen→broker→loadgen
self-loop, not Cassandra persistence. Removed for honesty; measuring
real Cassandra persistence would require a Cassandra poll or a new
persist-complete signal on the SUT side, both out of scope here.

## What it measures

End-to-end latency of the **first DM** path: the moment user A decides
to message user B (no existing DM room between them) all the way
through to user B receiving the broadcast.

Concretely, the scenario exercises room-service's
`handleCreateRoomDMOrBotDM` path: when no room exists for the
`(userA, userB)` tuple, `idgen.BuildDMRoomID(a, b)` is used to compute
a deterministic DM room ID and the room + subscription documents are
created on the fly. The scenario breaks the publish→broadcast path into
three sub-stages so operators can pinpoint whether room provisioning,
subscription fan-out, or broadcast delivery is the slow link.

## Three lag stages

All three are measured against a single anchor `publishedAt` (set right
before issuing the RoomCreate request from userA):

| Stage | From → To                                                                  |
|-------|----------------------------------------------------------------------------|
| room  | publishedAt → observer sees the `chat.room.canonical.{siteID}.create` event |
| subs  | publishedAt → `chat.user.*.event.subscription.update` seen for **both** userA and userB |
| e2e   | publishedAt → `chat.user.{userB}.event.room` broadcast delivered            |

The three stages map to `loadgen_first_dm_lag_seconds{stage}` histogram
labels. e2e ≥ subs ≥ room in steady state. Per-stage isolation lets ops
single out the slow link rather than reading a single end-to-end number.

## Pipeline

```
loadgen.tick:
    pair = pool.Next()                          # (userA, userB) from fixture pool
    roomID = idgen.BuildDMRoomID(userA, userB)  # deterministic DM room ID
    msgID = idgen.GenerateMessageID()           # message ID for e2e correlation
    publishedAt = now()
    tracker.RecordPublish(roomID, msgID, userA, userB, publishedAt)
    Requester.Request(chat.user.{userA}.request.room.{siteID}.create, …)
    Publisher.Publish(chat.msg.canonical.{siteID}.created, MessageEvent{ID: msgID, RoomID: roomID, …})

observer subscriptions (installed once at Run start):
    chat.room.canonical.{siteID}.create     → record "room" stage
    chat.user.*.event.subscription.update   → record "subs" stage (only after both userA and userB land)
    chat.user.*.event.room                  → record "e2e"  stage (matched by msgID → roomID lookup)

per iteration: poll the tracker until all three stages land or the
firstDMStageTimeout (10s) elapses; observe each present stage into
loadgen_first_dm_lag_seconds{stage}.
```

## Why NATS observers, not Mongo polls

The original spec sketch suggested polling the `subscriptions`
collection for the subs stage. We observe the `SubscriptionUpdate`
NATS event instead — room-worker publishes it from
`finishCreateRoom` (room-worker/handler.go) **after** the
subscription doc has been inserted, so the on-wire event is a
sufficient proxy for "doc is visible in Mongo". This keeps the
scenario NATS-only and avoids an extra Mongo connection per run.

## Fixture shape

The scenario needs a pool of **unused** user pairs — users who have
never messaged each other so the room-service DM-create path is
exercised fresh on every tick. Provision the pool via:

```bash
loadgen seed --workload=messages --preset=small \
    --include-first-dm-fixtures \
    --first-dm-pairs=1000
```

`--include-first-dm-fixtures` adds `--first-dm-pairs` (default 1000)
pairs of users prefixed with `loadgen-firstdm-user-` (see
[`preset.go::augmentWithFirstDMFixtures`](../../preset.go)). These
users carry full `EngName`, `ChineseName`, and `SiteID` fields so
room-service's `errInvalidUserData` check passes. No rooms or
subscriptions are seeded for these users — the SUT creates them
on first publish.

Without `--include-first-dm-fixtures` the scenario's fixture filter
returns zero pairs and `Run` exits cleanly with no observations
(`scenario_firstdm.go` line "first-dm pool empty; skipping").

## Recycle behaviour

`firstDMPool.Next` is monotonic: each pair fires exactly once per
run, then the pool is exhausted and `Run` returns cleanly with the
log line `first-dm pool exhausted; exiting cleanly`. With
`--first-dm-recycle` the pool wraps back to the first pair and reuses
pairs — useful for long ramp tests, but the scenario emits a
`first-dm: --first-dm-recycle enabled` warning at startup because on
the second pass each pair will hit the **already-exists** branch in
room-service (see `replyDMExists` in room-service/handler.go), so the
stage=room observation will be much shorter than the first pass.

For comparison-grade measurements, size `--first-dm-pairs` ≥
`--rate × --duration` so the pool is never recycled within the
measured window.

## Per-iteration timeout

`firstDMStageTimeout` is **10s**. Stages that don't complete within
this bound are silently omitted from the histogram for that iteration
— anything slower is interpreted as SUT failure, not measurement
failure, and is surfaced by the absence of histogram observations on
the dashboard. The cap also bounds goroutine accumulation under
sustained load: every in-flight publish has at most a 10s lifetime.

## Tracker bounds

The internal `firstDMTracker` is a FIFO-bounded map of in-flight DM
rooms, capped at **16384 entries**. At 5k rps publishes with the 10s
firstDMStageTimeout the steady-state set is ~50k slots; eviction kicks
in well before tracker memory becomes a concern. Eviction of an entry
whose stages were never recorded simply omits those observations from
the histogram — there is no error metric for "evicted before
completion" because the bound is sized so eviction is observable as a
gap in the histogram count, not silently lost.

## Metrics

- `loadgen_first_dm_lag_seconds{stage="room|subs|e2e"}` —
  per-stage histogram. Buckets: exponential from 1 ms (×2 factor, 14
  buckets → max ~16 s). Defined in
  [`metrics.go`](../../metrics.go) (search for `FirstDMLag`).
- Stages that never land within the 10s timeout do **not** record
  into the histogram. The Sample-count delta across stages
  (`stage=room` vs `stage=e2e`) is the operator's signal for "the
  pipeline is dropping iterations before reaching e2e".

## Required flags

- `--scenario=first-dm`
- `--include-first-dm-fixtures` on the prior `loadgen seed` call (the
  scenario otherwise emits no observations).

## Scenario-specific flags

| Flag | Default | Notes |
|------|---------|-------|
| `--first-dm-recycle` | false | Wrap around the user-pair pool when exhausted. Disable for comparison-grade runs (see [Recycle behaviour](#recycle-behaviour)). |
| `--first-dm-pairs` (seed-time) | 1000 | Number of pairs to provision; only effective with `--include-first-dm-fixtures`. |

## Operational guidance

- **stage=room only** — room-service is slow or saturated. Check
  `chat.room.canonical.{siteID}.create` consumer lag on the local
  JetStream MESSAGES_CANONICAL/ROOMS stream.
- **stage=room fast, stage=subs slow** — room-worker is keeping up
  with canonical inserts but falling behind on per-user
  `SubscriptionUpdate` fan-out. Look at room-worker's per-handler
  latency in the v2 services dashboard.
- **stage=subs fast, stage=e2e slow** — broadcast-worker fan-out to
  `UserRoomEvent` is the slow link. The DM fan-out path is per-
  recipient (see broadcast-worker/handler.go `publishDMEvents`).
  (Note: a previous shape included a separate `stage=persist`
  measurement here keyed off `chat.msg.canonical.{siteID}.created`,
  but message-worker consumes that subject silently with no
  persist-complete event, so the measurement was the
  loadgen→broker→loadgen self-loop and not informative. Removed.)

## See also

- [`scenario_firstdm.go`](../../scenario_firstdm.go) — scenario
  source; doc header describes the four subjects and the tracker
  shape.
- [`preset.go::augmentWithFirstDMFixtures`](../../preset.go) —
  fixture provisioning (User fields populated for room-service
  validation).
- [USAGE.md → first-dm](../../USAGE.md#first-dm)
- CLAUDE.md §6 (MongoDB) — `idgen.BuildDMRoomID(a, b)` is the
  canonical DM room-ID generator.
