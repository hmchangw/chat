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

## Task 4: Implement `publishThreadSubOutboxIfRemote` helper

The helper centralizes the local-vs-remote decision, payload marshalling, dedup-ID derivation, and publish call. It's used by Tasks 6, 7, 8.

**Files:**
- Modify: `message-worker/handler.go` (append helper near the bottom)
- Modify: `message-worker/handler_test.go` (append a new test function)

- [ ] **Step 4.1: Write the failing helper test**

Append to `message-worker/handler_test.go` (new top-level function, before the `fakeJSMsg` declaration):

```go
func TestHandler_PublishThreadSubOutboxIfRemote(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	baseSub := &model.ThreadSubscription{
		ID:              "sub-1",
		ParentMessageID: "pm-1",
		RoomID:          "r1",
		ThreadRoomID:    "tr-1",
		UserID:          "u-bob",
		UserAccount:     "bob",
		SiteID:          "site-b",
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	t.Run("same site — no publish", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		var called bool
		h := NewHandler(NewMockStore(ctrl), NewMockUserStore(ctrl), NewMockThreadStore(ctrl), "site-b",
			func(_ context.Context, _ string, _ []byte, _ string) error {
				called = true
				return nil
			})

		err := h.publishThreadSubOutboxIfRemote(context.Background(), baseSub, "msg-1")
		require.NoError(t, err)
		assert.False(t, called, "publish must not be called when sub.SiteID == h.siteID")
	})

	t.Run("empty siteID — skip with warn, no publish", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		var called bool
		h := NewHandler(NewMockStore(ctrl), NewMockUserStore(ctrl), NewMockThreadStore(ctrl), "site-a",
			func(_ context.Context, _ string, _ []byte, _ string) error {
				called = true
				return nil
			})

		emptySub := *baseSub
		emptySub.SiteID = ""
		err := h.publishThreadSubOutboxIfRemote(context.Background(), &emptySub, "msg-1")
		require.NoError(t, err)
		assert.False(t, called, "publish must not be called when sub.SiteID is empty")
	})

	t.Run("remote site — publishes with expected subject and dedup ID", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		var captured struct {
			subj    string
			data    []byte
			msgID   string
			callCnt int
		}
		h := NewHandler(NewMockStore(ctrl), NewMockUserStore(ctrl), NewMockThreadStore(ctrl), "site-a",
			func(_ context.Context, subj string, data []byte, msgID string) error {
				captured.subj = subj
				captured.data = data
				captured.msgID = msgID
				captured.callCnt++
				return nil
			})

		err := h.publishThreadSubOutboxIfRemote(context.Background(), baseSub, "msg-1")
		require.NoError(t, err)
		require.Equal(t, 1, captured.callCnt)
		assert.Equal(t, "outbox.site-a.to.site-b.thread_subscription_upserted", captured.subj)
		assert.NotEmpty(t, captured.msgID, "dedup ID must be set")

		// Same inputs → same dedup ID (stable across redeliveries).
		var second string
		h2 := NewHandler(NewMockStore(ctrl), NewMockUserStore(ctrl), NewMockThreadStore(ctrl), "site-a",
			func(_ context.Context, _ string, _ []byte, msgID string) error {
				second = msgID
				return nil
			})
		require.NoError(t, h2.publishThreadSubOutboxIfRemote(context.Background(), baseSub, "msg-1"))
		assert.Equal(t, captured.msgID, second, "dedup ID must be deterministic for the same (threadRoomID, userID, msgID) seed")

		// Different msgID → different dedup ID.
		var third string
		h3 := NewHandler(NewMockStore(ctrl), NewMockUserStore(ctrl), NewMockThreadStore(ctrl), "site-a",
			func(_ context.Context, _ string, _ []byte, msgID string) error {
				third = msgID
				return nil
			})
		require.NoError(t, h3.publishThreadSubOutboxIfRemote(context.Background(), baseSub, "msg-2"))
		assert.NotEqual(t, captured.msgID, third)

		// Payload is an OutboxEvent whose inner Payload decodes back to the ThreadSubscription.
		var outer model.OutboxEvent
		require.NoError(t, json.Unmarshal(captured.data, &outer))
		assert.Equal(t, model.OutboxThreadSubscriptionUpserted, outer.Type)
		assert.Equal(t, "site-a", outer.SiteID)
		assert.Equal(t, "site-b", outer.DestSiteID)
		assert.Greater(t, outer.Timestamp, int64(0))

		var inner model.ThreadSubscription
		require.NoError(t, json.Unmarshal(outer.Payload, &inner))
		assert.Equal(t, *baseSub, inner)
	})

	t.Run("publish error returned", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		boom := errors.New("publish boom")
		h := NewHandler(NewMockStore(ctrl), NewMockUserStore(ctrl), NewMockThreadStore(ctrl), "site-a",
			func(_ context.Context, _ string, _ []byte, _ string) error {
				return boom
			})

		err := h.publishThreadSubOutboxIfRemote(context.Background(), baseSub, "msg-1")
		require.Error(t, err)
		assert.ErrorIs(t, err, boom)
	})
}
```

