# Parse Message Mentions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Parse `@mention` tokens (including `@all`) from incoming message content in `message-worker` and persist them to Cassandra in a `mentions` column on every message save.

**Architecture:** The regex `(^|\s|>?)@([0-9a-zA-Z-_.]+(@[0-9a-zA-Z-_.]+)?)` is applied to `Message.Content` inside `processMessage`, before calling the store. The extracted mention strings (without the leading `@`) are stored in a new `Mentions []string` field on `model.Message` and written to Cassandra as `list<text>`. No new store interface methods are required — the existing `SaveMessage`/`SaveThreadMessage` signatures already pass the full `*model.Message`.

**Tech Stack:** Go 1.25, `regexp` (stdlib), `github.com/gocql/gocql` (Cassandra `list<text>`), `go.uber.org/mock` + `testify` (unit tests), `testcontainers-go/modules/cassandra` (integration test).

---

## File Map

| File | Change |
|------|--------|
| `pkg/model/message.go` | Add `Mentions []string` to `Message` struct |
| `pkg/model/model_test.go` | Fix `TestMessageJSON`, `TestMessageEventJSON`, `TestNotificationEventJSON`, `TestClientMessageJSON` — they call `roundTrip[T comparable]` which breaks once `Message` has a slice field |
| `message-worker/handler.go` | Add `parseMentions(content string) []string`; call it in `processMessage` to populate `evt.Message.Mentions` before saving |
| `message-worker/handler_test.go` | Add table-driven subtests for mention parsing; update existing cases to prove non-mention content is unaffected |
| `message-worker/store_cassandra.go` | Add `mentions` column to all three Cassandra INSERTs |
| `message-worker/integration_test.go` | Add `mentions list<text>` column to `setupCassandra` DDL; assert `mentions` is persisted by `SaveMessage` |

---

## Task 1 — Add `Mentions` field to `Message` model

**Files:**
- Modify: `pkg/model/message.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write a failing test for `Mentions` round-trip**

Add a new subtest inside `TestMessageJSON` in `pkg/model/model_test.go`:

```go
t.Run("mentions round-trip", func(t *testing.T) {
    m := model.Message{
        ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
        Content:   "hello @bob",
        CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
        Mentions:  []string{"bob"},
    }
    data, err := json.Marshal(&m)
    require.NoError(t, err)
    var dst model.Message
    require.NoError(t, json.Unmarshal(data, &dst))
    assert.Equal(t, m, dst)
})

