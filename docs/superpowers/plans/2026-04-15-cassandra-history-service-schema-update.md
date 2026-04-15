# Cassandra History Service Schema Update

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Align the history-service with the updated `messages_by_id` Cassandra schema that adds `thread_room_id`, `pinned_at`, and `pinned_by` columns.

**Architecture:** The `messages_by_id` table is now a superset of `messages_by_room` — it has 3 extra columns. We add these fields to the shared `Message` struct, then split the repository's column list and scan destinations so `messages_by_room` queries use the base set and `messages_by_id` queries use the extended set. No service-layer or interface changes needed.

**Tech Stack:** Go, gocql, testify, testcontainers-go (Cassandra)

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `pkg/model/cassandra/message.go` | Modify (lines 99-125) | Add `ThreadRoomID`, `PinnedAt`, `PinnedBy` fields to `Message` struct |
| `pkg/model/cassandra/message_test.go` | Modify | Add new fields to JSON round-trip tests |
| `history-service/internal/cassrepo/repository.go` | Modify (lines 14-35) | Split column list + scan dest into base vs. messages_by_id extended |
| `history-service/internal/cassrepo/integration_test.go` | Modify | Update `messages_by_id` CREATE TABLE + full-row test to cover new columns |

No changes needed to: `service.go`, `messages.go`, `utils.go`, mocks, `models/message.go` (type alias).

---

### Task 1: Add new fields to the Message struct

**Files:**
- Modify: `pkg/model/cassandra/message.go:99-125`
- Test: `pkg/model/cassandra/message_test.go`

- [ ] **Step 1: Write failing tests for new fields**

Add the new fields to the full round-trip test in `pkg/model/cassandra/message_test.go`. In `TestMessage_JSON`, add to the `msg` literal (after `UpdatedAt`):

```go
ThreadRoomID: "tr-1",
PinnedAt:     &edited, // reuse an existing *time.Time
PinnedBy:     &Participant{ID: "u9", Account: "pinner"},
```

And add assertions (after the `UpdatedAt` assertions):

```go
assert.Equal(t, "tr-1", got.ThreadRoomID)
require.NotNil(t, got.PinnedAt)
assert.Equal(t, edited, *got.PinnedAt)
require.NotNil(t, got.PinnedBy)
assert.Equal(t, "u9", got.PinnedBy.ID)
assert.Equal(t, "pinner", got.PinnedBy.Account)
```

In `TestMessage_JSON_Minimal`, add assertions:

```go
assert.Empty(t, got.ThreadRoomID)
assert.Nil(t, got.PinnedAt)
assert.Nil(t, got.PinnedBy)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model/cassandra`
Expected: compilation errors — `ThreadRoomID`, `PinnedAt`, `PinnedBy` don't exist on `Message` yet.

- [ ] **Step 3: Add the three fields to Message struct**

In `pkg/model/cassandra/message.go`, add after the `UpdatedAt` field (line 124):

```go
ThreadRoomID string       `json:"threadRoomId,omitempty"`
PinnedAt     *time.Time   `json:"pinnedAt,omitempty"`
PinnedBy     *Participant `json:"pinnedBy,omitempty"`
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/model/cassandra`
Expected: all PASS.

- [ ] **Step 5: Run lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add pkg/model/cassandra/message.go pkg/model/cassandra/message_test.go
git commit -m "feat(model): add ThreadRoomID, PinnedAt, PinnedBy to cassandra.Message"
```

---

### Task 2: Split repository column lists for messages_by_room vs messages_by_id

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go:14-35`

- [ ] **Step 1: Refactor constants and scan helpers**

Replace the current `messageColumns`, `messageByRoomQuery`, `messageByIDQuery`, and `messageScanDest` (lines 14-35) with the following:

```go
// baseColumns are shared across messages_by_room and messages_by_id.
const baseColumns = "room_id, created_at, message_id, sender, target_user, " +
	"msg, mentions, attachments, file, card, card_action, tshow, " +
	"thread_parent_id, thread_parent_created_at, quoted_parent_message, " +
	"visible_to, unread, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"

// messageByIDExtraColumns are the columns only present in messages_by_id.
const messageByIDExtraColumns = ", thread_room_id, pinned_at, pinned_by"

const messageByRoomQuery = "SELECT " + baseColumns + " FROM messages_by_room"
const messageByIDQuery = "SELECT " + baseColumns + messageByIDExtraColumns + " FROM messages_by_id"

// baseScanDest returns Scan destination pointers for the baseColumns in order.
func baseScanDest(m *models.Message) []any {
	return []any{
		&m.RoomID, &m.CreatedAt, &m.MessageID,
		&m.Sender, &m.TargetUser, &m.Msg,
		&m.Mentions, &m.Attachments, &m.File,
		&m.Card, &m.CardAction, &m.TShow,
		&m.ThreadParentID, &m.ThreadParentCreatedAt, &m.QuotedParentMessage,
		&m.VisibleTo, &m.Unread, &m.Reactions,
		&m.Deleted, &m.Type, &m.SysMsgData,
		&m.SiteID, &m.EditedAt, &m.UpdatedAt,
	}
}

// messageByIDScanDest returns Scan destination pointers for all messages_by_id columns.
func messageByIDScanDest(m *models.Message) []any {
	return append(baseScanDest(m), &m.ThreadRoomID, &m.PinnedAt, &m.PinnedBy)
}
```

