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
