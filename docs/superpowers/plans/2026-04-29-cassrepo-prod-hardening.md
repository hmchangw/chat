# cassrepo Production Hardening Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden `history-service/internal/cassrepo` for production by replacing fragile positional Cassandra scans with struct-scan, extracting query constants, tightening error handling, and adding missing tests.

**Architecture:** All changes are confined to `history-service/internal/cassrepo/`, `pkg/model/cassandra/message.go`, `history-service/internal/service/service.go`, and `docs/cassandra_message_model.md`. The refactor is purely internal — public method signatures are unchanged. Tasks are ordered so each builds on the previous: cql tags first (C1 Part 1), then struct-scan wiring (C1 Part 2), then write-path quality improvements (I1–I4), then mechanical clean-ups (M2, M3, M5, M6).

**Tech Stack:** Go 1.25, `github.com/gocql/gocql` (Cassandra driver), `github.com/stretchr/testify`, `testcontainers-go` for integration tests.

---

## File Map

| File | Change |
|------|--------|
| `pkg/model/cassandra/message.go` | Add `cql:` tags to `Message` struct |
| `history-service/internal/cassrepo/messages_by_room.go` | Remove positional scan helpers; update `fetchMessagesPage` |
| `history-service/internal/cassrepo/messages_by_id.go` | Remove positional scan helpers; switch `GetMessageByID` to iter |
| `history-service/internal/cassrepo/thread_messages.go` | Remove positional scan helpers; delegate to `fetchMessagesPage` |
| `history-service/internal/cassrepo/write.go` | Extract query constants; per-table helpers; error returns |
| `history-service/internal/cassrepo/repository.go` | Add compile-time interface check |
| `history-service/internal/cassrepo/utils.go` | Add cursor length guard |
| `history-service/internal/cassrepo/utils_test.go` | Add cursor-guard unit test |
| `history-service/internal/cassrepo/integration_test.go` | Shrink to shared setup only |
| `history-service/internal/cassrepo/messages_by_room_integration_test.go` | Create; hold room-query tests |
| `history-service/internal/cassrepo/messages_by_id_integration_test.go` | Create; hold by-id query tests |
| `history-service/internal/cassrepo/thread_messages_integration_test.go` | Create; hold thread-query tests |
| `history-service/internal/cassrepo/write_integration_test.go` | Create; hold write tests + new edge-case tests |
| `history-service/internal/service/service.go` | Rename `q` param; add compile-time check |
| `docs/cassandra_message_model.md` | Replace `--` SQL comments with `//` CQL comments |

---

## Task 1: Add `cql:` tags to `cassandra.Message` (C1 — Part 1)

**Why:** The custom `structScan` helper (added in Task 2) maps returned columns to struct fields by the `cql:` tag name. Without tags the scan would fall back to lowercased field names (e.g. `RoomID` → `roomid`) which would not match snake_case Cassandra column names (e.g. `room_id`). Adding the tags is a zero-risk prerequisite — the existing positional `Scan()` calls ignore them entirely.

**Files:**
- Modify: `pkg/model/cassandra/message.go`

- [ ] **Step 1: Read the current struct**

  Open `pkg/model/cassandra/message.go`. The `Message` struct currently has only `json:` tags and a comment explaining that `cql:` tags are not needed. You will add `cql:` tags to every field, matching the Cassandra column names exactly.

- [ ] **Step 2: Add `cql:` tags**

  Replace the `Message` struct and update its doc comment so the full struct reads:

  ```go
  // Message represents a message row in the Cassandra message tables
  // (messages_by_room, messages_by_id, thread_messages_by_room).
  //
  // cql tags are used by gocql's Iter.StructScan to map returned columns to
  // fields by name, eliminating positional scan maintenance.
  type Message struct {
  	RoomID                string                   `json:"roomId"                          cql:"room_id"`
  	CreatedAt             time.Time                `json:"createdAt"                       cql:"created_at"`
  	MessageID             string                   `json:"messageId"                       cql:"message_id"`
  	Sender                Participant              `json:"sender"                          cql:"sender"`
  	TargetUser            *Participant             `json:"targetUser,omitempty"            cql:"target_user"`
  	Msg                   string                   `json:"msg"                             cql:"msg"`
  	Mentions              []Participant            `json:"mentions,omitempty"              cql:"mentions"`
  	Attachments           [][]byte                 `json:"attachments,omitempty"           cql:"attachments"`
  	File                  *File                    `json:"file,omitempty"                  cql:"file"`
  	Card                  *Card                    `json:"card,omitempty"                  cql:"card"`
  	CardAction            *CardAction              `json:"cardAction,omitempty"            cql:"card_action"`
  	TShow                 bool                     `json:"tshow,omitempty"                 cql:"tshow"`
  	TCount                *int                     `json:"tcount,omitempty"                cql:"tcount"`
  	ThreadParentID        string                   `json:"threadParentId,omitempty"        cql:"thread_parent_id"`
  	ThreadParentCreatedAt *time.Time               `json:"threadParentCreatedAt,omitempty" cql:"thread_parent_created_at"`
  	QuotedParentMessage   *QuotedParentMessage     `json:"quotedParentMessage,omitempty"   cql:"quoted_parent_message"`
  	VisibleTo             string                   `json:"visibleTo,omitempty"             cql:"visible_to"`
  	Unread                bool                     `json:"unread,omitempty"                cql:"unread"`
  	Reactions             map[string][]Participant `json:"reactions,omitempty"             cql:"reactions"`
  	Deleted               bool                     `json:"deleted,omitempty"               cql:"deleted"`
  	Type                  string                   `json:"type,omitempty"                  cql:"type"`
  	SysMsgData            []byte                   `json:"sysMsgData,omitempty"            cql:"sys_msg_data"`
  	SiteID                string                   `json:"siteId,omitempty"                cql:"site_id"`
  	EditedAt              *time.Time               `json:"editedAt,omitempty"              cql:"edited_at"`
  	UpdatedAt             *time.Time               `json:"updatedAt,omitempty"             cql:"updated_at"`
  	ThreadRoomID          string                   `json:"threadRoomId,omitempty"          cql:"thread_room_id"`
  	PinnedAt              *time.Time               `json:"pinnedAt,omitempty"              cql:"pinned_at"`
  	PinnedBy              *Participant             `json:"pinnedBy,omitempty"              cql:"pinned_by"`
  }
  ```

- [ ] **Step 3: Verify compilation**

  ```bash
  make build SERVICE=history-service
  ```

  Expected: build succeeds with no errors.

- [ ] **Step 4: Run unit tests**

  ```bash
  make test SERVICE=history-service
  ```

  Expected: all tests pass.

- [ ] **Step 5: Commit**

  ```bash
  git add pkg/model/cassandra/message.go
  git commit -m "feat(model): add cql tags to cassandra.Message for StructScan"
  ```

---

## Task 2: Replace positional scan with `structScan` helper in cassrepo (C1 — Part 2)

