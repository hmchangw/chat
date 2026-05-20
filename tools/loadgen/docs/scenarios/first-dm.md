# first-dm scenario (Phase 3 §3.13)

## Status

**SKELETON — Phase 3.13 follow-up.** The scenario is registered and exposes
the `--first-dm-recycle` flag, the `--include-first-dm-fixtures` /
`--first-dm-pairs` seed knobs, the user-pair pool (`firstDMPool`), and the
`loadgen_first_dm_lag_seconds{stage}` histogram. The tick loop calls
`sendFirstDM` per pair on schedule — but `sendFirstDM` itself is a
placeholder that returns an empty map
([`scenario_firstdm.go:148-151`](../../scenario_firstdm.go)).

Runs against `--scenario=first-dm` therefore produce no histogram
observations. If the dashboard is silent, that is the current implementation
status, not a SUT problem. The rest of this document describes what the
scenario is meant to measure and the wire-up that will replace the stub.

## What it measures

End-to-end latency of the **lazy DM room-create** path: the moment user A
sends their first message to user B (no existing DM room between them) all
the way through to that message being persisted on the canonical pipeline
and visible to user B's clients.

In MongoDB terms this exercises the lazy-create branch of `room-service`'s
DM handler: when no room exists for the `(userA, userB)` tuple,
`idgen.BuildDMRoomID(a, b)` is used to compute a deterministic DM room ID
and the room + subscriptions documents are created on the fly. The scenario
breaks the publish→persist→visible path into four sub-stages so operators
can pinpoint whether room provisioning, subscription fan-out, or canonical
persistence is the slow link.

## Four lag stages

| Stage   | From → To                                                       |
|---------|-----------------------------------------------------------------|
| room    | publishedAt → observer sees room-create OUTBOX event            |
| subs    | room-create → observer sees subscription-create OUTBOX events   |
| persist | subscription-create → canonical message persisted in Cassandra  |
| e2e     | publishedAt → canonical message persisted (cumulative)          |

The four stages map to `loadgen_first_dm_lag_seconds{stage}` histogram
labels. E2E ≈ room + subs + persist (it's the cumulative observation); per-
stage isolation lets ops single out the slow link.

## Pipeline

```
loadgen.tick:
    pair = pool.Next()                         # (userA, userB) from fixture pool
    roomID = idgen.BuildDMRoomID(userA, userB) # deterministic DM room ID
    publishedAt = now()
    publish message(userA → roomID)            # lazy-creates the DM room

room-service:
    room MISSING for tuple (userA, userB) → CREATE room + 2 subscriptions
    → emit room-create + subscription-create OUTBOX events

loadgen observers (per stage):
    on room-create event:        record (roomID, t_room)
    on subscription-create:      record (roomID, t_subs)
    on canonical message persist record (msgID, t_persist)

→ observe per-stage lag against publishedAt + the previous stage timestamp.
```

## Fixture shape

The scenario needs a pool of **unused** user pairs — users who have never
messaged each other so the room-service lazy-create path is exercised
fresh on every tick. Provision the pool via:

```bash
loadgen seed --workload=messages --preset=small \
    --include-first-dm-fixtures \
    --first-dm-pairs=1000
```

`--include-first-dm-fixtures` adds `--first-dm-pairs` (default 1000) pairs
of users prefixed with `loadgen-firstdm-user-` (see
[`preset.go:62-82`](../../preset.go)). No rooms or subscriptions are seeded
for these users — the SUT lazy-creates them on first publish. The seed
guard ensures `MONGO_DB=loadgen*` so fixture rows never land in the
production DB.

Without `--include-first-dm-fixtures` the scenario's fixture filter returns
zero pairs and `Run` exits cleanly with no observations (see
[`scenario_firstdm.go:101-104`](../../scenario_firstdm.go)).

## Recycle behaviour

`firstDMPool.Next` is monotonic: each pair fires exactly once per run, then
the pool is exhausted and `Run` returns cleanly. With `--first-dm-recycle`
the pool wraps back to the first pair and reuses pairs — useful for long
ramp tests, but note that on the second pass each pair will hit the
already-exists branch in room-service (not the lazy-create branch), so the
stage=room observation will be much shorter than the first pass.

For comparison-grade measurements, size `--first-dm-pairs` ≥
`--rate × --duration` so the pool is never recycled within the measured
window.

## Metrics

- `loadgen_first_dm_lag_seconds{stage="room|subs|persist|e2e"}` — per-
  stage histogram. Buckets: exponential from 1 ms (×2 factor, 14 buckets →
  max ~16 s). Defined in
  [`metrics.go:283-287`](../../metrics.go).

No observations land until the `sendFirstDM` follow-up below ships.

## Activation steps — `sendFirstDM` wire-up

When the messaging-pipeline generator exposes a `publishOne`-compatible
function returning the published message ID and a Subscribers handle is
available on `ScenarioDeps`:

1. In `tools/loadgen/scenario_firstdm.go`, replace the placeholder body of
   `sendFirstDM` (currently
   [lines 148-151](../../scenario_firstdm.go)) with:
   - Before publishing, subscribe to the room-create and
     subscription-create OUTBOX subjects for `roomID = idgen.BuildDMRoomID(
     userA, userB)` via `g.deps.Subscribers().Subscribe(...)`. Cleanup the
     subscriptions on return.
   - Capture `publishedAt = time.Now().UTC()` and publish the DM via the
     pipeline publisher to `roomID`.
   - Wait (bounded by ctx) for each stage event, recording per-stage
     timestamps as they arrive.
   - Return a `map[string]time.Duration{"room": …, "subs": …, "persist":
     …, "e2e": …}`.
2. The existing `Run` loop already consumes that map and observes into
   `loadgen_first_dm_lag_seconds{stage}` for each entry — no changes
   needed there.
3. Update this document's Status section to "ACTIVE".

## Required flags

- `--scenario=first-dm`
- `--include-first-dm-fixtures` on the prior `loadgen seed` call (the
  scenario otherwise emits no observations).

## Scenario-specific flags

| Flag | Default | Notes |
|------|---------|-------|
| `--first-dm-recycle` | false | Wrap around the user-pair pool when exhausted. Disable for comparison-grade runs (see [Recycle behaviour](#recycle-behaviour)). |
| `--first-dm-pairs` (seed-time) | 1000 | Number of pairs to provision; only effective with `--include-first-dm-fixtures`. |

## See also

- [`scenario_firstdm.go`](../../scenario_firstdm.go) — scenario source, doc
  header lists the stage definitions and the TODO checklist.
- [`preset.go:62-82`](../../preset.go) — `augmentWithFirstDMFixtures`
  fixture provisioning.
- [USAGE.md → first-dm](../../USAGE.md#first-dm)
- CLAUDE.md §6 (MongoDB) — `idgen.BuildDMRoomID(a, b)` is the canonical DM
  room-ID generator.