- [ ] **Step 4.2: Run test to verify it fails**

Run: `make test SERVICE=message-worker`

Expected: compile error `h.publishThreadSubOutboxIfRemote undefined`.

- [ ] **Step 4.3: Implement the helper**

Append to `message-worker/handler.go` (after `markThreadMentions`, at end of file). Add `subject` to imports if not already present.

```go
// publishThreadSubOutboxIfRemote publishes a thread_subscription_upserted
// outbox event when sub.SiteID is a remote site. Same-site or empty SiteID
// is a no-op (empty SiteID logs a warning — it indicates a caller bug).
//
// The dedup-ID seed is (threadRoomID, userID, msgID): msg.ID is unique per
// reply, and (msg.ID, userID) is unique within a reply, so the seed is stable
// across MESSAGES_CANONICAL redeliveries and JetStream stream-level dedup
// absorbs duplicates within the dedup window.
func (h *Handler) publishThreadSubOutboxIfRemote(ctx context.Context, sub *model.ThreadSubscription, msgID string) error {
	if sub.SiteID == "" {
		slog.Warn("thread subscription has empty SiteID, skipping outbox publish",
			"threadRoomID", sub.ThreadRoomID, "userID", sub.UserID, "msgID", msgID)
		return nil
	}
	if sub.SiteID == h.siteID {
		return nil
	}

	payload, err := json.Marshal(sub)
	if err != nil {
		return fmt.Errorf("marshal thread subscription: %w", err)
	}
	outbox := model.OutboxEvent{
		Type:       model.OutboxThreadSubscriptionUpserted,
		SiteID:     h.siteID,
		DestSiteID: sub.SiteID,
		Payload:    payload,
		Timestamp:  time.Now().UTC().UnixMilli(),
	}
	data, err := json.Marshal(outbox)
	if err != nil {
		return fmt.Errorf("marshal outbox event: %w", err)
	}
	dedupID := idgen.DeriveID(fmt.Sprintf("thread-sub-outbox:%s:%s:%s", sub.ThreadRoomID, sub.UserID, msgID))
	subj := subject.Outbox(h.siteID, sub.SiteID, model.OutboxThreadSubscriptionUpserted)
	if err := h.publish(ctx, subj, data, dedupID); err != nil {
		return fmt.Errorf("publish thread subscription outbox to %s: %w", sub.SiteID, err)
	}
	return nil
}
```

If the file's import block doesn't already import `"github.com/hmchangw/chat/pkg/subject"`, add it.

- [ ] **Step 4.4: Run tests to verify they pass**

Run: `make test SERVICE=message-worker`

Expected: pass.