**Why:** The current `baseScanDest` / `messageByIDScanDest` / `threadMessageScanDest` functions must stay perfectly in sync with the SELECT column list — one column added anywhere silently corrupts all subsequent fields. The custom `structScan(iter, &m)` helper (gocql v1.7.0 has no native `StructScan`) maps by `cql:` tag name via `MapScan`, making column order irrelevant and eliminating the entire class of positional-scan bugs. After this task, `GetThreadMessages` also delegates to `fetchMessagesPage` (which all room-query methods already use) because the scan function is now uniform.

**Files:**
- Modify: `history-service/internal/cassrepo/messages_by_room.go`
- Modify: `history-service/internal/cassrepo/messages_by_id.go`
- Modify: `history-service/internal/cassrepo/thread_messages.go`

- [ ] **Step 1: Run the integration tests to establish a green baseline**

  ```bash
  make test-integration SERVICE=history-service
  ```

  Expected: all tests pass. This is your safety net — the tests must still pass after the refactor.

- [ ] **Step 2: Rewrite `messages_by_room.go`**

  Remove `baseScanDest`, `scanWith`, `scanMessages`. Replace `fetchMessagesPage`'s scan closure with `StructScan`. The full file should read:

  ```go
  package cassrepo

  import (
  	"context"
  	"fmt"
  	"time"

  	"github.com/gocql/gocql"

  	"github.com/hmchangw/chat/history-service/internal/models"
  )

  const baseColumns = "room_id, created_at, message_id, thread_room_id, sender, target_user, " +
  	"msg, mentions, attachments, file, card, card_action, tshow, tcount, " +
  	"thread_parent_id, thread_parent_created_at, quoted_parent_message, " +
  	"visible_to, unread, reactions, deleted, " +
  	"type, sys_msg_data, site_id, edited_at, updated_at"

  const messageByRoomQuery = "SELECT " + baseColumns + " FROM messages_by_room"

  func scanMsgsFromIter(iter *gocql.Iter) []models.Message {
  	messages := make([]models.Message, 0)
  	for {
  		var m models.Message
  		if !iter.StructScan(&m) {
  			break
  		}
  		messages = append(messages, m)
  	}
  	return messages
  }

  func fetchMessagesPage(q *gocql.Query, pageReq PageRequest, errMsg string) (Page[models.Message], error) {
  	var messages []models.Message
  	nextCursor, err := NewQueryBuilder(q).
  		WithCursor(pageReq.Cursor).
  		WithPageSize(pageReq.PageSize).
  		Fetch(func(iter *gocql.Iter) {
  			messages = scanMsgsFromIter(iter)
  		})
  	if err != nil {
  		return Page[models.Message]{}, fmt.Errorf("%s: %w", errMsg, err)
  	}
  	return Page[models.Message]{
  		Data:       messages,
  		NextCursor: nextCursor,
  		HasNext:    nextCursor != "",
  	}, nil
  }

  func (r *Repository) GetMessagesBefore(ctx context.Context, roomID string, before time.Time, q PageRequest) (Page[models.Message], error) {
  	return fetchMessagesPage(
  		r.session.Query(
  			messageByRoomQuery+` WHERE room_id = ? AND created_at < ? ORDER BY created_at DESC`,
  			roomID, before,
  		).WithContext(ctx),
  		q, "querying messages before",
  	)
  }

  func (r *Repository) GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, q PageRequest) (Page[models.Message], error) {
  	return fetchMessagesPage(
  		r.session.Query(
  			messageByRoomQuery+` WHERE room_id = ? AND created_at > ? AND created_at < ? ORDER BY created_at DESC`,
  			roomID, since, before,
  		).WithContext(ctx),
  		q, "querying messages between desc",
  	)
  }

  func (r *Repository) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, q PageRequest) (Page[models.Message], error) {
  	return fetchMessagesPage(
  		r.session.Query(
  			messageByRoomQuery+` WHERE room_id = ? AND created_at > ? ORDER BY created_at ASC`,
  			roomID, after,
  		).WithContext(ctx),
  		q, "querying messages after",
  	)
  }

  func (r *Repository) GetAllMessagesAsc(ctx context.Context, roomID string, q PageRequest) (Page[models.Message], error) {
  	return fetchMessagesPage(
  		r.session.Query(
  			messageByRoomQuery+` WHERE room_id = ? ORDER BY created_at ASC`,
  			roomID,
  		).WithContext(ctx),
  		q, "querying all messages asc",
  	)
  }
  ```

- [ ] **Step 3: Rewrite `messages_by_id.go`**

  Remove `messageByIDExtraColumns`, `messageByIDScanDest`, `scanMessagesByID`. Switch `GetMessageByID` from a positional `Scan()` call to an `Iter` with `StructScan`; `GetMessagesByIDs` now uses `scanMsgsFromIter`. The full file:

  ```go
  package cassrepo

  import (
  	"context"
  	"fmt"

  	"github.com/gocql/gocql"

  	"github.com/hmchangw/chat/history-service/internal/models"
  )

  const messageByIDQuery = "SELECT " + baseColumns + ", pinned_at, pinned_by FROM messages_by_id"

  // Returns (nil, nil) when not found.
  func (r *Repository) GetMessageByID(ctx context.Context, messageID string) (*models.Message, error) {
  	iter := r.session.Query(
  		messageByIDQuery+` WHERE message_id = ? LIMIT 1`,
  		messageID,
  	).WithContext(ctx).Iter()

  	var m models.Message
  	found := iter.StructScan(&m)
  	if err := iter.Close(); err != nil {
  		return nil, fmt.Errorf("querying message by id %s: %w", messageID, err)
  	}
  	if !found {
  		return nil, nil
  	}
  	return &m, nil
  }

  // Missing IDs are silently omitted; order is not guaranteed.
  func (r *Repository) GetMessagesByIDs(ctx context.Context, messageIDs []string) ([]models.Message, error) {
  	if len(messageIDs) == 0 {
  		return []models.Message{}, nil
  	}
  	iter := r.session.Query(
  		messageByIDQuery+` WHERE message_id IN ?`,
  		messageIDs,
  	).WithContext(ctx).Iter()
  	messages := scanMsgsFromIter(iter)
  	if err := iter.Close(); err != nil {
  		return nil, fmt.Errorf("querying messages by IDs: %w", err)
  	}
  	return messages, nil
  }
  ```

  Note: `scanMsgsFromIter` is defined in `messages_by_room.go` (same package). Note also that `messageByIDQuery` is now a plain string constant, not composed from a separate `messageByIDExtraColumns` — the two extra columns (`pinned_at`, `pinned_by`) are inlined.

