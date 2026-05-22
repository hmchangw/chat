# Spec: Extract Reactions to a Dedicated `message_reactions` Table

*Move reactions off the embedded `MAP<...>` column on message rows into a side table keyed by `message_id`; hydrate at read time in history-service.*

---

## 1. Goal

Replace the embedded `reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>` column on the four message tables with a dedicated `message_reactions` side table. Reactions are hydrated at read time in `history-service` via parallel single-partition queries (errgroup, token-aware routing). One side table covers both regular messages and thread replies — message IDs are unique across the site (20-char base62, ~119 bits entropy via `idgen.GenerateMessageID()`), so no separate thread reactions table is needed.

This is a pure refactor: reactions are not yet a live feature (no writer exists anywhere in the repo), no deployment, no data.

## 2. Cassandra Schema

```cql
CREATE TABLE IF NOT EXISTS chat.message_reactions (
  message_id TEXT,
  emoji      TEXT,
  users      SET<FROZEN<"Participant">>,
  PRIMARY KEY ((message_id), emoji)
) WITH compaction = {'class': 'LeveledCompactionStrategy'};
```

Partition per message. Tiny partitions (a few emojis × a few participants each). Reads always single-partition.

The outer `SET` is unfrozen (supports incremental `users + {?}` / `users - {?}` updates); the inner UDT is frozen because Cassandra requires UDTs inside collections to be frozen. LCS is the right compaction class for many small partitions with point reads.

## 3. Go Models — `pkg/model/cassandra/`

