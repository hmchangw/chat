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
- Modify: `history-service/docker-local/docker-compose.yml` (inline DDL embedded in the `cassandra-init` shell command — two `unread BOOLEAN,` lines, byte-identical, removable via a single `replace_all` Edit on the surrounding `visible_to TEXT,` / `reactions MAP<…>,` block)

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

### Task 6: Drop `unread` from `docs/cassandra_message_model.md`

**Files:**
- Modify: `docs/cassandra_message_model.md`

**Context:** Per CLAUDE.md, this doc is the single source of truth for the Cassandra message schema. Changes here must stay in lockstep with `pkg/model/cassandra/` (Task 3) and the local-dev init DDL (Task 5). The doc has four occurrences of `unread BOOLEAN,` — one per table block — and they all must be removed.

- [ ] **Step 6.1: Drop `unread BOOLEAN,` from `messages_by_room` table block**

Edit `docs/cassandra_message_model.md`. In the `messages_by_room` block, delete line 82:

```
  unread BOOLEAN,
```

After the edit, the relevant lines in that block read:

```
  visible_to TEXT,
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
```

- [ ] **Step 6.2: Drop `unread BOOLEAN,` from `thread_messages_by_room` table block**

In the `thread_messages_by_room` block, delete line 111:

```
  unread BOOLEAN,
```

After the edit:

```
  visible_to TEXT,
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
```

- [ ] **Step 6.3: Drop `unread BOOLEAN,` from `pinned_messages_by_room` table block**

In the `pinned_messages_by_room` block, delete line 138:

```
  unread BOOLEAN,
```

After the edit:

```
  visible_to TEXT,
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
```

- [ ] **Step 6.4: Drop `unread BOOLEAN,` from `messages_by_id` table block**

In the `messages_by_id` block, delete line 170:

```
  unread BOOLEAN,
```

After the edit:

```
  visible_to TEXT,
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
```

> **Implementation note for editor:** because all four removed lines are identical (`  unread BOOLEAN,`) and appear in distinct table blocks, the safest way to apply this task is one Edit per block, scoping the `old_string` to include the unique surrounding lines (e.g. the line above and below) so each Edit matches exactly one location. Do **not** use a global "replace all" — line numbers above are guidance, not anchors.

- [ ] **Step 6.5: Verify no `unread` remains in the doc**

Run: `grep -n unread docs/cassandra_message_model.md`
Expected: no output.

- [ ] **Step 6.6: Cross-check doc against Go struct**

Run: `grep -n 'cql:"' pkg/model/cassandra/message.go | wc -l`
Compare the count to the number of column lines in any one of the doc's table blocks (excluding primary-key declarations). They should align field-for-field — no `unread` mismatch on either side.

- [ ] **Step 6.7: Commit**

```bash
git add docs/cassandra_message_model.md
git commit -m "docs(cassandra): drop unread column from message schema doc"
```

---

### Task 7: Drop `unread` from integration-test inline DDL and INSERTs

**Files:**
- Modify: `history-service/internal/cassrepo/integration_test.go`
- Modify: `history-service/internal/cassrepo/thread_messages_integration_test.go`
- Modify: `history-service/internal/cassrepo/messages_by_id_integration_test.go`
- Modify: `history-service/internal/service/integration_test.go`

