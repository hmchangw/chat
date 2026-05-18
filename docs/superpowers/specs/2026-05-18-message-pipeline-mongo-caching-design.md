# Message Pipeline MongoDB Read Caching

## Summary

Eliminate the per-message MongoDB read amplification in `message-gatekeeper`
and `broadcast-worker` by introducing process-local subscription and
room-metadata caches, and by removing the wasted read-after-write in
`broadcast-worker.FetchAndUpdateRoom`. On warm caches this reduces the
per-message Mongo footprint from three indexed reads + one read-modify-write
to a single plain `UpdateOne`, eliminating the read side of the load entirely.

## Motivation

During loadgen runs, MongoDB CPU pegs at ~100% under sustained publishing.
Behavior is identical across the `small` (10 users / 5 rooms) and `large`
(10 000 users / 1 000 rooms) presets, ruling out dataset-size effects and
pointing instead to per-message work that is intrinsic to the publish path.

The only services that touch Mongo on the hot path in the loadgen compose
are `message-gatekeeper`, `message-worker`, and `broadcast-worker`. (No
`notification-worker`, `search-sync-worker`, or `inbox-worker` runs.) Of
those, `message-worker` persists to Cassandra, not Mongo, on uniform
presets. The full per-message Mongo footprint reduces to:

| Op | Service | Index used | Op shape |
|---|---|---|---|
| `subscriptions.FindOne({roomId, u.account})` | gatekeeper | `{roomId, u.account}` (unique) | Indexed equality; decode full Subscription doc |
| `rooms.FindOne({_id}, {userCount:1})` | gatekeeper | `_id` | Indexed equality; decode one field |
| `rooms.FindOneAndUpdate({_id}, $set lastMsg*)` | broadcast-worker | `_id` | Indexed equality + write + decode full Room doc (read-after-write) |

`mongotop` during a run shows `subscriptions` and `rooms` carrying ~equal
total time, with `subscriptions` 100% read and `rooms` ~50% read / ~50%
write. Decomposed by op:

| Op | % of Mongo CPU |
|---|---|
| `subscriptions.FindOne` (full doc decode) | ~50% |
| `rooms.FindOneAndUpdate` read-portion (full doc decode) | ~13% |
| `rooms.FindOne` (projected userCount) | ~12% |
| `rooms.FindOneAndUpdate` write commit | ~25% |

All three reads return data that changes rarely or not at all per message:

- `subscriptions` is read for `Roles` (used by `canBypassLargeRoomCap`)
  and `User.Account` (logging). Roles change only on admin operations.
- `rooms.userCount` is read solely to enforce the large-room post cap.
  It changes only on member add/remove.
- The read-back from `FetchAndUpdateRoom` returns `Type`, `Name`, `SiteID`,
  `UserCount` — all stable room metadata — purely so the broadcast event
  payload can include them. The "fresh" `lastMsgAt`/`lastMsgId` values
  returned are values the caller just supplied.

There is no missing index, no scan, no query-shape bug. The reads are
intrinsically wasted: the data has already been fetched recently and
will be fetched again hundreds of times per second, unchanged.

This design eliminates all three reads via in-process caches. The
`rooms.FindOneAndUpdate` becomes a plain `UpdateOne` with no read-back,
which by itself removes the ~13% read-portion and a wire round-trip.
The remaining ~25% write commit is left in place as a synchronous,
fully-error-handled write.

## Non-goals

- **Write coalescing / batched lastMessage updates.** The per-message
  `rooms.UpdateOne` stays synchronous. Coalescing requires async error
  handling, graceful-shutdown flush, bounded backpressure, and an explicit
  policy for what happens when a flush fails after the upstream message
  has been ACKed. That is a separate design with non-trivial correctness
  surface. Captured as a follow-up if Mongo CPU after this change is still
  problematic.
- **Moving room `lastMessage` state out of Mongo entirely.** Would
  eliminate the remaining write but introduces a second source of truth
  for room state. Only justified if write CPU dominates after this design
  ships.
