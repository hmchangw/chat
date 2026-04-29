# Message-worker thread subscription outbox events implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replicate `ThreadSubscription` writes (parent author, replier, mentionees) from message-worker on the room's home site to each affected user's home site via the existing OUTBOX/INBOX federation.

**Architecture:** Mirror room-worker's pattern. Message-worker gains a `publish PublishFunc` field; after every local Insert/Upsert/MarkThreadSubscriptionMention it emits a `thread_subscription_upserted` outbox event when the subscription owner's site differs from the room's site. Inbox-worker dispatches the new event type to a new `UpsertThreadSubscription` store method that uses MongoDB `$max` on `hasMention` for monotonic mention-flag merge.

**Tech Stack:** Go 1.25, NATS JetStream (`github.com/nats-io/nats.go/jetstream`), MongoDB (`go.mongodb.org/mongo-driver/v2`), `github.com/hmchangw/chat/pkg/{model,subject,idgen,mention,userstore,stream}`, `go.uber.org/mock`, `stretchr/testify`, `testcontainers-go`.

**Spec:** `docs/superpowers/specs/2026-04-28-message-worker-thread-subscription-outbox-design.md`

---

## File map

**Modify:**
- `pkg/model/event.go` — new `OutboxThreadSubscriptionUpserted` const; new `SiteID` field on `Participant`
- `pkg/model/model_test.go` — round-trip test for new event-type tag and `Participant.SiteID`
- `pkg/mention/mention.go` — propagate `SiteID` from looked-up `User` onto `Participant`
- `pkg/mention/mention_test.go` — extend `TestResolve` to assert `SiteID`
- `message-worker/handler.go` — `Handler` gains `siteID` + `publish` fields; new `publishThreadSubOutboxIfRemote` helper; owner-site semantic for `ThreadSubscription.SiteID`; parent-user lookup; publish wiring in three paths
- `message-worker/handler_test.go` — new test cases for cross-site publishes
- `message-worker/main.go` — wire `cfg.SiteID` and JetStream publish closure into `NewHandler`
- `inbox-worker/handler.go` — new dispatch case + handler for `thread_subscription_upserted`; extend `InboxStore` interface
- `inbox-worker/handler_test.go` — extend stub store with `threadSubscriptions`; new dispatch tests
- `inbox-worker/main.go` — `mongoInboxStore` gains `threadSubCol` + `UpsertThreadSubscription` method
- `inbox-worker/integration_test.go` — new integration tests for upsert insert path + monotonic `hasMention` merge

**No new files.** All work fits in existing files.

---

## Task 1: Add `OutboxThreadSubscriptionUpserted` constant

**Files:**
- Modify: `pkg/model/event.go:79-84`
- Modify (test): `pkg/model/model_test.go` (append a new test)

- [ ] **Step 1.1: Write the failing model test**

Append to `pkg/model/model_test.go` (after the existing `TestOutboxEventJSON`):

```go
func TestOutboxEventJSON_ThreadSubscriptionUpserted(t *testing.T) {
	src := model.OutboxEvent{
		Type:       model.OutboxThreadSubscriptionUpserted,
		SiteID:     "site-a",
		DestSiteID: "site-b",
		Payload:    []byte(`{"id":"sub-1","threadRoomId":"tr-1"}`),
		Timestamp:  1735689600000,
	}
	data, err := json.Marshal(&src)
	require.NoError(t, err)

	var dst model.OutboxEvent
	require.NoError(t, json.Unmarshal(data, &dst))
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
	}
	if dst.Type != "thread_subscription_upserted" {
		t.Errorf("Type = %q, want thread_subscription_upserted", dst.Type)
	}
}
```

- [ ] **Step 1.2: Run test to verify it fails**

Run: `make test SERVICE=pkg/model 2>&1 | tail -30`

Expected: compile error `undefined: model.OutboxThreadSubscriptionUpserted`.

- [ ] **Step 1.3: Add the constant**

In `pkg/model/event.go`, replace the existing const block (line ~81-84):

```go
const (
	OutboxMemberAdded                OutboxEventType = "member_added"
	OutboxMemberRemoved              OutboxEventType = "member_removed"
	OutboxThreadSubscriptionUpserted OutboxEventType = "thread_subscription_upserted"
)
```

- [ ] **Step 1.4: Run test to verify it passes**

Run: `make test SERVICE=pkg/model`

Expected: all `pkg/model` tests pass including the new one.

- [ ] **Step 1.5: Commit**

```bash
git add pkg/model/event.go pkg/model/model_test.go
git commit -m "model: add OutboxThreadSubscriptionUpserted event type constant"
```

---

## Task 2: Add `SiteID` to `model.Participant` and propagate from `mention.Resolve`

