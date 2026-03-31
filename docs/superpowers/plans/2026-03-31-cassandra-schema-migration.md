# Cassandra Schema Migration — history-service

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Align history-service with the production `messages_by_room` Cassandra schema, replacing the simplified 5-column model with the full 22-column schema including UDT types.

**Architecture:** The shared `pkg/model.Message` (domain DTO used by other services) stays unchanged. A new `models.Message` inside history-service maps 1:1 to the `messages_by_room` Cassandra table and is used in all API responses. Four UDT types (Participant, File, Card, CardAction) are defined with gocql marshal/unmarshal support. The repository layer is updated for the new table name, column set, and composite clustering key `(created_at DESC, message_id DESC)`.

**Tech Stack:** Go 1.24, gocql (Cassandra driver), go.uber.org/mock (mockgen), testcontainers-go

---

## File Structure

| Action | File | Responsibility |
|--------|------|---------------|
| **Create** | `history-service/internal/models/types.go` | UDT structs (Participant, File, Card, CardAction) with `MarshalUDT`/`UnmarshalUDT` |
| **Modify** | `history-service/internal/models/message.go` | Add `Message` struct (full Cassandra row); update response types from `model.Message` → `Message` |
| **Modify** | `history-service/internal/cassrepo/repository.go` | New table name, column list, `scanArgs` helper, updated queries and `scanMessages` |
| **Modify** | `history-service/internal/service/service.go` | Update `MessageRepository` interface to use `models.Message` instead of `model.Message` |
| **Regen** | `history-service/internal/service/mocks/mock_repository.go` | Regenerated from updated interface |
| **Modify** | `history-service/internal/service/messages.go` | Update handler types (import swap, `CreatedAt` refs stay, `ID` → `MessageID` for GetMessageByID) |
| **Modify** | `history-service/internal/service/messages_test.go` | All test data construction uses `models.Message`, field name updates |
| **Modify** | `history-service/internal/cassrepo/integration_test.go` | New schema with UDTs, updated seed function, updated assertions |

---

### Task 1: Create UDT Types

**Files:**
- Create: `history-service/internal/models/types.go`

- [ ] **Step 1: Create UDT structs with MarshalUDT/UnmarshalUDT**

Create `history-service/internal/models/types.go`:

```go
package models

import "github.com/gocql/gocql"

// Participant maps to the Cassandra "Participant" UDT.
type Participant struct {
	ID          string `json:"id"`
	UserName    string `json:"userName"`
	EngName     string `json:"engName,omitempty"`
	CompanyName string `json:"companyName,omitempty"`
	AppID       string `json:"appId,omitempty"`
	AppName     string `json:"appName,omitempty"`
	IsBot       bool   `json:"isBot,omitempty"`
}

func (p *Participant) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	switch name {
	case "id":
		return gocql.Unmarshal(info, data, &p.ID)
	case "user_name":
		return gocql.Unmarshal(info, data, &p.UserName)
	case "eng_name":
		return gocql.Unmarshal(info, data, &p.EngName)
	case "company_name":
		return gocql.Unmarshal(info, data, &p.CompanyName)
	case "app_id":
		return gocql.Unmarshal(info, data, &p.AppID)
	case "app_name":
		return gocql.Unmarshal(info, data, &p.AppName)
	case "is_bot":
		return gocql.Unmarshal(info, data, &p.IsBot)
	}
	return nil
}

func (p Participant) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "id":
		return gocql.Marshal(info, p.ID)
	case "user_name":
		return gocql.Marshal(info, p.UserName)
	case "eng_name":
		return gocql.Marshal(info, p.EngName)
	case "company_name":
		return gocql.Marshal(info, p.CompanyName)
	case "app_id":
		return gocql.Marshal(info, p.AppID)
	case "app_name":
		return gocql.Marshal(info, p.AppName)
	case "is_bot":
		return gocql.Marshal(info, p.IsBot)
	}
	return nil, nil
}

// File maps to the Cassandra "File" UDT.
type File struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (f *File) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	switch name {
	case "id":
		return gocql.Unmarshal(info, data, &f.ID)
	case "name":
		return gocql.Unmarshal(info, data, &f.Name)
	case "type":
		return gocql.Unmarshal(info, data, &f.Type)
	}
	return nil
}

func (f File) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "id":
		return gocql.Marshal(info, f.ID)
	case "name":
		return gocql.Marshal(info, f.Name)
	case "type":
		return gocql.Marshal(info, f.Type)
	}
	return nil, nil
}

// Card maps to the Cassandra "Card" UDT.
type Card struct {
	Template string `json:"template"`
	Data     []byte `json:"data,omitempty"`
}

func (c *Card) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	switch name {
	case "template":
		return gocql.Unmarshal(info, data, &c.Template)
	case "data":
		return gocql.Unmarshal(info, data, &c.Data)
	}
	return nil
}

func (c Card) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "template":
		return gocql.Marshal(info, c.Template)
	case "data":
		return gocql.Marshal(info, c.Data)
	}
	return nil, nil
}

// CardAction maps to the Cassandra "CardAction" UDT.
type CardAction struct {
	Verb       string `json:"verb"`
	Text       string `json:"text,omitempty"`
	CardID     string `json:"cardId,omitempty"`
	DisplayText string `json:"displayText,omitempty"`
	HideExecLog bool   `json:"hideExecLog,omitempty"`
	CardTmID   string `json:"cardTmId,omitempty"`
	Data       []byte `json:"data,omitempty"`
}

func (ca *CardAction) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	switch name {
	case "verb":
		return gocql.Unmarshal(info, data, &ca.Verb)
	case "text":
		return gocql.Unmarshal(info, data, &ca.Text)
	case "card_id":
		return gocql.Unmarshal(info, data, &ca.CardID)
	case "display_text":
		return gocql.Unmarshal(info, data, &ca.DisplayText)
	case "hide_exec_log":
		return gocql.Unmarshal(info, data, &ca.HideExecLog)
	case "card_tmid":
		return gocql.Unmarshal(info, data, &ca.CardTmID)
	case "data":
		return gocql.Unmarshal(info, data, &ca.Data)
	}
	return nil
}

func (ca CardAction) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "verb":
		return gocql.Marshal(info, ca.Verb)
	case "text":
		return gocql.Marshal(info, ca.Text)
	case "card_id":
		return gocql.Marshal(info, ca.CardID)
	case "display_text":
		return gocql.Marshal(info, ca.DisplayText)
	case "hide_exec_log":
		return gocql.Marshal(info, ca.HideExecLog)
	case "card_tmid":
		return gocql.Marshal(info, ca.CardTmID)
	case "data":
		return gocql.Marshal(info, ca.Data)
	}
	return nil, nil
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./history-service/internal/models/...`
Expected: Clean build, no errors.

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/models/types.go
git commit -m "feat(history): add Cassandra UDT types with marshal/unmarshal"
```

---

### Task 2: Create Message Model and Update Response Types

**Files:**
- Modify: `history-service/internal/models/message.go`

- [ ] **Step 1: Add the Message struct to message.go**

Add the `Message` struct above the request types and update the import. This struct maps 1:1 to the `messages_by_room` Cassandra table. Remove the `import "github.com/hmchangw/chat/pkg/model"` since `model.Message` is no longer referenced.

```go
package models

import "time"

// Message represents a full message row from the messages_by_room Cassandra table.
// Used in all history-service responses.
type Message struct {
	RoomID                string                  `json:"roomId"`
	CreatedAt             time.Time               `json:"createdAt"`
	MessageID             string                  `json:"messageId"`
	Sender                Participant             `json:"sender"`
	TargetUser            *Participant            `json:"targetUser,omitempty"`
	Msg                   string                  `json:"msg"`
	Mentions              []Participant           `json:"mentions,omitempty"`
	Attachments           [][]byte                `json:"attachments,omitempty"`
	File                  *File                   `json:"file,omitempty"`
	Card                  *Card                   `json:"card,omitempty"`
	CardAction            *CardAction             `json:"cardAction,omitempty"`
	TShow                 bool                    `json:"tshow,omitempty"`
	ThreadParentCreatedAt *time.Time              `json:"threadParentCreatedAt,omitempty"`
	VisibleTo             string                  `json:"visibleTo,omitempty"`
	Unread                bool                    `json:"unread,omitempty"`
	Reactions             map[string][]Participant `json:"reactions,omitempty"`
	Deleted               bool                    `json:"deleted,omitempty"`
	SysMsgType            string                  `json:"sysMsgType,omitempty"`
	SysMsgData            []byte                  `json:"sysMsgData,omitempty"`
	FederateFrom          string                  `json:"federateFrom,omitempty"`
	EditedAt              *time.Time              `json:"editedAt,omitempty"`
	UpdatedAt             *time.Time              `json:"updatedAt,omitempty"`
}
```

- [ ] **Step 2: Update response types to use the local Message**

Replace all `model.Message` references with `Message` in the same file. The updated response types:

```go
// LoadHistoryResponse is the response for LoadHistory.
type LoadHistoryResponse struct {
	Messages         []Message `json:"messages"`
	FirstUnread      *Message  `json:"firstUnread,omitempty"`
	HasNextUnread    bool      `json:"hasNextUnread"`
	NextUnreadCursor string    `json:"nextUnreadCursor,omitempty"`
}

