# Edit Message — Design

**Service:** `history-service`
**Operation:** synchronous NATS request/reply for editing a message's content
**Status:** design — spec in progress

---

## 1. Goal

Add a synchronous message-edit operation to `history-service` via NATS request/reply, with a best-effort live event fan-out so clients currently subscribed to the room see edits in real time. The sender — and only the sender, provided they are currently subscribed to the room — may edit their own message content.

---

## 2. Architecture

Single-service design: all logic stays in `history-service`. No changes to `message-gatekeeper`, `message-worker`, or `broadcast-worker`. No changes to the Cassandra schema. No cross-site federation in this PR.

Per-request flow:

```
NATS request → parse → subscription check → load message
  → sender check → content validation → Cassandra UPDATE(s)
  → publish live event to chat.room.{roomID}.event → reply
```

The reply is synchronous; the event publish is best-effort (logged on failure but does not roll back the UPDATE).

---

## 3. Authorization Model

Two gates, in order:

1. **Subscription gate** — caller must currently be subscribed to the room. Implemented via the existing `getAccessSince` helper in `history-service/internal/service/utils.go`. Non-subscribers receive `ErrForbidden("not subscribed to room")` **before** any message lookup. This prevents non-members from probing messageID → roomID mappings via differential error responses.
2. **Sender gate** — after loading the message, assert `msg.Sender.Account == callerAccount`. Anyone else (including room owners) receives `ErrForbidden("only the sender can edit")`.

Design notes:

- The subscription check enforces **current** membership, not historical membership. A user removed from the room cannot edit their own past messages.
- The `historySharedSince` time bound returned by `getAccessSince` is **not** enforced for edit. Sender identity is the real gate — a user who left and rejoined can still edit their own pre-rejoin messages.
- Role (`RoleOwner` vs `RoleMember`) is not consulted. Ownership privileges exist elsewhere (member management, restricted-room admission) but do not extend to editing another user's content.
- No extension to the `SubscriptionRepository` interface is required; the existing `GetHistorySharedSince` method (already in the interface and implementation) is sufficient.

---

## 4. Message Hydration — Why the Request Only Needs `messageID`

The `EditMessageRequest` carries only `MessageID` and `NewMsg`. All other PK components needed to target rows in `messages_by_room`, `thread_messages_by_room`, and `pinned_messages_by_room` (e.g. `room_id`, `created_at`, `thread_room_id`, `pinned_at`, and the discriminator `thread_parent_id`) are **not** supplied by the caller.

Instead, the handler hydrates the full `*models.Message` up-front via the existing `GetMessageByID(ctx, messageID)` method. That method queries the `messages_by_id` lookup table, which by design stores every column — it serves as a universal metadata lookup. Its PK is `PRIMARY KEY (message_id, created_at)` with `message_id` as the partition key, so a single-column `WHERE message_id = ?` query returns the unique row.

Once hydrated, the handler passes the full `*models.Message` to `UpdateMessageContent`. The repository reads `msg.RoomID`, `msg.CreatedAt`, `msg.ThreadParentID`, `msg.ThreadRoomID`, and `msg.PinnedAt` from that struct to construct each downstream table's `UPDATE … WHERE <full PK>` statement. The caller never needs to know or transmit those fields.

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
| Sender check | `msg.Sender.Account == account` | `ErrForbidden("only the sender can edit")` |
| Content validation | trimmed non-empty, raw ≤ 20 KB | `ErrBadRequest("newMsg must not be empty")` or `ErrBadRequest("newMsg exceeds maximum size")` |
| Cassandra UPDATE | all applicable tables updated | `ErrInternal("failed to edit message")`, no event published |
| Event publish | event delivered to `chat.room.{roomID}.event` | log warning, still reply success |

Write-path semantics:

- The write path is **best-effort multi-UPDATE, not atomic**. If one table UPDATE succeeds and a subsequent one fails, the caller receives `ErrInternal`. All UPDATEs are idempotent with respect to content — retries converge on consistent state. Timestamps (`edited_at`, `updated_at`) reflect the last successful write time and therefore change on each retry; they are not strictly idempotent.
- Event publish failure after a successful set of UPDATEs is logged as a warning; the reply still indicates success. Clients that missed the live event will observe the edit on their next history fetch (Cassandra is authoritative).

---

## 6. Request, Response, and Event Types

Declared in `history-service/internal/models/message.go`.

```go
type EditMessageRequest struct {
    MessageID string `json:"messageId"`
    NewMsg    string `json:"newMsg"`
}

type EditMessageResponse struct {
    MessageID string `json:"messageId"`
    EditedAt  int64  `json:"editedAt"` // UTC millis
}

type MessageEditedEvent struct {
    Type      string `json:"type"`      // "message_edited"
    Timestamp int64  `json:"timestamp"` // UTC millis, event publish time (per CLAUDE.md convention)
    RoomID    string `json:"roomId"`
    MessageID string `json:"messageId"`
    NewMsg    string `json:"newMsg"`
    EditedBy  string `json:"editedBy"`  // actor account (always == message.sender.account under sender-only auth)
    EditedAt  int64  `json:"editedAt"`  // UTC millis, domain time when edit occurred
}
```