**Files:**
- Modify: `pkg/model/event.go` (the `Participant` struct, around line 105-110)
- Modify: `pkg/model/model_test.go` (extend `TestParticipantJSON`)
- Modify: `pkg/mention/mention.go:77-84`
- Modify: `pkg/mention/mention_test.go` (extend `TestResolve`)

- [ ] **Step 2.1: Write the failing model test for `Participant.SiteID`**

Replace the `TestParticipantJSON` function in `pkg/model/model_test.go` with the version below (it adds two new sub-tests; keep the existing two):

```go
func TestParticipantJSON(t *testing.T) {
	t.Run("with userID", func(t *testing.T) {
		p := model.Participant{
			UserID:      "u1",
			Account:     "alice",
			ChineseName: "愛麗絲",
			EngName:     "Alice Wang",
		}
		roundTrip(t, &p, &model.Participant{})
	})

	t.Run("without userID omitted", func(t *testing.T) {
		p := model.Participant{
			Account:     "bob",
			ChineseName: "鮑勃",
			EngName:     "Bob Chen",
		}
		data, err := json.Marshal(p)
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, hasUserID := raw["userId"]
		assert.False(t, hasUserID, "userId should be omitted when empty")

		var dst model.Participant
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, p, dst)
	})

	t.Run("with siteID round-trips", func(t *testing.T) {
		p := model.Participant{
			UserID:      "u1",
			Account:     "alice",
			SiteID:      "site-a",
			ChineseName: "愛麗絲",
			EngName:     "Alice Wang",
		}
		roundTrip(t, &p, &model.Participant{})
	})

	t.Run("siteID omitted when empty", func(t *testing.T) {
		p := model.Participant{
			UserID:  "u1",
			Account: "alice",
			EngName: "Alice Wang",
		}
		data, err := json.Marshal(p)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, hasSiteID := raw["siteId"]
		assert.False(t, hasSiteID, "siteId should be omitted when empty")
	})
}
```

- [ ] **Step 2.2: Run test to verify it fails**

Run: `make test SERVICE=pkg/model`

Expected: compile error `unknown field SiteID in struct literal of type model.Participant`.

- [ ] **Step 2.3: Add the `SiteID` field**

In `pkg/model/event.go`, replace the `Participant` struct definition with:

```go
// Participant represents a user with display name info for client rendering.
type Participant struct {
	UserID      string `json:"userId,omitempty" bson:"userId,omitempty"`
	Account     string `json:"account" bson:"account"`
	SiteID      string `json:"siteId,omitempty" bson:"siteId,omitempty"`
	ChineseName string `json:"chineseName" bson:"chineseName"`
	EngName     string `json:"engName" bson:"engName"`
}
```

- [ ] **Step 2.4: Run model tests to verify they pass**

Run: `make test SERVICE=pkg/model`

Expected: all `pkg/model` tests pass.

- [ ] **Step 2.5: Write the failing mention test**

Replace the "single mention resolved" and "multiple mentions resolved" cases in `TestResolve` (`pkg/mention/mention_test.go`) — change the user fixtures and `wantParts` to include `SiteID`. Use this diff (replace the two named test cases inline):

```go
		bobUser := model.User{ID: "u-bob", Account: "bob", SiteID: "site-b", EngName: "Bob Chen", ChineseName: "鮑勃"}
		aliceUser := model.User{ID: "u-alice", Account: "alice", SiteID: "site-a", EngName: "Alice Wang", ChineseName: "愛麗絲"}
```

(Update both `bobUser` and `aliceUser` declarations at the top of `TestResolve` — add `SiteID` field.)

Then update `wantParts` for these cases:

```go
		{
			name:         "single mention resolved",
			content:      "hey @bob",
			lookupUsers:  []model.User{bobUser},
			wantAccounts: []string{"bob"},
			wantParts: []model.Participant{
				{UserID: "u-bob", Account: "bob", SiteID: "site-b", EngName: "Bob Chen", ChineseName: "鮑勃"},
			},
		},
		{
			name:         "multiple mentions resolved",
			content:      "@alice and @bob",
			lookupUsers:  []model.User{aliceUser, bobUser},
			wantAccounts: []string{"alice", "bob"},
			wantParts: []model.Participant{
				{UserID: "u-alice", Account: "alice", SiteID: "site-a", EngName: "Alice Wang", ChineseName: "愛麗絲"},
				{UserID: "u-bob", Account: "bob", SiteID: "site-b", EngName: "Bob Chen", ChineseName: "鮑勃"},
			},
		},
```

The other cases (`@all only`, `@all and individual`, `unresolved account skipped`, `lookup error — partial result`) are unchanged — `@all` Participants intentionally have no `SiteID`.

- [ ] **Step 2.6: Run test to verify it fails**