- **Active cache invalidation.** v1 is TTL-bounded staleness. A future
  PR may add NATS-driven invalidation messages on `subscription.update`
  / `room.update` events if production staleness windows prove
  problematic.
- **Subscription writes that already exist (mentions, lastSeenAt,
  read-receipts).** Those continue to write to Mongo on their existing
  paths. The cache stores only the gatekeeper-visible projection
  (`Roles`, `Account`); writes that touch other subscription fields do
  not need cache coordination because the cache does not surface those
  fields.
- **Other services that read `subscriptions` or `rooms`.** Only
  `message-gatekeeper` and `broadcast-worker` are on the message-publish
  hot path. `room-service`, `room-worker`, admin RPCs, and
  cross-site federation paths continue to read Mongo directly. They are
  off the hot path and gain nothing meaningful from caching.
- **Replacing or modifying `pkg/roomsubcache`.** That cache solves a
  different problem — caching full member lists for fan-out workers
  (notification-worker, broadcast-worker DM paths). The caches added here
  are point-lookup caches keyed by `(roomID, account)` and `roomID`
  respectively. Different shape, different consumer, different storage
  decision.

## Design

### Cache mechanism

Both caches are **process-local, in-memory, TTL-bounded, capacity-capped**.
Each service replica maintains its own caches.

Process-local was chosen over a shared Valkey cache because:

- **Latency.** Process-local hit is ~100ns; a Valkey hop is ~500μs–1ms
  even on-host. At hot-path rates (1500+ Mongo ops/sec collapsed to
  1500+ cache hits/sec), the latency difference shows up in p99
  publish-to-broadcast tail.
- **Operational simplicity.** No additional dependency for gatekeeper.
  No new failure mode (Valkey unavailable → fall back to Mongo →
  amplification storm). The cached fields tolerate TTL staleness, so
  cross-replica consistency is not required.
- **Different access pattern from `pkg/roomsubcache`.** That library
  caches full member lists (one large blob per room) for fan-out
  workers. Subscription validation here is a point lookup keyed by
  `(roomID, account)`. Sharing storage would mean either an extra
  Valkey round trip or coupling two cache layouts that change for
  independent reasons.

#### Cache implementation

Backed by `github.com/hashicorp/golang-lru/v2`'s `Expirable[K, V]`.
Battle-tested, thread-safe, and provides exactly the semantics we
need: get-or-set with per-entry TTL and capacity-bounded LRU eviction.
~400 LoC, no transitive runtime deps, MPL-2.0.

`go.mod` gains `github.com/hashicorp/golang-lru/v2` as a direct
dependency. This is the only new third-party addition this design
introduces.

### Cache 1: subscription cache in `message-gatekeeper`

**Key:** struct `{roomID string; account string}` (struct keys are
preferred over composed strings — no separator-collision risk, no
allocation per lookup).

**Value:** projected struct containing only fields gatekeeper reads:

```go
type cachedSubscription struct {
    Roles   []model.Role  // for canBypassLargeRoomCap
    Account string        // for log fields and error messages
}
```

Negative results are also cached. A sentinel value (or a
`(value, found)` pair) distinguishes "subscription exists, here are its
fields" from "subscription confirmed absent". Negative caching prevents
amplification when a misbehaving client retries against a room it cannot
post to.

**Defaults:**

- Capacity: 100 000 entries (env: `GATEKEEPER_SUB_CACHE_SIZE`, default
  `100000`). Covers `large` preset's 10 000 subscriptions 10×.
- TTL: `30s` (env: `GATEKEEPER_SUB_CACHE_TTL`, default `30s`).
- **No negative caching.** A `subscriptions.FindOne` miss on the
  `{roomId, u.account}` compound index is a microsecond-scale index
  probe with nothing to fetch — negative-caching it saves negligible
  Mongo CPU while introducing a user-visible "I just joined but can't
  post yet" tail. Every miss falls through to Mongo on every call.

