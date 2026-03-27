# System-Wide Username Subject Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate all NATS `chat.user.{userID}.*` subject patterns to use `{username}` instead, and update store queries from `u._id` to `u.username`.

**Architecture:** Rename subject builder parameters from `userID` to `username` (format strings unchanged). Update `GetSubscription` store interfaces/implementations to query by `u.username`. Services that need `userID` (e.g. for `Message.UserID`) get it from `sub.User.ID`. Add `InviteeUsername` and `CreatedByUsername` to request models so downstream services can populate subscription usernames and route to correct subjects.

**Tech Stack:** Go 1.24, NATS JetStream, MongoDB (`go.mongodb.org/mongo-driver/v2`), `go.uber.org/mock` (mockgen), `stretchr/testify`

**Spec:** `docs/superpowers/specs/2026-03-27-system-wide-username-subjects-design.md`

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `pkg/subject/subject.go` | Modify | Rename all `userID` params to `username` |
| `pkg/subject/subject_test.go` | Modify | Update test values to use usernames |
| `pkg/model/room.go` | Modify | Add `CreatedByUsername` to `CreateRoomRequest` |
| `pkg/model/event.go` | Modify | Add `InviteeUsername` to `InviteMemberRequest` |
| `message-worker/store.go` | Modify | Rename `GetSubscription` param to `username` |
| `message-worker/store_mongo.go` | Modify | Rename param, change filter `u._id` → `u.username` |
| `message-worker/handler.go` | Modify | Extract username from subject, use `sub.User.ID` for Message |
| `message-worker/handler_test.go` | Modify | Update mock expectations and assertions |
| `history-service/store.go` | Modify | Rename `GetSubscription` param to `username` |
| `history-service/store_real.go` | Modify | Rename param, change filter `u._id` → `u.username` |
| `history-service/handler.go` | Modify | Rename `userID` to `username` |
| `history-service/handler_test.go` | Modify | Update mock expectations |
| `room-service/store.go` | Modify | Rename `GetSubscription` param to `username` |
| `room-service/store_mongo.go` | Modify | Rename param, change filter `u._id` → `u.username` |
| `room-service/handler.go` | Modify | Use `inviterUsername`, populate `Username` in subscription |
| `room-service/handler_test.go` | Modify | Update mock expectations |
| `room-worker/handler.go` | Modify | Use `req.InviteeUsername`, `sub.User.Username` for subjects |
| `room-worker/handler_test.go` | Modify | Add `Username` to fixtures |
| `notification-worker/handler.go` | Modify | Use `sub.User.Username` for Notification subject |
| `notification-worker/handler_test.go` | Modify | Add `Username` to fixtures |
| `inbox-worker/handler.go` | Modify | Use `invite.InviteeUsername` for SubscriptionUpdate subject |
| `inbox-worker/handler_test.go` | Modify | Add `InviteeUsername` to test data |

---

### Task 1: Update Request Models and Subject Builders

**Files:**
- Modify: `pkg/model/room.go` — add `CreatedByUsername` to `CreateRoomRequest`
- Modify: `pkg/model/event.go` — add `InviteeUsername` to `InviteMemberRequest`
- Modify: `pkg/subject/subject.go` — rename all `userID` params to `username`
- Modify: `pkg/subject/subject_test.go` — update test values from `"u1"` to `"alice"`

- [ ] **Step 1: Update `pkg/subject/subject_test.go` (Red phase)**

In `TestSubjectBuilders`, change all `"u1"` to `"alice"` for user-scoped subjects and update expected strings accordingly (e.g. `chat.user.u1.room.r1...` → `chat.user.alice.room.r1...`). In `TestParseUserRoomSubject`, rename `wantUserID`/`uid` to `wantUsername`/`username`.

- [ ] **Step 2: Run tests — expect failure**

```bash
make test SERVICE=pkg/subject
```

- [ ] **Step 3: Rename all `userID` params to `username` in `pkg/subject/subject.go`**

Rename parameter in every function: `MsgSend`, `UserResponse`, `UserRoomUpdate`, `UserMsgStream`, `MemberInvite`, `MsgHistory`, `SubscriptionUpdate`, `RoomMetadataChanged`, `Notification`, `RoomsCreate`, `RoomsList`, `RoomsGet`. Rename `ParseUserRoomSubject` return from `userID` to `username`. Format strings are UNCHANGED — only Go parameter names change.

- [ ] **Step 4: Add `CreatedByUsername` to `CreateRoomRequest` in `pkg/model/room.go`**

```go
type CreateRoomRequest struct {
	Name              string   `json:"name"`
	Type              RoomType `json:"type"`
	CreatedBy         string   `json:"createdBy"`
	CreatedByUsername string   `json:"createdByUsername"`
	SiteID            string   `json:"siteId"`
	Members           []string `json:"members,omitempty"`
}
```

- [ ] **Step 5: Add `InviteeUsername` to `InviteMemberRequest` in `pkg/model/event.go`**

```go
type InviteMemberRequest struct {
	InviterID       string `json:"inviterId"`
	InviteeID       string `json:"inviteeId"`
	InviteeUsername string `json:"inviteeUsername"`
	RoomID          string `json:"roomId"`
	SiteID          string `json:"siteId"`
}
```