// LoadNextMessagesResponse is the response for LoadNextMessages.
type LoadNextMessagesResponse struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"nextCursor,omitempty"`
	HasNext    bool      `json:"hasNext"`
}

// LoadSurroundingMessagesResponse contains messages around the central message.
type LoadSurroundingMessagesResponse struct {
	Messages   []Message `json:"messages"`
	MoreBefore bool      `json:"moreBefore"`
	MoreAfter  bool      `json:"moreAfter"`
}
```

Remove the `import "github.com/hmchangw/chat/pkg/model"` line — it's no longer needed.

- [ ] **Step 3: Verify compilation of models package**

Run: `go build ./history-service/internal/models/...`
Expected: Clean build. Other packages (`service`, `cassrepo`) will NOT compile yet — that's expected.

- [ ] **Step 4: Commit**

```bash
git add history-service/internal/models/message.go
git commit -m "feat(history): add full Message model, update response types"
```

---

### Task 3: Update Repository Layer

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`

- [ ] **Step 1: Update imports, table/column constants, and scanArgs helper**

Replace the `model` import with `models` and add constants + helper:

```go
package cassrepo

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/history-service/internal/models"
)

const selectFromMessages = "SELECT room_id, created_at, message_id, sender, target_user, " +
	"msg, mentions, attachments, file, card, card_action, tshow, " +
	"thread_parent_created_at, visible_to, unread, reactions, deleted, " +
	"sys_msg_type, sys_msg_data, federate_from, edited_at, updated_at " +
	"FROM messages_by_room"

// scanArgs returns the Scan destination pointers for a Message in column order.
func scanArgs(m *models.Message) []any {
	return []any{
		&m.RoomID, &m.CreatedAt, &m.MessageID,
		&m.Sender, &m.TargetUser, &m.Msg,
		&m.Mentions, &m.Attachments, &m.File,
		&m.Card, &m.CardAction, &m.TShow,
		&m.ThreadParentCreatedAt, &m.VisibleTo, &m.Unread,
		&m.Reactions, &m.Deleted, &m.SysMsgType,
		&m.SysMsgData, &m.FederateFrom, &m.EditedAt,
		&m.UpdatedAt,
	}
}
```

- [ ] **Step 2: Update scanMessages function**

Replace the old `scanMessages` with a version that uses `scanArgs` and resets between rows to prevent pointer/slice/map leakage:

```go
func scanMessages(iter *gocql.Iter) []models.Message {
	var messages []models.Message
	var m models.Message
	for iter.Scan(scanArgs(&m)...) {
		messages = append(messages, m)
		m = models.Message{}
	}
	return messages
}
```

- [ ] **Step 3: Update all query methods**

Update every method to use `selectFromMessages`, return `Page[models.Message]`, and reference `models.Message`. The query SQL changes from `messages` to `messages_by_room` (handled by the constant), and column names update (`id` is now `message_id` in the constant).

**GetMessagesBefore:**
```go
func (r *Repository) GetMessagesBefore(ctx context.Context, roomID string, before time.Time, q PageRequest) (Page[models.Message], error) {
	var messages []models.Message

	nextCursor, err := NewQueryBuilder(
		r.session.Query(
			selectFromMessages+` WHERE room_id = ? AND created_at < ? ORDER BY created_at DESC`,
			roomID, before,
		).WithContext(ctx),
	).
		WithCursor(q.Cursor).
		WithPageSize(q.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanMessages(iter)
		})
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("querying messages before: %w", err)
	}

	return Page[models.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}