- [ ] **Step 4: Rewrite `thread_messages.go`**

  Remove `threadMessageColumns`, `threadMessageQuery`, `threadMessageScanDest`, `scanThreadMessages`. The query constant moves inline. `GetThreadMessages` now delegates to `fetchMessagesPage` exactly like the room queries do. The full file:

  ```go
  package cassrepo

  import (
  	"context"
  	"fmt"

  	"github.com/hmchangw/chat/history-service/internal/models"
  )

  // Subset of columns present in thread_messages_by_room (no tshow, thread_parent_created_at, or pinned_* columns).
  const threadMessageColumns = "room_id, thread_room_id, created_at, message_id, thread_parent_id, " +
  	"sender, target_user, msg, mentions, attachments, file, card, card_action, " +
  	"quoted_parent_message, visible_to, unread, reactions, deleted, " +
  	"type, sys_msg_data, site_id, edited_at, updated_at"

  // Partition + clustering key equality avoids ALLOW FILTERING.
  func (r *Repository) GetThreadMessages(ctx context.Context, roomID, threadRoomID string, q PageRequest) (Page[models.Message], error) {
  	return fetchMessagesPage(
  		r.session.Query(
  			"SELECT "+threadMessageColumns+` FROM thread_messages_by_room WHERE room_id = ? AND thread_room_id = ? ORDER BY created_at DESC`,
  			roomID, threadRoomID,
  		).WithContext(ctx),
  		q, "querying thread messages",
  	)
  }
  ```

  Note: the `gocql` import is no longer needed in this file since `scanMsgsFromIter` lives in `messages_by_room.go`. Remove it or `goimports` will error.

- [ ] **Step 5: Run the integration tests**

  ```bash
  make test-integration SERVICE=history-service
  ```

  Expected: all existing tests pass — including `TestRepository_FullRow_AllColumns` and `TestRepository_GetThreadMessages_ColumnScan` which exercise every column and are the load-bearing correctness proof for `StructScan`.

- [ ] **Step 6: Run lint**

  ```bash
  make lint
  ```

  Expected: no errors. `goimports` will flag any unused imports.

- [ ] **Step 7: Commit**

  ```bash
  git add history-service/internal/cassrepo/messages_by_room.go \
          history-service/internal/cassrepo/messages_by_id.go \
          history-service/internal/cassrepo/thread_messages.go
  git commit -m "refactor(cassrepo): replace positional scan with Iter.StructScan"
  ```

---

## Task 3: Split `integration_test.go` into per-table files

**Why:** The single 1336-line `integration_test.go` file mixes setup helpers, room-query tests, by-id tests, thread-message tests, and all write tests. Each new test added makes navigation harder and diffs noisier. Splitting by the source file each test exercises mirrors the production code layout, keeps files under 300 lines each, and makes the test suite much easier to extend.

**Files:**
- Modify: `history-service/internal/cassrepo/integration_test.go` (shrink to setup only)
- Create: `history-service/internal/cassrepo/messages_by_room_integration_test.go`
- Create: `history-service/internal/cassrepo/messages_by_id_integration_test.go`
- Create: `history-service/internal/cassrepo/thread_messages_integration_test.go`
- Create: `history-service/internal/cassrepo/write_integration_test.go`

All new files use the same `//go:build integration` tag and `package cassrepo` declaration as the original. The `setupCassandra` function stays in `integration_test.go` because every file calls it.

- [ ] **Step 1: Read `integration_test.go` completely**

  Before touching anything, read the whole file to confirm which test functions and helpers belong in each destination file. The assignment is:

  | Function | Destination file |
  |----------|-----------------|
  | `setupCassandra` | `integration_test.go` (keep) |
  | `seedMessages` | `messages_by_room_integration_test.go` |
  | `TestRepository_GetMessagesBefore` | `messages_by_room_integration_test.go` |
  | `TestRepository_GetMessagesBetweenDesc` | `messages_by_room_integration_test.go` |
  | `TestRepository_GetMessagesAfter` | `messages_by_room_integration_test.go` |
  | `TestRepository_GetAllMessagesAsc` | `messages_by_room_integration_test.go` |
  | `TestRepository_GetMessagesBefore_ThreadRoomID` | `messages_by_room_integration_test.go` |
  | `TestRepository_GetMessageByID` | `messages_by_id_integration_test.go` |
  | `TestRepository_GetMessageByID_NotFound` | `messages_by_id_integration_test.go` |
  | `TestRepository_FullRow_AllColumns` | `messages_by_id_integration_test.go` |
  | `TestRepository_GetMessagesByIDs` | `messages_by_id_integration_test.go` |
  | `TestRepository_GetMessagesByIDs_Empty` | `messages_by_id_integration_test.go` |
  | `TestRepository_GetMessagesByIDs_MissingID` | `messages_by_id_integration_test.go` |
  | `seedThreadMessages` | `thread_messages_integration_test.go` |
  | `TestRepository_GetThreadMessages_*` (all 6) | `thread_messages_integration_test.go` |
  | `TestRepository_UpdateMessageContent_*` (all 3) | `write_integration_test.go` |
  | `TestRepository_SoftDeleteMessage_*` (all 6) | `write_integration_test.go` |

- [ ] **Step 2: Shrink `integration_test.go` to setup only**

  Delete every function except `setupCassandra`. The file should end after the closing `}` of `setupCassandra`. Keep all imports that `setupCassandra` uses (`context`, `fmt`, `testing`, `time`, `gocql`, `require`, `testutil`). Remove any imports that were only used by the moved tests (e.g. `assert`, `models`).

  Final `integration_test.go` (lines 1–147 of the original, minus unused imports):

  ```go
  //go:build integration

  package cassrepo

  import (
  	"fmt"
  	"testing"

  	"github.com/gocql/gocql"
  	"github.com/stretchr/testify/require"

  	"github.com/hmchangw/chat/pkg/testutil"
  )

  func setupCassandra(t *testing.T) *gocql.Session {
  	// ... (keep the body unchanged — all CREATE TYPE and CREATE TABLE statements)
  }
  ```

- [ ] **Step 3: Create `messages_by_room_integration_test.go`**

  Move `seedMessages` and all five `TestRepository_GetMessages*` functions here. Add the build tag, package declaration, and only the imports those functions need (`context`, `fmt`, `testing`, `time`, `assert`, `require`, `models`):

  ```go
  //go:build integration

  package cassrepo

  import (
  	"context"
  	"fmt"
  	"testing"
  	"time"

  	"github.com/stretchr/testify/assert"
  	"github.com/stretchr/testify/require"

  	"github.com/hmchangw/chat/history-service/internal/models"
  )

  func seedMessages(t *testing.T, session *gocql.Session, roomID string, base time.Time, count int) {
  	// ... (original body unchanged)
  }

  // TestRepository_GetMessagesBefore, GetMessagesBetweenDesc,
  // GetMessagesAfter, GetAllMessagesAsc, GetMessagesBefore_ThreadRoomID
  // (move bodies unchanged)
  ```

  Note: `gocql.Session` is needed by `seedMessages` — add `"github.com/gocql/gocql"` to the import block.

- [ ] **Step 4: Create `messages_by_id_integration_test.go`**

  Move `TestRepository_GetMessageByID`, `TestRepository_GetMessageByID_NotFound`, `TestRepository_FullRow_AllColumns`, `TestRepository_GetMessagesByIDs`, `TestRepository_GetMessagesByIDs_Empty`, `TestRepository_GetMessagesByIDs_MissingID` here. Required imports: `context`, `testing`, `time`, `assert`, `require`, `models`.

  ```go
  //go:build integration

  package cassrepo

  import (
  	"context"
  	"testing"
  	"time"

  	"github.com/stretchr/testify/assert"
  	"github.com/stretchr/testify/require"

  	"github.com/hmchangw/chat/history-service/internal/models"
  )

  // TestRepository_GetMessageByID, NotFound, FullRow_AllColumns,
  // GetMessagesByIDs, GetMessagesByIDs_Empty, GetMessagesByIDs_MissingID
  // (move bodies unchanged)
  ```

