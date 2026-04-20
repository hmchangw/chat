# Search Sync Worker — Part 1: Model & Subject Changes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add EventType discriminator to MessageEvent and new canonical subject builders to support CUD (Create/Update/Delete) events on MESSAGES_CANONICAL.

**Architecture:** Extend existing MessageEvent with backward-compatible `Event` field. Add subject builders for canonical updated/deleted subjects. No existing worker changes needed — empty Event field is silently ignored by current consumers.

**Tech Stack:** Go, pkg/model, pkg/subject

**Spec:** `docs/superpowers/specs/2026-04-07-search-sync-worker-design.md`

---

## File Structure

| Action | File | Purpose |
|--------|------|---------|
| Modify | `pkg/model/event.go` | Add EventType + Event field to MessageEvent |
| Modify | `pkg/model/model_test.go` | Round-trip tests for new fields |
| Modify | `pkg/subject/subject.go` | Add MsgCanonicalUpdated, MsgCanonicalDeleted builders |
| Modify | `pkg/subject/subject_test.go` | Tests for new builders |

---

### Task 1: Add EventType to MessageEvent

**Files:**
- Modify: `pkg/model/event.go:1-9`
- Modify: `pkg/model/model_test.go:131-142`

- [ ] **Step 1: Write failing tests in `pkg/model/model_test.go`**

Add these tests after the existing `TestMessageEventJSON` (line 142):

```go
func TestEventTypeValues(t *testing.T) {
	assert.Equal(t, model.EventType("created"), model.EventCreated)
	assert.Equal(t, model.EventType("updated"), model.EventUpdated)
	assert.Equal(t, model.EventType("deleted"), model.EventDeleted)
}

func TestMessageEventJSON_WithEvent(t *testing.T) {
	t.Run("created event round-trip", func(t *testing.T) {
		src := model.MessageEvent{
			Event: model.EventCreated,
			Message: model.Message{
				ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
				Content:   "hello",
				CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			},
			SiteID:    "site-a",
			Timestamp: 1735689600000,
		}
		data, err := json.Marshal(src)
		require.NoError(t, err)
		var dst model.MessageEvent
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, src, dst)
	})

	t.Run("event field omitted when empty (backward compat)", func(t *testing.T) {
		src := model.MessageEvent{
			Message: model.Message{
				ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
				Content:   "hello",
				CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			},
			SiteID:    "site-a",
			Timestamp: 1735689600000,
		}
		data, err := json.Marshal(src)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, present := raw["event"]
		assert.False(t, present, "event should be omitted when empty")
	})

	t.Run("deleted event round-trip", func(t *testing.T) {
		src := model.MessageEvent{
			Event: model.EventDeleted,
			Message: model.Message{
				ID:        "m1",
				RoomID:    "r1",
				CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			},
			SiteID:    "site-a",
			Timestamp: 1735689600000,
		}
		data, err := json.Marshal(src)
		require.NoError(t, err)
		var dst model.MessageEvent
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, src, dst)
	})
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — `model.EventType`, `model.EventCreated`, etc. undefined

- [ ] **Step 3: Add EventType and Event field to `pkg/model/event.go`**

Add the type and constants before MessageEvent, and add the Event field:

```go
type EventType string

const (
	EventCreated EventType = "created"
	EventUpdated EventType = "updated"
	EventDeleted EventType = "deleted"
)

type MessageEvent struct {
	Event     EventType `json:"event,omitempty" bson:"event,omitempty"`
	Message   Message   `json:"message"`
	SiteID    string    `json:"siteId"`
	Timestamp int64     `json:"timestamp" bson:"timestamp"`
}
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/model/event.go pkg/model/model_test.go
git commit -m "feat(model): add EventType discriminator to MessageEvent for CUD events"
```

---

### Task 2: Add canonical subject builders

**Files:**
- Modify: `pkg/subject/subject.go:80-81`
- Modify: `pkg/subject/subject_test.go:37-38`

- [ ] **Step 1: Write failing tests in `pkg/subject/subject_test.go`**

Add to the `TestSubjectBuilders` table (after the MsgCanonicalCreated entry at line 38):

```go
{"MsgCanonicalUpdated", subject.MsgCanonicalUpdated("site-a"),
    "chat.msg.canonical.site-a.updated"},
{"MsgCanonicalDeleted", subject.MsgCanonicalDeleted("site-a"),
    "chat.msg.canonical.site-a.deleted"},
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `make test SERVICE=pkg/subject`
Expected: FAIL — `subject.MsgCanonicalUpdated` and `subject.MsgCanonicalDeleted` undefined

- [ ] **Step 3: Add builders to `pkg/subject/subject.go`**

Add after `MsgCanonicalCreated` (line 82):

```go
func MsgCanonicalUpdated(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.updated", siteID)
}

func MsgCanonicalDeleted(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.deleted", siteID)
}
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `make test SERVICE=pkg/subject`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add MsgCanonicalUpdated and MsgCanonicalDeleted builders"
```
