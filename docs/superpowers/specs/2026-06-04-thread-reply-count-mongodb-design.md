# Thread Reply Count via MongoDB (Remove LWT `tcount`) Design

**Status:** implemented
**Date:** 2026-06-04
**Branch:** `claude/cassandra-loading-impact-hCZuH`

## Problem

The per-reply thread-reply count (`tcount`, shown in the UI as "🧵 N replies") is
maintained on the parent message row in Cassandra using **Lightweight
Transactions (LWT / Paxos)**:

- `message-worker`'s `incrementParentTcount` (`store_cassandra.go`) does a
  `SELECT` + `UPDATE … IF tcount = ?` on **both** `messages_by_id` and
  `messages_by_room` for every thread reply, retrying up to 16 times on CAS
  conflict (`casIncrement`).
- `history-service`'s `decrementParentTcount` (`cassrepo/write.go`) mirrors the
  same LWT pattern on reply soft-delete (`casDecrement`).

LWT is the single most expensive operation Cassandra offers: each conditional
update is a full Paxos round (prepare/read/propose/commit), serialized per
partition, ~4× the coordination of a normal write. On a **hot thread** (a burst
of replies to one parent), CAS conflicts compound and the retry loop amplifies
the load — the textbook Cassandra anti-pattern of a contended counter
maintained via Paxos on a single partition. This is the heaviest *sustained*
write pattern in the system wherever threading is active.

A naive replacement (`$inc`, native Cassandra `COUNTER`, or Valkey `INCR`) is
**not idempotent**, and `message-worker` consumes from JetStream with
at-least-once delivery — redelivery of a reply event would double-count.

## Goals

- Remove both `tcount` LWTs (increment on write, decrement on delete).
- Make `ThreadRoom` in MongoDB the source of truth for the reply count, using an
  idempotent increment that is safe against JetStream redelivery.
- Keep the history read path and the existing delete semantics
  (`isThreadParent` / `message_removed`) byte-for-byte unchanged.
- No new third-party dependencies; follow the codebase's existing
  idempotent-operation idiom (no multi-document transactions).

## Non-Goals

- **No backfill.** Following the partition-bucketing spec precedent
  (`2026-05-05-message-partition-bucketing-design.md`): existing data is not
  migrated. Pre-existing threads `$inc` from absent; older counts may be
  approximate until the thread sees new activity.
- **No read-path change (Approach B rejected).** We do NOT drop `tcount` from
  Cassandra or enrich it from Mongo on read. The read path is the system's
  heaviest Cassandra load; we keep it untouched.
- **Delete gate unchanged.** The `IF deleted != true` LWT in `SoftDeleteMessage`
  stays — it is once-per-delete (not a per-reply burst), so it is not a load
  concern, and it conveniently guarantees the decrement runs exactly once.
- **Edit path untouched** — edits never change `tcount`.
- **No client/wire change.** `tcount` still surfaces identically;
  `docs/client-api.md` is unaffected.

## Design

### Source of truth: `ThreadRoom` (MongoDB)

The count lives on the `thread_rooms` document — one per thread (per parent
message) — which already exists, is created on first reply, and is updated on
every subsequent reply. Cassandra's `tcount` column becomes a **mirror** written
with a plain (non-LWT) `UPDATE`.

### Data model — `pkg/model/threadroom.go`

```go
ReplyCount     int      `json:"replyCount"     bson:"replyCount"`
CountedReplies []string `json:"countedReplies" bson:"countedReplies"`
```

`CountedReplies` is the single-document idempotency guard: the set of reply IDs
already counted. It is **bounded** via `$slice` to a cap
(`countedRepliesCap = 500`). 500 ≫ the number of replies that can plausibly
arrive within a JetStream redelivery window (seconds–minutes), which is all the
guard must remember. A reply ID is ~20 chars, so the field stays small
(~11 KB at cap).

### Idempotency mechanism (single-document guard)

The increment is one atomic single-document update whose filter is conditioned
on the reply ID being absent from `CountedReplies`:

```js
filter: { _id: threadRoomID, countedReplies: { $ne: replyID } }
update: {
  $inc:      { replyCount: 1 },
  $push:     { countedReplies: { $each: [replyID], $slice: -500 } },
  $set:      { lastMsgAt, lastMsgId, updatedAt },
  $addToSet: { replyAccounts: { $each: <accounts> } }
}
options: ReturnDocument = After
```

- First delivery: filter matches → count increments, reply ID recorded, lastMsg
  and replyAccounts updated, all atomically. Returns the updated document.
- Redelivery: `replyID` is already in `countedReplies` → filter misses →
  **no-op**, returns no document. The `lastMsg`/`replyAccounts` updates being
  skipped on redelivery is harmless (they are idempotent and already applied).

`$push` (not `$addToSet`) is used for `countedReplies` so `$slice` can bound it;
the `$ne` filter prevents duplicate pushes within the retained window.
`$addToSet` is still used for the separate `replyAccounts` field.

### Increment path — `message-worker`

The Mongo `$inc` happens where the thread store is already touched in
`handler.go`; the Cassandra LWT is replaced by a plain mirror write.

- **First reply** — `CreateThreadRoom` seeds `ReplyCount: 1`,
  `CountedReplies: [replyID]`. The count is known (1). This also seeds the guard
  so a redelivery of the first reply (which falls through to the subsequent path
  because `CreateThreadRoom` returns `errThreadRoomExists`) is correctly
  deduped.
- **Subsequent reply** — `UpdateThreadRoomLastMessage` becomes the guarded
  `findOneAndUpdate` above, returning `(newCount int, applied bool, err error)`.