**Behavior:**

```
GetSubscription(account, roomID):
    key = {roomID, account}
    if value, hit := cache.Get(key); hit:
        return value, nil
    sub, err := store.GetSubscription(ctx, account, roomID)
    if err != nil:
        return nil, err  // not cached — neither ErrSubscriptionNotFound nor transient errors
    cache.Put(key, projection(sub), ttl)
    return sub, nil
```

The cache lives in `message-gatekeeper` (only consumer); no new
`pkg/` package is required for the subscription side.

### Cache 2: room metadata cache (shared)

**Key:** `roomID string`.

**Value:** struct with stable room metadata referenced by both services:

```go
type cachedRoomMeta struct {
    Type      model.RoomType
    Name      string
    SiteID    string
    UserCount int
}
```

`Type`, `Name`, `SiteID` are read in `broadcast-worker`'s
`buildRoomEvent`. `UserCount` is read by gatekeeper's large-room cap
check and is also surfaced in the broadcast event payload.

**Defaults:**

- Capacity: 10 000 entries (env: `ROOM_META_CACHE_SIZE`, default
  `10000`). Covers `large` preset's 1 000 rooms 10×.
- TTL: `30s` (env: `ROOM_META_CACHE_TTL`, default `30s`).
- Negative TTL: `5s`. Newly-created rooms (especially via cross-site
  federation) must become reachable on a short window — a longer
  "room not found" cache would feel like a bug for users creating
  rooms during high load. Room-not-found is rare in normal operation
  (gatekeeper's subscription check fails fast and most "no such room"
  requests never reach the room read), so even this short negative
  cache contributes mainly hygiene against pathological retries.

**Lives in a new package** `pkg/roommetacache` so the same type is
instantiated by both services without code duplication. The package
exposes:

```go
type Cache struct { ... }
func New(size int, ttl, negativeTTL time.Duration) *Cache
func (c *Cache) Get(roomID string) (meta, found, hit bool)
func (c *Cache) Put(roomID string, meta cachedRoomMeta)
func (c *Cache) PutNegative(roomID string)
func (c *Cache) Invalidate(roomID string)   // for future use
```

`Invalidate` is included from v1 even though no caller uses it; the
follow-up NATS-driven invalidation work will plug straight in without
needing a package interface change.

### Concurrent-miss handling (singleflight)

Both caches wrap their store fall-through in `golang.org/x/sync/singleflight`
(`golang.org/x/sync` is already a direct dependency).

Justification: at service startup or after a TTL sweep, 100 concurrent
worker goroutines that all miss on the same hot `(roomID, account)`
would each issue an independent `subscriptions.FindOne`. Singleflight
collapses that to one underlying call; the other 99 await the result
and share it. Without singleflight, cold-cache spikes cause exactly the
amplification this design is meant to eliminate.

### Changes to `broadcast-worker`

Replace `FetchAndUpdateRoom` with two store methods:

```go
type Store interface {
    GetRoomMeta(ctx context.Context, roomID string) (*cachedRoomMeta, error)
    UpdateRoomLastMessage(ctx context.Context, roomID, msgID string,
                          msgAt time.Time, mentionAll bool) error
    // ...existing methods
}
```

`UpdateRoomLastMessage`:

```go
fields := bson.M{
    "lastMsgAt": msgAt,
    "lastMsgId": msgID,
    "updatedAt": msgAt,
}
if mentionAll {
    fields["lastMentionAllAt"] = msgAt
}
result, err := roomCol.UpdateOne(ctx, bson.M{"_id": roomID}, bson.M{"$set": fields})
if err != nil {
    return fmt.Errorf("update room %s last message: %w", roomID, err)
}
if result.MatchedCount == 0 {
    return fmt.Errorf("update room %s last message: %w", roomID, mongo.ErrNoDocuments)
}
return nil
```

