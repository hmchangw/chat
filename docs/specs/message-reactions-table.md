# Spec: Extract Reactions to a Dedicated `message_reactions` Table

*Move reactions off the embedded `MAP<...>` column on message rows into a side table keyed by `message_id`; hydrate at read time in history-service.*

---

## 1. Goal

Replace the embedded `reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>` column on the four message tables with a dedicated `message_reactions` side table. Reactions are hydrated at read time in `history-service` via parallel single-partition queries (errgroup, token-aware routing). One side table covers both regular messages and thread replies — message IDs are unique across the site, so no separate thread reactions table is needed.

This is a pure refactor: reactions are not yet a live feature (no writer exists anywhere in the repo), no production data, no migration concerns.

## 2. Cassandra Schema

```cql
CREATE TABLE IF NOT EXISTS chat.message_reactions (
  message_id TEXT,
  emoji      TEXT,
  users      SET<FROZEN<"Participant">>,
  PRIMARY KEY ((message_id), emoji)
);
```

Partition per message. Tiny partitions (a few emojis × a few participants each). Reads always single-partition.

## 3. Go Models — `pkg/model/cassandra/`

**`message.go:93`** — keep `Reactions map[string][]Participant` (it's the JSON/in-memory shape returned to clients), but **drop the `cql` tag** since this field no longer maps to a Cassandra column. Populated at hydration time by the repository.

**New `reaction.go`:**

```go
type MessageReactionRow struct {
    MessageID string        `cql:"message_id"`
    Emoji     string        `cql:"emoji"`
    Users     []Participant `cql:"users"`
}
```

Add roundtrip test cases in `pkg/model/cassandra/*_test.go` covering `MessageReactionRow`.

## 4. Cassandra DDL — `docker-local/cassandra/init/`

**Edit (drop the `reactions MAP<...>` column):**

- `10-table-messages_by_room.cql` (current line 20)
- `11-table-thread_messages_by_room.cql` (current line 17)
- `12-table-pinned_messages_by_room.cql` (current line 14)
- `13-table-messages_by_id.cql` (current line 18)

**Create `14-table-message_reactions.cql`** with the DDL from §2.

## 5. Schema Docs — `docs/cassandra_message_model.md`

- Remove the `reactions` row from each of the four message-table sections.
- Add new section "Reaction table" documenting `message_reactions`, the partition-per-message design, and the parallel-fetch read pattern.
- Explicitly note: no bucketing on reactions — partitions are single-message and stay small.

## 6. Client API Docs — `docs/client-api.md`

**No change.** Wire shape `reactions: { "<emoji>": [Participant, ...] }` on every Message stays identical. Hydration is server-side and invisible to clients. Endpoint signatures, error cases, and event types all unchanged.

## 7. `history-service` — Read Path

### 7.1 New repo file: `internal/cassrepo/message_reactions.go`

Two methods on `*Repository`:

```go
// LoadReactionsForMessages fans out parallel single-partition queries via
// errgroup. Each goroutine reads one message's reactions partition directly
// from the replica that owns it (token-aware routing). Returns
// messageID -> emoji -> users. Messages with no reactions are omitted.
func (r *Repository) LoadReactionsForMessages(
    ctx context.Context,
    messageIDs []string,
) (map[string]map[string][]Participant, error)

// LoadReactionsForMessage is the single-message variant used by GetMessageByID.
func (r *Repository) LoadReactionsForMessage(
    ctx context.Context,
    messageID string,
) (map[string][]Participant, error)
```

Per-message query:

```cql
SELECT emoji, users FROM message_reactions WHERE message_id = ?
```

`LoadReactionsForMessages` uses `golang.org/x/sync/errgroup` with a bounded concurrency cap (reuse the service's existing semaphore convention if present; otherwise cap at 50 in-flight). Empty input slice → return empty map without hitting Cassandra.

### 7.2 Remove embedded reads

- `internal/cassrepo/messages_by_room.go:16` — drop `reactions` from `baseColumns`.
- `internal/cassrepo/messages_by_id.go:10` — drop `reactions` from `baseColumns`.
- `internal/cassrepo/thread_messages.go` — drop `reactions` from column list.

### 7.3 Hydrate in service handlers

After page assembly in each of these five handlers, collect the page's message IDs, call the appropriate loader, and attach to each `*model.Message.Reactions`:

- `LoadHistory` (`internal/service/service.go:100`)
- `LoadNextMessages` (`internal/service/service.go:101`)
- `LoadSurroundingMessages` (`internal/service/service.go:102`)
- `GetMessageByID` (`internal/service/service.go:103`) — uses `LoadReactionsForMessage`
- `GetThreadMessages` (`internal/service/service.go:110`)

Empty page → skip the call entirely. Reactions-load error → fail the request (do not return a partial page).

## 8. Tests

### Unit tests (`handler_test.go`)

Table-driven, mock store, per handler:

- happy path with reactions on some messages
- empty reactions across the page
- every message has reactions
- store error on reactions fetch → request fails, no partial response
- empty page → reactions store not called

### Integration tests — `internal/cassrepo/`

- Update `messages_by_room_integration_test.go`, `messages_by_id_integration_test.go`, `thread_messages_integration_test.go` — remove embedded-reactions seed data and assertions.
- Update `internal/service/integration_test.go` — seed reactions into the new table and assert end-to-end hydration across the 5 handlers, including a sparse case (e.g. 10 of 25 messages have reactions).
- New `internal/cassrepo/message_reactions_integration_test.go`:
  - happy path
  - empty
  - sparse
  - concurrent fan-out
  - missing message_id (returns nothing for that id, no error)

Coverage target ≥90% on new code per CLAUDE.md §4.

## 9. `message-worker` — DDL Parity Fix

`message-worker/integration_test.go` (lines 50, 67, 86) — inline `CREATE TABLE` strings still declare `reactions MAP<...>`. Remove so test DDL matches production DDL.

No production code change in `message-worker` (`store_cassandra.go` already doesn't write the column).

## 10. Mocks

Run `make generate SERVICE=history-service` after adding new repo methods, if the handler's store interface is extended.

## 11. Out of Scope — `addReaction` PR

A future PR adds the reaction write handler. Documented here so the writer aligns with this read path:

- **Subject** (proposed): `chat.user.{account}.request.room.{roomID}.{siteID}.msg.reaction.add` / `.remove`
- **Handler location**: most natural fit is `room-service` (already hosts member / read-receipt handlers); a dedicated handler if scope grows.
- **Add write:**
  ```cql
  UPDATE message_reactions SET users = users + {?} WHERE message_id = ? AND emoji = ?
  ```
  No bucket, no `room_id`, no `created_at` lookup needed. Just `message_id` + `emoji` + `Participant`.
- **Remove write:**
  ```cql
  UPDATE message_reactions SET users = users - {?} WHERE message_id = ? AND emoji = ?
  ```
  Follow up with `DELETE FROM message_reactions WHERE message_id = ? AND emoji = ?` if the resulting set is empty (read-after-write, CAS, or accept the empty-row tombstone — decide in that PR).
- **Broadcast** of reaction events to room members → `broadcast-worker` integration, out of scope here.
- **Federation** → outbox event type for cross-site reaction propagation, out of scope here.
- **`docs/client-api.md`** updates land in the addReaction PR (per CLAUDE.md §5).

## 12. Clean services (audited, no changes needed)

`broadcast-worker`, `inbox-worker`, `notification-worker`, `room-service`, `room-worker`, `search-service`, `search-sync-worker`, `auth-service`, `mock-user-service`, `pkg/stream`, `pkg/subject` — zero reaction references today.

## 13. Rollout

1. Land DDL changes (drop column from 4 tables, create `message_reactions`) and `pkg/model/cassandra/` updates.
2. Land `history-service` read-path changes + tests.
3. Land `message-worker` integration test DDL fix in the same PR so CI passes.
4. `addReaction` PR follows independently.

## 14. Decisions Walked Back (for posterity)

- **Bucketed schema (`((room_id, bucket), message_id, emoji)`)** was considered to reduce read-query count from N-per-page to 1–3-per-page. Rejected: simple `((message_id), emoji)` with errgroup parallel reads is idiomatic Cassandra (token-aware routing, not the multi-partition `IN` anti-pattern), p99 difference is ~5ms on an endpoint that already pays 50–150ms for the message walk, and the bucketed variant adds cross-team coordination on `MESSAGE_BUCKET_HOURS`, bucket math in both reader and writer, and wider partitions for no measurable gain.
- **Keep reactions embedded in the message row** was considered as the cheapest path. Rejected because `SET<FROZEN<UDT>>` inside `MAP` accumulates tombstones on every reaction remove, bloats every message-row read with the full reactions map, and forces every reaction write to fan out across 2–4 denormalised message tables (`messages_by_room` + `messages_by_id` + optionally `thread_messages_by_room` + `pinned_messages_by_room`). Isolating reactions into a single side table fixes all three at the cost of one extra parallel read step in history-service.
