# Add Model Fields and Remove `Message.Unread` — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `Room.MinUserLastSeenAt`, `Subscription.ThreadUnread`, and `Subscription.Alert`; remove the now-unused `Unread` field from the Cassandra `Message` model and its accompanying schema/docs/tests.

**Architecture:** Pure data-shape changes. Per CLAUDE.md, models live in `pkg/model/`; the Cassandra UDT/row structs live in `pkg/model/cassandra/`. The schema doc at `docs/cassandra_message_model.md` is the single source of truth and must stay in lockstep with both the Go structs and the local-dev init DDL under `docker-local/cassandra/init/`. TDD: failing test → minimal implementation → commit per task.

**Tech Stack:** Go 1.25, gocql, MongoDB driver v2, testify, `go test -race`, `golangci-lint`.

**Spec:** `docs/superpowers/specs/2026-04-30-add-model-fields-design.md`

---

### Task 1: Add `Room.MinUserLastSeenAt`

**Files:**
- Modify: `pkg/model/room.go`
- Test: `pkg/model/model_test.go`

- [ ] **Step 1.1: Write the failing tests**

Edit `pkg/model/model_test.go`:

In `TestRoomJSON` (currently around line 31), set `MinUserLastSeenAt` on the literal so the round-trip exercises the new field. Replace the existing struct literal with:

```go
func TestRoomJSON(t *testing.T) {
	lastMsg := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	lastMention := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	minSeen := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r := model.Room{
		ID: "r1", Name: "general", Type: model.RoomTypeChannel,
		CreatedBy: "u1", SiteID: "site-a", UserCount: 5,
		LastMsgAt:         &lastMsg,
		LastMsgID:         "m1",
		LastMentionAllAt:  &lastMention,
		MinUserLastSeenAt: &minSeen,
		CreatedAt:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &r, &model.Room{})
}
```

In `TestRoomJSON_NilTimestampsOmitted` (around line 46), add an assertion for the new field:

```go
	_, hasMinSeen := raw["minUserLastSeenAt"]
	assert.False(t, hasMinSeen, "nil MinUserLastSeenAt must be omitted from JSON")
```

…and after the unmarshal block, add:

```go
	assert.Nil(t, dst.MinUserLastSeenAt, "absent JSON field must unmarshal to nil pointer")
```

- [ ] **Step 1.2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model`
Expected: compile error — `unknown field MinUserLastSeenAt in struct literal of type model.Room` (and `dst.MinUserLastSeenAt undefined`).

- [ ] **Step 1.3: Add the field**

Edit `pkg/model/room.go`. After the `LastMentionAllAt` line, insert:

```go
	MinUserLastSeenAt *time.Time `json:"minUserLastSeenAt,omitempty" bson:"minUserLastSeenAt,omitempty"`
```

Final relevant block:

```go
	LastMsgAt         *time.Time `json:"lastMsgAt,omitempty" bson:"lastMsgAt,omitempty"`
	LastMsgID         string     `json:"lastMsgId" bson:"lastMsgId"`
	LastMentionAllAt  *time.Time `json:"lastMentionAllAt,omitempty" bson:"lastMentionAllAt,omitempty"`
	MinUserLastSeenAt *time.Time `json:"minUserLastSeenAt,omitempty" bson:"minUserLastSeenAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt" bson:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt" bson:"updatedAt"`
