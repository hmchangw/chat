# Delete Message Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a synchronous `msg.delete` NATS request/reply operation to `history-service`. Soft-delete only (`deleted = true, updated_at = ?`; `msg` content retained). Sender-only authorization. Best-effort live event fan-out to `chat.room.{roomID}.event`. Decrements the parent's `tcount` when deleting a thread reply. No cascade when deleting a thread parent (replies stay intact).

**Architecture:** Single-service — all logic lives in `history-service`. Handler flow: parse → `getAccessSince` (subscription check) → `GetMessageByID` (hydrate) → `canModify` (sender equality) → already-deleted short-circuit → multi-table `deleted = true` UPDATE → conditional `tcount` decrement → publish event → reply. No changes to gatekeeper, workers, broadcast-worker, or the Cassandra schema.

**Spec:** `docs/superpowers/specs/2026-04-22-delete-message-design.md`

**Tech Stack:** Go 1.25, NATS core (via `*otelnats.Conn`), Cassandra (gocql), MongoDB, `go.uber.org/mock`, `stretchr/testify`, `testcontainers-go`.

**Dependencies:** The edit plan (`2026-04-22-history-edit-message.md`) must be merged first. This plan reuses:
- The `EventPublisher` interface and its `main.go` wire-up (edit Task 1).
- The `canModify(msg, account) bool` helper (edit Task 2).
- The `MessageRepository` interface with the existing `UpdateMessageContent` method (edit Task 6).
- The `setupCassandra` test helper in `cassrepo/integration_test.go` (edit Task 7) — it already provisions `messages_by_id`, `messages_by_room`, `thread_messages_by_room`, and `pinned_messages_by_room`.
- The service-level integration-test scaffolding (edit Task 13) — `recordingPublisher`, `alwaysSubscribedRepo`, and the shared `setupCassandra` in `history-service/internal/service/integration_test.go`.

---

## File Structure

| File | Role | Status |
|---|---|---|
| `pkg/subject/subject.go` | `MsgDeletePattern` subject builder | modified |
| `pkg/subject/subject_test.go` | subject-builder unit test | modified |
| `history-service/internal/models/message.go` | `DeleteMessageRequest` / `DeleteMessageResponse` / `MessageDeletedEvent` types | modified |
| `history-service/internal/models/message_test.go` | JSON round-trip tests for new types | modified |
| `history-service/internal/service/service.go` | `MessageRepository` extension (+`SoftDeleteMessage`), `RegisterHandlers` wiring | modified |
| `history-service/internal/service/messages.go` | `DeleteMessage` handler | modified |
| `history-service/internal/service/messages_test.go` | `DeleteMessage` unit tests | modified |
| `history-service/internal/service/integration_test.go` | service-level integration tests (adds delete + parent-no-cascade) | modified |
| `history-service/internal/service/mocks/mock_repository.go` | mockgen-regenerated mocks | regenerated |
| `history-service/internal/cassrepo/repository.go` | `SoftDeleteMessage` with tcount-decrement | modified |
| `history-service/internal/cassrepo/integration_test.go` | Cassandra integration tests (delete branches + tcount decrement + parent no-cascade) | modified |
| `chat-frontend/src/components/MessageArea.jsx` | `message_deleted` branch + tombstone rendering | modified (optional, can ship separately) |

---

## Phase 1 — Contracts (Types and NATS Subject)

Two tasks. These declare the over-the-wire shapes for delete. No behavior yet — pure data and a string builder.

### Task 1 — Add delete request, response, and event types

**Files:**
- Modify: `history-service/internal/models/message.go`
- Modify: `history-service/internal/models/message_test.go`

**What this does:** Declares the three new structs the delete handler uses: `DeleteMessageRequest` (what the caller sends), `DeleteMessageResponse` (what we reply with), and `MessageDeletedEvent` (what fans out to `chat.room.{roomID}.event`).

- [ ] **Step 1: Write the failing round-trip tests**

Append to `history-service/internal/models/message_test.go` (the file already exists from the edit plan):

```go
func TestDeleteMessageRequest_JSON(t *testing.T) {
	req := DeleteMessageRequest{MessageID: "m-abc"}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{"messageId":"m-abc"}`, string(data))

	var decoded DeleteMessageRequest
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, req, decoded)
}

func TestDeleteMessageResponse_JSON(t *testing.T) {
	resp := DeleteMessageResponse{
		MessageID: "m-abc",
		DeletedAt: 1_714_000_000_000,
	}
	data, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.JSONEq(t, `{"messageId":"m-abc","deletedAt":1714000000000}`, string(data))

	var decoded DeleteMessageResponse
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, resp, decoded)
}

