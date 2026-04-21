# Message Edit & Delete Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add message edit and soft-delete operations to `history-service` via NATS request/reply. After each successful write, `history-service` publishes a live event to the room subject so any currently-subscribed client updates in real time.

**Architecture:** All backend work lives in `history-service`. Per-handler flow: parse → authorize → validate → UPDATE Cassandra (all denormalized tables) → publish event to `chat.room.{roomID}.event` → reply. Uniform across group and DM rooms — edit/delete payloads don't need per-user customization, so a single room-subject publish reaches every subscribed client. No changes to `message-gatekeeper`, `message-worker`, `broadcast-worker`, or the Cassandra schema.

**Tech Stack:** Go 1.25, NATS (core pub/sub for fan-out), Cassandra (gocql), MongoDB, go.uber.org/mock, testify, testcontainers-go

**Decisions (locked):**
- **Scope:** `history-service` only. No changes to gatekeeper, worker, broadcast-worker, or schema.
- **Delete semantics:** Soft delete — `UPDATE … SET deleted = true, updated_at = now()`. `msg` content retained. No Cassandra `DELETE`.
- **Edit semantics:** `UPDATE … SET msg = ?, edited_at = now(), updated_at = now()`. Only `msg` is editable.
- **Authorization:** Actor is either the sender (`actor.account == message.sender.account`) or holds `RoleOwner` in their subscription for the room. No separate admin role today.
- **Message length:** Reuse existing `maxContentBytes = 20 * 1024` (20 KB) from `message-gatekeeper/handler.go:19`. Extract to a shared helper available to both services.
- **Live update fan-out:** Publish to `chat.room.{roomID}.event` after successful write. Works uniformly for group and DM rooms because edit/delete events carry no per-user fields. Publish is best-effort — failure to publish does not roll back the Cassandra UPDATE.
- **Idempotency:** All UPDATEs are idempotent — repeat calls converge on identical state. Retries are safe.
- **Naming:** `editedBy` / `deletedBy` carry the acting user's account (matches codebase convention of `account` as user identifier).