Error semantics are **identical** to today's `FindOneAndUpdate` path:
the call is synchronous, awaits server ack at the default write concern
(`{w:1}`), and any error propagates up to the JetStream handler, which
NAKs the message for redelivery. The only thing dropped is the decode
of the post-update document — data the caller no longer needs because
it gets stable fields from the room metadata cache instead.

In the handler:

```go
// Before
room, err := h.store.FetchAndUpdateRoom(ctx, msg.RoomID, msg.ID, msg.CreatedAt, resolved.MentionAll)

// After
if err := h.store.UpdateRoomLastMessage(ctx, msg.RoomID, msg.ID, msg.CreatedAt, resolved.MentionAll); err != nil {
    return fmt.Errorf("update room last message: %w", err)
}
room, err := h.roomMeta.GetOrLoad(ctx, msg.RoomID)
if err != nil {
    return fmt.Errorf("get room meta: %w", err)
}
```

`buildRoomEvent` is updated to take `*cachedRoomMeta` rather than
`*model.Room`. The fields it consumes (`Type`, `Name`, `SiteID`,
`UserCount`) are a strict subset.

### Changes to `message-gatekeeper`

1. Wrap `Store.GetSubscription` in the new subscription cache.
2. Wrap `Store.GetRoomUserCount` in the room metadata cache. On miss,
   the underlying `FindOne` is **upgraded** from the current
   `{userCount:1}` projection to a four-field projection
   `{type:1, name:1, siteId:1, userCount:1}` so the cached value is
   useful to both services (broadcast-worker reuses the cache shape
   even though gatekeeper only needs `UserCount` at the call site).
3. **Skip the cap check entirely when the cached subscription is
   bypass-eligible.** `canBypassLargeRoomCap(sub)` is a pure function;
   with `sub` in cache, evaluating it is a slice scan over `Roles`. If
   it returns `true`, gatekeeper does not call `GetRoomUserCount` at
   all. This eliminates the second Mongo read for the bypass-eligible
   majority of senders (owners, admins, bots, thread-reply senders)
   and re-orders today's checks so the cheap predicate runs first.

The wiring lives in `main.go` alongside the existing store
construction. Caches are constructed once at startup and injected into
the handler.

## Architecture

```
+-----------------------+
| message-gatekeeper    |
|                       |   hit: ~100ns
|   GetSubscription ----+--> [sub cache: {roomID, account} -> {Roles, Account}]
|                       |       miss: subscriptions.FindOne (projected)
|                       |       (singleflight on miss)
|                       |
|   GetRoomUserCount ---+--> [room meta cache: roomID -> {Type, Name, SiteID, UserCount}]
|                       |       miss: rooms.FindOne (projected: type, name, siteId, userCount)
+-----------------------+

+-----------------------+
| broadcast-worker      |
|                       |
|   GetRoomMeta --------+--> [room meta cache: separate instance, same shape]
|                       |       miss: rooms.FindOne (projected: type, name, siteId, userCount)
|                       |
|   UpdateRoomLastMessage ----> rooms.UpdateOne ($set lastMsg*)   ← synchronous, awaited
+-----------------------+
```

`pkg/roommetacache` exports the room metadata cache type. The
subscription cache is private to `message-gatekeeper`.

## Testing

Per CLAUDE.md §4: TDD, 80% minimum coverage, table-driven where
multiple input variations exist, no shared mutable state between tests,
mocks via `mockgen`.

### Unit tests (`pkg/roommetacache`)

- **Hit and miss bookkeeping.** Counters increment correctly.
- **TTL eviction.** Injectable clock; advance past TTL; assert next
  `Get` returns miss.
- **Negative TTL eviction.** Same, but for the shorter negative TTL.
- **Capacity eviction.** Fill to `cap`; insert one more; assert at
  least one prior entry is no longer present.
- **Concurrent populators.** Many goroutines call `Get` for the same
  key during a miss; assert the underlying loader runs at most once
  (singleflight semantics).
- **`Invalidate` removes both positive and negative entries.**