```

Apply the same pattern to all other methods:

- **GetMessagesBetweenDesc**: Same structure, `Page[models.Message]`
- **GetMessagesBetweenAsc**: Same structure, keep the dynamic `upperOp` logic, use `selectFromMessages`
- **GetMessagesAfter**: Same structure, `Page[models.Message]`
- **GetAllMessagesAsc**: Same structure, `Page[models.Message]`

For `GetMessagesBetweenAsc`, the dynamic query construction changes to:
```go
cql := selectFromMessages + fmt.Sprintf(` WHERE room_id = ? AND created_at > ? AND created_at %s ? ORDER BY created_at ASC`, upperOp)
```

- [ ] **Step 4: Update GetMessageByID**

```go
func (r *Repository) GetMessageByID(ctx context.Context, roomID, messageID string) (*models.Message, error) {
	var m models.Message
	err := r.session.Query(
		selectFromMessages+` WHERE room_id = ? AND message_id = ? ALLOW FILTERING`,
		roomID, messageID,
	).WithContext(ctx).Scan(scanArgs(&m)...)
	if err == gocql.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying message by id: %w", err)
	}
	return &m, nil
}
```

- [ ] **Step 5: Verify repository compiles**

Run: `go build ./history-service/internal/cassrepo/...`
Expected: Clean build.

- [ ] **Step 6: Commit**

```bash
git add history-service/internal/cassrepo/repository.go
git commit -m "feat(history): update repository for messages_by_room schema"
```

---

### Task 4: Update Service Interface and Regenerate Mocks

**Files:**
- Modify: `history-service/internal/service/service.go`
- Regen: `history-service/internal/service/mocks/mock_repository.go`

- [ ] **Step 1: Update MessageRepository interface**

In `service.go`, replace the `model` import with `models` and update all type references:

```go
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

//go:generate mockgen -destination=mocks/mock_repository.go -package=mocks . MessageRepository,SubscriptionRepository

