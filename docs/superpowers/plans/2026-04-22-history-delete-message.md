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