### Unit tests (`message-gatekeeper`)

- **Subscription cache hit / miss / negative.** Counting stub store;
  assert cache-hit calls touch the store zero times; miss exactly once;
  negative miss exactly once, then never again until TTL.
- **`canBypassLargeRoomCap` short-circuits `GetRoomUserCount`.**
  Cached bypass-eligible subscription; assert the handler does not
  call `GetRoomUserCount`.
- **Non-bypass path reads userCount from cache.** Assert one Mongo
  fetch per `roomID` regardless of message count.
- **Error from underlying store is not cached.** Stub returns
  transient error; next call still hits the store.
- **Table-driven coverage of the existing handler scenarios** — none
  of them should regress; tests that currently observe a Mongo store
  call sequence must be updated to expect cache wrappers.

### Unit tests (`broadcast-worker`)

- **`UpdateRoomLastMessage` happy path.** Assert one `UpdateOne` is
  issued with the expected `$set` document and no `ReturnDocument`
  option.
- **`UpdateRoomLastMessage` returns error when `MatchedCount == 0`.**
- **`UpdateRoomLastMessage` propagates driver errors unchanged.**
- **Handler uses cached room meta for `buildRoomEvent`.** Counting
  stub: second message to the same room issues zero `FindOne` on
  rooms.
- **Mention path (`mentionAll = true`)** writes the
  `lastMentionAllAt` field.

### Integration tests

- **`broadcast-worker/integration_test.go`:** Publish two
  `MESSAGES_CANONICAL` events to the same room; assert
  `lastMsgAt`/`lastMsgId` in Mongo reflect the second message; assert
  the broadcast event payload carries `Type`/`Name`/`SiteID`/
  `UserCount` correctly; assert (via a counting wrapper on the
  collection) that exactly one `FindOne` was issued against `rooms`
  across both messages.
- **`message-gatekeeper/integration_test.go`:** Publish two messages
  from the same `(account, roomID)`; assert exactly one
  `subscriptions.FindOne` was issued.

### Loadgen verification

After implementation, run `make -C tools/loadgen/deploy up` →
`seed PRESET=large` → `run PRESET=large RATE=500 DURATION=60s` and
capture:

- `docker stats mongodb` CPU% (target: drop from ~100% to ≤30%).
- `db.serverStatus().opcounters` delta over the 60s window (target:
  `query` count drops by ~95%; `update` count unchanged).
- `mongotop --rowcount 1 60` output (target: `subscriptions` and
  `rooms.read` time both near zero; only `rooms.write` remains).
- loadgen summary: `final_pending == 0`, no errors, end-to-end p99
  latency not worse than baseline.

The PR description must include before/after numbers from this run.

## Rollout

- **No feature flag.** TTL-bounded read caches are correctness-safe
  for the fields they store (`Roles`, room metadata). Worst case on
  bug: revert.
- **Defaults are conservative.** 60s TTL, 5s negative TTL, 10×
  working-set capacity. Production may dial TTL down to 5–10s if
  fresher role changes are desired.
- **Observability.** Each cache exposes Prometheus metrics:
  - `<name>_hits_total{result="hit|miss|negative"}`
  - `<name>_size`
  - `<name>_evictions_total{reason="ttl|capacity"}`

  Hit ratio is the primary tuning signal.
- **Mock regeneration.** Store interfaces change in both services
  (`FetchAndUpdateRoom` → `UpdateRoomLastMessage` + `GetRoomMeta`).
  Run `make generate SERVICE=broadcast-worker` and
  `make generate SERVICE=message-gatekeeper`.
- **Client API doc.** No client-facing handler changes; `docs/client-api.md`
  does not need updates per CLAUDE.md §5.

## Open questions for review

1. **Scope of `pkg/roommetacache`.** Should the package's public type
   be generic over the value (so a future "user metadata cache" or
   similar can reuse the same plumbing), or keep it specific to room
   metadata for now? Default is specific — generalize when a second
   consumer materializes.