**Context:** Integration tests embed their own keyspace DDL strings (so the test container doesn't depend on `docker-local/cassandra/init/*.cql`) and, in two cases, hand-rolled INSERT statements. Each occurrence of `unread BOOLEAN,` (DDL) and `unread,` (INSERT column list) — plus the matching `?` placeholder and value-arg — must be removed.

This is the runtime-validation task: once it lands, `make test-integration SERVICE=history-service` will rebuild the test container against the new schema and exercise the new SELECT/INSERT shapes end-to-end.

#### 7A. `cassrepo/integration_test.go` (3 DDLs)

- [ ] **Step 7.1: `messages_by_room` DDL — drop `unread BOOLEAN,`**

In `history-service/internal/cassrepo/integration_test.go`, in the `CREATE TABLE IF NOT EXISTS %s.messages_by_room` block (around lines 30–58), delete line 49:

```
		unread BOOLEAN,
```

Surrounding context after the edit:

```
		quoted_parent_message FROZEN<"QuotedParentMessage">,
		visible_to TEXT,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
```

- [ ] **Step 7.2: `messages_by_id` DDL — drop `unread BOOLEAN,`**

In the same file, in the `CREATE TABLE IF NOT EXISTS %s.messages_by_id` block (around lines 60–90), delete line 78:

```
		unread BOOLEAN,
```

Surrounding context after the edit:

```
		quoted_parent_message FROZEN<"QuotedParentMessage">,
		visible_to TEXT,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
```

- [ ] **Step 7.3: `thread_messages_by_room` DDL — drop `unread BOOLEAN,`**

In the same file, in the `CREATE TABLE IF NOT EXISTS %s.thread_messages_by_room` block (around lines 92–117), delete line 108:

```
		unread BOOLEAN,
```

Surrounding context after the edit:

```
		quoted_parent_message FROZEN<"QuotedParentMessage">,
		visible_to TEXT,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
```

> All three Edits target the same file. Use distinct surrounding context for each Edit — line numbers above are guidance, not anchors. Three near-identical strings in one file is a classic ambiguity trap.

#### 7B. `cassrepo/thread_messages_integration_test.go` (1 INSERT)

- [ ] **Step 7.4: Drop `unread` from the `thread_messages_by_room` INSERT**

In `history-service/internal/cassrepo/thread_messages_integration_test.go`, around line 189, the test runs:

```go
insertCQL := `INSERT INTO thread_messages_by_room (
    room_id, thread_room_id, created_at, message_id, thread_parent_id,
    sender, target_user, msg, mentions, attachments, file, card, card_action,
    quoted_parent_message, visible_to, unread, reactions, deleted,
    type, sys_msg_data, site_id, edited_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
```

Change to (drop `unread,` from the column list **and** drop one `?` from the placeholder list — count: 23 → 22):

```go
insertCQL := `INSERT INTO thread_messages_by_room (
    room_id, thread_room_id, created_at, message_id, thread_parent_id,
    sender, target_user, msg, mentions, attachments, file, card, card_action,
    quoted_parent_message, visible_to, reactions, deleted,
    type, sys_msg_data, site_id, edited_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
```

Then in the immediately following `insertArgs := []any{ … }` slice (around line 195), find the line:

```go
		quotedMsg, "u1", true,
```

The trailing `true` is the value for the `unread` column. Change to:

```go
		quotedMsg, "u1",
```

Sanity check: count the values in `insertArgs` before/after. Before the edit the slice has 23 items in column order; after the edit, 22. Each value must align positionally with a column name in the INSERT.

#### 7C. `cassrepo/messages_by_id_integration_test.go` (1 INSERT + 1 assert)

- [ ] **Step 7.5: Drop `unread` from the `messages_by_id` INSERT**

In `history-service/internal/cassrepo/messages_by_id_integration_test.go`, around line 71:

```go
insertCQL := `INSERT INTO messages_by_id (room_id, created_at, message_id, sender, target_user, msg, mentions, attachments, file, card, card_action, tshow, thread_parent_id, thread_parent_created_at, quoted_parent_message, visible_to, unread, reactions, deleted, type, sys_msg_data, site_id, edited_at, updated_at, thread_room_id, pinned_at, pinned_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
```

Change to (drop `unread, ` from the column list **and** drop one `?` from the placeholder list — count: 27 → 26):

```go
insertCQL := `INSERT INTO messages_by_id (room_id, created_at, message_id, sender, target_user, msg, mentions, attachments, file, card, card_action, tshow, thread_parent_id, thread_parent_created_at, quoted_parent_message, visible_to, reactions, deleted, type, sys_msg_data, site_id, edited_at, updated_at, thread_room_id, pinned_at, pinned_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
```

Then in `insertArgs` (around line 72) the line that currently reads:

```go
		true, "m-parent", threadParent, quotedMsg, "u1", true,
```

The leading `true` is `tshow`; the trailing `true` (after `"u1"`) is `unread`. Drop the trailing `true`:

```go
		true, "m-parent", threadParent, quotedMsg, "u1",
```

Sanity check: positional alignment. `insertArgs` had 27 items; after the edit, 26.

Then, in the same file (around line 159), drop the `assert.True(t, msg.Unread)` line. The surrounding assertions:

```go
	assert.Equal(t, "u1", msg.VisibleTo)
	assert.True(t, msg.Unread)   // ← delete this line
	assert.True(t, msg.Deleted)
```

After the edit:

```go
	assert.Equal(t, "u1", msg.VisibleTo)
	assert.True(t, msg.Deleted)
```

This assert is gated by `//go:build integration`, so `make test` (unit only) does not catch the now-dangling `msg.Unread` reference; the integration build does. Run `go vet -tags integration ./history-service/...` after the edit — expected output: empty.

#### 7C2. `cassrepo/thread_messages_integration_test.go` (1 assert)

- [ ] **Step 7.5b: Drop `assert.True(t, msg.Unread)`**

In `history-service/internal/cassrepo/thread_messages_integration_test.go` (around line 282), drop the `assert.True(t, msg.Unread)` line. Same shape as Step 7.5's assert delete — surrounded by `assert.Equal(t, "u1", msg.VisibleTo)` and `assert.True(t, msg.Deleted)`.

After the edit:

```go
	// Scalars
	assert.Equal(t, "u1", msg.VisibleTo)
	assert.True(t, msg.Deleted)
```

#### 7D. `service/integration_test.go` (3 DDLs)

- [ ] **Step 7.6: `messages_by_room` DDL — drop `unread BOOLEAN`**

In `history-service/internal/service/integration_test.go`, in the `CREATE TABLE IF NOT EXISTS %s.messages_by_room` block (around lines 42–52), the relevant line packs `visible_to`, `unread`, and the next column on line 48:

```
		quoted_parent_message FROZEN<"QuotedParentMessage">, visible_to TEXT, unread BOOLEAN,
```

Change to:

```
		quoted_parent_message FROZEN<"QuotedParentMessage">, visible_to TEXT,
```

- [ ] **Step 7.7: `messages_by_id` DDL — drop `unread BOOLEAN`**

In the same file, in the `CREATE TABLE IF NOT EXISTS %s.messages_by_id` block (around lines 55–66), line 61:

```
		quoted_parent_message FROZEN<"QuotedParentMessage">, visible_to TEXT, unread BOOLEAN,
```

Change to:

```
		quoted_parent_message FROZEN<"QuotedParentMessage">, visible_to TEXT,
```

- [ ] **Step 7.8: `thread_messages_by_room` DDL — drop `unread BOOLEAN`**

In the same file, in the `CREATE TABLE IF NOT EXISTS %s.thread_messages_by_room` block (around lines 69–79), line 75:

```
		quoted_parent_message FROZEN<"QuotedParentMessage">, visible_to TEXT, unread BOOLEAN,
```

Change to:

```
		quoted_parent_message FROZEN<"QuotedParentMessage">, visible_to TEXT,
```

> All three lines in `service/integration_test.go` are byte-identical. Disambiguate each Edit by including the surrounding `CREATE TABLE` line or other unique nearby tokens.

#### 7E. Verification

- [ ] **Step 7.9: Confirm no `unread` remains in repo (excluding superseded plans/specs)**

Run:

```bash
grep -rn unread \
  --include="*.go" --include="*.cql" --include="*.md" --include="*.yml" --include="*.yaml" \
  pkg/ history-service/ docker-local/ docs/ \
  | grep -v "docs/superpowers/plans/" \
  | grep -v "docs/superpowers/specs/"
```

> **Note (added retroactively):** the original sweep filter omitted `*.yml`/`*.yaml`, which missed inline DDL embedded in `history-service/docker-local/docker-compose.yml`. The filter above includes them. Always include every file extension where DDL or column lists could appear.

Expected: only matches in semantic prose (e.g., `mongorepo/pipelines.go` comment "Unread = subscribed AND lastMsgAt > lastSeenAt …", `mongorepo/threadroom.go` `fmt.Errorf("querying unread thread rooms: …")`, `models/thread_parent.go` `ThreadFilterUnread = "unread"`, `nats-subject-naming.md` discussing unread badges). No occurrences in DDL, INSERT, SELECT column lists, struct fields, or struct literals.

> The grep filter excludes `docs/superpowers/plans/` and `docs/superpowers/specs/` because superseded plans (e.g. `2026-04-21-history-list-threads.md`, `2026-04-15-move-cassandra-message-to-pkg.md`) intentionally retain their original wording. Only operational artifacts must be `unread`-free after this task.

- [ ] **Step 7.9b: Verify integration build compiles**

Run: `go vet -tags integration ./history-service/...`
Expected: empty output. This catches dangling `msg.Unread` field accesses that `make test` misses because they live behind `//go:build integration`.

- [ ] **Step 7.10: Run unit tests**

Run: `make test SERVICE=history-service`
Expected: PASS.

- [ ] **Step 7.11: Run integration tests**

Run: `make test-integration SERVICE=history-service`
Expected: PASS. The Cassandra testcontainer is started fresh per run, applies the updated inline DDL, exercises the updated SELECTs from Tasks 4 and 7's INSERTs, and verifies round-trips against the now-fieldless `cassandra.Message` struct.

If integration tests fail, common causes:
- A column name in an INSERT no longer matches its placeholder count (positional misalignment) — re-check Step 7.4 / 7.5 sanity counts.
- A column referenced by a SELECT (in `cassrepo/*.go` from Task 4) is missing from the test DDL — re-check Steps 7.1–7.3 / 7.6–7.8.
- Cassandra rejects the DDL (syntax) — verify only the `unread BOOLEAN,` token was removed and surrounding commas/whitespace are intact.

- [ ] **Step 7.12: Lint**

Run: `make lint`
Expected: clean exit.

- [ ] **Step 7.13: Commit**

```bash
git add history-service/internal/cassrepo/integration_test.go \
        history-service/internal/cassrepo/thread_messages_integration_test.go \
        history-service/internal/cassrepo/messages_by_id_integration_test.go \
        history-service/internal/service/integration_test.go
git commit -m "test(history-service): drop unread column from integration fixtures"
```

---

## Plan Self-Review

### Spec coverage

| Spec section | Covered by |
|---|---|
| `Room.MinUserLastSeenAt` field + tags | Task 1 |
| `Subscription.ThreadUnread` + `Alert` fields + tags | Task 2 |
| Remove `Unread` from `pkg/model/cassandra.Message` (struct + tests) | Task 3 |
| Drop `unread` from cassrepo SELECTs (`baseColumns`, `threadMessageColumns`) | Task 4 |
| Local-dev DDL (`docker-local/cassandra/init/*.cql`) | Task 5 |
| `docs/cassandra_message_model.md` | Task 6 |
| Integration-test DDL (`cassrepo/integration_test.go`, `service/integration_test.go`) | Task 7 |
| Integration-test INSERTs (`messages_by_id_integration_test.go`, `thread_messages_integration_test.go`) | Task 7 |
| Backward compat: prod ALTER TABLE | Out of scope (per spec) |
| Backward compat: pre-existing Mongo docs | No-op (verified by Tasks 1, 2 omit/default behavior) |

### Per-task verification gate

Each task ends with `make test` / `make lint` / (Task 7) `make test-integration`. The cumulative chain leaves the build green at every commit boundary.

### Placeholder scan

Searched the plan for "TBD", "TODO", "fill in", "appropriate", "similar to", "etc". None remain.

### Type / identifier consistency

- `MinUserLastSeenAt` (Task 1) ↔ JSON `minUserLastSeenAt` ↔ BSON `minUserLastSeenAt` — consistent.
- `ThreadUnread` (Task 2) ↔ JSON `threadUnread` ↔ BSON `threadUnread` — consistent.
- `Alert` (Task 2) ↔ JSON `alert` ↔ BSON `alert` — consistent.
- `baseColumns` and `threadMessageColumns` (Task 4) match their actual constant names in `messages_by_room.go` and `thread_messages.go`.
- INSERT column counts in Steps 7.4 (23 → 22) and 7.5 (27 → 26) match the placeholder-count drop and the `insertArgs` slice-length drop.

### Ordering invariant

Tasks are ordered so that the build is green at every commit:
- Tasks 1, 2 are pure additions.
- Task 3 removes a Go field; gocql `structScan` ignores Cassandra columns absent from the struct, so SELECTs still asking for `unread` continue to work between commit 3 and commit 4.
- Task 4 trims SELECTs; integration tests still pass because the DDL still has the column.
- Task 5 trims local-dev DDL; no Go test depends on these files.
- Task 6 trims the schema doc; no test depends on it.
- Task 7 trims integration-test DDL + INSERTs; the testcontainer recreates schema per run, so this commit is the one that actually changes integration-test runtime behavior. Tests must pass after this commit.