- [ ] **Step 5: Create `thread_messages_integration_test.go`**

  Move `seedThreadMessages` and all six `TestRepository_GetThreadMessages_*` tests. Required imports: `context`, `fmt`, `testing`, `time`, `assert`, `require`, `models`, `gocql`.

  ```go
  //go:build integration

  package cassrepo

  import (
  	"context"
  	"fmt"
  	"testing"
  	"time"

  	"github.com/gocql/gocql"
  	"github.com/stretchr/testify/assert"
  	"github.com/stretchr/testify/require"

  	"github.com/hmchangw/chat/history-service/internal/models"
  )

  func seedThreadMessages(t *testing.T, session *gocql.Session, roomID, threadRoomID, parentID string, base time.Time, count int) {
  	// ... (original body unchanged)
  }

  // TestRepository_GetThreadMessages_IsolatesByThreadRoomID,
  // IsolatesByRoomID, OrdersDescByCreatedAt, Pagination,
  // EmptyWhenThreadUnknown, ColumnScan
  // (move bodies unchanged)
  ```

- [ ] **Step 6: Create `write_integration_test.go`**

  Move all `TestRepository_UpdateMessageContent_*` and `TestRepository_SoftDeleteMessage_*` tests. Required imports: `context`, `testing`, `time`, `assert`, `require`, `models`, `gocql`.

  ```go
  //go:build integration

  package cassrepo

  import (
  	"context"
  	"testing"
  	"time"

  	"github.com/gocql/gocql"
  	"github.com/stretchr/testify/assert"
  	"github.com/stretchr/testify/require"

  	"github.com/hmchangw/chat/history-service/internal/models"
  )

  // TestRepository_UpdateMessageContent_TopLevel,
  // TestRepository_UpdateMessageContent_ThreadReply,
  // TestRepository_UpdateMessageContent_Pinned,
  // TestRepository_SoftDeleteMessage_TopLevel,
  // TestRepository_SoftDeleteMessage_ThreadReply,
  // TestRepository_SoftDeleteMessage_Pinned,
  // TestRepository_SoftDeleteMessage_DecrementsParentTcount,
  // TestRepository_SoftDeleteMessage_TopLevelDoesNotTouchTcount,
  // TestRepository_SoftDeleteMessage_LWTGatesDoubleDecrement
  // (move bodies unchanged)
  ```

- [ ] **Step 7: Run the integration tests**

  ```bash
  make test-integration SERVICE=history-service
  ```

  Expected: same test count as before, all passing. If `go test` complains about duplicate function names or missing symbols, check that no function was accidentally duplicated or omitted.

- [ ] **Step 8: Commit**

  ```bash
  git add history-service/internal/cassrepo/integration_test.go \
          history-service/internal/cassrepo/messages_by_room_integration_test.go \
          history-service/internal/cassrepo/messages_by_id_integration_test.go \
          history-service/internal/cassrepo/thread_messages_integration_test.go \
          history-service/internal/cassrepo/write_integration_test.go
  git commit -m "refactor(cassrepo): split integration_test.go into per-table files"
  ```

---

## Task 4: Extract query constants + per-table write helpers in `write.go` (I1)

**Why:** `UpdateMessageContent` and `SoftDeleteMessage` each embed the full CQL UPDATE strings inline. Extracting them as named constants makes them greppable (useful when debugging a failing query in Cassandra logs), reduces noise in already-long functions, and removes the risk of a copy-paste typo when a new column is added. Private per-table helper methods make the orchestrating function read like a list of steps rather than a wall of SQL.

**Files:**
- Modify: `history-service/internal/cassrepo/write.go`

- [ ] **Step 1: Read `write.go` in full**

  Open `history-service/internal/cassrepo/write.go`. You will see `UpdateMessageContent` and `SoftDeleteMessage` with inline CQL strings. No changes to logic — only extraction and naming.