- **Mirror** — a new `Store` method
  `UpdateParentTcount(ctx, roomID, parentID, parentCreatedAt, count)` runs a
  plain `UPDATE … SET tcount = ?` on **both** `messages_by_id` and
  `messages_by_room` (parent bucket derived from `parentCreatedAt`, as today).
  Called only when the Mongo update applied (i.e. not a redelivery) and
  `ThreadParentMessageCreatedAt` is present.
- **Deleted:** `incrementParentTcount` and `casIncrement` are removed from
  `store_cassandra.go`. `SaveThreadMessage` no longer calls the increment.

The handler threads the new count out of `handleThreadRoomAndSubscriptions`
(both first-reply and subsequent-reply branches) so it can drive the mirror
write after `SaveThreadMessage` persists the reply rows.

### Decrement path — `history-service`

The delete already runs behind the `IF deleted != true` LWT gate in
`SoftDeleteMessage` (`cassrepo/write.go`), which guarantees the post-gate block
runs **exactly once** per real not-deleted→deleted transition. The decrement
therefore needs no dedup of its own.

Because `cassrepo` must stay Mongo-free (existing layering rule), the
orchestration moves up to the **service layer**, which already holds both the
Cassandra repository and `ThreadRoomRepo`:

- `SoftDeleteMessage` stops calling `decrementParentTcount`. Its existing
  `(updatedAt, applied, err)` return already surfaces `applied`; the service
  determines "was a reply" from the input message's `ThreadParentID` (and the
  `threadRoomID` to target from `ThreadRoomID`), so no signature change is
  required there.
- The service-layer `DeleteMessage` handler, only when the delete applied and
  the message was a thread reply:
  1. `ThreadRoomRepo.DecrementReplyCount(ctx, threadRoomID) (int, error)` —
     `findOneAndUpdate({_id, replyCount: {$gt: 0}}, {$inc: {replyCount: -1}}, After)`.
     The `$gt: 0` guard prevents the count going negative.
  2. A plain `UpdateParentTcount` mirror on both Cassandra tables, using the
     returned count.
- **Deleted:** `decrementParentTcount` and `casDecrement` are removed from
  `cassrepo/write.go`.

### Read path & schema — unchanged

No change to any read query, to `pkg/model/cassandra/message.go`, or to the
Cassandra DDL. `tcount` stays on the parent row and is still populated, so
`history-service` reads it inline exactly as today, and the
`isThreadParent := msg.TCount != nil && *msg.TCount > 0` branch in
`SoftDeleteMessage` keeps working.

### Cross-service consistency

Both writers (`message-worker` increment, `history-service` decrement) derive
the value from the same Mongo source of truth, serialized by the guarded
`$inc`/`$inc:-1`. Cassandra is only ever a mirror. Under concurrent replies to
the same parent, last-write-wins can momentarily land an older value (transient
off-by-one), which self-heals on the next reply or delete. This is acceptable
for a display badge.

## Testing Strategy

Per CLAUDE.md Section 4 — TDD (Red-Green-Refactor), 80% minimum coverage,
table-driven where applicable, `-race` always.

**Unit — `message-worker` (mocked store):**
- Guarded `findOneAndUpdate` builds the expected filter (`countedReplies $ne`)
  and update (`$inc`, `$push`+`$slice`, `$set`, `$addToSet`).
- Redelivery (no document returned) → `UpdateParentTcount` is NOT called.
- First-reply path seeds `ReplyCount: 1` and `CountedReplies: [replyID]`.
- `UpdateParentTcount` binds the expected bucket and count on both tables.

**Unit — `history-service` (mocked repos):**
- Decrement invoked only when the delete applied and the message is a reply.
- `DecrementReplyCount` carries the `replyCount > 0` guard.
- Mirror `UpdateParentTcount` receives the returned count for both tables.

**Integration (`//go:build integration`, testcontainers via `pkg/testutil`):**
- First reply → `ReplyCount == 1`.
- N distinct replies → `ReplyCount == N`.
- **Same `replyID` applied twice → `ReplyCount` stays N** (core redelivery
  assertion).
- Reply delete → `N - 1`; delete clamps at 0 (never negative).
- `$slice` cap holds when more than `countedRepliesCap` replies are inserted.
- Cassandra `tcount` mirror equals the Mongo `ReplyCount` after increment and
  after decrement.

## Documentation Updates (in same PR)

- This design doc.
- `docs/client-api.md` — no change required (no wire change).
- No Cassandra schema-mirror updates required (`docs/cassandra_message_model.md`,
  `pkg/model/cassandra/`, `docker-local/cassandra/init/*.cql` all unchanged —
  the `tcount` column is untouched).

## Files Touched

- `pkg/model/threadroom.go` — `ReplyCount`, `CountedReplies` fields.
- `message-worker/store.go` — `ThreadStore.UpdateThreadRoomLastMessage` signature
  (return new count + applied); `Store.UpdateParentTcount`; regenerate mocks.
- `message-worker/store_mongo.go` — guarded `findOneAndUpdate`; `CreateThreadRoom`
  seeds count + guard.
- `message-worker/store_cassandra.go` — add `UpdateParentTcount`; delete
  `incrementParentTcount` + `casIncrement`.
- `message-worker/handler.go` — thread the count through and drive the mirror.
- `history-service/internal/cassrepo/write.go` — drop `decrementParentTcount` +
  `casDecrement` and `SoftDeleteMessage`'s internal decrement call (signature
  unchanged).
- `history-service/internal/mongorepo/threadroom.go` — `DecrementReplyCount`.
- `history-service/internal/cassrepo` — `UpdateParentTcount` (plain mirror
  UPDATE on both tables) for the decrement side.
- `history-service/internal/service/` — `DeleteMessage` orchestrates Mongo
  decrement + Cassandra mirror; regenerate mocks.
- Unit + integration tests across the above.
