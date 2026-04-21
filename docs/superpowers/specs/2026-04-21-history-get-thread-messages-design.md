# History Service — `getThreadMessages` Endpoint Design

**Date:** 2026-04-21
**Status:** Draft
**Derived from:** `2026-03-25-refactor-history-service-design.md`

## Summary

Add a NATS request/reply endpoint to `history-service` that returns the thread replies for a given thread-parent message, paginated newest-first. Ports the old Meteor `Messages.find({tmid: tmid}).sort({ts: -1})` query to the current stack: Cassandra as the store, cursor-based pagination matching the other history endpoints, and subscription-gated access via the existing `historySharedSince` helper.

The query targets the existing `thread_messages_by_room` table. The `thread_room_id` clustering column — today hardcoded to `"N/A"` by the writer — becomes the partition slice key, producing an efficient single-slice read with native Cassandra `PageState` pagination. No schema change, no writer change, no other-service change.

## Scope

Covers a single new NATS request/reply endpoint registered by `history-service`: parent-message lookup, subscription access check, cursor-paginated read of `thread_messages_by_room`, and the corresponding request/response types, subject builders, repo method, unit tests, and integration tests.

Out of scope: the `message-worker` / gatekeeper write path that persists thread replies (still writes `thread_room_id = "N/A"`); the future threadRooms-collection / thread-metadata design that will eventually populate `thread_room_id` with a MongoDB ObjectID; schema changes to any Cassandra table; changes to the other three history endpoints; dropping `{roomID}` from history subjects (flagged as a future cleanup, not done here).

## NATS Subject

| Operation | Subject Pattern | Queue Group |
|-----------|-----------------|-------------|
| Get thread messages | `chat.user.{account}.request.room.{roomID}.{siteID}.msg.thread` | `history-service` |

Registered via new builders in `pkg/subject`:

```go
func MsgThreadPattern(siteID string) string {
    return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.thread", siteID)
}

func MsgThreadWildcard(siteID string) string {
    return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.thread", siteID)
}
```

`{roomID}` is present in the pattern purely for consistency with the three existing history endpoints. **The handler does not read it.** The source of truth for the room is the parent message's own `room_id` (see Handler Flow). When a future cleanup drops `{roomID}` from history subjects, this handler requires no changes.

## Request / Response Types

Added to `history-service/internal/models/message.go`:

```go
type GetThreadMessagesRequest struct {
    ThreadMessageID string `json:"threadMessageId"` // parent message ID
    Cursor          string `json:"cursor,omitempty"`
    Limit           int    `json:"limit"`
}

type GetThreadMessagesResponse struct {
    Messages   []Message `json:"messages"`
    NextCursor string    `json:"nextCursor,omitempty"`
    HasNext    bool      `json:"hasNext"`
}
```

- `ThreadMessageID` — required; the message ID of the thread parent (in old terminology, `tmid`).
- `Cursor` — empty for first page; base64-encoded Cassandra `PageState` for subsequent pages. Same encoding used by `LoadNextMessages`.
- `Limit` — default 20, clamped to max 100. Same defaults as other endpoints.
- `Messages` — thread replies only, ordered newest-first. The parent is **not** included; the client already has it.

## Handler Flow

`(s *HistoryService) GetThreadMessages(c *natsrouter.Context, req GetThreadMessagesRequest) (*GetThreadMessagesResponse, error)`:

1. `account := c.Param("account")`. **`roomID` is never read from the subject.**
2. Reject empty `req.ThreadMessageID` with `ErrBadRequest("threadMessageId is required")`.
3. Fetch the parent: `parent, err := s.messages.GetMessageByID(c, req.ThreadMessageID)`. On error: log + `ErrInternal`. On nil: `ErrNotFound("message not found")`.
4. Derive `roomID := parent.RoomID`. All subsequent lookups use this.
5. `accessSince, err := s.getAccessSince(c, account, roomID)` — unchanged helper; returns `ErrForbidden` if the caller isn't subscribed.
6. If `accessSince != nil && parent.CreatedAt.Before(*accessSince)` → `ErrForbidden("thread is outside access window")`. Same gating semantics as `GetMessageByID` and `LoadSurroundingMessages`.
7. `pageReq, err := parsePageRequest(req.Cursor, req.Limit)` — existing helper; default 20, max 100.
8. `page, err := s.messages.GetThreadMessages(c, roomID, parent.ThreadRoomID, pageReq)`. On error: log + `ErrInternal("failed to load thread messages")`.
9. Return `&GetThreadMessagesResponse{Messages: page.Data, NextCursor: page.NextCursor, HasNext: page.HasNext}`.