Run: `make test SERVICE=pkg/mention`

Expected: `Resolve` returns Participants with empty `SiteID` — test diffs report missing `site-a` / `site-b`.

- [ ] **Step 2.7: Propagate SiteID in `mention.Resolve`**

In `pkg/mention/mention.go`, replace the loop that builds `result.Participants` from `users` (lines ~77-84):

```go
		for i := range users {
			result.Participants = append(result.Participants, model.Participant{
				UserID:      users[i].ID,
				Account:     users[i].Account,
				SiteID:      users[i].SiteID,
				ChineseName: users[i].ChineseName,
				EngName:     users[i].EngName,
			})
		}
```

- [ ] **Step 2.8: Run tests to verify they pass**

Run: `make test SERVICE=pkg/mention && make test SERVICE=pkg/model`

Expected: both packages pass.

- [ ] **Step 2.9: Verify no other consumer broke**

Run: `make test`

Expected: full unit test suite passes. (`Participant.SiteID` is omitempty and additive; existing fixtures that don't set it serialize identically.)

- [ ] **Step 2.10: Commit**

```bash
git add pkg/model/event.go pkg/model/model_test.go pkg/mention/mention.go pkg/mention/mention_test.go
git commit -m "model+mention: add SiteID to Participant, propagate from Resolve"
```

---

## Task 3: Add `siteID` and `publish` fields to message-worker `Handler`

This is a compile-only refactor. No new behavior — we just plumb the fields through `NewHandler` and update existing tests/main wiring. Behavior changes start in Task 4.

**Files:**
- Modify: `message-worker/handler.go:19-27` (Handler struct + NewHandler)
- Modify: `message-worker/handler_test.go` (every `NewHandler(...)` call)
- Modify: `message-worker/main.go:95` (the `NewHandler(...)` call site)
- Modify: `message-worker/integration_test.go` (every `NewHandler(...)` call site, if any)

- [ ] **Step 3.1: Update `Handler` struct and `NewHandler` constructor**

In `message-worker/handler.go`, replace lines 19-27 with:

```go
// PublishFunc publishes data; non-empty msgID sets Nats-Msg-Id for JetStream stream-level dedup.
// Mirrors room-worker's PublishFunc signature so message-worker can plug into the same publish closure.
type PublishFunc func(ctx context.Context, subj string, data []byte, msgID string) error

type Handler struct {
	store       Store
	userStore   userstore.UserStore
	threadStore ThreadStore
	siteID      string
	publish     PublishFunc
}

func NewHandler(store Store, userStore userstore.UserStore, threadStore ThreadStore, siteID string, publish PublishFunc) *Handler {
	return &Handler{
		store:       store,
		userStore:   userStore,
		threadStore: threadStore,
		siteID:      siteID,
		publish:     publish,
	}
}
```

- [ ] **Step 3.2: Update existing `NewHandler` call sites in tests**

In `message-worker/handler_test.go`, find every `NewHandler(mockStore, mockUserStore, mockThreadStore)` call (3 sites — `TestHandler_ProcessMessage`, `TestHandler_HandleThreadRoomAndSubscriptions`, `TestHandler_HandleJetStreamMsg`) and replace each with:

```go
				h := NewHandler(mockStore, mockUserStore, mockThreadStore, "site-a", func(_ context.Context, _ string, _ []byte, _ string) error {
					return nil
				})
```

Use exactly `"site-a"` — every existing test fixture uses that as both `User.SiteID` and `evt.SiteID`, so a no-op publish closure will be exercised but no remote-publish branch is taken yet (we add that in Task 4+).

- [ ] **Step 3.3: Update `main.go` `NewHandler` call**

In `message-worker/main.go` replace the line `handler := NewHandler(store, us, threadStore)` (~line 95) with:

```go
	handler := NewHandler(store, us, threadStore, cfg.SiteID, func(ctx context.Context, subj string, data []byte, msgID string) error {
		if msgID == "" {
			return nc.Publish(ctx, subj, data)
		}
		_, err := js.Publish(ctx, subj, data, jetstream.WithMsgID(msgID))
		return err
	})
```

- [ ] **Step 3.4: Update integration test `NewHandler` call sites if any**

Run: `grep -n "NewHandler(" message-worker/integration_test.go`

If results, update each call to pass `"site-a"` as siteID and a no-op publish closure (same shape as Step 3.2). If no results, skip this step.

- [ ] **Step 3.5: Verify build + unit tests**

Run: `make lint && make test SERVICE=message-worker`

Expected: pass. All existing tests still green — behavior unchanged.

- [ ] **Step 3.6: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go message-worker/main.go message-worker/integration_test.go
git commit -m "message-worker: plumb siteID and PublishFunc into Handler"
```

---

<!-- end-of-chunk-1 -->