- [ ] **Step 4.5: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go
git commit -m "message-worker: add publishThreadSubOutboxIfRemote helper"
```

---

## Task 5: Switch `ThreadSubscription.SiteID` to owner-site, lookup parent siteID, warn-and-skip on parent user not found

Today `buildThreadSubscription` is called with the message-event siteID (the room's home site). For cross-site replication the field must reflect the **subscription owner's** home site. This task threads the owner siteID through the three callers and adds a parent-user lookup. No publishing yet — that's Tasks 6/7/8.

**Files:**
- Modify: `message-worker/handler.go:131-204` (handleFirstThreadReply, handleSubsequentThreadReply, buildThreadSubscription, markThreadMentions)
- Modify: `message-worker/handler.go:50-73` (carry replier user through to thread reply path)
- Modify: `message-worker/handler_test.go` (add `FindUserByID` mock expectation for parents in every thread-message test case)

- [ ] **Step 5.1: Update `buildThreadSubscription` to receive owner siteID explicitly**

In `message-worker/handler.go`, replace the existing `buildThreadSubscription` (around line 209-222):

```go
// buildThreadSubscription constructs a ThreadSubscription for (threadRoomID, userID).
// ownerSiteID is the home site of the subscription's owner — NOT the room's site —
// so the document round-trips correctly through the OUTBOX/INBOX federation.
// lastSeenAt is always nil; the field is owned by user-action paths, not message-worker.
func (h *Handler) buildThreadSubscription(msg *model.Message, threadRoomID, userID, userAccount, ownerSiteID string, now time.Time) *model.ThreadSubscription {
	return &model.ThreadSubscription{
		ID:              idgen.GenerateID(),
		ParentMessageID: msg.ThreadParentMessageID,
		RoomID:          msg.RoomID,
		ThreadRoomID:    threadRoomID,
		UserID:          userID,
		UserAccount:     userAccount,
		SiteID:          ownerSiteID,
		LastSeenAt:      nil,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}
```

(Only the parameter name changes from `siteID` → `ownerSiteID`; existing callers stop here being correct because they currently pass the message-event siteID. The next steps fix the callers.)

- [ ] **Step 5.2: Carry replier `*model.User` into the thread-reply path**

The current `processMessage` (lines ~57-92) discards `user` after building `cassParticipant`. We need its `SiteID` inside `handleFirstThreadReply` / `handleSubsequentThreadReply` / `markThreadMentions`. Update `processMessage` to pass `user` through.

Replace lines 75-92 of `message-worker/handler.go` with:

```go
	if evt.Message.ThreadParentMessageID != "" {
		// Resolve (or create) the thread room first so we have the threadRoomID
		// before persisting the message to Cassandra.
		threadRoomID, err := h.handleThreadRoomAndSubscriptions(ctx, &evt.Message, evt.SiteID, user)
		if err != nil {
			return fmt.Errorf("handle thread room and subscriptions: %w", err)
		}
		if err := h.markThreadMentions(ctx, &evt.Message, threadRoomID, evt.SiteID); err != nil {
			return fmt.Errorf("mark thread mentions: %w", err)
		}
		if err := h.store.SaveThreadMessage(ctx, &evt.Message, sender, evt.SiteID, threadRoomID); err != nil {
			return fmt.Errorf("save thread message: %w", err)
		}
	} else {
		if err := h.store.SaveMessage(ctx, &evt.Message, sender, evt.SiteID); err != nil {
			return fmt.Errorf("save message: %w", err)
		}
	}
```

- [ ] **Step 5.3: Update `handleThreadRoomAndSubscriptions` and the two reply handlers**

Replace `handleThreadRoomAndSubscriptions`, `handleFirstThreadReply`, and `handleSubsequentThreadReply` in `message-worker/handler.go` with the versions below. The `replier *model.User` arg flows in; parent's siteID is fetched via `userStore.FindUserByID` with warn-and-skip on `userstore.ErrUserNotFound`.

```go
// handleThreadRoomAndSubscriptions creates the ThreadRoom on first reply and
// inserts ThreadSubscriptions for the parent author and replier. On subsequent
// replies it upserts both subscriptions and bumps the room's last-message pointer.
// It returns the threadRoomID so the caller can pass it to SaveThreadMessage.
//
// `replier` may be nil for system messages with no real user (rare in thread
// paths); subscriptions for the replier are skipped in that case.
func (h *Handler) handleThreadRoomAndSubscriptions(ctx context.Context, msg *model.Message, eventSiteID string, replier *model.User) (string, error) {
	now := msg.CreatedAt

	threadRoom := model.ThreadRoom{
		ID:              idgen.GenerateID(),
		ParentMessageID: msg.ThreadParentMessageID,
		RoomID:          msg.RoomID,
		SiteID:          eventSiteID,
		LastMsgAt:       now,
		LastMsgID:       msg.ID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	err := h.threadStore.CreateThreadRoom(ctx, &threadRoom)
	switch {
	case err == nil:
		return threadRoom.ID, h.handleFirstThreadReply(ctx, msg, threadRoom.ID, replier, now)
	case errors.Is(err, errThreadRoomExists):
		return h.handleSubsequentThreadReply(ctx, msg, replier, now)
	default:
		return "", fmt.Errorf("create thread room: %w", err)
	}
}

// handleFirstThreadReply runs after the thread room has just been created.
// It inserts subscriptions for the parent author (looked up via userStore for
// the parent's home site) and, if distinct, for the replier (using the
// replier's home site).
func (h *Handler) handleFirstThreadReply(ctx context.Context, msg *model.Message, threadRoomID string, replier *model.User, now time.Time) error {
	parentSender, err := h.store.GetMessageSender(ctx, msg.ThreadParentMessageID)
	if err != nil {
		if errors.Is(err, errMessageNotFound) {
			slog.Warn("thread reply parent not found — skipping subscription creation",
				"parentMessageID", msg.ThreadParentMessageID,
				"replyID", msg.ID)
			return nil
		}
		return fmt.Errorf("get parent message sender: %w", err)
	}

	parentSiteID, parentLookupOK := h.lookupOwnerSiteID(ctx, parentSender.ID, "first-reply parent")
	if parentLookupOK {
		if err := h.threadStore.InsertThreadSubscription(ctx,
			h.buildThreadSubscription(msg, threadRoomID, parentSender.ID, parentSender.Account, parentSiteID, now),
		); err != nil {
			return fmt.Errorf("insert parent author thread subscription: %w", err)
		}
	}

	if replier != nil && msg.UserID != parentSender.ID {
		if err := h.threadStore.InsertThreadSubscription(ctx,
			h.buildThreadSubscription(msg, threadRoomID, msg.UserID, msg.UserAccount, replier.SiteID, now),
		); err != nil {
			return fmt.Errorf("insert replier thread subscription: %w", err)
		}
	}

	return nil
}

// handleSubsequentThreadReply runs when CreateThreadRoom reported an existing room.
// Upserts subscriptions for both the parent author and the replier (idempotent
// on redelivery), then bumps the room's last-message pointer. Returns the
// existing thread room ID so the caller can pass it to SaveThreadMessage.
func (h *Handler) handleSubsequentThreadReply(ctx context.Context, msg *model.Message, replier *model.User, now time.Time) (string, error) {
	existingRoom, err := h.threadStore.GetThreadRoomByParentMessageID(ctx, msg.ThreadParentMessageID)
	if err != nil {
		return "", fmt.Errorf("get existing thread room: %w", err)
	}

	parentSender, err := h.store.GetMessageSender(ctx, msg.ThreadParentMessageID)
	switch {
	case err == nil:
		parentSiteID, parentLookupOK := h.lookupOwnerSiteID(ctx, parentSender.ID, "subsequent-reply parent")
		if parentLookupOK {
			if err := h.threadStore.UpsertThreadSubscription(ctx,
				h.buildThreadSubscription(msg, existingRoom.ID, parentSender.ID, parentSender.Account, parentSiteID, now),
			); err != nil {
				return "", fmt.Errorf("upsert parent author thread subscription: %w", err)
			}
		}
		if replier != nil && msg.UserID != parentSender.ID {
			if err := h.threadStore.UpsertThreadSubscription(ctx,
				h.buildThreadSubscription(msg, existingRoom.ID, msg.UserID, msg.UserAccount, replier.SiteID, now),
			); err != nil {
				return "", fmt.Errorf("upsert replier thread subscription: %w", err)
			}
		}
	case errors.Is(err, errMessageNotFound):
		slog.Warn("thread reply parent not found — skipping parent subscription upsert",
			"parentMessageID", msg.ThreadParentMessageID,
			"replyID", msg.ID)
		if replier != nil {
			if err := h.threadStore.UpsertThreadSubscription(ctx,
				h.buildThreadSubscription(msg, existingRoom.ID, msg.UserID, msg.UserAccount, replier.SiteID, now),
			); err != nil {
				return "", fmt.Errorf("upsert replier thread subscription: %w", err)
			}
		}
	default:
		return "", fmt.Errorf("get parent message sender: %w", err)
	}

	if err := h.threadStore.UpdateThreadRoomLastMessage(ctx, existingRoom.ID, msg.ID, now); err != nil {
		return "", fmt.Errorf("update thread room last message: %w", err)
	}

	return existingRoom.ID, nil
}

// lookupOwnerSiteID resolves a user's home site by ID. Returns (siteID, true)
// on success. On userstore.ErrUserNotFound, logs a warning and returns
// ("", false) so callers can skip that user gracefully (parallels the
// errMessageNotFound branch already in this file). Other errors propagate.
func (h *Handler) lookupOwnerSiteID(ctx context.Context, userID, role string) (string, bool) {
	user, err := h.userStore.FindUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, userstore.ErrUserNotFound) {
			slog.Warn("owner user not found — skipping thread subscription",
				"userID", userID, "role", role)
			return "", false
		}
		// Non-NotFound errors propagate via the caller — return ok=false but
		// the next call to userStore from the caller will surface the error.
		// For a clean API we return the lookup error to the caller instead.
		// Match existing pattern: rewrap and panic-style would be wrong; instead
		// the helper returns ok=false, and we rely on callers to handle the
		// transient case. Simpler: change signature to also return error.
		_ = err
		return "", false
	}
	return user.SiteID, true
}
```

> **Note on `lookupOwnerSiteID` error semantics:** for transient DB errors we still return `("", false)`, which causes the call site to skip that user. That's wrong for a transient failure — we should propagate. Fix this by returning an error.

Replace the `lookupOwnerSiteID` body with a version that returns `(string, error)`:

```go
// lookupOwnerSiteID resolves a user's home site by ID. Returns
// ("", nil) when the user is not found (logs a warning) so callers can skip
// gracefully. Other DB errors are returned for the caller to NAK on.
func (h *Handler) lookupOwnerSiteID(ctx context.Context, userID, role string) (string, error) {
	user, err := h.userStore.FindUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, userstore.ErrUserNotFound) {
			slog.Warn("owner user not found — skipping thread subscription",
				"userID", userID, "role", role)
			return "", nil
		}
		return "", fmt.Errorf("lookup user %s: %w", userID, err)
	}
	return user.SiteID, nil
}
```

Then update the two callers in `handleFirstThreadReply` and `handleSubsequentThreadReply` to:

```go
	parentSiteID, err := h.lookupOwnerSiteID(ctx, parentSender.ID, "first-reply parent")
	if err != nil {
		return fmt.Errorf("lookup parent owner site: %w", err)
	}
	if parentSiteID != "" {
		// ... insert/upsert parent subscription ...
	}
