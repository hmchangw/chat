# Spec: Reactions — Embedded `MAP<reaction_key, reactor_info>`

*Replace the original embedded `MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>` column on the four message tables with a flat, per-cell map keyed by `(emoji, user_account)`. The whole reactions map is read inline with the message row — no side table, no read-time fan-out.*

---

## 1. Goal

Move reactions to a shape that satisfies all three of:

1. **Per-cell writes** — adding or removing a reaction is one atomic `UPDATE … SET reactions[k] = v` (or `DELETE reactions[k]`). No read-modify-write, no LWT-CAS games.
2. **Inline reads** — reactions ride with the message row; no extra Cassandra round-trip per page.
3. **Self-uniqueness** — the map key encodes `(emoji, user_account)`, so DB-level uniqueness ("one user, one reaction per emoji") falls out of the schema.

The original embedded shape had a triple-nested `FROZEN<SET<FROZEN<UDT>>>` inside the map value — every reaction change rewrote the whole emoji's set and generated range tombstones on the hot message row. The new shape collapses that to a flat `MAP<FROZEN<UDT>, FROZEN<UDT>>` whose cells are independent: each reaction is its own map entry with its own atomic add/remove.

For migration purposes this spec treats the prior side-table design (an earlier iteration of this branch) as if it never landed. The Docker-local inline DDL goes directly from the original v1 shape to the new v3 shape; a single migration `.cql` handles dev keyspaces that still have the v1 column.

## 2. Cassandra Schema

### 2.1 New UDTs

```cql
CREATE TYPE IF NOT EXISTS chat.reaction_key (
  emoji        TEXT,
  user_account TEXT
);

CREATE TYPE IF NOT EXISTS chat.reactor_info (
  user_id       TEXT,
  chinese_name  TEXT,
  english_name  TEXT,
  account       TEXT,
  reacted_at    TIMESTAMP
);
```

`reaction_key` is the map key — Cassandra requires map keys to be `FROZEN`. `reactor_info` rides as the value and is also `FROZEN` because we always set the whole record on add; we never partially update a reactor's display fields.

`reaction_key.user_account` and `reactor_info.account` carry the same value. The duplication is intentional — keeping `account` in the value means the JSON projection returned to clients is a flat `{user, name, account, reacted_at}` record without callers having to re-stitch the key.

### 2.2 New column on the four message tables

```cql
ALTER TABLE chat.messages_by_room        ADD reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;
ALTER TABLE chat.messages_by_id          ADD reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;
ALTER TABLE chat.thread_messages_by_room ADD reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;
ALTER TABLE chat.pinned_messages_by_room ADD reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;
```

Fresh keyspaces get the column directly via the inline `CREATE TABLE` statements (§4). The `ALTER` form above is what the migration `.cql` runs for dev keyspaces upgrading in place.

### 2.3 Write semantics (out of scope for this PR — informative)

```cql
-- Add or replace one reaction
UPDATE messages_by_id
   SET reactions[{emoji: ?, user_account: ?}] = {user_id: ?, chinese_name: ?, english_name: ?, account: ?, reacted_at: ?}
 WHERE message_id = ? AND created_at = ?;

-- Remove one reaction
DELETE reactions[{emoji: ?, user_account: ?}]
  FROM messages_by_id
 WHERE message_id = ? AND created_at = ?;
```

One map-cell write per reaction toggle, in each of the 2–4 mirror tables that hold the message. No row read, no LWT.

## 3. Go Models — `pkg/model/cassandra/`

### 3.1 New UDT carriers

In `pkg/model/cassandra/message.go`, above `Message`:

```go
// ReactionKey is the map-key UDT for Message.Reactions.
type ReactionKey struct {
    Emoji       string `json:"emoji"       cql:"emoji"`
    UserAccount string `json:"userAccount" cql:"user_account"`
}

// ReactorInfo is the map-value UDT for Message.Reactions.
type ReactorInfo struct {
    UserID      string    `json:"userId"      cql:"user_id"`
    ChineseName string    `json:"chineseName" cql:"chinese_name"`
    EnglishName string    `json:"englishName" cql:"english_name"`
    Account     string    `json:"account"     cql:"account"`
    ReactedAt   time.Time `json:"reactedAt"   cql:"reacted_at"`
}
```

