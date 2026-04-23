# Delete Message — Design

**Service:** `history-service`
**Operation:** synchronous NATS request/reply for soft-deleting a message
**Status:** design — spec in progress

---

## 1. Goal

Add a synchronous soft-delete operation to `history-service` via NATS request/reply. The sender — and only the sender, provided they are currently subscribed to the room — may delete their own message. Deletion is a soft UPDATE (`deleted = true`); message content (`msg`) is retained but the client renders a "[message deleted]" placeholder. A best-effort live event fans out to room subscribers so clients currently viewing the room see the deletion in real time.

---

## 2. Architecture

Single-service design: all logic stays in `history-service`. No changes to `message-gatekeeper`, `message-worker`, or `broadcast-worker`. No changes to the Cassandra schema. No cross-site federation in this PR.

Per-request flow:

```
NATS request → parse → subscription check → load message
  → sender check → Cassandra UPDATE(s) SET deleted=true
  → publish live event to chat.room.{roomID}.event → reply
```

The reply is synchronous; the event publish is best-effort (logged on failure but does not roll back the UPDATE). No content validation step — the request has no editable payload.

This spec assumes the shared scaffolding introduced by the edit spec (`EventPublisher` interface, `canModify` helper, `MessageRepository` interface pattern, `messages_by_id` hydration) has already shipped. Section 9 below lists exactly what is reused.

---

## 3. Authorization Model

Two gates, in order:

1. **Subscription gate** — caller must currently be subscribed to the room. Implemented via the `getAccessSince` helper in `history-service/internal/service/utils.go`. Non-subscribers receive `ErrForbidden("not subscribed to room")` **before** any message lookup. This prevents non-members from probing messageID → roomID mappings via differential error responses.
2. **Sender gate** — after loading the message, assert `msg.Sender.Account == callerAccount` via the shared `canModify` helper. Anyone else (including room owners) receives `ErrForbidden("only the sender can delete")`.

Design notes:

- The subscription check enforces **current** membership, not historical membership. A user removed from the room cannot delete their own past messages.
- The `historySharedSince` time bound returned by `getAccessSince` is **not** enforced. Sender identity is the real gate — a user who left and rejoined can still delete their own pre-rejoin messages.
- Role (`RoleOwner` vs `RoleMember`) is not consulted. Room ownership does not grant a moderation path for deleting other users' messages in this PR.

---

## 4. Message Hydration — Why the Request Only Needs `messageID`

The `DeleteMessageRequest` carries only `MessageID`. All PK components needed to target rows in `messages_by_room`, `thread_messages_by_room`, and `pinned_messages_by_room` (e.g. `room_id`, `created_at`, `thread_room_id`, `pinned_at`, and the discriminator `thread_parent_id`) are **not** supplied by the caller.

Instead, the handler hydrates the full `*models.Message` up-front via `GetMessageByID(ctx, messageID)`, which queries the `messages_by_id` lookup table. That table stores every column and serves as a universal metadata lookup. Its PK is `PRIMARY KEY (message_id, created_at)` with `message_id` as the partition key, so a single-column `WHERE message_id = ?` query returns the unique row.

The repository then reads `msg.RoomID`, `msg.CreatedAt`, `msg.ThreadParentID`, `msg.ThreadRoomID`, and `msg.PinnedAt` from that hydrated struct to construct each downstream table's `UPDATE … WHERE <full PK>` statement.

