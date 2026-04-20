# Parse Message Mentions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Parse `@mention` tokens from incoming message content in `message-worker`, resolve each mentioned account to a full `Participant` via MongoDB, and persist them to Cassandra in the existing `mentions SET<FROZEN<"Participant">>` column.

**Architecture:** `parseMentions` extracts raw account strings from content using a regex. `resolveMentions` (a Handler method) batch-looks up non-`all` accounts via `FindUsersByAccounts`, builds `[]model.Participant`, appends a special `{Account:"all"}` entry if `@all` was present, and skips any account not found in MongoDB. The result is stored on `evt.Message.Mentions` before saving. The Cassandra store converts `[]model.Participant` → `[]*cassParticipant` via a `toMentionSet` helper for the `SET<FROZEN<"Participant">>` column.

**Tech Stack:** Go 1.25, `regexp` (stdlib), `github.com/gocql/gocql`, `go.uber.org/mock` + `testify`, `testcontainers-go/modules/cassandra`.

---

## File Map

| File | Change |
|------|--------|
| `pkg/model/message.go` | Add `Mentions []Participant` to `Message` |
| `pkg/model/model_test.go` | Fix `TestMessageJSON`, `TestMessageEventJSON`, `TestNotificationEventJSON` — break when `Message` gets a slice field (no longer satisfies `comparable`) |
| `message-worker/store.go` | Add `FindUsersByAccounts` to `UserStore` interface |
| `message-worker/mock_store_test.go` | Regenerated — never edit manually |
| `message-worker/handler.go` | Add `mentionRe`, `parseMentions`, `resolveMentions`; call `resolveMentions` in `processMessage` |
| `message-worker/handler_test.go` | Add `TestParseMentions`; add mention cases to `TestHandler_ProcessMessage` |
| `message-worker/store_cassandra.go` | Add `UnmarshalUDT` to `cassParticipant`; add `toMentionSet` helper; pass `toMentionSet(msg.Mentions)` in all three INSERTs |
| `message-worker/integration_test.go` | Add `mentions SET<FROZEN<"Participant">>` to DDL; assert mentions are persisted |

---

## Task 1 — Add `Mentions []Participant` to `Message` + fix model tests