t.Run("mentions omitted when nil", func(t *testing.T) {
    m := model.Message{
        ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
        Content:   "hello",
        CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
    }
    data, err := json.Marshal(&m)
    require.NoError(t, err)
    var raw map[string]any
    require.NoError(t, json.Unmarshal(data, &raw))
    _, present := raw["mentions"]
    assert.False(t, present, "mentions should be omitted when nil")
})
```

- [ ] **Step 2: Run the test to confirm it fails**

```bash
make test SERVICE=model
```

Expected: compilation error — `Message` has no field `Mentions`.

- [ ] **Step 3: Add `Mentions` field to `Message`**

In `pkg/model/message.go`, update the struct:

```go
type Message struct {
	ID                           string     `json:"id"                                      bson:"_id"`
	RoomID                       string     `json:"roomId"                                  bson:"roomId"`
	UserID                       string     `json:"userId"                                  bson:"userId"`
	UserAccount                  string     `json:"userAccount"                             bson:"userAccount"`
	Content                      string     `json:"content"                                 bson:"content"`
	Mentions                     []string   `json:"mentions,omitempty"                      bson:"mentions,omitempty"`
	CreatedAt                    time.Time  `json:"createdAt"                               bson:"createdAt"`
	ThreadParentMessageID        string     `json:"threadParentMessageId,omitempty"         bson:"threadParentMessageId,omitempty"`
	ThreadParentMessageCreatedAt *time.Time `json:"threadParentMessageCreatedAt,omitempty"  bson:"threadParentMessageCreatedAt,omitempty"`
}
```

- [ ] **Step 4: Fix compilation failures in `model_test.go`**

Adding `Mentions []string` makes `model.Message` non-comparable (slices can't be compared with `==`). The generic `roundTrip[T comparable]` helper will no longer accept `*model.Message`. Four tests are affected: `TestMessageJSON`, `TestMessageEventJSON`, `TestNotificationEventJSON`, and `TestClientMessageJSON`.

Replace the two `roundTrip` calls in `TestMessageJSON` that are affected.

**Before** (in `TestMessageJSON`, subtest `"with threadParentMessageId"`):
```go
roundTrip(t, &m, &model.Message{})
```

**After** — replace with manual marshal/unmarshal + assert.Equal:
```go
data, err := json.Marshal(&m)
require.NoError(t, err)
var dst model.Message
require.NoError(t, json.Unmarshal(data, &dst))
assert.Equal(t, m, dst)
```

Apply the same replacement to `TestMessageEventJSON`:

**Before:**
```go
roundTrip(t, &e, &model.MessageEvent{})
```

**After:**
```go
data, err := json.Marshal(&e)
require.NoError(t, err)
var dst model.MessageEvent
require.NoError(t, json.Unmarshal(data, &dst))
assert.Equal(t, e, dst)
```

Apply to `TestNotificationEventJSON`:

**Before:**
```go
roundTrip(t, &src, &model.NotificationEvent{})
```

**After:**
```go
data, err := json.Marshal(&src)
require.NoError(t, err)
var dst model.NotificationEvent
require.NoError(t, json.Unmarshal(data, &dst))
assert.Equal(t, src, dst)
```

`TestClientMessageJSON` already uses `assert.Equal` — no change needed there.

- [ ] **Step 5: Run all model tests to confirm they pass**

```bash
make test SERVICE=model
```

Expected: all `TestMessageJSON`, `TestMessageEventJSON`, `TestNotificationEventJSON`, `TestRoomEventJSON`, and new `mentions` subtests pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/model/message.go pkg/model/model_test.go
git commit -m "feat(model): add Mentions field to Message struct"
```

---

## Task 2 — Add `parseMentions` with TDD

**Files:**
- Modify: `message-worker/handler.go`
- Modify: `message-worker/handler_test.go`

- [ ] **Step 1: Write failing unit tests for `parseMentions`**

Add a new top-level test function in `message-worker/handler_test.go`:

```go
func TestParseMentions(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "no mentions",
			content: "hello world",
			want:    nil,
		},
		{
			name:    "single mention",
			content: "hello @bob",
			want:    []string{"bob"},
		},
		{
			name:    "multiple mentions",
			content: "@alice please check with @bob",
			want:    []string{"alice", "bob"},
		},
		{
			name:    "mention at start of string",
			content: "@alice hello",
			want:    []string{"alice"},
		},
		{
			name:    "at-all mention",
			content: "hey @all check this out",
			want:    []string{"all"},
		},
		{
			name:    "at-here mention",
			content: "hey @here look at this",
			want:    []string{"here"},
		},
		{
			name:    "email-style mention",
			content: "ping @user@domain.com please",
			want:    []string{"user@domain.com"},
		},
		{
			name:    "quoted reply mention with > prefix",
			content: ">@alice this is a quote",
			want:    []string{"alice"},
		},
		{
			name:    "duplicate mentions deduplicated",
			content: "@bob can you ask @bob again",
			want:    []string{"bob"},
		},
		{
			name:    "mentions with underscores and dots",
			content: "cc @first.last and @some_user-name",
			want:    []string{"first.last", "some_user-name"},
		},
		{
			name:    "empty content",
			content: "",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMentions(tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}
```

- [ ] **Step 2: Run to confirm tests fail**

```bash
make test SERVICE=message-worker
```

Expected: compilation error — `parseMentions` undefined.

- [ ] **Step 3: Implement `parseMentions` in `handler.go`**

Add the following to `message-worker/handler.go` (at package level, after imports):

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
)

var mentionRe = regexp.MustCompile(`(^|\s|>?)@([0-9a-zA-Z\-_.]+(@[0-9a-zA-Z\-_.]+)?)`)