**`message.go:93`** — keep `Reactions map[string][]Participant` (it's the JSON/in-memory shape returned to clients), but **drop the `cql` tag** since this field no longer maps to a Cassandra column. Populated at hydration time by the repository.

Safe because `structScan` at `history-service/internal/cassrepo/utils.go:120` skips fields without a `cql` tag. Update the round-trip cases in `pkg/model/cassandra/message_test.go:185,212` — keep the tests, drop the reactions column from the row literals.

**New `reaction.go`:**

```go
type MessageReactionRow struct {
    MessageID string        `json:"messageId" cql:"message_id"`
    Emoji     string        `json:"emoji"     cql:"emoji"`
    Users     []Participant `json:"users"     cql:"users"`
}
```

No `bson` tag — Cassandra-only carrier, matches `Participant` / `File` / `Card` precedent in `pkg/model/cassandra/`.

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
- Explicitly note: no bucketing on reactions (partitions are single-message and stay small); `created_at` deliberately not in the PK (no time-range queries on reactions).
- Update the `MESSAGE_BUCKET_HOURS` paragraph to clarify it applies to `messages_by_room` / `thread_messages_by_room` only, NOT `message_reactions`.

## 6. Client API Docs — `docs/client-api.md`

Wire shape `reactions: { "<emoji>": [Participant, ...] }` on every Message is unchanged. Object key order is not guaranteed (Go map iteration); existing clients already tolerate this.

Per CLAUDE.md §5 (any PR touching handlers under `chat.user.{account}.request.…` must update `docs/client-api.md` in the same PR), the **implementation PR** that lands the history-service read-path changes must add a one-line note near `client-api.md:949` (the `reactions` field description):

> *"Populated server-side via hydration; never written by clients."*

No endpoint signature, error case, or event type changes.

## 7. `history-service` — Read Path

### 7.1 New repo file: `internal/cassrepo/message_reactions.go`

```go
// ReactionMap is a per-message reactions view: emoji -> users who reacted.
type ReactionMap = map[string][]cassandra.Participant

// GetReactionsByMessageIDs fans out parallel single-partition queries via
// errgroup. Each goroutine reads one message's reactions partition directly
// from the replica that owns it (token-aware routing). Returns
// messageID -> ReactionMap. Messages with no reactions are omitted from
// the returned map (callers treat absence as "no reactions", not as error).
// Empty messageIDs returns an empty map without contacting Cassandra.
// Duplicate IDs in input are deduplicated before fan-out.
// First goroutine error cancels siblings via the errgroup-derived context.
func (r *Repository) GetReactionsByMessageIDs(
    ctx context.Context,
    messageIDs []string,
) (map[string]ReactionMap, error)

// GetReactionsByMessageID is the single-message variant used by
// GetMessageByID. Returns an empty ReactionMap (not nil) when the
// message has no reactions; never returns gocql.ErrNotFound for an
// empty partition.
func (r *Repository) GetReactionsByMessageID(
    ctx context.Context,
    messageID string,
) (ReactionMap, error)
```

Per-message query:

```cql
SELECT emoji, users FROM message_reactions WHERE message_id = ?
```

**Concurrency cap:** configurable via `REACTIONS_FETCH_CONCURRENCY` env var (default `50`). Per-call, not global — combining with `LoadSurroundingMessages`' existing fan-out is well within gocql's per-host stream budget (default `NumConns=2` × ~32K stream-IDs).

**Error wrapping** (CLAUDE.md §3):

- `fmt.Errorf("loading reactions for message %s: %w", id, err)` per goroutine
- `fmt.Errorf("loading reactions for messages: %w", err)` from the fan-out wrapper

Style reference: `messages_by_id.go:41`.

### 7.2 Remove embedded reads

- `internal/cassrepo/messages_by_room.go:16` — drop `reactions` from `baseColumns`.
- `internal/cassrepo/messages_by_id.go:10` — drop `reactions` from `baseColumns`.
- `internal/cassrepo/thread_messages.go` — drop `reactions` from column list.

### 7.3 Hydrate in service handlers

Hydration is centralised in a service-layer helper to avoid duplicating logic across six call sites:

```go
func (s *HistoryService) hydrateReactions(ctx context.Context, msgs []models.Message) error {
    if len(msgs) == 0 {
        return nil
    }
    ids := make([]string, len(msgs))
    for i, m := range msgs {
        ids[i] = m.ID
    }
    reactions, err := s.repo.GetReactionsByMessageIDs(ctx, ids)
    if err != nil {
        return fmt.Errorf("hydrating reactions: %w", err)
    }
    for i := range msgs {
        msgs[i].Reactions = reactions[msgs[i].ID] // nil-safe: missing key = no reactions
    }
    return nil
}
```

Called after page assembly in:

- `LoadHistory` (`internal/service/service.go:100`)
- `LoadNextMessages` (`internal/service/service.go:101`)
- `LoadSurroundingMessages` (`internal/service/service.go:102`)
- `GetMessageByID` (`internal/service/service.go:103`) — calls `GetReactionsByMessageID` directly (skip the helper)
- `GetThreadMessages` (`internal/service/service.go:110`)
- `GetThreadParentMessages` (`internal/service/service.go:111`)

On reactions error → fail the whole request via `natsrouter.ErrInternal("failed to load message history")` (style at `messages.go:102`). Do not return a partial page. Degraded mode (return messages with empty reactions, log + metric) was considered and rejected for v1 to avoid silent UI divergence.

Request ID propagation: child errgroup goroutines inherit `ctx`, so the request ID survives without manual injection. Empty-page short-circuit logs nothing; errors emit slog-JSON with the standard `requestID` field.

## 8. Tests

Follow Red-Green-Refactor per CLAUDE.md §4. Tests below must exist and fail before any implementation code in this PR.

Coverage: **≥80% floor required** (CLAUDE.md §4 hard gate); **≥90% target** on `cassrepo/message_reactions.go` and `service.hydrateReactions`.

### Unit tests (`handler_test.go`) — table-driven, mock store

- happy path: page with reactions on some messages
- happy path: every message has reactions
- happy path: empty reactions across the page (every message hydrates to `reactions: {}`)
- empty page → `mockReader.EXPECT().GetReactionsByMessageIDs(gomock.Any(), gomock.Any()).Times(0)`
- single-message page with reactions (page-size-1 edge)
- single-message page without reactions
- nil and empty `[]string{}` input → returns empty map, no Cassandra hit
- duplicate `messageIDs` in input → deduplicated before fan-out
- store error on reactions fetch → request fails with `ErrInternal`, no partial page
- ctx-cancellation mid-fan-out → returns `ctx.Err()`, no goroutine leak
- partial errgroup failure (1 of N goroutines errors) → all siblings cancelled
- `gocql.ErrNotFound` from a per-message query → treated as empty result, not error

### Integration tests — `internal/cassrepo/`

**Surgical edits to existing tests** — delete only the reactions seed/assert blocks; **keep** the surrounding row-round-trip assertions (~20 other columns):

- `messages_by_room_integration_test.go`
- `messages_by_id_integration_test.go:165-169`
- `thread_messages_integration_test.go:301-305`

**Update `internal/service/integration_test.go`** — seed reactions into the new table and assert end-to-end hydration across the 6 handlers (now including `GetThreadParentMessages`), including a sparse case (10 of 25 messages have reactions).

**New `internal/cassrepo/message_reactions_integration_test.go`:**

- Uses `testutil.CassandraKeyspace(t, "history_service_test")` (matches sibling prefix at `cassrepo/integration_test.go:17`). Helper hashes `t.Name()` internally — do not add per-test suffixes.
- Reuses existing `TestMain` at `cassrepo/main_test.go:11`; do NOT add a duplicate.
- Reuses `setupCassandra(t)` at `integration_test.go:15`, extended to create the new `message_reactions` table.
- Cases: happy / empty / sparse / nil-input / 100-message fan-out under `-race` / ctx-cancellation / per-message timeout / set-order-not-asserted (gocql returns SET as unordered slice — compare as set, not slice).

### Benchmark

Add `BenchmarkGetReactionsByMessageIDs` (50 messages × 5 reactions each) so future refactors have a baseline.

## 9. `message-worker` + `history-service` — DDL Parity Fix

The inline `CREATE TABLE` strings still declare `reactions MAP<...>` in six places across two services. Search regex: `reactions\s+MAP`. Remove from each so test DDL matches production DDL:

- `message-worker/integration_test.go` — `messages_by_room`, `messages_by_id`, `thread_messages_by_room` schemas
- `history-service/internal/cassrepo/integration_test.go` — `setupCassandra` helper
- `history-service/internal/service/integration_test.go` — same helper if locally duplicated

No production code change in `message-worker` (`store_cassandra.go` already doesn't write the column).

## 10. Mocks

Run `make generate SERVICE=history-service`. This regenerates `internal/service/mocks/mock_repository.go` because the new methods are added to the `MessageReader` interface at `internal/service/service.go:18-26`. Commit the regenerated mock file. Run `make lint` afterward — generated signatures sometimes need import additions.

## 11. Out of Scope — `addReaction` PR

A future PR adds the reaction write handler. Documented here so the writer aligns with this read path.

### Subjects (proposed)

- `chat.user.{account}.request.room.{roomID}.{siteID}.msg.reaction.add`
- `chat.user.{account}.request.room.{roomID}.{siteID}.msg.reaction.remove`

### Write CQL

```cql
UPDATE message_reactions SET users = users + {?} WHERE message_id = ? AND emoji = ?
UPDATE message_reactions SET users = users - {?} WHERE message_id = ? AND emoji = ?
```

No bucket, no `room_id`, no `created_at` lookup. Just `message_id` + `emoji` + `Participant`.

### Empty-set cleanup (decided here, not deferred)

After a remove, the writer issues an unconditional follow-up `DELETE FROM message_reactions WHERE message_id = ? AND emoji = ?` if the resulting set has zero elements. We accept the benign race where a concurrent ADD between read and DELETE gets erased — reaction add/remove is user-driven and the user can re-react. LWT (`IF users = {}`) was considered and rejected for performance.

### Message edit / delete

- **Edit:** reactions are preserved across edits. Side table is keyed by `message_id`, which doesn't change on edit, so no special handling.
- **Delete:** the message-delete handler (from #215) must cascade `DELETE FROM message_reactions WHERE message_id = ?` in the same code path. If delete is soft, reactions stay (invisible because the message is gone — no cleanup needed).

### Service touch points

| Service | Changes in addReaction PR |
|---|---|
| `room-service` | Host the `addReaction` / `removeReaction` NATS handlers. |
| `history-service` | Extend the message-delete handler (#215) to cascade-delete reactions. Edit path unchanged. |
| `broadcast-worker` | Subscribe to reaction events and fan out to room members. Pattern mirrors existing message delivery — same WebSocket envelope. |
| `inbox-worker` | Handle inbound reaction events from remote sites' OUTBOX; write to local `message_reactions`. Federation plumbing. |
| `notification-worker` | Product decision (default suggested: no notifications for reactions to avoid notification spam). |

### Unaffected

`message-worker`, `message-gatekeeper`, `search-service`, `search-sync-worker`, `auth-service`.

### Client API doc

`docs/client-api.md` gets the new request/response schemas and event types in the addReaction PR (per CLAUDE.md §5).

## 12. Clean services (audited, no changes needed in this PR)

`broadcast-worker`, `inbox-worker`, `notification-worker`, `room-service`, `room-worker`, `search-service`, `search-sync-worker`, `auth-service`, `mock-user-service`, `pkg/stream`, `pkg/subject` — zero reaction references today.

## 13. Rollout

1. **TDD:** write all tests from §8 first, confirm they fail (Red), then implement (Green), then refactor.
2. Land DDL changes + `pkg/model/cassandra/` updates.
3. Land `history-service` read-path changes + tests.
4. Land `message-worker` + `history-service` integration-test DDL parity fixes in the same PR.
5. `addReaction` PR follows independently.

### Acceptance criteria (definition of done)

- All 6 history handlers hydrate reactions (including `GetThreadParentMessages`).
- Integration tests cover sparse / empty / full pages, ctx-cancel, `-race` parallelism.
- ≥80% coverage on new code; ≥90% target on `cassrepo/message_reactions.go` and `service.hydrateReactions`.
- `make lint`, `make test`, `make test-integration SERVICE=history-service`, `make sast` all green.
- `docs/cassandra_message_model.md` and `docs/client-api.md` updated in the same PR.

Effort estimate: ~1–2 days for one engineer.

## 14. Decisions Walked Back (for posterity)

- **Bucketed schema (`((room_id, bucket), message_id, emoji)`)** was considered to reduce read-query count from N-per-page to 1–3-per-page. Rejected: simple `((message_id), emoji)` with errgroup parallel reads is idiomatic Cassandra (token-aware routing, not the multi-partition `IN` anti-pattern). The bucketed variant adds cross-team coordination on `MESSAGE_BUCKET_HOURS`, bucket math in both reader and writer, and wider partitions — for a difference measurable only in single-digit ms on an endpoint that already pays much more for the message walk itself.
- **Keep reactions embedded in the message row** was considered as the cheapest path. Rejected for two real reasons: (1) **write amplification** — every reaction add/remove fans out across 2–4 denormalised tables (`messages_by_room` + `messages_by_id` + optionally `thread_messages_by_room` + `pinned_messages_by_room`); the side table consolidates this to a single write target. (2) **Row-size bloat on hot messages** — embedded reactions are returned on every message read, so a reaction-heavy message bloats every history-page response; the side table hydrates on demand. Tombstones are *isolated* to a small partition that's only read on demand, not eliminated.
- **Row-per-reactor schema `PRIMARY KEY ((message_id), emoji, user_id)`** was evaluated. Pros: eliminates the SET entirely (no soft-limit concern for viral messages), single-row tombstones on remove, paging reactors becomes trivial. Cons: 5–20× row-count inflation, reads must aggregate users back into the API's `map<emoji, []user>` shape. Picked the SET-of-frozen-UDT shape for simpler reads. If partitions exceed ~5K elements in practice, migrate to row-per-reactor — schema-only change, hydrator logic stays.
