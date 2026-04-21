# Message Edit & Delete Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add message edit and soft-delete operations to `history-service` via NATS request/reply. Phased delivery: Phase 1 (Edit + shared infrastructure) ships first; Phase 2 (Delete) reuses Phase 1's scaffolding and follows after Phase 1 is merged.

**Architecture:** All backend work lives in `history-service`. Per-handler flow: parse → authorize → validate → UPDATE Cassandra (all denormalized tables) → publish event to `chat.room.{roomID}.event` → reply. Uniform across group and DM rooms — edit/delete payloads don't need per-user customization, so a single room-subject publish reaches every subscribed client. No changes to `message-gatekeeper`, `message-worker`, `broadcast-worker`, or the Cassandra schema.

**Tech Stack:** Go 1.25, NATS (core pub/sub for fan-out), Cassandra (gocql), MongoDB, go.uber.org/mock, testify, testcontainers-go

---

## Locked Decisions

- **Scope:** `history-service` only. No changes to gatekeeper, worker, broadcast-worker, or schema.
- **Delete semantics:** Soft delete — `UPDATE … SET deleted = true, updated_at = now()`. `msg` content retained. No Cassandra `DELETE`.
- **Edit semantics:** `UPDATE … SET msg = ?, edited_at = now(), updated_at = now()`. Only `msg` is editable.
- **Authorization:** Actor is either the sender (`actor.account == message.sender.account`) or holds `RoleOwner` in their subscription for the room. No separate admin role today.
- **Message length:** Reuse existing `maxContentBytes = 20 * 1024` (20 KB). Extract to a shared constant in `history-service` available to both services.
- **Live update fan-out:** Publish to `chat.room.{roomID}.event` after successful write. Works uniformly for group and DM rooms because edit/delete events carry no per-user fields. Publish is best-effort — failure to publish does not roll back the Cassandra UPDATE.
- **Idempotency:** All UPDATEs are idempotent — repeat calls converge on identical state. Retries are safe.
- **Naming:** `editedBy` / `deletedBy` carry the acting user's account (matches codebase convention of `account` as user identifier).

---

## Deferred / Out of Scope

