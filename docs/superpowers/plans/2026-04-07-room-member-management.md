# Room Member Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement room member management — add, remove, and role-update operations for chat room membership. `room-service` validates and publishes to a JetStream stream; `room-worker` consumes and persists to MongoDB with event fan-out.

**Architecture:** `room-service` (NATS request/reply) validates auth, capacity, normalizes input, publishes to `ROOMS_{siteID}` JetStream stream, replies `{"status":"accepted"}`. `room-worker` (JetStream consumer) persists subscriptions, room_members, userCount, dispatches `SubscriptionUpdateEvent` and `MemberChangeEvent`, publishes to outbox for federation.

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB (go.mongodb.org/mongo-driver/v2), go.uber.org/mock, stretchr/testify, testcontainers-go

**Spec:** `docs/superpowers/specs/2026-04-07-room-member-management-design.md`

---

## File Structure

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `pkg/model/user.go` | Add `Account` field to `User` |
| Modify | `pkg/model/subscription.go` | Add `Account` field to `SubscriptionUser` |
| Create | `pkg/model/member.go` | `AddMembersRequest`, `RemoveMemberRequest`, `UpdateRoleRequest`, `RoomMember`, `HistoryConfig` |
| Modify | `pkg/model/event.go` | Add `MemberChangeEvent` |
| Modify | `pkg/model/model_test.go` | Round-trip tests for all new/modified models |
| Modify | `pkg/subject/subject.go` | Add member subject builders, rename params to `account` |
| Modify | `pkg/subject/subject_test.go` | Tests for new builders |
| Create | `room-service/store.go` | `RoomStore` interface (read-only for member ops) |
| Create | `room-service/store_mongo.go` | MongoDB implementation + `EnsureIndexes` |
| Create | `room-service/handler.go` | Validate-and-publish handlers |
| Create | `room-service/helper.go` | `sanitizeError`, `HasRole`, `dedup`, `filterBots` |
| Modify | `room-service/main.go` | Register handlers, create ROOMS stream |
| Modify | `room-worker/store.go` | `SubscriptionStore` interface (read + write) |
| Modify | `room-worker/store_mongo.go` | All DB mutations + `EnsureIndexes` |
| Modify | `room-worker/handler.go` | Message routing + `processAddMembers`/`RemoveMember`/`RoleUpdate` |
| Modify | `room-worker/main.go` | OUTBOX stream, `EnsureIndexes`, dual publish wiring |

---

### Task 1: Add `Account` field to `User` and `SubscriptionUser`

**Files:**
- Modify: `pkg/model/user.go`
- Modify: `pkg/model/subscription.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write the failing test**

Update `TestUserJSON` in `pkg/model/model_test.go` to include `Account: "alice"` in the fixture:

```go
u := model.User{ID: "u1", Name: "alice", Username: "alice", Account: "alice", SiteID: "site-a"}
```

Update `TestSubscriptionJSON` to include `Account: "alice"` in the `SubscriptionUser`:

```go
User: model.SubscriptionUser{ID: "u1", Username: "alice", Account: "alice"},
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race ./pkg/model/...`
Expected: Compilation error — `Account` field does not exist.

- [ ] **Step 3: Write minimal implementation**

In `pkg/model/user.go`, add after `Username`:
```go
Account  string `json:"account"  bson:"account"`
```

In `pkg/model/subscription.go`, add to `SubscriptionUser` after `Username`:
```go
Account  string `json:"account"  bson:"account"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./pkg/model/...`
Expected: All tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/model/user.go pkg/model/subscription.go pkg/model/model_test.go
git commit -m "feat(model): add Account field to User and SubscriptionUser"
```

---

### Task 2: Create member data models and `MemberChangeEvent`

**Files:**
- Create: `pkg/model/member.go`
- Modify: `pkg/model/event.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `pkg/model/model_test.go`:

```go
func TestMemberChangeEventJSON(t *testing.T) {
    src := model.MemberChangeEvent{
        Type: "member-added", RoomID: "room-1",
        Accounts: []string{"alice", "bob"}, SiteID: "site-a",
    }
    data, err := json.Marshal(src)
    require.NoError(t, err)
    var dst model.MemberChangeEvent
    require.NoError(t, json.Unmarshal(data, &dst))
    assert.Equal(t, src, dst)
}

func TestAddMembersRequestJSON(t *testing.T) {
    src := model.AddMembersRequest{
        RoomID: "r1", Users: []string{"alice"}, Orgs: []string{"org-eng"},
        Channels: []string{"r2"}, History: model.HistoryConfig{Mode: model.HistoryModeAll},
    }
    // ... marshal/unmarshal/assert equal
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race ./pkg/model/...`
Expected: Compilation error — types don't exist.

- [ ] **Step 3: Write implementations**

Create `pkg/model/member.go` with `RoomMemberType`, `HistoryMode` constants, `HistoryConfig`, `AddMembersRequest`, `RemoveMemberRequest`, `UpdateRoleRequest`, `RoomMemberEntry`, `RoomMember`. All with both `json` and `bson` tags.

Add `MemberChangeEvent` to `pkg/model/event.go`:
```go
type MemberChangeEvent struct {
    Type     string   `json:"type"     bson:"type"`
    RoomID   string   `json:"roomId"   bson:"roomId"`
    Accounts []string `json:"accounts" bson:"accounts"`
    SiteID   string   `json:"siteId"   bson:"siteId"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./pkg/model/...`

- [ ] **Step 5: Commit**

```bash
git add pkg/model/member.go pkg/model/event.go pkg/model/model_test.go
git commit -m "feat(model): add member request/response types and MemberChangeEvent"
```

---

### Task 3: Add NATS subject builders for member operations

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write the failing tests**

Add entries to the `TestSubjectBuilders` table:
```go
{"MemberInvite", subject.MemberInvite("alice", "r1", "site-a"),
    "chat.user.alice.request.room.r1.site-a.member.invite"},
{"MemberAdd", subject.MemberAdd("alice", "r1", "site-a"),
    "chat.user.alice.request.room.r1.site-a.member.add"},
{"MemberRemove", subject.MemberRemove("alice", "r1", "site-a"),
    "chat.user.alice.request.room.r1.site-a.member.remove"},
{"MemberRoleUpdate", subject.MemberRoleUpdate("alice", "r1", "site-a"),
    "chat.user.alice.request.room.r1.site-a.member.role-update"},
```

Add entries to `TestWildcardPatterns` and `TestParseUserRoomSubject`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race ./pkg/subject/...`
Expected: Compilation error — functions don't exist.

- [ ] **Step 3: Write implementations**

Add to `pkg/subject/subject.go`: `MemberInvite(account, roomID, siteID)`, `MemberAdd`, `MemberRemove`, `MemberRoleUpdate` and their wildcard counterparts. Add `ParseUserRoomSubject` for extracting account + roomID. All parameters named `account`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./pkg/subject/...`

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add member operation subject builders"
```

---

### Task 4: Implement room-service store and handler

**Files:**
- Create: `room-service/store.go`
- Create: `room-service/store_mongo.go`
- Create: `room-service/handler.go`
- Create: `room-service/helper.go`
- Modify: `room-service/main.go`

- [ ] **Step 1: Define `RoomStore` interface**

Read-only for member operations:
```go
type RoomStore interface {
    CreateRoom(ctx, room) error
    GetRoom(ctx, id) (*Room, error)
    ListRooms(ctx) ([]Room, error)
    CreateSubscription(ctx, sub) error  // room creation only
    GetSubscription(ctx, account, roomID) (*Subscription, error)
    CountSubscriptions(ctx, roomID) (int, error)
    CountOwners(ctx, roomID) (int, error)
    ListSubscriptionsByRoom(ctx, roomID) ([]Subscription, error)
    GetRoomMembers(ctx, roomID) ([]RoomMember, error)
    GetOrgData(ctx, orgID) (name, locationURL string, err error)
    GetUserID(ctx, account) (string, error)
}
```

- [ ] **Step 2: Implement `MongoStore`**