```

Use `return "", fmt.Errorf(...)` in `handleSubsequentThreadReply` (the (string, error) signature).

- [ ] **Step 5.4: Update existing `markThreadMentions` to use `Participant.SiteID`**

Replace the existing `markThreadMentions` (around line 227-243 of `handler.go`) with:

```go
// markThreadMentions flips hasMention=true on the thread subscription of every
// @account mentionee in msg (auto-creating the subscription if absent). The
// sender is excluded, and @all is ignored at the thread level. The
// subscription's SiteID is the mentionee's home site (carried on Participant).
func (h *Handler) markThreadMentions(ctx context.Context, msg *model.Message, threadRoomID, eventSiteID string) error {
	for i := range msg.Mentions {
		p := &msg.Mentions[i]
		if p.Account == "all" {
			continue
		}
		if p.UserID == msg.UserID {
			continue
		}
		ownerSiteID := p.SiteID
		if ownerSiteID == "" {
			// Defensive: should not happen since mention.Resolve populates SiteID
			// for resolved users. Fall back to event site so the local mark still
			// happens; outbox publish (Task 8) will skip on empty SiteID anyway.
			slog.Warn("mentionee participant has empty SiteID, falling back to event site",
				"account", p.Account, "userID", p.UserID, "msgID", msg.ID)
			ownerSiteID = eventSiteID
		}
		sub := h.buildThreadSubscription(msg, threadRoomID, p.UserID, p.Account, ownerSiteID, msg.CreatedAt)
		sub.HasMention = true
		if err := h.threadStore.MarkThreadSubscriptionMention(ctx, sub); err != nil {
			return fmt.Errorf("mark thread subscription mention for user %s: %w", p.UserID, err)
		}
	}
	return nil
}
```

- [ ] **Step 5.5: Update existing handler tests with `FindUserByID` parent expectation**

In `message-worker/handler_test.go`, every thread-message test case in `TestHandler_ProcessMessage` and `TestHandler_HandleThreadRoomAndSubscriptions` now does an extra `userStore.FindUserByID(parentSender.ID)` lookup. Add the mock expectation just after the `GetMessageSender` mock in each thread test case.

For `TestHandler_ProcessMessage`, the affected cases are: `"thread message — calls SaveThreadMessage not SaveMessage"`, `"thread message save error — NAK after user lookup"`, `"thread reply mentioning non-participant — marks that user's subscription"`, `"thread reply where sender self-mentions — no MarkThreadSubscriptionMention call"`, `"thread reply with @all only — no MarkThreadSubscriptionMention call"`, `"thread reply with @all + @bob — only bob marked"`, `"thread reply mentioning non-participant — MarkThreadSubscriptionMention error is propagated"`. After each `store.EXPECT().GetMessageSender(...)` line, insert:

```go
					us.EXPECT().FindUserByID(gomock.Any(), "u-parent").
						Return(&model.User{ID: "u-parent", Account: "parent-user", SiteID: "site-a"}, nil)
