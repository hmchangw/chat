# History Service `getThreadMessages` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a new NATS request/reply endpoint `getThreadMessages` to `history-service` that returns thread replies for a given parent message, paginated newest-first with a cursor.

**Architecture:** Thin handler in `history-service/internal/service` calls a new Cassandra repo method `GetThreadMessages` that queries `thread_messages_by_room` on the partition key `room_id` + first clustering key `thread_room_id`. Handler derives the room from the fetched parent message — it never reads `roomID` from the NATS subject. Cursor-based pagination via existing `cassrepo.QueryBuilder` (native Cassandra `PageState`).

**Tech Stack:** Go 1.25, `github.com/gocql/gocql`, `pkg/natsrouter`, `go.uber.org/mock`, `testify`, `testcontainers-go/modules/cassandra` for integration tests.

**Spec:** `docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md`

---

## File Map

| File | Change |
|------|--------|
| `pkg/subject/subject.go` | Add `MsgThreadPattern(siteID)` and `MsgThreadWildcard(siteID)` |
| `pkg/subject/subject_test.go` | Add cases for the two new builders |
| `history-service/internal/models/message.go` | Add `GetThreadMessagesRequest` + `GetThreadMessagesResponse` |
| `history-service/internal/cassrepo/repository.go` | Add `threadMessageColumns`, `threadMessageScanDest`, `GetThreadMessages` |
| `history-service/internal/cassrepo/integration_test.go` | Add seeding helper + four integration test cases |
| `history-service/internal/service/service.go` | Extend `MessageRepository` interface; register handler |
| `history-service/internal/service/messages.go` | Add `GetThreadMessages` handler |
| `history-service/internal/service/messages_test.go` | Table-driven unit tests for the handler |
| `history-service/internal/service/mocks/mock_repository.go` | Regenerated via `make generate SERVICE=history-service` |

No other services, docker init, or docs are touched.

---

## Task 1 — Subject builders in `pkg/subject`

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write failing tests for the two new builders**

Open `pkg/subject/subject_test.go`. In `TestSubjectBuilders` (the table starting around line 9), add a new case to the `tests` slice next to the other `Msg*Pattern` entries. Since the existing file doesn't yet list `MsgHistoryPattern`/`MsgNextPattern`/`MsgSurroundingPattern`/`MsgGetPattern` in that test (they are `Pattern` style, not `Wildcard`), add the thread pattern case to `TestSubjectBuilders` and the thread wildcard case to `TestWildcardPatterns`.

In `TestSubjectBuilders` add (before the closing `}` of the `tests` slice):

```go
{"MsgThreadPattern", subject.MsgThreadPattern("site-a"),
    "chat.user.{account}.request.room.{roomID}.site-a.msg.thread"},
```

In `TestWildcardPatterns` add (before the closing `}` of that slice):

```go
{"MsgThreadWild", subject.MsgThreadWildcard("site-a"),
    "chat.user.*.request.room.*.site-a.msg.thread"},
```

- [ ] **Step 2: Run tests — confirm they fail**

```bash
go test -race ./pkg/subject/...
```

Expected: FAIL with `undefined: subject.MsgThreadPattern` and `undefined: subject.MsgThreadWildcard`.

- [ ] **Step 3: Add the two builders**

Open `pkg/subject/subject.go`. Immediately after `MsgGetPattern` (line 194 area), add:

```go
func MsgThreadPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.thread", siteID)
}
```

And immediately after `MsgHistoryWildcard` (line 156 area), add:

```go
func MsgThreadWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.thread", siteID)
}
```

- [ ] **Step 4: Run tests — confirm they pass**

```bash
go test -race ./pkg/subject/...
```

Expected: PASS. All existing cases still green.

- [ ] **Step 5: Lint + commit**

```bash
make lint
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(pkg/subject): add MsgThread pattern and wildcard builders"
```

---

## Task 2 — Request and response types

**Files:**
- Modify: `history-service/internal/models/message.go`

These are plain data types with no logic. No test file needed — they're exercised via handler tests in Task 5. Add them first so later tasks can reference them.

- [ ] **Step 1: Add the two types at the end of the file**

Open `history-service/internal/models/message.go`. Append (after the existing `GetMessageByIDRequest` definition):

