# Edit Message Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a synchronous `msg.edit` NATS request/reply operation to `history-service`, with a best-effort live event fan-out to `chat.room.{roomID}.event`. Sender-only authorization, 20 KB content limit, conditional multi-table Cassandra UPDATEs. Introduces shared infrastructure (`EventPublisher`, `canModify`, `maxContentBytes`) that the delete plan reuses.

**Architecture:** Single-service — all logic lives in `history-service`. Handler flow: parse → `getAccessSince` (subscription check) → `GetMessageByID` (hydrate full `*models.Message` from `messages_by_id`) → `canModify` (sender equality) → content validation → conditional multi-table UPDATE → publish event → reply. No changes to gatekeeper, workers, broadcast-worker, or the Cassandra schema.

**Spec:** `docs/superpowers/specs/2026-04-22-edit-message-design.md`

**Tech Stack:** Go 1.25, NATS core (via `*otelnats.Conn`), Cassandra (gocql), MongoDB, `go.uber.org/mock`, `stretchr/testify`, `testcontainers-go`.

**Dependencies:** None. This plan ships first; the delete plan depends on its shared infrastructure.

---

## File Structure

| File | Role | Status |
|---|---|---|
| `pkg/subject/subject.go` | `MsgEditPattern` subject builder | modified |
| `pkg/subject/subject_test.go` | subject-builder unit test | modified |
| `history-service/internal/models/message.go` | `EditMessageRequest` / `EditMessageResponse` / `MessageEditedEvent` types | modified |
| `history-service/internal/models/message_test.go` | JSON round-trip tests for new types | new |
| `history-service/internal/service/service.go` | `EventPublisher` interface, `HistoryService` constructor extension, `MessageRepository` interface extension, `RegisterHandlers` wiring | modified |
| `history-service/internal/service/utils.go` | `canModify` helper | modified |
| `history-service/internal/service/utils_test.go` | `canModify` unit tests | new |
| `history-service/internal/service/messages.go` | `maxContentBytes` constant, `EditMessage` handler | modified |
| `history-service/internal/service/messages_test.go` | `EditMessage` unit tests | modified |
| `history-service/internal/service/integration_test.go` | service-level end-to-end test | new |
| `history-service/internal/service/mocks/mock_repository.go` | mockgen-regenerated mocks | regenerated |
| `history-service/internal/cassrepo/repository.go` | `UpdateMessageContent` repository method | modified |
| `history-service/internal/cassrepo/integration_test.go` | Cassandra integration tests for conditional UPDATE branching | modified |
| `history-service/cmd/main.go` | `EventPublisher` closure + `service.New` call | modified |
| `chat-frontend/src/components/MessageArea.jsx` | `message_edited` event branch + "(edited)" indicator | modified (optional, can ship separately) |

---

## Phase 1 — Shared Infrastructure

Three tasks that introduce the scaffolding the edit handler needs. None of these change user-visible behavior on their own; they set up the interface surface the subsequent phases build on. The delete plan reuses all three.

### Task 1 — Add `EventPublisher` interface and wire it through `HistoryService`

**Files:**
- Modify: `history-service/internal/service/service.go`
- Modify: `history-service/cmd/main.go`

**What this does:** Introduces the `EventPublisher` interface that the handler uses to publish live events to `chat.room.{roomID}.event`. Implemented in `main.go` by a thin struct wrapping `*otelnats.Conn.Publish`, mirroring the pattern in `broadcast-worker/main.go:152-159`. No runtime behavior changes yet.

- [ ] **Step 1: Add the interface, field, and constructor argument in `service.go`**

Replace the contents of `history-service/internal/service/service.go` with:

```go
package service

import (
	"context"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/subject"
)

//go:generate mockgen -destination=mocks/mock_repository.go -package=mocks . MessageRepository,SubscriptionRepository,EventPublisher

// MessageRepository defines Cassandra-backed message operations.
type MessageRepository interface {
	GetMessagesBefore(ctx context.Context, roomID string, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesAfter(ctx context.Context, roomID string, after time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetAllMessagesAsc(ctx context.Context, roomID string, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessageByID(ctx context.Context, messageID string) (*models.Message, error)
}

// SubscriptionRepository defines MongoDB-backed subscription lookups.
type SubscriptionRepository interface {
	GetHistorySharedSince(ctx context.Context, account, roomID string) (*time.Time, bool, error)
}

// EventPublisher publishes live events to a NATS subject. Implemented by a
// thin wrapper around *otelnats.Conn in main.go.
type EventPublisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

// HistoryService handles message history queries and (starting with this PR) mutations.
// Transport-agnostic.
type HistoryService struct {
	messages      MessageRepository
	subscriptions SubscriptionRepository
	publisher     EventPublisher
}

// New creates a HistoryService with the given repositories and event publisher.
func New(msgs MessageRepository, subs SubscriptionRepository, pub EventPublisher) *HistoryService {
	return &HistoryService{messages: msgs, subscriptions: subs, publisher: pub}
}

// RegisterHandlers wires all NATS endpoints for the history service.
// Panics if any subscription fails (startup-only, fatal if broken).
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, siteID string) {
	natsrouter.Register(r, subject.MsgHistoryPattern(siteID), s.LoadHistory)
	natsrouter.Register(r, subject.MsgNextPattern(siteID), s.LoadNextMessages)
	natsrouter.Register(r, subject.MsgSurroundingPattern(siteID), s.LoadSurroundingMessages)
	natsrouter.Register(r, subject.MsgGetPattern(siteID), s.GetMessageByID)
}
```

The `//go:generate` directive now includes `EventPublisher` so the mockgen tool generates a mock for it too.

- [ ] **Step 2: Add a publisher adapter in `main.go` and pass it to `service.New`**

Edit `history-service/cmd/main.go`. Add the `otelnats` import to the existing import block (the package is already used transitively by `natsutil.Connect`):

```go
import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/config"
	"github.com/hmchangw/chat/history-service/internal/mongorepo"
	"github.com/hmchangw/chat/history-service/internal/service"
	"github.com/hmchangw/chat/pkg/cassutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/shutdown"
)
```

Replace the service-wiring block (lines 57-59 today) with:

```go
	cassRepo := cassrepo.NewRepository(cassSession)
	mongoRepo := mongorepo.NewSubscriptionRepo(mongoClient.Database(cfg.Mongo.DB))
	publisher := &natsPublisher{nc: nc}
	svc := service.New(cassRepo, mongoRepo, publisher)
```

Add this adapter at the very bottom of `main.go` (after the closing `}` of `main`):

```go
// natsPublisher adapts *otelnats.Conn to the service.EventPublisher interface.
// Mirrors broadcast-worker/main.go:152-159.
type natsPublisher struct {
	nc *otelnats.Conn
}

func (p *natsPublisher) Publish(ctx context.Context, subject string, data []byte) error {
	return p.nc.Publish(ctx, subject, data)
}
```

- [ ] **Step 3: Regenerate mocks to include `MockEventPublisher`**

```bash
make generate SERVICE=history-service
```

Expected: `history-service/internal/service/mocks/mock_repository.go` is overwritten and now contains a `MockEventPublisher` type alongside the existing `MockMessageRepository` and `MockSubscriptionRepository`. The `//go:generate` directive added in Step 1 drives this.

- [ ] **Step 4: Update the `newService` test helper to pass a mock publisher**

Edit `history-service/internal/service/messages_test.go`. Find the existing helper (around line 30):

```go
func newService(t *testing.T) (*service.HistoryService, *mocks.MockMessageRepository, *mocks.MockSubscriptionRepository) {
	ctrl := gomock.NewController(t)
	msgs := mocks.NewMockMessageRepository(ctrl)
	subs := mocks.NewMockSubscriptionRepository(ctrl)
	return service.New(msgs, subs), msgs, subs
}
```