No `bson` tags (Cassandra-only carriers, matches existing precedent in this package — see package-level doc).

### 3.2 Updated `Message.Reactions` field

```go
// Reactions: keys are unique (emoji, user_account) pairs; one map cell per reaction.
// Read inline with the message row — no separate hydration step.
Reactions map[ReactionKey]ReactorInfo `json:"reactions,omitempty" cql:"reactions"`
```

The `cql` tag is **restored**. The field is scanned directly by `structScan` like every other column.

JSON serialisation: Go's `encoding/json` cannot use a struct as a map key directly. The wire shape is decided in §6 and implemented via custom `MarshalJSON` / `UnmarshalJSON` on the field's container type.

### 3.3 Round-trip tests

Add to `pkg/model/cassandra/message_test.go`:

- `TestReactionKey_JSONRoundTrip`
- `TestReactorInfo_JSONRoundTrip`
- `TestMessage_Reactions_JSONRoundTrip` — assert a 2-entry `Reactions` map serialises and round-trips correctly (covers whatever wire shape §6 picks).

The existing `MessageReaction` carrier from the side-table iteration is **deleted**.

## 4. DDL Files — `docker-local/cassandra/init/`

**Add:**
- `07-udt-reaction_key.cql` — the new key UDT.
- `08-udt-reactor_info.cql` — the new value UDT.

**Edit (restore the `reactions` column with the new shape):**
- `10-table-messages_by_room.cql`
- `11-table-thread_messages_by_room.cql`
- `12-table-pinned_messages_by_room.cql`
- `13-table-messages_by_id.cql`

Each gains a line:

```cql
reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>,
```

Slotted next to the other rich-content columns (after `visible_to`, before `deleted` — exactly where the original column sat).