func TestMessageDeletedEvent_JSON(t *testing.T) {
	evt := MessageDeletedEvent{
		Type:      "message_deleted",
		Timestamp: 1_714_000_000_000,
		RoomID:    "r1",
		MessageID: "m-abc",
		DeletedBy: "alice",
		DeletedAt: 1_714_000_000_000,
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type":"message_deleted",
		"timestamp":1714000000000,
		"roomId":"r1",
		"messageId":"m-abc",
		"deletedBy":"alice",
		"deletedAt":1714000000000
	}`, string(data))

	var decoded MessageDeletedEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, evt, decoded)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
make test SERVICE=history-service
```

Expected: compilation errors — `undefined: DeleteMessageRequest`, `undefined: DeleteMessageResponse`, `undefined: MessageDeletedEvent`.

- [ ] **Step 3: Implement the types**

Edit `history-service/internal/models/message.go`. Append below the existing edit types (after `MessageEditedEvent`):

```go
// DeleteMessageRequest is the payload for soft-deleting a message.
type DeleteMessageRequest struct {
	MessageID string `json:"messageId"`
}

// DeleteMessageResponse is the reply returned by the delete handler.
// DeletedAt mirrors the updated_at value written to Cassandra (there is no
// separate deleted_at column in the current schema).
type DeleteMessageResponse struct {
	MessageID string `json:"messageId"`
	DeletedAt int64  `json:"deletedAt"` // UTC millis
}

// MessageDeletedEvent is the live event published to chat.room.{roomID}.event
// after a successful soft delete. Per CLAUDE.md, every NATS event carries a
// Timestamp (event publish time). DeletedAt is the domain time when the
// delete occurred; both are populated from a single time.Now().UTC() in the
// handler. DeletedBy equals the sender account under sender-only auth and is
// included for client rendering convenience — it is not persisted to Cassandra.
type MessageDeletedEvent struct {
	Type      string `json:"type"`      // always "message_deleted"
	Timestamp int64  `json:"timestamp"` // UTC millis, event publish time
	RoomID    string `json:"roomId"`
	MessageID string `json:"messageId"`
	DeletedBy string `json:"deletedBy"` // actor account (always == sender under sender-only auth)
	DeletedAt int64  `json:"deletedAt"` // UTC millis, domain time when delete occurred
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
make test SERVICE=history-service
```

Expected: `PASS` for `TestDeleteMessageRequest_JSON`, `TestDeleteMessageResponse_JSON`, `TestMessageDeletedEvent_JSON`. Existing edit tests also still pass.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/models/message.go history-service/internal/models/message_test.go
git commit -m "feat(history-service): add DeleteMessageRequest/Response and MessageDeletedEvent types"
```

---

### Task 2 — Add `MsgDeletePattern` subject builder

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

**What this does:** Adds the natsrouter pattern `chat.user.{account}.request.room.{roomID}.{siteID}.msg.delete`. Mirrors the existing `MsgEditPattern` style.

- [ ] **Step 1: Write the failing test**

Edit `pkg/subject/subject_test.go`. In the existing `TestSubjectBuilders` table, add alongside the other `Msg*Pattern` entries (likely adjacent to the `MsgEditPattern` row added by the edit plan):

```go
		{"MsgDeletePattern", subject.MsgDeletePattern("site-a"),
			"chat.user.{account}.request.room.{roomID}.site-a.msg.delete"},
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
make test
```

Expected: compilation error — `undefined: subject.MsgDeletePattern`.

- [ ] **Step 3: Implement the builder**

Edit `pkg/subject/subject.go`. Locate `MsgEditPattern` (added by edit plan) and append immediately after it:

```go
// MsgDeletePattern is the natsrouter pattern for soft-deleting a message.
// The {account} and {roomID} placeholders are extracted by natsrouter.
func MsgDeletePattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.delete", siteID)
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
make test
```

Expected: `PASS` for `TestSubjectBuilders/MsgDeletePattern`.

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add MsgDeletePattern natsrouter builder"
```

---

## Phase 2 — Cassandra Repository

Five tasks. `SoftDeleteMessage` is implemented incrementally across branches (top-level → thread-reply → pinned), then tcount decrement is added on top for thread replies. Each task ships with its own integration test.

### Task 3 — Extend `MessageRepository` interface with `SoftDeleteMessage` and regenerate mocks

**Files:**
- Modify: `history-service/internal/service/service.go`
- Modify: `history-service/internal/cassrepo/repository.go` (add stub)
- Regenerate: `history-service/internal/service/mocks/mock_repository.go`

**What this does:** Adds `SoftDeleteMessage` to the `MessageRepository` interface so the handler can mock it in unit tests. Adds a temporary stub in the concrete repo so the tree stays compilable between tasks. The real implementation lands in Tasks 4-7.

- [ ] **Step 1: Add the method to the interface**

Edit `history-service/internal/service/service.go`. Extend the existing `MessageRepository` interface (already has `UpdateMessageContent` from the edit plan) with the new method:

```go
type MessageRepository interface {
	GetMessagesBefore(ctx context.Context, roomID string, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesAfter(ctx context.Context, roomID string, after time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetAllMessagesAsc(ctx context.Context, roomID string, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessageByID(ctx context.Context, messageID string) (*models.Message, error)
	UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error
	SoftDeleteMessage(ctx context.Context, msg *models.Message, deletedAt time.Time) error
}
```

- [ ] **Step 2: Regenerate mocks**

```bash
make generate SERVICE=history-service
```

Expected: `history-service/internal/service/mocks/mock_repository.go` now contains a `MockMessageRepository.SoftDeleteMessage` method alongside the existing `UpdateMessageContent` mock.

- [ ] **Step 3: Add a stub in the concrete repository**

Edit `history-service/internal/cassrepo/repository.go`. Append at the end of the file:

```go
// SoftDeleteMessage is implemented incrementally across Tasks 4-7 of the
// delete plan. This stub keeps the interface contract compilable between
// tasks; Task 4 replaces it with the top-level-message branch.
func (r *Repository) SoftDeleteMessage(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
	return fmt.Errorf("SoftDeleteMessage not yet implemented")
}
```

- [ ] **Step 4: Build to verify compilation**

```bash
make build SERVICE=history-service
make test SERVICE=history-service
```

Expected: successful build; existing unit tests continue to pass.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/service.go \
	history-service/internal/service/mocks/mock_repository.go \
	history-service/internal/cassrepo/repository.go
git commit -m "feat(history-service): extend MessageRepository with SoftDeleteMessage (stub)"
```

---

### Task 4 — Implement `SoftDeleteMessage` top-level branch with integration test

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Modify: `history-service/internal/cassrepo/integration_test.go`

**What this does:** Replaces the stub with the two-table `deleted = true` UPDATE for top-level messages (`msg.ThreadParentID == ""`). The integration test asserts both tables are updated, `msg` content is preserved, and `thread_messages_by_room` stays phantom-free.

- [ ] **Step 1: Write the failing integration test**

Append to `history-service/internal/cassrepo/integration_test.go`:

```go
func TestRepository_SoftDeleteMessage_TopLevel(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-del-top"
	msgID := "m-del-top"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	// Seed a top-level message in both tables.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "original", "", false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "original", "", false,
	).Exec())

	msg := &models.Message{
		MessageID:      msgID,
		RoomID:         roomID,
		CreatedAt:      createdAt,
		Sender:         sender,
		ThreadParentID: "",
	}
	deletedAt := createdAt.Add(time.Minute)
	require.NoError(t, repo.SoftDeleteMessage(ctx, msg, deletedAt))

	// messages_by_id: deleted = true, msg retained, updated_at advanced
	var gotDeleted bool
	var gotMsg string
	var gotUpdatedAt time.Time
	require.NoError(t, session.Query(
		`SELECT deleted, msg, updated_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotDeleted, &gotMsg, &gotUpdatedAt))
	assert.True(t, gotDeleted, "messages_by_id.deleted should be true")
	assert.Equal(t, "original", gotMsg, "msg content must be preserved")
	assert.WithinDuration(t, deletedAt, gotUpdatedAt, time.Second)

	// messages_by_room: same assertions
	require.NoError(t, session.Query(
		`SELECT deleted, msg FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotDeleted, &gotMsg))
	assert.True(t, gotDeleted)
	assert.Equal(t, "original", gotMsg)

	// thread_messages_by_room must have no phantom row
	var threadCount int
	require.NoError(t, session.Query(
		`SELECT COUNT(*) FROM thread_messages_by_room WHERE room_id = ?`,
		roomID,
	).Scan(&threadCount))
	assert.Equal(t, 0, threadCount, "top-level soft-delete must not write to thread_messages_by_room")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