```

(Re-align the struct columns — gofmt will fix any tab spacing.)

- [ ] **Step 1.4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS, including `TestRoomJSON` and `TestRoomJSON_NilTimestampsOmitted`.

- [ ] **Step 1.5: Lint**

Run: `make lint`
Expected: clean exit.

- [ ] **Step 1.6: Commit**

```bash
git add pkg/model/room.go pkg/model/model_test.go
git commit -m "feat(model): add Room.MinUserLastSeenAt"
```

---

### Task 2: Add `Subscription.ThreadUnread` and `Subscription.Alert`

**Files:**
- Modify: `pkg/model/subscription.go`
- Test: `pkg/model/model_test.go`

**Semantics reminder:** `Alert` is a stored materialization of `len(ThreadUnread) > 0`. The writer service maintains the invariant; this task only defines the field shape and serialization. `ThreadUnread` is a slice of `ThreadParentMessageID` strings.

- [ ] **Step 2.1: Write the failing tests**

Edit `pkg/model/model_test.go`. Update `TestSubscriptionJSON` (currently around line 383) to populate the new fields, replacing its existing struct literal:

```go
func TestSubscriptionJSON(t *testing.T) {
	hss := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := model.Subscription{
		ID:                 "s1",
		User:               model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID:             "r1",
		RoomType:           model.RoomTypeChannel,
		SiteID:             "site-a",
		Roles:              []model.Role{model.RoleOwner},
		HistorySharedSince: &hss,
		JoinedAt:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt:         time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		HasMention:         true,
		ThreadUnread:       []string{"parent-1", "parent-2"},
		Alert:              true,
	}

	data, err := json.Marshal(&s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dst model.Subscription
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(s, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, s)
	}
}
```

Add a new sibling test immediately after `TestSubscriptionJSON` to lock in the omit-empty / always-emit behaviors:

```go
func TestSubscriptionJSON_NewFieldsOmitBehavior(t *testing.T) {
	s := model.Subscription{
		ID:       "s1",
		User:     model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID:   "r1",
		RoomType: model.RoomTypeChannel,
		SiteID:   "site-a",
		Roles:    []model.Role{model.RoleMember},
		JoinedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		// ThreadUnread nil, Alert false — defaults
	}

	data, err := json.Marshal(&s)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	_, hasThreadUnread := raw["threadUnread"]
	assert.False(t, hasThreadUnread, "nil/empty ThreadUnread must be omitted from JSON")

	alertVal, hasAlert := raw["alert"]
	assert.True(t, hasAlert, "alert must be present in JSON even when false")
	assert.Equal(t, false, alertVal)

	var dst model.Subscription
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Nil(t, dst.ThreadUnread, "absent threadUnread must unmarshal to nil")
	assert.False(t, dst.Alert)
}
```

- [ ] **Step 2.2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model`
Expected: compile error — `unknown field ThreadUnread in struct literal of type model.Subscription` and `unknown field Alert in struct literal of type model.Subscription`.

- [ ] **Step 2.3: Add the fields**

Edit `pkg/model/subscription.go`. After the `HasMention` line in `Subscription`, insert:

```go
	ThreadUnread []string `json:"threadUnread,omitempty" bson:"threadUnread,omitempty"`
	Alert        bool     `json:"alert" bson:"alert"`
```

Final relevant block:

```go
type Subscription struct {
	ID                 string           `json:"id" bson:"_id"`
	User               SubscriptionUser `json:"u" bson:"u"`
	RoomID             string           `json:"roomId" bson:"roomId"`
	RoomType           RoomType         `json:"roomType" bson:"roomType"`
	SiteID             string           `json:"siteId" bson:"siteId"`
	Roles              []Role           `json:"roles" bson:"roles"`
	HistorySharedSince *time.Time       `json:"historySharedSince,omitempty" bson:"historySharedSince,omitempty"`
	JoinedAt           time.Time        `json:"joinedAt" bson:"joinedAt"`
	LastSeenAt         time.Time        `json:"lastSeenAt" bson:"lastSeenAt"`
	HasMention         bool             `json:"hasMention" bson:"hasMention"`
	ThreadUnread       []string         `json:"threadUnread,omitempty" bson:"threadUnread,omitempty"`
	Alert              bool             `json:"alert" bson:"alert"`
}
```

- [ ] **Step 2.4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS, including `TestSubscriptionJSON` and the new `TestSubscriptionJSON_NewFieldsOmitBehavior`.

- [ ] **Step 2.5: Lint**

Run: `make lint`
Expected: clean exit.

- [ ] **Step 2.6: Commit**

```bash
git add pkg/model/subscription.go pkg/model/model_test.go
git commit -m "feat(model): add Subscription.ThreadUnread and Alert"
```

---

### Task 3: Remove `Unread` from `pkg/model/cassandra.Message`

**Files:**
- Modify: `pkg/model/cassandra/message.go`
- Modify: `pkg/model/cassandra/message_test.go`

**Rationale:** Per-message unread state is being centralized at the subscription level via `ThreadUnread`/`Alert`. The Cassandra column will be left in place physically (ops/IaC owns prod schema); removing the Go field is a read-safe change because gocql `structScan` ignores columns not present in the struct's `cql` tags.

This task touches only the model and its unit tests. The cassrepo column-list constants and integration-test fixtures are addressed in Tasks 4 and 7 respectively.

- [ ] **Step 3.1: Update unit tests first (drop `Unread` references)**

Edit `pkg/model/cassandra/message_test.go`:

In `TestMessage_JSON_Full` (around line 132), remove the `Unread: true,` line from the struct literal (currently line 153) and remove the `assert.True(t, got.Unread)` line (currently line 182).

In `TestMessage_JSON_Minimal` (around line 196), remove `assert.False(t, got.Unread)` (currently line 218).

- [ ] **Step 3.2: Verify the unit tests fail to compile**