```

For `TestHandler_HandleThreadRoomAndSubscriptions`, every case that calls `GetMessageSender` and returns a non-error/non-`errMessageNotFound` parentSender needs the same `FindUserByID` mock added. The `errMessageNotFound` cases skip the lookup entirely (the function returns before calling it).

Also: replace `h.handleThreadRoomAndSubscriptions(context.Background(), tt.msg, tt.siteID)` with `h.handleThreadRoomAndSubscriptions(context.Background(), tt.msg, tt.siteID, &model.User{ID: tt.msg.UserID, Account: tt.msg.UserAccount, SiteID: "site-a"})` to thread the replier through (the function signature changed in Step 5.3).

For each affected test in `TestHandler_ProcessMessage`, the `processMessage` entry point already calls `FindUserByID` for the replier — that mock is already set. Nothing more to add on the replier path.

- [ ] **Step 5.6: Add new test cases for parent user-not-found and DB-error**

Append two new test cases in `TestHandler_HandleThreadRoomAndSubscriptions`:

```go
		{
			name:   "first reply — parent user not found in userStore — warn-and-skip parent, still inserts replier",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").Return(parentSender, nil)
			},
			extraUserStoreSetup: func(us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-parent").
					Return(nil, fmt.Errorf("wrap: %w", userstore.ErrUserNotFound))
			},
			expectReplierInsert: true,
		},
		{
			name:   "first reply — parent user lookup DB error — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").Return(parentSender, nil)
			},
			extraUserStoreSetup: func(us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-parent").
					Return(nil, errors.New("mongo: connection refused"))
			},
			wantErr: true,
		},