make test-integration SERVICE=history-service
```

Expected: FAIL — the stub returns `SoftDeleteMessage not yet implemented`.

- [ ] **Step 3: Replace the stub with the top-level implementation**

Edit `history-service/internal/cassrepo/repository.go`. Replace the stub with:

```go
// SoftDeleteMessage marks the given message deleted across all applicable
// Cassandra tables. For thread replies it also decrements the parent
// message's tcount via lightweight transactions. See the delete spec for the
// table-membership + tcount semantics.
//
// Order: (1) the `deleted = true` UPDATEs on all applicable tables, (2) the
// tcount decrement on parent tables when the target is a thread reply. On
// partial failure between the two phases, tcount can drift by one — this is
// documented and matches the existing worker-side increment drift model.
// Idempotent with respect to the `deleted` column; `updated_at` advances per
// call.
func (r *Repository) SoftDeleteMessage(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
	// Always: messages_by_id
	if err := r.session.Query(
		`UPDATE messages_by_id SET deleted = true, updated_at = ? WHERE message_id = ? AND created_at = ?`,
		deletedAt, msg.MessageID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update messages_by_id: %w", err)
	}

	// Top-level only: messages_by_room
	if msg.ThreadParentID == "" {
		if err := r.session.Query(
			`UPDATE messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			deletedAt, msg.RoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update messages_by_room: %w", err)
		}
	}

	// Thread-reply branch, pinned branch, and tcount decrement are added in Tasks 5-7.
	return nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
make test-integration SERVICE=history-service
```

Expected: `PASS` for `TestRepository_SoftDeleteMessage_TopLevel`.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/repository.go history-service/internal/cassrepo/integration_test.go
git commit -m "feat(history-service): SoftDeleteMessage top-level branch with integration test"
```

---

### Task 5 — Add thread-reply branch to `SoftDeleteMessage` with integration test

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Modify: `history-service/internal/cassrepo/integration_test.go`

**What this does:** Extends `SoftDeleteMessage` to cover thread replies (`msg.ThreadParentID != ""`). The integration test asserts the thread-table update and absence of phantom row in `messages_by_room`. **tcount decrement is NOT added in this task** — that's Task 7 (kept separate because LWT machinery is its own topic).

- [ ] **Step 1: Write the failing integration test**

Append to `history-service/internal/cassrepo/integration_test.go`:

```go
func TestRepository_SoftDeleteMessage_ThreadReply(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-del-thread"
	threadRoomID := "thread-del-1"
	parentID := "m-del-parent"
	parentCreatedAt := time.Now().UTC().Truncate(time.Millisecond)
	replyID := "m-del-reply"
	replyCreatedAt := parentCreatedAt.Add(10 * time.Second)

	// Seed the parent (so the thread_parent_created_at reference is real).
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, deleted, tcount) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		parentID, roomID, parentCreatedAt, sender, "parent", "", false, 1,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id, deleted, tcount) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		roomID, parentCreatedAt, parentID, sender, "parent", "", false, 1,
	).Exec())

	// Seed the thread reply in messages_by_id and thread_messages_by_room.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, thread_parent_created_at, thread_room_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		replyID, roomID, replyCreatedAt, sender, "reply", parentID, parentCreatedAt, threadRoomID, false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, sender, msg, thread_parent_id, thread_parent_created_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		roomID, threadRoomID, replyCreatedAt, replyID, sender, "reply", parentID, parentCreatedAt, false,
	).Exec())

	parentCreatedAtPtr := parentCreatedAt
	msg := &models.Message{
		MessageID:             replyID,
		RoomID:                roomID,
		CreatedAt:             replyCreatedAt,
		Sender:                sender,
		ThreadParentID:        parentID,
		ThreadParentCreatedAt: &parentCreatedAtPtr,
		ThreadRoomID:          threadRoomID,
	}
	deletedAt := replyCreatedAt.Add(time.Minute)
	require.NoError(t, repo.SoftDeleteMessage(ctx, msg, deletedAt))

	// messages_by_id: reply now deleted
	var gotDeleted bool
	require.NoError(t, session.Query(
		`SELECT deleted FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		replyID, replyCreatedAt,
	).Scan(&gotDeleted))
	assert.True(t, gotDeleted)

	// thread_messages_by_room: reply now deleted (full PK including thread_room_id)
	require.NoError(t, session.Query(
		`SELECT deleted FROM thread_messages_by_room WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, threadRoomID, replyCreatedAt, replyID,
	).Scan(&gotDeleted))
	assert.True(t, gotDeleted)

	// messages_by_room must NOT have a phantom row for this thread reply
	var roomCount int
	require.NoError(t, session.Query(
		`SELECT COUNT(*) FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, replyCreatedAt, replyID,
	).Scan(&roomCount))
	assert.Equal(t, 0, roomCount, "thread-reply soft-delete must not write to messages_by_room")

	// Parent's tcount should still be 1 — tcount decrement is added in Task 7.
	var gotTcount int
	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, parentCreatedAt, parentID,
	).Scan(&gotTcount))
	assert.Equal(t, 1, gotTcount, "tcount decrement is not yet implemented — expected unchanged")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
make test-integration SERVICE=history-service
```

Expected: FAIL — `TestRepository_SoftDeleteMessage_ThreadReply` — the thread-reply branch is missing, so `thread_messages_by_room` still holds `deleted = false`.

- [ ] **Step 3: Add the thread-reply branch**

Edit `history-service/internal/cassrepo/repository.go`. Replace the body of `SoftDeleteMessage` with the two-branch version:

```go
func (r *Repository) SoftDeleteMessage(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
	// Always: messages_by_id
	if err := r.session.Query(
		`UPDATE messages_by_id SET deleted = true, updated_at = ? WHERE message_id = ? AND created_at = ?`,
		deletedAt, msg.MessageID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update messages_by_id: %w", err)
	}

	// Top-level vs thread-reply: mutually exclusive.
	if msg.ThreadParentID == "" {
		if err := r.session.Query(
			`UPDATE messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			deletedAt, msg.RoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update messages_by_room: %w", err)
		}
	} else {
		if err := r.session.Query(
			`UPDATE thread_messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
			deletedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update thread_messages_by_room: %w", err)
		}
	}

	// Pinned branch and tcount decrement are added in Tasks 6-7.
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
make test-integration SERVICE=history-service
```

Expected: `PASS` for both `TestRepository_SoftDeleteMessage_TopLevel` and `TestRepository_SoftDeleteMessage_ThreadReply`.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/repository.go history-service/internal/cassrepo/integration_test.go
git commit -m "feat(history-service): SoftDeleteMessage thread-reply branch with integration test"
```

---

### Task 6 — Add pinned branch to `SoftDeleteMessage` with integration test

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Modify: `history-service/internal/cassrepo/integration_test.go`

**What this does:** Extends `SoftDeleteMessage` to additionally update `pinned_messages_by_room` when `msg.PinnedAt != nil`. Mirrors edit's Task 9 pattern. Additive — it does not replace the top-level or thread-reply branch.

- [ ] **Step 1: Write the failing integration test**

Append to `history-service/internal/cassrepo/integration_test.go`:

```go
func TestRepository_SoftDeleteMessage_Pinned(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-del-pin"
	msgID := "m-del-pin"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)
	pinnedAt := createdAt.Add(10 * time.Second)

	// Seed a top-level pinned message in all three tables.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, pinned_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "content", "", pinnedAt, false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "content", "", false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO pinned_messages_by_room (room_id, created_at, message_id, sender, msg, deleted) VALUES (?, ?, ?, ?, ?, ?)`,
		roomID, pinnedAt, msgID, sender, "content", false,
	).Exec())

	msg := &models.Message{
		MessageID:      msgID,
		RoomID:         roomID,
		CreatedAt:      createdAt,
		Sender:         sender,
		ThreadParentID: "",
		PinnedAt:       &pinnedAt,
	}
	deletedAt := createdAt.Add(time.Minute)
	require.NoError(t, repo.SoftDeleteMessage(ctx, msg, deletedAt))

	// All three tables should reflect deleted = true
	var gotDeleted bool

	require.NoError(t, session.Query(
		`SELECT deleted FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotDeleted))
	assert.True(t, gotDeleted, "messages_by_id should be deleted")

	require.NoError(t, session.Query(
		`SELECT deleted FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotDeleted))
	assert.True(t, gotDeleted, "messages_by_room should be deleted")

	require.NoError(t, session.Query(
		`SELECT deleted FROM pinned_messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, pinnedAt, msgID,
	).Scan(&gotDeleted))
	assert.True(t, gotDeleted, "pinned_messages_by_room should be deleted")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
make test-integration SERVICE=history-service
```

Expected: FAIL — `TestRepository_SoftDeleteMessage_Pinned` — the pinned branch is missing, so `pinned_messages_by_room` still has `deleted = false`.

- [ ] **Step 3: Add the pinned branch**

Edit `history-service/internal/cassrepo/repository.go`. Replace the body of `SoftDeleteMessage` with the three-branch version (still without tcount decrement — that's Task 7):

```go
func (r *Repository) SoftDeleteMessage(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
	// Always: messages_by_id
	if err := r.session.Query(
		`UPDATE messages_by_id SET deleted = true, updated_at = ? WHERE message_id = ? AND created_at = ?`,
		deletedAt, msg.MessageID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update messages_by_id: %w", err)
	}

	// Top-level vs thread-reply: mutually exclusive.
	if msg.ThreadParentID == "" {
		if err := r.session.Query(
			`UPDATE messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			deletedAt, msg.RoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update messages_by_room: %w", err)
		}
	} else {
		if err := r.session.Query(
			`UPDATE thread_messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
			deletedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update thread_messages_by_room: %w", err)
		}
	}

	// Pinned mirror — additive to either of the above.
	if msg.PinnedAt != nil {
		if err := r.session.Query(
			`UPDATE pinned_messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			deletedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update pinned_messages_by_room: %w", err)
		}
	}

	// tcount decrement for thread replies is added in Task 7.
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
make test-integration SERVICE=history-service
```

Expected: `PASS` for all three `TestRepository_SoftDeleteMessage_*` tests (top-level, thread-reply, pinned).

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/repository.go history-service/internal/cassrepo/integration_test.go
git commit -m "feat(history-service): SoftDeleteMessage pinned branch with integration test"
```

---

### Task 7 — Add tcount decrement for thread replies (LWT on `messages_by_id` and `messages_by_room`)

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Modify: `history-service/internal/cassrepo/integration_test.go`

**What this does:** When a thread reply is soft-deleted, decrement the parent's `tcount` in both `messages_by_id` and `messages_by_room` using Cassandra lightweight transactions (`IF tcount = ?`). Mirrors the increment pattern in `message-worker/store_cassandra.go:146-205`.

This task **adds a new test** for tcount-specific behavior AND **updates the Task 5 test** whose previous assertion (`tcount unchanged == 1`) becomes stale once decrement is implemented.

- [ ] **Step 1: Update the Task 5 test to expect decrement**

Edit `history-service/internal/cassrepo/integration_test.go`. Locate `TestRepository_SoftDeleteMessage_ThreadReply` (committed in Task 5). Replace its final block (the one asserting `tcount == 1` with the comment "tcount decrement is not yet implemented — expected unchanged") with:

```go
	// Parent's tcount should have been decremented from 1 to 0 — see Task 7.
	var gotTcount int
	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, parentCreatedAt, parentID,
	).Scan(&gotTcount))
	assert.Equal(t, 0, gotTcount, "tcount should be decremented on thread-reply soft-delete")
```

- [ ] **Step 2: Write a new failing test focused on tcount decrement**

Append to `history-service/internal/cassrepo/integration_test.go`:

```go
func TestRepository_SoftDeleteMessage_DecrementsParentTcount(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-tcount"
	threadRoomID := "thread-tcount"
	parentID := "m-tcount-parent"
	parentCreatedAt := time.Now().UTC().Truncate(time.Millisecond)
	replyID := "m-tcount-reply"
	replyCreatedAt := parentCreatedAt.Add(10 * time.Second)

	// Parent has tcount = 3 (three replies, of which we're about to delete one).
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, tcount, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		parentID, roomID, parentCreatedAt, sender, "parent", "", 3, false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id, tcount, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		roomID, parentCreatedAt, parentID, sender, "parent", "", 3, false,
	).Exec())

	// Seed the reply we're deleting.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, thread_parent_created_at, thread_room_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		replyID, roomID, replyCreatedAt, sender, "reply", parentID, parentCreatedAt, threadRoomID, false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, sender, msg, thread_parent_id, thread_parent_created_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		roomID, threadRoomID, replyCreatedAt, replyID, sender, "reply", parentID, parentCreatedAt, false,
	).Exec())

	parentCreatedAtPtr := parentCreatedAt
	msg := &models.Message{
		MessageID:             replyID,
		RoomID:                roomID,
		CreatedAt:             replyCreatedAt,
		Sender:                sender,
		ThreadParentID:        parentID,
		ThreadParentCreatedAt: &parentCreatedAtPtr,
		ThreadRoomID:          threadRoomID,
	}
	require.NoError(t, repo.SoftDeleteMessage(ctx, msg, replyCreatedAt.Add(time.Minute)))

	// Both tables' tcount should now be 2.
	var gotTcount int
	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		parentID, parentCreatedAt,
	).Scan(&gotTcount))
	assert.Equal(t, 2, gotTcount, "messages_by_id.tcount should decrement 3 -> 2")

	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, parentCreatedAt, parentID,
	).Scan(&gotTcount))
	assert.Equal(t, 2, gotTcount, "messages_by_room.tcount should decrement 3 -> 2")
}

func TestRepository_SoftDeleteMessage_TopLevelDoesNotTouchTcount(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-tcount-top"
	msgID := "m-tcount-top"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	// Seed a top-level message with tcount=5 (pretend it has 5 thread replies).
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, tcount, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "top", "", 5, false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id, tcount, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "top", "", 5, false,
	).Exec())

	msg := &models.Message{
		MessageID:      msgID,
		RoomID:         roomID,
		CreatedAt:      createdAt,
		Sender:         sender,
		ThreadParentID: "",
	}
	require.NoError(t, repo.SoftDeleteMessage(ctx, msg, createdAt.Add(time.Minute)))

	// tcount stays at 5 — top-level delete does not cascade / decrement (spec §8.2).
	var gotTcount int
	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotTcount))
	assert.Equal(t, 5, gotTcount, "top-level soft-delete must not touch tcount — replies are preserved (no cascade)")
}
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
make test-integration SERVICE=history-service
```

Expected: FAIL on `TestRepository_SoftDeleteMessage_ThreadReply` (tcount still 1, expected 0), FAIL on `TestRepository_SoftDeleteMessage_DecrementsParentTcount` (tcount still 3, expected 2). The top-level-doesn't-touch-tcount test passes trivially because the current implementation never touches tcount.

- [ ] **Step 4: Add the `casDecrement` helper and the `decrementParentTcount` method**

Edit `history-service/internal/cassrepo/repository.go`. Add these additions near the top of the file (after the scan-dest helpers, before the `Repository` type declaration):

```go
// casMaxRetries mirrors the constant used by message-worker's tcount
// increment. A conflict means another thread-reply increment or decrement
// landed between our read and CAS; 16 retries are sufficient for realistic
// bursts while bounding the loop.
const casMaxRetries = 16

// casDecrement atomically decrements a nullable INT counter toward zero
// (clamping at zero). Mirrors the shape of message-worker's casIncrement at
// message-worker/store_cassandra.go:127 but decrements instead of increments.
func casDecrement(maxRetries int, initial *int, update func(newVal int, expected *int) (applied bool, current *int, err error)) error {
	tcount := initial
	for range maxRetries {
		newVal := 0
		if tcount != nil && *tcount > 0 {
			newVal = *tcount - 1
		}
		applied, current, err := update(newVal, tcount)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
		tcount = current
	}
	return fmt.Errorf("cas decrement exceeded %d retries", maxRetries)
}
```

Append a new method after `SoftDeleteMessage`:

```go
// decrementParentTcount decrements tcount on the parent message row in both
// messages_by_id and messages_by_room using Cassandra Lightweight Transactions
// (IF tcount = ?). Silently skips if ThreadParentCreatedAt is nil or if the
// parent row is missing (ErrNotFound).
func (r *Repository) decrementParentTcount(ctx context.Context, msg *models.Message) error {
	if msg.ThreadParentCreatedAt == nil {
		return nil
	}
	parentID := msg.ThreadParentID
	parentCreatedAt := *msg.ThreadParentCreatedAt

	// CAS decrement on messages_by_id.
	var tcount *int
	if err := r.session.Query(
		`SELECT tcount FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		parentID, parentCreatedAt,
	).WithContext(ctx).Scan(&tcount); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read tcount for parent %s in messages_by_id: %w", parentID, err)
	}
	if err := casDecrement(casMaxRetries, tcount, func(newVal int, expected *int) (bool, *int, error) {
		var current *int
		applied, err := r.session.Query(
			`UPDATE messages_by_id SET tcount = ? WHERE message_id = ? AND created_at = ? IF tcount = ?`,
			newVal, parentID, parentCreatedAt, expected,
		).WithContext(ctx).ScanCAS(&current)
		return applied, current, err
	}); err != nil {
		return fmt.Errorf("cas tcount decrement in messages_by_id for parent %s: %w", parentID, err)
	}

	// CAS decrement on messages_by_room.
	if err := r.session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		msg.RoomID, parentCreatedAt, parentID,
	).WithContext(ctx).Scan(&tcount); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read tcount for parent %s in messages_by_room: %w", parentID, err)
	}
	if err := casDecrement(casMaxRetries, tcount, func(newVal int, expected *int) (bool, *int, error) {
		var current *int
		applied, err := r.session.Query(
			`UPDATE messages_by_room SET tcount = ? WHERE room_id = ? AND created_at = ? AND message_id = ? IF tcount = ?`,
			newVal, msg.RoomID, parentCreatedAt, parentID, expected,
		).WithContext(ctx).ScanCAS(&current)
		return applied, current, err
	}); err != nil {
		return fmt.Errorf("cas tcount decrement in messages_by_room for parent %s: %w", parentID, err)
	}

	return nil
}
```

Add the `errors` import to the `cassrepo/repository.go` import block if not already present.

- [ ] **Step 5: Wire `decrementParentTcount` into `SoftDeleteMessage`**

Edit `history-service/internal/cassrepo/repository.go`. At the end of `SoftDeleteMessage`, immediately before `return nil`, add:

```go
	// tcount decrement on parent for thread-reply deletes.
	if msg.ThreadParentID != "" {
		if err := r.decrementParentTcount(ctx, msg); err != nil {
			return fmt.Errorf("decrement parent tcount: %w", err)
		}
	}

	return nil
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
make test-integration SERVICE=history-service
```

Expected: `PASS` for all five `TestRepository_SoftDeleteMessage_*` tests (top-level, thread-reply, pinned, decrements-parent-tcount, top-level-does-not-touch-tcount).

- [ ] **Step 7: Commit**

```bash
git add history-service/internal/cassrepo/repository.go history-service/internal/cassrepo/integration_test.go
git commit -m "feat(history-service): decrement parent tcount on thread-reply soft-delete (LWT)"
```

---

## Phase 3 — Service Handler

Two tasks. Task 8 lands the handler plus the happy-path and short-circuit unit tests. Task 9 covers every remaining error path.

### Task 8 — Implement `DeleteMessage` handler, happy-path test, and already-deleted short-circuit test

**Files:**
- Modify: `history-service/internal/service/messages.go`
- Modify: `history-service/internal/service/messages_test.go`

**What this does:** Adds `DeleteMessage` on `HistoryService`. Flow: subscription check → hydrate → sender check → **short-circuit if already deleted** → `SoftDeleteMessage` → publish event (best-effort) → reply. Tests exercise the happy path (successful delete + event payload) and the short-circuit (no repo call, no publish, but success reply).

- [ ] **Step 1: Write the failing happy-path test**

Append to `history-service/internal/service/messages_test.go`:

```go
// --- DeleteMessage ---