Run: `make test SERVICE=pkg/model`
Expected: tests still PASS — the test edits removed references to `Unread` but the field still exists on the struct, so compilation succeeds. (This step is a sanity check that the test edits compile cleanly before we touch the struct.)

> Note: this task departs slightly from strict TDD red-then-green because we're *removing* a field, not adding one. The "red" we care about is "downstream callers no longer reference the removed field"; we lean on the compiler to catch any reference we missed in Step 3.3.

- [ ] **Step 3.3: Remove the field from the struct**

Edit `pkg/model/cassandra/message.go`. Delete the `Unread` line (currently line 88):

```go
	Unread                bool                     `json:"unread,omitempty"                cql:"unread"`
```

The surrounding context after the edit should look like:

```go
	VisibleTo             string                   `json:"visibleTo,omitempty"             cql:"visible_to"`
	Reactions             map[string][]Participant `json:"reactions,omitempty"             cql:"reactions"`
	Deleted               bool                     `json:"deleted,omitempty"               cql:"deleted"`
```

- [ ] **Step 3.4: Confirm package builds and unit tests pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS for both `pkg/model` and `pkg/model/cassandra`. If any test references a now-removed `Unread`, the compiler will surface it — fix by deleting that reference, then re-run.

- [ ] **Step 3.5: Confirm no other unit-test code in the repo still references the field**

Run: `grep -rEn "\.Unread\b|Unread:\s*(true|false)" --include="*.go" pkg/ history-service/ | grep -v "_test.go"; echo "---test files---"; grep -rEn "\.Unread\b|Unread:\s*(true|false)" --include="*_test.go" pkg/`
Expected: no production-code matches (`---test files---` separator with no `pkg/model/cassandra/message_test.go` lines remaining). Integration-test files under `history-service/internal/cassrepo/` and `history-service/internal/service/` are addressed in Task 7 and may still appear here — that's expected at this stage.

- [ ] **Step 3.6: Lint**

Run: `make lint`
Expected: clean exit. (history-service still references `unread` only in column-list strings and integration-test DDLs, which are not affected by removing the Go struct field.)

- [ ] **Step 3.7: Commit**

```bash
git add pkg/model/cassandra/message.go pkg/model/cassandra/message_test.go
git commit -m "refactor(cassandra): remove unused Unread field from Message model"
```

---

### Task 4: Drop `unread` from `cassrepo` column-list constants

**Files:**
- Modify: `history-service/internal/cassrepo/messages_by_room.go`
- Modify: `history-service/internal/cassrepo/thread_messages.go`

**Why these two only:**
- `messages_by_room.go` exports `baseColumns`, which is also reused by `messages_by_id.go` via `messageByIDQuery = "SELECT " + baseColumns + ", pinned_at, pinned_by FROM messages_by_id"`. Updating `baseColumns` propagates to both `messages_by_room` and `messages_by_id` SELECTs.
- `thread_messages.go` defines its own `threadMessageColumns` for the `thread_messages_by_room` SELECT.
- The `pinned_messages_by_room` SELECT happens in a separate file but does **not** include `unread` in its column list (verified: no `unread` reference in `pinned_messages.go` or equivalent), so no edit needed there.

**Verification:** `make test SERVICE=history-service` covers these constants only via integration tests. We rely on those for runtime validation in Task 7. For this task, the unit-level guarantee is that the package compiles and unit tests still pass.

- [ ] **Step 4.1: Drop `unread` from `baseColumns`**

Edit `history-service/internal/cassrepo/messages_by_room.go`. Change:

```go
const baseColumns = "room_id, created_at, message_id, thread_room_id, sender, target_user, " +
	"msg, mentions, attachments, file, card, card_action, tshow, tcount, " +
	"thread_parent_id, thread_parent_created_at, quoted_parent_message, " +
	"visible_to, unread, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"
```

To:

```go
const baseColumns = "room_id, created_at, message_id, thread_room_id, sender, target_user, " +
	"msg, mentions, attachments, file, card, card_action, tshow, tcount, " +
	"thread_parent_id, thread_parent_created_at, quoted_parent_message, " +
	"visible_to, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"
```

(Only the `unread, ` token is removed; spacing/quoting on the surrounding lines is preserved.)

- [ ] **Step 4.2: Drop `unread` from `threadMessageColumns`**

Edit `history-service/internal/cassrepo/thread_messages.go`. Change:

```go
const threadMessageColumns = "room_id, thread_room_id, created_at, message_id, thread_parent_id, " +
	"sender, target_user, msg, mentions, attachments, file, card, card_action, " +
	"quoted_parent_message, visible_to, unread, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"
```