```go
// GetThreadMessagesRequest is the payload for loading replies in a thread.
type GetThreadMessagesRequest struct {
	ThreadMessageID string `json:"threadMessageId"` // parent message ID (old Meteor `tmid`)
	Cursor          string `json:"cursor,omitempty"`
	Limit           int    `json:"limit"`
}

// GetThreadMessagesResponse is the response for GetThreadMessages.
type GetThreadMessagesResponse struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"nextCursor,omitempty"`
	HasNext    bool      `json:"hasNext"`
}
```

- [ ] **Step 2: Confirm the package still builds**

```bash
go build ./history-service/...
```

Expected: no output, exit 0.

- [ ] **Step 3: Lint + commit**

```bash
make lint
git add history-service/internal/models/message.go
git commit -m "feat(history-service): add thread-messages request/response types"
```

---

## Task 3 — Cassandra repository method `GetThreadMessages`

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Modify: `history-service/internal/cassrepo/integration_test.go`

TDD note: unit tests for this are in Task 5 via mocks; real DB coverage lives in the integration test we write here in Step 1. Integration tests require Docker (`testcontainers-go`).

- [ ] **Step 1: Write failing integration tests**

Open `history-service/internal/cassrepo/integration_test.go`. The file already creates the `messages_by_room` and `messages_by_id` tables in `setupCassandra`. We need to also create `thread_messages_by_room` and add a seeding helper and four test cases.

Inside `setupCassandra`, add the `thread_messages_by_room` table creation right after the `messages_by_id` `CREATE TABLE` block (after line 107, before `cluster.Keyspace = "chat_test"`):

```go
require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.thread_messages_by_room (
    room_id TEXT,
    thread_room_id TEXT,
    created_at TIMESTAMP,
    message_id TEXT,
    thread_parent_id TEXT,
    sender FROZEN<"Participant">,
    target_user FROZEN<"Participant">,
    msg TEXT,
    mentions SET<FROZEN<"Participant">>,
    attachments LIST<BLOB>,
    file FROZEN<"File">,
    card FROZEN<"Card">,
    card_action FROZEN<"CardAction">,
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
) WITH CLUSTERING ORDER BY (thread_room_id DESC, created_at DESC, message_id DESC)`).Exec())
```

Then, at the end of the file, add a seeding helper and the four tests:

```go
func seedThreadMessages(t *testing.T, session *gocql.Session, roomID, threadRoomID, parentID string, base time.Time, count int) {
	t.Helper()
	sender := models.Participant{ID: "u1", Account: "user1"}
	for i := 0; i < count; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		err := session.Query(
			`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, thread_parent_id, sender, msg) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			roomID, threadRoomID, ts, fmt.Sprintf("%s-reply-%d", threadRoomID, i), parentID, sender, fmt.Sprintf("reply-%d", i),
		).Exec()
		require.NoError(t, err)
	}
}

func TestRepository_GetThreadMessages_IsolatesByThreadRoomID(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// Two threads in the same room with different thread_room_ids.
	seedThreadMessages(t, session, "r-A", "tr-1", "m-parent-1", base, 3)
	seedThreadMessages(t, session, "r-A", "tr-2", "m-parent-2", base, 5)

	q, err := ParsePageRequest("", 100)
	require.NoError(t, err)

	page1, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q)
	require.NoError(t, err)
	assert.Len(t, page1.Data, 3)
	for _, m := range page1.Data {
		assert.Equal(t, "tr-1", m.ThreadRoomID)
		assert.Equal(t, "m-parent-1", m.ThreadParentID)
	}

	page2, err := repo.GetThreadMessages(ctx, "r-A", "tr-2", q)
	require.NoError(t, err)
	assert.Len(t, page2.Data, 5)
	for _, m := range page2.Data {
		assert.Equal(t, "tr-2", m.ThreadRoomID)
	}
}