- [ ] **Step 6: Run tests**

```bash
make test
```

- [ ] **Step 7: Lint and commit**

```bash
make fmt && make lint
git add pkg/subject/subject.go pkg/subject/subject_test.go pkg/model/room.go pkg/model/event.go
git commit -m "refactor: rename userID to username in subject builders, add username fields to request models"
```

---

### Task 2: Update Store Interfaces and MongoDB Queries

**Files:**
- Modify: `message-worker/store.go`, `message-worker/store_mongo.go`
- Modify: `history-service/store.go`, `history-service/store_real.go`
- Modify: `room-service/store.go`, `room-service/store_mongo.go`

- [ ] **Step 1: Rename `GetSubscription` parameter from `userID` to `username` in all 3 store interfaces**

In each `store.go`:
```go
// Before
GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
// After
GetSubscription(ctx context.Context, username, roomID string) (*model.Subscription, error)
```

- [ ] **Step 2: Rename parameter and change MongoDB filter in all 3 implementations**

In each `store_mongo.go` / `store_real.go`:
```go
// Before
func (s *MongoStore) GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error) {
	filter := bson.M{"u._id": userID, "roomId": roomID}
// After
func (s *MongoStore) GetSubscription(ctx context.Context, username, roomID string) (*model.Subscription, error) {
	filter := bson.M{"u.username": username, "roomId": roomID}
```

- [ ] **Step 3: Regenerate mocks**

```bash
make generate SERVICE=message-worker
make generate SERVICE=history-service
make generate SERVICE=room-service
```

- [ ] **Step 4: Commit (tests will fail until callers are updated in Tasks 3-5)**

```bash
git add message-worker/store.go message-worker/store_mongo.go message-worker/mock_store_test.go \
       history-service/store.go history-service/store_real.go history-service/mock_store_test.go \
       room-service/store.go room-service/store_mongo.go room-service/mock_store_test.go
git commit -m "refactor(stores): rename GetSubscription userID to username, query u.username"
```

---

### Task 3: Update message-worker to use username

**Files:**
- Modify: `message-worker/handler.go`
- Modify: `message-worker/handler_test.go`

- [ ] **Step 1: Update `handler_test.go` (Red phase)**

Change `GetSubscription` mock: `"u1"` → `"alice"`. Return subscription with `Username: "alice"`. Call `processMessage(ctx, "alice", "r1", "site-a", data)`. Verify `msg.UserID == "u1"` (from `sub.User.ID`, NOT from username param).

```go
store.EXPECT().
	GetSubscription(gomock.Any(), "alice", "r1").
	Return(&model.Subscription{
		User: model.SubscriptionUser{ID: "u1", Username: "alice"}, RoomID: "r1", Role: model.RoleMember,
	}, nil)
```

Call: `h.processMessage(context.Background(), "alice", "r1", "site-a", data)`

Assert: `msg.UserID == "u1"` and `msg.Content == "hello"`

Same pattern for `TestHandler_ProcessMessage_NotSubscribed` — pass `"alice"`.

- [ ] **Step 2: Run tests — expect failure**

```bash
make test SERVICE=message-worker
```

- [ ] **Step 3: Update `handler.go`**

In `HandleJetStreamMsg`: rename `userID := parts[2]` to `username := parts[2]`. Pass `username` to `processMessage` and `subject.UserResponse(username, reqID)`.

In `processMessage`: change param from `userID` to `username`. Capture subscription: `sub, err := h.store.GetSubscription(ctx, username, roomID)` (was `_, err`). Use `sub.User.ID` for `Message.UserID`:

```go
msg := model.Message{
	ID:        uuid.New().String(),
	RoomID:    roomID,
	UserID:    sub.User.ID,  // was: userID
	Content:   req.Content,
	CreatedAt: now,
}
```

- [ ] **Step 4: Run tests**

```bash
make test SERVICE=message-worker
```

- [ ] **Step 5: Lint and commit**

```bash
make fmt && make lint
git add message-worker/handler.go message-worker/handler_test.go
git commit -m "feat(message-worker): use username from subject, derive UserID from subscription"
```

---

### Task 4: Update history-service to use username

**Files:**
- Modify: `history-service/handler.go`
- Modify: `history-service/handler_test.go`

- [ ] **Step 1: Update `handler_test.go` (Red phase)**

Change `GetSubscription` mocks: `"u1"` → `"alice"`. Return subscription with `Username: "alice"`.

- [ ] **Step 2: Run tests — expect failure**

```bash
make test SERVICE=history-service
```

- [ ] **Step 3: Update `handler.go`**

Rename `userID` to `username` where extracted from `ParseUserRoomSubject`. Pass `username` to `GetSubscription`:

```go
username, roomID, ok := subject.ParseUserRoomSubject(subj)
// ...
sub, err := h.store.GetSubscription(ctx, username, roomID)
```

- [ ] **Step 4: Run tests**

```bash
make test SERVICE=history-service
```

- [ ] **Step 5: Lint and commit**

