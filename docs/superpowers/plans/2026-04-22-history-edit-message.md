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

- [ ] **Step 3: Build to verify compilation**

```bash
make build SERVICE=history-service
```

Expected: binary built without errors. No runtime tests yet — the wiring is correct if and only if the program compiles.

- [ ] **Step 4: Commit**

```bash
git add history-service/internal/service/service.go history-service/cmd/main.go
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