Replace it with:

```go
func newService(t *testing.T) (*service.HistoryService, *mocks.MockMessageRepository, *mocks.MockSubscriptionRepository, *mocks.MockEventPublisher) {
	ctrl := gomock.NewController(t)
	msgs := mocks.NewMockMessageRepository(ctrl)
	subs := mocks.NewMockSubscriptionRepository(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)
	return service.New(msgs, subs, pub), msgs, subs, pub
}
```

Every existing call site for `newService` in `messages_test.go` currently destructures into `svc, msgs, subs`. Update each of those calls to destructure into `svc, msgs, subs, _` (the edit/delete tests will consume the publisher later; read-only tests ignore it). Do a find-and-replace:

```bash
grep -n "newService(t)" history-service/internal/service/messages_test.go
```

For each line matching `svc, msgs, subs := newService(t)`, change the left-hand side to `svc, msgs, subs, _ := newService(t)`. Some tests discard additional return values (e.g. `_, msgs, subs := newService(t)` becomes `_, msgs, subs, _ := newService(t)`). Keep the ordering consistent with the new helper signature.

- [ ] **Step 5: Build and run tests to verify nothing is broken**

```bash
make build SERVICE=history-service
make test SERVICE=history-service
```

Expected: successful build and all existing tests (`TestHistoryService_LoadHistory_*`, `TestHistoryService_LoadNextMessages_*`, etc.) continue to pass. No behavior regression.

- [ ] **Step 6: Commit**

```bash
git add history-service/internal/service/service.go \
	history-service/internal/service/mocks/mock_repository.go \
	history-service/internal/service/messages_test.go \
	history-service/cmd/main.go
git commit -m "feat(history-service): add EventPublisher interface and wire in main.go"
```

---

### Task 2 — Add `canModify` authorization helper

**Files:**
- Modify: `history-service/internal/service/utils.go`
- Create: `history-service/internal/service/utils_test.go`

**What this does:** Adds a pure equality helper that the edit and delete handlers call to gate modifications by sender identity. No context, no dependencies, no mocks — a single boolean check against the hydrated message's `Sender.Account`.

- [ ] **Step 1: Write the failing unit tests**

Create `history-service/internal/service/utils_test.go` with:

```go
package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/history-service/internal/models"
)

func TestCanModify(t *testing.T) {
	tests := []struct {
		name    string
		msg     *models.Message
		account string
		want    bool
	}{
		{
			name:    "sender matches — allowed",
			msg:     &models.Message{Sender: models.Participant{Account: "alice"}},
			account: "alice",
			want:    true,
		},
		{
			name:    "different account — not allowed",
			msg:     &models.Message{Sender: models.Participant{Account: "alice"}},
			account: "bob",
			want:    false,
		},
		{
			name:    "empty sender account — not allowed",
			msg:     &models.Message{Sender: models.Participant{Account: ""}},
			account: "",
			want:    false,
		},
		{
			name:    "empty caller account — not allowed even if sender is set",
			msg:     &models.Message{Sender: models.Participant{Account: "alice"}},
			account: "",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canModify(tt.msg, tt.account)
			assert.Equal(t, tt.want, got)
		})
	}
}
```

Note: the `Sender` field on the `cassandra.Message` struct is a value type (`Participant`, not `*Participant`), so we don't need to construct it via pointer. Check `pkg/model/cassandra/message.go:63`.

- [ ] **Step 2: Run the test to verify it fails**

```bash
make test SERVICE=history-service
```

Expected: compilation error — `undefined: canModify` — because the function doesn't exist yet.

- [ ] **Step 3: Implement `canModify`**

Edit `history-service/internal/service/utils.go`. Add the helper at the bottom of the file, after `timeMax`:

```go
// canModify reports whether the given account is authorized to edit or
// soft-delete the target message. Under sender-only authorization, the caller
// must be the message's original sender; room-owner roles are not honored.
// The helper treats an empty account on either side as unauthorized to avoid
// matching messages with missing sender data.
func canModify(msg *models.Message, account string) bool {
	if account == "" {
		return false
	}
	if msg.Sender.Account == "" {
		return false
	}
	return msg.Sender.Account == account
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
make test SERVICE=history-service
```

Expected: `PASS` for `TestCanModify` and all four sub-tests.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/utils.go history-service/internal/service/utils_test.go
git commit -m "feat(history-service): add canModify sender-equality helper"
```

---

### Task 3 — Add `maxContentBytes` constant

**Files:**
- Modify: `history-service/internal/service/messages.go`

**What this does:** Adds the 20 KB content-size limit constant that the edit handler uses for `newMsg` validation. Mirrors `message-gatekeeper`'s existing limit (duplicating a single `const` is acceptable to keep this PR focused; extraction into `pkg/` is a future cleanup).

- [ ] **Step 1: Add the constant**

Edit `history-service/internal/service/messages.go`. Extend the existing `const` block (lines 12-16) to include `maxContentBytes`:

```go
const (
	defaultPageSize     = 20
	surroundingPageSize = 50
	maxPageSize         = 100
	maxContentBytes     = 20 * 1024 // 20 KB; mirrors message-gatekeeper's content cap
)
```

- [ ] **Step 2: Build to verify compilation**

```bash
make build SERVICE=history-service
```

Expected: binary built without errors. (The constant is not yet consumed — that happens in Task 10 where the edit handler is implemented. Declaring it here keeps Phase 1 self-contained and makes later commits focused on behavior.)

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/service/messages.go
git commit -m "feat(history-service): add maxContentBytes constant (20 KB)"
```

---

## Phase 2 — Contracts (Types and NATS Subject)

Two tasks that declare the over-the-wire shapes. These are pure data and pure strings — no behavior — so the tests are JSON round-trip and exact-string equality.

### Task 4 — Add edit request, response, and event types

**Files:**
- Modify: `history-service/internal/models/message.go`
- Create: `history-service/internal/models/message_test.go`

**What this does:** Declares the three new structs the edit handler uses: `EditMessageRequest` (what the caller sends), `EditMessageResponse` (what we reply with), and `MessageEditedEvent` (what fans out to `chat.room.{roomID}.event`). JSON round-trip tests verify field names and tag stability.

- [ ] **Step 1: Write the failing round-trip tests**

Create `history-service/internal/models/message_test.go` with:

```go
package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEditMessageRequest_JSON(t *testing.T) {
	req := EditMessageRequest{
		MessageID: "m-abc",
		NewMsg:    "corrected text",
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{"messageId":"m-abc","newMsg":"corrected text"}`, string(data))

	var decoded EditMessageRequest
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, req, decoded)
}

func TestEditMessageResponse_JSON(t *testing.T) {
	resp := EditMessageResponse{
		MessageID: "m-abc",
		EditedAt:  1_714_000_000_000,
	}
	data, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.JSONEq(t, `{"messageId":"m-abc","editedAt":1714000000000}`, string(data))

	var decoded EditMessageResponse
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, resp, decoded)
}

