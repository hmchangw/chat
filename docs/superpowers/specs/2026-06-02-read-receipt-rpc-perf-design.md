# Read-Receipt RPC Performance — Design

**Date:** 2026-06-02
**Status:** Proposed
**Service:** `room-service` (with a shared extraction touching `broadcast-worker`)

## Problem

The read-receipt RPC (`chat.user.{account}.request.room.{roomID}.{siteID}.message.read-receipt`,
handled by `room-service/handler.go:handleMessageReadReceipt`) is slow under load.

The hot path's cost is the store call `ListReadReceipts` (`room-service/store_mongo.go:924`),
a MongoDB aggregation that, after matching a room's subscriptions by `lastSeenAt`,
performs a per-subscription `$lookup` into the `users` collection to attach
`chineseName`/`engName`:

```
$match { roomId, lastSeenAt: {$gte: since}, "u.account": {$ne: sender} }
$lookup users (let uid=$u._id; pipeline match _id==$$uid; project _id,account,chineseName,engName)
$unwind / $replaceWith / $limit maxRoomSize
```

Two compounding costs:

1. **`$lookup` fan-out.** One inner `users` lookup per matched subscription. For an
   old message in a large room, `lastSeenAt >= since` matches nearly every member,
   so N≈room-size (capped at `maxRoomSize`, default 1000) dependent lookups run per
   call — and each call holds a connection from the default-capped (100) Mongo pool
   for the whole fan-out, so concurrent calls queue on checkout.
2. **Non-covering match index.** The existing `(roomId, lastSeenAt)` index drives the
   `$match` range but does not include `u._id`/`u.account`, forcing a full-document
   **FETCH** per matched subscription before the join even begins.

The names (`chineseName`/`engName`) live only in `users`; the embedded
`subscriptions.u` carries only `{_id, account, isBot}` (`pkg/model/subscription.go:22`),
so the join is structurally required by the current query shape.

## Goals

- Remove the per-subscription `$lookup` from the read-receipt hot path.
- Make the subscriptions-side query index-covered (no FETCH).
- Reuse existing patterns; add no new infrastructure dependency to room-service's hot path.
- Preserve the RPC's response schema and observable behavior exactly.

## Non-Goals

- Active cache invalidation on user rename. **No user-updated event exists**, and no
  production service writes the `users` collection (profile data is ingested
  out-of-band; the only writers are the seed tool and tests). Building an ingestion
  service + event to drive invalidation is out of scope. TTL-bounded staleness is
  accepted, consistent with `pkg/roomsubcache`'s stance and the existing
  `broadcast-worker` user cache.
- Changing read-receipt semantics, request/response schema, or error cases.

## Approach

Two independent, composable changes.

### 1. Lean store query + cached name resolution

**`ListReadReceipts` becomes lean.** Drop `$lookup`/`$unwind`/`$replaceWith`. The query
returns only the matched readers' identity from the subscription document itself:

```
$match { roomId, lastSeenAt: {$gte: since}, "u.account": {$ne: sender} }
$project { _id: 0, account: "$u.account" }     // u.account already embedded
$limit  maxRoomSize
```

Return type shrinks from `[]ReadReceiptRow{UserID,Account,ChineseName,EngName}` to the
matched **accounts** (`[]string`). The `(roomId, lastSeenAt, u.account, u._id)` index
(below) serves this fully covered — no FETCH.

**Name resolution moves to the handler**, via a cached `userstore.UserStore`:

- Handler holds a new dependency `userStore userstore.UserStore` (injected via
  constructor, alongside `store`). In production it is the cache wrapper; in tests it
  is a mock.
- After `ListReadReceipts` returns the reader accounts, the handler calls
  `userStore.FindUsersByAccounts(ctx, accounts)` (already exists,
  `pkg/userstore/userstore.go:47` — one `$in` query projecting
  `{_id, account, engName, chineseName, ...}`), builds the response from the returned
  `model.User` records.
- Accounts present in subscriptions but absent from `users` (deleted users) are
  dropped — identical to today's `$unwind preserveNullAndEmptyArrays:false`.

Worst case (cold cache) collapses N dependent `$lookup`s into **one** batched `$in`
query; warm case resolves from memory.

### 2. Promote the existing user cache to `pkg/usercache`

`broadcast-worker/usercache.go` already implements exactly the needed resolver:
`CachedUserStore` — wraps `userstore.UserStore`, caches `FindUsersByAccounts` results in
an in-process LRU+TTL, delegates `FindUserByID` (`broadcast-worker/usercache.go:24`).

- **Move** `CachedUserStore` (+ its tests) into a new shared package `pkg/usercache`
  (depends on `pkg/userstore`, `pkg/model`). Exported `New(inner, maxSize, ttl)`.
- **Rewire `broadcast-worker`** to import `pkg/usercache` and delete its local copy.
  Its construction (`broadcast-worker/main.go:84`) and `USER_CACHE_SIZE`/`USER_CACHE_TTL`
  env vars are unchanged in behavior.