func TestHistoryService_DeleteMessage_Success(t *testing.T) {
	svc, msgs, subs, pub := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "r1",
		Sender:    models.Participant{Account: "u1"},
		Deleted:   false,
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)

	msgs.EXPECT().
		SoftDeleteMessage(gomock.Any(), hydrated, gomock.Any()).
		Return(nil)

	pub.EXPECT().
		Publish(gomock.Any(), "chat.room.r1.event", gomock.Any()).
		DoAndReturn(func(_ context.Context, subj string, data []byte) error {
			var evt models.MessageDeletedEvent
			require.NoError(t, json.Unmarshal(data, &evt))
			assert.Equal(t, "message_deleted", evt.Type)
			assert.Equal(t, "r1", evt.RoomID)
			assert.Equal(t, "m-abc", evt.MessageID)
			assert.Equal(t, "u1", evt.DeletedBy)
			assert.NotZero(t, evt.Timestamp)
			assert.NotZero(t, evt.DeletedAt)
			return nil
		})

	resp, err := svc.DeleteMessage(c, models.DeleteMessageRequest{MessageID: "m-abc"})
	require.NoError(t, err)
	assert.Equal(t, "m-abc", resp.MessageID)
	assert.NotZero(t, resp.DeletedAt)
}

func TestHistoryService_DeleteMessage_AlreadyDeleted_ShortCircuits(t *testing.T) {
	svc, msgs, subs, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	priorUpdatedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "r1",
		Sender:    models.Participant{Account: "u1"},
		Deleted:   true,
		UpdatedAt: &priorUpdatedAt,
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)

	// No SoftDeleteMessage call expected. No Publish call expected. gomock will
	// fail the test if either is invoked unexpectedly.

	resp, err := svc.DeleteMessage(c, models.DeleteMessageRequest{MessageID: "m-abc"})
	require.NoError(t, err)
	assert.Equal(t, "m-abc", resp.MessageID)
	assert.Equal(t, priorUpdatedAt.UnixMilli(), resp.DeletedAt, "short-circuit should echo the existing updated_at")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