func TestMessageEditedEvent_JSON(t *testing.T) {
	evt := MessageEditedEvent{
		Type:      "message_edited",
		Timestamp: 1_714_000_000_000,
		RoomID:    "r1",
		MessageID: "m-abc",
		NewMsg:    "corrected text",
		EditedBy:  "alice",
		EditedAt:  1_714_000_000_000,
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type":"message_edited",
		"timestamp":1714000000000,
		"roomId":"r1",
		"messageId":"m-abc",
		"newMsg":"corrected text",
		"editedBy":"alice",
		"editedAt":1714000000000
	}`, string(data))

	var decoded MessageEditedEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, evt, decoded)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
make test SERVICE=history-service
```

Expected: compilation errors — `undefined: EditMessageRequest`, `undefined: EditMessageResponse`, `undefined: MessageEditedEvent`.

- [ ] **Step 3: Implement the types**

Edit `history-service/internal/models/message.go`. Append the three new types below the existing `GetMessageByIDRequest` (after line 64):

```go
// EditMessageRequest is the payload for editing a message.
type EditMessageRequest struct {
	MessageID string `json:"messageId"`
	NewMsg    string `json:"newMsg"`
}

// EditMessageResponse is the reply returned by the edit handler.
type EditMessageResponse struct {
	MessageID string `json:"messageId"`
	EditedAt  int64  `json:"editedAt"` // UTC millis
}

// MessageEditedEvent is the live event published to chat.room.{roomID}.event
// after a successful edit. Per CLAUDE.md, every NATS event carries a
// Timestamp (event publish time). EditedAt is the domain time when the edit
// occurred; both are populated from a single time.Now().UTC() in the handler.
type MessageEditedEvent struct {
	Type      string `json:"type"`      // always "message_edited"
	Timestamp int64  `json:"timestamp"` // UTC millis, event publish time
	RoomID    string `json:"roomId"`
	MessageID string `json:"messageId"`
	NewMsg    string `json:"newMsg"`
	EditedBy  string `json:"editedBy"`  // actor account (always == message.sender.account under sender-only auth)
	EditedAt  int64  `json:"editedAt"`  // UTC millis, domain time when edit occurred
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
make test SERVICE=history-service
```

Expected: `PASS` for `TestEditMessageRequest_JSON`, `TestEditMessageResponse_JSON`, `TestMessageEditedEvent_JSON`.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/models/message.go history-service/internal/models/message_test.go
git commit -m "feat(history-service): add EditMessageRequest/Response and MessageEditedEvent types"
```

---

### Task 5 — Add `MsgEditPattern` subject builder

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

**What this does:** Adds the natsrouter pattern `chat.user.{account}.request.room.{roomID}.{siteID}.msg.edit` that the service registers. The `{account}` and `{roomID}` placeholders are extracted by natsrouter at dispatch time. Mirrors the existing `MsgGetPattern` / `MsgHistoryPattern` style.

- [ ] **Step 1: Write the failing test**

Edit `pkg/subject/subject_test.go`. Find the existing `TestSubjectBuilders` table-driven test (around line 9) and add a new case to the `tests` slice — insert near the other `MsgXxxPattern` entries:

```go
		{"MsgEditPattern", subject.MsgEditPattern("site-a"),
			"chat.user.{account}.request.room.{roomID}.site-a.msg.edit"},
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
make test
```

Expected: compilation error — `undefined: subject.MsgEditPattern`.

- [ ] **Step 3: Implement the builder**

Edit `pkg/subject/subject.go`. Locate the existing natsrouter-pattern section (near `MsgGetPattern` around line 194) and add the new builder immediately after it:

```go
// MsgEditPattern is the natsrouter pattern for editing a message.
// The {account} and {roomID} placeholders are extracted by natsrouter.
func MsgEditPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.edit", siteID)
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
make test
```

Expected: `PASS` for `TestSubjectBuilders/MsgEditPattern`.

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add MsgEditPattern natsrouter builder"
```

---

## Phase 3 — Cassandra Repository

Four tasks. The repository method `UpdateMessageContent` is implemented incrementally — one table branch at a time, each with its own integration test. This keeps commits small and makes each branch's correctness independently verifiable.

### Task 6 — Extend `MessageRepository` interface with `UpdateMessageContent` and regenerate mocks

**Files:**
- Modify: `history-service/internal/service/service.go`
- Regenerate: `history-service/internal/service/mocks/mock_repository.go` (via `make generate`)

**What this does:** Adds the `UpdateMessageContent` method to the repository interface so the handler can mock it in unit tests. The implementation lands in Tasks 7–9; this task only extends the interface contract and regenerates the mocks.

- [ ] **Step 1: Add the method to the interface**

Edit `history-service/internal/service/service.go`. Extend the `MessageRepository` interface (currently 5 methods, lines 15-22) with the new method:

```go
// MessageRepository defines Cassandra-backed message operations.
type MessageRepository interface {
	GetMessagesBefore(ctx context.Context, roomID string, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesAfter(ctx context.Context, roomID string, after time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetAllMessagesAsc(ctx context.Context, roomID string, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessageByID(ctx context.Context, messageID string) (*models.Message, error)
	UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error
}
```

- [ ] **Step 2: Regenerate mocks**

```bash
make generate SERVICE=history-service
```

Expected: `history-service/internal/service/mocks/mock_repository.go` is overwritten. The new file should contain a `MockMessageRepository.UpdateMessageContent` method.

- [ ] **Step 3: Verify build**

```bash
make build SERVICE=history-service
```

Expected: the build fails in `cassrepo` because `UpdateMessageContent` is not yet implemented on the concrete `Repository` type. **This is expected.** The next task (Task 7) adds the implementation, restoring the build.

To avoid leaving the tree in a broken state at commit time, add a temporary method stub in `history-service/internal/cassrepo/repository.go` at the end of the file:

```go
// UpdateMessageContent is implemented incrementally across Tasks 7-9 of the
// edit plan. This stub keeps the interface contract compilable between tasks;
// Task 7 replaces it with the top-level-message branch.
func (r *Repository) UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	return fmt.Errorf("UpdateMessageContent not yet implemented")
}
```

Add the `fmt` import if not already present.

Re-run `make build SERVICE=history-service` — expected: successful build.

- [ ] **Step 4: Commit**

```bash
git add history-service/internal/service/service.go \
	history-service/internal/service/mocks/mock_repository.go \
	history-service/internal/cassrepo/repository.go
git commit -m "feat(history-service): extend MessageRepository with UpdateMessageContent (stub)"
```

---

### Task 7 — Implement `UpdateMessageContent` top-level branch and integration test

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Modify: `history-service/internal/cassrepo/integration_test.go`

**What this does:** Replaces the stub with the two-table UPDATE path for top-level messages (`msg.ThreadParentID == ""`). Extends `setupCassandra` to create `thread_messages_by_room` and `pinned_messages_by_room` tables so later tests (and the no-phantom assertion in this task) can query them.

- [ ] **Step 1: Extend `setupCassandra` to create the two additional tables**

Edit `history-service/internal/cassrepo/integration_test.go`. After the `messages_by_id` `CREATE TABLE` block (ends around line 107) and **before** the `cluster.Keyspace = "chat_test"` line, insert:

```go
	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.thread_messages_by_room (
		room_id TEXT,
		thread_room_id TEXT,
		created_at TIMESTAMP,
		message_id TEXT,
		sender FROZEN<"Participant">,
		target_user FROZEN<"Participant">,
		msg TEXT,
		mentions SET<FROZEN<"Participant">>,
		attachments LIST<BLOB>,
		file FROZEN<"File">,
		card FROZEN<"Card">,
		card_action FROZEN<"CardAction">,
		tshow BOOLEAN,
		tcount INT,
		thread_parent_id TEXT,
		thread_parent_created_at TIMESTAMP,
		quoted_parent_message FROZEN<"QuotedParentMessage">,
		visible_to TEXT,
		unread BOOLEAN,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
		deleted BOOLEAN,
		type TEXT,
		sys_msg_data BLOB,
		site_id TEXT,
		edited_at TIMESTAMP,
		updated_at TIMESTAMP,
		PRIMARY KEY ((room_id), thread_room_id, created_at, message_id)
	) WITH CLUSTERING ORDER BY (thread_room_id ASC, created_at DESC, message_id DESC)`).Exec())

	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.pinned_messages_by_room (
		room_id TEXT,
		created_at TIMESTAMP,
		message_id TEXT,
		sender FROZEN<"Participant">,
		msg TEXT,
		file FROZEN<"File">,
		card FROZEN<"Card">,
		deleted BOOLEAN,
		edited_at TIMESTAMP,
		updated_at TIMESTAMP,
		PRIMARY KEY ((room_id), created_at, message_id)
	) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`).Exec())
```