**Ordering note.** This handler deliberately fetches the parent message *before* running the subscription check, inverting the order used by `LoadSurroundingMessages` and `GetMessageByID`. Rationale: a missing/invalid `threadMessageId` short-circuits to 404 without a Mongo subscription lookup. Trade-off: an attacker that somehow knows or guesses a message-ID can distinguish "this ID exists in some room" (`ErrForbidden`) from "this ID exists nowhere" (`ErrNotFound`) — whereas access-first would return `ErrForbidden` in both cases. This is a mild global-existence oracle, mitigated by message IDs being random UUIDs (not enumerable) and by the oracle revealing nothing about content or room membership. Accepted given the latency win on genuine not-founds. Flagged as a deliberate deviation from the local convention; if the service later standardizes either direction, this handler moves with it.

## Cassandra Repository

New method on `cassrepo.Repository`:

```go
func (r *Repository) GetThreadMessages(
    ctx context.Context, roomID, threadRoomID string, q PageRequest,
) (Page[models.Message], error)
```

Query:

```cql
SELECT <threadMessageColumns>
  FROM thread_messages_by_room
 WHERE room_id = ? AND thread_room_id = ?
 ORDER BY created_at DESC
```

Partition-key + first-clustering-key equality seek. No `ALLOW FILTERING`. `ORDER BY created_at DESC` matches the table's native clustering order. Paginated via the existing `NewQueryBuilder(q).WithCursor(q.Cursor).WithPageSize(q.PageSize).Fetch(...)` pattern so the cursor is the same opaque base64-`PageState` token the other endpoints use.

### Column selection

`thread_messages_by_room` has a different column set from `messages_by_room` (no `tshow`, `tcount`, `thread_parent_created_at`, `pinned_at`, `pinned_by`). A dedicated column list and scan-destination helper sits alongside the existing `baseColumns` / `baseScanDest`:

```go
const threadMessageColumns = "room_id, thread_room_id, created_at, message_id, thread_parent_id, " +
    "sender, target_user, msg, mentions, attachments, file, card, card_action, " +
    "quoted_parent_message, visible_to, unread, reactions, deleted, " +
    "type, sys_msg_data, site_id, edited_at, updated_at"

func threadMessageScanDest(m *models.Message) []any { /* parallel to baseScanDest */ }
```

Columns absent from `thread_messages_by_room` are simply left at the struct's zero values (`TShow=false`, `TCount=0`, `ThreadParentCreatedAt=nil`, `PinnedAt=nil`, `PinnedBy=nil`), which marshals out with `omitempty` so the wire payload stays clean.

### Repository interface extension

`service.MessageRepository` gains one method:

```go
GetThreadMessages(ctx context.Context, roomID, threadRoomID string, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
```

`make generate SERVICE=history-service` regenerates `mocks/mock_repository.go`.

## Handler Registration

`RegisterHandlers` in `history-service/internal/service/service.go` gains one line:

```go
natsrouter.Register(r, subject.MsgThreadPattern(siteID), s.GetThreadMessages)
```

## The `thread_room_id = "N/A"` Situation

The current writer persists thread replies with `thread_room_id = "N/A"`, pending the future threadRooms-collection work that will populate it with a real per-thread identifier (planned to be a MongoDB ObjectID referencing a thread-metadata document; Cassandra is unsuitable for the high-update cadence that metadata requires). This endpoint is designed on the *assumption that `thread_room_id` is populated* on both thread parents and thread replies.

Implications accepted by this design:

- **No defensive branch in the handler.** `parent.ThreadRoomID` flows straight into the Cassandra query. If a parent ever carries `""` or `"N/A"`, the query runs against that literal value and returns whatever rows in the partition happen to share it — almost certainly wrong data with the current writer, which lumps every thread reply in a room into the `"N/A"` bucket. We accept this because the service is not in production and no client is wired to call this endpoint yet.
- **The writer stays out of scope.** `message-worker` (or wherever thread replies are persisted) continues to write `"N/A"` until the threadRooms-collection work lands. That change is a separate spec.
- **Test fixtures and integration seed rows always use real `thread_room_id` values.** No `"N/A"` anywhere in test code. Typical values are deterministic strings (e.g. the parent message ID, or a fixed UUID per test case) — just not the literal sentinel.
- **The DDL comment stays as-is.** `docs/cassandra_message_model.md` and `docker-local/cassandra/init/11-table-thread_messages_by_room.cql` still say `// todo in future, value will come from threadRooms collection, currently it hold a value "N/A"`. That's a statement about the writer, which is still true. The endpoint-side story is documented here in the spec, not in the DDL comment.
- **Forward compatibility is free.** When the threadRooms writer lands and thread replies start carrying real `thread_room_id` values, this endpoint begins returning correct data with zero code changes. No flag flip, no migration path — the handler reads whatever the parent has.