make test SERVICE=history-service
```

Expected: compilation error — `undefined: svc.DeleteMessage`.

- [ ] **Step 3: Implement the handler**

Edit `history-service/internal/service/messages.go`. Append the `DeleteMessage` method at the bottom of the file (after `EditMessage`):

```go
// DeleteMessage handles chat.user.{account}.request.room.{roomID}.{siteID}.msg.delete.
// Sender-only auth. Soft-deletes (deleted = true, updated_at = ?) across all
// applicable Cassandra tables via SoftDeleteMessage, including tcount
// decrement on the parent for thread replies. On already-deleted messages the
// handler short-circuits and returns success without repeating the UPDATEs or
// publishing a duplicate event — this prevents tcount drift on caller retry.
func (s *HistoryService) DeleteMessage(c *natsrouter.Context, req models.DeleteMessageRequest) (*models.DeleteMessageResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")

	// 1. Subscription gate.
	if _, err := s.getAccessSince(c, account, roomID); err != nil {
		return nil, err
	}

	// 2. Hydrate. findMessage does the roomID-match check and ErrNotFound handling.
	msg, err := s.findMessage(c, roomID, req.MessageID)
	if err != nil {
		return nil, err
	}

	// 3. Sender gate.
	if !canModify(msg, account) {
		return nil, natsrouter.ErrForbidden("only the sender can delete")
	}

	// 4. Already-deleted short-circuit. Echo the current updated_at as the
	// DeletedAt. Prevents tcount double-decrement on caller retry and avoids
	// duplicate message_deleted events.
	if msg.Deleted {
		var deletedAtMs int64
		if msg.UpdatedAt != nil {
			deletedAtMs = msg.UpdatedAt.UnixMilli()
		}
		return &models.DeleteMessageResponse{
			MessageID: req.MessageID,
			DeletedAt: deletedAtMs,
		}, nil
	}

	// 5. Persist.
	deletedAt := time.Now().UTC()
	if err := s.messages.SoftDeleteMessage(c, msg, deletedAt); err != nil {
		slog.Error("delete: soft-delete", "error", err, "messageID", req.MessageID)
		return nil, natsrouter.ErrInternal("failed to delete message")
	}

	// 6. Publish live event (best-effort).
	deletedAtMs := deletedAt.UnixMilli()
	evt := models.MessageDeletedEvent{
		Type:      "message_deleted",
		Timestamp: deletedAtMs,
		RoomID:    roomID,
		MessageID: req.MessageID,
		DeletedBy: account,
		DeletedAt: deletedAtMs,
	}
	if payload, err := json.Marshal(evt); err == nil {
		if pubErr := s.publisher.Publish(c, subject.RoomEvent(roomID), payload); pubErr != nil {
			slog.Warn("delete: publish event failed", "error", pubErr, "messageID", req.MessageID)
		}
	} else {
		slog.Warn("delete: marshal event failed", "error", err, "messageID", req.MessageID)
	}

	return &models.DeleteMessageResponse{
		MessageID: req.MessageID,
		DeletedAt: deletedAtMs,
	}, nil
}
```

No new imports are needed — `encoding/json`, `log/slog`, `time`, `natsrouter`, and `subject` are already imported by the edit handler.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
make test SERVICE=history-service
```

