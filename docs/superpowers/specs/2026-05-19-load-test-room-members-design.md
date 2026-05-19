# Load test: adding members to rooms

## Goal

Benchmark the add-member path end-to-end on a single site, covering both
sustained throughput and large-room capacity behavior. Reuses the existing
`tools/loadgen` binary, fixtures, metrics server, and Prometheus/Grafana
overlay.

The pipeline under test:

```
client
  → chat.user.{account}.request.room.{roomID}.{siteID}.member.add
  → room-service.handleAddMembers  (auth, capacity, dedup, channel expansion)
  → chat.room.canonical.{siteID}.member.add  (ROOMS stream)
  → room-worker  (resolve, write subscriptions, emit broadcast + outbox)
  → chat.room.{roomID}.event  (member_added RoomEvent)
```

## Non-goals

- Cross-site channel expansion. Single site only.
- CI regression gate. Invoked manually.
- Auth benchmark. Uses shared `backend.creds` like existing loadgen.
- Cross-machine absolute-number comparisons. Within-machine A/B only.

## Architecture

Two new subcommands on `tools/loadgen`, sharing the existing NATS connect,
Mongo/Valkey wiring, metrics server, percentile/CSV writers, signal
handling, and consumer-lag sampler:

```
loadgen members-sustained --preset --rate --duration --warmup \
                           --inject --users-per-add --shape [--csv] [--e2-timeout]
loadgen members-capacity   --preset --target-size \
                           --inject --users-per-add --shape [--max-rate] [--csv] [--e2-timeout]
```

Flag defaults: `--users-per-add=10`, `--shape=users`, `--inject=frontdoor`,
`--warmup=10s`, `--duration=60s`, `--rate=100`, `--e2-timeout=30s`. No
default for `--target-size` (required on `members-capacity`).

The existing `seed` and `teardown` subcommands gain `--workload=messages|members`
(default `messages`, for back-compat) so the same binary can stage either
workload's fixtures. Re-seeding between runs (the chosen sustained-mode
strategy for handling permanent room growth) is `teardown` + `seed` wrapped
in a Makefile target.

## Inject modes

### Frontdoor (`--inject=frontdoor`)

- `nc.PublishRequest(memberAddSubject, replyInbox, payload)` with a single
  subscription on `_INBOX.loadgen.members.>` for reply correlation. Member-add
  uses standard `m.Msg.Respond`, not the bespoke
  `chat.user.{account}.response.{reqID}` pattern that msg-send uses, so the
  reply path differs from the existing loadgen collector.
- Hits `room-service.handleAddMembers`: subscription lookup, room lookup,
  ChannelRef expansion (local only), `CountNewMembers`, capacity check.
- Reply `{"status":"accepted"}` → **E1**.
- Publishes canonical event → room-worker resolves + writes + broadcasts
  → **E2**.

### Canonical (`--inject=canonical`)

- `js.Publish(chat.room.canonical.{siteID}.member.add, payload)` into the
  ROOMS stream. Skips room-service entirely.
- Room-worker still does its own resolution (`ListNewMembers` re-resolves
  orgs against subscriptions) and the write + broadcast.
- **No E1** — JetStream publish has no reply. Only E2 is recorded. Same
  blind spot as the existing loadgen's canonical mode.

### `shape=channels` incompatibility

Channel expansion lives in `room-service.handleAddMembers`. Once the
canonical event is published, `ChannelRefs` are decorative (preserved
for the sys-message payload only); room-worker uses the flat
`Users` / `Orgs` lists. So `--shape=channels --inject=canonical` would
silently add zero members.

**Decision:** reject this combination at flag-parse time with an explicit
error rather than re-implementing channel expansion in the loadgen.

### `--inject=both`

Not a real mode. Document in the README as "run the same workload twice,
diff the summaries". The flag stays scalar.

## Fixtures & presets

New `MembersPreset` struct, distinct from the message presets:

```go
type MembersPreset struct {
    Name           string
    Users          int       // global user pool
    Rooms          int       // rooms to seed
    BaselineSize   int       // members per room at seed time (incl. owner)
    CandidatePool  int       // unused-but-eligible users tagged per room
    Shape          ShapeMix  // {usersFrac, orgsFrac, channelsFrac}
    Orgs           int       // org pool (zero if shape excludes orgs)
    OrgSize        int       // avg members per org
    SourceChannels int       // pool of channel-refs (zero if shape excludes channels)
}
```