## Access Control

Reuses the two gates already applied across the service:

1. `getAccessSince(account, roomID)` — subscription existence + `historySharedSince` lookup via `SubscriptionRepository`. Non-subscribers receive `ErrForbidden("not subscribed to room")`. `roomID` here is the parent's own `room_id`, not a client-supplied value.
2. `accessSince` window check — if `historySharedSince` is set and the **parent** was created before it, the whole thread is treated as outside the access window and returns `ErrForbidden`. Matches the old Meteor behavior of "if you can see the parent, you can see the thread."

No additional validation of `tshow` / `tcount` on the parent. A message with no replies simply produces an empty page.

## Error Matrix

| Condition | Response | Code |
|-----------|----------|------|
| `ThreadMessageID` empty | `ErrBadRequest` | `bad_request` |
| Parent not found (`GetMessageByID` returns nil) | `ErrNotFound("message not found")` | `not_found` |
| Parent lookup DB error | `ErrInternal("failed to retrieve message")` | — |
| Caller not subscribed to parent's room | `ErrForbidden("not subscribed to room")` | `forbidden` |
| Parent predates caller's `historySharedSince` | `ErrForbidden("thread is outside access window")` | `forbidden` |
| Invalid cursor | `ErrBadRequest("invalid pagination cursor")` | `bad_request` |
| Thread message query error | `ErrInternal("failed to load thread messages")` | — |
| Success (thread with no replies) | `{ messages: [], hasNext: false }` | — |

## Testing

### Unit (`history-service/internal/service/messages_test.go`)

Table-driven, mocked repos, following the existing pattern in the same file. Covers:

| Case | Setup | Expected |
|------|-------|----------|
| Happy path, first page | Parent exists, caller subscribed, no `historySharedSince`, repo returns 3 messages + empty cursor | Messages returned, `hasNext=false`, `nextCursor=""` |
| Happy path, paged | Repo returns 20 messages + non-empty cursor | `hasNext=true`, cursor passed through |
| Cursor continuation | Request with non-empty cursor | Repo called with decoded cursor |
| Empty `ThreadMessageID` | — | `ErrBadRequest` |
| Parent not found | `GetMessageByID` returns `(nil, nil)` | `ErrNotFound`, no `getAccessSince` call |
| Parent lookup repo error | `GetMessageByID` returns error | `ErrInternal`, no `getAccessSince` call |
| Not subscribed | `GetHistorySharedSince` returns `(nil, false, nil)` | `ErrForbidden("not subscribed to room")` |
| `historySharedSince` error | `GetHistorySharedSince` returns error | `ErrInternal` |
| Parent before `accessSince` | Parent `CreatedAt` < `historySharedSince` | `ErrForbidden("thread is outside access window")` |
| Parent at/after `accessSince` | Parent `CreatedAt` >= `historySharedSince` | Success |
| Invalid cursor | Malformed base64 in `Cursor` | `ErrBadRequest("invalid pagination cursor")` |
| Limit defaulting | `Limit = 0` | Repo called with `PageSize = 20` |
| Limit clamping | `Limit = 500` | Repo called with `PageSize = 100` |
| Negative limit | `Limit = -1` | Repo called with `PageSize = 20` |
| Thread query repo error | `GetThreadMessages` returns error | `ErrInternal` |
| Subject `roomID` is never read | Any test | Assert `GetHistorySharedSince` called with `parent.RoomID`, not a subject-derived value |

Every test sets `parent.ThreadRoomID` to a realistic value (e.g. `"tr-test-1"`). No test uses `"N/A"`.

TDD: tests land first, fail, then the handler + repo method are written to make them pass. `make generate SERVICE=history-service` runs before tests once the interface changes.

### Integration (`history-service/internal/cassrepo/integration_test.go`)

Extends the existing testcontainer-based suite. Seed data:

- One room (`room-A`) containing two threads with different `thread_room_id`s (`tr-1` and `tr-2`), each with several replies at distinct `created_at` timestamps.
- A second room (`room-B`) containing a thread with `thread_room_id = tr-1` (same value, different partition) to prove the partition key isolates rooms.

