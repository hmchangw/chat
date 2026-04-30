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