**Files:**
- Modify: `pkg/model/message.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing model tests for Mentions**

Add two subtests inside `TestMessageJSON` in `pkg/model/model_test.go` (after the existing subtests):

```go
t.Run("mentions round-trip", func(t *testing.T) {
    m := model.Message{
        ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
        Content:   "hello @bob",
        CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
        Mentions: []model.Participant{{
            UserID:      "u-bob",
            Account:     "bob",
            ChineseName: "鮑勃",
            EngName:     "Bob Chen",
        }},
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

- [ ] **Step 2: Run to confirm it fails**

```bash
make test SERVICE=model
```

Expected: compilation error — `Message` has no field `Mentions`.

- [ ] **Step 3: Add `Mentions` to `Message`**

Replace the full struct in `pkg/model/message.go`:

```go
type Message struct {
	ID                           string        `json:"id"                                     bson:"_id"`
	RoomID                       string        `json:"roomId"                                 bson:"roomId"`
	UserID                       string        `json:"userId"                                 bson:"userId"`
	UserAccount                  string        `json:"userAccount"                            bson:"userAccount"`
	Content                      string        `json:"content"                                bson:"content"`
	Mentions                     []Participant `json:"mentions,omitempty"                     bson:"mentions,omitempty"`
	CreatedAt                    time.Time     `json:"createdAt"                              bson:"createdAt"`
	ThreadParentMessageID        string        `json:"threadParentMessageId,omitempty"        bson:"threadParentMessageId,omitempty"`
	ThreadParentMessageCreatedAt *time.Time    `json:"threadParentMessageCreatedAt,omitempty" bson:"threadParentMessageCreatedAt,omitempty"`
}
```

- [ ] **Step 4: Fix compilation failures in `model_test.go`**

`[]Participant` makes `Message` non-comparable, breaking `roundTrip[T comparable]` for three tests.

**`TestMessageJSON` — subtest `"with threadParentMessageId"`:**

Before:
```go
roundTrip(t, &m, &model.Message{})
```

After:
```go
data, err := json.Marshal(&m)
require.NoError(t, err)
var dst model.Message
require.NoError(t, json.Unmarshal(data, &dst))
assert.Equal(t, m, dst)
```

**`TestMessageEventJSON`:**

Before:
```go
roundTrip(t, &e, &model.MessageEvent{})
```

After:
```go
data, err := json.Marshal(&e)
require.NoError(t, err)
var dst model.MessageEvent
require.NoError(t, json.Unmarshal(data, &dst))
assert.Equal(t, e, dst)
```

**`TestNotificationEventJSON`:**

Before:
```go
roundTrip(t, &src, &model.NotificationEvent{})
```

After:
```go
data, err := json.Marshal(&src)
require.NoError(t, err)
var dst model.NotificationEvent
require.NoError(t, json.Unmarshal(data, &dst))
assert.Equal(t, src, dst)
```

- [ ] **Step 5: Run model tests**

```bash
make test SERVICE=model
```

Expected: all model tests pass including the two new `mentions` subtests.

- [ ] **Step 6: Commit**

```bash
git add pkg/model/message.go pkg/model/model_test.go
git commit -m "feat(model): add Mentions []Participant to Message"
```

---

## Task 2 — Add `parseMentions` with TDD

**Files:**
- Modify: `message-worker/handler.go`
- Modify: `message-worker/handler_test.go`

- [ ] **Step 1: Write failing tests for `parseMentions`**

Add this test function to `message-worker/handler_test.go`:

```go
func TestParseMentions(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{name: "no mentions", content: "hello world", want: nil},
		{name: "single mention", content: "hello @bob", want: []string{"bob"}},
		{name: "multiple mentions", content: "@alice check with @bob", want: []string{"alice", "bob"}},
		{name: "mention at start", content: "@alice hello", want: []string{"alice"}},
		{name: "at-all", content: "hey @all check this", want: []string{"all"}},
		{name: "email-style mention", content: "ping @user@domain.com", want: []string{"user@domain.com"}},
		{name: "quoted reply prefix", content: ">@alice this is quoted", want: []string{"alice"}},
		{name: "duplicates deduplicated", content: "@bob and @bob again", want: []string{"bob"}},
		{name: "dots and hyphens", content: "cc @first.last and @my-user", want: []string{"first.last", "my-user"}},
		{name: "empty content", content: "", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseMentions(tt.content))
		})
	}
}
```

- [ ] **Step 2: Run to confirm it fails**

```bash
make test SERVICE=message-worker
```

Expected: compilation error — `parseMentions` undefined.

- [ ] **Step 3: Implement `parseMentions` in `handler.go`**

Add `regexp` to imports and append below the existing imports block:

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

// parseMentions returns the unique mention targets found in content (without the @ prefix).
// Returns nil when content has no mentions.
func parseMentions(content string) []string {
	matches := mentionRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	var out []string
	for _, m := range matches {
		account := m[2]
		if _, exists := seen[account]; !exists {
			seen[account] = struct{}{}
			out = append(out, account)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests**

```bash
make test SERVICE=message-worker
```

Expected: all `TestParseMentions` subtests pass; existing `TestHandler_ProcessMessage` unaffected.

- [ ] **Step 5: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go
git commit -m "feat(message-worker): add parseMentions"
```

---

## Task 3 — Add `FindUsersByAccounts` to `UserStore` interface and regenerate mock

**Files:**
- Modify: `message-worker/store.go`
- Regenerate: `message-worker/mock_store_test.go`

- [ ] **Step 1: Add method to `UserStore`**

In `message-worker/store.go`, update the interface:

```go
//go:generate mockgen -destination=mock_store_test.go -package=main . Store,UserStore

// Store defines Cassandra persistence operations for the message worker.
type Store interface {
	SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error
	SaveThreadMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error
}

// UserStore defines MongoDB user lookup operations for the message worker.
type UserStore interface {
	FindUserByID(ctx context.Context, id string) (*model.User, error)
	FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error)
}
```

- [ ] **Step 2: Regenerate mock**

```bash
make generate SERVICE=message-worker
```

Expected: `message-worker/mock_store_test.go` is updated with `MockUserStore` now including `FindUsersByAccounts`.

- [ ] **Step 3: Verify compilation**

```bash
make test SERVICE=message-worker
```

Expected: all existing tests still pass (existing tests only call `FindUserByID` on `MockUserStore`, and the new method is simply not called — gomock strict mode only fails if an *unexpected* call is made, not if an expected call is absent without `Times`).

- [ ] **Step 4: Commit**

```bash
git add message-worker/store.go message-worker/mock_store_test.go
git commit -m "feat(message-worker): add FindUsersByAccounts to UserStore interface"
```

---

## Task 4 — Add `resolveMentions`, wire into `processMessage`, update handler tests

**Files:**
- Modify: `message-worker/handler.go`
- Modify: `message-worker/handler_test.go`

- [ ] **Step 1: Add test fixtures and new test cases**

Add these variables near the top of `TestHandler_ProcessMessage` (after the `threadData` block):

```go
bobUser := &model.User{
    ID:          "u-bob",
    Account:     "bob",
    SiteID:      "site-a",
    EngName:     "Bob Chen",
    ChineseName: "鮑勃",
}

// Event with a real user mention — Mentions field is absent in the inbound event
// and will be populated by resolveMentions.
evtWithMention := model.MessageEvent{
    Message: model.Message{
        ID: "msg-3", RoomID: "r1", UserID: "u-1", UserAccount: "alice",
        Content:   "hey @bob can you check this?",
        CreatedAt: now,
    },
    SiteID: "site-a", Timestamp: now.UnixMilli(),
}
dataWithMention, _ := json.Marshal(evtWithMention)

// Expected stored message: Mentions resolved to full Participant.
msgWithMention := model.Message{
    ID: "msg-3", RoomID: "r1", UserID: "u-1", UserAccount: "alice",
    Content:   "hey @bob can you check this?",
    CreatedAt: now,
    Mentions: []model.Participant{{
        UserID: "u-bob", Account: "bob", ChineseName: "鮑勃", EngName: "Bob Chen",
    }},
}

// Event with @all — no user lookup should occur.
evtWithAll := model.MessageEvent{
    Message: model.Message{
        ID: "msg-4", RoomID: "r1", UserID: "u-1", UserAccount: "alice",
        Content:   "hello @all please read",
        CreatedAt: now,
    },
    SiteID: "site-a", Timestamp: now.UnixMilli(),
}
dataWithAll, _ := json.Marshal(evtWithAll)

msgWithAll := model.Message{
    ID: "msg-4", RoomID: "r1", UserID: "u-1", UserAccount: "alice",
    Content:   "hello @all please read",
    CreatedAt: now,
    Mentions:  []model.Participant{{Account: "all", EngName: "all"}},
}
```

Add these cases to the `tests` slice:

```go
{
    name: "mention resolved to Participant and stored",
    data: dataWithMention,
    setupMocks: func(store *MockStore, us *MockUserStore) {
        us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob"}).
            Return([]model.User{*bobUser}, nil)
        us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
        store.EXPECT().SaveMessage(gomock.Any(), &msgWithMention, &expectedSender, "site-a").Return(nil)
    },
},
{
    name: "@all stored as special Participant without DB lookup",
    data: dataWithAll,
    setupMocks: func(store *MockStore, us *MockUserStore) {
        us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
        store.EXPECT().SaveMessage(gomock.Any(), &msgWithAll, &expectedSender, "site-a").Return(nil)
    },
},
{
    name: "mention user lookup error — NAK before sender lookup",
    data: dataWithMention,
    setupMocks: func(store *MockStore, us *MockUserStore) {
        us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob"}).
            Return(nil, errors.New("mongo: connection refused"))
        // FindUserByID and SaveMessage must NOT be called
    },
    wantErr: true,
},
```

- [ ] **Step 2: Run to confirm new cases fail**

```bash
make test SERVICE=message-worker
```

Expected: the three new cases fail — `resolveMentions` does not exist yet.

- [ ] **Step 3: Implement `resolveMentions` and update `processMessage`**

Add `resolveMentions` to `message-worker/handler.go`:

```go
// resolveMentions parses @mention tokens from content, looks up real users in
// MongoDB, and returns them as Participants. @all is always included as a
// special entry without a DB lookup. Accounts not found in MongoDB are skipped.
// Returns nil when content has no mentions.
func (h *Handler) resolveMentions(ctx context.Context, content string) ([]model.Participant, error) {
	parsed := parseMentions(content)
	if len(parsed) == 0 {
		return nil, nil
	}

	var mentionAll bool
	var userAccounts []string
	for _, account := range parsed {
		if account == "all" {
			mentionAll = true
		} else {
			userAccounts = append(userAccounts, account)
		}
	}

	var participants []model.Participant

	if len(userAccounts) > 0 {
		users, err := h.userStore.FindUsersByAccounts(ctx, userAccounts)
		if err != nil {
			return nil, fmt.Errorf("find mentioned users: %w", err)
		}
		for _, u := range users {
			participants = append(participants, model.Participant{
				UserID:      u.ID,
				Account:     u.Account,
				ChineseName: u.ChineseName,
				EngName:     u.EngName,
			})
		}
	}

	if mentionAll {
		participants = append(participants, model.Participant{
			Account: "all",
			EngName: "all",
		})
	}

	if len(participants) == 0 {
		return nil, nil
	}
	return participants, nil
}
```

Update `processMessage` to call `resolveMentions` before the sender lookup:

```go
func (h *Handler) processMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	mentions, err := h.resolveMentions(ctx, evt.Message.Content)
	if err != nil {
		return fmt.Errorf("resolve mentions: %w", err)
	}
	evt.Message.Mentions = mentions

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

- [ ] **Step 4: Run all message-worker tests**

```bash
make test SERVICE=message-worker
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go
git commit -m "feat(message-worker): resolve mentions to Participants before save"
```

---

## Task 5 — Update Cassandra store for `SET<FROZEN<"Participant">>`

**Files:**
- Modify: `message-worker/store_cassandra.go`
- Modify: `message-worker/integration_test.go`

The production schema (`docs/cassandra_message_model.md`) already has `mentions SET<FROZEN<"Participant">>` on all three tables. No `ALTER TABLE` is needed in production. The integration test DDL must be updated to include the column.

- [ ] **Step 1: Add `UnmarshalUDT` and `toMentionSet` to `store_cassandra.go`**

Append to `message-worker/store_cassandra.go` after `MarshalUDT`:

```go
// UnmarshalUDT implements gocql.UDTUnmarshaler for cassParticipant.
// Required to scan SET<FROZEN<"Participant">> columns back from Cassandra in tests.
func (p *cassParticipant) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	switch name {
	case "id":
		return gocql.Unmarshal(info, data, &p.ID)
	case "eng_name":
		return gocql.Unmarshal(info, data, &p.EngName)
	case "company_name":
		return gocql.Unmarshal(info, data, &p.CompanyName)
	case "account":
		return gocql.Unmarshal(info, data, &p.Account)
	case "app_id":
		return gocql.Unmarshal(info, data, &p.AppID)
	case "app_name":
		return gocql.Unmarshal(info, data, &p.AppName)
	case "is_bot":
		return gocql.Unmarshal(info, data, &p.IsBot)
	default:
		return nil
	}
}

// toMentionSet converts []model.Participant to []*cassParticipant for binding
// to a Cassandra SET<FROZEN<"Participant">> column.
func toMentionSet(mentions []model.Participant) []*cassParticipant {
	if len(mentions) == 0 {
		return nil
	}
	result := make([]*cassParticipant, len(mentions))
	for i, m := range mentions {
		result[i] = &cassParticipant{
			ID:          m.UserID,
			EngName:     m.EngName,
			CompanyName: m.ChineseName,
			Account:     m.Account,
		}
	}
	return result
}
```

- [ ] **Step 2: Update `SaveMessage` INSERTs**

Replace both queries in `SaveMessage`:

```go
func (s *CassandraStore) SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error {
	if err := s.cassSession.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, sender, msg.Content, siteID, msg.CreatedAt, toMentionSet(msg.Mentions),
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_room %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO messages_by_id (message_id, created_at, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, sender, msg.Content, siteID, msg.CreatedAt, toMentionSet(msg.Mentions),
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_id %s: %w", msg.ID, err)
	}

	return nil
}
```

- [ ] **Step 3: Update `SaveThreadMessage` INSERTs**

Replace both queries in `SaveThreadMessage`:

```go
func (s *CassandraStore) SaveThreadMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error {
	if err := s.cassSession.Query(
		`INSERT INTO messages_by_id (message_id, created_at, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, sender, msg.Content, siteID, msg.CreatedAt, toMentionSet(msg.Mentions),
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_id %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, thread_message_id, sender, msg, site_id, updated_at, mentions)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.ThreadParentMessageID, msg.CreatedAt, msg.ID, msg.ID, sender, msg.Content, siteID, msg.CreatedAt, toMentionSet(msg.Mentions),
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert thread_messages_by_room %s: %w", msg.ID, err)
	}

	return nil
}
```

- [ ] **Step 4: Update integration test DDL and add mention assertions**

In `message-worker/integration_test.go`, update `setupCassandra` to add `mentions SET<FROZEN<"Participant">>` to both table DDL statements:

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
        mentions      SET<FROZEN<"Participant">>,
        PRIMARY KEY ((room_id), created_at, message_id)
    ) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`,
    `CREATE TABLE IF NOT EXISTS chat_test.messages_by_id (
        message_id TEXT,
        created_at TIMESTAMP,
        sender     FROZEN<"Participant">,
        msg        TEXT,
        site_id    TEXT,
        updated_at TIMESTAMP,
        mentions   SET<FROZEN<"Participant">>,
        PRIMARY KEY (message_id, created_at)
    ) WITH CLUSTERING ORDER BY (created_at DESC)`,
}
```

Replace `TestCassandraStore_SaveMessage` with a version that includes mention fixtures and assertions:

```go
func TestCassandraStore_SaveMessage(t *testing.T) {
	cassSession := setupCassandra(t)
	store := NewCassandraStore(cassSession)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	sender := &cassParticipant{
		ID:          "u-1",
		EngName:     "Alice Wang",
		CompanyName: "愛麗絲",
		Account:     "alice",
	}
	msg := &model.Message{
		ID:          "m-1",
		RoomID:      "r-1",
		UserID:      "u-1",
		UserAccount: "alice",
		Content:     "hello @bob",
		CreatedAt:   now,
		Mentions: []model.Participant{{
			UserID:      "u-bob",
			Account:     "bob",
			ChineseName: "鮑勃",
			EngName:     "Bob Chen",
		}},
	}

	err := store.SaveMessage(ctx, msg, sender, "site-a")
	require.NoError(t, err)

	t.Run("messages_by_room row correct", func(t *testing.T) {
		var gotMsg, gotSiteID string
		var gotUpdatedAt time.Time
		err := cassSession.Query(
			`SELECT msg, site_id, updated_at FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			"r-1", now, "m-1",
		).Scan(&gotMsg, &gotSiteID, &gotUpdatedAt)
		require.NoError(t, err)
		assert.Equal(t, "hello @bob", gotMsg)
		assert.Equal(t, "site-a", gotSiteID)
		assert.Equal(t, now, gotUpdatedAt.UTC().Truncate(time.Millisecond))
	})

	t.Run("messages_by_room mentions persisted", func(t *testing.T) {
		var gotMentions []*cassParticipant
		err := cassSession.Query(
			`SELECT mentions FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			"r-1", now, "m-1",
		).Scan(&gotMentions)
		require.NoError(t, err)
		require.Len(t, gotMentions, 1)
		assert.Equal(t, "bob", gotMentions[0].Account)
		assert.Equal(t, "Bob Chen", gotMentions[0].EngName)
		assert.Equal(t, "u-bob", gotMentions[0].ID)
	})

	t.Run("messages_by_id row correct", func(t *testing.T) {
		var gotMsg, gotSiteID string
		var gotUpdatedAt time.Time
		err := cassSession.Query(
			`SELECT msg, site_id, updated_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-1", now,
		).Scan(&gotMsg, &gotSiteID, &gotUpdatedAt)
		require.NoError(t, err)
		assert.Equal(t, "hello @bob", gotMsg)
		assert.Equal(t, "site-a", gotSiteID)
		assert.Equal(t, now, gotUpdatedAt.UTC().Truncate(time.Millisecond))
	})

	t.Run("messages_by_id mentions persisted", func(t *testing.T) {
		var gotMentions []*cassParticipant
		err := cassSession.Query(
			`SELECT mentions FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-1", now,
		).Scan(&gotMentions)
		require.NoError(t, err)
		require.Len(t, gotMentions, 1)
		assert.Equal(t, "bob", gotMentions[0].Account)
		assert.Equal(t, "Bob Chen", gotMentions[0].EngName)
	})
}
```

- [ ] **Step 5: Run unit tests**

```bash
make test SERVICE=message-worker
```

Expected: all unit tests pass.

- [ ] **Step 6: Run integration tests**

```bash
make test-integration SERVICE=message-worker
```

Expected: all integration tests pass including new `mentions persisted` subtests.

- [ ] **Step 7: Commit**

```bash
git add message-worker/store_cassandra.go message-worker/integration_test.go
git commit -m "feat(message-worker): persist mentions as SET<FROZEN<Participant>> in Cassandra"
```

---

## Self-Review

### Spec coverage

| Requirement | Task |
|-------------|------|
| Parse `@someone` + `@all` from content | Task 2 (`parseMentions`) |
| Look up mentioned accounts via MongoDB `FindUsersByAccounts` | Task 3 (interface), Task 4 (`resolveMentions`) |
| Store as Cassandra `Participant` UDT with eng name etc. | Task 5 (`toMentionSet`, store INSERTs) |
| `@all` stored as special Participant (no DB lookup) | Task 4 (`resolveMentions`) |
| Non-`@all` unresolvable mentions skipped | Task 4 (`resolveMentions` — only adds found users) |
| `mentions` field on `Message` model | Task 1 |
| TDD red → green on every task | Every task has explicit fail/pass steps |

### No production DDL needed

The production Cassandra schema already has `mentions SET<FROZEN<"Participant">>` on `messages_by_room`, `messages_by_id`, and `thread_messages_by_room` per `docs/cassandra_message_model.md`. Only the integration test DDL needed updating.