```bash
make fmt && make lint
git add history-service/handler.go history-service/handler_test.go
git commit -m "feat(history-service): pass username to GetSubscription"
```

---

### Task 5: Update room-service to use username

**Files:**
- Modify: `room-service/handler.go`
- Modify: `room-service/handler_test.go`

- [ ] **Step 1: Update `handler_test.go` (Red phase)**

Change `GetSubscription` mocks: `"u1"` → `"alice"`, `"u2"` → `"bob"`. Return subscriptions with `Username`. Update `InviteMemberRequest` fixtures to include `InviteeUsername`. Update `subject.MemberInvite` calls to pass `"alice"` instead of `"u1"`. For `handleCreateRoom` test, add `CreatedByUsername: "alice"` and assert created subscription has `User.Username == "alice"`.

- [ ] **Step 2: Run tests — expect failure**

```bash
make test SERVICE=room-service
```

- [ ] **Step 3: Update `handler.go`**

In `handleInvite`: rename `inviterID` → `inviterUsername`, pass to `GetSubscription`.

In `handleCreateRoom`: populate `Username` on owner subscription:
```go
User: model.SubscriptionUser{ID: req.CreatedBy, Username: req.CreatedByUsername},
```

- [ ] **Step 4: Run tests**

```bash
make test SERVICE=room-service
```

- [ ] **Step 5: Lint and commit**

```bash
make fmt && make lint
git add room-service/handler.go room-service/handler_test.go
git commit -m "feat(room-service): use username for invite auth and owner subscription"
```

---

### Task 6: Update room-worker to use username

**Files:**
- Modify: `room-worker/handler.go`
- Modify: `room-worker/handler_test.go`

- [ ] **Step 1: Update `handler_test.go` (Red phase)**

Add `Username` to subscription fixtures in `ListByRoom` return. Add `InviteeUsername: "bob"` to `InviteMemberRequest`. Assert published subjects use usernames:
- `subject.SubscriptionUpdate("bob")` → `"chat.user.bob.event.subscription.update"`
- `subject.RoomMetadataChanged("alice")` → `"chat.user.alice.event.room.metadata.update"`
- `subject.RoomMetadataChanged("bob")` → `"chat.user.bob.event.room.metadata.update"`

- [ ] **Step 2: Run tests — expect failure**

```bash
make test SERVICE=room-worker
```

- [ ] **Step 3: Update `handler.go`**

Three changes in `processInvite`:
1. Subscription creation: `User: model.SubscriptionUser{ID: req.InviteeID, Username: req.InviteeUsername}`
2. SubscriptionUpdate subject: `subject.SubscriptionUpdate(req.InviteeUsername)`
3. RoomMetadataChanged subject: `subject.RoomMetadataChanged(members[i].User.Username)`

- [ ] **Step 4: Run tests**

```bash
make test SERVICE=room-worker
```

- [ ] **Step 5: Lint and commit**

```bash
make fmt && make lint
git add room-worker/handler.go room-worker/handler_test.go
git commit -m "feat(room-worker): route subscription and metadata events to username subjects"
```

---

### Task 7: Update notification-worker and inbox-worker to use username

**Files:**
- Modify: `notification-worker/handler.go`, `notification-worker/handler_test.go`
- Modify: `inbox-worker/handler.go`, `inbox-worker/handler_test.go`

- [ ] **Step 1: Update `notification-worker/handler_test.go` (Red phase)**

Add `Username` to subscription fixtures with distinct values from `ID` (e.g. `ID: "alice", Username: "username-alice"`) to prove subjects route by `Username` not `ID`. Update subject assertions: `"chat.user.username-bob.notification"`.

- [ ] **Step 2: Update `notification-worker/handler.go` (Green phase)**

Change notification subject: `subject.Notification(subs[i].User.Username)` (was `subs[i].User.ID`). Keep sender-skip on `User.ID` unchanged.

- [ ] **Step 3: Run notification-worker tests**

```bash
make test SERVICE=notification-worker
```

- [ ] **Step 4: Update `inbox-worker/handler_test.go` (Red phase)**

Add `InviteeUsername` to `InviteMemberRequest` fixtures. Add test `TestHandleEvent_MemberAdded_UsernameRoutedSubject` with distinct `InviteeID: "uid-bob"` and `InviteeUsername: "username-bob"` to prove subject uses username.

- [ ] **Step 5: Update `inbox-worker/handler.go` (Green phase)**

Two changes in `handleMemberAdded`:
1. Subscription creation: `User: model.SubscriptionUser{ID: invite.InviteeID, Username: invite.InviteeUsername}`
2. SubscriptionUpdate subject: `subject.SubscriptionUpdate(invite.InviteeUsername)`

- [ ] **Step 6: Run inbox-worker tests**

```bash
make test SERVICE=inbox-worker
```

- [ ] **Step 7: Run full test suite, lint, and commit**

```bash
make test
make fmt && make lint
git add notification-worker/handler.go notification-worker/handler_test.go \
       inbox-worker/handler.go inbox-worker/handler_test.go
git commit -m "feat(notification,inbox): route events to username-based subjects"
```