func TestRepository_GetThreadMessages_IsolatesByRoomID(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// Same thread_room_id value in two different rooms — partition key must isolate them.
	seedThreadMessages(t, session, "r-A", "tr-1", "m-parent-A", base, 2)
	seedThreadMessages(t, session, "r-B", "tr-1", "m-parent-B", base, 4)

	q, err := ParsePageRequest("", 100)
	require.NoError(t, err)

	pageA, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q)
	require.NoError(t, err)
	assert.Len(t, pageA.Data, 2)

	pageB, err := repo.GetThreadMessages(ctx, "r-B", "tr-1", q)
	require.NoError(t, err)
	assert.Len(t, pageB.Data, 4)
}

func TestRepository_GetThreadMessages_OrdersDescByCreatedAt(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	seedThreadMessages(t, session, "r-A", "tr-1", "m-parent-1", base, 4)

	q, err := ParsePageRequest("", 100)
	require.NoError(t, err)

	page, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q)
	require.NoError(t, err)
	require.Len(t, page.Data, 4)
	for i := 0; i < len(page.Data)-1; i++ {
		assert.True(t, page.Data[i].CreatedAt.After(page.Data[i+1].CreatedAt),
			"expected DESC order at index %d", i)
	}
}

func TestRepository_GetThreadMessages_Pagination(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	seedThreadMessages(t, session, "r-A", "tr-1", "m-parent-1", base, 7)

	q, err := ParsePageRequest("", 3)
	require.NoError(t, err)

	page1, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q)
	require.NoError(t, err)
	assert.Len(t, page1.Data, 3)
	assert.True(t, page1.HasNext)
	require.NotEmpty(t, page1.NextCursor)

	q2, err := ParsePageRequest(page1.NextCursor, 3)
	require.NoError(t, err)
	page2, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q2)
	require.NoError(t, err)
	assert.Len(t, page2.Data, 3)

	q3, err := ParsePageRequest(page2.NextCursor, 3)
	require.NoError(t, err)
	page3, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q3)
	require.NoError(t, err)
	assert.Len(t, page3.Data, 1)
	assert.False(t, page3.HasNext)

	// No overlap, no gaps.
	seen := map[string]bool{}
	for _, m := range page1.Data {
		seen[m.MessageID] = true
	}
	for _, m := range page2.Data {
		assert.False(t, seen[m.MessageID], "page2 overlaps page1: %s", m.MessageID)
		seen[m.MessageID] = true
	}
	for _, m := range page3.Data {
		assert.False(t, seen[m.MessageID], "page3 overlaps earlier pages: %s", m.MessageID)
	}
	assert.Len(t, seen, 6) // page1 (3) + page2 (3); page3 tracked via the False check only
}

func TestRepository_GetThreadMessages_EmptyWhenThreadUnknown(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	page, err := repo.GetThreadMessages(ctx, "r-A", "tr-nonexistent", q)
	require.NoError(t, err)
	assert.Empty(t, page.Data)
	assert.False(t, page.HasNext)
	assert.Empty(t, page.NextCursor)
}
```

- [ ] **Step 2: Run the integration tests — confirm they fail**

```bash
make test-integration SERVICE=history-service
```

Expected: compilation error `repo.GetThreadMessages undefined`. (If Docker isn't available, the test binary still needs to compile — `go build -tags integration ./history-service/...` must also fail the same way.)

- [ ] **Step 3: Implement `threadMessageColumns` + scan helper + method**

Open `history-service/internal/cassrepo/repository.go`. After the existing `messageByIDQuery` constant (line 25), add:

```go
// threadMessageColumns lists the columns present in thread_messages_by_room.
// This table has a strict subset of messages_by_room's columns — no tshow,
// no tcount, no thread_parent_created_at, no pinned_* — so it needs its own
// column list and scan destination.
const threadMessageColumns = "room_id, thread_room_id, created_at, message_id, thread_parent_id, " +
	"sender, target_user, msg, mentions, attachments, file, card, card_action, " +
	"quoted_parent_message, visible_to, unread, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"

const threadMessageQuery = "SELECT " + threadMessageColumns + " FROM thread_messages_by_room"

// threadMessageScanDest returns Scan destination pointers for threadMessageColumns in order.
func threadMessageScanDest(m *models.Message) []any {
	return []any{
		&m.RoomID, &m.ThreadRoomID, &m.CreatedAt, &m.MessageID, &m.ThreadParentID,
		&m.Sender, &m.TargetUser, &m.Msg,
		&m.Mentions, &m.Attachments, &m.File,
		&m.Card, &m.CardAction,
		&m.QuotedParentMessage,
		&m.VisibleTo, &m.Unread, &m.Reactions,
		&m.Deleted, &m.Type, &m.SysMsgData,
		&m.SiteID, &m.EditedAt, &m.UpdatedAt,
	}
}