Cases:

| Case | Assertion |
|------|-----------|
| Query `(room-A, tr-1)` | Returns only `tr-1` replies from `room-A`, `created_at DESC` order |
| Query `(room-A, tr-2)` | Returns only `tr-2` replies, no crossover |
| Query `(room-B, tr-1)` | Returns only `room-B`'s `tr-1` replies, not `room-A`'s |
| Query `(room-A, tr-unknown)` | Empty page, `hasNext=false` |
| Pagination | Limit below total → `hasNext=true` and a non-empty cursor; second call with that cursor returns the remainder, no overlap, no gaps |
| Column scan | All populated columns marshal back into `models.Message` correctly (reuse the round-trip assertion style already in the suite) |

No seed row uses `"N/A"` for `thread_room_id`.

## File Change Surface

| File | Change |
|------|--------|
| `pkg/subject/subject.go` | Add `MsgThreadPattern` + `MsgThreadWildcard` |
| `pkg/subject/subject_test.go` | Cover the two new builders |
| `history-service/internal/models/message.go` | Add `GetThreadMessagesRequest` + `GetThreadMessagesResponse` |
| `history-service/internal/service/service.go` | Extend `MessageRepository` interface; register handler |
| `history-service/internal/service/messages.go` | Add `GetThreadMessages` handler |
| `history-service/internal/service/messages_test.go` | Table-driven unit tests |
| `history-service/internal/service/mocks/mock_repository.go` | Regenerated via `make generate SERVICE=history-service` |
| `history-service/internal/cassrepo/repository.go` | Add `threadMessageColumns`, `threadMessageScanDest`, `GetThreadMessages` |
| `history-service/internal/cassrepo/integration_test.go` | Seed + cases above |

No edits to: `message-worker`, `message-gatekeeper`, `broadcast-worker`, `chat-frontend`, `room-service`, `docker-local/**`, `docs/cassandra_message_model.md`.

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Query `thread_messages_by_room` keyed on `thread_room_id` (not `thread_parent_id`) | `thread_room_id` is the first clustering column, so equality is a native slice seek with no `ALLOW FILTERING`. `thread_parent_id` is a plain column and would force a full-partition scan. The table was designed for this access shape — the writer is just lagging. |
| No new table; reuse the existing one | Adding a `thread_messages_by_parent` table was considered. Once we accepted that `thread_room_id` will eventually be populated per the DDL comment, the existing table already supports the query efficiently. No need for denormalization or a second write. |
| No schema / writer / other-service change in this task | Keeps the change atomic and reviewable. The writer is a separate concern tied to the unbuilt threadRooms-collection design. |
| No `"N/A"` defensive check in the handler | Requested. Pre-prod, no real caller. Returning stale/wrong data when the writer hasn't been updated yet is an acceptable failure mode — the spec documents exactly when it becomes correct (the future threadRooms writer). |
| Don't read `{roomID}` from the subject | Requested forward-compat stance. The parent's own `room_id` is the authoritative source. Client-supplied room context is never trusted, and dropping `{roomID}` from the subject later becomes a zero-code change in this handler. |
| Keep `{roomID}` in the subject pattern | Consistency with the other three history endpoints. Dropping it is a cross-cutting cleanup that should touch all four together, not one in isolation. |
| Parent excluded from response | The caller necessarily already has the parent (they sourced the thread ID from it). Returning it again wastes bytes and complicates pagination boundaries. |
| Access check uses parent's `CreatedAt`, not per-reply | Matches the old Meteor semantics: visibility of the thread is gated on the parent. Per-reply filtering would make pagination return inconsistent result sizes and doesn't map to any stored access rule. |
| Fetch parent before subscription lookup | Saves a Mongo round trip on 404s and there's no information-disclosure gap here (subject `roomID` isn't even read). Deliberately diverges from the local convention of access-first; noted in the spec. |
| Cursor-based pagination | Consistent with `LoadNextMessages`; native Cassandra `PageState` is the cheapest and most correct option. The old Meteor `skip/limit` model doesn't survive the translation to Cassandra cleanly. |
| Dedicated `threadMessageColumns` / scan helper rather than reusing `baseColumns` | `thread_messages_by_room` has a strict subset of columns; reusing `baseColumns` would reference columns the table doesn't have and fail at prepare time. Following the existing `baseScanDest` / `messageByIDScanDest` pattern keeps the repo file consistent. |
| Default limit 20, max 100 | Matches `LoadHistory` and `LoadNextMessages` exactly. |