Per seeded room: one owner subscription + `BaselineSize-1` regular member
subscriptions, all drawn deterministically from the global user pool.
The remaining `CandidatePool` users per room are *not* subscribed —
they are the pool the generator draws from. `roomID → []candidateAccount`
lives in the in-memory `MembersFixtures` struct, derived deterministically
from `--seed`, never persisted.

Three new presets:

| preset             | rooms | baseline | candidate pool | use case                                |
|--------------------|-------|----------|----------------|-----------------------------------------|
| `members-small`    | 5     | 10       | 50             | smoke / dev                             |
| `members-medium`   | 100   | 100      | 500            | sustained-throughput default            |
| `members-capacity` | 5     | 0        | 10000          | capacity-growth, fills to MAX_ROOM_SIZE |

Shape mix defaults to `{users:1.0}` for all three presets, overridable
per-run via `--shape=users|orgs|channels|mixed`.

## Generators

### SustainedGenerator

Open-loop ticker at `--rate` req/sec for `--duration`, bounded by the
existing `MaxInFlight` semaphore. Each tick:

1. Pick a room round-robin across the fixture set.
2. Pop K candidates from that room's in-memory pool. If the pool would
   drop below K, mark the room "exhausted" and skip; if all rooms
   exhausted, abort early with `"preset's CandidatePool too small for
   rate × duration × K — re-seed with larger pool"`.
3. Build the `AddMembersRequest` (shape selector picks users / orgs /
   channels). For canonical inject, set `RequesterID` + `RequesterAccount`
   from the seeded owner so the canonical payload matches what room-service
   would have produced.
4. Hand to publisher, tag with correlation ID for E1/E2 join.

### CapacityGenerator

Sequential per room. Each loop:

1. Iterate the preset's room set (5 rooms for `members-capacity`),
   running rooms concurrently so a slow room doesn't gate the others.
2. Pop K candidates, build request, publish, **wait for E2 on this room**
   before the next iteration on the same room. Within-room sequencing is
   what makes "current size" a clean x-axis.
3. Stop a room when it reaches `--target-size` or its pool exhausts.
4. Each completed add records `(roomSize, e1Latency, e2Latency)` into a
   size-bucketed histogram.

`--max-rate` is a safety valve: if set, cap each room's per-second adds
to avoid drowning room-worker during the smaller-room phase. Default
unset (sequential pacing alone).

## Publisher

```go
type MemberPublisher interface {
    Publish(ctx context.Context, requesterAccount, roomID string,
            req *model.AddMembersRequest, corrID string) error
}
```

Two implementations:

- `frontdoorPublisher` — `nc.PublishRequest` with reply inbox.
- `canonicalPublisher` — `js.Publish` to ROOMS stream subject.

The owner subscription is seeded per room regardless of inject mode
(frontdoor requires it for the auth check; canonical doesn't, but
keeping it lets you swap `--inject` between runs against the same
fixture without re-seeding).

## Collector

Mirrors the existing `Collector` shape but with `RecordMemberEvent` that
filters E2 to `member_added` `RoomEvent`s only (broadcast-worker emits
multiple event types per room). Reuses `ComputePercentiles` + CSV
writer.

Capacity mode swaps the single-percentile summary for a size-bucketed
table: `[size_bucket, count, e1_p50, e1_p99, e2_p50, e2_p99]`.

E1/E2 correlation:

- **E1** (frontdoor only) — NATS request/reply already gives a unique
  reply inbox per request; the publisher records the send time when it
  calls `PublishRequest` and the reply handler computes the delta. No
  payload-side correlation ID needed.
- **E2** — room-worker generates the sys-message ID, so the existing
  loadgen's `LastMsgID` trick doesn't apply. Instead, the collector
  keys on `(roomID, sortedAddedAccounts)`. The candidate-pool design
  guarantees disjoint user sets across concurrent requests to the same
  room (each request pops K unique candidates), so this composite key
  is unique without coordination. The broadcast handler decodes
  `RoomEvent.Message.Members` (or whatever field the `member_added`
  payload uses to list added accounts — to be confirmed against
  `pkg/model` during implementation; if not present, the implementation
  plan must add a correlation-friendly field on the broadcast event).

