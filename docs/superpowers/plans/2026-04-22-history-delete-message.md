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



