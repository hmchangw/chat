# NATS Event Timestamps Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Add `Timestamp int64` (Unix millis) to every NATS event struct, update all publishers, add a CLAUDE.md rule, and update tests.

**Architecture:** Add a `Timestamp` field to all 8 event structs in `pkg/model/event.go`. Update 9 publish sites across 7 services to set the field via `time.Now().UTC().UnixMilli()`. Add a CLAUDE.md rule under NATS and Messaging. Update all tests.

**Tech Stack:** Go, NATS JetStream, testify, go.uber.org/mock

**Spec:** `docs/superpowers/specs/2026-04-08-nats-event-timestamps-design.md`

---

## File Map

| Action | File | Purpose |
|--------|------|---------|
| Modify | `pkg/model/event.go` | Add/change `Timestamp int64` on all 8 event structs |
| Modify | `pkg/model/model_test.go` | Update round-trip tests for changed structs |
| Modify | `message-gatekeeper/handler.go:160` | Set `Timestamp` on `MessageEvent` |
| Modify | `message-gatekeeper/handler_test.go` | Assert `Timestamp > 0` on published `MessageEvent` |
| Modify | `broadcast-worker/handler.go:131-143` | Change `RoomEvent.Timestamp` from `time.Time` to `int64` |
| Modify | `broadcast-worker/handler_test.go` | Update `RoomEvent.Timestamp` assertions |
| Modify | `room-worker/handler.go` | Set `Timestamp` on `SubscriptionUpdateEvent`, `OutboxEvent`, `RoomMetadataUpdateEvent` |
| Modify | `room-worker/handler_test.go` | Assert `Timestamp > 0` on published events |
| Modify | `room-service/handler.go:152-159` | Set `Timestamp` on `InviteMemberRequest` before publishing |
| Modify | `room-service/handler_test.go` | Assert `Timestamp > 0` on published `InviteMemberRequest` |
| Modify | `inbox-worker/handler.go:78-84` | Set `Timestamp` on `SubscriptionUpdateEvent` |
| Modify | `inbox-worker/handler_test.go` | Assert `Timestamp > 0` on published events |
| Modify | `notification-worker/handler.go:46-51` | Set `Timestamp` on `NotificationEvent` |
| Modify | `notification-worker/handler_test.go` | Assert `Timestamp > 0` on published `NotificationEvent` |
| Modify | `pkg/roomkeysender/roomkeysender.go:27` | Set `Timestamp` on `RoomKeyEvent` |
| Modify | `pkg/roomkeysender/roomkeysender_test.go` | Assert `Timestamp > 0` on published `RoomKeyEvent` |
| Modify | `CLAUDE.md:197` | Add Event Timestamps rule after NATS and Messaging section |

---

### Task 1: Update event structs in `pkg/model/event.go`

**Files:**
- Modify: `pkg/model/event.go`
- Test: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing round-trip tests for new Timestamp fields**

Update `pkg/model/model_test.go` to include `Timestamp` in event test fixtures. Add tests for events that don't have round-trip tests yet (`NotificationEvent`, `SubscriptionUpdateEvent`, `InviteMemberRequest`, `OutboxEvent`, `RoomMetadataUpdateEvent`).

In `pkg/model/model_test.go`, add these test functions after `TestRoomKeyEventJSON`:

```go
func TestNotificationEventJSON(t *testing.T) {
	src := model.NotificationEvent{
		Type:   "new_message",
		RoomID: "room-1",
		Message: model.Message{
			ID: "m1", RoomID: "room-1", UserID: "u1", UserAccount: "alice",
			Content: "hello", CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		Timestamp: 1735689600000,
	}
	roundTrip(t, &src, &model.NotificationEvent{})
}

func TestSubscriptionUpdateEventJSON(t *testing.T) {
	src := model.SubscriptionUpdateEvent{
		UserID: "u1",
		Subscription: model.Subscription{
			ID:       "s1",
			User:     model.SubscriptionUser{ID: "u1", Account: "alice"},
			RoomID:   "r1",
			SiteID:   "site-a",
			Role:     model.RoleMember,
			JoinedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Action:    "added",
		Timestamp: 1735689600000,
	}
	roundTrip(t, &src, &model.SubscriptionUpdateEvent{})
}

func TestInviteMemberRequestJSON(t *testing.T) {
	src := model.InviteMemberRequest{
		InviterID:      "u1",
		InviteeID:      "u2",
		InviteeAccount: "bob",
		RoomID:         "r1",
		SiteID:         "site-a",
		Timestamp:      1735689600000,
	}
	roundTrip(t, &src, &model.InviteMemberRequest{})
}

func TestOutboxEventJSON(t *testing.T) {
	src := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-a",
		DestSiteID: "site-b",
		Payload:    []byte(`{"inviterId":"u1"}`),
		Timestamp:  1735689600000,
	}
	roundTrip(t, &src, &model.OutboxEvent{})
}

func TestRoomMetadataUpdateEventJSON(t *testing.T) {
	src := model.RoomMetadataUpdateEvent{
		RoomID:        "r1",
		Name:          "general",
		UserCount:     5,
		LastMessageAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Timestamp:     1735689600000,
	}
	roundTrip(t, &src, &model.RoomMetadataUpdateEvent{})
}
```