- [ ] **Step 2: Write the failing integration test**

At the end of `history-service/internal/cassrepo/integration_test.go`, append:

```go
func TestRepository_UpdateMessageContent_TopLevel(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-top"
	msgID := "m-top"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	// Seed a top-level message in both tables (ThreadParentID == "").
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "original", "",
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "original", "",
	).Exec())

	msg := &models.Message{
		MessageID:      msgID,
		RoomID:         roomID,
		CreatedAt:      createdAt,
		Sender:         sender,
		ThreadParentID: "",
	}
	editedAt := createdAt.Add(time.Minute)
	require.NoError(t, repo.UpdateMessageContent(ctx, msg, "edited", editedAt))

	// messages_by_id updated
	var gotMsg, gotEditedAt any
	require.NoError(t, session.Query(
		`SELECT msg, edited_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotMsg, &gotEditedAt))
	assert.Equal(t, "edited", gotMsg)
	assert.WithinDuration(t, editedAt, gotEditedAt.(time.Time), time.Second)

	// messages_by_room updated
	require.NoError(t, session.Query(
		`SELECT msg, edited_at FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotMsg, &gotEditedAt))
	assert.Equal(t, "edited", gotMsg)

	// thread_messages_by_room must NOT have a phantom row for this message
	var threadCount int
	require.NoError(t, session.Query(
		`SELECT COUNT(*) FROM thread_messages_by_room WHERE room_id = ?`,
		roomID,
	).Scan(&threadCount))
	assert.Equal(t, 0, threadCount, "top-level edit must not write to thread_messages_by_room")
}
```

- [ ] **Step 3: Run the test to verify it fails**

```bash
make test-integration SERVICE=history-service
```

Expected: FAIL with the stub's error `UpdateMessageContent not yet implemented`.

- [ ] **Step 4: Replace the stub with the top-level implementation**

Edit `history-service/internal/cassrepo/repository.go`. Replace the stub added in Task 6 with:

```go
// UpdateMessageContent updates the msg, edited_at, and updated_at fields
// across the Cassandra tables that actually hold the row, determined from
// msg's own metadata. Top-level messages (msg.ThreadParentID == "") land in
// messages_by_room; thread replies land in thread_messages_by_room; pinned
// messages additionally land in pinned_messages_by_room. messages_by_id is
// always updated. All UPDATEs use the full PK; none is a no-op against a
// missing row — see spec doc for the Cassandra phantom-row rationale.
// Idempotent with respect to msg content; timestamps advance per call.
func (r *Repository) UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	// Always: messages_by_id
	if err := r.session.Query(
		`UPDATE messages_by_id SET msg = ?, edited_at = ?, updated_at = ? WHERE message_id = ? AND created_at = ?`,
		newMsg, editedAt, editedAt, msg.MessageID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update messages_by_id: %w", err)
	}

	// Top-level only: messages_by_room
	if msg.ThreadParentID == "" {
		if err := r.session.Query(
			`UPDATE messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			newMsg, editedAt, editedAt, msg.RoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update messages_by_room: %w", err)
		}
	}

	// Thread-reply and pinned branches are added in Tasks 8 and 9.
	return nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
