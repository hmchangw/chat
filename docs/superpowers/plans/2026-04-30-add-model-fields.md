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
