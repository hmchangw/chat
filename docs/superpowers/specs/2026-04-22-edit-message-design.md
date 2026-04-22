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