Collections: `rooms`, `subscriptions`, `room_members`, `hr_data`, `users`, `orgs`. Add `EnsureIndexes` with unique indexes on `subscriptions (roomId, u.account)` and `room_members (rid, member.type, member.id, member.username)`. Bot filter in `CountSubscriptions`: regex `(\.bot$|^p_)`.

- [ ] **Step 3: Create helper functions**

`room-service/helper.go`: `sanitizeError`, `HasRole`, `dedup`, `filterBots`, `isBot`.

- [ ] **Step 4: Implement handler**

`Handler` struct: `store`, `siteID`, `currentDomain`, `maxRoomSize`, `publishToStream`.

- `handleAddMembers`: parse subject → unmarshal → expand channels → resolve orgs (hr_data) → dedup → filter bots → capacity check → publish normalized request to ROOMS stream → reply `{"status":"accepted"}`
- `handleRemoveMember`: parse → unmarshal → ambiguity check → auth (owner for non-self) → last-owner guard → publish → reply
- `handleUpdateRole`: parse → unmarshal → owner check → role validity → federation guard (`sub.SiteID != h.siteID`) → last-owner demotion guard → publish → reply
- All NATS handlers use `sanitizeError` before replying

- [ ] **Step 5: Wire `main.go`**

Create ROOMS stream. Init MongoStore + EnsureIndexes. Create handler with JetStream publish callback. Register CRUD + invite handlers.

- [ ] **Step 6: Generate mocks and verify**

```bash
make generate SERVICE=room-service
go vet ./room-service/...
```

- [ ] **Step 7: Commit**

```bash
git add room-service/
git commit -m "feat(room-service): implement validate-and-publish member management"
```

---

### Task 5: Implement room-worker store layer

**Files:**
- Modify: `room-worker/store.go`
- Modify: `room-worker/store_mongo.go`

- [ ] **Step 1: Expand `SubscriptionStore` interface**

Add: `BulkCreateSubscriptions`, `DeleteSubscription`, `UpdateSubscriptionRole`, `CreateRoomMember`, `DeleteRoomMember`, `DeleteOrgRoomMember`, `IncrementUserCount`, `DecrementUserCount`, `GetUser`, `GetOrgAccounts`, `GetRoomMembers`, `EnsureIndexes`.

- [ ] **Step 2: Implement all store methods**

Add collections: `roomMembers`, `users`, `hrData`. `BulkCreateSubscriptions` uses `InsertMany` with `mongo.IsDuplicateKeyError` → no-op. `GetUser` queries by `account` field. `GetOrgAccounts` queries `hr_data` where `sectId == orgID`, returns `accountName` values. `EnsureIndexes` creates matching unique indexes.

- [ ] **Step 3: Generate mocks**

```bash
make generate SERVICE=room-worker
```

- [ ] **Step 4: Commit**

```bash
git add room-worker/store.go room-worker/store_mongo.go room-worker/mock_store_test.go
git commit -m "feat(room-worker): expand store with member management methods"
```

---

### Task 6: Implement room-worker handler and main.go

**Files:**
- Modify: `room-worker/handler.go`
- Modify: `room-worker/main.go`

- [ ] **Step 1: Add message routing**

`HandleJetStreamMsg` inspects last two subject tokens: `member.invite` → `processInvite`, `member.add` → `processAddMembers`, `member.remove` → `processRemoveMember`, `member.role-update` → `processRoleUpdate`.

Handler gets two publish functions: `publishLocal` (NATS core) and `publishOutbox` (JetStream).

- [ ] **Step 2: Implement `processAddMembers`**

1. Unmarshal `AddMembersRequest`
2. For each account: `GetUser(account)` — skip `ErrNoDocuments`, propagate others
3. Build `Subscription` with user's ID, Account, Username, SiteID. Set `HistorySharedSince` per mode
4. `BulkCreateSubscriptions` (idempotent)
5. Ack message
6. Async goroutine: write room_members, increment userCount, publish `SubscriptionUpdateEvent` per account, publish `MemberChangeEvent` to `chat.room.{roomID}.event.member`, publish `MemberChangeEvent` to outbox → `room.SiteID`