- [ ] **Step 2: Update scanMessages to use baseScanDest**

The `scanMessages` function (used by messages_by_room queries) should call `baseScanDest`:

```go
func scanMessages(iter *gocql.Iter) []models.Message {
	messages := make([]models.Message, 0)
	for {
		var m models.Message
		if !iter.Scan(baseScanDest(&m)...) {
			break
		}
		messages = append(messages, m)
	}
	return messages
}
```

- [ ] **Step 3: Update GetMessageByID to use messageByIDScanDest**

Change `GetMessageByID` (line 172) from `messageScanDest` to `messageByIDScanDest`:

```go
func (r *Repository) GetMessageByID(ctx context.Context, messageID string) (*models.Message, error) {
	var m models.Message
	err := r.session.Query(
		messageByIDQuery+` WHERE message_id = ?`,
		messageID,
	).WithContext(ctx).Scan(messageByIDScanDest(&m)...)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying message by id: %w", err)
	}
	return &m, nil
}
```

- [ ] **Step 4: Verify compilation**

Run: `make build SERVICE=history-service`
Expected: compiles successfully.

- [ ] **Step 5: Run existing unit tests**

Run: `make test SERVICE=history-service/...`
Expected: all PASS (unit tests use mocks, not real Cassandra).

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add history-service/internal/cassrepo/repository.go
git commit -m "refactor(history-service): split column lists for messages_by_room vs messages_by_id"
```

---

### Task 3: Update integration test schema and full-row assertions

**Files:**
- Modify: `history-service/internal/cassrepo/integration_test.go`

- [ ] **Step 1: Add new columns to messages_by_id CREATE TABLE**

In `setupCassandra`, update the `messages_by_id` CREATE TABLE (lines 75-101) to add the 3 new columns right before the PRIMARY KEY line:

```cql
thread_room_id TEXT,
pinned_at TIMESTAMP,
pinned_by FROZEN<"Participant">,
PRIMARY KEY (message_id)
```

The full replacement for the `messages_by_id` CREATE TABLE block:

```go
require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages_by_id (
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
	thread_room_id TEXT,
	pinned_at TIMESTAMP,
	pinned_by FROZEN<"Participant">,
	PRIMARY KEY (message_id)
)`).Exec())
```

- [ ] **Step 2: Update TestRepository_FullRow_AllColumns INSERT to include new columns**

Update the `insertCQL` and `insertArgs` in `TestRepository_FullRow_AllColumns` to include the 3 new columns.

Add a `pinnedBy` participant and `pinnedAt` timestamp before the insert:

```go
pinnedAt := ts.Add(2 * time.Hour)
pinnedBy := models.Participant{ID: "u9", Account: "pinner"}
```

Update `insertCQL`:

```go
insertCQL := `INSERT INTO messages_by_id (room_id, created_at, message_id, sender, target_user, msg, mentions, attachments, file, card, card_action, tshow, thread_parent_id, thread_parent_created_at, quoted_parent_message, visible_to, unread, reactions, deleted, type, sys_msg_data, site_id, edited_at, updated_at, thread_room_id, pinned_at, pinned_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
```

Append to `insertArgs` (after `updatedAt`):

```go
"N/A", pinnedAt, pinnedBy,
```

- [ ] **Step 3: Add assertions for new columns**

After the existing `Reactions` assertions at the end of `TestRepository_FullRow_AllColumns`, add:

```go
// messages_by_id extra columns
assert.Equal(t, "N/A", msg.ThreadRoomID)
require.NotNil(t, msg.PinnedAt)
assert.Equal(t, pinnedAt.UTC(), msg.PinnedAt.UTC())
require.NotNil(t, msg.PinnedBy)
assert.Equal(t, "u9", msg.PinnedBy.ID)
assert.Equal(t, "pinner", msg.PinnedBy.Account)
```

- [ ] **Step 4: Run lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/integration_test.go
git commit -m "test(history-service): update integration test schema for messages_by_id extra columns"
```

---

### Task 4: Final verification

- [ ] **Step 1: Run all unit tests**

Run: `make test`
Expected: all PASS.

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 3: Push**

```bash
git push -u origin claude/cassandra-history-service-S5YUO
```
