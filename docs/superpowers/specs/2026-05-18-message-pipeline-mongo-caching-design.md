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

### Why the TTL default is 2 minutes

The TTL is the central tuning knob of this design and the only choice
in it that trades safety for performance. This section records the
analysis behind picking 2 minutes specifically.

#### What TTL trades

**Higher TTL** → higher hit ratio (less Mongo load) AND higher worst-case
staleness for cached fields. **Lower TTL** → fresher data AND more
Mongo refresh load. There is no third axis: the choice is one number
on this curve.

#### Diminishing returns on Mongo CPU

Hit ratio per cache key as a function of TTL × per-key access rate
(`hit_ratio ≈ (rate × TTL) / (rate × TTL + 1)`):

| TTL | Hit ratio at 0.1 req/sec/key | Hit ratio at 1 req/sec/key |
|---|---|---|
| 30s | 75% | 97% |
| 60s | 86% | 98% |
| 2min | 92% | 99.2% |
| 5min | 97% | 99.7% |
| 1hr | 99.7% | 99.97% |

At any production-realistic access rate, the curve is essentially flat
past ~5 minutes. The Mongo-CPU savings from going from 2m to 5m are
real but small; from 5m to 1hr are negligible.

#### Linear growth of the malicious-user abuse window

The staleness concern is bounded by the existing `SubscriptionUpdateEvent`
push path (see Motivation): honest clients reflect role/subscription
changes in their UI within milliseconds, so server-side cache staleness
produces no observable problem for well-behaved users at any TTL.

For malicious or modified clients that ignore the push event, the
abuse window scales linearly with TTL:

| TTL | Approx. messages a demoted abuser can post under stale cap bypass |
|---|---|
| 30s | ~25 |
| 60s | ~50 |
| 2min | ~100 |
| 5min | ~250 |
| 1hr | ~3000 |

(Rough numbers assuming sustained ~0.8 msg/sec from one client; varies
with the cap and rate limits applied elsewhere.)

The "remove from room" escape hatch propagates immediately under this
design (no subscription negative caching → next request re-reads Mongo
and gets `ErrSubscriptionNotFound`). So even in the abuse case, the
TTL window is the failure mode for "demote was the chosen response";
"remove" remains an immediate-effect tool.

#### Choosing the number

Reasonable defenders of three different points on the curve:

- **60s** — optimizes for moderation responsiveness. Mongo win is
  ~86% hit ratio on wide workloads. Best choice if active-moderation
  use cases dominate.
- **5min** — optimizes for Mongo CPU. Hit ratio is near saturation;
  abuse window (~250 msgs) is still bounded and self-healing. Best
  choice for large deployments where Mongo load is the operational
  concern.
- **2min** — middle ground. Captures most of the Mongo win (92% vs
  97%) at half the abuse-window cost (~100 vs ~250 msgs). No strong
  signal toward either pole, so the balanced default wins.

This is a deployment-time choice, not a code-time choice. The env vars
(`GATEKEEPER_SUB_CACHE_TTL`, `ROOM_META_CACHE_TTL`) let operators dial
the number per environment without code changes. A production
deployment that finds 2m too lax can run with 30s; one that finds
Mongo load still high can run with 5m. The 2m default is the starting
point, not the contract.

#### Bounds and ceilings

- **Below ~30s**, the cache is doing measurably less work because
  entries expire faster than typical access cycles for slow-changing
  metadata. Paying Mongo for no UX benefit.
- **Above ~5min**, the moderation-cost curve grows linearly while
  the Mongo-savings curve flattens. Net negative for most operating
  regimes.
- **Hard ceiling at deploy frequency.** Any TTL longer than the time
  between deploys means cached struct projections could outlive a
  schema change. We do not currently version the cache key on
  schema change, so TTLs comparable to deploy cadence (hours+) need
  that mechanism added before going there. 2m is comfortably below
  this ceiling.

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

Negative results are not cached (see "No negative caching" under
Defaults below).

**Defaults:**

- Capacity: 100 000 entries (env: `GATEKEEPER_SUB_CACHE_SIZE`, default
  `100000`). Covers `large` preset's 10 000 subscriptions 10×.
- TTL: `2m` (env: `GATEKEEPER_SUB_CACHE_TTL`, default `2m`).
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
- TTL: `2m` (env: `ROOM_META_CACHE_TTL`, default `2m`).
- **No negative caching.** Same reasoning as the subscription cache:
  a `rooms.FindOne` miss on `_id` is a microsecond-scale index probe;
  the publish path almost never reaches the room read for a
  non-existent room anyway (gatekeeper's subscription check fails
  fast); and on cross-site federation race, dropping the negative
  cache means newly-created rooms become reachable on the very next
  call rather than after a TTL tail. JetStream redelivery bounds the
  per-message cost when broadcast-worker encounters a missing room.

**Lives in a new package** `pkg/roommetacache` so the same type is
instantiated by both services without code duplication. The package
exposes:

```go
type Cache struct { ... }
func New(size int, ttl time.Duration) *Cache
func (c *Cache) Get(roomID string) (meta cachedRoomMeta, hit bool)
func (c *Cache) Put(roomID string, meta cachedRoomMeta)
func (c *Cache) Invalidate(roomID string)   // for future use
```

`Invalidate` is included from v1 even though no caller uses it; a
future NATS-driven invalidation feature plugs straight in without
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
- **Capacity eviction.** Fill to `cap`; insert one more; assert at
  least one prior entry is no longer present.
- **Concurrent populators.** Many goroutines call `Get` for the same
  key during a miss; assert the underlying loader runs at most once
  (singleflight semantics).
- **`Invalidate` removes the entry.**

### Unit tests (`message-gatekeeper`)

- **Subscription cache hit / miss.** Counting stub store; assert
  cache-hit calls touch the store zero times; miss exactly once,
  result cached for subsequent calls; `ErrSubscriptionNotFound`
  returned to the caller but **not** cached (next call re-issues the
  store read).
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
- **Defaults are middle-ground.** 2m TTL on both caches, no negative
  caching, 10× working-set capacity. The 2m TTL is justified in the
  Motivation section by the existing `SubscriptionUpdateEvent` push
  to clients: well-behaved clients reflect role changes within
  milliseconds, so server-side cache staleness produces no observable
  UX problem for honest users and a bounded, self-healing window for
  malicious ones. Production may dial up or down via env vars without
  code changes.
- **Observability.** Each cache exposes Prometheus metrics:
  - `<name>_hits_total{result="hit|miss"}`
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