- **room-service** constructs the same wrapper in `main.go` and injects it into the
  handler. New config (mirroring broadcast-worker):
  - `USER_CACHE_SIZE int  envDefault:"10000"`
  - `USER_CACHE_TTL  time.Duration envDefault:"5m"`
  Cache is enabled when both are positive; otherwise the raw `userstore.UserStore` is
  injected (no caching), preserving correctness.

In-process LRU (not Valkey-shared) is deliberate: it adds no network hop to the hot
path, and under TTL-only invalidation the per-replica cold-start cost is negligible for
rarely-changing directory names.

### 3. Covering index

Add to room-service `EnsureIndexes` (`store_mongo.go`):

```
{ roomId: 1, lastSeenAt: 1, "u.account": 1, "u._id": 1 }
```

This serves the lean `$match` (roomId eq + lastSeenAt range), evaluates the
`u.account != sender` residual on index keys, and covers the `u.account` projection —
**no FETCH**.

The existing `{roomId: 1, lastSeenAt: 1}` index is a strict prefix of the new one and
becomes redundant; **drop it**. It currently also backs
`MinSubscriptionLastSeenByRoomID` (`store_mongo.go:866`, a `FindOne` sort `{lastSeenAt:1}`
projecting `{lastSeenAt:1}`); the new index's `(roomId, lastSeenAt)` prefix serves and
covers that query identically. (Index swap is a sub-decision flagged for review — if
dropping feels risky for a first cut, keep both and remove the old one in a follow-up.)

## Data Flow (after)

```
RPC → handleMessageReadReceipt
  ├─ validate (parallel): GetSubscription ∥ GetMessageRoomAndCreatedAt   [unchanged]
  ├─ store.ListReadReceipts(roomID, since, sender, maxRoomSize) → []account   [lean, covered]
  ├─ userStore.FindUsersByAccounts(accounts) → []model.User    [cache hit: memory; miss: one $in]
  └─ assemble ReadReceiptResponse{Readers:[{userId,account,chineseName,engName}]}  [schema unchanged]
```

## Error Handling

- `ListReadReceipts` error → wrapped `fmt.Errorf("list read receipts: %w", err)` (unchanged).
- `FindUsersByAccounts` error → new wrap `fmt.Errorf("resolve reader names: %w", err)`;
  surfaces as an internal error to the caller, sanitized at the NATS boundary like the
  existing path. No partial/empty success on a resolution failure.
- Cache layer never fails independently of its inner store (it delegates on miss); a
  Mongo error propagates through unchanged.

## Testing (TDD)

Red → Green → Refactor for each unit; ≥80% coverage (target 90% on handler/store).

- **`pkg/usercache`** — move existing `CachedUserStore` tests; every exported function
  covered (LRU eviction, TTL expiry, batch hit/miss split, delegation of `FindUserByID`).
- **`room-service` handler** (`handler_test.go`, mocked `store` + mocked
  `userstore.UserStore`): valid request returns readers; sender mismatch / not-member /
  message-not-found / room-mismatch error paths (unchanged); `ListReadReceipts` store
  error; `FindUsersByAccounts` resolution error; deleted-user-dropped edge; empty reader
  set; account-order independence of resolution.
- **`room-service` store integration** (`integration_test.go`, testcontainers Mongo):
  lean `ListReadReceipts` returns the correct reader accounts honoring
  `lastSeenAt>=since`, `!= sender`, and `limit`; assert the new index exists and (via
  `explain`) that the query is covered (no FETCH).
- **`broadcast-worker`** — adjust imports; existing behavior tests stay green.

## Migration / Ops Notes

- `make generate` after the `RoomStore.ListReadReceipts` signature change (regenerates
  `room-service/mock_store_test.go`); add a generated mock for `userstore.UserStore` in
  room-service tests.
- Index change is online: `EnsureIndexes` creates the covering index at startup; the old
  index drop is idempotent. Rolling deploy is safe (old code tolerates the extra index;
  new code tolerates the old index during the window).
- `docs/client-api.md`: **no change** — request/response schema, errors, and triggered
  events are identical.
- New env vars `USER_CACHE_SIZE`/`USER_CACHE_TTL` added to room-service
  `deploy/docker-compose.yml` (and documented defaults).

## Open Decisions (for review)

1. **Drop the old `(roomId, lastSeenAt)` index now, or keep both for a first cut?**
   Recommendation: drop (it is a strict prefix; nothing else needs the standalone form).
2. **Cache key granularity** — keep the existing per-account caching of
   `FindUsersByAccounts` (recommended; zero change to the proven cache), vs. per-user
   keying. Recommendation: per-account, as-is.
3. **`USER_CACHE_TTL` default** — 5m (mirror broadcast-worker) vs. longer given names
   change rarely. Recommendation: 5m for consistency; revisit with data.