**Deferred / out of scope:**
- Thread `QuotedParentMessage` snapshot updates in child rows (accepted eventual-consistency gap)
- Audit / edit-history table (overwrites `msg` in place)
- Cross-site federation via outbox
- DM inbox "bump" on edit (intentionally not pushed — editing shouldn't reorder threads)
- Push notifications on edit/delete
- Frontend-side subscription to non-open rooms (liveness only applies to currently-open room; missed events are picked up on next history fetch)

**Known eventual-consistency gaps (documented, not fixed):**
- Multi-table fan-out partial failure: if one UPDATE succeeds and another fails, the room is temporarily inconsistent. Mitigation: idempotent UPDATEs + handler returns error → caller retries → convergence.
- Thread parent snapshot drift: edited/deleted parent messages won't update the embedded `QuotedParentMessage` in child rows.
- Missed live event: users not subscribed to the room subject at publish time see the change on next history fetch (Cassandra is authoritative).

---

## Section A: Request/Response Model Types

### Task 1: Add edit/delete request and response types

**Files:**
- Modify: `history-service/internal/models/message.go`

- [ ] **Step 1: Add request types**

```go
type EditMessageRequest struct {
    MessageID string `json:"messageId"`
    NewMsg    string `json:"newMsg"`
}

type EditMessageResponse struct {
    MessageID string `json:"messageId"`
    EditedAt  int64  `json:"editedAt"` // UTC millis
}

type DeleteMessageRequest struct {
    MessageID string `json:"messageId"`
}

type DeleteMessageResponse struct {
    MessageID string `json:"messageId"`
    DeletedAt int64  `json:"deletedAt"` // UTC millis
}
```

- [ ] **Step 2: Add fan-out event types (published to the room subject)**

```go
type MessageEditedEvent struct {
    Type      string `json:"type"`      // "message_edited"
    RoomID    string `json:"roomId"`
    MessageID string `json:"messageId"`
    NewMsg    string `json:"newMsg"`
    EditedBy  string `json:"editedBy"`  // actor account
    EditedAt  int64  `json:"editedAt"`  // UTC millis
}

type MessageDeletedEvent struct {
    Type      string `json:"type"`      // "message_deleted"
    RoomID    string `json:"roomId"`
    MessageID string `json:"messageId"`
    DeletedBy string `json:"deletedBy"`
    DeletedAt int64  `json:"deletedAt"`
}
```

- [ ] **Step 3: Round-trip JSON tests** in `message_test.go` or equivalent.

---

## Section B: NATS Subject Patterns

### Task 2: Add edit/delete request subject builders

**Files:**
- Modify: `pkg/subject/subject.go`
- Test: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write failing tests** for `MsgEditPattern` and `MsgDeletePattern`.

- [ ] **Step 2: Add builders** matching the existing `MsgHistoryPattern` style:

```go
func MsgEditPattern(siteID string) string {
    return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.edit", siteID)
}

func MsgDeletePattern(siteID string) string {
    return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.delete", siteID)
}
```

(Fan-out event uses the existing `subject.RoomEvent(roomID)` — no new builder needed.)

---

## Section C: Cassandra Repository — UPDATE Methods

### Task 3: Add `UpdateMessageContent` and `SoftDeleteMessage` to the repo

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Test: `history-service/internal/cassrepo/integration_test.go`

- [ ] **Step 1: Write failing integration tests (testcontainers)** covering:
  - Edit updates `msg`, `edited_at`, `updated_at` in `messages_by_room` and `messages_by_id`
  - Edit also updates `thread_messages_by_room` when the row exists there
  - Edit also updates `pinned_messages_by_room` when the row exists there
  - Edit on a non-existent row in a table is a safe no-op (UPDATE is idempotent in Cassandra)
  - Delete sets `deleted = true` + `updated_at` across the same set of tables; `msg` content is preserved
  - Running the same UPDATE twice produces identical state (idempotency)

- [ ] **Step 2: Implement the two methods**

```go
// UpdateMessageContent sets msg, edited_at, and updated_at across all tables
// that may hold the row. UPDATEs are issued unconditionally — Cassandra treats
// UPDATE against a missing PK as a no-op (actually creates a "row" with only
// the updated columns set; safe here because all four tables share the PK and
// the row exists in `messages_by_room` / `messages_by_id` by construction,
// and `thread_messages_by_room` / `pinned_messages_by_room` rows only exist
// when they should, so the "no-op" case leaves them absent or matching).
func (r *Repository) UpdateMessageContent(
    ctx context.Context,
    roomID, messageID string,
    createdAt time.Time,
    newMsg string,
    editedAt time.Time,
) error { ... }

// SoftDeleteMessage sets deleted=true and updated_at across all tables that
// hold the row. msg content is retained.
func (r *Repository) SoftDeleteMessage(
    ctx context.Context,
    roomID, messageID string,
    createdAt time.Time,
    deletedAt time.Time,
) error { ... }
```

Both methods UPDATE these tables in order:
1. `messages_by_room` — PK `(room_id, created_at, message_id)`
2. `messages_by_id` — PK `(message_id, created_at)`
3. `thread_messages_by_room` — PK `(room_id, thread_room_id, created_at, message_id)` — only UPDATE when the message was threaded; for simplicity, always issue, treat as best-effort
4. `pinned_messages_by_room` — PK `(room_id, created_at, message_id)` — only if pinned; for simplicity, always issue

**On first error, return immediately.** Retries from the caller re-attempt the full set; idempotency guarantees convergence.

---

## Section D: Authorization Helper

### Task 4: Add `canModify` and extend `SubscriptionRepository`

**Files:**
- Modify: `history-service/internal/service/service.go` (interface)
- Modify: `history-service/internal/service/utils.go` (or equivalent) — add helper
- Modify: `history-service/internal/mongorepo/subscription.go` (add interface method wiring only; `GetSubscription` already implemented at line 29)
- Test: `history-service/internal/service/messages_test.go`

- [ ] **Step 1: Extend `SubscriptionRepository` interface** (in `service/service.go`):

```go
type SubscriptionRepository interface {
    GetHistorySharedSince(ctx context.Context, account, roomID string) (*time.Time, bool, error)
    GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
}
```

The `SubscriptionRepo` implementation already has `GetSubscription` — no new Mongo code.

- [ ] **Step 2: Run `make generate`** to regenerate `service/mocks/mock_repository.go`.

- [ ] **Step 3: Write failing tests for `canModify`:**
  - Sender of the message → allowed
  - Different user with `RoleOwner` in their subscription → allowed
  - Different user without `RoleOwner` → forbidden
  - No subscription for actor → forbidden
  - Subscription store error → internal error

- [ ] **Step 4: Implement `canModify`**

```go
func (s *HistoryService) canModify(ctx context.Context, account, roomID string, msg *models.Message) (bool, error) {
    if msg.Sender != nil && msg.Sender.Account == account {
        return true, nil
    }
    sub, err := s.subscriptions.GetSubscription(ctx, account, roomID)
    if err != nil {
        return false, natsrouter.ErrInternal("failed to check role")
    }
    if sub == nil {
        return false, nil
    }
    for _, role := range sub.Roles {
        if role == model.RoleOwner {
            return true, nil
        }
    }
    return false, nil
}
```

---

## Section E: Service Handlers

### Task 5: Implement `EditMessage` and `DeleteMessage`

**Files:**
- Modify: `history-service/internal/service/messages.go`
- Test: `history-service/internal/service/messages_test.go`

- [ ] **Step 1: Add a publisher dependency to `HistoryService`**

The service needs to publish to a subject. Add a small interface and constructor param:

```go
type EventPublisher interface {
    Publish(ctx context.Context, subject string, data []byte) error
}

type HistoryService struct {
    messages      MessageRepository
    subscriptions SubscriptionRepository
    publisher     EventPublisher
}

func New(msgs MessageRepository, subs SubscriptionRepository, pub EventPublisher) *HistoryService { ... }
```

In `cmd/main.go`, wrap `nc.Publish` into the `EventPublisher` shape (mirrors `broadcast-worker/main.go:152-159`).

- [ ] **Step 2: Run `make generate`** to regenerate mocks if any interface test support is needed.

- [ ] **Step 3: Write failing table-driven tests for `EditMessage`** covering:
  - Happy path (sender edits own message) — UPDATE called, event published, success reply
  - Happy path (owner edits someone else's message) — same
  - Message not found → `ErrNotFound`, no UPDATE, no publish
  - Message's `roomID` doesn't match subject param → `ErrNotFound`, no UPDATE, no publish
  - Actor is neither sender nor owner → `ErrForbidden`, no UPDATE, no publish
  - Empty `newMsg` (after trim) → `ErrBadRequest`, no UPDATE, no publish
  - `newMsg` > 20 KB → `ErrBadRequest`, no UPDATE, no publish
  - Cassandra UPDATE error → `ErrInternal`, no publish
  - Publisher error → log warning, still reply success (best-effort fan-out)

- [ ] **Step 4: Implement `EditMessage`**

```go
func (s *HistoryService) EditMessage(c *natsrouter.Context, req models.EditMessageRequest) (*models.EditMessageResponse, error) {
    account := c.Param("account")
    roomID := c.Param("roomID")

    // Load & validate target message
    msg, err := s.messages.GetMessageByID(c, req.MessageID)
    if err != nil {
        slog.Error("edit: load message", "error", err, "messageID", req.MessageID)
        return nil, natsrouter.ErrInternal("failed to load message")
    }
    if msg == nil || msg.RoomID != roomID {
        return nil, natsrouter.ErrNotFound("message not found")
    }

    // Authorize
    allowed, err := s.canModify(c, account, roomID, msg)
    if err != nil { return nil, err }
    if !allowed {
        return nil, natsrouter.ErrForbidden("only the sender or a room owner can edit")
    }

    // Validate input
    trimmed := strings.TrimSpace(req.NewMsg)
    if trimmed == "" {
        return nil, natsrouter.ErrBadRequest("newMsg must not be empty")
    }
    if len(req.NewMsg) > maxContentBytes {
        return nil, natsrouter.ErrBadRequest("newMsg exceeds maximum size")
    }

    // Persist
    editedAt := time.Now().UTC()
    if err := s.messages.UpdateMessageContent(c, roomID, req.MessageID, msg.CreatedAt, req.NewMsg, editedAt); err != nil {
        slog.Error("edit: cassandra update", "error", err, "messageID", req.MessageID)
        return nil, natsrouter.ErrInternal("failed to edit message")
    }

    // Fan out (best-effort)
    evt := models.MessageEditedEvent{
        Type: "message_edited", RoomID: roomID, MessageID: req.MessageID,
        NewMsg: req.NewMsg, EditedBy: account, EditedAt: editedAt.UnixMilli(),
    }
    if payload, err := json.Marshal(evt); err == nil {
        if pubErr := s.publisher.Publish(c, subject.RoomEvent(roomID), payload); pubErr != nil {
            slog.Warn("edit: publish event failed", "error", pubErr, "messageID", req.MessageID)
        }
    }

    return &models.EditMessageResponse{MessageID: req.MessageID, EditedAt: editedAt.UnixMilli()}, nil
}
```

- [ ] **Step 5: Write failing table-driven tests for `DeleteMessage`** — same structure as Edit but without length validation.

- [ ] **Step 6: Implement `DeleteMessage`** — same shape, calls `SoftDeleteMessage`, publishes `MessageDeletedEvent`.

- [ ] **Step 7: Define `maxContentBytes` constant** at the top of `messages.go` with the same value as `message-gatekeeper` (`20 * 1024`). (Extraction into a shared `pkg/` helper is a follow-up cleanup — not required here to keep scope minimal.)

---

## Section F: Handler Registration

### Task 6: Wire the new handlers in `RegisterHandlers`

**Files:**
- Modify: `history-service/internal/service/service.go`

- [ ] **Step 1: Add registrations** next to the existing ones:

```go
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, siteID string) {
    natsrouter.Register(r, subject.MsgHistoryPattern(siteID), s.LoadHistory)
    natsrouter.Register(r, subject.MsgNextPattern(siteID), s.LoadNextMessages)
    natsrouter.Register(r, subject.MsgSurroundingPattern(siteID), s.LoadSurroundingMessages)
    natsrouter.Register(r, subject.MsgGetPattern(siteID), s.GetMessageByID)
    natsrouter.Register(r, subject.MsgEditPattern(siteID), s.EditMessage)     // new
    natsrouter.Register(r, subject.MsgDeletePattern(siteID), s.DeleteMessage) // new
}
```

- [ ] **Step 2: Smoke test** — build and run against `docker-local`, issue a NATS request to the edit subject, verify reply + Cassandra row + event fan-out (e.g. via `nats sub` on the room subject in a second terminal).

---

## Section G: Integration Tests

### Task 7: End-to-end integration coverage

**Files:**
- Modify: `history-service/internal/cassrepo/integration_test.go` (already covered in Task 3)
- Modify: `history-service/internal/service/messages_test.go` (unit-level, covered in Task 5)

- [ ] **Step 1: Add a history-service-level integration test** (build tag `integration`) that:
  - Inserts a message via direct CQL
  - Calls `EditMessage` through the in-process service
  - Asserts Cassandra rows are updated
  - Asserts an event was published to the room subject (use a recording publisher like `broadcast-worker/integration_test.go:recordingPublisher`)
  - Repeats for `DeleteMessage`

- [ ] **Step 2: Verify coverage** meets CLAUDE.md thresholds (≥80% package, target 90%+ for handlers/store).

---

## Section H: Frontend — Handle Edit/Delete Events

### Task 8: Update `MessageArea.jsx` to render live edits and deletes

**Files:**
- Modify: `chat-frontend/src/components/MessageArea.jsx`

- [ ] **Step 1: Extend the existing subscription callback**

The current code only handles `evt.type === 'new_message'`. Add two branches:

```jsx
const sub = subscribe(roomEvent(room.id), (evt) => {
    if (evt.type === 'new_message' && evt.message) {
        setMessages((prev) => { /* existing dedupe-and-append logic */ })
        return
    }
    if (evt.type === 'message_edited') {
        setMessages((prev) => prev.map((m) =>
            messageId(m) === evt.messageId
                ? { ...m, msg: evt.newMsg, editedAt: evt.editedAt }
                : m
        ))
        return
    }
    if (evt.type === 'message_deleted') {
        setMessages((prev) => prev.map((m) =>
            messageId(m) === evt.messageId
                ? { ...m, deleted: true }
                : m
        ))
        return
    }
})
```

- [ ] **Step 2: Render `edited` indicator and deleted placeholder** in the message list component. Keep it simple — render a small "(edited)" label next to edited messages, and "[message deleted]" for deleted ones. Visual polish is separate.

- [ ] **Step 3: (Optional) Add edit/delete buttons** to the user's own messages. Call the new request/reply endpoints. This may be a separate PR depending on scope.

---

## Section I: Documentation

### Task 9: Note the new write operations

**Files:**
- Modify: `history-service/README.md` (create if absent — short description is fine)

- [ ] **Step 1: Add a brief note** that history-service handles message read **and** write operations — four reads (`msg.history`, `msg.next`, `msg.surrounding`, `msg.get`) and two writes (`msg.edit`, `msg.delete`). Call out explicitly that it is no longer a read-only service. Writes are synchronous request/reply: the handler UPDATEs all denormalized Cassandra tables that may hold the row, then publishes a best-effort live event to `chat.room.{roomID}.event` for subscribed clients.

- [ ] **Step 2: Document known limitations** in the same section:
  - Users not subscribed to the room at publish time see the update on next history fetch
  - DM thread ordering is not bumped on edit/delete (intentional)
  - Thread parent edits don't propagate to child rows' `QuotedParentMessage` snapshot
  - No audit history — `msg` is overwritten in place

- [ ] **Step 3: No schema changes** — `docs/cassandra_message_model.md` is unchanged. All needed columns (`edited_at`, `deleted`, `updated_at`) already exist.

---

## Implementation Order (dependency-aware)

1. Task 1 — model types
2. Task 2 — subject patterns
3. Task 3 — Cassandra repo UPDATE methods + integration tests (locks storage contract)
4. Task 4 — authorization helper + interface extension
5. Task 5 — service handlers + unit tests
6. Task 6 — handler registration
7. Task 7 — service-level integration test
8. Task 8 — frontend changes (can be a separate PR if backend is shipped first)
9. Task 9 — docs
10. Manual smoke test via `docker-local`

## Quality Gates (per CLAUDE.md)

- TDD: Red → Green → Refactor for every task. Never write implementation before a failing test exists.
- Coverage: ≥80% per package, target 90%+ for handlers and store methods.
- `make lint`, `make test` (with `-race`) green before each commit.
- `make test-integration SERVICE=history-service` green before PR merge.
- Pre-commit hook runs lint + tests — fix root causes, never bypass with `--no-verify`.

## Risk Callouts

- **Denormalization fan-out:** a partial failure across tables leaves a room temporarily inconsistent. Mitigation: idempotent UPDATEs + caller retry converge.
- **No audit trail:** owner/sender can silently rewrite history. Accepted; note in PR description.
- **Thread parent drift:** deferred, documented.
- **Best-effort fan-out:** a publish failure after a successful UPDATE is logged but not retried. Clients will still see the edit/delete on next history fetch. Accepted trade-off — retrying publish in-handler risks duplicate events if the retry succeeds after an apparent failure.