make test-integration SERVICE=history-service
```

Expected: `PASS` for `TestRepository_UpdateMessageContent_TopLevel`.

- [ ] **Step 6: Commit**

```bash
git add history-service/internal/cassrepo/repository.go history-service/internal/cassrepo/integration_test.go
git commit -m "feat(history-service): UpdateMessageContent top-level branch with integration test"
```

---

### Task 8 — Add thread-reply branch to `UpdateMessageContent` with integration test

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Modify: `history-service/internal/cassrepo/integration_test.go`

**What this does:** Extends `UpdateMessageContent` to cover thread replies (`msg.ThreadParentID != ""`), which live in `thread_messages_by_room` (using `thread_room_id` as a PK component) instead of `messages_by_room`. The integration test asserts both the thread-table update and the absence of any phantom row in `messages_by_room`.

- [ ] **Step 1: Write the failing integration test**

Append to `history-service/internal/cassrepo/integration_test.go`:

```go
func TestRepository_UpdateMessageContent_ThreadReply(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-thread"
	threadRoomID := "thread-1"
	parentID := "m-parent"
	msgID := "m-reply"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	// Seed a thread reply in messages_by_id and thread_messages_by_room.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, thread_room_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "original", parentID, threadRoomID,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		roomID, threadRoomID, createdAt, msgID, sender, "original", parentID,
	).Exec())

	msg := &models.Message{
		MessageID:      msgID,
		RoomID:         roomID,
		CreatedAt:      createdAt,
		Sender:         sender,
		ThreadParentID: parentID,
		ThreadRoomID:   threadRoomID,
	}
	editedAt := createdAt.Add(time.Minute)
	require.NoError(t, repo.UpdateMessageContent(ctx, msg, "edited", editedAt))

	// messages_by_id updated
	var gotMsg string
	require.NoError(t, session.Query(
		`SELECT msg FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotMsg))
	assert.Equal(t, "edited", gotMsg)

	// thread_messages_by_room updated (verify with the full PK including thread_room_id)
	require.NoError(t, session.Query(
		`SELECT msg FROM thread_messages_by_room WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, threadRoomID, createdAt, msgID,
	).Scan(&gotMsg))
	assert.Equal(t, "edited", gotMsg)

	// messages_by_room must NOT have a phantom row for this thread reply
	var roomCount int
	require.NoError(t, session.Query(
		`SELECT COUNT(*) FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&roomCount))
	assert.Equal(t, 0, roomCount, "thread-reply edit must not write to messages_by_room")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
make test-integration SERVICE=history-service
```

Expected: FAIL — `TestRepository_UpdateMessageContent_ThreadReply` — the thread-reply branch is missing, so the SELECT from `thread_messages_by_room` returns the original `"original"` content.

- [ ] **Step 3: Add the thread-reply branch**

Edit `history-service/internal/cassrepo/repository.go`. Replace the body of `UpdateMessageContent` with the version below (top-level branch plus the new thread branch):

```go
func (r *Repository) UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	// Always: messages_by_id
	if err := r.session.Query(
		`UPDATE messages_by_id SET msg = ?, edited_at = ?, updated_at = ? WHERE message_id = ? AND created_at = ?`,
		newMsg, editedAt, editedAt, msg.MessageID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update messages_by_id: %w", err)
	}

	// Top-level vs thread-reply: mutually exclusive.
	if msg.ThreadParentID == "" {
		if err := r.session.Query(
			`UPDATE messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			newMsg, editedAt, editedAt, msg.RoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update messages_by_room: %w", err)
		}
	} else {
		if err := r.session.Query(
			`UPDATE thread_messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
			newMsg, editedAt, editedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update thread_messages_by_room: %w", err)
		}
	}

	// Pinned branch is added in Task 9.
	return nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
make test-integration SERVICE=history-service
```

Expected: `PASS` for both `TestRepository_UpdateMessageContent_TopLevel` and `TestRepository_UpdateMessageContent_ThreadReply`.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/repository.go history-service/internal/cassrepo/integration_test.go
git commit -m "feat(history-service): UpdateMessageContent thread-reply branch with integration test"
```

---

### Task 9 — Add pinned branch to `UpdateMessageContent` with integration test

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Modify: `history-service/internal/cassrepo/integration_test.go`

**What this does:** Extends `UpdateMessageContent` to additionally update `pinned_messages_by_room` when `msg.PinnedAt != nil`. The pinned branch is *additive* (it does not replace the top-level or thread-reply branch); a pinned message is either top-level + pinned or thread-reply + pinned, but in both cases the pinned row must be kept in sync. No pin operation exists in the codebase today, so this branch is dead code in production but is required for future correctness.

- [ ] **Step 1: Write the failing integration test**

Append to `history-service/internal/cassrepo/integration_test.go`:

```go
func TestRepository_UpdateMessageContent_Pinned(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-pin"
	msgID := "m-pin"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)
	pinnedAt := createdAt.Add(10 * time.Second)

	// Seed a top-level pinned message in all three tables.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, pinned_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "original", "", pinnedAt,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "original", "",
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO pinned_messages_by_room (room_id, created_at, message_id, sender, msg) VALUES (?, ?, ?, ?, ?)`,
		roomID, pinnedAt, msgID, sender, "original",
	).Exec())

	msg := &models.Message{
		MessageID:      msgID,
		RoomID:         roomID,
		CreatedAt:      createdAt,
		Sender:         sender,
		ThreadParentID: "",
		PinnedAt:       &pinnedAt,
	}
	editedAt := createdAt.Add(time.Minute)
	require.NoError(t, repo.UpdateMessageContent(ctx, msg, "edited", editedAt))

	// All three affected tables updated
	var gotMsg string

	require.NoError(t, session.Query(
		`SELECT msg FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotMsg))
	assert.Equal(t, "edited", gotMsg, "messages_by_id should reflect the edit")

	require.NoError(t, session.Query(
		`SELECT msg FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotMsg))
	assert.Equal(t, "edited", gotMsg, "messages_by_room should reflect the edit")

	require.NoError(t, session.Query(
		`SELECT msg FROM pinned_messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, pinnedAt, msgID,
	).Scan(&gotMsg))
	assert.Equal(t, "edited", gotMsg, "pinned_messages_by_room should reflect the edit")
}
```

Note: the `pinned_messages_by_room` table uses `pinnedAt` as its `created_at` clustering column (per the schema in `docker-local/cassandra/init/12-table-pinned_messages_by_room.cql`), which is why the WHERE clause in the pinned UPDATE uses `msg.PinnedAt` as the value for that column.

- [ ] **Step 2: Run the test to verify it fails**

```bash
make test-integration SERVICE=history-service
```

Expected: FAIL — `TestRepository_UpdateMessageContent_Pinned` — the pinned branch is missing, so the row in `pinned_messages_by_room` retains `"original"`.

- [ ] **Step 3: Add the pinned branch**

Edit `history-service/internal/cassrepo/repository.go`. Replace the body of `UpdateMessageContent` with the final three-branch version:

```go
func (r *Repository) UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	// Always: messages_by_id
	if err := r.session.Query(
		`UPDATE messages_by_id SET msg = ?, edited_at = ?, updated_at = ? WHERE message_id = ? AND created_at = ?`,
		newMsg, editedAt, editedAt, msg.MessageID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update messages_by_id: %w", err)
	}

	// Top-level vs thread-reply: mutually exclusive.
	if msg.ThreadParentID == "" {
		if err := r.session.Query(
			`UPDATE messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			newMsg, editedAt, editedAt, msg.RoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update messages_by_room: %w", err)
		}
	} else {
		if err := r.session.Query(
			`UPDATE thread_messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
			newMsg, editedAt, editedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update thread_messages_by_room: %w", err)
		}
	}

	// Pinned mirror — additive to either of the above.
	if msg.PinnedAt != nil {
		if err := r.session.Query(
			`UPDATE pinned_messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			newMsg, editedAt, editedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("update pinned_messages_by_room: %w", err)
		}
	}

	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
make test-integration SERVICE=history-service
```

Expected: `PASS` for all three `TestRepository_UpdateMessageContent_*` tests (top-level, thread-reply, pinned).

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/repository.go history-service/internal/cassrepo/integration_test.go
git commit -m "feat(history-service): UpdateMessageContent pinned branch with integration test"
```

---

## Phase 4 — Service Handler

Two tasks. Task 10 lands the handler plus the happy-path unit test (minimum to prove the full flow). Task 11 covers every error path as a table-driven test, which is a big chunk on its own.

### Task 10 — Implement `EditMessage` handler and happy-path unit test

**Files:**
- Modify: `history-service/internal/service/messages.go`
- Modify: `history-service/internal/service/messages_test.go`

**What this does:** Adds the `EditMessage` method on `HistoryService`. Flow: subscription check via `getAccessSince` → hydrate via `findMessage` (which already does the roomID match and ErrNotFound handling) → sender check via `canModify` → content validation → `UpdateMessageContent` → publish event (best-effort) → reply. The happy-path test verifies every mock is called with the right arguments, including the published event's payload shape.

- [ ] **Step 1: Write the failing happy-path test**

Append to `history-service/internal/service/messages_test.go`:

```go
// --- EditMessage ---

func TestHistoryService_EditMessage_Success(t *testing.T) {
	svc, msgs, subs, pub := newService(t)
	c := testContext()

	// Subscription check passes (accessSince nil means full history access, non-nil also fine)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	// Message lookup returns the user's own message in the expected room.
	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "r1",
		Sender:    models.Participant{Account: "u1"},
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)

	// UPDATE succeeds. The handler passes the hydrated *Message directly.
	msgs.EXPECT().
		UpdateMessageContent(gomock.Any(), hydrated, "new content", gomock.Any()).
		Return(nil)

	// Publish succeeds. Validate the payload.
	pub.EXPECT().
		Publish(gomock.Any(), "chat.room.r1.event", gomock.Any()).
		DoAndReturn(func(_ context.Context, subj string, data []byte) error {
			var evt models.MessageEditedEvent
			require.NoError(t, json.Unmarshal(data, &evt))
			assert.Equal(t, "message_edited", evt.Type)
			assert.Equal(t, "r1", evt.RoomID)
			assert.Equal(t, "m-abc", evt.MessageID)
			assert.Equal(t, "new content", evt.NewMsg)
			assert.Equal(t, "u1", evt.EditedBy)
			assert.NotZero(t, evt.Timestamp)
			assert.NotZero(t, evt.EditedAt)
			return nil
		})

	resp, err := svc.EditMessage(c, models.EditMessageRequest{
		MessageID: "m-abc",
		NewMsg:    "new content",
	})
	require.NoError(t, err)
	assert.Equal(t, "m-abc", resp.MessageID)
	assert.NotZero(t, resp.EditedAt)
}
```

Add `context` and `encoding/json` to the test file's import block if not already present (they are needed for `DoAndReturn` and the event payload unmarshal).

- [ ] **Step 2: Run the test to verify it fails**

```bash
make test SERVICE=history-service
```

Expected: compilation error — `undefined: svc.EditMessage`. The handler does not exist yet.

- [ ] **Step 3: Implement the handler**

Edit `history-service/internal/service/messages.go`. Add these imports to the existing import block:

```go
import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/subject"
)
```

Then append the `EditMessage` method at the bottom of the file (after `GetMessageByID`):

```go
// EditMessage handles chat.user.{account}.request.room.{roomID}.{siteID}.msg.edit.
// Sender-only auth. Writes to all applicable Cassandra tables via
// UpdateMessageContent, then publishes a best-effort MessageEditedEvent to
// chat.room.{roomID}.event for live fan-out.
func (s *HistoryService) EditMessage(c *natsrouter.Context, req models.EditMessageRequest) (*models.EditMessageResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")

	// 1. Subscription gate — non-subscribers cannot probe messageID -> roomID mappings.
	if _, err := s.getAccessSince(c, account, roomID); err != nil {
		return nil, err
	}

	// 2. Hydrate. findMessage returns ErrNotFound for missing IDs and for
	// messages that belong to a different room (same error, no leak).
	msg, err := s.findMessage(c, roomID, req.MessageID)
	if err != nil {
		return nil, err
	}

	// 3. Sender gate.
	if !canModify(msg, account) {
		return nil, natsrouter.ErrForbidden("only the sender can edit")
	}

	// 4. Content validation.
	if strings.TrimSpace(req.NewMsg) == "" {
		return nil, natsrouter.ErrBadRequest("newMsg must not be empty")
	}
	if len(req.NewMsg) > maxContentBytes {
		return nil, natsrouter.ErrBadRequest("newMsg exceeds maximum size")
	}

	// 5. Persist.
	editedAt := time.Now().UTC()
	if err := s.messages.UpdateMessageContent(c, msg, req.NewMsg, editedAt); err != nil {
		slog.Error("edit: update content", "error", err, "messageID", req.MessageID)
		return nil, natsrouter.ErrInternal("failed to edit message")
	}

	// 6. Publish live event (best-effort — publish failure is logged, not returned).
	editedAtMs := editedAt.UnixMilli()
	evt := models.MessageEditedEvent{
		Type:      "message_edited",
		Timestamp: editedAtMs,
		RoomID:    roomID,
		MessageID: req.MessageID,
		NewMsg:    req.NewMsg,
		EditedBy:  account,
		EditedAt:  editedAtMs,
	}
	if payload, err := json.Marshal(evt); err == nil {
		if pubErr := s.publisher.Publish(c, subject.RoomEvent(roomID), payload); pubErr != nil {
			slog.Warn("edit: publish event failed", "error", pubErr, "messageID", req.MessageID)
		}
	} else {
		slog.Warn("edit: marshal event failed", "error", err, "messageID", req.MessageID)
	}

	return &models.EditMessageResponse{
		MessageID: req.MessageID,
		EditedAt:  editedAtMs,
	}, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
make test SERVICE=history-service
```

Expected: `PASS` for `TestHistoryService_EditMessage_Success`.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/messages.go history-service/internal/service/messages_test.go
git commit -m "feat(history-service): implement EditMessage handler with happy-path test"
```

---

### Task 11 — Add error-path unit tests for `EditMessage`

**Files:**
- Modify: `history-service/internal/service/messages_test.go`

**What this does:** Exercises every non-happy branch of the handler to lock behavior and make regressions loud. One test per scenario (not table-driven) because assertion shapes differ — some check the error code, some verify that certain mocks were **not** called, some verify the publish-warning-but-still-succeed path.

- [ ] **Step 1: Write the failing error-path tests**

Append to `history-service/internal/service/messages_test.go`:

```go
func TestHistoryService_EditMessage_NotSubscribed(t *testing.T) {
	svc, _, subs, _ := newService(t)
	c := testContext()

	// Not subscribed — the helper returns ErrForbidden before we touch anything else.
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, false, nil)

	resp, err := svc.EditMessage(c, models.EditMessageRequest{MessageID: "m-abc", NewMsg: "x"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeForbidden, routeErr.Code)
	assert.Equal(t, "not subscribed to room", routeErr.Message)
}

func TestHistoryService_EditMessage_NotSender(t *testing.T) {
	svc, msgs, subs, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	// Message exists in the expected room but a different account is the sender.
	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "r1",
		Sender:    models.Participant{Account: "someone-else"},
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)

	resp, err := svc.EditMessage(c, models.EditMessageRequest{MessageID: "m-abc", NewMsg: "x"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeForbidden, routeErr.Code)
	assert.Equal(t, "only the sender can edit", routeErr.Message)
}

func TestHistoryService_EditMessage_NotFound(t *testing.T) {
	svc, msgs, subs, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
	msgs.EXPECT().GetMessageByID(gomock.Any(), "missing").Return(nil, nil)

	resp, err := svc.EditMessage(c, models.EditMessageRequest{MessageID: "missing", NewMsg: "x"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeNotFound, routeErr.Code)
}

func TestHistoryService_EditMessage_WrongRoom(t *testing.T) {
	svc, msgs, subs, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	// Message exists but in a different room — findMessage returns ErrNotFound (no leak).
	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "other-room",
		Sender:    models.Participant{Account: "u1"},
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)

	resp, err := svc.EditMessage(c, models.EditMessageRequest{MessageID: "m-abc", NewMsg: "x"})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeNotFound, routeErr.Code)
}

func TestHistoryService_EditMessage_EmptyNewMsg(t *testing.T) {
	svc, msgs, subs, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "r1",
		Sender:    models.Participant{Account: "u1"},
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)

	resp, err := svc.EditMessage(c, models.EditMessageRequest{MessageID: "m-abc", NewMsg: "   "})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeBadRequest, routeErr.Code)
	assert.Equal(t, "newMsg must not be empty", routeErr.Message)
}

func TestHistoryService_EditMessage_TooLarge(t *testing.T) {
	svc, msgs, subs, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "r1",
		Sender:    models.Participant{Account: "u1"},
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)

	// 20 KB + 1 byte
	oversize := strings.Repeat("a", 20*1024+1)

	resp, err := svc.EditMessage(c, models.EditMessageRequest{MessageID: "m-abc", NewMsg: oversize})
	assert.Nil(t, resp)

	var routeErr *natsrouter.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, natsrouter.CodeBadRequest, routeErr.Code)
	assert.Equal(t, "newMsg exceeds maximum size", routeErr.Message)
}

func TestHistoryService_EditMessage_UpdateFails(t *testing.T) {
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
		UpdateMessageContent(gomock.Any(), hydrated, "new content", gomock.Any()).
		Return(fmt.Errorf("cassandra timeout"))

	// No publish should happen when the UPDATE fails. The mock publisher is
	// not configured to expect any call; gomock will fail the test if Publish
	// is invoked.

	resp, err := svc.EditMessage(c, models.EditMessageRequest{MessageID: "m-abc", NewMsg: "new content"})
	assert.Nil(t, resp)
	assertInternalErr(t, err, "failed to edit message")
}

func TestHistoryService_EditMessage_PublishFails(t *testing.T) {
	svc, msgs, subs, pub := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	hydrated := &models.Message{
		MessageID: "m-abc",
		RoomID:    "r1",
		Sender:    models.Participant{Account: "u1"},
	}
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)
	msgs.EXPECT().UpdateMessageContent(gomock.Any(), hydrated, "new content", gomock.Any()).Return(nil)

	// Publisher fails, but handler must still return success (best-effort fan-out).
	pub.EXPECT().Publish(gomock.Any(), "chat.room.r1.event", gomock.Any()).Return(fmt.Errorf("nats disconnected"))

	resp, err := svc.EditMessage(c, models.EditMessageRequest{MessageID: "m-abc", NewMsg: "new content"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "m-abc", resp.MessageID)
}
```

Add `strings` to the test file's import block if not already present.

- [ ] **Step 2: Run the tests to verify they pass**

```bash
make test SERVICE=history-service
```

Expected: `PASS` for all eight `TestHistoryService_EditMessage_*` tests (the one from Task 10 plus the seven new error-path cases). No new production code is needed — these tests exercise branches already implemented in Task 10.

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/service/messages_test.go
git commit -m "test(history-service): add EditMessage error-path unit tests"
```

---

## Phase 5 — Handler Registration and Service-Level Integration Test

Two tasks. Task 12 is a one-line wiring change that makes the handler reachable over NATS. Task 13 adds a new integration-test file that verifies the whole flow end-to-end against a real Cassandra, with in-memory publisher recording.

### Task 12 — Register `EditMessage` in `RegisterHandlers`

**Files:**
- Modify: `history-service/internal/service/service.go`

**What this does:** Adds one `natsrouter.Register` line so the NATS router dispatches `chat.user.{account}.request.room.{roomID}.{siteID}.msg.edit` requests to `s.EditMessage`. Until this line lands, the handler exists but receives no traffic.

- [ ] **Step 1: Add the registration**

Edit `history-service/internal/service/service.go`. Extend `RegisterHandlers` (around lines 40-47 after Phase 1) with the new line:

```go
// RegisterHandlers wires all NATS endpoints for the history service.
// Panics if any subscription fails (startup-only, fatal if broken).
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, siteID string) {
	natsrouter.Register(r, subject.MsgHistoryPattern(siteID), s.LoadHistory)
	natsrouter.Register(r, subject.MsgNextPattern(siteID), s.LoadNextMessages)
	natsrouter.Register(r, subject.MsgSurroundingPattern(siteID), s.LoadSurroundingMessages)
	natsrouter.Register(r, subject.MsgGetPattern(siteID), s.GetMessageByID)
	natsrouter.Register(r, subject.MsgEditPattern(siteID), s.EditMessage)
}
```

- [ ] **Step 2: Build and run all tests**

```bash
make build SERVICE=history-service
make test SERVICE=history-service
```

Expected: build succeeds; all unit tests pass (no new test needed — this change is purely about routing and is covered end-to-end in Task 13).

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/service/service.go
git commit -m "feat(history-service): register EditMessage handler in RegisterHandlers"
```