```

To use `extraUserStoreSetup`, extend the test struct definition near the top of `TestHandler_HandleThreadRoomAndSubscriptions`:

```go
	tests := []struct {
		name                string
		msg                 *model.Message
		siteID              string
		setupMocks          func(store *MockStore, ts *MockThreadStore)
		extraUserStoreSetup func(us *MockUserStore)
		expectReplierInsert bool
		wantErr             bool
	}{
```

And in the run loop, after `tt.setupMocks(...)`, call:

```go
			if tt.extraUserStoreSetup != nil {
				tt.extraUserStoreSetup(mockUserStore)
			}
			if tt.expectReplierInsert {
				mockThreadStore.EXPECT().InsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
			}
```

Add `"github.com/hmchangw/chat/pkg/userstore"` to the test file's imports if not already present.

- [ ] **Step 5.7: Add `errors` import if missing**

Verify `message-worker/handler.go` imports `"github.com/hmchangw/chat/pkg/userstore"` (it already does — used by `Handler.userStore`). No new imports needed beyond what Task 4 added (`subject`).

- [ ] **Step 5.8: Run tests to verify they pass**

Run: `make generate SERVICE=message-worker && make test SERVICE=message-worker`

Expected: pass. (`make generate` regenerates mocks if any interface signature changed.)

- [ ] **Step 5.9: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go message-worker/mock_store_test.go message-worker/mock_userstore_test.go
git commit -m "message-worker: ThreadSubscription.SiteID = owner site; lookup parent siteID"
```

---

## Task 6: Wire outbox publish into `handleFirstThreadReply`

After each `InsertThreadSubscription` succeeds, call `publishThreadSubOutboxIfRemote`. The helper short-circuits same-site, so local-only setups remain quiet.

**Files:**
- Modify: `message-worker/handler.go` (handleFirstThreadReply body — only the post-insert lines)
- Modify: `message-worker/handler_test.go` (new test cases)

- [ ] **Step 6.1: Write the failing test for first-reply remote publish**

Append to `message-worker/handler_test.go` (a new top-level test, not part of the existing tables — having a dedicated function keeps the publish-recording wiring isolated):

```go
func TestHandler_FirstReply_OutboxPublishes(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	parentSender := &cassParticipant{ID: "u-parent", Account: "parent-user"}
	parentUserAtA := &model.User{ID: "u-parent", Account: "parent-user", SiteID: "site-a"}
	parentUserAtC := &model.User{ID: "u-parent", Account: "parent-user", SiteID: "site-c"}

	type publishCall struct {
		subj  string
		data  []byte
		msgID string
	}

	tests := []struct {
		name              string
		replierSite       string
		parentUser        *model.User
		wantPublishToSite map[string]int // destSite → expected count
	}{
		{
			name:              "both local — no publish",
			replierSite:       "site-a",
			parentUser:        parentUserAtA,
			wantPublishToSite: map[string]int{},
		},
		{
			name:              "replier remote — one publish to replier site",
			replierSite:       "site-b",
			parentUser:        parentUserAtA,
			wantPublishToSite: map[string]int{"site-b": 1},
		},
		{
			name:              "parent remote — one publish to parent site",
			replierSite:       "site-a",
			parentUser:        parentUserAtC,
			wantPublishToSite: map[string]int{"site-c": 1},
		},
		{
			name:              "both remote, different sites — two publishes",
			replierSite:       "site-b",
			parentUser:        parentUserAtC,
			wantPublishToSite: map[string]int{"site-b": 1, "site-c": 1},
		},
		{
			name:              "both remote, same site — two publishes to that site",
			replierSite:       "site-b",
			parentUser:        &model.User{ID: "u-parent", Account: "parent-user", SiteID: "site-b"},
			wantPublishToSite: map[string]int{"site-b": 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			ts := NewMockThreadStore(ctrl)

			store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").Return(parentSender, nil)
			us.EXPECT().FindUserByID(gomock.Any(), "u-parent").Return(tt.parentUser, nil)
			ts.EXPECT().InsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
			ts.EXPECT().InsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)

			var calls []publishCall
			h := NewHandler(store, us, ts, "site-a", func(_ context.Context, subj string, data []byte, msgID string) error {
				calls = append(calls, publishCall{subj: subj, data: data, msgID: msgID})
				return nil
			})

			replier := &model.User{ID: "u-replier", Account: "replier", SiteID: tt.replierSite}
			msg := &model.Message{
				ID:                    "msg-reply",
				RoomID:                "r1",
				UserID:                "u-replier",
				UserAccount:           "replier",
				CreatedAt:             now,
				ThreadParentMessageID: "msg-parent",
			}

			err := h.handleFirstThreadReply(context.Background(), msg, "tr-1", replier, now)
			require.NoError(t, err)

			gotByDest := map[string]int{}
			for _, c := range calls {
				var outer model.OutboxEvent
				require.NoError(t, json.Unmarshal(c.data, &outer))
				assert.Equal(t, model.OutboxThreadSubscriptionUpserted, outer.Type)
				gotByDest[outer.DestSiteID]++
			}
			assert.Equal(t, tt.wantPublishToSite, gotByDest)
		})
	}
}

func TestHandler_FirstReply_OutboxPublishError_NAKs(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	ts := NewMockThreadStore(ctrl)

	store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
		Return(&cassParticipant{ID: "u-parent", Account: "parent-user"}, nil)
	us.EXPECT().FindUserByID(gomock.Any(), "u-parent").
		Return(&model.User{ID: "u-parent", Account: "parent-user", SiteID: "site-c"}, nil)
	ts.EXPECT().InsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
	// Replier insert never reached because parent-publish fails first.

	boom := errors.New("publish boom")
	h := NewHandler(store, us, ts, "site-a", func(_ context.Context, _ string, _ []byte, _ string) error {
		return boom
	})

	msg := &model.Message{
		ID: "msg-reply", RoomID: "r1", UserID: "u-replier", UserAccount: "replier",
		CreatedAt: now, ThreadParentMessageID: "msg-parent",
	}
	err := h.handleFirstThreadReply(context.Background(), msg,
		"tr-1", &model.User{ID: "u-replier", SiteID: "site-b"}, now)
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
}
```

- [ ] **Step 6.2: Run tests to verify they fail**

Run: `make test SERVICE=message-worker -run TestHandler_FirstReply_Outbox`

Expected: FAIL — current handler never calls publish.

- [ ] **Step 6.3: Wire the publish call into `handleFirstThreadReply`**

In `message-worker/handler.go`, replace the body of `handleFirstThreadReply` (the version from Task 5.3) so that each successful insert is followed by an outbox publish:

```go
func (h *Handler) handleFirstThreadReply(ctx context.Context, msg *model.Message, threadRoomID string, replier *model.User, now time.Time) error {
	parentSender, err := h.store.GetMessageSender(ctx, msg.ThreadParentMessageID)
	if err != nil {
		if errors.Is(err, errMessageNotFound) {
			slog.Warn("thread reply parent not found — skipping subscription creation",
				"parentMessageID", msg.ThreadParentMessageID,
				"replyID", msg.ID)
			return nil
		}
		return fmt.Errorf("get parent message sender: %w", err)
	}

	parentSiteID, err := h.lookupOwnerSiteID(ctx, parentSender.ID, "first-reply parent")
	if err != nil {
		return fmt.Errorf("lookup parent owner site: %w", err)
	}
	if parentSiteID != "" {
		parentSub := h.buildThreadSubscription(msg, threadRoomID, parentSender.ID, parentSender.Account, parentSiteID, now)
		if err := h.threadStore.InsertThreadSubscription(ctx, parentSub); err != nil {
			return fmt.Errorf("insert parent author thread subscription: %w", err)
		}
		if err := h.publishThreadSubOutboxIfRemote(ctx, parentSub, msg.ID); err != nil {
			return err
		}
	}

	if replier != nil && msg.UserID != parentSender.ID {
		replierSub := h.buildThreadSubscription(msg, threadRoomID, msg.UserID, msg.UserAccount, replier.SiteID, now)
		if err := h.threadStore.InsertThreadSubscription(ctx, replierSub); err != nil {
			return fmt.Errorf("insert replier thread subscription: %w", err)
		}
		if err := h.publishThreadSubOutboxIfRemote(ctx, replierSub, msg.ID); err != nil {
			return err
		}
	}

	return nil
}
```

- [ ] **Step 6.4: Run tests to verify they pass**

Run: `make test SERVICE=message-worker`

Expected: pass — new tests green and existing tests still green (existing fixtures use `site-a` everywhere so no remote publishes fire).

- [ ] **Step 6.5: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go
git commit -m "message-worker: emit outbox event on first-reply ThreadSubscription inserts"
```

---

## Task 7: Wire outbox publish into `handleSubsequentThreadReply`

Same pattern as Task 6 but on the upsert path. Subsequent replies always upsert both subscriptions (idempotent on redelivery), so we publish after each successful upsert.

**Files:**
- Modify: `message-worker/handler.go` (handleSubsequentThreadReply body)
- Modify: `message-worker/handler_test.go` (new test cases)

- [ ] **Step 7.1: Write the failing test for subsequent-reply remote publish**

Append to `message-worker/handler_test.go`:

```go
func TestHandler_SubsequentReply_OutboxPublishes(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	parentSender := &cassParticipant{ID: "u-parent", Account: "parent-user"}
	parentUserAtA := &model.User{ID: "u-parent", Account: "parent-user", SiteID: "site-a"}
	parentUserAtC := &model.User{ID: "u-parent", Account: "parent-user", SiteID: "site-c"}

	tests := []struct {
		name              string
		replierSite       string
		parentUser        *model.User
		wantPublishToSite map[string]int
	}{
		{
			name:              "both local — no publish",
			replierSite:       "site-a",
			parentUser:        parentUserAtA,
			wantPublishToSite: map[string]int{},
		},
		{
			name:              "replier remote — one publish",
			replierSite:       "site-b",
			parentUser:        parentUserAtA,
			wantPublishToSite: map[string]int{"site-b": 1},
		},
		{
			name:              "parent remote — one publish",
			replierSite:       "site-a",
			parentUser:        parentUserAtC,
			wantPublishToSite: map[string]int{"site-c": 1},
		},
		{
			name:              "both remote, different sites — two publishes",
			replierSite:       "site-b",
			parentUser:        parentUserAtC,
			wantPublishToSite: map[string]int{"site-b": 1, "site-c": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			ts := NewMockThreadStore(ctrl)

			ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-parent").
				Return(&model.ThreadRoom{ID: "tr-existing"}, nil)
			store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").Return(parentSender, nil)
			us.EXPECT().FindUserByID(gomock.Any(), "u-parent").Return(tt.parentUser, nil)
			ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
			ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
			ts.EXPECT().UpdateThreadRoomLastMessage(gomock.Any(), "tr-existing", "msg-reply", now).Return(nil)

			var publishedDests []string
			h := NewHandler(store, us, ts, "site-a", func(_ context.Context, _ string, data []byte, _ string) error {
				var outer model.OutboxEvent
				if err := json.Unmarshal(data, &outer); err != nil {
					return err
				}
				publishedDests = append(publishedDests, outer.DestSiteID)
				return nil
			})

			replier := &model.User{ID: "u-replier", Account: "replier", SiteID: tt.replierSite}
			msg := &model.Message{
				ID:                    "msg-reply",
				RoomID:                "r1",
				UserID:                "u-replier",
				UserAccount:           "replier",
				CreatedAt:             now,
				ThreadParentMessageID: "msg-parent",
			}

			roomID, err := h.handleSubsequentThreadReply(context.Background(), msg, replier, now)
			require.NoError(t, err)
			assert.Equal(t, "tr-existing", roomID)

			gotByDest := map[string]int{}
			for _, d := range publishedDests {
				gotByDest[d]++
			}
			assert.Equal(t, tt.wantPublishToSite, gotByDest)
		})
	}
}

func TestHandler_SubsequentReply_OutboxPublishError_NAKs(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	ts := NewMockThreadStore(ctrl)

	ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-parent").
		Return(&model.ThreadRoom{ID: "tr-1"}, nil)
	store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
		Return(&cassParticipant{ID: "u-parent", Account: "parent-user"}, nil)
	us.EXPECT().FindUserByID(gomock.Any(), "u-parent").
		Return(&model.User{ID: "u-parent", SiteID: "site-c"}, nil)
	ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)

	boom := errors.New("publish boom")
	h := NewHandler(store, us, ts, "site-a", func(_ context.Context, _ string, _ []byte, _ string) error {
		return boom
	})

	msg := &model.Message{
		ID: "msg-reply", RoomID: "r1", UserID: "u-replier", UserAccount: "replier",
		CreatedAt: now, ThreadParentMessageID: "msg-parent",
	}
	_, err := h.handleSubsequentThreadReply(context.Background(), msg,
		&model.User{ID: "u-replier", SiteID: "site-b"}, now)
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
}
```

- [ ] **Step 7.2: Run tests to verify they fail**

Run: `make test SERVICE=message-worker -run TestHandler_SubsequentReply_Outbox`

Expected: FAIL — no publish happens yet on the upsert path.

- [ ] **Step 7.3: Wire the publish call into `handleSubsequentThreadReply`**

Replace `handleSubsequentThreadReply` in `message-worker/handler.go` with:

```go
func (h *Handler) handleSubsequentThreadReply(ctx context.Context, msg *model.Message, replier *model.User, now time.Time) (string, error) {
	existingRoom, err := h.threadStore.GetThreadRoomByParentMessageID(ctx, msg.ThreadParentMessageID)
	if err != nil {
		return "", fmt.Errorf("get existing thread room: %w", err)
	}

	parentSender, err := h.store.GetMessageSender(ctx, msg.ThreadParentMessageID)
	switch {
	case err == nil:
		parentSiteID, lookupErr := h.lookupOwnerSiteID(ctx, parentSender.ID, "subsequent-reply parent")
		if lookupErr != nil {
			return "", fmt.Errorf("lookup parent owner site: %w", lookupErr)
		}
		if parentSiteID != "" {
			parentSub := h.buildThreadSubscription(msg, existingRoom.ID, parentSender.ID, parentSender.Account, parentSiteID, now)
			if err := h.threadStore.UpsertThreadSubscription(ctx, parentSub); err != nil {
				return "", fmt.Errorf("upsert parent author thread subscription: %w", err)
			}
			if err := h.publishThreadSubOutboxIfRemote(ctx, parentSub, msg.ID); err != nil {
				return "", err
			}
		}
		if replier != nil && msg.UserID != parentSender.ID {
			replierSub := h.buildThreadSubscription(msg, existingRoom.ID, msg.UserID, msg.UserAccount, replier.SiteID, now)
			if err := h.threadStore.UpsertThreadSubscription(ctx, replierSub); err != nil {
				return "", fmt.Errorf("upsert replier thread subscription: %w", err)
			}
			if err := h.publishThreadSubOutboxIfRemote(ctx, replierSub, msg.ID); err != nil {
				return "", err
			}
		}
	case errors.Is(err, errMessageNotFound):
		slog.Warn("thread reply parent not found — skipping parent subscription upsert",
			"parentMessageID", msg.ThreadParentMessageID,
			"replyID", msg.ID)
		if replier != nil {
			replierSub := h.buildThreadSubscription(msg, existingRoom.ID, msg.UserID, msg.UserAccount, replier.SiteID, now)
			if err := h.threadStore.UpsertThreadSubscription(ctx, replierSub); err != nil {
				return "", fmt.Errorf("upsert replier thread subscription: %w", err)
			}
			if err := h.publishThreadSubOutboxIfRemote(ctx, replierSub, msg.ID); err != nil {
				return "", err
			}
		}
	default:
		return "", fmt.Errorf("get parent message sender: %w", err)
	}

	if err := h.threadStore.UpdateThreadRoomLastMessage(ctx, existingRoom.ID, msg.ID, now); err != nil {
		return "", fmt.Errorf("update thread room last message: %w", err)
	}

	return existingRoom.ID, nil
}
```

- [ ] **Step 7.4: Run tests to verify they pass**

Run: `make test SERVICE=message-worker`

Expected: pass.

- [ ] **Step 7.5: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go
git commit -m "message-worker: emit outbox event on subsequent-reply ThreadSubscription upserts"
```

---

<!-- end-of-chunk-3 -->