To:

```go
const threadMessageColumns = "room_id, thread_room_id, created_at, message_id, thread_parent_id, " +
	"sender, target_user, msg, mentions, attachments, file, card, card_action, " +
	"quoted_parent_message, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"
```

(Only `unread, ` is removed; `visible_to, ` continues directly into `reactions, `.)

- [ ] **Step 4.3: Confirm package compiles and unit tests pass**

Run: `make test SERVICE=history-service`
Expected: PASS. (This runs the non-integration tests; cassrepo unit tests do not exercise the SELECTs against a live Cassandra.)

- [ ] **Step 4.4: Lint**

Run: `make lint`
Expected: clean exit.

- [ ] **Step 4.5: Confirm no other production code still mentions `unread` in a column list**

Run: `grep -rn '"unread\b\|, unread\b\|unread,' --include="*.go" history-service/ | grep -v "_test.go"`
Expected: no output. Any remaining matches inside `*_integration_test.go` are addressed in Task 7.

- [ ] **Step 4.6: Commit**

```bash
git add history-service/internal/cassrepo/messages_by_room.go history-service/internal/cassrepo/thread_messages.go
git commit -m "refactor(history-service): drop unread column from cassrepo SELECTs"
```

---

### Task 5: Drop `unread` from local-dev Cassandra init DDL

**Files:**
- Modify: `docker-local/cassandra/init/10-table-messages_by_room.cql`
- Modify: `docker-local/cassandra/init/11-table-thread_messages_by_room.cql`
- Modify: `docker-local/cassandra/init/12-table-pinned_messages_by_room.cql`
- Modify: `docker-local/cassandra/init/13-table-messages_by_id.cql`

**Context:** These `.cql` files are executed by the `cassandra-init` one-shot from `make deps-up` to create the local-dev keyspace + tables. They are **not** consulted by Go integration tests (those tests embed their own DDL — see Task 7). Production schema is owned by ops/IaC and is out of scope.

Each file has a single `unread BOOLEAN,` line that needs to go away. Column ordering is otherwise preserved.

- [ ] **Step 5.1: `10-table-messages_by_room.cql` — drop `unread`**

In `docker-local/cassandra/init/10-table-messages_by_room.cql`, delete line 20:

```cql
  unread                   BOOLEAN,
```

The surrounding context after the edit:

```cql
  quoted_parent_message    FROZEN<"QuotedParentMessage">,
  visible_to               TEXT,
  reactions                MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
```

- [ ] **Step 5.2: `11-table-thread_messages_by_room.cql` — drop `unread`**

In `docker-local/cassandra/init/11-table-thread_messages_by_room.cql`, delete line 17:

```cql
  unread                BOOLEAN,
```

Surrounding context after the edit:

```cql
  quoted_parent_message FROZEN<"QuotedParentMessage">,
  visible_to            TEXT,
  reactions             MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
```

- [ ] **Step 5.3: `12-table-pinned_messages_by_room.cql` — drop `unread`**

In `docker-local/cassandra/init/12-table-pinned_messages_by_room.cql`, delete line 15:

```cql
  unread                BOOLEAN,
```

Surrounding context after the edit:

```cql
  quoted_parent_message FROZEN<"QuotedParentMessage">,
  visible_to            TEXT,
  reactions             MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
```

- [ ] **Step 5.4: `13-table-messages_by_id.cql` — drop `unread`**

In `docker-local/cassandra/init/13-table-messages_by_id.cql`, delete line 19:

```cql
  unread                   BOOLEAN,
```

Surrounding context after the edit:

```cql
  quoted_parent_message    FROZEN<"QuotedParentMessage">,
  visible_to               TEXT,
  reactions                MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
```

- [ ] **Step 5.5: Verify no `unread` remains under `docker-local/cassandra/`**

Run: `grep -rn unread docker-local/cassandra/`
Expected: no output.

- [ ] **Step 5.6: (Optional) Re-bootstrap local Cassandra**

> Skip this step in CI / agentic execution; execute only on a developer machine where local Cassandra is running.

```bash
make deps-down
make deps-up
```

Expected: `cassandra-init` runs the four updated `.cql` files without error and produces the four tables without an `unread` column.

- [ ] **Step 5.7: Commit**

```bash
git add docker-local/cassandra/init/10-table-messages_by_room.cql \
        docker-local/cassandra/init/11-table-thread_messages_by_room.cql \
        docker-local/cassandra/init/12-table-pinned_messages_by_room.cql \
        docker-local/cassandra/init/13-table-messages_by_id.cql
git commit -m "chore(cassandra-init): drop unread column from local-dev DDL"
```

---