---

### Task 13 — Service-level integration test (real Cassandra, recording publisher)

**Files:**
- Create: `history-service/internal/service/integration_test.go`

**What this does:** Adds the first service-level integration test for history-service. Wires the real `cassrepo.Repository` against a testcontainers Cassandra, uses an in-memory "recording" publisher to capture the emitted event, and exercises the end-to-end `EditMessage` flow. This is the authoritative check that hydration + multi-table UPDATE + event publish all compose correctly under realistic storage.

The `SubscriptionRepository` is stubbed with a minimal fake (subscription-check passes) — this keeps the test focused on the edit path without standing up a MongoDB container.

- [ ] **Step 1: Write the integration test file**

Create `history-service/internal/service/integration_test.go` with:

```go
//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/cassandra"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/service"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// setupCassandra provisions a Cassandra container with the four message
// tables plus the required UDTs. Mirrors the helper in cassrepo/integration_test.go.
func setupCassandra(t *testing.T) *gocql.Session {
	t.Helper()
	ctx := context.Background()
	container, err := cassandra.Run(ctx, "cassandra:5")
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.ConnectionHost(ctx)
	require.NoError(t, err)

	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.One
	session, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })

	require.NoError(t, session.Query(`CREATE KEYSPACE IF NOT EXISTS chat_test WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`).Exec())

	for _, cql := range []string{
		`CREATE TYPE IF NOT EXISTS chat_test."Participant" (id TEXT, eng_name TEXT, company_name TEXT, app_id TEXT, app_name TEXT, is_bot BOOLEAN, account TEXT)`,
		`CREATE TYPE IF NOT EXISTS chat_test."File" (id TEXT, name TEXT, type TEXT)`,
		`CREATE TYPE IF NOT EXISTS chat_test."Card" (template TEXT, data BLOB)`,
		`CREATE TYPE IF NOT EXISTS chat_test."CardAction" (verb TEXT, text TEXT, card_id TEXT, display_text TEXT, hide_exec_log BOOLEAN, card_tmid TEXT, data BLOB)`,
		`CREATE TYPE IF NOT EXISTS chat_test."QuotedParentMessage" (message_id TEXT, room_id TEXT, sender FROZEN<"Participant">, created_at TIMESTAMP, msg TEXT, mentions SET<FROZEN<"Participant">>, attachments LIST<BLOB>, message_link TEXT)`,
	} {
		require.NoError(t, session.Query(cql).Exec())
	}

	// messages_by_room
	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages_by_room (
		room_id TEXT, created_at TIMESTAMP, message_id TEXT, thread_room_id TEXT,
		sender FROZEN<"Participant">, target_user FROZEN<"Participant">, msg TEXT,
		mentions SET<FROZEN<"Participant">>, attachments LIST<BLOB>,
		file FROZEN<"File">, card FROZEN<"Card">, card_action FROZEN<"CardAction">,
		tshow BOOLEAN, tcount INT, thread_parent_id TEXT, thread_parent_created_at TIMESTAMP,
		quoted_parent_message FROZEN<"QuotedParentMessage">, visible_to TEXT, unread BOOLEAN,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>, deleted BOOLEAN,
		type TEXT, sys_msg_data BLOB, site_id TEXT, edited_at TIMESTAMP, updated_at TIMESTAMP,
		PRIMARY KEY ((room_id), created_at, message_id)
	) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`).Exec())

	// messages_by_id
	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages_by_id (
		message_id TEXT, room_id TEXT, thread_room_id TEXT,
		sender FROZEN<"Participant">, target_user FROZEN<"Participant">, msg TEXT,
		mentions SET<FROZEN<"Participant">>, attachments LIST<BLOB>,
		file FROZEN<"File">, card FROZEN<"Card">, card_action FROZEN<"CardAction">,
		tshow BOOLEAN, tcount INT, thread_parent_id TEXT, thread_parent_created_at TIMESTAMP,
		quoted_parent_message FROZEN<"QuotedParentMessage">, visible_to TEXT, unread BOOLEAN,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>, deleted BOOLEAN,
		type TEXT, sys_msg_data BLOB, site_id TEXT, edited_at TIMESTAMP, created_at TIMESTAMP,
		updated_at TIMESTAMP, pinned_at TIMESTAMP, pinned_by FROZEN<"Participant">,
		PRIMARY KEY (message_id, created_at)
	) WITH CLUSTERING ORDER BY (created_at DESC)`).Exec())

	// thread_messages_by_room and pinned_messages_by_room aren't needed for the
	// top-level edit flow exercised here; the cassrepo integration tests cover
	// those branches directly. Keeping the setup minimal reduces container-start time.

	cluster.Keyspace = "chat_test"
	ksSession, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { ksSession.Close() })
	return ksSession
}

// recordingPublisher captures every Publish call for assertions.
type recordingPublisher struct {
	mu   sync.Mutex
	sent []recordedMessage
}

type recordedMessage struct {
	Subject string
	Data    []byte
}

func (p *recordingPublisher) Publish(_ context.Context, subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	p.sent = append(p.sent, recordedMessage{Subject: subj, Data: cp})
	return nil
}

// alwaysSubscribedRepo stubs SubscriptionRepository so the subscription gate passes.
type alwaysSubscribedRepo struct{}

func (alwaysSubscribedRepo) GetHistorySharedSince(_ context.Context, _, _ string) (*time.Time, bool, error) {
	return nil, true, nil
}

func TestEditMessage_Integration(t *testing.T) {
	session := setupCassandra(t)
	repo := cassrepo.NewRepository(session)
	pub := &recordingPublisher{}
	svc := service.New(repo, alwaysSubscribedRepo{}, pub)

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "r-integ"
	msgID := "m-integ"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	// Seed the message directly via CQL (bypassing message-worker).
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "original", "",
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "original", "",
	).Exec())

	// Call the handler directly with a prepared natsrouter.Context.
	c := natsrouter.NewContext(map[string]string{"account": "alice", "roomID": roomID})
	resp, err := svc.EditMessage(c, models.EditMessageRequest{
		MessageID: msgID,
		NewMsg:    "edited via integration test",
	})
	require.NoError(t, err)
	assert.Equal(t, msgID, resp.MessageID)
	assert.NotZero(t, resp.EditedAt)

	// Cassandra: both tables should reflect the edit.
	var gotMsg string
	require.NoError(t, session.Query(
		`SELECT msg FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotMsg))
	assert.Equal(t, "edited via integration test", gotMsg)

	require.NoError(t, session.Query(
		`SELECT msg FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotMsg))
	assert.Equal(t, "edited via integration test", gotMsg)

	// Publisher: exactly one event captured, on the right subject, with the right payload.
	pub.mu.Lock()
	defer pub.mu.Unlock()
	require.Len(t, pub.sent, 1)
	assert.Equal(t, "chat.room."+roomID+".event", pub.sent[0].Subject)

	var evt models.MessageEditedEvent
	require.NoError(t, json.Unmarshal(pub.sent[0].Data, &evt))
	assert.Equal(t, "message_edited", evt.Type)
	assert.Equal(t, roomID, evt.RoomID)
	assert.Equal(t, msgID, evt.MessageID)
	assert.Equal(t, "edited via integration test", evt.NewMsg)
	assert.Equal(t, "alice", evt.EditedBy)
	assert.NotZero(t, evt.Timestamp)
	assert.NotZero(t, evt.EditedAt)
}
```

- [ ] **Step 2: Run the integration test**

```bash
make test-integration SERVICE=history-service
```

Expected: `PASS` for `TestEditMessage_Integration` (alongside all `TestRepository_UpdateMessageContent_*` tests from Phase 3). Total running time: around 30-60 seconds depending on Cassandra container startup.

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/service/integration_test.go
git commit -m "test(history-service): add EditMessage service-level integration test"
```

---

## Phase 6 — Frontend Integration (OPTIONAL — can ship as a separate PR)

One task. The spec explicitly notes that the frontend change does not block the backend PR. Ship it in the same PR if it's convenient; ship it separately if it would complicate review. The task is standalone either way.

### Task 14 — Handle `message_edited` in `MessageArea.jsx`

**Files:**
- Modify: `chat-frontend/src/components/MessageArea.jsx`

**What this does:** Extends the existing `roomEvent` subscription callback to match on `evt.type === 'message_edited'`, updating the matched message's `msg` and `editedAt` fields in local state. Adds an "(edited)" indicator next to the timestamp for any message with a non-null `editedAt`.

- [ ] **Step 1: Extend the subscription callback**

Edit `chat-frontend/src/components/MessageArea.jsx`. Locate the existing subscription block (lines 43-51 today):

```jsx
    const sub = subscribe(roomEvent(room.id), (evt) => {
      if (evt.type === 'new_message' && evt.message) {
        setMessages((prev) => {
          const id = messageId(evt.message)
          if (prev.some((m) => messageId(m) === id)) return prev
          return [...prev, evt.message]
        })
      }
    })
```

Replace with:

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

- [ ] **Step 2: Render an "(edited)" indicator**

Locate the message-list render block (around line 103-109):

```jsx
        {messages.map((msg) => (
          <div key={messageId(msg)} className="message">
            <span className="message-sender">{senderName(msg)}</span>
            <span className="message-time">{formatTime(msg.createdAt)}</span>
            <div className="message-content">{messageContent(msg)}</div>
          </div>
        ))}
```

Replace with:

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

- [ ] **Step 3: Manual test in a browser**

Start the local stack and exercise the feature:

```bash
make dev  # or the project's equivalent local-dev target
```

- Open two browser windows (Alice and Bob) on the same room.
- Alice posts a message.
- Alice (via a second tab or the new edit UI, whichever ships first) sends a NATS request to `chat.user.alice.request.room.{roomID}.{siteID}.msg.edit` with `{messageId, newMsg: "fixed"}`.
- Verify Bob's window updates the message content to "fixed" and shows "(edited)".
- Refresh Bob's page and verify the edited message plus "(edited)" indicator both persist (Cassandra is authoritative).

This manual test is the only UI validation in this plan — there is no frontend test harness in this codebase today.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/components/MessageArea.jsx
git commit -m "feat(chat-frontend): handle message_edited event and render (edited) indicator"
```

---

## Implementation Order

The phases are strictly dependency-ordered — later tasks rely on earlier ones:

1. **Phase 1 — Shared infrastructure** (Tasks 1-3): introduces the interface surface every downstream phase consumes.
2. **Phase 2 — Contracts** (Tasks 4-5): types and subject pattern. No consumers yet, but the next phase's mocks and the handler reference them.
3. **Phase 3 — Cassandra repository** (Tasks 6-9): interface extension first, then three incremental branches each with its own integration test.
4. **Phase 4 — Service handler** (Tasks 10-11): happy path first (proves the flow), then error paths (locks every branch).
5. **Phase 5 — Wiring + E2E** (Tasks 12-13): exposes the handler over NATS, then integration-tests it end-to-end against real Cassandra.
6. **Phase 6 — Frontend** (Task 14, optional): can ship in this PR or a follow-up.

## Quality Gates (per CLAUDE.md)

- TDD — Red → Green → Refactor for every task. Never write implementation before its test exists.
- `make lint` green before each commit.
- `make test SERVICE=history-service` green (with `-race`) before each commit.
- `make test-integration SERVICE=history-service` green before PR merge (Phase 3 and Phase 5 add integration tests; the rest are unit).
- Coverage ≥ 80% per package; target ≥ 90% for `EditMessage` and `UpdateMessageContent`.
- Pre-commit hook runs lint + tests — fix root causes, never `--no-verify`.

## Risk Callouts (tie back to spec §13)

- **Multi-table UPDATE partial failure**: idempotent retry converges.
- **`pinned_messages_by_room` branch is dead code today**: no pin operation exists; the branch is kept for correctness when a future pin operation ships.
- **Best-effort publish**: publish failure does not roll back the Cassandra UPDATE; clients see the edit on next history fetch.
- **`historySharedSince` intentionally not enforced**: users who left and rejoined can edit their own pre-rejoin messages.
- **No message cache invalidation needed**: no message cache exists in the codebase today (verified).
- **MongoDB `rooms`/`threadRooms` not touched on edit**: inbox sort position unchanged, consistent with no-op-on-new-content behavior.

## Definition of Done

Edit PR is ready to merge when:

- [ ] All tasks in Phases 1-5 committed.
- [ ] `make lint` green.
- [ ] `make test SERVICE=history-service` green with `-race`.
- [ ] `make test-integration SERVICE=history-service` green.
- [ ] Coverage ≥ 80% per package, ≥ 90% on new handler + repo method (verify with `go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out`).
- [ ] Smoke-tested against `docker-local`: `nats req chat.user.alice.request.room.r1.site-a.msg.edit '{"messageId":"m-test","newMsg":"fixed"}'` returns a success reply; Cassandra row reflects the change; a subscriber on `chat.room.r1.event` sees the `message_edited` event.
- [ ] (If Phase 6 is in-scope) frontend manually verified.





