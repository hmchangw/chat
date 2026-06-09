# tchat 2.0 → nextgen migration connector — design

## Problem

tchat 2.0 is a Rocket.Chat-based Go system operating across multiple independent
sites (e.g. FOC, TSM). nextgen replaces it with this distributed Go/NATS/Cassandra
architecture. Two migration modes are needed:

1. **One-time bulk migration** — copy all historical data before cutover.
2. **Live connector** — during the parallel-run window, replay tchat 2.0 change
   events into nextgen in real time so both systems stay consistent.

This spec focuses primarily on the **live connector** architecture. Bulk migration
reuses the same routing logic without the Change Stream listener (a batch job drives
the same Stage 2 processor).

## Goal

Replay every write from tchat 2.0 MongoDB into nextgen with correct business logic,
correct cross-site scoping, no duplicate notifications, and no duplicate cross-site
publishing. One connector instance per site, self-contained.

## Source collections

| tchat 2.0 collection | nextgen destination | site filter |
|---|---|---|
| `rocketchat_messages` | Cassandra (via `message-worker`) | `federation.origin` |
| `rocketchat_rooms` | MongoDB `rooms` | `federation.origin` |
| `rocketchat_subscriptions` | MongoDB `subscriptions` | `federation.origin` |
| `tsmc_room_members` | MongoDB `room_members` | `federation.origin` |
| `tsmc_thread_subscriptions` | MongoDB `thread_subscriptions` | `federation.origin` |
| `users` | MongoDB `users` | **no filter** (shadow copies of foreign users must be replicated) |

`federation.origin` is present on all collections. For a site with `siteID=FOC`,
only documents where `federation.origin == "FOC"` are migrated (except `users`).

## Three-phase one-time migration (context)

The live connector runs after the one-time migration completes. For reference, the
one-time migration runs in three phases:

- **Phase 1 (parallel, one pipeline M per site):** Metadata collections (rooms,
  subscriptions, members, thread_subscriptions, users) from tchat 2.0 → nextgen.
  Includes a **Thread Derivation pre-pass**: `$group` aggregation on
  `rocketchat_messages` (with `allowDiskUse:true`) to synthesise `threadRooms`
  (this collection doesn't exist in tchat 2.0).
- **Phase 2 (cross-site sync):** Validate that cross-site room memberships are
  consistent across all sites before proceeding to messages.
- **Phase 3 (parallel, one pipeline D per site):** Message history
  (`rocketchat_messages` → Cassandra).

## Live connector architecture

One connector instance per site. Reads its own tchat 2.0 MongoDB source; writes
into its own nextgen stack. tchat 2.0 already distributed the data — there is no
need for an OUTBOX hop.

```
SOURCE: tchat 2.0 MongoDB
┌─────────────────────────────────────────────────────────────────────────────┐
│  rocketchat_messages · rocketchat_rooms · rocketchat_subscriptions          │
│  tsmc_room_members · tsmc_thread_subscriptions · users                      │
└─────────────────────────────────┬───────────────────────────────────────────┘
                                  │  MongoDB Change Streams (6 watches)
                                  ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  STAGE 1 · CAPTURE                                                          │
│                                                                             │
│  Wraps each change: { collection, op, doc, resumeToken, ts }               │
│  Publishes to MIGRATION_OPLOG_{siteID}                                     │
│  Advances resume token ──────────────────────── ONLY after NATS PubAck    │
└─────────────────────────────────┬───────────────────────────────────────────┘
                                  │
                    ┌─────────────▼─────────────┐
                    │  MIGRATION_OPLOG_{siteID} │  JetStream · durable buffer
                    └─────────────┬─────────────┘
                                  │  durable consumer
                                  ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  STAGE 2 · PROCESSOR                                                        │
│                                                                             │
│  federation.origin filter                                                   │
│    origin == siteID  →  proceed                                             │
│    origin != siteID  →  ack & drop                                          │
│    collection=users  →  skip filter (migrate all shadow copies)            │
└──────────┬──────────────────────────────────────┬───────────────────────────┘
           │                                      │
           ▼ collection=rocketchat_messages        ▼ all other collections
           │                                      │
┌──────────┴──────────────────────┐  ┌───────────┴────────────────────────────┐
│       MESSAGES  BRANCH          │  │        COLLECTIONS  BRANCH             │
├─────────────────────────────────┤  ├────────────────────────────────────────┤
│                                 │  │                                        │
│  op = INSERT                    │  │  rocketchat_rooms                      │
│  │                              │  │    → event_type: room_sync             │
│  ▼ publish                      │  │                                        │
│  MESSAGES_CANONICAL_{siteID}    │  │  rocketchat_subscriptions              │
│  Header: X-Migration: live      │  │  tsmc_room_members                     │
│  │                              │  │    → event_type: member_added          │
│  ├──▶ message-worker            │  │                  member_removed        │
│  │    ├─ Cassandra writes       │  │                                        │
│  │    │   (all mirror tables)   │  │  tsmc_thread_subscriptions             │
│  │    ├─ threadRoom creation    │  │    → event_type:                       │
│  │    ├─ broadcast-worker  ✗    │  │       thread_subscription_upserted     │
│  │    └─ notification-wkr ✗     │  │                                        │
│  │       (X-Migration skips)    │  │  publish to  INBOX_{siteID}            │
│  │                              │  │       │                                │
│  └──▶ search-sync-worker        │  │       ▼                                │
│        Elasticsearch index      │  │  inbox-worker                          │
│                                 │  │    room_sync    → UpsertRoom           │
│  op = UPDATE  (message edited)  │  │    member_added → BulkCreateSubs       │
│  │                              │  │      (computes roles, name,            │
│  ▼  NATS request/reply          │  │       isSubscribed per roomType)       │
│  chat.migration.internal        │  │    member_removed → DeleteSubsByAccts  │
│    .{siteID}.msg.edit           │  │    thread_sub → UpsertThreadSub        │
│  │                              │  │    no notifications                    │
│  ▼  history-service             │  │    no cross-site publishing            │
│     new internal handler        │  │                                        │
│     skips: canModify            │  ├────────────────────────────────────────┤
│             getAccessSince      │  │  collection = users                    │
│     reuses: UpdateMessageContent│  │    direct MongoDB upsert               │
│       ├─ messages_by_id         │  │    (inbox-worker: no user event type)  │
│       ├─ messages_by_room       │  └────────────────────────────────────────┘
│       ├─ thread_messages_by_    │
│       │   thread (if thread)    │
│       ├─ pinned_messages_by_    │
│       │   room   (if pinned)    │
│       └─ at-rest encryption     │
│                                 │
│  op = DELETE  (message deleted) │
│  │                              │
│  ▼  NATS request/reply          │
│  chat.migration.internal        │
│    .{siteID}.msg.delete         │
│  │                              │
│  ▼  history-service             │
│     new internal handler        │
│     skips: canModify            │
│             getAccessSince      │
│     reuses: SoftDeleteMessage   │
│       ├─ LWT: IF deleted!=true  │
│       │   (idempotent replay)   │
│       ├─ 4 mirror table writes  │
│       ├─ CAS tcount decrement   │
│       │   (≤16 retries)         │
│       └─ type='message_removed' │
│          if thread parent       │
└─────────────────────────────────┘

STREAMS NOT USED IN MIGRATION:
  OUTBOX_{siteID}   — removed; each site's connector is self-contained
  ROOMS_{siteID}    — not used
  MESSAGES_{siteID} — not used (MESSAGES_CANONICAL only)
```

## X-Migration: live header

Set on all events published to `MESSAGES_CANONICAL_{siteID}` by the connector.

| Consumer | Behaviour when header present |
|---|---|
| `message-worker` | Normal Cassandra writes + threadRoom creation. Skips any cross-site INBOX publishing. |
| `broadcast-worker` | Skips entirely — no delivery to connected clients. |
| `notification-worker` | Skips entirely — no push/email notifications. |
| `search-sync-worker` | Normal Elasticsearch indexing (search index must be populated). |

## MIGRATION_OPLOG_{siteID} stream

New JetStream stream, created by the connector bootstrap (opt-in via
`BOOTSTRAP_STREAMS=true`). Shape follows the project's existing stream bootstrap
pattern (`pkg/stream/stream.go`).

Message envelope:

```json
{
  "collection": "rocketchat_messages",
  "op": "insert",
  "doc": { ... },
  "resumeToken": "...",
  "ts": 1749456000000
}
```

`op` values: `"insert"`, `"update"`, `"delete"`, `"replace"`.

## Stage 1 · Capture back-pressure

Resume token is advanced **only after the NATS PubAck** from
`MIGRATION_OPLOG_{siteID}`. If Stage 1 crashes before the ack, the Change Stream
resumes from the last committed token and republishes — Stage 2 must be idempotent.

Idempotency guarantees:
- Message inserts: `message-worker` deduplicates on `messageID` (Cassandra LWT).
- Message edits: `UpdateMessageContent` is a pure overwrite.
- Message deletes: `SoftDeleteMessage` uses `IF deleted != true` LWT.
- Room/subscription upserts: inbox-worker uses `UpsertRoom` / `BulkCreateSubscriptions`.

## History-service internal migration handlers

Two new NATS subjects, subscribed in `history-service` via its service queue group:

```
chat.migration.internal.{siteID}.msg.edit
chat.migration.internal.{siteID}.msg.delete
```

These handlers share all Cassandra write logic with the existing user-facing
`EditMessage` / `DeleteMessage` handlers but skip:

- `getAccessSince` (permission window check)
- `canModify` (ownership check)
- canonical event publishing (`publishCanonicalBestEffort`) — the connector does
  not need downstream canonical events for edit/delete; the Cassandra write alone
  is the goal.

Request shapes mirror the existing internal edit/delete request structs. These
subjects are not exposed in `docs/client-api.md` (internal only).

## Collections branch — inbox-worker routing

inbox-worker already handles all required event types for metadata replication:

| inbox-worker event | Maps to tchat 2.0 change | Computed fields applied |
|---|---|---|
| `room_sync` | `rocketchat_rooms` insert/update | — |
| `member_added` | `rocketchat_subscriptions` + `tsmc_room_members` insert | `rolesForType`, `subscriptionName`, `subscriptionIsSubscribed` |
| `member_removed` | `tsmc_room_members` delete | — |
| `thread_subscription_upserted` | `tsmc_thread_subscriptions` insert/update | — |

Publishing to `INBOX_{siteID}` (same-site) is semantically unusual (INBOX normally
receives events from *other* sites), but it is the pragmatic choice: inbox-worker
has all the correct business logic and applies computed fields that the connector
would otherwise need to duplicate.

## Thread Derivation pre-pass (one-time migration only)

tchat 2.0 has no `threadRooms` collection. Before migrating messages in Phase 3,
a pre-pass runs a MongoDB aggregation:

```js
db.rocketchat_messages.aggregate([
  { $match: { "federation.origin": siteID, "tmid": { $exists: false }, "tcount": { $gt: 0 } } },
  { $group: { _id: "$_id", roomId: { $first: "$rid" }, createdAt: { $first: "$ts" } } }
], { allowDiskUse: true })
```

Each result creates a `threadRoom` document in nextgen MongoDB. The live connector
does not need this pre-pass — `message-worker` creates `threadRooms` naturally when
the thread-parent INSERT arrives through `MESSAGES_CANONICAL_{siteID}`.

## Open questions

1. **Archived / closed rooms:** tchat 2.0 has `archived` and `closed` room states.
   Confirm which inbox-worker `room_sync` fields carry these and whether nextgen
   room model has equivalent states.

2. **Room encryption keys:** `tchat2_room_encryption_keys` (if it exists) is not
   yet covered. Needs separate migration path (possibly directly to `room-service`
   MongoDB collection).

3. **File uploads:** Attachments stored in tchat 2.0 object storage are not covered
   by this spec. Likely a separate bulk copy job outside the connector.

4. **Message reactions and read receipts:** Not yet modelled. Determine if these
   live in dedicated collections or embedded in messages.

5. **Replay order for edits/deletes:** If a message is created and then immediately
   edited in tchat 2.0, the connector may receive INSERT and UPDATE out of order
   due to JetStream redelivery. `UpdateMessageContent` must handle the case where
   the message doesn't exist yet (no-op or retry with backoff).

6. **DM rooms:** `BuildDMRoomID` is deterministic — no conflict expected. Confirm
   tchat 2.0 DM room `_id` format for the ID mapping step.

7. **Users — profile fields mapping:** tchat 2.0 user schema differs from nextgen.
   Exact field mapping for direct MongoDB upsert needs to be documented before
   implementation.