Expected: `PASS` for `TestHistoryService_DeleteMessage_Success` and `TestHistoryService_DeleteMessage_AlreadyDeleted_ShortCircuits`.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/messages.go history-service/internal/service/messages_test.go
git commit -m "feat(history-service): implement DeleteMessage handler with short-circuit"
```

---

### Task 9 — Add error-path unit tests for `DeleteMessage`

**Files:**
- Modify: `history-service/internal/service/messages_test.go`

**What this does:** Exercises every non-happy branch of the delete handler: subscription failure, sender mismatch, message-not-found, wrong room, Cassandra UPDATE failure, publisher failure. One test per scenario to lock each error branch.

- [ ] **Step 1: Write the failing error-path tests**

Append to `history-service/internal/service/messages_test.go`:

```go
func TestHistoryService_DeleteMessage_NotSubscribed(t *testing.T) {
	svc, _, subs, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, false, nil)

	resp, err := svc.DeleteMessage(c, models.DeleteMessageRequest{MessageID: "m-abc"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeForbidden, routeErr.Code)
	assert.Equal(t, "not subscribed to room", routeErr.Message)
}

func TestHistoryService_DeleteMessage_NotSender(t *testing.T) {
	svc, msgs, subs, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "r1",
		Sender:    models.Participant{Account: "someone-else"},
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)

	resp, err := svc.DeleteMessage(c, models.DeleteMessageRequest{MessageID: "m-abc"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeForbidden, routeErr.Code)
	assert.Equal(t, "only the sender can delete", routeErr.Message)
}

func TestHistoryService_DeleteMessage_NotFound(t *testing.T) {
	svc, msgs, subs, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
	msgs.EXPECT().GetMessageByID(gomock.Any(), "missing").Return(nil, nil)

	resp, err := svc.DeleteMessage(c, models.DeleteMessageRequest{MessageID: "missing"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeNotFound, routeErr.Code)
}

func TestHistoryService_DeleteMessage_WrongRoom(t *testing.T) {
	svc, msgs, subs, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "other-room",
		Sender:    models.Participant{Account: "u1"},
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)

	resp, err := svc.DeleteMessage(c, models.DeleteMessageRequest{MessageID: "m-abc"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeNotFound, routeErr.Code)
}

func TestHistoryService_DeleteMessage_SoftDeleteFails(t *testing.T) {
	svc, msgs, subs, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "r1",
		Sender:    models.Participant{Account: "u1"},
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)
	msgs.EXPECT().
		SoftDeleteMessage(gomock.Any(), hydrated, gomock.Any()).
		Return(fmt.Errorf("cassandra timeout"))

	// No Publish call expected when the UPDATE fails.

	resp, err := svc.DeleteMessage(c, models.DeleteMessageRequest{MessageID: "m-abc"})
	assert.Nil(t, resp)
	assertInternalErr(t, err, "failed to delete message")
}

func TestHistoryService_DeleteMessage_PublishFails(t *testing.T) {
	svc, msgs, subs, pub := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "r1",
		Sender:    models.Participant{Account: "u1"},
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)
	msgs.EXPECT().SoftDeleteMessage(gomock.Any(), hydrated, gomock.Any()).Return(nil)

	pub.EXPECT().Publish(gomock.Any(), "chat.room.r1.event", gomock.Any()).Return(fmt.Errorf("nats disconnected"))

	resp, err := svc.DeleteMessage(c, models.DeleteMessageRequest{MessageID: "m-abc"})
	require.NoError(t, err, "best-effort publish: failure is logged, not returned")
	require.NotNil(t, resp)
	assert.Equal(t, "m-abc", resp.MessageID)
}
```

- [ ] **Step 2: Run the tests to verify they pass**

```bash
make test SERVICE=history-service
```

Expected: `PASS` for all eight `TestHistoryService_DeleteMessage_*` tests. No new production code is needed — these tests exercise branches already implemented in Task 8.

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/service/messages_test.go
git commit -m "test(history-service): add DeleteMessage error-path unit tests"
```

---

## Phase 4 — Handler Registration and Service-Level Integration Tests

Two tasks. Task 10 wires the handler to NATS. Task 11 adds service-level integration tests, including the critical parent-no-cascade verification.