| Column in `messages_by_id` | Used as PK component in |
|---|---|
| `message_id` | all 4 tables |
| `created_at` | all 4 tables |
| `room_id` | `messages_by_room`, `thread_messages_by_room`, `pinned_messages_by_room` |
| `thread_parent_id` | *discriminator* — decides top-level vs thread reply |
| `thread_room_id` | `thread_messages_by_room` |
| `pinned_at` | `pinned_messages_by_room` (serves as the table's `created_at` PK column for pinned rows) |

This pattern matches the existing `GetMessageByID` convention at `history-service/internal/cassrepo/repository.go:174-189` and requires no new lookup infrastructure.

---

## 5. Data Flow & Error Handling

| Step | Success condition | Failure response |
|------|-------------------|-----------------|
| Subject parse | all required params extracted | `ErrBadRequest` if required params missing |
| Subscription check | `getAccessSince` returns no error | `ErrForbidden("not subscribed to room")` |
| Load message by ID | non-nil `*Message` returned | `ErrNotFound("message not found")` |
| Room-ID match | `msg.RoomID == roomID` from subject | `ErrNotFound("message not found")` (same error — no leak) |
| Sender check | `msg.Sender.Account == account` via `canModify` | `ErrForbidden("only the sender can delete")` |
| Already-deleted short-circuit | if `msg.Deleted == true`, return the current state without issuing further UPDATEs or publishing | — (idempotent no-op success) |
| Cassandra UPDATE | all applicable tables updated with `deleted = true, updated_at = ?` | `ErrInternal("failed to delete message")`; no event published |
| Event publish | event delivered to `chat.room.{roomID}.event` | log warning; still reply success |

Write-path semantics:

- The write path is **best-effort multi-UPDATE, not atomic**. If one table UPDATE succeeds and a subsequent one fails, the caller receives `ErrInternal`. Delete UPDATEs are strictly idempotent on the `deleted` column (a boolean that stays `true` across retries); `updated_at` reflects the last successful write time and advances on each retry.
- Event publish failure after a successful set of UPDATEs is logged as a warning; the reply still indicates success. Clients that missed the live event will observe the deletion on their next history fetch (Cassandra is authoritative).

**Design note on the already-deleted short-circuit:** returning success without re-issuing UPDATEs for an already-deleted message prevents `updated_at` drift on repeated delete calls and avoids publishing duplicate `message_deleted` events. This is a deliberate choice — the alternative (always re-UPDATE and re-publish) is simpler but noisier.

---

## 6. Request, Response, and Event Types

Declared in `history-service/internal/models/message.go`.

```go
type DeleteMessageRequest struct {
    MessageID string `json:"messageId"`
}

type DeleteMessageResponse struct {
    MessageID string `json:"messageId"`
    DeletedAt int64  `json:"deletedAt"` // UTC millis, mirrors the updated_at set by the UPDATE
}

type MessageDeletedEvent struct {
    Type      string `json:"type"`      // "message_deleted"
    Timestamp int64  `json:"timestamp"` // UTC millis, event publish time (per CLAUDE.md convention)
    RoomID    string `json:"roomId"`
    MessageID string `json:"messageId"`
    DeletedBy string `json:"deletedBy"` // actor account (always == message.sender.account under sender-only auth)
    DeletedAt int64  `json:"deletedAt"` // UTC millis, domain time when delete occurred
}
```

`Timestamp` is the event-envelope time (required on every NATS event per CLAUDE.md); `DeletedAt` is the domain time when the delete occurred. Both are populated from a single `time.Now().UTC()` captured in the handler immediately before the Cassandra UPDATE. Since there is no new `deleted_at` column, `DeletedAt` in the response and event is the same value stored in the `updated_at` column.

`DeletedBy` is always equal to `msg.Sender.Account` under sender-only authorization. It is included in the event payload for client rendering convenience (e.g. "deleted by Alice" in audit-style UI) but carries no additional authorization information.

---

## 7. NATS Subject

Request subject pattern added to `pkg/subject/subject.go`, mirroring the existing `MsgHistoryPattern` / `MsgGetPattern` style:

```go
func MsgDeletePattern(siteID string) string {
    return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.delete", siteID)
}
```

Concrete example: `chat.user.alice.request.room.r1.site-a.msg.delete`.

Event fan-out uses the existing `subject.RoomEvent(roomID)` → `chat.room.{roomID}.event`. No new event-subject builder is needed. This is the same subject group and DM rooms already use for `new_message` events, so the frontend's existing subscription path is reused with one additional `evt.type` branch.

---

## 8. Cassandra UPDATE Strategy

Cassandra `UPDATE … WHERE <full PK>` against a missing row is **not** a no-op — it writes a phantom row containing only the updated columns. UPDATEs must therefore be conditional on which tables actually hold the row, decided from the hydrated `*models.Message`'s metadata.

Table membership (verified against `message-worker/store_cassandra.go` — `SaveMessage` writes to `messages_by_room` + `messages_by_id`; `SaveThreadMessage` writes to `messages_by_id` + `thread_messages_by_room`):

| Table | PK | Who's in it | UPDATE when |
|---|---|---|---|
| `messages_by_id` | `(message_id, created_at)` | every message | always |
| `messages_by_room` | `((room_id), created_at, message_id)` | top-level messages only | `msg.ThreadParentID == ""` |
| `thread_messages_by_room` | `((room_id), thread_room_id, created_at, message_id)` | thread replies only | `msg.ThreadParentID != ""`; supply `msg.ThreadRoomID` |
| `pinned_messages_by_room` | `((room_id), created_at=pinnedAt, message_id)` | pinned messages only (no pin operation exists yet) | `msg.PinnedAt != nil`; supply `msg.PinnedAt` |

**NULL handling (gocql):** `ThreadParentID` and `ThreadRoomID` are typed `string` (non-pointer) in `pkg/model/cassandra/message.go`, so gocql maps a NULL Cassandra column to Go zero value `""` during scan. The check `msg.ThreadParentID == ""` therefore matches both NULL and explicitly-empty-string cases. `PinnedAt` is typed `*time.Time` (pointer), so NULL maps to `nil`; the check `msg.PinnedAt != nil` handles both NULL and unset consistently.

The SET clause on every applicable table is uniform: `SET deleted = true, updated_at = ?`. The `msg` field is **not** touched — deleted messages retain their content in Cassandra, and the frontend is responsible for rendering a placeholder when `deleted == true`. No `deleted_at` or `deleted_by` columns are introduced; the `updated_at` column serves as the delete timestamp when `deleted == true`.

---

## 9. Reused Shared Infrastructure

This spec depends on scaffolding introduced by the edit spec. That scaffolding is assumed to already be merged:

| Artifact | Location | Role in delete |
|---|---|---|
| `EventPublisher` interface | `history-service/internal/service/service.go` | Publishes `MessageDeletedEvent` to `chat.room.{roomID}.event`. |
| `EventPublisher` wire-up in `main.go` | `history-service/cmd/main.go` | Closure wrapping `nc.Publish`; unchanged. |
| `canModify(msg, account) bool` | `history-service/internal/service/utils.go` | Sender equality check; reused unchanged. |
| `getAccessSince` helper | `history-service/internal/service/utils.go` | Subscription check; unchanged. |
| `MessageRepository` interface pattern | `history-service/internal/service/service.go` | Extended with `SoftDeleteMessage` in addition to the existing `UpdateMessageContent` added by edit. |
| `GetMessageByID` | `history-service/internal/cassrepo/repository.go` | Hydrates the full `*models.Message`; unchanged. |

**New additions specific to delete:**

```go
type MessageRepository interface {
    // ... existing methods including UpdateMessageContent from edit ...
    SoftDeleteMessage(ctx context.Context, msg *models.Message, deletedAt time.Time) error
}
```

`make generate` regenerates `service/mocks/mock_repository.go` to include the new method. No other interfaces are touched. No `SubscriptionRepository` extension is needed because sender-only authorization does not require role lookup.

---

## 10. Testing Strategy

**Unit tests** (`history-service/internal/service/messages_test.go`) — table-driven, covering:

| Scenario | Expected outcome |
|---|---|
| Sender deletes own message — happy path | `DeleteMessageResponse` returned; `SoftDeleteMessage` called once with correct `*Message`; event published once |
| Non-subscriber caller | `ErrForbidden("not subscribed to room")`; message never loaded; no UPDATE; no publish |
| Subscriber but not sender | `ErrForbidden("only the sender can delete")`; no UPDATE; no publish |
| Message ID not found | `ErrNotFound`; no UPDATE; no publish |
| Message found but wrong roomID | `ErrNotFound` (no leak); no UPDATE; no publish |
| Already-deleted message | success reply with existing state; `SoftDeleteMessage` **not** called; no publish |
| Cassandra UPDATE error | `ErrInternal("failed to delete message")`; no publish |
| Publisher returns error after successful UPDATE | success reply returned; warning logged |

Mocks: `MessageRepository` (regenerated with the new method), `SubscriptionRepository` (unchanged), in-memory fake `EventPublisher` that captures published payloads.

**Integration tests** (`history-service/internal/cassrepo/integration_test.go`, build tag `integration`) — use `testcontainers-go` Cassandra module:

| Scenario | Assertion |
|---|---|
| Top-level message delete | `messages_by_id` and `messages_by_room` rows have `deleted == true`; `msg` content preserved; `updated_at` advanced; `thread_messages_by_room` row count == 0 (no phantom) |
| Thread reply delete | `messages_by_id` and `thread_messages_by_room` rows marked deleted with correct `thread_room_id` PK; no phantom in `messages_by_room` |
| Delete on pinned message (seeded directly in `pinned_messages_by_room`) | `deleted == true` also propagated to the pinned mirror |
| Idempotency | running the same `SoftDeleteMessage` twice yields identical row state (except `updated_at`, which advances); `deleted` column stays `true` |

**Service-level integration test** (`history-service/internal/service/integration_test.go`, build tag `integration`) — wires the real repo, a recording `EventPublisher`, and asserts both Cassandra state and event publication in one flow.

**Coverage expectations** (per CLAUDE.md): ≥ 80 % per package; ≥ 90 % for the handler and repo method introduced by this spec.

---

## 11. Frontend Integration Contract

This spec does **not** block the frontend; the JS change can ship in a separate PR. It documents only the contract.

Backend emits to `chat.room.{roomID}.event` with:

```json
{
    "type": "message_deleted",
    "timestamp": 1714000000000,
    "roomId": "r1",
    "messageId": "m-abc",
    "deletedBy": "alice",
    "deletedAt": 1714000000000
}
```

Expected `chat-frontend/src/components/MessageArea.jsx` behavior:

1. Subscribe to `roomEvent(room.id)` (already present for `new_message`).
2. Add a branch for `evt.type === 'message_deleted'`: find the message by `evt.messageId` in local state and set `deleted: true`.
3. Render "[message deleted]" in place of the message content for any message with `deleted == true`. The original `msg` value is still available on the object (Cassandra retained it) and clients may choose to display it for the sender's own view; this PR does not mandate that UX.

Missed-event behavior: if the client was not subscribed at publish time, the deletion is observed on the next history fetch — Cassandra is authoritative, and the `deleted` column is persisted.

---

## 12. Out of Scope / Deferred

- **Hard delete** — this spec is soft-delete only. `DELETE` statements against Cassandra are not used.
- **Audit trail / "deleted at/by" persistence** — no new columns. `DeletedBy` travels only in the live event; after refresh, clients see `deleted == true` and `updated_at`, but not the actor name. Acceptable under sender-only authorization since `DeletedBy == Sender`. This is an **intentional design decision, not an oversight**: the codebase has no existing audit pattern for messages, rooms, or subscriptions (verified by searching for `*_audit`, `*_history`, `revisions` tables and structs). Adding audit columns or a dedicated revisions table would require its own design covering retention, access control, and UI surface — all out of scope for this PR.
- **Moderation by room owners** — explicitly excluded. A future "admin-delete" operation would require a separate authorization path and likely separate audit columns.
- **Thread parent `QuotedParentMessage` snapshot update** — deleting a message that is the parent of a thread does not update the embedded `QuotedParentMessage` in child rows' denormalized copies. Accepted eventual-consistency gap.
- **Cross-site federation** — no outbox/inbox propagation.
- **DM inbox reordering on delete** — intentionally not bumped.
- **Push notification cancellation** — if a notification was already sent for the deleted message, it is not recalled.
- **Edit operation** — covered by a separate spec.

---

## 13. Risks & Known Limitations

- **Multi-table fan-out partial failure** — if `messages_by_id` UPDATE succeeds but `messages_by_room` UPDATE fails, the room is temporarily inconsistent. Caller retry converges the state because `deleted = true` is strictly idempotent.
- **`pinned_messages_by_room` branch is dead code today** — no pin operation exists yet. The branch is kept so future pin code does not need to retrofit delete to cover it.
- **Best-effort publish** — a publish failure after a successful UPDATE is logged but not retried. Clients will still see the deletion on the next history fetch.
- **No persisted "who deleted" attribution** — acceptable under sender-only authorization (actor always equals sender). If future scope introduces moderator-delete, a `deleted_by` column will be required.
- **`historySharedSince` bound intentionally not enforced** — a user who leaves and rejoins can delete their own messages that predate their new join window. Documented as an intentional design choice, not a bug.
- **No message cache invalidation is required** — confirmed by code audit that no message cache exists. The only Valkey-backed cache in the codebase (`pkg/roomkeystore`) holds room encryption keys; there is no in-memory or external cache of message content or message lists. `history-service` reads Cassandra directly on every request, so the `deleted = true` flag is visible on the next read without any invalidation step.