// MessageRepository defines Cassandra-backed message operations.
type MessageRepository interface {
	GetMessagesBefore(ctx context.Context, roomID string, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesBetweenAsc(ctx context.Context, roomID string, after, before time.Time, inclusive bool, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesAfter(ctx context.Context, roomID string, after time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetAllMessagesAsc(ctx context.Context, roomID string, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessageByID(ctx context.Context, roomID, messageID string) (*models.Message, error)
}
```

The `SubscriptionRepository` interface and the rest of the file stay the same.

- [ ] **Step 2: Regenerate mocks**

Run: `export PATH=$PATH:$(go env GOPATH)/bin && make generate SERVICE=history-service`
Expected: `go generate ./history-service/...` completes with no errors.

- [ ] **Step 3: Verify compilation**

Run: `go build ./history-service/...`
Expected: Service and handler packages may not compile yet (they still reference `model.Message`). That's expected — next task fixes them.

- [ ] **Step 4: Commit**

```bash
git add history-service/internal/service/service.go history-service/internal/service/mocks/mock_repository.go
git commit -m "feat(history): update MessageRepository interface for new schema"
```

---

### Task 5: Update Service Handlers

**Files:**
- Modify: `history-service/internal/service/messages.go`

- [ ] **Step 1: Update imports and type references**

Remove the `model` import, the `models` import already exists. Update handler return types and field references.

Key changes across all handlers:
- `model.Message` → `models.Message` (in variable types and return types)
- `cassrepo.Page[model.Message]` → `cassrepo.Page[models.Message]`
- `msg.ID` → `msg.MessageID` (in `GetMessageByID` handler, and anywhere a message ID is accessed)

**LoadHistory** — `var page cassrepo.Page[models.Message]` (line ~49). The unread section: `cassrepo.PageRequest{Cursor: &cassrepo.Cursor{}, PageSize: 1}` stays the same.

**LoadNextMessages** — `var page cassrepo.Page[models.Message]` (line ~117).

**LoadSurroundingMessages** — `var beforePage cassrepo.Page[models.Message]` (line ~167). The assembly section:
```go
messages := make([]models.Message, 0, len(beforePage.Data)+1+len(afterPage.Data))
```

**GetMessageByID** — Return type is already `*model.Message` in the handler signature. Change to `*models.Message`:
```go
func (s *HistoryService) GetMessageByID(ctx context.Context, p natsrouter.Params, req models.GetMessageByIDRequest) (*models.Message, error) {
```

- [ ] **Step 2: Verify full compilation**

Run: `go build ./history-service/...`
Expected: Clean build across all packages. Tests will not pass yet (test data still uses `model.Message`).

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/service/messages.go
git commit -m "feat(history): update handlers for new Message model"
```

---

### Task 6: Update Unit Tests

**Files:**
- Modify: `history-service/internal/service/messages_test.go`

- [ ] **Step 1: Update imports and test data construction**

Replace `model` import with `models` import. The `models` import already exists for the request types, so just remove the `model` line.

Update the `makePage` helper:
```go
func makePage(msgs []models.Message, hasNext bool) cassrepo.Page[models.Message] {
	nextCursor := ""
	if hasNext {
		nextCursor = "fake-next-cursor"
	}
	return cassrepo.Page[models.Message]{Data: msgs, NextCursor: nextCursor, HasNext: hasNext}
}
```

- [ ] **Step 2: Update all test message construction**

Replace every `model.Message{ID: "m1", RoomID: "r1", CreatedAt: ...}` with `models.Message{MessageID: "m1", RoomID: "r1", CreatedAt: ...}`.

The field mappings:
- `ID` → `MessageID`
- `RoomID` → `RoomID` (unchanged)
- `CreatedAt` → `CreatedAt` (unchanged)

Update every assertion that checks `.ID` to check `.MessageID`:
- `assert.Equal(t, "m1", result.ID)` → `assert.Equal(t, "m1", result.MessageID)`
- `assert.Equal(t, "m4", resp.Messages[0].ID)` → `assert.Equal(t, "m4", resp.Messages[0].MessageID)`
- etc.

Also update mock return values for `GetMessageByID`:
- `&model.Message{ID: "m1", ...}` → `&models.Message{MessageID: "m1", ...}`

- [ ] **Step 3: Run tests**

Run: `export PATH=$PATH:$(go env GOPATH)/bin && make test SERVICE=history-service`
Expected: All unit tests PASS.

- [ ] **Step 4: Commit**

```bash
git add history-service/internal/service/messages_test.go
git commit -m "feat(history): update unit tests for new Message model"
```

---

### Task 7: Update Integration Tests

**Files:**
- Modify: `history-service/internal/cassrepo/integration_test.go`

- [ ] **Step 1: Update schema creation in setupCassandra**

Replace the simple `messages` table creation with UDT creation + `messages_by_room` table:

```go
func setupCassandra(t *testing.T) *gocql.Session {
	t.Helper()
	ctx := context.Background()
	container, err := cassandra.Run(ctx, "cassandra:5")
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.ConnectionHost(ctx)
	require.NoError(t, err)

	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.One
	session, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })

	require.NoError(t, session.Query(`CREATE KEYSPACE IF NOT EXISTS chat_test WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`).Exec())

	// Create UDTs
	for _, cql := range []string{
		`CREATE TYPE IF NOT EXISTS chat_test."Participant" (id TEXT, user_name TEXT, eng_name TEXT, company_name TEXT, app_id TEXT, app_name TEXT, is_bot BOOLEAN)`,
		`CREATE TYPE IF NOT EXISTS chat_test."File" (id TEXT, name TEXT, type TEXT)`,
		`CREATE TYPE IF NOT EXISTS chat_test."Card" (template TEXT, data BLOB)`,
		`CREATE TYPE IF NOT EXISTS chat_test."CardAction" (verb TEXT, text TEXT, card_id TEXT, display_text TEXT, hide_exec_log BOOLEAN, card_tmid TEXT, data BLOB)`,
	} {
		require.NoError(t, session.Query(cql).Exec())
	}

	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages_by_room (
		room_id TEXT,
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
		thread_parent_created_at TIMESTAMP,
		visible_to TEXT,
		unread BOOLEAN,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
		deleted BOOLEAN,
		sys_msg_type TEXT,
		sys_msg_data BLOB,
		federate_from TEXT,
		edited_at TIMESTAMP,
		updated_at TIMESTAMP,
		PRIMARY KEY ((room_id), created_at, message_id)
	) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`).Exec())

	cluster.Keyspace = "chat_test"
	ksSession, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { ksSession.Close() })
	return ksSession
}
```

- [ ] **Step 2: Update seed function**

Replace `seedMessages` to insert into `messages_by_room` with a `Participant` sender:

```go
func seedMessages(t *testing.T, session *gocql.Session, roomID string, base time.Time, count int) {
	t.Helper()
	sender := models.Participant{ID: "u1", UserName: "user1"}
	for i := 0; i < count; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		err := session.Query(
			`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg) VALUES (?, ?, ?, ?, ?)`,
			roomID, ts, fmt.Sprintf("m%d", i), sender, fmt.Sprintf("msg-%d", i),
		).Exec()
		require.NoError(t, err)
	}
}
```

Note: This requires `models.Participant` to implement `MarshalUDT` (done in Task 1).

- [ ] **Step 3: Update test assertions**

Update every test that accesses message fields:
- `page.Data[0].ID` → `page.Data[0].MessageID`
- `msg.ID` → `msg.MessageID`
- `msg.RoomID` stays `msg.RoomID`

Also add the `models` import and remove the old `model` import if present (it likely isn't — integration tests use the repo directly).

The integration test file imports should become:
```go
import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/cassandra"

	"github.com/hmchangw/chat/history-service/internal/models"
)
```

- [ ] **Step 4: Run integration tests (if Docker available)**

Run: `export PATH=$PATH:$(go env GOPATH)/bin && make test-integration SERVICE=history-service`
Expected: All integration tests PASS.

If Docker is not available, run unit tests only: `make test SERVICE=history-service`

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/integration_test.go
git commit -m "feat(history): update integration tests for messages_by_room schema"
```