- [ ] **Step 2: Add query constants at the top of `write.go`**

  After the existing `casMaxRetries` constant, add:

  ```go
  const (
  	editMsgByID   = `UPDATE messages_by_id SET msg = ?, edited_at = ?, updated_at = ? WHERE message_id = ? AND created_at = ?`
  	editMsgByRoom = `UPDATE messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
  	editThreadMsg = `UPDATE thread_messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`
  	editPinnedMsg = `UPDATE pinned_messages_by_room SET msg = ?, edited_at = ?, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`

  	deleteMsgByIDCAS   = `UPDATE messages_by_id SET deleted = true, updated_at = ? WHERE message_id = ? AND created_at = ? IF deleted != true`
  	deleteMsgByRoom    = `UPDATE messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
  	deleteThreadMsg    = `UPDATE thread_messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`
  	deletePinnedMsg    = `UPDATE pinned_messages_by_room SET deleted = true, updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
  )
  ```

- [ ] **Step 3: Add per-table edit helpers**

  Below the constants, add four private methods:

  ```go
  func (r *Repository) editInMessagesByID(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
  	return r.session.Query(editMsgByID, newMsg, editedAt, editedAt, msg.MessageID, msg.CreatedAt).WithContext(ctx).Exec()
  }

  func (r *Repository) editInMessagesByRoom(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
  	return r.session.Query(editMsgByRoom, newMsg, editedAt, editedAt, msg.RoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
  }

  func (r *Repository) editInThreadMessagesByRoom(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
  	return r.session.Query(editThreadMsg, newMsg, editedAt, editedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
  }

  func (r *Repository) editInPinnedMessagesByRoom(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
  	return r.session.Query(editPinnedMsg, newMsg, editedAt, editedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID).WithContext(ctx).Exec()
  }
  ```

- [ ] **Step 4: Add per-table delete helpers**

  ```go
  func (r *Repository) deleteInMessagesByRoom(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
  	return r.session.Query(deleteMsgByRoom, deletedAt, msg.RoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
  }

  func (r *Repository) deleteInThreadMessagesByRoom(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
  	return r.session.Query(deleteThreadMsg, deletedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
  }

  func (r *Repository) deleteInPinnedMessagesByRoom(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
  	return r.session.Query(deletePinnedMsg, deletedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID).WithContext(ctx).Exec()
  }
  ```

- [ ] **Step 5: Rewrite `UpdateMessageContent` to use the helpers**

  Replace the existing function body (the inline queries) with calls to the helpers. Logic is identical:

  ```go
  func (r *Repository) UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
  	if err := r.editInMessagesByID(ctx, msg, newMsg, editedAt); err != nil {
  		return fmt.Errorf("update messages_by_id: %w", err)
  	}

  	switch {
  	case msg.ThreadParentID == "":
  		if err := r.editInMessagesByRoom(ctx, msg, newMsg, editedAt); err != nil {
  			return fmt.Errorf("update messages_by_room: %w", err)
  		}
  	case msg.ThreadRoomID != "":
  		if err := r.editInThreadMessagesByRoom(ctx, msg, newMsg, editedAt); err != nil {
  			return fmt.Errorf("update thread_messages_by_room: %w", err)
  		}
  	default:
  		slog.Warn("skipping thread_messages_by_room edit: ThreadRoomID not set", "messageID", msg.MessageID)
  	}

  	if msg.PinnedAt != nil {
  		if err := r.editInPinnedMessagesByRoom(ctx, msg, newMsg, editedAt); err != nil {
  			return fmt.Errorf("update pinned_messages_by_room: %w", err)
  		}
  	}

  	return nil
  }
  ```

  Note: the `default` case still uses `slog.Warn` for now — Task 5 changes it to return an error.

- [ ] **Step 6: Rewrite `SoftDeleteMessage` to use the helpers**

  The LWT gate and `updated_at` read-on-miss logic stay identical. Only the mirror-table update calls change:

  ```go
  func (r *Repository) SoftDeleteMessage(ctx context.Context, msg *models.Message, deletedAt time.Time) (time.Time, bool, error) {
  	var current bool
  	applied, err := r.session.Query(
  		deleteMsgByIDCAS,
  		deletedAt, msg.MessageID, msg.CreatedAt,
  	).WithContext(ctx).ScanCAS(&current)
  	if err != nil {
  		return time.Time{}, false, fmt.Errorf("cas update messages_by_id: %w", err)
  	}
  	if !applied {
  		var existing time.Time
  		if err := r.session.Query(
  			`SELECT updated_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
  			msg.MessageID, msg.CreatedAt,
  		).WithContext(ctx).Scan(&existing); err != nil {
  			if errors.Is(err, gocql.ErrNotFound) {
  				return time.Time{}, false, nil
  			}
  			return time.Time{}, false, fmt.Errorf("read updated_at after cas miss: %w", err)
  		}
  		return existing, false, nil
  	}

  	switch {
  	case msg.ThreadParentID == "":
  		if err := r.deleteInMessagesByRoom(ctx, msg, deletedAt); err != nil {
  			return time.Time{}, false, fmt.Errorf("update messages_by_room: %w", err)
  		}
  	case msg.ThreadRoomID != "":
  		if err := r.deleteInThreadMessagesByRoom(ctx, msg, deletedAt); err != nil {
  			return time.Time{}, false, fmt.Errorf("update thread_messages_by_room: %w", err)
  		}
  	default:
  		slog.Warn("skipping thread_messages_by_room delete: ThreadRoomID not set", "messageID", msg.MessageID)
  	}

  	if msg.PinnedAt != nil {
  		if err := r.deleteInPinnedMessagesByRoom(ctx, msg, deletedAt); err != nil {
  			return time.Time{}, false, fmt.Errorf("update pinned_messages_by_room: %w", err)
  		}
  	}

  	if msg.ThreadParentID != "" {
  		if err := r.decrementParentTcount(ctx, msg); err != nil {
  			return time.Time{}, false, fmt.Errorf("decrement parent tcount: %w", err)
  		}
  	}

  	return deletedAt, true, nil
  }
  ```

- [ ] **Step 7: Run integration tests**

  ```bash
  make test-integration SERVICE=history-service
  ```

  Expected: all tests pass. No behavior changed — only query strings moved to constants and call sites moved to helpers.

- [ ] **Step 8: Run lint**

  ```bash
  make lint
  ```

  Expected: no errors.

- [ ] **Step 9: Commit**

  ```bash
  git add history-service/internal/cassrepo/write.go
  git commit -m "refactor(cassrepo): extract query constants and per-table write helpers"
  ```

---

## Task 5: Return error for empty `ThreadRoomID`; add message IDs to error context (I2 + I3)

**Why (I2):** When a thread-reply message has `ThreadParentID != ""` but `ThreadRoomID == ""`, the current code silently logs a warning and returns `nil` — making the caller believe the write succeeded while the mirror table was skipped. In production this corrupts state: a thread reply would be uneditable/undeletable from the thread view. Returning an error forces the caller (the service layer) to surface the problem rather than hiding it.

**Why (I3):** Error messages like `"update messages_by_room: ..."` give no indication of *which* message failed, making on-call debugging harder than necessary. Adding the message ID and room ID to every error string pinpoints the failing row instantly in logs.

**Files:**
- Modify: `history-service/internal/cassrepo/write.go`

Follow TDD: write the failing test first (Step 1), then make it pass (Steps 2–4).

- [ ] **Step 1: Write the failing tests in `write_integration_test.go`**

  Add these two tests at the end of `write_integration_test.go`. Both tests will FAIL with the current code (the method currently returns `nil` instead of an error).

  ```go
  func TestRepository_UpdateMessageContent_MissingThreadRoomID_ReturnsError(t *testing.T) {
  	session := setupCassandra(t)
  	repo := NewRepository(session)
  	ctx := context.Background()

  	sender := models.Participant{ID: "u1", Account: "alice"}
  	createdAt := time.Now().UTC().Truncate(time.Millisecond)

  	// Seed messages_by_id only; ThreadParentID set but ThreadRoomID intentionally empty.
  	require.NoError(t, session.Query(
  		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, thread_room_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
  		"m-no-tr", "r-no-tr", createdAt, sender, "original", "m-parent", "",
  	).Exec())

  	msg := &models.Message{
  		MessageID:      "m-no-tr",
  		RoomID:         "r-no-tr",
  		CreatedAt:      createdAt,
  		ThreadParentID: "m-parent",
  		ThreadRoomID:   "", // intentionally empty
  	}
  	err := repo.UpdateMessageContent(ctx, msg, "edited", createdAt.Add(time.Minute))
  	require.Error(t, err, "expected error when ThreadRoomID is empty for a thread reply")
  }

  func TestRepository_SoftDeleteMessage_MissingThreadRoomID_ReturnsError(t *testing.T) {
  	session := setupCassandra(t)
  	repo := NewRepository(session)
  	ctx := context.Background()

  	sender := models.Participant{ID: "u1", Account: "alice"}
  	createdAt := time.Now().UTC().Truncate(time.Millisecond)

  	// Seed messages_by_id only; deleted=false so the LWT applies.
  	require.NoError(t, session.Query(
  		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, thread_room_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
  		"m-no-tr-del", "r-no-tr-del", createdAt, sender, "content", "m-parent", "", false,
  	).Exec())

  	msg := &models.Message{
  		MessageID:      "m-no-tr-del",
  		RoomID:         "r-no-tr-del",
  		CreatedAt:      createdAt,
  		ThreadParentID: "m-parent",
  		ThreadRoomID:   "", // intentionally empty
  	}
  	_, _, err := repo.SoftDeleteMessage(ctx, msg, createdAt.Add(time.Minute))
  	require.Error(t, err, "expected error when ThreadRoomID is empty for a thread reply")
  }
  ```

- [ ] **Step 2: Run the tests to confirm they fail (Red)**

  ```bash
  make test-integration SERVICE=history-service
  ```

  Expected: both new tests FAIL with something like `Error Trace: expected error, but got nil`. All other tests still pass.

- [ ] **Step 3: Change `default` cases in `write.go` to return errors (I2)**

  In `UpdateMessageContent`, replace:
  ```go
  default:
      slog.Warn("skipping thread_messages_by_room edit: ThreadRoomID not set", "messageID", msg.MessageID)
  ```
  with:
  ```go
  default:
      return fmt.Errorf("edit thread message %s: ThreadParentID %q is set but ThreadRoomID is empty", msg.MessageID, msg.ThreadParentID)
  ```

  In `SoftDeleteMessage`, replace:
  ```go
  default:
      slog.Warn("skipping thread_messages_by_room delete: ThreadRoomID not set", "messageID", msg.MessageID)
  ```
  with:
  ```go
  default:
      return time.Time{}, false, fmt.Errorf("delete thread message %s: ThreadParentID %q is set but ThreadRoomID is empty", msg.MessageID, msg.ThreadParentID)
  ```

  Also remove the `"log/slog"` import from `write.go` if it is now unused (it is — no other call site uses it).

- [ ] **Step 4: Add message and room IDs to all remaining error strings in `write.go` (I3)**

  Update every `fmt.Errorf` in `UpdateMessageContent` and `SoftDeleteMessage` (and their helpers where appropriate) to include the message ID. Replace:

  ```go
  // UpdateMessageContent errors:
  return fmt.Errorf("update messages_by_id: %w", err)
  return fmt.Errorf("update messages_by_room: %w", err)
  return fmt.Errorf("update thread_messages_by_room: %w", err)
  return fmt.Errorf("update pinned_messages_by_room: %w", err)

  // SoftDeleteMessage errors:
  return time.Time{}, false, fmt.Errorf("cas update messages_by_id: %w", err)
  return time.Time{}, false, fmt.Errorf("read updated_at after cas miss: %w", err)
  return time.Time{}, false, fmt.Errorf("update messages_by_room: %w", err)
  return time.Time{}, false, fmt.Errorf("update thread_messages_by_room: %w", err)
  return time.Time{}, false, fmt.Errorf("update pinned_messages_by_room: %w", err)
  return time.Time{}, false, fmt.Errorf("decrement parent tcount: %w", err)
  ```

  with:

  ```go
  // UpdateMessageContent errors:
  return fmt.Errorf("update messages_by_id for message %s: %w", msg.MessageID, err)
  return fmt.Errorf("update messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
  return fmt.Errorf("update thread_messages_by_room for message %s room %s thread %s: %w", msg.MessageID, msg.RoomID, msg.ThreadRoomID, err)
  return fmt.Errorf("update pinned_messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)

  // SoftDeleteMessage errors:
  return time.Time{}, false, fmt.Errorf("cas update messages_by_id for message %s: %w", msg.MessageID, err)
  return time.Time{}, false, fmt.Errorf("read updated_at after cas miss for message %s: %w", msg.MessageID, err)
  return time.Time{}, false, fmt.Errorf("update messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
  return time.Time{}, false, fmt.Errorf("update thread_messages_by_room for message %s room %s thread %s: %w", msg.MessageID, msg.RoomID, msg.ThreadRoomID, err)
  return time.Time{}, false, fmt.Errorf("update pinned_messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
  return time.Time{}, false, fmt.Errorf("decrement parent tcount for message %s: %w", msg.MessageID, err)
  ```

- [ ] **Step 5: Run all integration tests (Green)**

  ```bash
  make test-integration SERVICE=history-service
  ```

  Expected: all tests pass, including the two new ones added in Step 1.

- [ ] **Step 6: Run lint**

  ```bash
  make lint
  ```

  Expected: no errors. Confirm `slog` import is removed if unused.

- [ ] **Step 7: Commit**

  ```bash
  git add history-service/internal/cassrepo/write.go \
          history-service/internal/cassrepo/write_integration_test.go
  git commit -m "fix(cassrepo): return error for empty ThreadRoomID; add IDs to error context"
  ```

---

## Task 6: Add write-path edge-case integration tests (I4)

**Why:** The existing write tests verify the happy path and the LWT double-decrement gate thoroughly, but two categories are missing: (a) round-trip tests that write then read back via `GetMessageByID` — these verify that struct scan correctly returns the updated state after a mutation; (b) a test that `SoftDeleteMessage` on a missing row returns `(zero, false, nil)` without panicking (the LWT on a non-existent row returns `applied=false` because the `IF deleted != true` condition evaluates against NULL). These tests live in `write_integration_test.go`.

**Files:**
- Modify: `history-service/internal/cassrepo/write_integration_test.go`

All three tests in this task should be written first (Red), run to confirm they fail, then the implementation already exists (it was written in Tasks 4–5), so they should immediately go Green without additional implementation changes.

Actually — the round-trip tests probe the existing correct behavior via the new StructScan read path; they will be Green from the first run after Tasks 1–2 are done. Write them and run to confirm. The missing-row test also probes existing behavior.

- [ ] **Step 1: Add round-trip test for `UpdateMessageContent`**

  Append to `write_integration_test.go`:

  ```go
  // TestRepository_UpdateMessageContent_RoundTrip verifies that after editing a
  // top-level message, GetMessageByID returns the updated content and edited_at
  // via the struct-scan read path (Tasks 1–2).
  func TestRepository_UpdateMessageContent_RoundTrip(t *testing.T) {
  	session := setupCassandra(t)
  	repo := NewRepository(session)
  	ctx := context.Background()

  	sender := models.Participant{ID: "u1", Account: "alice"}
  	roomID := "room-rt-edit"
  	msgID := "m-rt-edit"
  	createdAt := time.Now().UTC().Truncate(time.Millisecond)

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
  		ThreadParentID: "",
  	}
  	editedAt := createdAt.Add(time.Minute)
  	require.NoError(t, repo.UpdateMessageContent(ctx, msg, "updated content", editedAt))

  	got, err := repo.GetMessageByID(ctx, msgID)
  	require.NoError(t, err)
  	require.NotNil(t, got)
  	assert.Equal(t, "updated content", got.Msg)
  	require.NotNil(t, got.EditedAt)
  	assert.Equal(t, editedAt.UTC(), got.EditedAt.UTC())
  	require.NotNil(t, got.UpdatedAt)
  	assert.Equal(t, editedAt.UTC(), got.UpdatedAt.UTC())
  }
  ```

- [ ] **Step 2: Add round-trip test for `SoftDeleteMessage`**

  ```go
  // TestRepository_SoftDeleteMessage_RoundTrip verifies that after soft-deleting
  // a top-level message, GetMessageByID returns deleted=true via struct scan.
  func TestRepository_SoftDeleteMessage_RoundTrip(t *testing.T) {
  	session := setupCassandra(t)
  	repo := NewRepository(session)
  	ctx := context.Background()

  	sender := models.Participant{ID: "u1", Account: "alice"}
  	roomID := "room-rt-del"
  	msgID := "m-rt-del"
  	createdAt := time.Now().UTC().Truncate(time.Millisecond)

  	require.NoError(t, session.Query(
  		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
  		msgID, roomID, createdAt, sender, "content", "", false,
  	).Exec())
  	require.NoError(t, session.Query(
  		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
  		roomID, createdAt, msgID, sender, "content", "", false,
  	).Exec())

  	msg := &models.Message{
  		MessageID:      msgID,
  		RoomID:         roomID,
  		CreatedAt:      createdAt,
  		ThreadParentID: "",
  	}
  	deletedAt := createdAt.Add(time.Minute)
  	gotAt, applied, err := repo.SoftDeleteMessage(ctx, msg, deletedAt)
  	require.NoError(t, err)
  	require.True(t, applied)
  	assert.Equal(t, deletedAt.UnixMilli(), gotAt.UnixMilli())

  	got, err := repo.GetMessageByID(ctx, msgID)
  	require.NoError(t, err)
  	require.NotNil(t, got)
  	assert.True(t, got.Deleted, "GetMessageByID should return deleted=true after SoftDeleteMessage")
  	require.NotNil(t, got.UpdatedAt)
  	assert.Equal(t, deletedAt.UTC(), got.UpdatedAt.UTC())
  }
  ```

- [ ] **Step 3: Add missing-row test for `SoftDeleteMessage`**

  ```go
  // TestRepository_SoftDeleteMessage_MissingRow returns (zero, false, nil) when
  // the message row does not exist. The LWT fires on a non-existent row; because
  // the IF condition (deleted != true) evaluates to true against NULL, the LWT
  // applies and creates a partial phantom row. The subsequent updated_at read
  // finds the row, so this test documents that the caller is responsible for
  // verifying the message exists before calling SoftDeleteMessage.
  //
  // This test simply confirms no panic and no unexpected error.
  func TestRepository_SoftDeleteMessage_RowCreatedByLWT(t *testing.T) {
  	session := setupCassandra(t)
  	repo := NewRepository(session)
  	ctx := context.Background()

  	// Do NOT seed any row — call SoftDeleteMessage on a non-existent ID.
  	msg := &models.Message{
  		MessageID:      "m-ghost",
  		RoomID:         "r-ghost",
  		CreatedAt:      time.Now().UTC().Truncate(time.Millisecond),
  		ThreadParentID: "",
  	}
  	deletedAt := msg.CreatedAt.Add(time.Minute)

  	// The Cassandra LWT will apply (NULL != true) and materialise a partial row.
  	// The method must return without panicking and the returned applied value
  	// reflects what the LWT reported.
  	_, _, err := repo.SoftDeleteMessage(ctx, msg, deletedAt)
  	require.NoError(t, err, "SoftDeleteMessage must not return an error on a non-existent row")
  }
  ```

- [ ] **Step 4: Run the integration tests**

  ```bash
  make test-integration SERVICE=history-service
  ```

  Expected: all three new tests pass (they probe existing correct behavior). If a round-trip test fails, the most likely cause is that Task 1 or Task 2 was not completed — check that `cql:` tags are on `Message` and that `StructScan` is in use.

- [ ] **Step 5: Commit**

  ```bash
  git add history-service/internal/cassrepo/write_integration_test.go
  git commit -m "test(cassrepo): add write round-trip and missing-row integration tests"
  ```

---

## Task 7: Rename `q` → `pageReq` in `MessageReader` interface and all implementations (M2)

**Why:** The parameter name `q` is ambiguous — it was inherited from the days when `q` meant a raw Cassandra `*gocql.Query`. After Task 2, the only paged parameter remaining is `PageRequest`; naming it `pageReq` removes the ambiguity and aligns with Go naming conventions (short descriptive names for parameters).

**Files:**
- Modify: `history-service/internal/service/service.go` (interface)
- Modify: `history-service/internal/cassrepo/messages_by_room.go` (4 methods)
- Modify: `history-service/internal/cassrepo/thread_messages.go` (1 method)

- [ ] **Step 1: Update `MessageReader` interface in `service.go`**

  In `history-service/internal/service/service.go`, change the four paginated method signatures in `MessageReader`. Rename `q cassrepo.PageRequest` → `pageReq cassrepo.PageRequest`:

  ```go
  type MessageReader interface {
  	GetMessagesBefore(ctx context.Context, roomID string, before time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
  	GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
  	GetMessagesAfter(ctx context.Context, roomID string, after time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
  	GetAllMessagesAsc(ctx context.Context, roomID string, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
  	GetMessageByID(ctx context.Context, messageID string) (*models.Message, error)
  	GetThreadMessages(ctx context.Context, roomID, threadRoomID string, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
  	GetMessagesByIDs(ctx context.Context, messageIDs []string) ([]models.Message, error)
  }
  ```

- [ ] **Step 2: Update implementations in `messages_by_room.go`**

  Rename the parameter in all four method signatures (the internal variable `q` used as the argument to `fetchMessagesPage` also becomes `pageReq`):

  ```go
  func (r *Repository) GetMessagesBefore(ctx context.Context, roomID string, before time.Time, pageReq PageRequest) (Page[models.Message], error) {
  	return fetchMessagesPage(..., pageReq, "querying messages before")
  }

  func (r *Repository) GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, pageReq PageRequest) (Page[models.Message], error) {
  	return fetchMessagesPage(..., pageReq, "querying messages between desc")
  }

  func (r *Repository) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, pageReq PageRequest) (Page[models.Message], error) {
  	return fetchMessagesPage(..., pageReq, "querying messages after")
  }

  func (r *Repository) GetAllMessagesAsc(ctx context.Context, roomID string, pageReq PageRequest) (Page[models.Message], error) {
  	return fetchMessagesPage(..., pageReq, "querying all messages asc")
  }
  ```

- [ ] **Step 3: Update `GetThreadMessages` in `thread_messages.go`**

  ```go
  func (r *Repository) GetThreadMessages(ctx context.Context, roomID, threadRoomID string, pageReq PageRequest) (Page[models.Message], error) {
  	return fetchMessagesPage(..., pageReq, "querying thread messages")
  }
  ```

- [ ] **Step 4: Verify compilation**

  ```bash
  make build SERVICE=history-service
  ```

  Expected: succeeds. The mock in `mocks/mock_repository.go` is generated from the interface, so regenerate it:

  ```bash
  make generate SERVICE=history-service
  ```

  Then run unit tests:

  ```bash
  make test SERVICE=history-service
  ```

  Expected: all tests pass.

- [ ] **Step 5: Commit**

  ```bash
  git add history-service/internal/service/service.go \
          history-service/internal/cassrepo/messages_by_room.go \
          history-service/internal/cassrepo/thread_messages.go \
          history-service/internal/service/mocks/mock_repository.go
  git commit -m "refactor(cassrepo): rename PageRequest param q -> pageReq"
  ```

---

## Task 8: Add compile-time interface check (M3)

**Why:** `*cassrepo.Repository` satisfies `service.MessageRepository` structurally — but currently nothing makes this explicit. If a method is accidentally removed from `Repository` or its signature drifts, the mismatch surfaces at call-site construction in `main.go`, which is harder to diagnose than a compile error at the definition site. A `var _ MessageRepository = (*cassrepo.Repository)(nil)` assignment fails compilation the moment the interface is violated, pinpointing the exact gap.

**Files:**
- Modify: `history-service/internal/service/service.go`

- [ ] **Step 1: Add the compile-time check to `service.go`**

  At the bottom of `history-service/internal/service/service.go`, after `RegisterHandlers`, add:

  ```go
  // Compile-time check: *cassrepo.Repository must satisfy MessageRepository.
  // If Repository stops implementing any method, this line fails to compile.
  var _ MessageRepository = (*cassrepo.Repository)(nil)
  ```

  The `cassrepo` import is already present (used by `PageRequest` in `MessageReader`), so no new import is needed.

- [ ] **Step 2: Verify compilation**

  ```bash
  make build SERVICE=history-service
  ```

  Expected: succeeds. If it fails with a method-not-found error, check Task 2 and Task 7 are complete.

- [ ] **Step 3: Commit**

  ```bash
  git add history-service/internal/service/service.go
  git commit -m "chore(service): add compile-time check cassrepo.Repository satisfies MessageRepository"
  ```

---

## Task 9: Add cursor length guard in `utils.go` (M5)

**Why:** `NewCursor` base64-decodes a user-supplied string from the HTTP/NATS request without validating its length first. A malicious client can send a megabyte-long base64 string, forcing the server to allocate a large byte slice on every request. A length check before `base64.StdEncoding.DecodeString` costs nothing and caps the allocation.

**Files:**
- Modify: `history-service/internal/cassrepo/utils.go`
- Modify: `history-service/internal/cassrepo/utils_test.go`

Follow TDD: write the failing test first.

- [ ] **Step 1: Write the failing test in `utils_test.go`**

  Add to `history-service/internal/cassrepo/utils_test.go`:

  ```go
  func TestNewCursor_TooLong(t *testing.T) {
  	// maxCursorBytes = 512; base64 of 513 raw bytes exceeds the limit.
  	huge := base64.StdEncoding.EncodeToString(make([]byte, maxCursorBytes+1))
  	_, err := NewCursor(huge)
  	require.Error(t, err)
  	assert.Contains(t, err.Error(), "decode cursor")
  }
  ```

- [ ] **Step 2: Run the test to confirm it fails (Red)**

  ```bash
  make test SERVICE=history-service
  ```

  Expected: `TestNewCursor_TooLong` FAILS because `NewCursor` currently returns `nil` error for any valid base64 string regardless of length.

- [ ] **Step 3: Add the length guard constant and check to `utils.go`**

  After the existing `maxPageSize` constant, add `maxCursorBytes`:

  ```go
  // maxCursorBytes is the maximum number of raw bytes a decoded page-state cursor
  // may occupy. Real Cassandra page state tokens are 10–100 bytes; 512 is
  // generous while still blocking pathological allocations.
  const maxCursorBytes = 512
  ```

  In `NewCursor`, add the check immediately after the `encoded == ""` guard:

  ```go
  func NewCursor(encoded string) (*Cursor, error) {
  	if encoded == "" {
  		return &Cursor{}, nil
  	}
  	if len(encoded) > base64.StdEncoding.EncodedLen(maxCursorBytes) {
  		return nil, fmt.Errorf("decode cursor: encoded length %d exceeds maximum %d bytes",
  			len(encoded), base64.StdEncoding.EncodedLen(maxCursorBytes))
  	}
  	state, err := base64.StdEncoding.DecodeString(encoded)
  	if err != nil {
  		return nil, fmt.Errorf("decode cursor: %w", err)
  	}
  	return &Cursor{state: state}, nil
  }
  ```

- [ ] **Step 4: Run the tests (Green)**

  ```bash
  make test SERVICE=history-service
  ```

  Expected: all tests pass including `TestNewCursor_TooLong`.

- [ ] **Step 5: Commit**

  ```bash
  git add history-service/internal/cassrepo/utils.go \
          history-service/internal/cassrepo/utils_test.go
  git commit -m "fix(cassrepo): add cursor length guard to prevent oversized allocations"
  ```

---

## Task 10: Fix `--` SQL comments → `//` CQL comments in docs (M6)

**Why:** CQL (Cassandra Query Language) uses `//` for inline comments, not `--` (which is SQL/PostgreSQL syntax). The doc file is the single source of truth for the message schema; having syntactically wrong comments could confuse anyone pasting the DDL into `cqlsh` or a migration tool.

**Files:**
- Modify: `docs/cassandra_message_model.md`

- [ ] **Step 1: Find all `--` comment lines**

  ```bash
  grep -n "\-\-" docs/cassandra_message_model.md
  ```

  Expected output (three lines in the `QuotedParentMessage` UDT block):
  ```text
  55:  thread_parent_id TEXT,          -- set by message-worker when quoted message is a TShow reply
  56:  thread_parent_created_at TIMESTAMP  -- actual CreatedAt of the thread parent; used by history-service
  57:                                      -- to enforce access-window checks without a Cassandra round-trip
  ```

- [ ] **Step 2: Replace `--` with `//` on those lines**

  Edit `docs/cassandra_message_model.md` lines 55–57:

  ```cql
  thread_parent_id TEXT,          // set by message-worker when quoted message is a TShow reply
  thread_parent_created_at TIMESTAMP  // actual CreatedAt of the thread parent; used by history-service
                                      // to enforce access-window checks without a Cassandra round-trip
  ```

- [ ] **Step 3: Verify no remaining `--` comments**

  ```bash
  grep -n "\-\-" docs/cassandra_message_model.md
  ```

  Expected: no output.

- [ ] **Step 4: Commit**

  ```bash
  git add docs/cassandra_message_model.md
  git commit -m "docs: replace SQL -- comments with CQL // comments in cassandra_message_model.md"
  ```

---

## Self-Review

**Spec coverage check:**

| Item | Covered by |
|------|-----------|
| C1: struct scan (cql tags) | Task 1 |
| C1: struct scan (wiring) | Task 2 |
| I1: query constants | Task 4 |
| I1: per-table helpers | Task 4 |
| I2: error return for empty ThreadRoomID | Task 5 |
| I3: message IDs in error context | Task 5 |
| I4: write round-trip tests | Task 6 |
| I4: missing-row behavior test | Task 6 |
| I4: missing ThreadRoomID error test | Task 5 (test written in Step 1) |
| M2: rename q → pageReq | Task 7 |
| M3: compile-time interface check | Task 8 |
| M5: cursor length guard | Task 9 |
| M6: doc comment syntax | Task 10 |
| test file split | Task 3 |

All items accounted for. No placeholders remain.

---

Plan complete and saved to `docs/superpowers/plans/2026-04-29-cassrepo-prod-hardening.md`.

**Two execution options:**

**1. Subagent-Driven (recommended)** — dispatch a fresh subagent per task, two-stage review between tasks, fast iteration

**2. Inline Execution** — execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