// parseMentions extracts @mention targets from content.
// Returns nil when there are no mentions.
// Duplicate accounts are removed; the @ prefix is stripped from each result.
func parseMentions(content string) []string {
	matches := mentionRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	var mentions []string
	for _, m := range matches {
		account := m[2] // group 2: the mention target, e.g. "bob" or "user@domain.com"
		if _, exists := seen[account]; !exists {
			seen[account] = struct{}{}
			mentions = append(mentions, account)
		}
	}
	return mentions
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
make test SERVICE=message-worker
```

Expected: all `TestParseMentions` subtests pass. Existing `TestHandler_ProcessMessage` tests also pass unaffected.

- [ ] **Step 5: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go
git commit -m "feat(message-worker): add parseMentions function"
```

---

## Task 3 — Wire `parseMentions` into `processMessage`

**Files:**
- Modify: `message-worker/handler.go`
- Modify: `message-worker/handler_test.go`

- [ ] **Step 1: Add handler test cases that assert mentions are stored**

Add these two cases to the `tests` slice in `TestHandler_ProcessMessage` in `message-worker/handler_test.go`.

First, add a helper message with a single mention. Add these lines near the top of `TestHandler_ProcessMessage`, after the existing `threadEvt`/`threadData` block:

```go
msgWithMention := model.Message{
    ID:          "msg-3",
    RoomID:      "r1",
    UserID:      "u-1",
    UserAccount: "alice",
    Content:     "hey @bob can you check this?",
    CreatedAt:   now,
    Mentions:    []string{"bob"},
}
// The event carries the raw message without Mentions populated —
// the handler fills Mentions after parsing content.
evtWithMention := model.MessageEvent{
    Message: model.Message{
        ID:          "msg-3",
        RoomID:      "r1",
        UserID:      "u-1",
        UserAccount: "alice",
        Content:     "hey @bob can you check this?",
        CreatedAt:   now,
    },
    SiteID:    "site-a",
    Timestamp: now.UnixMilli(),
}
dataWithMention, _ := json.Marshal(evtWithMention)

msgWithMentionAll := model.Message{
    ID:          "msg-4",
    RoomID:      "r1",
    UserID:      "u-1",
    UserAccount: "alice",
    Content:     "hello @all please read this",
    CreatedAt:   now,
    Mentions:    []string{"all"},
}
evtWithMentionAll := model.MessageEvent{
    Message: model.Message{
        ID:          "msg-4",
        RoomID:      "r1",
        UserID:      "u-1",
        UserAccount: "alice",
        Content:     "hello @all please read this",
        CreatedAt:   now,
    },
    SiteID:    "site-a",
    Timestamp: now.UnixMilli(),
}
dataWithMentionAll, _ := json.Marshal(evtWithMentionAll)
```

Then add these cases to the `tests` slice:

```go
{
    name: "message with single mention — Mentions populated before save",
    data: dataWithMention,
    setupMocks: func(store *MockStore, us *MockUserStore) {
        us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
        store.EXPECT().SaveMessage(gomock.Any(), &msgWithMention, &expectedSender, "site-a").Return(nil)
    },
},
{
    name: "message with @all — Mentions includes 'all'",
    data: dataWithMentionAll,
    setupMocks: func(store *MockStore, us *MockUserStore) {
        us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
        store.EXPECT().SaveMessage(gomock.Any(), &msgWithMentionAll, &expectedSender, "site-a").Return(nil)
    },
},
```

- [ ] **Step 2: Run tests to confirm the new cases fail**

```bash
make test SERVICE=message-worker
```

Expected: the two new test cases fail with mock mismatch — the actual call passes `Mentions: nil` but the mock expects `Mentions: ["bob"]` (or `["all"]`).

- [ ] **Step 3: Wire `parseMentions` into `processMessage`**

Update `processMessage` in `message-worker/handler.go` to parse mentions before calling the store. Change:

```go
func (h *Handler) processMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	user, err := h.userStore.FindUserByID(ctx, evt.Message.UserID)
	if err != nil {
		return fmt.Errorf("lookup user %s: %w", evt.Message.UserID, err)
	}

	sender := cassParticipant{
		ID:          user.ID,
		EngName:     user.EngName,
		CompanyName: user.ChineseName,
		Account:     evt.Message.UserAccount,
	}

	if evt.Message.ThreadParentMessageID != "" {
		if err := h.store.SaveThreadMessage(ctx, &evt.Message, &sender, evt.SiteID); err != nil {
			return fmt.Errorf("save thread message: %w", err)
		}
	} else {
		if err := h.store.SaveMessage(ctx, &evt.Message, &sender, evt.SiteID); err != nil {
			return fmt.Errorf("save message: %w", err)
		}
	}

	return nil
}
```

To:

```go
func (h *Handler) processMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	evt.Message.Mentions = parseMentions(evt.Message.Content)

	user, err := h.userStore.FindUserByID(ctx, evt.Message.UserID)
	if err != nil {
		return fmt.Errorf("lookup user %s: %w", evt.Message.UserID, err)
	}

	sender := cassParticipant{
		ID:          user.ID,
		EngName:     user.EngName,
		CompanyName: user.ChineseName,
		Account:     evt.Message.UserAccount,
	}

	if evt.Message.ThreadParentMessageID != "" {
		if err := h.store.SaveThreadMessage(ctx, &evt.Message, &sender, evt.SiteID); err != nil {
			return fmt.Errorf("save thread message: %w", err)
		}
	} else {
		if err := h.store.SaveMessage(ctx, &evt.Message, &sender, evt.SiteID); err != nil {
			return fmt.Errorf("save message: %w", err)
		}
	}

	return nil
}
```

- [ ] **Step 4: Run all message-worker tests to confirm they pass**

```bash
make test SERVICE=message-worker
```

Expected: all tests pass, including the new mention cases.

- [ ] **Step 5: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go
git commit -m "feat(message-worker): parse mentions from message content before save"
```

---

## Task 4 — Persist mentions in Cassandra

**Files:**
- Modify: `message-worker/store_cassandra.go`
- Modify: `message-worker/integration_test.go`

- [ ] **Step 1: Write a failing integration test for mentions persistence**

Add a new subtest to `TestCassandraStore_SaveMessage` in `message-worker/integration_test.go`:

```go
func TestCassandraStore_SaveMessage(t *testing.T) {
	cassSession := setupCassandra(t)
	store := NewCassandraStore(cassSession)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	sender := &cassParticipant{
		ID:      "u-1",
		EngName: "Alice Wang",
		CompanyName: "愛麗絲",
		Account: "alice",
	}
	msg := &model.Message{
		ID:          "m-1",
		RoomID:      "r-1",
		UserID:      "u-1",
		UserAccount: "alice",
		Content:     "hello @bob and @charlie",
		CreatedAt:   now,
		Mentions:    []string{"bob", "charlie"},
	}

	err := store.SaveMessage(ctx, msg, sender, "site-a")
	require.NoError(t, err)

	t.Run("messages_by_room row exists with correct fields", func(t *testing.T) {
		var gotMsg, gotSiteID string
		var gotUpdatedAt time.Time
		err := cassSession.Query(
			`SELECT msg, site_id, updated_at FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			"r-1", now, "m-1",
		).Scan(&gotMsg, &gotSiteID, &gotUpdatedAt)
		require.NoError(t, err)
		assert.Equal(t, "hello @bob and @charlie", gotMsg)
		assert.Equal(t, "site-a", gotSiteID)
		assert.Equal(t, now, gotUpdatedAt.UTC().Truncate(time.Millisecond))
	})

	t.Run("messages_by_room row has correct mentions", func(t *testing.T) {
		var gotMentions []string
		err := cassSession.Query(
			`SELECT mentions FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			"r-1", now, "m-1",
		).Scan(&gotMentions)
		require.NoError(t, err)
		assert.Equal(t, []string{"bob", "charlie"}, gotMentions)
	})

	t.Run("messages_by_id row exists with correct fields", func(t *testing.T) {
		var gotMsg, gotSiteID string
		var gotUpdatedAt time.Time
		err := cassSession.Query(
			`SELECT msg, site_id, updated_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-1", now,
		).Scan(&gotMsg, &gotSiteID, &gotUpdatedAt)
		require.NoError(t, err)
		assert.Equal(t, "hello @bob and @charlie", gotMsg)
		assert.Equal(t, "site-a", gotSiteID)
		assert.Equal(t, now, gotUpdatedAt.UTC().Truncate(time.Millisecond))
	})

	t.Run("messages_by_id row has correct mentions", func(t *testing.T) {
		var gotMentions []string
		err := cassSession.Query(
			`SELECT mentions FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-1", now,
		).Scan(&gotMentions)
		require.NoError(t, err)
		assert.Equal(t, []string{"bob", "charlie"}, gotMentions)
	})
}
```

Also update `setupCassandra` to add the `mentions list<text>` column to both table DDL statements:

```go
stmts := []string{
    `CREATE KEYSPACE IF NOT EXISTS chat_test WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`,
    `CREATE TYPE IF NOT EXISTS chat_test."Participant" (id TEXT, eng_name TEXT, company_name TEXT, app_id TEXT, app_name TEXT, is_bot BOOLEAN, account TEXT)`,
    `CREATE TABLE IF NOT EXISTS chat_test.messages_by_room (
        room_id       TEXT,
        created_at    TIMESTAMP,
        message_id    TEXT,
        sender        FROZEN<"Participant">,
        msg           TEXT,
        site_id       TEXT,
        updated_at    TIMESTAMP,
        mentions      list<text>,
        PRIMARY KEY ((room_id), created_at, message_id)
    ) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`,
    `CREATE TABLE IF NOT EXISTS chat_test.messages_by_id (
        message_id TEXT,
        created_at TIMESTAMP,
        sender     FROZEN<"Participant">,
        msg        TEXT,
        site_id    TEXT,
        updated_at TIMESTAMP,
        mentions   list<text>,
        PRIMARY KEY (message_id, created_at)
    ) WITH CLUSTERING ORDER BY (created_at DESC)`,
}
```

- [ ] **Step 2: Update `SaveMessage` INSERT queries in `store_cassandra.go`**

Replace the two INSERT queries in `SaveMessage`:

```go
func (s *CassandraStore) SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error {
	if err := s.cassSession.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, sender, msg.Content, siteID, msg.CreatedAt, msg.Mentions,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_room %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO messages_by_id (message_id, created_at, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, sender, msg.Content, siteID, msg.CreatedAt, msg.Mentions,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_id %s: %w", msg.ID, err)
	}

	return nil
}
```

- [ ] **Step 3: Update `SaveThreadMessage` INSERT queries in `store_cassandra.go`**

Replace the two INSERT queries in `SaveThreadMessage`:

```go
func (s *CassandraStore) SaveThreadMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error {
	if err := s.cassSession.Query(
		`INSERT INTO messages_by_id (message_id, created_at, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, sender, msg.Content, siteID, msg.CreatedAt, msg.Mentions,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_id %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, thread_message_id, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.ThreadParentMessageID, msg.CreatedAt, msg.ID, msg.ID, sender, msg.Content, siteID, msg.CreatedAt, msg.Mentions,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert thread_messages_by_room %s: %w", msg.ID, err)
	}

	return nil
}
```

- [ ] **Step 4: Run unit tests to confirm nothing is broken**

```bash
make test SERVICE=message-worker
```

Expected: all unit tests pass (store methods have no unit tests, so this validates compilation and handler tests).

- [ ] **Step 5: Run integration tests to confirm Cassandra persistence**

```bash
make test-integration SERVICE=message-worker
```

Expected: `TestCassandraStore_SaveMessage` passes, including the new `mentions` subtests.

- [ ] **Step 6: Commit**

```bash
git add message-worker/store_cassandra.go message-worker/integration_test.go
git commit -m "feat(message-worker): persist mentions list to Cassandra"
```

---

## Self-Review

### Spec coverage

| Requirement | Task |
|-------------|------|
| Parse `@someone` mentions from content | Task 2 (`parseMentions`), Task 3 (wiring) |
| Parse `@all` mentions from content | Task 2 (test case), Task 3 (wiring) |
| Use provided regex | Task 2 (implementation) |
| Save to database in `mentions` field | Task 4 (Cassandra store + DDL) |
| TDD cycle (red → green) | Every task has explicit fail/pass steps |

### Schema migration note

The `mentions list<text>` column must also be added to the production Cassandra keyspace before deploying. Run this on the live keyspace:

```cql
ALTER TABLE messages_by_room ADD mentions list<text>;
ALTER TABLE messages_by_id ADD mentions list<text>;
ALTER TABLE thread_messages_by_room ADD mentions list<text>;
```

The `IF NOT EXISTS` in `CREATE TABLE` in the integration test DDL is replaced with `ALTER TABLE` for production — no data migration needed since the column is additive and Cassandra returns `null` for existing rows.