---

### Task 8: Full Verification and Final Commit

- [ ] **Step 1: Run linter**

Run: `make lint`
Expected: `0 issues.`

- [ ] **Step 2: Run all unit tests**

Run: `make test`
Expected: All packages PASS.

- [ ] **Step 3: Run history-service tests with coverage**

Run: `go test -race -coverprofile=coverage.out ./history-service/internal/service/ && go tool cover -func=coverage.out | tail -1`
Expected: Coverage >= 80%.

- [ ] **Step 4: Squash into single commit and push**

```bash
git push -u origin claude/review-history-service-4IpAp
```

---

## Post-Migration Improvements

These items are out of scope for this plan but should be addressed after the schema migration:

1. **message-worker write path** — Currently inserts 5 columns into `messages`. Must be updated to insert into `messages_by_room` with a `Participant` sender UDT and all required columns. This is the most critical follow-up.

2. **docker-local schema init** — The `docker-local/docker-compose.yml` Cassandra init script still creates the old `messages` table. Update to create UDTs + `messages_by_room`.

3. **deploy/docker-compose.yml** — Same schema init update needed for the deploy Compose file.

4. **`pkg/model.Message` divergence** — The shared domain DTO (`pkg/model.Message`) is still the 5-field version used by message-worker, broadcast-worker, notification-worker. As these services adopt the new schema, consider whether `pkg/model.Message` should grow to match, or if each service should have its own internal model (like history-service now does).

5. **`reactions` field testing** — The complex `MAP<TEXT, FROZEN<SET<FROZEN<Participant>>>>` type needs integration test coverage to verify gocql can roundtrip it correctly. Add a seed with reactions data and assert deserialization.

6. **`attachments`/`sys_msg_data` serialization** — These `BLOB`/`LIST<BLOB>` fields serialize as base64 in JSON. Verify the frontend expects this encoding, or add custom JSON marshalers if it expects hex or raw bytes.

7. **`GetMessageByID` efficiency** — With `message_id` now a clustering key, consider storing `created_at` alongside message references (e.g., in MongoDB subscription metadata) to enable point lookups `WHERE room_id = ? AND created_at = ? AND message_id = ?` without `ALLOW FILTERING`.

8. **Deleted message filtering** — The schema has a `deleted` boolean. Queries should probably filter `WHERE deleted = false` or handle deleted messages at the service layer. Determine the product requirement.

9. **`visible_to` access control** — Some messages have a `visible_to` field restricting visibility. The service layer should enforce this alongside the existing `historySharedSince` check.

10. **Thread support (`tshow`, `thread_parent_created_at`)** — Messages with `tshow = true` are thread replies shown in the main channel. Consider whether LoadHistory should include/exclude these or annotate them for the frontend.