### Task 10 — Register `DeleteMessage` in `RegisterHandlers`

**Files:**
- Modify: `history-service/internal/service/service.go`

**What this does:** Adds one `natsrouter.Register` line so the router dispatches `chat.user.{account}.request.room.{roomID}.{siteID}.msg.delete` requests to `s.DeleteMessage`.

- [ ] **Step 1: Add the registration**

Edit `history-service/internal/service/service.go`. Extend `RegisterHandlers` (which already lists the edit handler from the edit plan) with the new line:

```go
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, siteID string) {
	natsrouter.Register(r, subject.MsgHistoryPattern(siteID), s.LoadHistory)
	natsrouter.Register(r, subject.MsgNextPattern(siteID), s.LoadNextMessages)
	natsrouter.Register(r, subject.MsgSurroundingPattern(siteID), s.LoadSurroundingMessages)
	natsrouter.Register(r, subject.MsgGetPattern(siteID), s.GetMessageByID)
	natsrouter.Register(r, subject.MsgEditPattern(siteID), s.EditMessage)
	natsrouter.Register(r, subject.MsgDeletePattern(siteID), s.DeleteMessage)
}
```

- [ ] **Step 2: Build and run all tests**

```bash
make build SERVICE=history-service
make test SERVICE=history-service
```

Expected: build succeeds; all unit tests pass.

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/service/service.go
git commit -m "feat(history-service): register DeleteMessage handler in RegisterHandlers"
```

---

### Task 11 — Service-level integration tests (delete flow + parent-no-cascade)

**Files:**
- Modify: `history-service/internal/service/integration_test.go`

**What this does:** Adds two integration tests to the file introduced by the edit plan. The first exercises the full delete flow end-to-end (real Cassandra via testcontainers + `recordingPublisher`). The second verifies the parent-thread-delete no-cascade behavior from spec §8.2 — when a thread parent is soft-deleted, its replies stay `deleted = false` and `tcount` on the parent is preserved.

- [ ] **Step 1: Write the failing integration tests**

Append to `history-service/internal/service/integration_test.go`:

```go
func TestDeleteMessage_Integration(t *testing.T) {
	session := setupCassandra(t)
	repo := cassrepo.NewRepository(session)
	pub := &recordingPublisher{}
	svc := service.New(repo, alwaysSubscribedRepo{}, pub)

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "r-del-integ"
	msgID := "m-del-integ"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	// Seed a top-level message directly via CQL.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "content", "", false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "content", "", false,
	).Exec())

	c := natsrouter.NewContext(map[string]string{"account": "alice", "roomID": roomID})
	resp, err := svc.DeleteMessage(c, models.DeleteMessageRequest{MessageID: msgID})
	require.NoError(t, err)
	assert.Equal(t, msgID, resp.MessageID)
	assert.NotZero(t, resp.DeletedAt)

	// Cassandra: both tables flipped to deleted = true; msg content preserved.
	var gotDeleted bool
	var gotMsg string
	require.NoError(t, session.Query(
		`SELECT deleted, msg FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotDeleted, &gotMsg))
	assert.True(t, gotDeleted)
	assert.Equal(t, "content", gotMsg, "msg content must be retained on soft-delete")

	require.NoError(t, session.Query(
		`SELECT deleted, msg FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotDeleted, &gotMsg))
	assert.True(t, gotDeleted)
	assert.Equal(t, "content", gotMsg)

	// Publisher: exactly one message_deleted event on the room subject.
	pub.mu.Lock()
	defer pub.mu.Unlock()
	require.Len(t, pub.sent, 1)
	assert.Equal(t, "chat.room."+roomID+".event", pub.sent[0].Subject)

	var evt models.MessageDeletedEvent
	require.NoError(t, json.Unmarshal(pub.sent[0].Data, &evt))
	assert.Equal(t, "message_deleted", evt.Type)
	assert.Equal(t, roomID, evt.RoomID)
	assert.Equal(t, msgID, evt.MessageID)
	assert.Equal(t, "alice", evt.DeletedBy)
	assert.NotZero(t, evt.Timestamp)
	assert.NotZero(t, evt.DeletedAt)
}

func TestDeleteMessage_ParentWithReplies_NoCascade(t *testing.T) {
	session := setupCassandra(t)
	repo := cassrepo.NewRepository(session)
	pub := &recordingPublisher{}
	svc := service.New(repo, alwaysSubscribedRepo{}, pub)

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "r-parent-cascade"
	threadRoomID := "thread-parent-cascade"
	parentID := "m-parent-casc"
	parentCreatedAt := time.Now().UTC().Truncate(time.Millisecond)
	replyID := "m-reply-survives"
	replyCreatedAt := parentCreatedAt.Add(10 * time.Second)

	// Parent top-level message with tcount = 1 reflecting the one existing reply.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, tcount, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		parentID, roomID, parentCreatedAt, sender, "parent question", "", 1, false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id, tcount, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		roomID, parentCreatedAt, parentID, sender, "parent question", "", 1, false,
	).Exec())

	// Reply authored by someone else — the cascade question is specifically
	// about other users' content being preserved.
	otherSender := models.Participant{ID: "u2", Account: "bob"}
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, thread_parent_created_at, thread_room_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		replyID, roomID, replyCreatedAt, otherSender, "bob's reply", parentID, parentCreatedAt, threadRoomID, false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, sender, msg, thread_parent_id, thread_parent_created_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		roomID, threadRoomID, replyCreatedAt, replyID, otherSender, "bob's reply", parentID, parentCreatedAt, false,
	).Exec())

	// Alice (the parent's sender) deletes the parent.
	c := natsrouter.NewContext(map[string]string{"account": "alice", "roomID": roomID})
	_, err := svc.DeleteMessage(c, models.DeleteMessageRequest{MessageID: parentID})
	require.NoError(t, err)

	// Parent is soft-deleted.
	var gotDeleted bool
	require.NoError(t, session.Query(
		`SELECT deleted FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		parentID, parentCreatedAt,
	).Scan(&gotDeleted))
	assert.True(t, gotDeleted, "parent should be deleted")

	// Reply is untouched — no cascade. Bob's content survives.
	require.NoError(t, session.Query(
		`SELECT deleted FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		replyID, replyCreatedAt,
	).Scan(&gotDeleted))
	assert.False(t, gotDeleted, "thread reply must survive parent deletion (no cascade)")

	require.NoError(t, session.Query(
		`SELECT deleted FROM thread_messages_by_room WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, threadRoomID, replyCreatedAt, replyID,
	).Scan(&gotDeleted))
	assert.False(t, gotDeleted, "thread_messages_by_room reply must survive parent deletion")

	// Parent's tcount is preserved (no decrement on parent-delete; the parent
	// doesn't have its own parent to decrement).
	var gotTcount int
	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, parentCreatedAt, parentID,
	).Scan(&gotTcount))
	assert.Equal(t, 1, gotTcount, "parent tcount should be unchanged (replies still exist and are counted)")
}
```

- [ ] **Step 2: Run the integration tests**

```bash
make test-integration SERVICE=history-service
```

Expected: `PASS` for `TestDeleteMessage_Integration` and `TestDeleteMessage_ParentWithReplies_NoCascade`, alongside the edit tests from the edit plan and the repository-level tests from Phase 2 of this plan.

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/service/integration_test.go
git commit -m "test(history-service): add DeleteMessage integration tests (happy path + parent no-cascade)"
```

---

## Phase 5 — Frontend Integration (OPTIONAL — can ship as a separate PR)

One task. Like the edit plan's Phase 6, this is optional and can be shipped with the backend PR or in a follow-up.

### Task 12 — Handle `message_deleted` in `MessageArea.jsx`

**Files:**
- Modify: `chat-frontend/src/components/MessageArea.jsx`

**What this does:** Extends the existing `roomEvent` subscription callback to match on `evt.type === 'message_deleted'`, setting the matched message's `deleted` flag in local state. Renders "[message deleted]" placeholder when `msg.deleted === true`.

- [ ] **Step 1: Extend the subscription callback**

Edit `chat-frontend/src/components/MessageArea.jsx`. The edit plan's Task 14 already added a `message_edited` branch. Extend that block to add the delete branch. Locate the subscription callback:

```jsx
    const sub = subscribe(roomEvent(room.id), (evt) => {
      if (evt.type === 'new_message' && evt.message) {
        setMessages((prev) => {
          const id = messageId(evt.message)
          if (prev.some((m) => messageId(m) === id)) return prev
          return [...prev, evt.message]
        })
        return
      }
      if (evt.type === 'message_edited') {
        setMessages((prev) =>
          prev.map((m) =>
            messageId(m) === evt.messageId
              ? { ...m, msg: evt.newMsg, editedAt: evt.editedAt }
              : m
          )
        )
      }
    })
```

Add the delete branch (as a third `if` before the closing `})`):

```jsx
      if (evt.type === 'message_deleted') {
        setMessages((prev) =>
          prev.map((m) =>
            messageId(m) === evt.messageId ? { ...m, deleted: true } : m
          )
        )
      }