- [ ] **Step 3: Implement `processRemoveMember`**

Individual: delete subscription. Org: `GetOrgAccounts` → delete all. Ack. Async: delete room_members, decrement userCount, fan out events + outbox.

- [ ] **Step 4: Implement `processRoleUpdate`**

`UpdateSubscriptionRole`, publish `SubscriptionUpdateEvent` (action: `"role_updated"`).

- [ ] **Step 5: Wire `main.go`**

Add OUTBOX stream creation. Call `EnsureIndexes`. Dual publish: `publishLocal` = `nc.Publish`, `publishOutbox` = `js.Publish`.

- [ ] **Step 6: Verify**

```bash
go vet ./room-worker/...
```

- [ ] **Step 7: Commit**

```bash
git add room-worker/
git commit -m "feat(room-worker): implement processAddMembers, processRemoveMember, processRoleUpdate"
```

---

### Task 7: Unit tests

**Files:**
- Create: `room-service/handler_test.go`
- Create: `room-service/helper_test.go`
- Modify: `room-worker/handler_test.go`

- [ ] **Step 1: Write room-service handler tests**

Table-driven tests with `MockRoomStore`. Capture `publishToStream` data, assert `{"status":"accepted"}` responses:
- `TestHandler_AddMembers`: individuals, org expansion, bot filtering, capacity rejection, malformed JSON
- `TestHandler_RemoveMember`: self-leave, owner-removes-other, non-owner rejection, last-owner guard
- `TestHandler_UpdateRole`: promote, demote, non-owner, federation guard, last-owner demotion

- [ ] **Step 2: Write helper tests**

`TestHasRole`, `TestDedup`, `TestFilterBots`, `TestSanitizeError`.

- [ ] **Step 3: Write room-worker handler tests**

Table-driven tests with `MockSubscriptionStore`:
- `TestHandler_ProcessAddMembers`: happy path, user-not-found skip, bulk create failure
- `TestHandler_ProcessRemoveMember`: individual, org removal
- `TestHandler_ProcessRoleUpdate`: happy path, store error

- [ ] **Step 4: Run all tests**

```bash
make test SERVICE=room-service && make test SERVICE=room-worker
```

- [ ] **Step 5: Commit**

```bash
git add room-service/handler_test.go room-service/helper_test.go room-worker/handler_test.go
git commit -m "test: add unit tests for member management handlers"
```

---

### Task 8: Integration tests

**Files:**
- Create: `room-service/integration_test.go`
- Create: `room-service/nats_integration_test.go`
- Modify: `room-worker/integration_test.go`

All integration tests use `//go:build integration` and testcontainers.

- [ ] **Step 1: room-service MongoDB store tests**

Test all `RoomStore` methods against real MongoDB: room CRUD, subscription CRUD, count operations, member reads, org data lookups, bot filtering, `EnsureIndexes` idempotency.

- [ ] **Step 2: room-service NATS integration tests**

End-to-end with MongoDB + NATS containers: create room, add members (verify `{"status":"accepted"}` + ROOMS stream message), remove member (auth + publish), update role (guards + publish). Log subjects and payloads via `t.Logf`.

- [ ] **Step 3: room-worker integration tests**

Test persistence against real MongoDB: `processAddMembers` (verify subs + room_members created), `processRemoveMember` (verify deleted + userCount), `processRoleUpdate` (verify role changed).

- [ ] **Step 4: Run integration tests**

```bash
make test-integration SERVICE=room-service
make test-integration SERVICE=room-worker
```

- [ ] **Step 5: Commit**

```bash
git add room-service/integration_test.go room-service/nats_integration_test.go room-worker/integration_test.go
git commit -m "test: add integration tests for member management"
```

---

### Task 9: Final verification

- [ ] **Step 1: Run all unit tests**

```bash
make test
```
Expected: All tests PASS.

- [ ] **Step 2: Run lint**

```bash
make lint
```
Expected: No lint errors.

- [ ] **Step 3: Run integration tests**

```bash
make test-integration
```
Expected: All integration tests PASS (requires Docker).