`Timestamp` is the event-envelope time (required on every NATS event per CLAUDE.md); `EditedAt` is the domain time when the edit occurred. Both are populated from a single `time.Now().UTC()` captured in the handler immediately before the Cassandra UPDATE.

---

## 7. NATS Subject

Request subject pattern added to `pkg/subject/subject.go`, mirroring the existing `MsgHistoryPattern` / `MsgGetPattern` style:

```go
func MsgEditPattern(siteID string) string {
    return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.edit", siteID)
}
```

Concrete example: `chat.user.alice.request.room.r1.site-a.msg.edit`.

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

**NULL handling (gocql):** `ThreadParentID` and `ThreadRoomID` are typed `string` (non-pointer) in `pkg/model/cassandra/message.go`, so gocql maps a NULL Cassandra column to Go zero value `""` during scan. The check `msg.ThreadParentID == ""` therefore matches both NULL and explicitly-empty-string cases. `PinnedAt` is typed `*time.Time` (pointer), so NULL maps to `nil`; the check `msg.PinnedAt != nil` handles both NULL and unset consistently. This mirrors the write-path branching in `message-worker/handler.go:75` (`if evt.Message.ThreadParentMessageID != ""`) so reads and writes agree.

The SET clause on every applicable table is uniform: `SET msg = ?, edited_at = ?, updated_at = ?`. Only `msg` is editable; all other columns retain their existing values. No schema changes.

---

## 9. Shared Infrastructure Introduced by This Spec

Edit is the first write operation in `history-service`. It introduces scaffolding that the delete operation (separate spec, deferred to a follow-up PR) will reuse.

**9.1 `EventPublisher` interface** — declared in `history-service/internal/service/service.go`:

```go
type EventPublisher interface {
    Publish(ctx context.Context, subject string, data []byte) error
}
```

Injected into `HistoryService` via constructor. In `cmd/main.go`, a thin closure wraps the NATS core `nc.Publish`:

```go
pub := func(ctx context.Context, subject string, data []byte) error {
    return nc.Publish(ctx, subject, data)
}
```

Live events are core NATS (not JetStream), matching the existing room-event fan-out in `broadcast-worker`.

**9.2 `canModify` authorization helper** — declared in `history-service/internal/service/utils.go`:

```go
func canModify(msg *models.Message, account string) bool {
    return msg.Sender != nil && msg.Sender.Account == account
}
```

A pure function: no context, no dependencies, no mocks. Reused unchanged by the delete handler.

**9.3 `maxContentBytes` constant** — declared at the top of `history-service/internal/service/messages.go`:

```go
const maxContentBytes = 20 * 1024 // 20 KB, mirrors message-gatekeeper
```

Extraction into a shared `pkg/` helper is deferred — duplicating a single `const` is acceptable to keep this PR focused. Only edit uses this (delete has no content-size constraint).

**9.4 `MessageRepository` interface extension** — declared in `history-service/internal/service/service.go`:

```go
type MessageRepository interface {
    // ... existing methods ...
    UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error
}
```

Mocks regenerated via `make generate`. This explicit interface-extension step addresses the missing task called out in PR #112 code review.

---

## 10. Testing Strategy

**Unit tests** (`history-service/internal/service/messages_test.go`) — table-driven, covering:

| Scenario | Expected outcome |
|---|---|
| Sender edits own message — happy path | `EditMessageResponse` returned; UPDATE called once with correct `*Message`; event published once to `chat.room.<roomID>.event` |
| Non-subscriber caller | `ErrForbidden("not subscribed to room")`; message never loaded; no UPDATE; no publish |
| Subscriber but not sender | `ErrForbidden("only the sender can edit")`; no UPDATE; no publish |
| Message ID not found | `ErrNotFound`; no UPDATE; no publish |
| Message found but wrong roomID | `ErrNotFound` (same error — no leak); no UPDATE; no publish |
| `newMsg` empty after trim | `ErrBadRequest("newMsg must not be empty")`; no UPDATE; no publish |
| `newMsg` > 20 KB | `ErrBadRequest("newMsg exceeds maximum size")`; no UPDATE; no publish |
| Cassandra UPDATE error | `ErrInternal("failed to edit message")`; no publish |
| Publisher returns error after successful UPDATE | success reply returned; warning logged |

Mocks: `MessageRepository` (regenerated via `make generate`), `SubscriptionRepository` (unchanged), and an in-memory fake `EventPublisher` that captures published payloads for assertion.

**Integration tests** (`history-service/internal/cassrepo/integration_test.go`, build tag `integration`) — use `testcontainers-go` Cassandra module:

| Scenario | Assertion |
|---|---|
| Top-level message edit | `messages_by_id` and `messages_by_room` rows have new `msg` and `edited_at`; `thread_messages_by_room` row count == 0 (no phantom) |
| Thread reply edit | `messages_by_id` and `thread_messages_by_room` rows updated with correct `thread_room_id` PK; no phantom in `messages_by_room` |
| Edit on pinned message (seeded directly in `pinned_messages_by_room`) | new `msg` also propagated to the pinned mirror |
| Idempotency | running the same UPDATE twice yields identical row state (except timestamps, which advance) |

**Service-level integration test** (`history-service/internal/service/integration_test.go`, build tag `integration`) — wires the real repo, a recording `EventPublisher`, and asserts both Cassandra state and event publication in one flow.

**Coverage expectations** (per CLAUDE.md): ≥ 80 % per package; ≥ 90 % for the handler and repo methods introduced by this spec. Every error path above must have a corresponding test case — no happy-path-only coverage.

---

## 11. Frontend Integration Contract

This spec does **not** block the frontend; the JS change can ship in a separate PR. It documents only the contract.

Backend emits to `chat.room.{roomID}.event` with:

```json
{
    "type": "message_edited",
    "timestamp": 1714000000000,
    "roomId": "r1",
    "messageId": "m-abc",
    "newMsg": "corrected text",
    "editedBy": "alice",
    "editedAt": 1714000000000
}
```

Expected `chat-frontend/src/components/MessageArea.jsx` behavior:

1. Subscribe to `roomEvent(room.id)` (already present for `new_message`).
2. Add a branch for `evt.type === 'message_edited'`: find the message by `evt.messageId` in local state; update `msg` to `evt.newMsg` and set `editedAt` to `evt.editedAt`.
3. Render a small "(edited)" indicator next to the timestamp for any message with a non-null `editedAt`.

Missed-event behavior: if the client was not subscribed at publish time, the edit is observed on the next history fetch — Cassandra is authoritative, and the `edited_at` column is persisted.

---

## 12. Out of Scope / Deferred

- **Audit history / edit log** — `msg` is overwritten in place; no revision table. This is an **intentional design decision, not an oversight**: the codebase has no existing audit pattern for messages, rooms, or subscriptions (verified by searching for `*_audit`, `*_history`, `revisions` tables and structs). Adopting one for messages would require its own dedicated design covering retention policy, access control, storage model, and UI surface area — all out of scope for this PR. Future work can introduce a `message_revisions_by_id` table if the product surfaces "view edit history".
- **Thread parent `QuotedParentMessage` snapshot update** — editing a message that is the parent of a thread does not update the embedded `QuotedParentMessage` in child rows' denormalized copies. Accepted eventual-consistency gap.
- **Cross-site federation** — no outbox/inbox propagation. An edit on site A does not propagate to site B's Cassandra. Future PR.
- **DM inbox reordering on edit** — intentionally not bumped. Editing should not move a thread to the top of the inbox.
- **Push notifications on edit** — no re-notification.
- **Delete operation** — covered by a separate spec.
- **`MessageRepository` mock hand-edits** — mocks are regenerated via `make generate`; do not edit `mock_repository.go` manually.

---

## 13. Risks & Known Limitations

- **Multi-table fan-out partial failure** — if `messages_by_id` UPDATE succeeds but `messages_by_room` UPDATE fails, the room is temporarily inconsistent. Caller retry converges the state because all UPDATEs are idempotent with respect to content.
- **`pinned_messages_by_room` branch is dead code today** — no pin operation exists yet. The branch is kept so future pin code does not need to retrofit edit to cover it.
- **Best-effort publish** — a publish failure after a successful UPDATE is logged but not retried. Clients will still see the edit on the next history fetch. Retrying publish in-handler would risk duplicate events.
- **`historySharedSince` bound intentionally not enforced** — a user who leaves and rejoins can edit their own messages that predate their new join window. Documented as an intentional design choice, not a bug.
- **No message cache invalidation is required** — confirmed by code audit that no message cache exists. The only Valkey-backed cache in the codebase (`pkg/roomkeystore`) holds room encryption keys; there is no in-memory or external cache of message content or message lists. `history-service` reads Cassandra directly on every request, so edited content is visible on the next read without any invalidation step.
- **MongoDB `rooms` / `threadRooms` summary fields are not touched on edit** — `rooms.LastMsgID`, `rooms.LastMsgAt`, `rooms.LastMentionAllAt`, `threadRooms.LastMsgID`, and `threadRooms.LastMsgAt` are written only by `message-worker` / `broadcast-worker` on new message arrival. Edit leaves them alone; inbox sort position and thread last-message pointers are unaffected (which is the intuitive behavior — editing a message should not move its room to the top of anyone's inbox).