Update `TestMessageEventJSON` to include `Timestamp`:

```go
func TestMessageEventJSON(t *testing.T) {
	e := model.MessageEvent{
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content:   "hello",
			CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		SiteID:    "site-a",
		Timestamp: 1735689600000,
	}
	roundTrip(t, &e, &model.MessageEvent{})
}
```

Update `TestRoomEventJSON` — change `Timestamp` from `time.Time` to `int64` in both subtests:

In `"all fields populated"`:
```go
Timestamp:  now.UnixMilli(),
```

In `"nil message and empty mentions omitted"`:
```go
Timestamp: now.UnixMilli(),
```

Update `TestRoomKeyEventJSON` to include `Timestamp`:

```go
func TestRoomKeyEventJSON(t *testing.T) {
	src := model.RoomKeyEvent{
		RoomID:     "room-1",
		Version:    42,
		PublicKey:  []byte{0x04, 0x01, 0x02, 0x03},
		PrivateKey: []byte{0x0a, 0x0b, 0x0c},
		Timestamp:  1735689600000,
	}

	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dst model.RoomKeyEvent
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — `Timestamp` field does not exist on event structs yet.

- [ ] **Step 3: Add Timestamp field to all event structs**

In `pkg/model/event.go`, make these changes:

1. Remove the `"time"` import (no longer needed after `RoomEvent.Timestamp` becomes `int64`). Keep it if `RoomMetadataUpdateEvent` still uses `time.Time` for `LastMessageAt`/`UpdatedAt` — yes it does, so keep the import.

2. Add `Timestamp int64` to `MessageEvent`:
```go
type MessageEvent struct {
	Message   Message `json:"message"`
	SiteID    string  `json:"siteId"`
	Timestamp int64   `json:"timestamp" bson:"timestamp"`
}
```

3. Add `Timestamp int64` to `RoomMetadataUpdateEvent`:
```go
type RoomMetadataUpdateEvent struct {
	RoomID        string    `json:"roomId"`
	Name          string    `json:"name"`
	UserCount     int       `json:"userCount"`
	LastMessageAt time.Time `json:"lastMessageAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Timestamp     int64     `json:"timestamp" bson:"timestamp"`
}
```

4. Add `Timestamp int64` to `SubscriptionUpdateEvent`:
```go
type SubscriptionUpdateEvent struct {
	UserID       string       `json:"userId"`
	Subscription Subscription `json:"subscription"`
	Action       string       `json:"action"`
	Timestamp    int64        `json:"timestamp" bson:"timestamp"`
}
```

5. Add `Timestamp int64` to `InviteMemberRequest`:
```go
type InviteMemberRequest struct {
	InviterID      string `json:"inviterId"`
	InviteeID      string `json:"inviteeId"`
	InviteeAccount string `json:"inviteeAccount"`
	RoomID         string `json:"roomId"`
	SiteID         string `json:"siteId"`
	Timestamp      int64  `json:"timestamp" bson:"timestamp"`
}
```

6. Add `Timestamp int64` to `NotificationEvent`:
```go
type NotificationEvent struct {
	Type      string  `json:"type"`
	RoomID    string  `json:"roomId"`
	Message   Message `json:"message"`
	Timestamp int64   `json:"timestamp" bson:"timestamp"`
}
```

7. Add `Timestamp int64` to `OutboxEvent`:
```go
type OutboxEvent struct {
	Type       string `json:"type"`
	SiteID     string `json:"siteId"`
	DestSiteID string `json:"destSiteId"`
	Payload    []byte `json:"payload"`
	Timestamp  int64  `json:"timestamp" bson:"timestamp"`
}
```

8. Change `RoomEvent.Timestamp` from `time.Time` to `int64` and add `bson` tag:
```go
type RoomEvent struct {
	Type      RoomEventType `json:"type"`
	RoomID    string        `json:"roomId"`
	Timestamp int64         `json:"timestamp" bson:"timestamp"`
	// ... rest unchanged
}
```

9. Add `Timestamp int64` to `RoomKeyEvent`:
```go
type RoomKeyEvent struct {
	RoomID     string `json:"roomId"`
	Version    int    `json:"version"`
	PublicKey  []byte `json:"publicKey"`
	PrivateKey []byte `json:"privateKey"`
	Timestamp  int64  `json:"timestamp" bson:"timestamp"`
}
```

- [ ] **Step 4: Run model tests to verify they pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/model/event.go pkg/model/model_test.go
git commit -m "Add Timestamp int64 field to all NATS event structs"
```

---

### Task 2: Update `message-gatekeeper` publisher and tests

**Files:**
- Modify: `message-gatekeeper/handler.go:160`
- Modify: `message-gatekeeper/handler_test.go`

- [ ] **Step 1: Update test to assert Timestamp on published MessageEvent**

In `message-gatekeeper/handler_test.go`, update the `"happy path"` test case's `checkResult` function. After the existing assertions (around line 93), add:

```go
// Verify MessageEvent has Timestamp set
var evt model.MessageEvent
err = json.Unmarshal(published[0].data, &evt)
require.NoError(t, err)
assert.Greater(t, evt.Timestamp, int64(0))
```

Also update the `"happy path with thread parent"` test case's `checkResult` — after line 130 (`require.NoError(t, json.Unmarshal(published[0].data, &evt))`), add:

```go
assert.Greater(t, evt.Timestamp, int64(0))
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=message-gatekeeper`
Expected: FAIL — `evt.Timestamp` is `0` because the handler doesn't set it yet.

- [ ] **Step 3: Set Timestamp in handler**

In `message-gatekeeper/handler.go`, line 160, change:

```go
evt := model.MessageEvent{Message: msg, SiteID: siteID}
```

to:

```go
evt := model.MessageEvent{Message: msg, SiteID: siteID, Timestamp: now.UnixMilli()}
```

(`now` is already defined at line 148 as `time.Now().UTC()`)

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=message-gatekeeper`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add message-gatekeeper/handler.go message-gatekeeper/handler_test.go
git commit -m "Set Timestamp on MessageEvent in message-gatekeeper"
```

---

### Task 3: Update `broadcast-worker` publisher and tests

**Files:**
- Modify: `broadcast-worker/handler.go:131-143`
- Modify: `broadcast-worker/handler_test.go`

- [ ] **Step 1: Update tests to assert int64 Timestamp on RoomEvent**

In `broadcast-worker/handler_test.go`, the `TestHandler_HandleMessage_GroupRoom` test already decodes `RoomEvent`. After line 152 (`assert.Equal(t, "msg-1", evt.LastMsgID)`), add:

```go
assert.Greater(t, evt.Timestamp, int64(0))
```

In the `TestHandler_HandleMessage_DMRoom` test, after line 253 (`assert.Equal(t, model.RoomEventNewMessage, aliceEvt.Type)`), add:

```go
assert.Greater(t, aliceEvt.Timestamp, int64(0))
```

After line 262 (`require.NotNil(t, bobEvt.Message)`), add:

```go
assert.Greater(t, bobEvt.Timestamp, int64(0))
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=broadcast-worker`
Expected: Compilation error — `buildRoomEvent` sets `Timestamp: clientMsg.CreatedAt` which is `time.Time`, but `Timestamp` is now `int64`.

- [ ] **Step 3: Update `buildRoomEvent` to use int64 timestamp**

In `broadcast-worker/handler.go`, change `buildRoomEvent` (line 131-143):

```go
func buildRoomEvent(room *model.Room, clientMsg *model.ClientMessage) model.RoomEvent {
	return model.RoomEvent{
		Type:      model.RoomEventNewMessage,
		RoomID:    room.ID,
		Timestamp: time.Now().UTC().UnixMilli(),
		RoomName:  room.Name,
		RoomType:  room.Type,
		SiteID:    room.SiteID,
		UserCount: room.UserCount,
		LastMsgAt: clientMsg.CreatedAt,
		LastMsgID: clientMsg.ID,
		Message:   clientMsg,
	}
}
```

Add `"time"` to the imports if not already present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=broadcast-worker`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add broadcast-worker/handler.go broadcast-worker/handler_test.go
git commit -m "Change RoomEvent.Timestamp to int64 UnixMilli in broadcast-worker"
```

---

### Task 4: Update `room-worker` publisher and tests

**Files:**
- Modify: `room-worker/handler.go`
- Modify: `room-worker/handler_test.go`

- [ ] **Step 1: Update test to assert Timestamp on published events**

In `room-worker/handler_test.go`, after verifying published subjects (around line 62-76), add assertions for Timestamp on the published events. After the subject checks, add:

```go
// Verify all published events have Timestamp set
for _, p := range published {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(p.data, &raw); err != nil {
		t.Fatalf("unmarshal published data: %v", err)
	}
	if _, ok := raw["timestamp"]; !ok {
		t.Errorf("published event to %s missing timestamp field", p.subj)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=room-worker`
Expected: FAIL — events don't have `timestamp` field yet.

- [ ] **Step 3: Set Timestamp on all events in handler**

In `room-worker/handler.go`, the handler already has `now := time.Now().UTC()` at line 41. Make these changes:

1. At line 64-69, change the `OutboxEvent` construction to include `Timestamp`:
```go
outbox := model.OutboxEvent{
	Type:       "member_added",
	SiteID:     h.siteID,
	DestSiteID: req.SiteID,
	Payload:    data,
	Timestamp:  now.UnixMilli(),
}
```

2. At line 78, change the `SubscriptionUpdateEvent` to include `Timestamp`:
```go
subEvt := model.SubscriptionUpdateEvent{UserID: req.InviteeID, Subscription: sub, Action: "added", Timestamp: now.UnixMilli()}
```

3. At lines 87-92, change the `RoomMetadataUpdateEvent` to include `Timestamp`:
```go
metaEvt := model.RoomMetadataUpdateEvent{
	RoomID:    req.RoomID,
	Name:      room.Name,
	UserCount: room.UserCount,
	UpdatedAt: now,
	Timestamp: now.UnixMilli(),
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=room-worker`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add room-worker/handler.go room-worker/handler_test.go
git commit -m "Set Timestamp on OutboxEvent, SubscriptionUpdateEvent, and RoomMetadataUpdateEvent in room-worker"
```

---

### Task 5: Update `room-service` publisher and tests

**Files:**
- Modify: `room-service/handler.go:152-159`
- Modify: `room-service/handler_test.go`

- [ ] **Step 1: Update test to assert Timestamp on published InviteMemberRequest**

In `room-service/handler_test.go`, update `TestHandler_InviteOwner_Success`. After line 69 (`if jsPublished == nil`), add:

```go
// Verify the published InviteMemberRequest has a Timestamp set
var publishedReq model.InviteMemberRequest
if err := json.Unmarshal(jsPublished, &publishedReq); err != nil {
	t.Fatalf("unmarshal published request: %v", err)
}
if publishedReq.Timestamp <= 0 {
	t.Error("expected Timestamp > 0 on published InviteMemberRequest")
}
```

Also add `"time"` to imports if needed.

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=room-service`
Expected: FAIL — `publishedReq.Timestamp` is `0`.

- [ ] **Step 3: Set Timestamp on InviteMemberRequest before publishing**

In `room-service/handler.go`, the `handleInvite` method currently publishes the raw `data` it received (line 159). Since the request comes from the client without a timestamp, we need to unmarshal, set the timestamp, and re-marshal. Change lines 152-160:

```go
	// Only unmarshal after authorization checks pass
	var req model.InviteMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	// Set event timestamp
	req.Timestamp = time.Now().UTC().UnixMilli()

	timestampedData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal invite request: %w", err)
	}

	// Publish to ROOMS stream for room-worker processing
	if err := h.publishToStream(timestampedData); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=room-service`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add room-service/handler.go room-service/handler_test.go
git commit -m "Set Timestamp on InviteMemberRequest in room-service"
```

---

### Task 6: Update `inbox-worker` publisher and tests

**Files:**
- Modify: `inbox-worker/handler.go:78-84`
- Modify: `inbox-worker/handler_test.go`

- [ ] **Step 1: Update test to assert Timestamp on published SubscriptionUpdateEvent**

In `inbox-worker/handler_test.go`, in `TestHandleEvent_MemberAdded` (around line 155-167), after the existing assertions on `updateEvt`, add:

```go
if updateEvt.Timestamp <= 0 {
	t.Error("expected Timestamp > 0 on SubscriptionUpdateEvent")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=inbox-worker`
Expected: FAIL — `updateEvt.Timestamp` is `0`.

- [ ] **Step 3: Set Timestamp in handler**

In `inbox-worker/handler.go`, at line 78-82, change:

```go
updateEvt := model.SubscriptionUpdateEvent{
	UserID:       invite.InviteeID,
	Subscription: sub,
	Action:       "added",
	Timestamp:    now.UnixMilli(),
}
```

(`now` is already defined at line 63 as `time.Now()` — note: change it to `time.Now().UTC()` for consistency if it isn't already)

Actually, looking at line 63: `now := time.Now()` — this should be `time.Now().UTC()` for consistency with the convention. Change line 63:

```go
now := time.Now().UTC()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=inbox-worker`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add inbox-worker/handler.go inbox-worker/handler_test.go
git commit -m "Set Timestamp on SubscriptionUpdateEvent in inbox-worker"
```

---

### Task 7: Update `notification-worker` publisher and tests

**Files:**
- Modify: `notification-worker/handler.go:46-51`
- Modify: `notification-worker/handler_test.go`

- [ ] **Step 1: Update test to assert Timestamp on published NotificationEvent**

In `notification-worker/handler_test.go`, in `TestHandleMessage_FanOutSkipsSender` (around line 91-107), inside the loop over records, after the existing assertions on `notif`, add:

```go
if notif.Timestamp <= 0 {
	t.Errorf("expected Timestamp > 0 on NotificationEvent")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=notification-worker`
Expected: FAIL — `notif.Timestamp` is `0`.

- [ ] **Step 3: Set Timestamp in handler**

In `notification-worker/handler.go`, at lines 46-51, change:

```go
notif := model.NotificationEvent{
	Type:      "new_message",
	RoomID:    evt.Message.RoomID,
	Message:   evt.Message,
	Timestamp: time.Now().UTC().UnixMilli(),
}
```

Add `"time"` to the imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=notification-worker`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add notification-worker/handler.go notification-worker/handler_test.go
git commit -m "Set Timestamp on NotificationEvent in notification-worker"
```

---

### Task 8: Update `pkg/roomkeysender` publisher and tests

**Files:**
- Modify: `pkg/roomkeysender/roomkeysender.go:27`
- Modify: `pkg/roomkeysender/roomkeysender_test.go`

- [ ] **Step 1: Update test to assert Timestamp on published RoomKeyEvent**

In `pkg/roomkeysender/roomkeysender_test.go`, in the `"valid send"` and `"different user produces different subject"` test cases, the tests verify the payload round-trips correctly. After the existing assertions (around line 103-106), add:

```go
assert.Greater(t, got.Timestamp, int64(0))
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/roomkeysender`
Expected: FAIL — `got.Timestamp` is `0`.

- [ ] **Step 3: Set Timestamp in Send method**

In `pkg/roomkeysender/roomkeysender.go`, change the `Send` method (line 27-37) to set the timestamp before marshaling:

```go
func (s *Sender) Send(account string, evt *model.RoomKeyEvent) error {
	evt.Timestamp = time.Now().UTC().UnixMilli()
	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal room key event: %w", err)
	}
	subj := subject.RoomKeyUpdate(account)
	if err := s.pub.Publish(subj, data); err != nil {
		return fmt.Errorf("publish room key event: %w", err)
	}
	return nil
}
```

Add `"time"` to the imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/roomkeysender`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/roomkeysender/roomkeysender.go pkg/roomkeysender/roomkeysender_test.go
git commit -m "Set Timestamp on RoomKeyEvent in roomkeysender"
```

---

### Task 9: Add CLAUDE.md rule

**Files:**
- Modify: `CLAUDE.md:197` (after NATS and Messaging section, before NATS Subject Naming)

- [ ] **Step 1: Add Event Timestamps rule**

In `CLAUDE.md`, after line 197 (`- Use \`js.CreateOrUpdateStream\` at startup — it's idempotent`), add:

```markdown

### Event Timestamps
- Every NATS event struct in `pkg/model` must include a `Timestamp int64 \`json:"timestamp" bson:"timestamp"\`` field
- Set the timestamp at the publish site using `time.Now().UTC().UnixMilli()`
- This is the event-level timestamp (when the event was published), distinct from any domain-level timestamps in embedded structs (e.g., `Message.CreatedAt`)
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "Add Event Timestamps rule to CLAUDE.md"
```

---

### Task 10: Final verification

- [ ] **Step 1: Run full lint**

Run: `make lint`
Expected: PASS — no lint errors.

- [ ] **Step 2: Run full test suite**

Run: `make test`
Expected: PASS — all unit tests pass.

- [ ] **Step 3: Push**

```bash
git push -u origin claude/add-nats-event-timestamps-Sps6H
```