```

- [ ] **Step 2: Render the deleted placeholder**

Locate the message-content render (same block modified by the edit plan's Task 14):

```jsx
        {messages.map((msg) => (
          <div key={messageId(msg)} className="message">
            <span className="message-sender">{senderName(msg)}</span>
            <span className="message-time">{formatTime(msg.createdAt)}</span>
            {msg.editedAt && <span className="message-edited-tag">(edited)</span>}
            <div className="message-content">{messageContent(msg)}</div>
          </div>
        ))}
```

Replace the `<div className="message-content">` element with a conditional that renders either the deleted placeholder or the real content:

```jsx
        {messages.map((msg) => (
          <div key={messageId(msg)} className={`message ${msg.deleted ? 'message-deleted' : ''}`}>
            <span className="message-sender">{senderName(msg)}</span>
            <span className="message-time">{formatTime(msg.createdAt)}</span>
            {msg.editedAt && !msg.deleted && <span className="message-edited-tag">(edited)</span>}
            <div className="message-content">
              {msg.deleted ? <em>[message deleted]</em> : messageContent(msg)}
            </div>
          </div>
        ))}
```

- [ ] **Step 3: Manual test**

```bash
make dev
```

- Open two browser windows (Alice and Bob) on the same room.
- Alice posts a message, then deletes it via the new UI or a direct NATS request to `chat.user.alice.request.room.{roomID}.{siteID}.msg.delete`.
- Verify Bob's window renders "[message deleted]" in place of the original content.
- Refresh Bob's page and verify the placeholder persists (Cassandra authoritative).
- Repeat with a thread parent (Alice's) that has a reply (Bob's). Verify only the parent renders as tombstone; Bob's reply is still visible.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/components/MessageArea.jsx
git commit -m "feat(chat-frontend): handle message_deleted event and render deleted placeholder"
```

---

## Implementation Order

Phases are strictly dependency-ordered. Run them in sequence:

1. **Phase 1 — Contracts** (Tasks 1-2): types and subject pattern.
2. **Phase 2 — Cassandra repository** (Tasks 3-7): interface + stub first, then three incremental branches (top-level → thread-reply → pinned), then the tcount decrement on top.
3. **Phase 3 — Service handler** (Tasks 8-9): happy path + short-circuit first, then error paths.
4. **Phase 4 — Wiring + E2E** (Tasks 10-11): expose the handler, then integration-test including parent-no-cascade.
5. **Phase 5 — Frontend** (Task 12, optional): can ship in this PR or a follow-up.

**Prerequisite:** The edit plan (`2026-04-22-history-edit-message.md`) must be merged before this plan starts. Section 9 (Reused Shared Infrastructure) lists exactly which artifacts come from the edit plan.

## Quality Gates (per CLAUDE.md)

- TDD — Red → Green → Refactor for every task. Never write implementation before its test exists.
- `make lint` green before each commit.
- `make test SERVICE=history-service` green (with `-race`) before each commit.
- `make test-integration SERVICE=history-service` green before PR merge (Phase 2 Tasks 4-7 and Phase 4 Task 11 add integration tests).
- Coverage ≥ 80% per package; target ≥ 90% for `DeleteMessage` and `SoftDeleteMessage`.
- Pre-commit hook runs lint + tests — fix root causes, never `--no-verify`.

## Risk Callouts (tie back to spec §13)

- **Multi-table UPDATE partial failure**: idempotent retry converges on `deleted = true`. `updated_at` advances on retry.
- **tcount drift on partial failure**: if `deleted = true` commits but the tcount decrement fails, caller retry short-circuits and tcount stays +1 from reality. Matches existing worker-side increment-drift semantics. Future reconciliation job could correct.
- **Short-circuit on `msg.Deleted == true`**: prevents double-decrement on retry. Tradeoff: the handler can't recover from a "deleted=true but decrement never happened" partial-failure state through retry — this is documented and accepted.
- **Parent-thread-delete no-cascade**: deliberate product decision to respect sender-only auth. Spec §8.2.
- **`pinned_messages_by_room` branch is dead code today**: kept for correctness when a future pin operation ships.
- **Best-effort publish**: publish failure is logged, not returned. Clients see the delete on next history fetch.
- **MongoDB `rooms`/`threadRooms` not touched on delete**: inbox-sort position unchanged; "[message deleted]" shows as preview in the inbox for the current window. Consistent with edit behavior; documented.
- **No persisted "who deleted" attribution**: acceptable under sender-only auth (`DeletedBy == Sender`). A future moderator-delete would need a `deleted_by` column.

## Definition of Done

Delete PR is ready to merge when:

- [ ] All tasks in Phases 1-4 committed.
- [ ] `make lint` green.
- [ ] `make test SERVICE=history-service` green with `-race`.
- [ ] `make test-integration SERVICE=history-service` green.
- [ ] Coverage ≥ 80% per package, ≥ 90% on new handler + repo method.
- [ ] Smoke-tested against `docker-local`: `nats req chat.user.alice.request.room.r1.site-a.msg.delete '{"messageId":"m-test"}'` returns a success reply; Cassandra row shows `deleted = true`; a subscriber on `chat.room.r1.event` sees the `message_deleted` event; a thread reply's parent shows decremented `tcount`.
- [ ] Verified manually that deleting a thread parent leaves replies intact (no cascade).
- [ ] (If Phase 5 is in-scope) frontend manually verified.




