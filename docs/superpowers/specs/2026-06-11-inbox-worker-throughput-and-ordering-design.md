# inbox-worker Throughput & Federation Ordering — Design

**Date:** 2026-06-11
**Branch:** `claude/inbox-worker-performance-a6JX6`
**Status:** Implemented (PR #256)

## Summary

`inbox-worker` is the federated INBOX consumer: it sources cross-site
`OutboxEvent`s from the local INBOX stream (fed from remote OUTBOX streams)
and replicates subscription/room metadata onto the home site. It never
touches room keys.

The aggregate lane carries every federated event from all sites and is
dominated by `subscription_read`/`thread_read` receipts. The original
implementation processed messages one at a time via a sequential
`cons.Consume()` callback, capping throughput at a single Mongo round trip
per message. This design fans processing out across a bounded worker pool
while preserving correctness for the handful of event types that are not
individually order-safe.

The work lands in three layers, each building on the previous:

1. **Throughput** — replace the sequential consumer with the
   high-throughput pull pattern (`Messages()` + `MAX_WORKERS` semaphore +
   `WaitGroup`), matching `message-worker`/`broadcast-worker`.
2. **Order-safety guards** — make the "Group B" writes (`room_sync`,
   `role_updated`, `subscription_mute_toggled`) order-independent with
   monotonic high-water-mark guards, so concurrent processing cannot
   regress state.
3. **Membership serialization (A1)** — route membership events
   (`member_added`/`member_removed`) onto a single FIFO lane, because they
   carry no high-water mark and so cannot be made individually order-safe.

## Goals

1. Remove the one-Mongo-round-trip-at-a-time throughput ceiling on the
   read-receipt path.
2. Keep every concurrently-processed handler idempotent and
   order-independent, so out-of-order federated delivery (federation
   reorder, NAK redelivery, worker-pool interleaving) cannot regress
   replicated state.
3. Return the membership add/remove resurrection race to its pre-fan-out
   baseline without changing the `subscriptions` read contract used by
   other services.
4. Preserve graceful-shutdown semantics: drain in-flight work before
   closing the NATS connection.

## Non-Goals

- A complete fix for the membership resurrection race (see
  [Membership ordering](#membership-ordering-a1)). A1 is a deliberately
  small mitigation, not an elimination.
- Cross-replica ordering. Multiple `inbox-worker` replicas share one
  durable consumer; this design orders events only *within* a single
  instance.
- Changing stream or durable-consumer configuration. The site-scoped
  `FilterSubjects` (`aggregate.>` only) is unchanged.
- Touching room-key replication or any non-INBOX path.

## Two-Lane Consumer

The single INBOX aggregate durable consumer is drained by a pull iterator
(`cons.Messages(PullMaxMessages(2 * MaxWorkers))`). A dispatcher goroutine
inspects each message's subject and routes it to one of two lanes:

| Lane            | Events                                          | Concurrency           | Why                                                                                 |
|-----------------|-------------------------------------------------|-----------------------|-------------------------------------------------------------------------------------|
| **Fan-out**     | read receipts, `role_updated`, `room_sync`, mute/favorite toggles, renames, visibility | up to `MAX_WORKERS`   | Every handler is idempotent and order-safe via a per-document high-water-mark guard (Mongo `$lt`/`$max`/`$setOnInsert`) — see [Order-Safety Guards](#order-safety-guards-group-b). |
| **Membership**  | `member_added`, `member_removed`                | 1 (FIFO)              | No per-document high-water mark; must be applied in arrival order.                   |

```text
                          ┌──────────────► sem (MAX_WORKERS) ──► process()  (fan-out)
iter.Next() ──► dispatch ─┤
                          └──────────────► membershipCh ──► single worker ──► process()  (FIFO)
```

The dispatcher routes via `isMembershipSubject(subj, siteID)`, which
matches the site-scoped `member_added`/`member_removed` aggregate subjects
built from `pkg/subject`. Membership traffic is a tiny fraction of the
lane, so serializing it costs negligible throughput while the read-receipt
path keeps its full `MAX_WORKERS` concurrency.

Both lanes share one `process(msg)` closure and one `WaitGroup`, so a
single drain step covers all in-flight work at shutdown.

### Per-message processing

`process` stamps a request ID from the message headers
(`natsutil.StampRequestID`), invokes `handler.HandleEvent`, and acks/naks:

- **Success** → `Ack`.
- **Permanent failure** (`errcode.IsPermanent`) → log at warn and `Ack`,
  so JetStream stops redelivering a poison message.
- **Transient/infra failure** → log at error and `Nak` for redelivery.

### Configuration

- `MAX_WORKERS` (default `100`) — fan-out lane concurrency, consistent
  with the other high-throughput workers. The iterator prefetches
  `2 × MAX_WORKERS`.

### Graceful shutdown

Order is unchanged in spirit (`pkg/shutdown.Wait`, 25s budget):
`iter.Stop()` → drain both lanes under one `WaitGroup` (with timeout) →
`nc.Drain()` → tracer shutdown → Mongo disconnect. Stopping the iterator
ends the dispatcher loop, which closes `membershipCh`; the membership
worker then drains and exits, and the fan-out goroutines complete under the
shared `WaitGroup`.

## Order-Safety Guards (Group B)

Once events process concurrently, two writes for the same key can land out
of publish order. The read-receipt handler already used a `$lt`
last-seen guard; this extends the same idiom to the remaining mutable
`$set` writes so they are order-independent.

| Handler                          | Write                                       | Guard field         | Rule                                              |
|----------------------------------|---------------------------------------------|---------------------|---------------------------------------------------|
| `room_sync`                      | room metadata `$set`                        | `updatedAt`         | apply only if event `UpdatedAt` > stored          |
| `role_updated`                   | subscription roles `$set`                   | `rolesEventTs`      | apply only if event timestamp > stored            |
| `subscription_mute_toggled`      | subscription `muted` `$set`                 | `muteEventTs`       | apply only if event timestamp > stored            |
| `subscription_favorite_toggled`  | subscription `favorite` `$set`              | `favoriteEventTs`   | apply only if event timestamp > stored            |
| `room_renamed`                   | per-sub `name` `$set` (UpdateMany)          | `nameEventTs`       | apply per sub only if event timestamp > stored    |
| `room_restricted` (visibility)   | per-sub `{restricted, externalAccess, roles}` `$set` (UpdateMany) | `visibilityEventTs` | apply per sub only if event timestamp > stored    |

The guard timestamp is the source event's publish time, threaded from the
event into the store method (e.g. `UpdateSubscriptionMute(..., eventTs)`).
Older or duplicate events are silent no-ops; a genuinely missing
subscription is also a silent no-op (federation race — the user may have
left mid-flight), except `role_updated`, which returns an error so the
event is redelivered until `member_added` lands.

The two room-wide handlers (`room_renamed`, `room_restricted`) are
`UpdateMany` writes; the `$lt` guard lives in the filter so it is evaluated
**per document**. A sub already carrying a newer event is skipped while its
siblings advance, so a partially-applied newer event is never regressed by a
later-arriving stale one.

### No schema migration

The guard fields (`updatedAt`/`rolesEventTs`/`muteEventTs`/`favoriteEventTs`/
`nameEventTs`/`visibilityEventTs`) are seeded lazily: the guard treats a
missing field (`$exists: false`) as "older than any event," so existing
documents accept the first write and adopt the field. No backfill is
required.

## Membership Ordering (A1)

`member_added`/`member_removed` are **not** individually order-safe. A
`member_removed` performs a physical subscription delete that carries no
high-water mark, so a stale `member_added` arriving after a newer
`member_removed` can resurrect a membership the remove had deleted (and the
mirror case can drop a live one). The worker-pool fan-out amplified this
versus the prior sequential consumer.

A1 routes both membership event types onto a single FIFO lane (one
worker), restoring in-arrival-order processing within the instance. This
reverses the amplification the fan-out introduced and returns the race to
its pre-fan-out baseline.

### Why A1 and not a full fix

A1 is a low-blast-radius mitigation, not an elimination. It does **not**:

- order events across replicas that share the durable consumer, or
- defend against federation/NAK-redelivery reorder, where delivery order
  ≠ publish order.

The complete fix — a soft-delete tombstone carrying a membership
high-water mark, honored at every subscription reader — was prototyped and
reviewed but deferred in favor of shipping this lighter mitigation first.
It is the natural follow-up: it would let membership events rejoin the
fan-out lane and would close the cross-replica and redelivery-reorder gaps
that A1 leaves open.

## Testing

- `TestConfig_MaxWorkers` — env default and override.
- `TestIsMembershipSubject` — the lane-routing predicate (membership vs.
  read-receipt subjects, site-scoped).
- Group-B guard unit tests — the handler threads the event timestamp into
  the store call.
- Integration tests per guard — out-of-order, equal-timestamp, and
  newer-applies behavior against a real Mongo.

Local verification: `make lint` (0 issues), `make test` (`-race`, unit),
`go test -tags=integration` compiles. Integration tests run in CI (no
Docker in the build env).

## Risks and Open Questions

- **Residual resurrection race.** A1 does not eliminate it; cross-replica
  and redelivery reorder remain. Tracked by the deferred tombstone fix.
- **Single membership worker as a bottleneck.** Acceptable while
  membership traffic is a small fraction of the lane. If a future load
  profile makes membership a hot path, the tombstone fix (which removes the
  need for the FIFO lane) is the escalation, not widening the lane.
- **Guard-field seeding under mixed fleets.** During a rolling deploy,
  older `inbox-worker` instances without the guards may still issue
  unguarded `$set`s. The window is bounded by the deploy and self-heals
  once all instances carry the guards; no manual step required.