func scanThreadMessages(iter *gocql.Iter) []models.Message {
	messages := make([]models.Message, 0)
	for {
		var m models.Message
		if !iter.Scan(threadMessageScanDest(&m)...) {
			break
		}
		messages = append(messages, m)
	}
	return messages
}
```

Then, append the new method at the end of the file (after `GetMessageByID`):

```go
// GetThreadMessages returns a paginated set of thread replies for the given
// (roomID, threadRoomID) partition-slice, newest-first.
//
// Partition key equality on room_id + first clustering key equality on
// thread_room_id yields a single-slice seek with no ALLOW FILTERING.
// Clustering ORDER BY (thread_room_id DESC, created_at DESC, message_id DESC)
// means ORDER BY created_at DESC is the natural order within the slice.
func (r *Repository) GetThreadMessages(ctx context.Context, roomID, threadRoomID string, q PageRequest) (Page[models.Message], error) {
	var messages []models.Message

	nextCursor, err := NewQueryBuilder(
		r.session.Query(
			threadMessageQuery+` WHERE room_id = ? AND thread_room_id = ? ORDER BY created_at DESC`,
			roomID, threadRoomID,
		).WithContext(ctx),
	).
		WithCursor(q.Cursor).
		WithPageSize(q.PageSize).
		Fetch(func(iter *gocql.Iter) {
			messages = scanThreadMessages(iter)
		})
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("querying thread messages: %w", err)
	}

	return Page[models.Message]{
		Data:       messages,
		NextCursor: nextCursor,
		HasNext:    nextCursor != "",
	}, nil
}
```

- [ ] **Step 4: Run the integration tests — confirm they pass**

```bash
make test-integration SERVICE=history-service
```

Expected: all pre-existing integration tests plus the five new `TestRepository_GetThreadMessages_*` tests pass.

- [ ] **Step 5: Lint + commit**

```bash
make lint
git add history-service/internal/cassrepo/repository.go history-service/internal/cassrepo/integration_test.go
git commit -m "feat(history-service): add GetThreadMessages cassandra repo method"
```

---

## Task 4 — Extend `MessageRepository` interface and regenerate mocks

**Files:**
- Modify: `history-service/internal/service/service.go`
- Regenerate: `history-service/internal/service/mocks/mock_repository.go`

This task only adds the method signature to the consumer-side interface and regenerates the mock. It does **not** register the handler — that's Task 6, after the handler implementation has unit tests passing.

- [ ] **Step 1: Add the method to the `MessageRepository` interface**

Open `history-service/internal/service/service.go`. In the `MessageRepository` interface (currently lines 16–22), add a new line after `GetMessageByID`:

```go
type MessageRepository interface {
	GetMessagesBefore(ctx context.Context, roomID string, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesAfter(ctx context.Context, roomID string, after time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetAllMessagesAsc(ctx context.Context, roomID string, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessageByID(ctx context.Context, messageID string) (*models.Message, error)
	GetThreadMessages(ctx context.Context, roomID, threadRoomID string, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
}
```

- [ ] **Step 2: Regenerate mocks**

```bash
make generate SERVICE=history-service
```

Expected: `history-service/internal/service/mocks/mock_repository.go` is updated with a new `GetThreadMessages` method on `MockMessageRepository` and its recorder.

- [ ] **Step 3: Verify the package builds**

```bash
go build ./history-service/...
```

Expected: no output, exit 0. The real `cassrepo.Repository` (from Task 3) already satisfies the extended interface.

- [ ] **Step 4: Verify existing unit tests still pass**

```bash
make test SERVICE=history-service
```

Expected: all pre-existing `service` unit tests still green. Nothing new exercises the interface yet.

- [ ] **Step 5: Lint + commit**

```bash
make lint
git add history-service/internal/service/service.go history-service/internal/service/mocks/mock_repository.go
git commit -m "feat(history-service): extend MessageRepository with GetThreadMessages"
```

---