**Delete** (artefacts of the side-table iteration that we're forgetting ever existed):
- `14-table-message_reactions.cql`
- `90-migrate-drop-old-reactions-column.cql`

**Add:** `90-migrate-reactions-to-v3.cql` — for dev keyspaces still on the v1 shape (`MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>`):

```cql
CREATE TYPE IF NOT EXISTS chat.reaction_key (emoji TEXT, user_account TEXT);
CREATE TYPE IF NOT EXISTS chat.reactor_info (user_id TEXT, chinese_name TEXT, english_name TEXT, account TEXT, reacted_at TIMESTAMP);

ALTER TABLE chat.messages_by_room        DROP IF EXISTS reactions;
ALTER TABLE chat.messages_by_room        ADD reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;

ALTER TABLE chat.messages_by_id          DROP IF EXISTS reactions;
ALTER TABLE chat.messages_by_id          ADD reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;

ALTER TABLE chat.thread_messages_by_room DROP IF EXISTS reactions;
ALTER TABLE chat.thread_messages_by_room ADD reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;

ALTER TABLE chat.pinned_messages_by_room DROP IF EXISTS reactions;
ALTER TABLE chat.pinned_messages_by_room ADD reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;
```

Idempotent on fresh setups (no column to drop). On re-runs the `ADD reactions` call will fail loudly because the column already exists — acceptable; this script is meant to run once during the upgrade window.

## 5. Schema Docs — `docs/cassandra_message_model.md`

- Restore the `reactions` row on each of the four message-table sections; type column reads `MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>`.
- Drop the "Reaction table" section that the side-table iteration added.
- Add a "Reaction UDTs" subsection documenting `reaction_key` + `reactor_info` with each field's role.
- Keep the existing `MESSAGE_BUCKET_HOURS` paragraph as-is. Reactions sit inside the bucketed tables, so bucketing now naturally applies to reactions too — note this explicitly so a reader doesn't infer otherwise from the side-table-era spec.

## 6. Client API — `docs/client-api.md`

The map cannot serialise as a JSON object directly because the key is a struct. Two viable wire shapes:

- **(a) Array of objects** — `reactions: [{key: {emoji, userAccount}, value: {...}}, …]`. Verbose but JSON-faithful.
- **(b) Flat object keyed by `"<emoji>:<userAccount>"`** — `reactions: {"👍:alice": {...}, …}`. Compact, but the FE must split the composite key.

**Picked: (a) array of objects.** JSON-natural, no string parsing on the FE, the `key` substructure carries semantic information separately from the value. Implemented via custom `MarshalJSON` / `UnmarshalJSON` for the `Message.Reactions` map.

Affected handlers: same six as before (`LoadHistory`, `LoadNextMessages`, `LoadSurroundingMessages`, `GetMessageByID`, `GetThreadMessages`, `GetThreadParentMessages`). Update `docs/client-api.md`'s description of the `reactions` field to the new shape; per CLAUDE.md §5 this update lands in the same PR.

## 7. `history-service` — Read Path

The branch's side-table machinery is **removed**:

- Delete `history-service/internal/cassrepo/message_reactions.go`, `message_reactions_integration_test.go`, `message_reactions_bench_test.go`, `repository_test.go` (the clamp test).
- Delete `history-service/internal/service/reactions.go`, `reactions_test.go`.
- Remove the 6 `hydrateReactions` / `GetReactionsByMessageID` call sites in `messages.go` + `threads.go`.
- Remove the 4 `_HydrateReactionsError` tests in `messages_test.go` and the 2 in `threads_test.go`.
- Remove the 5 `*_HydratesReactions` happy-path tests; the reactions assertions move into the existing per-handler happy-path tests (since reactions now ride with the row, the regular happy-path tests can seed + assert reactions in the same fixture).
- Revert `MessageReader` interface: drop `GetReactionsByMessageID` and `GetReactionsByMessageIDs`.
- Regenerate mocks (`make generate SERVICE=history-service`).
- Revert `NewRepository` to its 3-arg form `(session, bucket, maxBuckets)`. Drop the `reactionsConcurrency` field on `Repository` and the `< 1 → 1` clamp.
- Restore the `reactions` column to `baseColumns` in `messages_by_room.go` and to the column list in `thread_messages.go`.
- Drop `REACTIONS_FETCH_CONCURRENCY` from `internal/config/config.go`.
- Update `cmd/main.go:93` to pass the 3-arg `NewRepository` shape; remove the `cfg.ReactionsFetchConcurrency` reference.

Update the 30+ test sites that pass `(session, bucket, 365, 50)` back to `(session, bucket, 365)`.

After this PR a paged read returns reactions for free — no additional Cassandra round-trips, no errgroup, no concurrency cap.

## 8. Tests

### Unit tests
- Round-trip tests for `ReactionKey`, `ReactorInfo`, `Message.Reactions` JSON marshalling (including the custom `MarshalJSON`/`UnmarshalJSON` for the map-as-array wire shape).
- The existing handler unit tests' happy-path cases seed + assert reactions on the row. No separate `*_HydratesReactions` / `*_HydrateReactionsError` tests needed.

### Integration tests
- Add an integration test that writes a row with a 2-entry reactions map via raw CQL and confirms `GetMessageByID` returns the deserialised map.
- Update existing `*_integration_test.go` row-round-trip tests to seed + assert on the new map shape (the side-table iteration removed those blocks; we re-add them).

### Concurrency / benchmark
- Delete `BenchmarkGetReactionsByMessageIDs` (no longer applicable).
- No fan-out concurrency to test.

Coverage targets unchanged: ≥80% floor / ≥90% on touched code per CLAUDE.md §4.

## 9. `message-worker` — DDL parity

The inline `CREATE TABLE` strings in `message-worker/integration_test.go` need the `reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>` column **and** the two new UDTs (`reaction_key`, `reactor_info`) created above the table DDL. Same for `history-service/internal/cassrepo/integration_test.go` and `internal/service/integration_test.go`. Search regex (`reactions\s+MAP`) catches all four sites.

`message-worker`'s production INSERT statements do not currently set the reactions column (they never did; the column is populated by reaction writes, not message creation). Confirm and leave alone.

## 10. Mocks

`make generate SERVICE=history-service` regenerates `internal/service/mocks/mock_repository.go` after the `MessageReader` interface shrinks. Commit the regenerated file.

## 11. Out of Scope — `addReaction` PR

The reaction-write path remains a separate PR. With the new shape it is **much simpler** than what the in-flight `add-reaction-support` branch currently has. The write path becomes:

```cql
-- add
UPDATE messages_by_id
   SET reactions[{emoji: ?, user_account: ?}] = {user_id: ?, chinese_name: ?, english_name: ?, account: ?, reacted_at: ?}
 WHERE message_id = ? AND created_at = ?;

-- remove
DELETE reactions[{emoji: ?, user_account: ?}]
  FROM messages_by_id
 WHERE message_id = ? AND created_at = ?;
```

Plus the same `UPDATE` / `DELETE` against the 1–3 mirror tables the message lives in (`messages_by_room`, `thread_messages_by_room`, `pinned_messages_by_room` when applicable). One map-cell write per reaction op, per mirror. No LWT-CAS, no read-modify-write.

The canonical event pipeline (broadcast-worker reaction fan-out, notification-worker reaction handling, search-sync-worker skipping the event) stays as the `add-reaction-support` branch has it.

Edit/delete behaviour of the host message:
- **Edit:** reactions are preserved (the field isn't touched by the edit handler).
- **Soft delete:** reactions stay on the row; rendered as "[deleted]" upstream, so they are effectively invisible without any code touching them.
- **Hard delete** (if/when introduced): reactions go away with the row. No cascade needed.

## 12. Clean services (no changes needed)

`broadcast-worker`, `inbox-worker`, `notification-worker`, `room-service`, `room-worker`, `search-service`, `search-sync-worker`, `auth-service`, `mock-user-service`, `pkg/stream`, `pkg/subject` — no reaction references to touch in this PR.

## 13. Rollout

1. Land the new UDTs (`07`, `08`) + the column on the four message tables (10-13) + the migration script (90).
2. Land the Go model changes (`ReactionKey`, `ReactorInfo`, updated `Message.Reactions`, custom JSON marshal).
3. Land the history-service rollback of the side-table read path.
4. Land the test + docs updates.
5. The `add-reaction-support` branch rebases against this; their writer collapses to single-cell map writes.

Acceptance criteria:
- All 6 history handlers return reactions inline on the message rows.
- `make lint`, `make test`, `make test-integration SERVICE=history-service`, `make sast` all green.
- `docs/cassandra_message_model.md` and `docs/client-api.md` updated in the same PR.

Effort estimate: ~1 day. Most of it is mechanical reversal of the side-table iteration plus the new UDT plumbing.

## 14. Decisions Walked Back (for posterity)

This is the **third** iteration of the reactions storage design on this branch. The journey:

- **v1 — Original embedded `MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>`.** Rejected mid-branch because every change to a single reaction required rewriting the whole emoji's set, generating range tombstones on the message row and accumulating read amplification.
- **v2 — Side table `message_reactions((message_id), emoji)`** (the initial direction of this branch). Eliminated write amplification by keeping reactions in one place, but introduced N parallel single-partition reads per page hydration. Workable but added complexity (`hydrateReactions` helper, errgroup, fan-out cap config) and shifted the cost to the read path — wrong direction for a read-heavy workload.
- **v3 — Embedded `MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>`** (this spec). Keeps reactions inline (free read), uses a compound `(emoji, user)` map key so each reaction is its own atomic map cell (no nested frozen collection, no whole-set rewrites). Pays write amplification (mirror tables) but for a low-frequency event that's acceptable; pays zero read amplification. Self-uniqueness falls out of the map-key constraint. Denormalised reactor display info lives in the value so reads are render-ready.

The v2 iteration is being treated as if it never landed for migration purposes — the inline DDL goes straight from v1 to v3, and the migration script handles dev keyspaces still on v1.