## Observability

New Prometheus metrics on the existing registry:

- `loadgen_member_published_total{phase,inject,shape}`
- `loadgen_member_publish_errors_total{reason=publish|room_service|timeout}`
- `loadgen_member_e1_latency_seconds` (histogram; frontdoor only)
- `loadgen_member_e2_latency_seconds` (histogram)
- `loadgen_member_room_size{room_id}` (gauge; capacity mode only)
- Existing `loadgen_consumer_pending{stream,consumer}` reused, sampled
  against `room-worker` consumer on ROOMS stream.

Summary printer extended with a `MembersSummary` variant:

```
Preset            members-medium
Inject            frontdoor
Shape             users
Target rate       100 req/s
Actual rate       98.7 req/s
Duration          60s (warmup 10s)
Users per add     10
Members added     59220
Errors            publish=0 room_service=2 timeout=0
E1 p50/p95/p99    4.2 / 12.1 / 28.0 ms
E2 p50/p95/p99    9.7 / 31.0 / 78.4 ms
Consumer lag      room-worker final_pending=0
```

Capacity mode adds a per-bucket table and a `room_id, final_size` block.

CSV export for capacity mode adds `room_size` and `inject` columns.

## Error handling

Three classes, distinct labels on `loadgen_member_publish_errors_total`:

- `publish` — NATS or JetStream publish failure → log + count, continue.
- `room_service` — frontdoor reply with non-empty `error` field
  (capacity, dedup, permission) → log first 5 verbatim, count rest.
  Hitting `"room is at maximum capacity"` is the natural stop signal in
  sustained mode if the candidate pool is sized too generously.
- `timeout` — E2 not seen within `--e2-timeout` (default 30s) → count,
  mark missing in collector. In capacity mode this is a hard stop for
  that room (subsequent adds would queue behind a stuck worker).

Generator abort is fatal-but-clean: cancel the run context, drain
in-flight, print the partial summary with an `exhausted` or
`timed_out` flag set. Reuses the existing `signal.NotifyContext` and
2-second reply-drain from `runRun`.

## Testing

Per CLAUDE.md TDD rules (Red → Green → Refactor → Commit, ≥80% coverage):

- `members_test.go` — generator unit tests with stub `MemberPublisher`
  and synthetic clock: rate adherence, candidate-pool exhaustion abort,
  shape selector distribution (chi-squared over N=10000), capacity-mode
  sequential ordering, `--max-rate` cap.
- `collector_member_test.go` — E1/E2 correlation join, size-bucketing
  edges, percentile output, `member_added` filter rejects other
  RoomEvent types.
- `seed_member_test.go` — fixture builder determinism for a given seed,
  candidate pool disjoint from seeded members, owner role present on
  every room.
- `members_integration_test.go` (`//go:build integration`) —
  testcontainers: NATS+JS, Mongo, Valkey, real `room-service` and
  `room-worker`. Seeds `members-small`, runs 5s sustained + a 1-room
  capacity to size 50, asserts zero errors, non-empty E1+E2, final
  Mongo subscription count matches expected.
- Flag validation: `--shape=channels --inject=canonical` rejected at
  parse time; `--users-per-add` ≥ 1; `--target-size` ≤ MAX_ROOM_SIZE.

## File layout

```
tools/loadgen/
  members.go             # MembersPreset, generators, publisher
  members_test.go
  members_collector.go   # E1/E2 join, size buckets, summary printer
  members_collector_test.go
  members_integration_test.go
  seed_members.go        # MembersFixtures builder, Seed/Teardown integration
  seed_members_test.go
  main.go                # +members-sustained, members-capacity subcommands;
                         #  +--workload flag on seed/teardown
  report.go              # +MembersSummary variant
  metrics.go             # +member_* metric families
  deploy/Makefile        # +seed-members, run-members-sustained,
                         #  run-members-capacity, reset-members targets
  README.md              # +members section, presets table, examples
```

Existing files touched: `main.go`, `report.go`, `metrics.go`,
`deploy/Makefile`, `README.md`. New code mostly isolated to
`members*.go` and `seed_members*.go` so the messaging benchmark stays
readable.