- Thread `QuotedParentMessage` snapshot updates in child rows (accepted eventual-consistency gap)
- Audit / edit-history table (overwrites `msg` in place)
- Cross-site federation via outbox
- DM inbox "bump" on edit (intentionally not pushed — editing shouldn't reorder threads)
- Push notifications on edit/delete
- Frontend-side subscription to non-open rooms (liveness only applies to currently-open room; missed events are picked up on next history fetch)

---

## Table Membership (Critical — Cassandra UPDATE is an Upsert)

The four tables are populated differently. UPDATEs must be conditional on which tables actually hold the row, decided from the message's own metadata:

| Table | PK | Who's in it | UPDATE when |
|---|---|---|---|
| `messages_by_id` | `(message_id, created_at)` | every message | always |
| `messages_by_room` | `((room_id), created_at, message_id)` | top-level messages only | `msg.ThreadParentID == ""` |
| `thread_messages_by_room` | `((room_id), thread_room_id, created_at, message_id)` | thread replies only | `msg.ThreadParentID != ""`, supply `msg.ThreadRoomID` |
| `pinned_messages_by_room` | `((room_id), created_at=pinnedAt, message_id)` | empty today (no pin op) | `msg.PinnedAt != nil`, supply `msg.PinnedAt` |

Verified by reading `message-worker/store_cassandra.go` — `SaveMessage` writes only to `messages_by_room` + `messages_by_id`; `SaveThreadMessage` writes only to `messages_by_id` + `thread_messages_by_room`.

**Why this matters:** `UPDATE … WHERE <full PK>` against a missing row in Cassandra is NOT a no-op — it writes a phantom row containing just the updated columns. Blasting UPDATEs to every table would pollute them with junk.

---

## Known Eventual-Consistency Gaps (Documented, Not Fixed)

- Multi-table fan-out partial failure: if one UPDATE succeeds and another fails, the room is temporarily inconsistent. Mitigation: idempotent UPDATEs + handler returns error → caller retries → convergence.
- Thread parent snapshot drift: edited/deleted parent messages won't update the embedded `QuotedParentMessage` in child rows.
- Missed live event: users not subscribed to the room subject at publish time see the change on next history fetch (Cassandra is authoritative).
- `pinned_messages_by_room` branch is dead code today (no pin operation exists). Kept in the implementation so future pin code doesn't need to retrofit edit/delete to cover it.

---

## File Structure Reference

**Phase 1 + Shared Infrastructure:**
- `history-service/internal/models/message.go` (edit types)
- `pkg/subject/subject.go` + `pkg/subject/subject_test.go` (edit pattern)
- `history-service/internal/service/service.go` (EventPublisher, SubscriptionRepository extension)
- `history-service/internal/service/mocks/mock_repository.go` (regenerated)
- `history-service/internal/service/utils.go` (canModify helper)
- `history-service/internal/service/messages.go` + `messages_test.go` (EditMessage handler)
- `history-service/internal/cassrepo/repository.go` + `integration_test.go` (UpdateMessageContent)
- `history-service/cmd/main.go` (EventPublisher wire-up)
- `chat-frontend/src/components/MessageArea.jsx` (message_edited branch)
- `history-service/README.md` (new — reads + writes framing)

**Phase 2 Additions:**
- Same files as Phase 1, with delete-specific types/methods/handlers appended (no new files)

---

# PHASE 1 — Edit & Shared Infrastructure (ships first)

---

## Section A: Request/Response Model Types (Edit)

### Task 1: Add edit request and response types

**Files:**
- Modify: `history-service/internal/models/message.go`

- [ ] **Step 1: Write failing JSON marshal/unmarshal tests** (in `message_test.go` or equivalent) for `EditMessageRequest` and `EditMessageResponse`.

- [ ] **Step 2: Add types to `message.go`**

```go
type EditMessageRequest struct {
    MessageID string `json:"messageId"`
    NewMsg    string `json:"newMsg"`
}

type EditMessageResponse struct {
    MessageID string `json:"messageId"`
    EditedAt  int64  `json:"editedAt"` // UTC millis
}
```

- [ ] **Step 3: Run `make test SERVICE=history-service`** — tests should PASS.

- [ ] **Commit:** `git commit -m "feat: add EditMessageRequest/Response types"`

---

## Section B: NATS Subject Pattern (Edit)

### Task 2: Add edit request subject builder

**Files:**
- Modify: `pkg/subject/subject.go`
- Test: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write failing test** for `MsgEditPattern`:

```go
func TestMsgEditPattern(t *testing.T) {
    assert.Equal(t, "chat.user.{account}.request.room.{roomID}.site-123.msg.edit", MsgEditPattern("site-123"))
}
```

- [ ] **Step 2: Run `make test`** — test should FAIL.

- [ ] **Step 3: Implement the builder**

```go
func MsgEditPattern(siteID string) string {
    return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.edit", siteID)
}
```

- [ ] **Step 4: Run `make test`** — test should PASS.

- [ ] **Commit:** `git commit -m "feat: add MsgEditPattern subject builder"`

---

## Section C: Shared Infrastructure (Extend Repositories, Add Publisher, Add Auth Helper)

### Task 3: Extend `SubscriptionRepository` interface

**Files:**
- Modify: `history-service/internal/service/service.go`

- [ ] **Step 1: Add method to interface** (in `service.go`):

```go
type SubscriptionRepository interface {
    GetHistorySharedSince(ctx context.Context, account, roomID string) (*time.Time, bool, error)
    GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
}
```

The `SubscriptionRepo` implementation in `internal/mongorepo/subscription.go` already has `GetSubscription` at line 29 — no new Mongo code needed.

- [ ] **Step 2: Run `make generate`** to regenerate `service/mocks/mock_repository.go`.

- [ ] **Commit:** `git commit -m "feat: extend SubscriptionRepository interface with GetSubscription"`

---

### Task 4: Add `EventPublisher` interface and wire in `main.go`

**Files:**
- Modify: `history-service/internal/service/service.go`
- Modify: `history-service/cmd/main.go`

- [ ] **Step 1: Add `EventPublisher` interface** to `service.go`:

```go
type EventPublisher interface {
    Publish(ctx context.Context, subject string, data []byte) error
}
```

- [ ] **Step 2: Update `HistoryService` struct** to include publisher:

```go
type HistoryService struct {
    messages      MessageRepository
    subscriptions SubscriptionRepository
    publisher     EventPublisher
}
```

- [ ] **Step 3: Update `New` constructor**:

```go
func New(msgs MessageRepository, subs SubscriptionRepository, pub EventPublisher) *HistoryService {
    return &HistoryService{
        messages:      msgs,
        subscriptions: subs,
        publisher:     pub,
    }
}
```

- [ ] **Step 4: In `cmd/main.go`, create a wrapper** around `nc.Publish` and pass it to the service. Reference `broadcast-worker/main.go:152-159` for the pattern:

```go
pub := func(ctx context.Context, subject string, data []byte) error {
    return nc.Publish(ctx, subject, data)
}
handler := service.New(messagesRepo, subscriptionsRepo, pub)
```

- [ ] **Step 5: Run `make build SERVICE=history-service`** — should compile without error.

- [ ] **Commit:** `git commit -m "feat: add EventPublisher interface and wire in service + main.go"`

---

### Task 5: Add `canModify` authorization helper

**Files:**
- Modify: `history-service/internal/service/utils.go` (or equivalent)

- [ ] **Step 1: Write failing tests** in `messages_test.go` covering:
  - Sender of the message → allowed
  - Different user with `RoleOwner` in subscription → allowed
  - Different user without `RoleOwner` → forbidden
  - No subscription for actor → forbidden
  - Subscription store error → internal error

- [ ] **Step 2: Implement helper**:

```go
// canModify checks if account is authorized to edit/delete the message.
// Authorization passes if: sender of the message, OR holds RoleOwner in subscription.
func (s *HistoryService) canModify(ctx context.Context, account, roomID string, msg *models.Message) (bool, error) {
    if msg.Sender != nil && msg.Sender.Account == account {
        return true, nil
    }
    sub, err := s.subscriptions.GetSubscription(ctx, account, roomID)
    if err != nil {
        slog.Error("canModify: get subscription", "error", err, "account", account, "roomID", roomID)
        return false, natsrouter.ErrInternal("failed to check authorization")
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

- [ ] **Step 3: Run `make test SERVICE=history-service`** — all authorization tests should PASS.

- [ ] **Commit:** `git commit -m "feat: add canModify authorization helper"`

---

### Task 6: Add `maxContentBytes` constant

**Files:**
- Modify: `history-service/internal/service/messages.go`

- [ ] **Step 1: Add constant** at the top of `messages.go`:

```go
const maxContentBytes = 20 * 1024 // 20 KB, same as message-gatekeeper
```

- [ ] **Step 2: Run `make build SERVICE=history-service`** — should compile without error.

- [ ] **Commit:** `git commit -m "feat: add maxContentBytes constant to messages.go"`

---

## Section D: Cassandra Repository — `UpdateMessageContent` Method

### Task 7: Add `UpdateMessageContent` to repository with conditional table UPDATEs

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Test: `history-service/internal/cassrepo/integration_test.go`

- [ ] **Step 1: Write failing integration tests** (testcontainers) covering:
  - **Top-level edit** → `messages_by_room` and `messages_by_id` updated; `thread_messages_by_room` row count == 0 (no phantom)
  - **Thread reply edit** → `thread_messages_by_room` and `messages_by_id` updated with correct `thread_room_id` PK; no phantom in `messages_by_room`
  - **Edit on pinned message** (seed row in `pinned_messages_by_room`) → new `msg` in pinned table too
  - **Idempotency** — running the same UPDATE twice yields identical state

Example test structure (Red phase):

```go
func TestUpdateMessageContent_TopLevel(t *testing.T) {
    // setup: insert message into messages_by_id and messages_by_room
    msg := &models.Message{MessageID: "m1", RoomID: "r1", CreatedAt: time.Now(), ThreadParentID: ""}
    // ... insert via repo.SaveMessage
    
    newMsg := "edited content"
    editedAt := time.Now()
    
    err := repo.UpdateMessageContent(context.Background(), msg, newMsg, editedAt)
    require.NoError(t, err)
    
    // assert messages_by_id updated
    // assert messages_by_room updated
    // assert thread_messages_by_room empty (count == 0)
}
```

- [ ] **Step 2: Run `make test-integration SERVICE=history-service`** — tests should FAIL (method doesn't exist yet).

- [ ] **Step 3: Add method signature** to `repository.go`:

```go
// UpdateMessageContent updates the msg, edited_at, and updated_at fields across
// the tables that actually hold the row, determined from msg's metadata.
// See decision rule in plan — conditionally updates messages_by_room, thread_messages_by_room, pinned_messages_by_room.
// Always updates messages_by_id.
func (r *Repository) UpdateMessageContent(
    ctx context.Context,
    msg *models.Message,
    newMsg string,
    editedAt time.Time,
) error { ... }
```

- [ ] **Step 4: Implement the method** with conditional branching:

```go
func (r *Repository) UpdateMessageContent(
    ctx context.Context,
    msg *models.Message,
    newMsg string,
    editedAt time.Time,
) error {
    // Always: messages_by_id
    if err := r.session.Query(
        `UPDATE messages_by_id SET msg = ?, edited_at = ?, updated_at = ?
         WHERE message_id = ? AND created_at = ?`,
        newMsg, editedAt, editedAt, msg.MessageID, msg.CreatedAt,
    ).WithContext(ctx).Exec(); err != nil {
        return fmt.Errorf("update messages_by_id: %w", err)
    }

    // Conditional: top-level vs thread reply
    if msg.ThreadParentID == "" {
        if err := r.session.Query(
            `UPDATE messages_by_room SET msg = ?, edited_at = ?, updated_at = ?
             WHERE room_id = ? AND created_at = ? AND message_id = ?`,
            newMsg, editedAt, editedAt, msg.RoomID, msg.CreatedAt, msg.MessageID,
        ).WithContext(ctx).Exec(); err != nil {
            return fmt.Errorf("update messages_by_room: %w", err)
        }
    } else {
        if err := r.session.Query(
            `UPDATE thread_messages_by_room SET msg = ?, edited_at = ?, updated_at = ?
             WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
            newMsg, editedAt, editedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID,
        ).WithContext(ctx).Exec(); err != nil {
            return fmt.Errorf("update thread_messages_by_room: %w", err)
        }
    }

    // Pinned mirror (rare today, but keep consistent)
    if msg.PinnedAt != nil {
        if err := r.session.Query(
            `UPDATE pinned_messages_by_room SET msg = ?, edited_at = ?, updated_at = ?
             WHERE room_id = ? AND created_at = ? AND message_id = ?`,
            newMsg, editedAt, editedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID,
        ).WithContext(ctx).Exec(); err != nil {
            return fmt.Errorf("update pinned_messages_by_room: %w", err)
        }
    }

    return nil
}
```

- [ ] **Step 5: Run `make test-integration SERVICE=history-service`** — all tests should PASS.

- [ ] **Commit:** `git commit -m "feat: add UpdateMessageContent with conditional table branching"`

---

## Section E: Service Handler — `EditMessage`

### Task 8: Implement `EditMessage` handler with full unit tests

**Files:**
- Modify: `history-service/internal/service/messages.go`
- Test: `history-service/internal/service/messages_test.go`

- [ ] **Step 1: Write failing table-driven tests** covering:
  - Happy path (sender edits own message) → `EditMessageResponse` with `editedAt`, UPDATE called, event published
  - Happy path (owner edits another's message) → same
  - Message not found → `ErrNotFound`, no UPDATE, no publish
  - Wrong `roomID` → `ErrNotFound`, no UPDATE, no publish
  - Actor not authorized → `ErrForbidden`, no UPDATE, no publish
  - Empty `newMsg` (after trim) → `ErrBadRequest`, no UPDATE, no publish
  - `newMsg` > 20 KB → `ErrBadRequest`, no UPDATE, no publish
  - Cassandra UPDATE error → `ErrInternal`, no publish
  - Publisher error → log warning, reply success (best-effort)

Example test case:

```go
{
    name: "happy path: sender edits own message",
    account: "alice",
    roomID: "room1",
    messageID: "msg1",
    req: models.EditMessageRequest{MessageID: "msg1", NewMsg: "edited text"},
    setupMock: func(mr *mockRepositories) {
        mockMsg := &models.Message{MessageID: "msg1", RoomID: "room1", Sender: &models.Participant{Account: "alice"}}
        mr.messages.EXPECT().GetMessageByID(mock.Anything, "msg1").Return(mockMsg, nil)
        mr.messages.EXPECT().UpdateMessageContent(mock.Anything, mockMsg, "edited text", mock.Anything).Return(nil)
        mr.publisher.EXPECT().Publish(mock.Anything, "chat.room.room1.event", mock.MatcherFunc(func(data []byte) bool {
            var evt models.MessageEditedEvent
            json.Unmarshal(data, &evt)
            return evt.Type == "message_edited" && evt.MessageID == "msg1"
        })).Return(nil)
    },
    expectSuccess: true,
}
```

- [ ] **Step 2: Run `make test SERVICE=history-service`** — tests should FAIL (handler doesn't exist).

- [ ] **Step 3: Implement `EditMessage` handler**:

```go
// EditMessage handles chat.user.{account}.request.room.{roomID}.{siteID}.msg.edit
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
    if err != nil {
        return nil, err
    }
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
    if err := s.messages.UpdateMessageContent(c, msg, req.NewMsg, editedAt); err != nil {
        slog.Error("edit: cassandra update", "error", err, "messageID", req.MessageID)
        return nil, natsrouter.ErrInternal("failed to edit message")
    }

    // Fan out (best-effort)
    now := time.Now().UTC().UnixMilli()
    evt := models.MessageEditedEvent{
        Type: "message_edited",
        Timestamp: now,
        RoomID: roomID,
        MessageID: req.MessageID,
        NewMsg: req.NewMsg,
        EditedBy: account,
        EditedAt: editedAt.UnixMilli(),
    }
    if payload, err := json.Marshal(evt); err == nil {
        if pubErr := s.publisher.Publish(c, subject.RoomEvent(roomID), payload); pubErr != nil {
            slog.Warn("edit: publish event failed", "error", pubErr, "messageID", req.MessageID)
        }
    }

    return &models.EditMessageResponse{MessageID: req.MessageID, EditedAt: editedAt.UnixMilli()}, nil
}
```

- [ ] **Step 4: Run `make test SERVICE=history-service`** — tests should PASS.

- [ ] **Step 5: Run `make lint`** and fix any issues.

- [ ] **Commit:** `git commit -m "feat: implement EditMessage handler"`

---

## Section F: Event Types for Fan-Out

### Task 9: Add `MessageEditedEvent` type

**Files:**
- Modify: `history-service/internal/models/message.go`

- [ ] **Step 1: Write failing JSON tests** for `MessageEditedEvent`.

- [ ] **Step 2: Add type**:

```go
type MessageEditedEvent struct {
    Type      string `json:"type"`      // "message_edited"
    Timestamp int64  `json:"timestamp"` // UTC millis, event publish time (per CLAUDE.md)
    RoomID    string `json:"roomId"`
    MessageID string `json:"messageId"`
    NewMsg    string `json:"newMsg"`
    EditedBy  string `json:"editedBy"`  // actor account
    EditedAt  int64  `json:"editedAt"`  // UTC millis, domain time when edit occurred
}
```

- [ ] **Step 3: Run `make test SERVICE=history-service`** — tests should PASS.

- [ ] **Commit:** `git commit -m "feat: add MessageEditedEvent type"`

---

## Section G: Handler Registration

### Task 10: Register the edit handler

**Files:**
- Modify: `history-service/internal/service/service.go`

- [ ] **Step 1: Add registration** in `RegisterHandlers`:

```go
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, siteID string) {
    natsrouter.Register(r, subject.MsgHistoryPattern(siteID), s.LoadHistory)
    natsrouter.Register(r, subject.MsgNextPattern(siteID), s.LoadNextMessages)
    natsrouter.Register(r, subject.MsgSurroundingPattern(siteID), s.LoadSurroundingMessages)
    natsrouter.Register(r, subject.MsgGetPattern(siteID), s.GetMessageByID)
    natsrouter.Register(r, subject.MsgEditPattern(siteID), s.EditMessage) // NEW
}
```

- [ ] **Step 2: Run `make build SERVICE=history-service`** — should compile.

- [ ] **Commit:** `git commit -m "feat: register EditMessage handler"`

---

## Section H: Service-Level Integration Test

### Task 11: End-to-end test of edit flow (CQL seed → EditMessage → verify Cassandra + event publish)

**Files:**
- Test: `history-service/internal/service/integration_test.go` (or append to existing integration suite, build tag `integration`)

- [ ] **Step 1: Write a test** that:
  - Sets up in-process NATS + Cassandra (testcontainers)
  - Inserts a message directly via CQL
  - Calls `EditMessage` through the service
  - Asserts Cassandra rows were updated
  - Asserts an event was published to `chat.room.<roomID>.event`

Example structure:

```go
//go:build integration

func TestEditMessage_Integration(t *testing.T) {
    // setup cassandra + nats
    // insert message directly
    msg := &models.Message{MessageID: "m1", RoomID: "r1", ...}
    // call EditMessage
    resp, err := service.EditMessage(ctx, natsrouter.Context{...}, models.EditMessageRequest{...})
    require.NoError(t, err)
    // assert Cassandra updated
    // assert event published (recording publisher or nats subscription)
}
```

- [ ] **Step 2: Run `make test-integration SERVICE=history-service`** — test should PASS.

- [ ] **Commit:** `git commit -m "test: add EditMessage service-level integration test"`

---

## Section I: Frontend — Handle Edit Events

### Task 12: Update `MessageArea.jsx` to subscribe to and render edit events

**Files:**
- Modify: `chat-frontend/src/components/MessageArea.jsx`

- [ ] **Step 1: Extend the existing room event subscription callback**

The current code likely only handles `evt.type === 'new_message'`. Add an edit branch:

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
})
```

- [ ] **Step 2: Render edited indicator** in the message component (simple "(edited)" label next to timestamp).

- [ ] **Step 3: Manual test** in browser — open two windows on the same room, edit a message in one window, verify the other window updates in real time.

- [ ] **Commit:** `git commit -m "feat: handle message_edited events in MessageArea"`

---

## Section J: Documentation Update

### Task 13: Note that history-service now handles both reads and writes

**Files:**
- Create/modify: `history-service/README.md`

- [ ] **Step 1: Add section** describing reads and writes:

```markdown
## Read and Write Operations

`history-service` handles both synchronous read and write operations on message history.

**Reads (4 endpoints):**
- `msg.history` — Load paginated message history for a room
- `msg.next` — Load next N messages after a cursor
- `msg.surrounding` — Load messages around a target message
- `msg.get` — Retrieve a single message by ID

**Writes (2 endpoints):**
- `msg.edit` — Edit a message's content (sender or room owner only)
- `msg.delete` — Soft-delete a message (sender or room owner only)

### Write Flow

When a write succeeds:
1. Cassandra is updated atomically across all denormalized tables
2. A live event is published to `chat.room.<roomID>.event` for subscribed clients
3. The write response is returned to the client

Publish is best-effort — if the event fails to publish after a successful UPDATE, the UPDATE is retained. Clients will see the change on next history fetch.

### Known Limitations

- Users not subscribed to the room subject at publish time see the update on the next history fetch
- DM thread ordering is intentionally NOT bumped on edit/delete
- Thread parent edits do not propagate to child rows' `QuotedParentMessage` snapshot
- No audit history — `msg` is overwritten in place
```

- [ ] **Step 2: Add note** that the Cassandra schema (`docs/cassandra_message_model.md`) is unchanged — all needed columns (`edited_at`, `deleted`, `updated_at`) already exist.

- [ ] **Commit:** `git commit -m "docs: update history-service README — reads + writes, known limitations"`

---

## End of Phase 1

At the end of Phase 1, the following are complete and ready for review:

- ✅ Edit request/response types + tests
- ✅ Edit subject pattern + tests
- ✅ Shared infrastructure: `EventPublisher` interface, `SubscriptionRepository.GetSubscription`, `canModify` authorization, `maxContentBytes`
- ✅ Cassandra `UpdateMessageContent` method with conditional table branching + integration tests
- ✅ `EditMessage` service handler + unit tests
- ✅ Handler registration
- ✅ Service-level integration test
- ✅ Frontend message_edited event handling
- ✅ README updated

Phase 1 is **self-contained** and independently reviewable. It includes all shared scaffolding that Phase 2 will reuse.

**Before merging Phase 1:**
- `make lint` green
- `make test SERVICE=history-service` green
- `make test-integration SERVICE=history-service` green
- Coverage ≥80% per package, target 90%+
- Manual smoke test via `docker-local`

---

# PHASE 2 — Delete (builds on Phase 1 scaffolding)

---

## Section K: Request/Response Model Types (Delete)

### Task 14: Add delete request and response types

**Files:**
- Modify: `history-service/internal/models/message.go`

- [ ] **Step 1: Write failing JSON tests** for `DeleteMessageRequest` and `DeleteMessageResponse`.

- [ ] **Step 2: Add types**:

```go
type DeleteMessageRequest struct {
    MessageID string `json:"messageId"`
}

type DeleteMessageResponse struct {
    MessageID string `json:"messageId"`
    DeletedAt int64  `json:"deletedAt"` // UTC millis
}
```

- [ ] **Step 3: Run `make test SERVICE=history-service`** — tests should PASS.

- [ ] **Commit:** `git commit -m "feat: add DeleteMessageRequest/Response types"`

---

## Section L: NATS Subject Pattern (Delete)

### Task 15: Add delete request subject builder

**Files:**
- Modify: `pkg/subject/subject.go`
- Test: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write failing test** for `MsgDeletePattern`.

- [ ] **Step 2: Run `make test`** — test should FAIL.

- [ ] **Step 3: Implement**:

```go
func MsgDeletePattern(siteID string) string {
    return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.delete", siteID)
}
```

- [ ] **Step 4: Run `make test`** — test should PASS.

- [ ] **Commit:** `git commit -m "feat: add MsgDeletePattern subject builder"`

---

## Section M: Cassandra Repository — `SoftDeleteMessage` Method

### Task 16: Add `SoftDeleteMessage` to repository (mirrors `UpdateMessageContent` structure)

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Test: `history-service/internal/cassrepo/integration_test.go`

- [ ] **Step 1: Write failing integration tests** covering:
  - **Top-level soft delete** → `deleted=true` in `messages_by_room` and `messages_by_id`; `msg` content preserved
  - **Thread reply soft delete** → `deleted=true` in `thread_messages_by_room` and `messages_by_id`; no phantom in `messages_by_room`
  - **Idempotency** — running the same DELETE twice yields identical state

- [ ] **Step 2: Run `make test-integration SERVICE=history-service`** — tests should FAIL.

- [ ] **Step 3: Implement `SoftDeleteMessage`** (mirrors `UpdateMessageContent` branching logic):

```go
// SoftDeleteMessage sets deleted=true and updated_at across the tables that
// actually hold the row. msg content is retained. Idempotent.
func (r *Repository) SoftDeleteMessage(
    ctx context.Context,
    msg *models.Message,
    deletedAt time.Time,
) error {
    // Always: messages_by_id
    if err := r.session.Query(
        `UPDATE messages_by_id SET deleted = true, updated_at = ?
         WHERE message_id = ? AND created_at = ?`,
        deletedAt, msg.MessageID, msg.CreatedAt,
    ).WithContext(ctx).Exec(); err != nil {
        return fmt.Errorf("update messages_by_id: %w", err)
    }

    // Conditional: top-level vs thread reply
    if msg.ThreadParentID == "" {
        if err := r.session.Query(
            `UPDATE messages_by_room SET deleted = true, updated_at = ?
             WHERE room_id = ? AND created_at = ? AND message_id = ?`,
            deletedAt, msg.RoomID, msg.CreatedAt, msg.MessageID,
        ).WithContext(ctx).Exec(); err != nil {
            return fmt.Errorf("update messages_by_room: %w", err)
        }
    } else {
        if err := r.session.Query(
            `UPDATE thread_messages_by_room SET deleted = true, updated_at = ?
             WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
            deletedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID,
        ).WithContext(ctx).Exec(); err != nil {
            return fmt.Errorf("update thread_messages_by_room: %w", err)
        }
    }

    // Pinned mirror
    if msg.PinnedAt != nil {
        if err := r.session.Query(
            `UPDATE pinned_messages_by_room SET deleted = true, updated_at = ?
             WHERE room_id = ? AND created_at = ? AND message_id = ?`,
            deletedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID,
        ).WithContext(ctx).Exec(); err != nil {
            return fmt.Errorf("update pinned_messages_by_room: %w", err)
        }
    }

    return nil
}
```

- [ ] **Step 4: Run `make test-integration SERVICE=history-service`** — all tests should PASS.

- [ ] **Commit:** `git commit -m "feat: add SoftDeleteMessage with conditional table branching"`

---

## Section N: Service Handler — `DeleteMessage`

### Task 17: Implement `DeleteMessage` handler

**Files:**
- Modify: `history-service/internal/service/messages.go`
- Test: `history-service/internal/service/messages_test.go`

- [ ] **Step 1: Write failing table-driven tests** (same authorization and error path coverage as edit, but without length validation):
  - Happy path (sender deletes own) → `DeleteMessageResponse`, UPDATE called, event published
  - Happy path (owner deletes another's) → same
  - Message not found → `ErrNotFound`, no UPDATE, no publish
  - Wrong `roomID` → `ErrNotFound`, no UPDATE, no publish
  - Not authorized → `ErrForbidden`, no UPDATE, no publish
  - Cassandra UPDATE error → `ErrInternal`, no publish
  - Publisher error → log warning, reply success (best-effort)

- [ ] **Step 2: Run `make test SERVICE=history-service`** — tests should FAIL.

- [ ] **Step 3: Implement handler**:

```go
// DeleteMessage handles chat.user.{account}.request.room.{roomID}.{siteID}.msg.delete
func (s *HistoryService) DeleteMessage(c *natsrouter.Context, req models.DeleteMessageRequest) (*models.DeleteMessageResponse, error) {
    account := c.Param("account")
    roomID := c.Param("roomID")

    // Load & validate target message
    msg, err := s.messages.GetMessageByID(c, req.MessageID)
    if err != nil {
        slog.Error("delete: load message", "error", err, "messageID", req.MessageID)
        return nil, natsrouter.ErrInternal("failed to load message")
    }
    if msg == nil || msg.RoomID != roomID {
        return nil, natsrouter.ErrNotFound("message not found")
    }

    // Authorize
    allowed, err := s.canModify(c, account, roomID, msg)
    if err != nil {
        return nil, err
    }
    if !allowed {
        return nil, natsrouter.ErrForbidden("only the sender or a room owner can delete")
    }

    // Persist
    deletedAt := time.Now().UTC()
    if err := s.messages.SoftDeleteMessage(c, msg, deletedAt); err != nil {
        slog.Error("delete: cassandra update", "error", err, "messageID", req.MessageID)
        return nil, natsrouter.ErrInternal("failed to delete message")
    }

    // Fan out (best-effort)
    now := time.Now().UTC().UnixMilli()
    evt := models.MessageDeletedEvent{
        Type: "message_deleted",
        Timestamp: now,
        RoomID: roomID,
        MessageID: req.MessageID,
        DeletedBy: account,
        DeletedAt: deletedAt.UnixMilli(),
    }
    if payload, err := json.Marshal(evt); err == nil {
        if pubErr := s.publisher.Publish(c, subject.RoomEvent(roomID), payload); pubErr != nil {
            slog.Warn("delete: publish event failed", "error", pubErr, "messageID", req.MessageID)
        }
    }

    return &models.DeleteMessageResponse{MessageID: req.MessageID, DeletedAt: deletedAt.UnixMilli()}, nil
}
```

- [ ] **Step 4: Run `make test SERVICE=history-service`** — tests should PASS.

- [ ] **Step 5: Run `make lint`** — fix any issues.

- [ ] **Commit:** `git commit -m "feat: implement DeleteMessage handler"`

---

## Section O: Event Type for Fan-Out

### Task 18: Add `MessageDeletedEvent` type

**Files:**
- Modify: `history-service/internal/models/message.go`

- [ ] **Step 1: Write failing JSON tests** for `MessageDeletedEvent`.

- [ ] **Step 2: Add type**:

```go
type MessageDeletedEvent struct {
    Type      string `json:"type"`      // "message_deleted"
    Timestamp int64  `json:"timestamp"` // UTC millis, event publish time (per CLAUDE.md)
    RoomID    string `json:"roomId"`
    MessageID string `json:"messageId"`
    DeletedBy string `json:"deletedBy"`
    DeletedAt int64  `json:"deletedAt"`  // UTC millis, domain time when delete occurred
}
```

- [ ] **Step 3: Run `make test SERVICE=history-service`** — tests should PASS.

- [ ] **Commit:** `git commit -m "feat: add MessageDeletedEvent type"`

---

## Section P: Handler Registration

### Task 19: Register the delete handler

**Files:**
- Modify: `history-service/internal/service/service.go`

- [ ] **Step 1: Add registration** in `RegisterHandlers`:

```go
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, siteID string) {
    natsrouter.Register(r, subject.MsgHistoryPattern(siteID), s.LoadHistory)
    natsrouter.Register(r, subject.MsgNextPattern(siteID), s.LoadNextMessages)
    natsrouter.Register(r, subject.MsgSurroundingPattern(siteID), s.LoadSurroundingMessages)
    natsrouter.Register(r, subject.MsgGetPattern(siteID), s.GetMessageByID)
    natsrouter.Register(r, subject.MsgEditPattern(siteID), s.EditMessage)
    natsrouter.Register(r, subject.MsgDeletePattern(siteID), s.DeleteMessage) // NEW
}
```

- [ ] **Step 2: Run `make build SERVICE=history-service`** — should compile.

- [ ] **Commit:** `git commit -m "feat: register DeleteMessage handler"`

---

## Section Q: Service-Level Integration Test

### Task 20: End-to-end test of delete flow

**Files:**
- Test: `history-service/internal/service/integration_test.go`

- [ ] **Step 1: Write a test** (mirrors edit integration test) that:
  - Inserts a message directly via CQL
  - Calls `DeleteMessage` through the service
  - Asserts Cassandra rows have `deleted=true`
  - Asserts an event was published to `chat.room.<roomID>.event`

- [ ] **Step 2: Run `make test-integration SERVICE=history-service`** — test should PASS.

- [ ] **Commit:** `git commit -m "test: add DeleteMessage service-level integration test"`

---

## Section R: Frontend — Handle Delete Events

### Task 21: Update `MessageArea.jsx` to render deleted messages

**Files:**
- Modify: `chat-frontend/src/components/MessageArea.jsx`

- [ ] **Step 1: Extend the room event subscription callback**

Add a delete branch to the existing subscription:

```jsx
if (evt.type === 'message_deleted') {
    setMessages((prev) => prev.map((m) =>
        messageId(m) === evt.messageId
            ? { ...m, deleted: true }
            : m
    ))
    return
}
```

- [ ] **Step 2: Render deleted placeholder** in the message component (e.g., "[message deleted]").

- [ ] **Step 3: Manual test** in browser — open two windows on the same room, delete a message in one window, verify the other window shows "[message deleted]" in real time.

- [ ] **Commit:** `git commit -m "feat: handle message_deleted events in MessageArea"`

---

## End of Phase 2

At the end of Phase 2, the following additions are complete:

- ✅ Delete request/response types + tests
- ✅ Delete subject pattern + tests
- ✅ Cassandra `SoftDeleteMessage` method (reuses conditional branching pattern)
- ✅ `DeleteMessage` service handler (reuses `canModify` authorization)
- ✅ Handler registration
- ✅ Service-level integration test
- ✅ Frontend message_deleted event handling

Phase 2 builds entirely on Phase 1's infrastructure and adds minimal new code.

**Before merging Phase 2:**
- `make lint` green
- `make test SERVICE=history-service` green
- `make test-integration SERVICE=history-service` green
- Coverage ≥80% per package, target 90%+

---

## Quality Gates (per CLAUDE.md)

- **TDD:** Red → Green → Refactor for every task. Never write implementation before a failing test exists.
- **Coverage:** ≥80% per package, target 90%+ for handlers and store methods.
- **`make lint`**, **`make test`** (with `-race`) green before each commit.
- **`make test-integration SERVICE=history-service`** green before PR merge.
- **Pre-commit hook** runs lint + tests — fix root causes, never bypass with `--no-verify`.

## Risk Callouts

- **Denormalization fan-out:** a partial failure across tables leaves a room temporarily inconsistent. Mitigation: idempotent UPDATEs + caller retry converge. Phase 1 establishes the pattern; Phase 2 inherits it.
- **No audit trail:** owner/sender can silently rewrite/delete history. Accepted; note in PR description.
- **Thread parent drift:** deferred, documented.
- **Best-effort fan-out:** a publish failure after a successful UPDATE is logged but not retried. Clients will still see the edit/delete on next history fetch. Accepted trade-off.
