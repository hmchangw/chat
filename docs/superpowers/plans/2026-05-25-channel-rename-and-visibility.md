# Channel Rename and Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add two admin RPCs for channel rooms: `room.rename` (owner or platform admin) and `room.visibility` (platform admin only, sets `Restricted` + `ExternalAccess` and rewrites roles on the unrestricted→restricted transition).

**Architecture:** Two-service split following the existing role-update pattern. `room-service` validates synchronously and publishes a canonical event to `ROOMS_{siteID}`. `room-worker` (home site) persists Mongo writes, emits a sys message for rename, fans `SubscriptionUpdateEvent` per affected subscription, and publishes one outbox event per remote site that has federated members. `inbox-worker` (remote sites) mirrors the subscription mutation locally — no event fan-out (NATS supercluster routes per-account events from home site).

**Tech Stack:** Go 1.25, NATS + JetStream, MongoDB (operational), `go.uber.org/mock` (mocks), `stretchr/testify` (assertions), `testcontainers-go` (integration).

**Spec:** `docs/superpowers/plans/../specs/2026-05-22-channel-rename-and-visibility-design.md`

---

## Conventions used throughout this plan

- **Commit cadence:** one commit per task. Each task ends with a commit step. Never combine tasks into one commit.
- **TDD cycle per task:** write failing test → run and confirm RED → implement minimal code → run and confirm GREEN → commit. Refactor only after green; never before.
- **Test command shorthand:**
  - `make test SERVICE=<dir>` — unit tests (e.g. `SERVICE=pkg/model`, `SERVICE=room-worker`)
  - `make test-integration SERVICE=<dir>` — integration tests (Docker required)
  - `make generate SERVICE=<dir>` — regenerate mocks (after store interface changes)
  - `make lint`, `make sast` — pre-push checks
- **Never edit `mock_store_test.go` by hand** — always run `make generate SERVICE=<dir>` after interface changes.
- **Spec line refs** in the spec document drift over time; use them as anchors when reading the spec, not as code-search targets.

---

## File map

### Files to create

None. All changes are additions to existing files.

### Files to modify

| File | What changes |
|------|--------------|
| `pkg/model/user.go` | Add `UserRole` type, `UserRoleAdmin`/`UserRoleUser` consts, `User.Roles` field |
| `pkg/model/room.go` | Add `Room.ExternalAccess` field, `RenameRoomRequest`, `RoomVisibilityRequest` |
| `pkg/model/subscription.go` | Add `Subscription.Restricted`, `Subscription.ExternalAccess` |
| `pkg/model/message.go` | Add `MessageTypeRoomRenamed` const, `RoomRenamedSysData` type |
| `pkg/model/event.go` | Add `OutboxRoomRenamed`/`OutboxRoomVisibilityChanged` consts, `RoomRenamedOutboxPayload`/`RoomVisibilityOutboxPayload` types, `AsyncJobOpRoomRename`/`AsyncJobOpRoomVisibility` consts, extend `SubscriptionUpdateEvent.Action` doc-comment |
| `pkg/model/model_test.go` | Round-trip tests for all new structs/fields |
| `pkg/subject/subject.go` | Four new builders: `RoomRename`, `RoomRenameWildcard`, `RoomVisibility`, `RoomVisibilityWildcard` |
| `pkg/subject/subject_test.go` | Test cases for four new builders |
| `room-worker/store.go` | Add 4 methods to `SubscriptionStore` interface |
| `room-worker/store_mongo.go` | Implement the 4 new methods |
| `room-worker/integration_test.go` | Integration tests for the 4 new store methods |
| `room-worker/handler.go` | Add `findRemoteSitesForAccounts` + `publishSubscriptionEvents` helpers; add `processRoomRename` + `processRoomVisibility`; wire dispatch in `HandleJetStreamMsg`; retrofit `processAddMembers` + `processRemoveOrg` to use the new helper |
| `room-worker/handler_test.go` | Unit tests for new processors and helpers |
| `room-worker/mock_store_test.go` | Auto-regenerated via `make generate` |
| `inbox-worker/handler.go` | Add `errPermanent` + `permanentError` + `newPermanent`; add 2 methods to `InboxStore`; add `handleRoomRenamed` + `handleRoomVisibilityChanged`; wire dispatch in `HandleEvent` switch |
| `inbox-worker/main.go` | Update `cons.Consume` callback to `Ack` on permanent errors |
| `inbox-worker/store.go` (NEW, see Task 13 note) or `inbox-worker/main.go` | Implement the 2 new `InboxStore` methods (depending on existing layout; today the mongo impl lives in `main.go`) |
| `inbox-worker/handler_test.go` | Unit tests for new handlers and permanent-error dispatch |
| `inbox-worker/integration_test.go` | Integration tests for the 2 new store methods |
| `room-service/store.go` | (No new methods — existing `GetRoom`/`GetUser`/`GetSubscription` cover validation) |
| `room-service/helper.go` | Add `isPlatformAdmin`, sentinel errors for new validation rules, extend `sanitizeError` prefix list |
| `room-service/handler.go` | Add `natsRoomRename` + `handleRoomRename`, `natsRoomVisibility` + `handleRoomVisibility`; register both in `RegisterCRUD` |
| `room-service/handler_test.go` | Unit tests for both new handlers |
| `room-service/integration_test.go` | End-to-end NATS request/reply with stream-publish assertions |
| `room-service/main.go` | Add `RestrictedRoomMinMembers int` to config struct with `RESTRICTED_ROOM_MIN_MEMBERS` env tag (default `5`) |
| `mock-user-service/handler.go` | Seed `admin1` → admin role in any path that surfaces user records (see Task 36 for discovery note) |
| `docs/client-api.md` | Document both new RPCs under §3.1 |

---

## Phase 1: Data Models (`pkg/model`)

### Task 1: Add `UserRole` type and `User.Roles` field

**Files:**
- Modify: `pkg/model/user.go`
- Modify: `pkg/model/model_test.go` (append after `TestUserJSON_WithSectAndDept`)

- [ ] **Step 1.1: Write the failing round-trip test**

Append to `pkg/model/model_test.go` after the existing `TestUserJSON_WithSectAndDept` (around the User test block):

```go
func TestUserJSON_WithRoles(t *testing.T) {
	u := model.User{
		ID: "u1", Account: "admin1", SiteID: "site-a",
		Roles: []model.UserRole{model.UserRoleAdmin},
	}
	roundTrip(t, &u, &model.User{})
}

func TestUserJSON_DefaultRolesEmpty(t *testing.T) {
	u := model.User{ID: "u1", Account: "alice", SiteID: "site-a"}
	roundTrip(t, &u, &model.User{})
}

func TestUserRoleConstants(t *testing.T) {
	if model.UserRoleAdmin != "admin" {
		t.Errorf("UserRoleAdmin = %q, want %q", model.UserRoleAdmin, "admin")
	}
	if model.UserRoleUser != "user" {
		t.Errorf("UserRoleUser = %q, want %q", model.UserRoleUser, "user")
	}
}
```

- [ ] **Step 1.2: Run test, verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — `undefined: model.UserRole`, `undefined: model.UserRoleAdmin`, `unknown field Roles`.

- [ ] **Step 1.3: Add the type, constants, and field**

Replace `pkg/model/user.go` with:

```go
package model

// UserRole is a platform-level role flag on the User record.
// Empty Roles reads as ["user"]; only positive marker is "admin".
type UserRole string

const (
	UserRoleAdmin UserRole = "admin"
	UserRoleUser  UserRole = "user"
)

type User struct {
	ID          string     `json:"id"           bson:"_id"`
	Account     string     `json:"account"      bson:"account"`
	SiteID      string     `json:"siteId"       bson:"siteId"`
	SectID      string     `json:"sectId"       bson:"sectId"`
	SectName    string     `json:"sectName"     bson:"sectName"`
	SectTCName  string     `json:"sectTCName"   bson:"sectTCName"`
	DeptID      string     `json:"deptId"       bson:"deptId"`
	DeptName    string     `json:"deptName"     bson:"deptName"`
	DeptTCName  string     `json:"deptTCName"   bson:"deptTCName"`
	EngName     string     `json:"engName"      bson:"engName"`
	ChineseName string     `json:"chineseName"  bson:"chineseName"`
	EmployeeID  string     `json:"employeeId"   bson:"employeeId"`
	Roles       []UserRole `json:"roles"        bson:"roles"`
}
```

- [ ] **Step 1.4: Run tests, verify pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS (all three new tests + existing ones).

- [ ] **Step 1.5: Commit**

```bash
git add pkg/model/user.go pkg/model/model_test.go
git commit -m "feat(model): add UserRole type and User.Roles field"
```

---

### Task 2: Add `Room.ExternalAccess` field

**Files:**
- Modify: `pkg/model/room.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 2.1: Write the failing round-trip test**

Append after `TestRoomJSON_NilTimestampsOmitted`:

```go
func TestRoomJSON_RestrictedAndExternalAccess(t *testing.T) {
	r := model.Room{
		ID: "r1", Name: "private", Type: model.RoomTypeChannel,
		SiteID: "site-a", UserCount: 5,
		CreatedAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Restricted:     true,
		ExternalAccess: true,
	}
	roundTrip(t, &r, &model.Room{})
}

func TestRoomJSON_ExternalAccessOmittedWhenFalse(t *testing.T) {
	r := model.Room{
		ID: "r1", Name: "open", Type: model.RoomTypeChannel,
		SiteID: "site-a", UserCount: 1,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(&r)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, has := raw["externalAccess"]
	assert.False(t, has, "false ExternalAccess must be omitted from JSON")
}
```

- [ ] **Step 2.2: Run test, verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — `unknown field ExternalAccess`.

- [ ] **Step 2.3: Add the field**

In `pkg/model/room.go`, edit the `Room` struct — add `ExternalAccess` immediately after `Restricted`:

```go
	Restricted        bool       `json:"restricted,omitempty" bson:"restricted,omitempty"`
	ExternalAccess    bool       `json:"externalAccess,omitempty" bson:"externalAccess,omitempty"`
	UIDs              []string   `json:"uids,omitempty"     bson:"uids,omitempty"`
```

- [ ] **Step 2.4: Run tests, verify pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS.

- [ ] **Step 2.5: Commit**

```bash
git add pkg/model/room.go pkg/model/model_test.go
git commit -m "feat(model): add Room.ExternalAccess field"
```

---

### Task 3: Add `Subscription.Restricted` and `Subscription.ExternalAccess`

**Files:**
- Modify: `pkg/model/subscription.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 3.1: Write the failing round-trip test**

Append to `pkg/model/model_test.go` near the existing Subscription tests:

```go
func TestSubscriptionJSON_RestrictedAndExternalAccess(t *testing.T) {
	s := model.Subscription{
		ID:             "sub-1",
		User:           model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID:         "r1",
		SiteID:         "site-a",
		Roles:          []model.Role{model.RoleMember},
		Name:           "general",
		RoomType:       model.RoomTypeChannel,
		JoinedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Restricted:     true,
		ExternalAccess: true,
	}
	roundTrip(t, &s, &model.Subscription{})
}

func TestSubscriptionJSON_RestrictedOmittedWhenFalse(t *testing.T) {
	s := model.Subscription{
		ID: "sub-1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", SiteID: "site-a",
		Roles: []model.Role{model.RoleMember}, Name: "general",
		RoomType: model.RoomTypeChannel,
		JoinedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(&s)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasRestricted := raw["restricted"]
	_, hasExternal := raw["externalAccess"]
	assert.False(t, hasRestricted, "false Restricted must be omitted")
	assert.False(t, hasExternal, "false ExternalAccess must be omitted")
}
```

- [ ] **Step 3.2: Run test, verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — unknown fields `Restricted` / `ExternalAccess`.

- [ ] **Step 3.3: Add the fields**

In `pkg/model/subscription.go`, edit the `Subscription` struct — add the two fields immediately after `Muted`:

```go
	Alert              bool             `json:"alert" bson:"alert"`
	Muted              bool             `json:"muted" bson:"muted"`
	// Denormalized from Room.Restricted / Room.ExternalAccess so a single
	// SubscriptionUpdateEvent carries every client-facing room field.
	// Readers MUST treat missing as false. Room remains the access-control
	// source of truth.
	Restricted     bool `json:"restricted,omitempty"     bson:"restricted,omitempty"`
	ExternalAccess bool `json:"externalAccess,omitempty" bson:"externalAccess,omitempty"`
}
```

- [ ] **Step 3.4: Run tests, verify pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS.

- [ ] **Step 3.5: Commit**

```bash
git add pkg/model/subscription.go pkg/model/model_test.go
git commit -m "feat(model): add Subscription.Restricted and ExternalAccess denorm fields"
```

---

### Task 4: Add `RenameRoomRequest` and `RoomVisibilityRequest`

**Files:**
- Modify: `pkg/model/room.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 4.1: Write the failing round-trip tests**

Append to `pkg/model/model_test.go`:

```go
func TestRenameRoomRequestJSON(t *testing.T) {
	r := model.RenameRoomRequest{
		RoomID:    "r1",
		NewName:   "new-name",
		Account:   "alice",
		Timestamp: 1700000000000,
	}
	roundTrip(t, &r, &model.RenameRoomRequest{})
}

func TestRoomVisibilityRequestJSON(t *testing.T) {
	r := model.RoomVisibilityRequest{
		RoomID:         "r1",
		Restricted:     true,
		ExternalAccess: false,
		OwnerAccount:   "alice",
		Account:        "admin1",
		Timestamp:      1700000000000,
	}
	roundTrip(t, &r, &model.RoomVisibilityRequest{})
}

func TestRoomVisibilityRequestJSON_OwnerAccountOmittedWhenEmpty(t *testing.T) {
	r := model.RoomVisibilityRequest{
		RoomID: "r1", Restricted: false, ExternalAccess: false,
		Account: "admin1", Timestamp: 1700000000000,
	}
	data, err := json.Marshal(&r)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, has := raw["ownerAccount"]
	assert.False(t, has, "empty OwnerAccount must be omitted from JSON")
}
```

- [ ] **Step 4.2: Run test, verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — undefined types.

- [ ] **Step 4.3: Add the request types**

Append to `pkg/model/room.go` (after `RoomsInfoBatchResponse` or at the end of the file before `BuildDMParticipants`):

```go
// RenameRoomRequest is the canonical event for renaming a channel room.
// Account and Timestamp are server-set by room-service before publishing.
type RenameRoomRequest struct {
	RoomID    string `json:"roomId"    bson:"roomId"`
	NewName   string `json:"newName"   bson:"newName"`
	Account   string `json:"account"   bson:"account"`
	Timestamp int64  `json:"timestamp" bson:"timestamp"`
}

// RoomVisibilityRequest is the canonical event for setting Restricted and
// ExternalAccess on a channel room. When Restricted=true and OwnerAccount
// is non-empty, that account becomes the sole owner (regardless of prior
// role). Account and Timestamp are server-set by room-service.
type RoomVisibilityRequest struct {
	RoomID         string `json:"roomId"                 bson:"roomId"`
	Restricted     bool   `json:"restricted"             bson:"restricted"`
	ExternalAccess bool   `json:"externalAccess"         bson:"externalAccess"`
	OwnerAccount   string `json:"ownerAccount,omitempty" bson:"ownerAccount,omitempty"`
	Account        string `json:"account"                bson:"account"`
	Timestamp      int64  `json:"timestamp"              bson:"timestamp"`
}
```

- [ ] **Step 4.4: Run tests, verify pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS.

- [ ] **Step 4.5: Commit**

```bash
git add pkg/model/room.go pkg/model/model_test.go
git commit -m "feat(model): add RenameRoomRequest and RoomVisibilityRequest"
```

---

### Task 5: Add `MessageTypeRoomRenamed` and `RoomRenamedSysData`

**Files:**
- Modify: `pkg/model/event.go` (add const next to existing `MessageTypeRoomCreated` block)
- Modify: `pkg/model/message.go` (add SysData type — keep sys-data types co-located with `Message`)
- Modify: `pkg/model/model_test.go`

- [ ] **Step 5.1: Write the failing test**

Append to `pkg/model/model_test.go`:

```go
func TestMessageTypeRoomRenamedConstant(t *testing.T) {
	if model.MessageTypeRoomRenamed != "room_renamed" {
		t.Errorf("MessageTypeRoomRenamed = %q", model.MessageTypeRoomRenamed)
	}
}

func TestRoomRenamedSysDataJSON(t *testing.T) {
	d := model.RoomRenamedSysData{NewName: "renamed", ByAccount: "alice"}
	roundTrip(t, &d, &model.RoomRenamedSysData{})
}
```

- [ ] **Step 5.2: Run test, verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — undefined.

- [ ] **Step 5.3: Add constant and type**

In `pkg/model/event.go`, extend the existing `MessageType*` const block (around line 267) by adding the new constant. Replace the block:

```go
const (
	// MessageTypeRoomCreated is the system-message type emitted on room creation (channels only).
	MessageTypeRoomCreated = "room_created"
	// MessageTypeMembersAdded is the system-message type emitted when members are added.
	MessageTypeMembersAdded = "members_added"
	// MessageTypeMemberRemoved is the system-message type emitted when a member is removed.
	MessageTypeMemberRemoved = "member_removed"
	// MessageTypeMemberLeft is the system-message type emitted when a member self-leaves.
	MessageTypeMemberLeft = "member_left"
	// MessageTypeRoomRenamed is the system-message type emitted when a channel is renamed.
	MessageTypeRoomRenamed = "room_renamed"
)
```

In `pkg/model/message.go`, append after the `SendMessageRequest` type:

```go
// RoomRenamedSysData is the JSON payload stored in Message.SysMsgData
// for a room_renamed system message.
type RoomRenamedSysData struct {
	NewName   string `json:"newName"   bson:"newName"`
	ByAccount string `json:"byAccount" bson:"byAccount"`
}
```

- [ ] **Step 5.4: Run tests, verify pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS.

- [ ] **Step 5.5: Commit**

```bash
git add pkg/model/event.go pkg/model/message.go pkg/model/model_test.go
git commit -m "feat(model): add MessageTypeRoomRenamed and RoomRenamedSysData"
```

---

### Task 6: Add outbox constants, payload types, and AsyncJob ops

**Files:**
- Modify: `pkg/model/event.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 6.1: Write the failing tests**

Append to `pkg/model/model_test.go`:

```go
func TestOutboxRoomEventConstants(t *testing.T) {
	if model.OutboxRoomRenamed != "room_renamed" {
		t.Errorf("OutboxRoomRenamed = %q", model.OutboxRoomRenamed)
	}
	if model.OutboxRoomVisibilityChanged != "room_visibility_changed" {
		t.Errorf("OutboxRoomVisibilityChanged = %q", model.OutboxRoomVisibilityChanged)
	}
}

func TestAsyncJobOpRoomConstants(t *testing.T) {
	if model.AsyncJobOpRoomRename != "room.rename" {
		t.Errorf("AsyncJobOpRoomRename = %q", model.AsyncJobOpRoomRename)
	}
	if model.AsyncJobOpRoomVisibility != "room.visibility" {
		t.Errorf("AsyncJobOpRoomVisibility = %q", model.AsyncJobOpRoomVisibility)
	}
}

func TestRoomRenamedOutboxPayloadJSON(t *testing.T) {
	p := model.RoomRenamedOutboxPayload{
		RoomID: "r1", NewName: "x", Timestamp: 1700000000000,
	}
	roundTrip(t, &p, &model.RoomRenamedOutboxPayload{})
}

func TestRoomVisibilityOutboxPayloadJSON(t *testing.T) {
	p := model.RoomVisibilityOutboxPayload{
		RoomID:         "r1",
		Restricted:     true,
		ExternalAccess: false,
		OwnerAccount:   "alice",
		Timestamp:      1700000000000,
	}
	roundTrip(t, &p, &model.RoomVisibilityOutboxPayload{})
}
```

- [ ] **Step 6.2: Run test, verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — undefined symbols.

- [ ] **Step 6.3: Add constants and payload types**

In `pkg/model/event.go`, edit the `OutboxEventType` const block (around line 81-88) — append two new constants:

```go
const (
	OutboxMemberAdded                OutboxEventType = "member_added"
	OutboxMemberRemoved              OutboxEventType = "member_removed"
	OutboxSubscriptionRead           OutboxEventType = "subscription_read"
	OutboxSubscriptionMuteToggled    OutboxEventType = "subscription_mute_toggled"
	OutboxThreadSubscriptionUpserted OutboxEventType = "thread_subscription_upserted"
	OutboxThreadRead                 OutboxEventType = "thread_read"
	OutboxRoomRenamed                OutboxEventType = "room_renamed"
	OutboxRoomVisibilityChanged      OutboxEventType = "room_visibility_changed"
)
```

In `pkg/model/event.go`, edit the AsyncJobOp const block (around line 259-265) — append:

```go
const (
	AsyncJobOpRoomCreate           = "room.create"
	AsyncJobOpRoomMemberAdd        = "room.member.add"
	AsyncJobOpRoomMemberRemove     = "room.member.remove"
	AsyncJobOpRoomMemberRemoveOrg  = "room.member.remove_org"
	AsyncJobOpRoomMemberRoleUpdate = "room.member.role_update"
	AsyncJobOpRoomRename           = "room.rename"
	AsyncJobOpRoomVisibility       = "room.visibility"
)
```

In `pkg/model/event.go`, update the `SubscriptionUpdateEvent.Action` doc-comment to mention the new values:

```go
type SubscriptionUpdateEvent struct {
	UserID       string       `json:"userId"`
	Subscription Subscription `json:"subscription"`
	Action       string       `json:"action"` // "added" | "removed" | "role_updated" | "mute_toggled" | "renamed" | "visibility_changed"
	Timestamp    int64        `json:"timestamp" bson:"timestamp"`
}
```

Append the payload types at the end of `pkg/model/event.go`:

```go
// RoomRenamedOutboxPayload is wrapped in OutboxEvent.Payload for
// OutboxRoomRenamed. Replicated to remote sites that hold federated
// subscriptions on the renamed room.
type RoomRenamedOutboxPayload struct {
	RoomID    string `json:"roomId"    bson:"roomId"`
	NewName   string `json:"newName"   bson:"newName"`
	Timestamp int64  `json:"timestamp" bson:"timestamp"`
}

// RoomVisibilityOutboxPayload is wrapped in OutboxEvent.Payload for
// OutboxRoomVisibilityChanged. When OwnerAccount is non-empty AND
// Restricted is true, the destination site's $cond promotes that
// account's local subscription mirror to the sole owner.
type RoomVisibilityOutboxPayload struct {
	RoomID         string `json:"roomId"                 bson:"roomId"`
	Restricted     bool   `json:"restricted"             bson:"restricted"`
	ExternalAccess bool   `json:"externalAccess"         bson:"externalAccess"`
	OwnerAccount   string `json:"ownerAccount,omitempty" bson:"ownerAccount,omitempty"`
	Timestamp      int64  `json:"timestamp"              bson:"timestamp"`
}
```

- [ ] **Step 6.4: Run tests, verify pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS.

- [ ] **Step 6.5: Commit**

```bash
git add pkg/model/event.go pkg/model/model_test.go
git commit -m "feat(model): add outbox + AsyncJob constants and payloads for room rename/visibility"
```

---

## Phase 2: Subject Builders (`pkg/subject`)

### Task 7: Add four subject builders

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 7.1: Write the failing test cases**

In `pkg/subject/subject_test.go`, append to the `TestSubjectBuilders` table (after the existing `MemberRoleUpdate` entry):

```go
		{"RoomRename", subject.RoomRename("alice", "r1", "site-a"),
			"chat.user.alice.request.room.r1.site-a.room.rename"},
		{"RoomRenameWildcard", subject.RoomRenameWildcard("site-a"),
			"chat.user.*.request.room.*.site-a.room.rename"},
		{"RoomVisibility", subject.RoomVisibility("alice", "r1", "site-a"),
			"chat.user.alice.request.room.r1.site-a.room.visibility"},
		{"RoomVisibilityWildcard", subject.RoomVisibilityWildcard("site-a"),
			"chat.user.*.request.room.*.site-a.room.visibility"},
```

- [ ] **Step 7.2: Run test, verify it fails**

Run: `make test SERVICE=pkg/subject`
Expected: FAIL — undefined functions.

- [ ] **Step 7.3: Add the four builders**

In `pkg/subject/subject.go`, locate `MemberRoleUpdate` / `MemberRoleUpdateWildcard` (lines ~68-70 and ~213-215). Append the four new builders immediately after the corresponding existing ones (keep grouped):

After `MemberRoleUpdate`:

```go
// RoomRename is the request/reply subject for the rename RPC.
// Owner or platform admin only (see room-service validation).
func RoomRename(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.room.rename", account, roomID, siteID)
}

// RoomVisibility is the request/reply subject for the visibility RPC.
// Platform admin only.
func RoomVisibility(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.room.visibility", account, roomID, siteID)
}
```

After `MemberRoleUpdateWildcard`:

```go
// RoomRenameWildcard is the queue-subscribe pattern for room.rename on a site.
func RoomRenameWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.room.rename", siteID)
}

// RoomVisibilityWildcard is the queue-subscribe pattern for room.visibility on a site.
func RoomVisibilityWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.room.visibility", siteID)
}
```

- [ ] **Step 7.4: Run tests, verify pass**

Run: `make test SERVICE=pkg/subject`
Expected: PASS.

- [ ] **Step 7.5: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add room.rename and room.visibility subject builders"
```

---

## Phase 3: room-worker store layer

### Task 8: Extend `SubscriptionStore` interface and regenerate mocks

**Files:**
- Modify: `room-worker/store.go`
- Regenerate: `room-worker/mock_store_test.go` (via `make generate`)

- [ ] **Step 8.1: Add the four new method signatures to the interface**

In `room-worker/store.go`, open the `SubscriptionStore` interface (starts around line 52). Append the four new methods at the end of the interface block (keep them grouped at the bottom with a brief leading comment):

```go
	// Rename/visibility operations (see room-worker handler's
	// processRoomRename / processRoomVisibility).

	// UpdateRoomName sets {name: newName, updatedAt: now} on the room doc.
	// Returns model.ErrRoomNotFound when no doc matches {_id: roomID, type: "channel"}.
	UpdateRoomName(ctx context.Context, roomID, newName string) error

	// UpdateRoomVisibility sets {restricted, externalAccess, updatedAt} on the
	// room doc. Returns model.ErrRoomNotFound when no doc matches
	// {_id: roomID, type: "channel"}.
	UpdateRoomVisibility(ctx context.Context, roomID string, restricted, externalAccess bool) error

	// UpdateSubscriptionNamesForRoom is a single updateMany on subscriptions
	// matching {roomId: roomID}; sets name = newName.
	UpdateSubscriptionNamesForRoom(ctx context.Context, roomID, newName string) error

	// ApplySubscriptionVisibility is a single updateMany on subscriptions
	// matching {roomId: roomID}. Three branches keyed on (restricted, ownerAccount):
	//   - restricted=true, ownerAccount non-empty: aggregation pipeline sets
	//     restricted+externalAccess AND $cond rewrites roles to ["owner"]
	//     for u.account == ownerAccount else ["member"].
	//   - restricted=true, ownerAccount empty: $set restricted+externalAccess only.
	//   - restricted=false: $set restricted+externalAccess only.
	// All branches are idempotent on retry.
	ApplySubscriptionVisibility(ctx context.Context, roomID string, restricted, externalAccess bool, ownerAccount string) error
```

(You may need to ensure `model.ErrRoomNotFound` and `model.ErrNotChannelRoom` exist — check `pkg/model` and existing store code. If `ErrRoomNotFound` doesn't exist as a model-level sentinel, use whatever the existing room-worker code uses for "channel room not found." Search: `grep -rn "ErrRoomNotFound\|ErrNotChannelRoom" pkg/ room-worker/ room-service/` to find current location. If they live in `room-worker/store.go` as package sentinels, reference them as `ErrRoomNotFound` / `ErrNotChannelRoom`.)

- [ ] **Step 8.2: Regenerate mocks**

Run: `make generate SERVICE=room-worker`
Expected: `mock_store_test.go` updated with four new methods.

- [ ] **Step 8.3: Verify compilation**

Run: `go build ./room-worker/...`
Expected: FAIL — `MongoStore does not implement SubscriptionStore` (missing methods). This is expected; impl is Task 9-11.

- [ ] **Step 8.4: Commit interface + regenerated mocks**

```bash
git add room-worker/store.go room-worker/mock_store_test.go
git commit -m "feat(room-worker): extend SubscriptionStore with rename/visibility methods"
```

---

### Task 9: Implement `UpdateRoomName` and `UpdateRoomVisibility` in Mongo store

**Files:**
- Modify: `room-worker/store_mongo.go`
- Modify: `room-worker/integration_test.go`

- [ ] **Step 9.1: Write failing integration tests**

In `room-worker/integration_test.go`, append two test functions (use the existing test pattern — copy the setup harness from a nearby test like the existing `processCreateRoom` integration test). Each test:

1. Seeds a `model.Room{ID: "r1", Type: model.RoomTypeChannel, ...}` into the mongo `rooms` collection.
2. Calls the store method.
3. Reads back the room and asserts the field.
4. Also tests the not-found path returning `ErrRoomNotFound`.
5. Also tests the wrong-type path returning `ErrNotChannelRoom` (insert a DM room, expect not-channel error).

Skeleton:

```go
func TestMongoStore_UpdateRoomName(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "room-worker-rename")
	store := NewMongoStore(db)

	t.Run("happy path", func(t *testing.T) {
		_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
			ID: "r1", Name: "old", Type: model.RoomTypeChannel, SiteID: "site-a",
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
		require.NoError(t, store.UpdateRoomName(ctx, "r1", "new"))
		got, err := store.GetRoom(ctx, "r1")
		require.NoError(t, err)
		assert.Equal(t, "new", got.Name)
	})

	t.Run("room not found", func(t *testing.T) {
		err := store.UpdateRoomName(ctx, "missing", "x")
		assert.ErrorIs(t, err, ErrRoomNotFound)
	})

	t.Run("wrong room type rejected", func(t *testing.T) {
		_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
			ID: "dm1", Type: model.RoomTypeDM, SiteID: "site-a",
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
		err = store.UpdateRoomName(ctx, "dm1", "x")
		assert.ErrorIs(t, err, ErrNotChannelRoom)
	})
}

func TestMongoStore_UpdateRoomVisibility(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "room-worker-visibility")
	store := NewMongoStore(db)

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r1", Name: "x", Type: model.RoomTypeChannel, SiteID: "site-a",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	require.NoError(t, store.UpdateRoomVisibility(ctx, "r1", true, true))
	got, err := store.GetRoom(ctx, "r1")
	require.NoError(t, err)
	assert.True(t, got.Restricted)
	assert.True(t, got.ExternalAccess)

	// Idempotent: re-apply same values.
	require.NoError(t, store.UpdateRoomVisibility(ctx, "r1", true, true))

	// Flip both.
	require.NoError(t, store.UpdateRoomVisibility(ctx, "r1", false, false))
	got, err = store.GetRoom(ctx, "r1")
	require.NoError(t, err)
	assert.False(t, got.Restricted)
	assert.False(t, got.ExternalAccess)

	// Wrong room type
	_, err = db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "dm1", Type: model.RoomTypeDM, SiteID: "site-a",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	err = store.UpdateRoomVisibility(ctx, "dm1", true, true)
	assert.ErrorIs(t, err, ErrNotChannelRoom)
}
```

(Sentinel error names: confirm `ErrRoomNotFound` and `ErrNotChannelRoom` exist in `room-worker/store.go`. If not, define them at the top of `store.go` with `var (... )` block before adding usages.)

- [ ] **Step 9.2: Run integration test, verify failure**

Run: `make test-integration SERVICE=room-worker`
Expected: FAIL — methods unimplemented.

- [ ] **Step 9.3: Implement both methods in `store_mongo.go`**

Append to `room-worker/store_mongo.go`:

```go
// UpdateRoomName atomically renames a channel room. Refuses non-channel rooms.
func (s *MongoStore) UpdateRoomName(ctx context.Context, roomID, newName string) error {
	res, err := s.rooms.UpdateOne(ctx,
		bson.M{"_id": roomID, "type": model.RoomTypeChannel},
		bson.M{"$set": bson.M{"name": newName, "updatedAt": time.Now().UTC()}},
	)
	if err != nil {
		return fmt.Errorf("update room name: %w", err)
	}
	if res.MatchedCount == 0 {
		// Disambiguate not-found vs wrong-type.
		var probe model.Room
		probeErr := s.rooms.FindOne(ctx, bson.M{"_id": roomID}).Decode(&probe)
		if errors.Is(probeErr, mongo.ErrNoDocuments) {
			return ErrRoomNotFound
		}
		if probeErr != nil {
			return fmt.Errorf("probe room type: %w", probeErr)
		}
		return ErrNotChannelRoom
	}
	return nil
}

// UpdateRoomVisibility sets Restricted and ExternalAccess on a channel room.
func (s *MongoStore) UpdateRoomVisibility(ctx context.Context, roomID string, restricted, externalAccess bool) error {
	res, err := s.rooms.UpdateOne(ctx,
		bson.M{"_id": roomID, "type": model.RoomTypeChannel},
		bson.M{"$set": bson.M{
			"restricted":     restricted,
			"externalAccess": externalAccess,
			"updatedAt":      time.Now().UTC(),
		}},
	)
	if err != nil {
		return fmt.Errorf("update room visibility: %w", err)
	}
	if res.MatchedCount == 0 {
		var probe model.Room
		probeErr := s.rooms.FindOne(ctx, bson.M{"_id": roomID}).Decode(&probe)
		if errors.Is(probeErr, mongo.ErrNoDocuments) {
			return ErrRoomNotFound
		}
		if probeErr != nil {
			return fmt.Errorf("probe room type: %w", probeErr)
		}
		return ErrNotChannelRoom
	}
	return nil
}
```

(If `s.rooms` is not the existing collection-field name, use whatever the existing `MongoStore` uses — check `room-worker/store_mongo.go` constructor.)

If `ErrRoomNotFound` / `ErrNotChannelRoom` aren't already defined in `store.go`, add them at the top of `store.go`:

```go
var (
	ErrRoomNotFound    = errors.New("room not found")
	ErrNotChannelRoom  = errors.New("not a channel room")
)
```

- [ ] **Step 9.4: Run integration test, verify pass**

Run: `make test-integration SERVICE=room-worker`
Expected: PASS.

- [ ] **Step 9.5: Commit**

```bash
git add room-worker/store.go room-worker/store_mongo.go room-worker/integration_test.go
git commit -m "feat(room-worker): implement UpdateRoomName and UpdateRoomVisibility store methods"
```

---

### Task 10: Implement `UpdateSubscriptionNamesForRoom`

**Files:**
- Modify: `room-worker/store_mongo.go`
- Modify: `room-worker/integration_test.go`

- [ ] **Step 10.1: Write failing integration test**

Append to `room-worker/integration_test.go`:

```go
func TestMongoStore_UpdateSubscriptionNamesForRoom(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "room-worker-sub-name")
	store := NewMongoStore(db)
	subs := db.Collection("subscriptions")

	_, err := subs.InsertMany(ctx, []any{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", SiteID: "site-a", Name: "old", Roles: []model.Role{model.RoleMember}, RoomType: model.RoomTypeChannel, JoinedAt: time.Now().UTC()},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", SiteID: "site-a", Name: "old", Roles: []model.Role{model.RoleOwner}, RoomType: model.RoomTypeChannel, JoinedAt: time.Now().UTC()},
		model.Subscription{ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "carol"}, RoomID: "other", SiteID: "site-a", Name: "untouched", Roles: []model.Role{model.RoleMember}, RoomType: model.RoomTypeChannel, JoinedAt: time.Now().UTC()},
	})
	require.NoError(t, err)

	require.NoError(t, store.UpdateSubscriptionNamesForRoom(ctx, "r1", "new"))

	all, err := store.ListByRoom(ctx, "r1")
	require.NoError(t, err)
	for _, sub := range all {
		assert.Equal(t, "new", sub.Name, "sub %s", sub.ID)
	}

	other, err := store.ListByRoom(ctx, "other")
	require.NoError(t, err)
	require.Len(t, other, 1)
	assert.Equal(t, "untouched", other[0].Name)

	// Idempotent
	require.NoError(t, store.UpdateSubscriptionNamesForRoom(ctx, "r1", "new"))
}
```

- [ ] **Step 10.2: Run integration test, verify failure**

Run: `make test-integration SERVICE=room-worker`
Expected: FAIL.

- [ ] **Step 10.3: Implement the method**

Append to `room-worker/store_mongo.go`:

```go
// UpdateSubscriptionNamesForRoom mass-renames every subscription on a room.
// Idempotent: re-applying the same name is a silent no-op at the BSON layer.
func (s *MongoStore) UpdateSubscriptionNamesForRoom(ctx context.Context, roomID, newName string) error {
	_, err := s.subscriptions.UpdateMany(ctx,
		bson.M{"roomId": roomID},
		bson.M{"$set": bson.M{"name": newName}},
	)
	if err != nil {
		return fmt.Errorf("update subscription names for room %s: %w", roomID, err)
	}
	return nil
}
```

- [ ] **Step 10.4: Run integration test, verify pass**

Run: `make test-integration SERVICE=room-worker`
Expected: PASS.

- [ ] **Step 10.5: Commit**

```bash
git add room-worker/store_mongo.go room-worker/integration_test.go
git commit -m "feat(room-worker): implement UpdateSubscriptionNamesForRoom store method"
```

---

### Task 11: Implement `ApplySubscriptionVisibility` (three branches)

**Files:**
- Modify: `room-worker/store_mongo.go`
- Modify: `room-worker/integration_test.go`

- [ ] **Step 11.1: Write failing integration tests for all three branches**

Append to `room-worker/integration_test.go`:

```go
func TestMongoStore_ApplySubscriptionVisibility(t *testing.T) {
	seed := func(t *testing.T, db *mongo.Database) {
		t.Helper()
		_, err := db.Collection("subscriptions").InsertMany(context.Background(), []any{
			model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", SiteID: "site-a", Name: "n", Roles: []model.Role{model.RoleOwner}, RoomType: model.RoomTypeChannel, JoinedAt: time.Now().UTC()},
			model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", SiteID: "site-a", Name: "n", Roles: []model.Role{model.RoleMember}, RoomType: model.RoomTypeChannel, JoinedAt: time.Now().UTC()},
			model.Subscription{ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "carol"}, RoomID: "r1", SiteID: "site-a", Name: "n", Roles: []model.Role{model.RoleMember}, RoomType: model.RoomTypeChannel, JoinedAt: time.Now().UTC()},
		})
		require.NoError(t, err)
	}
	rolesByAccount := func(t *testing.T, store *MongoStore, roomID string) map[string][]model.Role {
		t.Helper()
		subs, err := store.ListByRoom(context.Background(), roomID)
		require.NoError(t, err)
		out := make(map[string][]model.Role, len(subs))
		for _, sub := range subs {
			out[sub.User.Account] = sub.Roles
		}
		return out
	}

	t.Run("restrict transition with ownerAccount rewrites roles", func(t *testing.T) {
		db := testutil.MongoDB(t, "room-worker-visibility-restrict")
		store := NewMongoStore(db)
		seed(t, db)

		require.NoError(t, store.ApplySubscriptionVisibility(context.Background(), "r1", true, false, "bob"))

		roles := rolesByAccount(t, store, "r1")
		assert.Equal(t, []model.Role{model.RoleOwner}, roles["bob"], "bob (chosen owner) becomes sole owner")
		assert.Equal(t, []model.Role{model.RoleMember}, roles["alice"], "alice (prior owner) demoted")
		assert.Equal(t, []model.Role{model.RoleMember}, roles["carol"])

		// Idempotent
		require.NoError(t, store.ApplySubscriptionVisibility(context.Background(), "r1", true, false, "bob"))
		roles2 := rolesByAccount(t, store, "r1")
		assert.Equal(t, roles, roles2, "second apply is a no-op on roles")

		// Flags persisted
		subs, err := store.ListByRoom(context.Background(), "r1")
		require.NoError(t, err)
		for _, sub := range subs {
			assert.True(t, sub.Restricted, "sub %s restricted", sub.ID)
			assert.False(t, sub.ExternalAccess, "sub %s externalAccess", sub.ID)
		}
	})

	t.Run("restricted, empty ownerAccount only flips flags (roles untouched)", func(t *testing.T) {
		db := testutil.MongoDB(t, "room-worker-visibility-flagsonly")
		store := NewMongoStore(db)
		seed(t, db)

		require.NoError(t, store.ApplySubscriptionVisibility(context.Background(), "r1", true, true, ""))

		roles := rolesByAccount(t, store, "r1")
		assert.Equal(t, []model.Role{model.RoleOwner}, roles["alice"], "alice still owner")
		assert.Equal(t, []model.Role{model.RoleMember}, roles["bob"])
		assert.Equal(t, []model.Role{model.RoleMember}, roles["carol"])

		subs, err := store.ListByRoom(context.Background(), "r1")
		require.NoError(t, err)
		for _, sub := range subs {
			assert.True(t, sub.Restricted)
			assert.True(t, sub.ExternalAccess)
		}
	})

	t.Run("unrestrict only flips flags (roles untouched even if ownerAccount supplied)", func(t *testing.T) {
		db := testutil.MongoDB(t, "room-worker-visibility-unrestrict")
		store := NewMongoStore(db)
		seed(t, db)

		require.NoError(t, store.ApplySubscriptionVisibility(context.Background(), "r1", false, false, "bob"))

		roles := rolesByAccount(t, store, "r1")
		assert.Equal(t, []model.Role{model.RoleOwner}, roles["alice"], "alice still owner — unrestrict ignores ownerAccount")
		assert.Equal(t, []model.Role{model.RoleMember}, roles["bob"])
	})
}
```

- [ ] **Step 11.2: Run, verify failure**

Run: `make test-integration SERVICE=room-worker`
Expected: FAIL.

- [ ] **Step 11.3: Implement the method**

Append to `room-worker/store_mongo.go`:

```go
// ApplySubscriptionVisibility runs a single updateMany on subscriptions
// matching {roomId: roomID}. Three branches:
//
//   - restricted=true AND ownerAccount non-empty: aggregation pipeline
//     sets restricted+externalAccess AND rewrites roles via $cond
//     (u.account == ownerAccount → ["owner"], else ["member"]).
//   - restricted=true AND ownerAccount empty: $set restricted+externalAccess
//     only; roles untouched (lets admin toggle externalAccess without
//     wiping the existing owner).
//   - restricted=false: $set restricted+externalAccess only; roles untouched.
//     OwnerAccount is ignored on unrestrict.
func (s *MongoStore) ApplySubscriptionVisibility(ctx context.Context, roomID string, restricted, externalAccess bool, ownerAccount string) error {
	filter := bson.M{"roomId": roomID}

	if restricted && ownerAccount != "" {
		// Aggregation-pipeline update with $cond on u.account.
		pipeline := mongo.Pipeline{
			bson.D{{Key: "$set", Value: bson.M{
				"restricted":     true,
				"externalAccess": externalAccess,
				"roles": bson.M{"$cond": bson.M{
					"if":   bson.M{"$eq": bson.A{"$u.account", ownerAccount}},
					"then": bson.A{string(model.RoleOwner)},
					"else": bson.A{string(model.RoleMember)},
				}},
			}}},
		}
		if _, err := s.subscriptions.UpdateMany(ctx, filter, pipeline); err != nil {
			return fmt.Errorf("apply subscription visibility (restrict+rewrite): %w", err)
		}
		return nil
	}

	if _, err := s.subscriptions.UpdateMany(ctx, filter, bson.M{
		"$set": bson.M{
			"restricted":     restricted,
			"externalAccess": externalAccess,
		},
	}); err != nil {
		return fmt.Errorf("apply subscription visibility (flags only): %w", err)
	}
	return nil
}
```

- [ ] **Step 11.4: Run integration test, verify pass**

Run: `make test-integration SERVICE=room-worker`
Expected: PASS.

- [ ] **Step 11.5: Commit**

```bash
git add room-worker/store_mongo.go room-worker/integration_test.go
git commit -m "feat(room-worker): implement ApplySubscriptionVisibility with three branches"
```

---

## Phase 4: inbox-worker store layer

### Task 12: Extend `InboxStore` interface and regenerate mocks

**Files:**
- Modify: `inbox-worker/handler.go` (interface block)
- Regenerate: `inbox-worker/mock_store_test.go`

> **Layout note:** the inbox-worker's mongo store implementation currently lives in `main.go` (search for `type mongoInboxStore struct`). Implementation tasks (Task 14-15) will add new methods to that struct in `main.go` to match the existing layout — do not create a new `store.go` for these two methods.

- [ ] **Step 12.1: Add the two new method signatures to the interface**

In `inbox-worker/handler.go`, edit the `InboxStore` interface (around line 18-36). Append at the end:

```go
	// UpdateSubscriptionNamesForRoom is a single updateMany on subscriptions
	// matching {roomId: roomID}; sets name = newName. On a remote site this
	// only matches the federated subscription copies stored locally.
	UpdateSubscriptionNamesForRoom(ctx context.Context, roomID, newName string) error

	// ApplySubscriptionVisibility mirrors the room-worker counterpart: same
	// three branches keyed on (restricted, ownerAccount). On a remote site
	// this only updates the federated subscription copies whose users are
	// homed here; OwnerAccount is load-bearing so the $cond can promote the
	// chosen owner's local mirror.
	ApplySubscriptionVisibility(ctx context.Context, roomID string, restricted, externalAccess bool, ownerAccount string) error
```

- [ ] **Step 12.2: Regenerate mocks**

Run: `make generate SERVICE=inbox-worker`
Expected: `mock_store_test.go` updated.

- [ ] **Step 12.3: Verify compilation fails as expected**

Run: `go build ./inbox-worker/...`
Expected: FAIL — `mongoInboxStore does not implement InboxStore`.

- [ ] **Step 12.4: Commit interface + regenerated mocks**

```bash
git add inbox-worker/handler.go inbox-worker/mock_store_test.go
git commit -m "feat(inbox-worker): extend InboxStore with rename/visibility methods"
```

---

### Task 13: Implement `UpdateSubscriptionNamesForRoom` in inbox-worker

**Files:**
- Modify: `inbox-worker/main.go` (append to `mongoInboxStore`)
- Modify: `inbox-worker/integration_test.go`

- [ ] **Step 13.1: Write the failing integration test**

Append to `inbox-worker/integration_test.go`:

```go
func TestMongoInboxStore_UpdateSubscriptionNamesForRoom(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "inbox-worker-rename")
	store := &mongoInboxStore{subs: db.Collection("subscriptions")}

	_, err := db.Collection("subscriptions").InsertMany(ctx, []any{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", SiteID: "site-a", Name: "old", Roles: []model.Role{model.RoleMember}, RoomType: model.RoomTypeChannel, JoinedAt: time.Now().UTC()},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "other", SiteID: "site-a", Name: "untouched", Roles: []model.Role{model.RoleMember}, RoomType: model.RoomTypeChannel, JoinedAt: time.Now().UTC()},
	})
	require.NoError(t, err)

	require.NoError(t, store.UpdateSubscriptionNamesForRoom(ctx, "r1", "new"))

	var got model.Subscription
	require.NoError(t, db.Collection("subscriptions").FindOne(ctx, bson.M{"_id": "s1"}).Decode(&got))
	assert.Equal(t, "new", got.Name)

	require.NoError(t, db.Collection("subscriptions").FindOne(ctx, bson.M{"_id": "s2"}).Decode(&got))
	assert.Equal(t, "untouched", got.Name)
}
```

(If the existing `mongoInboxStore` field name for subscriptions differs from `subs`, adjust the test accordingly — check `inbox-worker/main.go`'s `mongoInboxStore` definition.)

- [ ] **Step 13.2: Run, verify failure**

Run: `make test-integration SERVICE=inbox-worker`
Expected: FAIL.

- [ ] **Step 13.3: Implement**

Append to `inbox-worker/main.go` (in the same block as other `mongoInboxStore` methods):

```go
func (s *mongoInboxStore) UpdateSubscriptionNamesForRoom(ctx context.Context, roomID, newName string) error {
	_, err := s.subs.UpdateMany(ctx,
		bson.M{"roomId": roomID},
		bson.M{"$set": bson.M{"name": newName}},
	)
	if err != nil {
		return fmt.Errorf("update subscription names for room %s: %w", roomID, err)
	}
	return nil
}
```

(Use the actual subscription-collection field name. If it's `s.subscriptions`, swap.)

- [ ] **Step 13.4: Run, verify pass**

Run: `make test-integration SERVICE=inbox-worker`
Expected: PASS.

- [ ] **Step 13.5: Commit**

```bash
git add inbox-worker/main.go inbox-worker/integration_test.go
git commit -m "feat(inbox-worker): implement UpdateSubscriptionNamesForRoom store method"
```

---

### Task 14: Implement `ApplySubscriptionVisibility` in inbox-worker

**Files:**
- Modify: `inbox-worker/main.go`
- Modify: `inbox-worker/integration_test.go`

- [ ] **Step 14.1: Write failing integration tests (3 branches)**

Append to `inbox-worker/integration_test.go`:

```go
func TestMongoInboxStore_ApplySubscriptionVisibility(t *testing.T) {
	seed := func(t *testing.T, db *mongo.Database) {
		t.Helper()
		_, err := db.Collection("subscriptions").InsertMany(context.Background(), []any{
			model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", SiteID: "site-a", Name: "n", Roles: []model.Role{model.RoleOwner}, RoomType: model.RoomTypeChannel, JoinedAt: time.Now().UTC()},
			model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", SiteID: "site-a", Name: "n", Roles: []model.Role{model.RoleMember}, RoomType: model.RoomTypeChannel, JoinedAt: time.Now().UTC()},
		})
		require.NoError(t, err)
	}
	getRoles := func(t *testing.T, db *mongo.Database, id string) []model.Role {
		t.Helper()
		var sub model.Subscription
		require.NoError(t, db.Collection("subscriptions").FindOne(context.Background(), bson.M{"_id": id}).Decode(&sub))
		return sub.Roles
	}

	t.Run("restrict transition with ownerAccount", func(t *testing.T) {
		db := testutil.MongoDB(t, "inbox-worker-visibility-restrict")
		store := &mongoInboxStore{subs: db.Collection("subscriptions")}
		seed(t, db)

		require.NoError(t, store.ApplySubscriptionVisibility(context.Background(), "r1", true, false, "bob"))
		assert.Equal(t, []model.Role{model.RoleOwner}, getRoles(t, db, "s2"))
		assert.Equal(t, []model.Role{model.RoleMember}, getRoles(t, db, "s1"))
	})

	t.Run("flags only — empty ownerAccount", func(t *testing.T) {
		db := testutil.MongoDB(t, "inbox-worker-visibility-flags")
		store := &mongoInboxStore{subs: db.Collection("subscriptions")}
		seed(t, db)

		require.NoError(t, store.ApplySubscriptionVisibility(context.Background(), "r1", true, true, ""))
		assert.Equal(t, []model.Role{model.RoleOwner}, getRoles(t, db, "s1"))
		assert.Equal(t, []model.Role{model.RoleMember}, getRoles(t, db, "s2"))
	})

	t.Run("unrestrict ignores ownerAccount", func(t *testing.T) {
		db := testutil.MongoDB(t, "inbox-worker-visibility-unrestrict")
		store := &mongoInboxStore{subs: db.Collection("subscriptions")}
		seed(t, db)

		require.NoError(t, store.ApplySubscriptionVisibility(context.Background(), "r1", false, false, "bob"))
		assert.Equal(t, []model.Role{model.RoleOwner}, getRoles(t, db, "s1"))
		assert.Equal(t, []model.Role{model.RoleMember}, getRoles(t, db, "s2"))
	})
}
```

- [ ] **Step 14.2: Run, verify failure**

Run: `make test-integration SERVICE=inbox-worker`
Expected: FAIL.

- [ ] **Step 14.3: Implement (same body as room-worker — deliberate duplication per spec)**

Append to `inbox-worker/main.go`:

```go
func (s *mongoInboxStore) ApplySubscriptionVisibility(ctx context.Context, roomID string, restricted, externalAccess bool, ownerAccount string) error {
	filter := bson.M{"roomId": roomID}

	if restricted && ownerAccount != "" {
		pipeline := mongo.Pipeline{
			bson.D{{Key: "$set", Value: bson.M{
				"restricted":     true,
				"externalAccess": externalAccess,
				"roles": bson.M{"$cond": bson.M{
					"if":   bson.M{"$eq": bson.A{"$u.account", ownerAccount}},
					"then": bson.A{string(model.RoleOwner)},
					"else": bson.A{string(model.RoleMember)},
				}},
			}}},
		}
		if _, err := s.subs.UpdateMany(ctx, filter, pipeline); err != nil {
			return fmt.Errorf("apply subscription visibility (restrict+rewrite): %w", err)
		}
		return nil
	}

	if _, err := s.subs.UpdateMany(ctx, filter, bson.M{
		"$set": bson.M{
			"restricted":     restricted,
			"externalAccess": externalAccess,
		},
	}); err != nil {
		return fmt.Errorf("apply subscription visibility (flags only): %w", err)
	}
	return nil
}
```

- [ ] **Step 14.4: Run, verify pass**

Run: `make test-integration SERVICE=inbox-worker`
Expected: PASS.

- [ ] **Step 14.5: Commit**

```bash
git add inbox-worker/main.go inbox-worker/integration_test.go
git commit -m "feat(inbox-worker): implement ApplySubscriptionVisibility with three branches"
```

---

## Phase 5: inbox-worker permanent-error refactor

### Task 15: Port `errPermanent` and `permanentError` to inbox-worker

**Files:**
- Modify: `inbox-worker/handler.go`
- Modify: `inbox-worker/handler_test.go`

- [ ] **Step 15.1: Write failing tests for the sentinel and constructor**

Append to `inbox-worker/handler_test.go`:

```go
func TestNewPermanent_WrapsErrPermanent(t *testing.T) {
	err := newPermanent("bad payload: %s", "boom")
	require.NotNil(t, err)
	assert.True(t, errors.Is(err, errPermanent), "newPermanent must be matchable via errPermanent")
	assert.Equal(t, "bad payload: boom", err.Error())
}

func TestPermanentError_NestedIs(t *testing.T) {
	inner := newPermanent("a")
	wrapped := fmt.Errorf("outer: %w", inner)
	assert.True(t, errors.Is(wrapped, errPermanent))
}
```

(Add the missing `errors` / `fmt` imports if not already present.)

- [ ] **Step 15.2: Run test, verify failure**

Run: `make test SERVICE=inbox-worker`
Expected: FAIL — `errPermanent`, `newPermanent` undefined.

- [ ] **Step 15.3: Add the sentinel, type, constructor, and methods to `handler.go`**

In `inbox-worker/handler.go`, add at the top (after imports) — sentinel, type, constructor, methods. Mirror `room-worker/handler.go:30, 124-146`:

```go
// errPermanent marks non-retryable errors (caller Acks instead of Nak).
var errPermanent = errors.New("permanent")

// permanentError pairs a user-safe message with the errPermanent sentinel so
// the consume loop can Ack the JetStream message on poison input.
type permanentError struct {
	msg   string
	cause error
}

func newPermanent(format string, args ...any) error {
	return &permanentError{msg: fmt.Sprintf(format, args...)}
}

func (e *permanentError) Error() string { return e.msg }
func (e *permanentError) Unwrap() error { return e.cause }
func (e *permanentError) Is(target error) bool {
	if target == errPermanent {
		return true
	}
	_, ok := target.(*permanentError)
	return ok
}
```

Make sure `errors` and `fmt` are in the import block.

- [ ] **Step 15.4: Run tests, verify pass**

Run: `make test SERVICE=inbox-worker`
Expected: PASS.

- [ ] **Step 15.5: Commit**

```bash
git add inbox-worker/handler.go inbox-worker/handler_test.go
git commit -m "refactor(inbox-worker): port errPermanent sentinel and permanentError type"
```

---

### Task 16: Ack-on-permanent in `cons.Consume` callback

**Files:**
- Modify: `inbox-worker/main.go`
- Modify: `inbox-worker/main_test.go` (or `handler_test.go` if dispatch logic tests live there)

- [ ] **Step 16.1: Write failing test for ack-on-permanent dispatch**

Search for an existing unit test that covers the `cons.Consume` callback. If a small dispatch helper exists, test that directly. If not, add a test for a `dispatchAckPolicy(err error) ackAction` helper that you'll extract from the callback (it makes the loop testable):

Append to `inbox-worker/main_test.go`:

```go
func TestDispatchAckPolicy(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ackAction
	}{
		{"nil → ack", nil, ackActionAck},
		{"permanent → ack", newPermanent("bad"), ackActionAck},
		{"wrapped permanent → ack", fmt.Errorf("ctx: %w", newPermanent("bad")), ackActionAck},
		{"transient → nak", errors.New("mongo timeout"), ackActionNak},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, dispatchAckPolicy(tt.err))
		})
	}
}
```

- [ ] **Step 16.2: Run, verify failure**

Run: `make test SERVICE=inbox-worker`
Expected: FAIL — `dispatchAckPolicy`, `ackAction`, `ackActionAck`, `ackActionNak` undefined.

- [ ] **Step 16.3: Implement the helper and use it in `cons.Consume`**

In `inbox-worker/main.go`, add near the top (above the `main` func):

```go
type ackAction int

const (
	ackActionNak ackAction = iota
	ackActionAck
)

// dispatchAckPolicy decides whether a handler error should Ack (poison
// message; stop redelivering) or Nak (transient; retry). Permanent
// failures Ack so JetStream stops looping on them.
func dispatchAckPolicy(err error) ackAction {
	if err == nil {
		return ackActionAck
	}
	if errors.Is(err, errPermanent) {
		return ackActionAck
	}
	return ackActionNak
}
```

Locate the `cons.Consume` callback (around lines 298-308). Replace the inner handle-and-ack block:

```go
cctx, err := cons.Consume(func(m oteljetstream.Msg) {
	handlerCtx := natsutil.RequestIDContextFromMsg(m.Msg) // (use whatever ctx helper is already in scope)
	handlerErr := handler.HandleEvent(handlerCtx, m.Data())
	if handlerErr != nil {
		slog.Error("handle event failed",
			"error", handlerErr,
			"request_id", natsutil.RequestIDFromContext(handlerCtx))
	}
	switch dispatchAckPolicy(handlerErr) {
	case ackActionAck:
		if err := m.Ack(); err != nil {
			slog.Error("failed to ack message", "error", err)
		}
	case ackActionNak:
		if err := m.Nak(); err != nil {
			slog.Error("failed to nak message", "error", err)
		}
	}
})
```

(Match the exact ctx-helper currently used; only the policy branch changes.)

- [ ] **Step 16.4: Run, verify pass**

Run: `make test SERVICE=inbox-worker`
Expected: PASS.

- [ ] **Step 16.5: Commit**

```bash
git add inbox-worker/main.go inbox-worker/main_test.go
git commit -m "refactor(inbox-worker): ack on permanent errors in cons.Consume callback"
```

---

## Phase 6: room-service handlers

### Task 17: Add validation sentinels, `isPlatformAdmin`, and config

**Files:**
- Modify: `room-service/helper.go`
- Modify: `room-service/helper_test.go`
- Modify: `room-service/main.go` (config struct)

- [ ] **Step 17.1: Write failing test for `isPlatformAdmin`**

Append to `room-service/helper_test.go`:

```go
func TestIsPlatformAdmin(t *testing.T) {
	tests := []struct {
		name string
		user *model.User
		want bool
	}{
		{"nil user", nil, false},
		{"empty roles", &model.User{Account: "alice"}, false},
		{"user role only", &model.User{Account: "alice", Roles: []model.UserRole{model.UserRoleUser}}, false},
		{"admin role", &model.User{Account: "admin1", Roles: []model.UserRole{model.UserRoleAdmin}}, true},
		{"mixed with admin", &model.User{Account: "x", Roles: []model.UserRole{model.UserRoleUser, model.UserRoleAdmin}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isPlatformAdmin(tt.user))
		})
	}
}
```

- [ ] **Step 17.2: Run, verify failure**

Run: `make test SERVICE=room-service`
Expected: FAIL — `isPlatformAdmin` undefined.

- [ ] **Step 17.3: Add the helper and the new sentinels**

In `room-service/helper.go`, append after `dedup`:

```go
// isPlatformAdmin returns true when u has the UserRoleAdmin role.
// Nil-safe; empty Roles reads as non-admin.
func isPlatformAdmin(u *model.User) bool {
	if u == nil {
		return false
	}
	for _, r := range u.Roles {
		if r == model.UserRoleAdmin {
			return true
		}
	}
	return false
}
```

Add new sentinel errors at the top of the `var (...)` block in `helper.go`:

```go
	errOnlyOwnersOrAdmins        = errors.New("only owners or admins can rename a channel")
	errOnlyAdmins                = errors.New("only admins can change room visibility")
	errOwnerNotMember            = errors.New("owner account is not a member of this room")
	errOwnerAccountRequired      = errors.New("owner account is required when restricting a room")
	errNotEnoughMembers          = errors.New("not enough members to restrict")
	errInvalidName               = errors.New("invalid name")
	errRenameChannelOnly         = errors.New("rename is only allowed in channel rooms")
	errVisibilityChannelOnly     = errors.New("visibility change is only allowed in channel rooms")
	errRoomNotFound              = errors.New("room not found")
```

Also extend `sanitizeError`'s sentinel list (Task 18) to include `errRoomNotFound` so the handler can return it directly without wrapping.

- [ ] **Step 17.4: Run, verify pass**

Run: `make test SERVICE=room-service`
Expected: PASS.

- [ ] **Step 17.5: Add `RestrictedRoomMinMembers` to config**

In `room-service/main.go`, find the `config` struct (or wherever `caarlos0/env` is parsed) and add:

```go
RestrictedRoomMinMembers int `env:"RESTRICTED_ROOM_MIN_MEMBERS" envDefault:"5"`
```

Pass it through to `NewHandler` (extend constructor signature) and store it on `Handler`.

In `room-service/handler.go`, add a field to the `Handler` struct:

```go
restrictedRoomMinMembers int
```

Set it in `NewHandler` from a new parameter.

- [ ] **Step 17.6: Verify compilation**

Run: `go build ./room-service/...`
Expected: PASS (or build error pointing at any caller of `NewHandler` that needs updating — fix those callers to pass the new arg).

- [ ] **Step 17.7: Commit**

```bash
git add room-service/helper.go room-service/helper_test.go room-service/handler.go room-service/main.go
git commit -m "feat(room-service): add isPlatformAdmin, visibility sentinels, RestrictedRoomMinMembers config"
```

---

### Task 18: Extend `sanitizeError` prefix list

**Files:**
- Modify: `room-service/helper.go`
- Modify: `room-service/helper_test.go`

- [ ] **Step 18.1: Write failing test cases for new sanitized prefixes**

Append to `room-service/helper_test.go`:

```go
func TestSanitizeError_RenameAndVisibilityPrefixes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"only owners or admins", errOnlyOwnersOrAdmins, "only owners or admins can rename a channel"},
		{"only admins", errOnlyAdmins, "only admins can change room visibility"},
		{"owner not member", errOwnerNotMember, "owner account is not a member of this room"},
		{"owner account required", errOwnerAccountRequired, "owner account is required when restricting a room"},
		{"not enough members", errNotEnoughMembers, "not enough members to restrict"},
		{"invalid name", errInvalidName, "invalid name"},
		{"rename channel only", errRenameChannelOnly, "rename is only allowed in channel rooms"},
		{"visibility channel only", errVisibilityChannelOnly, "visibility change is only allowed in channel rooms"},
		{"room not found", errRoomNotFound, "room not found"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitizeError(tt.err))
		})
	}
}
```

- [ ] **Step 18.2: Run, verify failure**

Run: `make test SERVICE=room-service`
Expected: FAIL — `sanitizeError` returns `"internal error"` for these sentinels.

- [ ] **Step 18.3: Extend `sanitizeError`'s sentinel list**

In `room-service/helper.go`, edit the long `errors.Is(...)` block in `sanitizeError` — add the new sentinels to the existing case:

```go
		errors.Is(err, errInvalidThreadID),
		errors.Is(err, errThreadSubNotFound),
		errors.Is(err, errOnlyOwnersOrAdmins),
		errors.Is(err, errOnlyAdmins),
		errors.Is(err, errOwnerNotMember),
		errors.Is(err, errOwnerAccountRequired),
		errors.Is(err, errNotEnoughMembers),
		errors.Is(err, errInvalidName),
		errors.Is(err, errRenameChannelOnly),
		errors.Is(err, errVisibilityChannelOnly),
		errors.Is(err, errRoomNotFound),
		errors.Is(err, &dmExistsError{}),
		errors.Is(err, &channelExpandTimeoutError{}):
```

- [ ] **Step 18.4: Run, verify pass**

Run: `make test SERVICE=room-service`
Expected: PASS.

- [ ] **Step 18.5: Commit**

```bash
git add room-service/helper.go room-service/helper_test.go
git commit -m "feat(room-service): extend sanitizeError with rename/visibility sentinels"
```

---

### Task 19: Implement `handleRoomRename` validation

**Files:**
- Modify: `room-service/handler.go`
- Modify: `room-service/handler_test.go`

- [ ] **Step 19.1: Write failing table-driven test for all validation rules**

Append to `room-service/handler_test.go`:

```go
func TestHandleRoomRename_Validation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	channelRoom := &model.Room{ID: "r1", Name: "old", Type: model.RoomTypeChannel, SiteID: "site-a"}
	dmRoom := &model.Room{ID: "dm1", Type: model.RoomTypeDM, SiteID: "site-a"}

	tests := []struct {
		name       string
		subj       string
		body       []byte
		setupStore func(s *MockRoomStore)
		wantErr    error
	}{
		{
			name: "invalid subject",
			subj: "garbage",
			body: mustJSON(t, model.RenameRoomRequest{NewName: "x"}),
			wantErr: errInvalidRenameSubject,
		},
		{
			name: "blank newName rejected",
			subj: subject.RoomRename("alice", "r1", "site-a"),
			body: mustJSON(t, model.RenameRoomRequest{RoomID: "r1", NewName: "   "}),
			setupStore: func(s *MockRoomStore) {},
			wantErr: errInvalidName,
		},
		{
			name: "name too long rejected",
			subj: subject.RoomRename("alice", "r1", "site-a"),
			body: mustJSON(t, model.RenameRoomRequest{RoomID: "r1", NewName: strings.Repeat("x", 101)}),
			setupStore: func(s *MockRoomStore) {},
			wantErr: errInvalidName,
		},
		{
			name: "room not found",
			subj: subject.RoomRename("alice", "r1", "site-a"),
			body: mustJSON(t, model.RenameRoomRequest{RoomID: "r1", NewName: "new"}),
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{Account: "alice"}, nil)
				s.EXPECT().GetRoom(gomock.Any(), "r1").Return(nil, mongo.ErrNoDocuments)
			},
			wantErr: errRoomNotFound,
		},
		{
			name: "wrong room type",
			subj: subject.RoomRename("alice", "dm1", "site-a"),
			body: mustJSON(t, model.RenameRoomRequest{RoomID: "dm1", NewName: "new"}),
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{Account: "alice"}, nil)
				s.EXPECT().GetRoom(gomock.Any(), "dm1").Return(dmRoom, nil)
			},
			wantErr: errRenameChannelOnly,
		},
		{
			name: "non-owner non-admin rejected",
			subj: subject.RoomRename("alice", "r1", "site-a"),
			body: mustJSON(t, model.RenameRoomRequest{RoomID: "r1", NewName: "new"}),
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{Account: "alice", Roles: []model.UserRole{model.UserRoleUser}}, nil)
				s.EXPECT().GetRoom(gomock.Any(), "r1").Return(channelRoom, nil)
				s.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{Roles: []model.Role{model.RoleMember}}, nil)
			},
			wantErr: errOnlyOwnersOrAdmins,
		},
		{
			name: "owner allowed",
			subj: subject.RoomRename("alice", "r1", "site-a"),
			body: mustJSON(t, model.RenameRoomRequest{RoomID: "r1", NewName: "new"}),
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{Account: "alice", Roles: []model.UserRole{model.UserRoleUser}}, nil)
				s.EXPECT().GetRoom(gomock.Any(), "r1").Return(channelRoom, nil)
				s.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{Roles: []model.Role{model.RoleOwner}}, nil)
			},
			wantErr: nil,
		},
		{
			name: "admin allowed without subscription",
			subj: subject.RoomRename("admin1", "r1", "site-a"),
			body: mustJSON(t, model.RenameRoomRequest{RoomID: "r1", NewName: "new"}),
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "admin1").Return(&model.User{Account: "admin1", Roles: []model.UserRole{model.UserRoleAdmin}}, nil)
				s.EXPECT().GetRoom(gomock.Any(), "r1").Return(channelRoom, nil)
				// No GetSubscription call: admin bypass.
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMockRoomStore(ctrl)
			if tt.setupStore != nil {
				tt.setupStore(store)
			}
			h := makeTestHandler(t, store) // helper sets up a Handler with a no-op publishToStream
			ctx := natsutil.ContextWithRequestID(context.Background(), "01970a4f-8c2d-7c9a-abcd-e0123456789f")
			_, err := h.handleRoomRename(ctx, tt.subj, tt.body)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
```

(`mustJSON` and `makeTestHandler` are likely already present in `handler_test.go`; if not, add small helpers. The exact subject-parse-error sentinel name will be whatever the handler chooses — pick a clear name like `errInvalidRenameSubject`.)

- [ ] **Step 19.2: Run, verify failure**

Run: `make test SERVICE=room-service`
Expected: FAIL — `handleRoomRename` undefined.

- [ ] **Step 19.3: Implement the handler**

Append to `room-service/handler.go`:

```go
var errInvalidRenameSubject = errors.New("invalid rename subject")

// natsRoomRename is the NATS request handler; thin wrapper around handleRoomRename.
func (h *Handler) natsRoomRename(m otelnats.Msg) {
	ctx := wrappedCtx(m)
	resp, err := h.handleRoomRename(ctx, m.Msg.Subject, m.Msg.Data)
	if err != nil {
		slog.Error("rename room failed", "error", err)
		natsutil.ReplyError(m.Msg, sanitizeError(err))
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to room.rename", "error", err)
	}
}

func (h *Handler) handleRoomRename(ctx context.Context, subj string, data []byte) ([]byte, error) {
	account, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errInvalidRenameSubject, subj)
	}

	requestID := natsutil.RequestIDFromContext(ctx)
	if requestID == "" {
		return nil, errMissingRequestID
	}
	if !idgen.IsValidUUID(requestID) {
		return nil, errInvalidRequestID
	}

	var req model.RenameRoomRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}
	if req.RoomID != "" && req.RoomID != roomID {
		return nil, fmt.Errorf("invalid request: room ID mismatch")
	}
	if req.Account != "" && req.Account != account {
		return nil, fmt.Errorf("invalid request: account mismatch")
	}
	req.RoomID = roomID
	req.Account = account

	name := strings.TrimSpace(req.NewName)
	if name == "" || len(name) > 100 {
		return nil, errInvalidName
	}
	req.NewName = name

	requesterUser, err := h.store.GetUser(ctx, account)
	if err != nil && !errors.Is(err, ErrUserNotFound) {
		return nil, fmt.Errorf("get user: %w", err)
	}

	room, err := h.store.GetRoom(ctx, roomID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errRoomNotFound // existing sentinel; use whatever the create-room handler uses
		}
		return nil, fmt.Errorf("get room: %w", err)
	}
	if room.Type != model.RoomTypeChannel {
		return nil, errRenameChannelOnly
	}

	// Admin bypass, otherwise owner subscription required.
	if !isPlatformAdmin(requesterUser) {
		sub, subErr := h.store.GetSubscription(ctx, account, roomID)
		if subErr != nil || !hasRole(sub.Roles, model.RoleOwner) {
			return nil, errOnlyOwnersOrAdmins
		}
	}

	req.Timestamp = time.Now().UTC().UnixMilli()
	out, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal rename request: %w", err)
	}
	if err := h.publishToStream(ctx, subject.RoomCanonical(h.siteID, "room.rename"), out); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}
	return json.Marshal(map[string]string{"status": "accepted", "requestId": requestID})
}
```

(If the spec mentions `errRoomNotFound` and it doesn't exist, add it to the helper sentinel block.)

- [ ] **Step 19.4: Run tests, verify pass**

Run: `make test SERVICE=room-service`
Expected: PASS for `TestHandleRoomRename_Validation`.

- [ ] **Step 19.5: Commit**

```bash
git add room-service/handler.go room-service/handler_test.go
git commit -m "feat(room-service): add room.rename handler with validation"
```

---

### Task 20: Implement `handleRoomVisibility` validation

**Files:**
- Modify: `room-service/handler.go`
- Modify: `room-service/handler_test.go`

- [ ] **Step 20.1: Write failing table-driven tests for validation rules**

Append to `room-service/handler_test.go` a `TestHandleRoomVisibility_Validation` with table cases:

- Invalid subject → `errInvalidVisibilitySubject`.
- Unmarshal failure → "invalid request".
- Non-admin requester → `errOnlyAdmins`.
- Room not found → `errRoomNotFound`.
- Non-channel room → `errVisibilityChannelOnly`.
- Restricted=true, ownerAccount non-empty, owner not a member → `errOwnerNotMember`.
- Restricted=true (transition false→true) without ownerAccount → `errOwnerAccountRequired`.
- Restricted=true (transition) with UserCount below threshold → `errNotEnoughMembers`.
- Restricted=true (transition) with valid owner + sufficient UserCount → success.
- Restricted=false (unrestrict) → success (no owner / threshold checks).
- Restricted=true non-transition with ownerAccount → success (owner change while staying restricted).

Use the same mock-store pattern from Task 19. Set the handler's `restrictedRoomMinMembers` to `5` via the test harness.

- [ ] **Step 20.2: Run, verify failure**

Run: `make test SERVICE=room-service`
Expected: FAIL — `handleRoomVisibility` undefined.

- [ ] **Step 20.3: Implement the handler**

Append to `room-service/handler.go`:

```go
var errInvalidVisibilitySubject = errors.New("invalid visibility subject")

func (h *Handler) natsRoomVisibility(m otelnats.Msg) {
	ctx := wrappedCtx(m)
	resp, err := h.handleRoomVisibility(ctx, m.Msg.Subject, m.Msg.Data)
	if err != nil {
		slog.Error("room visibility failed", "error", err)
		natsutil.ReplyError(m.Msg, sanitizeError(err))
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to room.visibility", "error", err)
	}
}

func (h *Handler) handleRoomVisibility(ctx context.Context, subj string, data []byte) ([]byte, error) {
	account, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errInvalidVisibilitySubject, subj)
	}

	requestID := natsutil.RequestIDFromContext(ctx)
	if requestID == "" {
		return nil, errMissingRequestID
	}
	if !idgen.IsValidUUID(requestID) {
		return nil, errInvalidRequestID
	}

	var req model.RoomVisibilityRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}
	if req.RoomID != "" && req.RoomID != roomID {
		return nil, fmt.Errorf("invalid request: room ID mismatch")
	}
	if req.Account != "" && req.Account != account {
		return nil, fmt.Errorf("invalid request: account mismatch")
	}
	req.RoomID = roomID
	req.Account = account

	requesterUser, err := h.store.GetUser(ctx, account)
	if err != nil && !errors.Is(err, ErrUserNotFound) {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if !isPlatformAdmin(requesterUser) {
		return nil, errOnlyAdmins
	}

	room, err := h.store.GetRoom(ctx, roomID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errRoomNotFound
		}
		return nil, fmt.Errorf("get room: %w", err)
	}
	if room.Type != model.RoomTypeChannel {
		return nil, errVisibilityChannelOnly
	}

	isTransition := req.Restricted && !room.Restricted

	// Rule #7 applies to both transitions and non-transitions when OwnerAccount is supplied.
	if req.Restricted && req.OwnerAccount != "" {
		if _, subErr := h.store.GetSubscription(ctx, req.OwnerAccount, roomID); subErr != nil {
			if errors.Is(subErr, mongo.ErrNoDocuments) || errors.Is(subErr, model.ErrSubscriptionNotFound) {
				return nil, errOwnerNotMember
			}
			return nil, fmt.Errorf("get owner subscription: %w", subErr)
		}
	}

	if isTransition {
		if req.OwnerAccount == "" {
			return nil, errOwnerAccountRequired
		}
		if room.UserCount < h.restrictedRoomMinMembers {
			return nil, fmt.Errorf("%w (need at least %d)", errNotEnoughMembers, h.restrictedRoomMinMembers)
		}
	}

	req.Timestamp = time.Now().UTC().UnixMilli()
	out, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal visibility request: %w", err)
	}
	if err := h.publishToStream(ctx, subject.RoomCanonical(h.siteID, "room.visibility"), out); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}
	return json.Marshal(map[string]string{"status": "accepted", "requestId": requestID})
}
```

- [ ] **Step 20.4: Run tests, verify pass**

Run: `make test SERVICE=room-service`
Expected: PASS for the new visibility table tests.

- [ ] **Step 20.5: Commit**

```bash
git add room-service/handler.go room-service/handler_test.go
git commit -m "feat(room-service): add room.visibility handler with validation"
```

---

### Task 21: Register `room.rename` and `room.visibility` in `RegisterCRUD`

**Files:**
- Modify: `room-service/handler.go`

- [ ] **Step 21.1: Add the two new subscribes**

Inside `RegisterCRUD` (around lines 66-110), append two new `QueueSubscribe` calls before the closing `return nil`:

```go
	if _, err := nc.QueueSubscribe(subject.RoomRenameWildcard(h.siteID), queue, h.natsRoomRename); err != nil {
		return fmt.Errorf("subscribe room rename: %w", err)
	}
	if _, err := nc.QueueSubscribe(subject.RoomVisibilityWildcard(h.siteID), queue, h.natsRoomVisibility); err != nil {
		return fmt.Errorf("subscribe room visibility: %w", err)
	}
```

- [ ] **Step 21.2: Verify compilation**

Run: `go build ./room-service/...`
Expected: PASS.

- [ ] **Step 21.3: Commit**

```bash
git add room-service/handler.go
git commit -m "feat(room-service): register room.rename and room.visibility subscriptions"
```

---

### Task 22: Integration test for both RPCs

**Files:**
- Modify: `room-service/integration_test.go`

- [ ] **Step 22.1: Write end-to-end integration test**

Append to `room-service/integration_test.go` — for each RPC, send a NATS request, assert reply shape, and assert a canonical event was published to `ROOMS_{siteID}`. Follow the pattern of existing integration tests in the file (find an existing `TestIntegration_…` for the role-update RPC and copy the harness).

Key assertions for rename:
- Reply body = `{"status":"accepted","requestId":"<uuid>"}`.
- A JetStream message published to `chat.room.canonical.<siteID>.room.rename` with a `RenameRoomRequest` payload where `Account` and `Timestamp` are set.

Key assertions for visibility:
- Reply body = `{"status":"accepted","requestId":"<uuid>"}`.
- A JetStream message published to `chat.room.canonical.<siteID>.room.visibility`.

Both should also include negative scenarios: e.g., non-admin requesting visibility → reply contains the sanitized error.

- [ ] **Step 22.2: Run, verify pass**

Run: `make test-integration SERVICE=room-service`
Expected: PASS.

- [ ] **Step 22.3: Commit**

```bash
git add room-service/integration_test.go
git commit -m "test(room-service): integration tests for room.rename and room.visibility"
```

---

## Phase 7: room-worker processors

### Task 23: Extract `findRemoteSitesForAccounts` helper + retrofit existing call sites

**Files:**
- Modify: `room-worker/handler.go`
- Modify: `room-worker/handler_test.go`

- [ ] **Step 23.1: Write failing unit test for the helper**

Append to `room-worker/handler_test.go`:

```go
func TestFindRemoteSitesForAccounts(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"alice", "bob", "carol"}).Return([]model.User{
		{Account: "alice", SiteID: "site-a"}, // local
		{Account: "bob", SiteID: "site-b"},   // remote
		{Account: "carol", SiteID: "site-c"}, // remote
	}, nil)

	h := &Handler{store: store, siteID: "site-a"}
	got, err := h.findRemoteSitesForAccounts(context.Background(), []string{"alice", "bob", "carol"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"site-b", "site-c"}, got)
}

func TestFindRemoteSitesForAccounts_Empty(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)

	h := &Handler{store: store, siteID: "site-a"}
	got, err := h.findRemoteSitesForAccounts(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFindRemoteSitesForAccounts_Dedupes(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).Return([]model.User{
		{Account: "bob", SiteID: "site-b"},
		{Account: "dave", SiteID: "site-b"}, // same site as bob
	}, nil)

	h := &Handler{store: store, siteID: "site-a"}
	got, err := h.findRemoteSitesForAccounts(context.Background(), []string{"bob", "dave"})
	require.NoError(t, err)
	assert.Equal(t, []string{"site-b"}, got)
}
```

- [ ] **Step 23.2: Run, verify failure**

Run: `make test SERVICE=room-worker`
Expected: FAIL — `findRemoteSitesForAccounts` undefined.

- [ ] **Step 23.3: Implement the helper**

Append to `room-worker/handler.go` (near the other helpers, e.g. after `messageDedupSeed`):

```go
// findRemoteSitesForAccounts looks up the home site of each account and
// returns the deduplicated set of remote sites (siteID != h.siteID).
// Returns an empty slice when accounts is empty; never returns nil.
func (h *Handler) findRemoteSitesForAccounts(ctx context.Context, accounts []string) ([]string, error) {
	if len(accounts) == 0 {
		return []string{}, nil
	}
	users, err := h.store.FindUsersByAccounts(ctx, accounts)
	if err != nil {
		return nil, fmt.Errorf("find users by accounts: %w", err)
	}
	seen := make(map[string]struct{}, len(users))
	out := make([]string, 0, len(users))
	for _, u := range users {
		if u.SiteID == h.siteID {
			continue
		}
		if _, dup := seen[u.SiteID]; dup {
			continue
		}
		seen[u.SiteID] = struct{}{}
		out = append(out, u.SiteID)
	}
	return out, nil
}
```

- [ ] **Step 23.4: Retrofit `processAddMembers` (around lines 1117-1124) to use the helper**

Find the existing snippet that calls `h.store.FindUsersByAccounts(ctx, accounts)` and buckets into `remoteSites map[string]struct{}` inside `processAddMembers`. Replace with:

```go
	remoteSites, err := h.findRemoteSitesForAccounts(ctx, accounts)
	if err != nil {
		return fmt.Errorf("find remote sites for outbox fan-out: %w", err)
	}
```

Then change the `for destSiteID := range remoteSites { ... }` loop to `for _, destSiteID := range remoteSites { ... }`.

- [ ] **Step 23.5: Retrofit `processRemoveOrg` (around lines 706-712)**

Locate the equivalent snippet inside `processRemoveOrg` (it builds `siteAccounts map[string][]string` — slightly different shape because it groups accounts by site). The bucket of `destSiteID`s is still derivable. If the surrounding logic genuinely needs the `siteID → []accounts` map (it iterates `for destSiteID, accounts := range siteAccounts`), leave `processRemoveOrg` as-is and note in the commit message that the helper is a future cleanup candidate for that site. Otherwise replace as above.

**Decision rule:** keep the existing per-site account grouping in `processRemoveOrg` if its loop body iterates accounts; the helper is only suited for code that just needs a `[]siteID`. Document this in your commit message.

- [ ] **Step 23.6: Run tests, verify pass**

Run: `make test SERVICE=room-worker`
Expected: PASS (new helper tests + existing tests, including `TestProcessAddMembers_*` if any).

- [ ] **Step 23.7: Commit**

```bash
git add room-worker/handler.go room-worker/handler_test.go
git commit -m "refactor(room-worker): extract findRemoteSitesForAccounts helper and retrofit processAddMembers"
```

---

### Task 24: Extract `publishSubscriptionEvents` helper

**Files:**
- Modify: `room-worker/handler.go`
- Modify: `room-worker/handler_test.go`

- [ ] **Step 24.1: Write failing unit test**

Append to `room-worker/handler_test.go`:

```go
func TestPublishSubscriptionEvents(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)

	type published struct {
		subject string
		action  string
		account string
	}
	var got []published
	publish := func(_ context.Context, subj string, data []byte, _ string) error {
		var evt model.SubscriptionUpdateEvent
		require.NoError(t, json.Unmarshal(data, &evt))
		got = append(got, published{subject: subj, action: evt.Action, account: evt.Subscription.User.Account})
		return nil
	}

	h := &Handler{store: store, siteID: "site-a", publish: publish}
	subs := []model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1"},
		{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1"},
	}
	h.publishSubscriptionEvents(context.Background(), subs, "renamed")

	require.Len(t, got, 2)
	assert.Equal(t, subject.SubscriptionUpdate("alice"), got[0].subject)
	assert.Equal(t, "renamed", got[0].action)
	assert.Equal(t, "alice", got[0].account)
	assert.Equal(t, subject.SubscriptionUpdate("bob"), got[1].subject)
}
```

- [ ] **Step 24.2: Run, verify failure**

Run: `make test SERVICE=room-worker`
Expected: FAIL — undefined.

- [ ] **Step 24.3: Implement the helper**

Append to `room-worker/handler.go` (near `publishSubscriptionUpdates` — line ~1746). The new helper accepts `action` as a parameter:

```go
// publishSubscriptionEvents fans out one SubscriptionUpdateEvent per
// subscription to chat.user.{account}.event.subscription.update. action
// is the event's Action field (e.g. "renamed", "visibility_changed").
// Best-effort: failures are logged but don't abort the loop.
func (h *Handler) publishSubscriptionEvents(ctx context.Context, subs []model.Subscription, action string) {
	now := time.Now().UTC().UnixMilli()
	for i := range subs {
		evt := model.SubscriptionUpdateEvent{
			UserID:       subs[i].User.ID,
			Subscription: subs[i],
			Action:       action,
			Timestamp:    now,
		}
		data, err := json.Marshal(evt)
		if err != nil {
			slog.Error("marshal subscription update event failed",
				"error", err, "account", subs[i].User.Account, "action", action)
			continue
		}
		if err := h.publish(ctx, subject.SubscriptionUpdate(subs[i].User.Account), data, ""); err != nil {
			slog.Error("publish subscription update failed",
				"error", err, "account", subs[i].User.Account, "action", action)
		}
	}
}
```

- [ ] **Step 24.4: Run, verify pass**

Run: `make test SERVICE=room-worker`
Expected: PASS.

- [ ] **Step 24.5: Commit**

```bash
git add room-worker/handler.go room-worker/handler_test.go
git commit -m "feat(room-worker): add publishSubscriptionEvents helper"
```

---

### Task 25: Implement `processRoomRename`

**Files:**
- Modify: `room-worker/handler.go`
- Modify: `room-worker/handler_test.go`

- [ ] **Step 25.1: Write failing unit tests**

Append to `room-worker/handler_test.go`. Test cases:

1. **Missing requestID → permanent error, no store calls.**
2. **Invalid UUID → permanent.**
3. **Unmarshal failure → permanent, deferred AsyncJobResult does NOT publish (requesterAccount empty).**
4. **`UpdateRoomName` returns `ErrRoomNotFound` → permanent error + AsyncJobResult{Status:"error", Error:"room not found"}.**
5. **`UpdateRoomName` returns `ErrNotChannelRoom` → permanent + AsyncJobResult error.**
6. **Transient store error (e.g. mongo timeout) on `UpdateSubscriptionNamesForRoom` → returns non-permanent error → AsyncJobResult{Status:"error"}.**
7. **Happy path with no remote sites: `UpdateRoomName`, `UpdateSubscriptionNamesForRoom`, sys message published with deterministic `Nats-Msg-Id`, `ListByRoom` → `publishSubscriptionEvents` × N, `FindUsersByAccounts` returns only local users → no outbox publish, AsyncJobResult{Status:"ok"}.**
8. **Happy path with remote sites: same as 7 plus one outbox publish per distinct remote site.**

Use mocked `SubscriptionStore` and a capturing `publish` function (see Task 24 pattern). Sample skeleton for case 7:

```go
func TestProcessRoomRename_HappyPathNoRemoteSites(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)

	roomID := "r1"
	newName := "renamed"
	requestID := "01970a4f-8c2d-7c9a-abcd-e0123456789f"

	subs := []model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: roomID, Name: "old"},
		{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: roomID, Name: "old"},
	}

	store.EXPECT().UpdateRoomName(gomock.Any(), roomID, newName).Return(nil)
	store.EXPECT().UpdateSubscriptionNamesForRoom(gomock.Any(), roomID, newName).Return(nil)
	store.EXPECT().ListByRoom(gomock.Any(), roomID).Return(subs, nil)
	store.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).Return([]model.User{
		{Account: "alice", SiteID: "site-a"},
		{Account: "bob", SiteID: "site-a"},
	}, nil)

	var publishedSubjects []string
	publish := func(_ context.Context, subj string, _ []byte, _ string) error {
		publishedSubjects = append(publishedSubjects, subj)
		return nil
	}

	h := &Handler{store: store, siteID: "site-a", publish: publish}
	ctx := natsutil.ContextWithRequestID(context.Background(), requestID)

	body, _ := json.Marshal(model.RenameRoomRequest{RoomID: roomID, NewName: newName, Account: "alice", Timestamp: time.Now().UTC().UnixMilli()})

	err := h.processRoomRename(ctx, body)
	require.NoError(t, err)

	// Expect: one canonical sys-msg publish + two SubscriptionUpdateEvent publishes + one AsyncJobResult.
	assert.Contains(t, publishedSubjects, subject.MsgCanonicalCreated("site-a"))
	assert.Contains(t, publishedSubjects, subject.SubscriptionUpdate("alice"))
	assert.Contains(t, publishedSubjects, subject.SubscriptionUpdate("bob"))
	assert.Contains(t, publishedSubjects, subject.UserResponse("alice", requestID))
	// No outbox publishes (all subs local).
	for _, subj := range publishedSubjects {
		assert.NotContains(t, subj, "outbox.", "outbox publish unexpected: %s", subj)
	}
}
```

Repeat the pattern for the other cases. **Crucially, also add the error-then-ok retry sequence** (per spec §Processing — "Duplicate AsyncJobResult on retry"):

```go
func TestProcessRoomRename_ErrorThenOkRetrySequence(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)

	roomID := "r1"
	requestID := "01970a4f-8c2d-7c9a-abcd-e0123456789f"

	// First call: transient error on UpdateRoomName → error AsyncJobResult.
	store.EXPECT().UpdateRoomName(gomock.Any(), roomID, "x").Return(errors.New("mongo timeout"))
	// Second call (simulated redelivery): everything succeeds.
	store.EXPECT().UpdateRoomName(gomock.Any(), roomID, "x").Return(nil)
	store.EXPECT().UpdateSubscriptionNamesForRoom(gomock.Any(), roomID, "x").Return(nil)
	store.EXPECT().ListByRoom(gomock.Any(), roomID).Return([]model.Subscription{}, nil)
	store.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).Return([]model.User{}, nil)

	var asyncResults []model.AsyncJobResult
	publish := func(_ context.Context, subj string, data []byte, _ string) error {
		if subj == subject.UserResponse("alice", requestID) {
			var r model.AsyncJobResult
			require.NoError(t, json.Unmarshal(data, &r))
			asyncResults = append(asyncResults, r)
		}
		return nil
	}

	h := &Handler{store: store, siteID: "site-a", publish: publish}
	ctx := natsutil.ContextWithRequestID(context.Background(), requestID)
	body, _ := json.Marshal(model.RenameRoomRequest{RoomID: roomID, NewName: "x", Account: "alice", Timestamp: 1700000000000})

	// First delivery: non-permanent transient error → caller would Nak.
	err := h.processRoomRename(ctx, body)
	require.Error(t, err)
	assert.False(t, errors.Is(err, errPermanent), "transient error must not be permanent")

	// Second delivery (simulated redelivery): success.
	require.NoError(t, h.processRoomRename(ctx, body))

	require.Len(t, asyncResults, 2)
	assert.Equal(t, model.AsyncJobStatusError, asyncResults[0].Status, "first delivery publishes error")
	assert.Equal(t, model.AsyncJobStatusOK, asyncResults[1].Status, "retry publishes ok")
}
```

- [ ] **Step 25.2: Run, verify failure**

Run: `make test SERVICE=room-worker`
Expected: FAIL — `processRoomRename` undefined.

- [ ] **Step 25.3: Implement `processRoomRename`**

Append to `room-worker/handler.go`:

```go
func (h *Handler) processRoomRename(ctx context.Context, data []byte) (err error) {
	var requesterAccount, roomID string
	defer func() {
		h.publishAsyncJobResult(ctx, requesterAccount, model.AsyncJobOpRoomRename, roomID, err)
	}()

	requestID := natsutil.RequestIDFromContext(ctx)
	if requestID == "" {
		return newPermanent("missing X-Request-ID")
	}
	if !idgen.IsValidUUID(requestID) {
		return newPermanent("invalid X-Request-ID: must be a hyphenated UUID")
	}

	var req model.RenameRoomRequest
	if err = json.Unmarshal(data, &req); err != nil {
		return newPermanent("unmarshal rename request: %s", err.Error())
	}
	requesterAccount = req.Account
	roomID = req.RoomID

	if err = h.store.UpdateRoomName(ctx, req.RoomID, req.NewName); err != nil {
		if errors.Is(err, ErrRoomNotFound) {
			return newPermanent("room not found")
		}
		if errors.Is(err, ErrNotChannelRoom) {
			return newPermanent("rename is only allowed in channel rooms")
		}
		return fmt.Errorf("update room name: %w", err)
	}

	if err = h.store.UpdateSubscriptionNamesForRoom(ctx, req.RoomID, req.NewName); err != nil {
		return fmt.Errorf("update subscription names: %w", err)
	}

	// Sys message
	sysData, err := json.Marshal(model.RoomRenamedSysData{
		NewName:   req.NewName,
		ByAccount: req.Account,
	})
	if err != nil {
		return fmt.Errorf("marshal sys data: %w", err)
	}
	msg := model.Message{
		ID:          idgen.MessageIDFromRequestID(requestID, "room_renamed"),
		RoomID:      req.RoomID,
		UserAccount: req.Account,
		Type:        model.MessageTypeRoomRenamed,
		Content:     fmt.Sprintf("%s renamed the channel to %q", req.Account, req.NewName),
		SysMsgData:  sysData,
		CreatedAt:   time.UnixMilli(req.Timestamp).UTC(),
	}
	if err = h.publishCanonical(ctx, &msg, h.siteID, time.Now().UTC()); err != nil {
		return fmt.Errorf("publish room_renamed sys message: %w", err)
	}

	subs, err := h.store.ListByRoom(ctx, req.RoomID)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	h.publishSubscriptionEvents(ctx, subs, "renamed")

	accounts := make([]string, 0, len(subs))
	for _, sub := range subs {
		accounts = append(accounts, sub.User.Account)
	}
	remoteSites, err := h.findRemoteSitesForAccounts(ctx, accounts)
	if err != nil {
		return fmt.Errorf("find remote sites for outbox fan-out: %w", err)
	}
	for _, remoteSiteID := range remoteSites {
		payload, marshalErr := json.Marshal(model.RoomRenamedOutboxPayload{
			RoomID:    req.RoomID,
			NewName:   req.NewName,
			Timestamp: req.Timestamp,
		})
		if marshalErr != nil {
			return fmt.Errorf("marshal rename outbox payload: %w", marshalErr)
		}
		evt := model.OutboxEvent{
			Type:       model.OutboxRoomRenamed,
			SiteID:     h.siteID,
			DestSiteID: remoteSiteID,
			Payload:    payload,
			Timestamp:  time.Now().UTC().UnixMilli(),
		}
		evtData, marshalErr := json.Marshal(evt)
		if marshalErr != nil {
			return fmt.Errorf("marshal rename outbox event: %w", marshalErr)
		}
		payloadSeed := fmt.Sprintf("%s:%s:%d", req.RoomID, req.NewName, req.Timestamp)
		dedupID := natsutil.OutboxDedupID(ctx, remoteSiteID, payloadSeed)
		if err = h.publish(ctx, subject.Outbox(h.siteID, remoteSiteID, model.OutboxRoomRenamed), evtData, dedupID); err != nil {
			return fmt.Errorf("publish rename outbox to %s: %w", remoteSiteID, err)
		}
	}

	return nil
}
```

- [ ] **Step 25.4: Run, verify pass**

Run: `make test SERVICE=room-worker`
Expected: PASS.

- [ ] **Step 25.5: Commit**

```bash
git add room-worker/handler.go room-worker/handler_test.go
git commit -m "feat(room-worker): implement processRoomRename"
```

---

### Task 26: Implement `processRoomVisibility`

**Files:**
- Modify: `room-worker/handler.go`
- Modify: `room-worker/handler_test.go`

- [ ] **Step 26.1: Write failing unit tests**

Append a parallel suite to Task 25's tests. Coverage:

- Missing/invalid requestID → permanent.
- Unmarshal failure → permanent + no stale ok.
- `UpdateRoomVisibility` returns `ErrRoomNotFound` → permanent + AsyncJobResult error.
- Happy path no remote sites: `UpdateRoomVisibility`, `ApplySubscriptionVisibility(... ownerAccount="bob")`, `ListByRoom` → `publishSubscriptionEvents(subs, "visibility_changed")`, AsyncJobResult ok. NO sys message publish.
- Happy path with remote sites: above plus outbox publish per remote site with `RoomVisibilityOutboxPayload` carrying `OwnerAccount`.

Sample case skeleton (happy path with remote site):

```go
func TestProcessRoomVisibility_HappyPathWithRemoteSite(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)

	roomID := "r1"
	requestID := "01970a4f-8c2d-7c9a-abcd-e0123456789f"
	ownerAccount := "bob"

	subs := []model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: roomID},
		{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: roomID},
	}

	store.EXPECT().UpdateRoomVisibility(gomock.Any(), roomID, true, false).Return(nil)
	store.EXPECT().ApplySubscriptionVisibility(gomock.Any(), roomID, true, false, ownerAccount).Return(nil)
	store.EXPECT().ListByRoom(gomock.Any(), roomID).Return(subs, nil)
	store.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).Return([]model.User{
		{Account: "alice", SiteID: "site-a"},
		{Account: "bob", SiteID: "site-b"}, // remote
	}, nil)

	var publishedSubjects []string
	var capturedOutboxPayloads [][]byte
	publish := func(_ context.Context, subj string, data []byte, _ string) error {
		publishedSubjects = append(publishedSubjects, subj)
		if strings.HasPrefix(subj, "outbox.") {
			capturedOutboxPayloads = append(capturedOutboxPayloads, data)
		}
		return nil
	}

	h := &Handler{store: store, siteID: "site-a", publish: publish}
	ctx := natsutil.ContextWithRequestID(context.Background(), requestID)

	body, _ := json.Marshal(model.RoomVisibilityRequest{
		RoomID: roomID, Restricted: true, ExternalAccess: false,
		OwnerAccount: ownerAccount, Account: "admin1", Timestamp: time.Now().UTC().UnixMilli(),
	})

	require.NoError(t, h.processRoomVisibility(ctx, body))

	assert.Contains(t, publishedSubjects, subject.SubscriptionUpdate("alice"))
	assert.Contains(t, publishedSubjects, subject.SubscriptionUpdate("bob"))
	assert.Contains(t, publishedSubjects, subject.Outbox("site-a", "site-b", model.OutboxRoomVisibilityChanged))
	assert.Contains(t, publishedSubjects, subject.UserResponse("admin1", requestID))

	// Decoding the outbox payload to assert OwnerAccount is carried.
	require.Len(t, capturedOutboxPayloads, 1)
	var outboxEvt model.OutboxEvent
	require.NoError(t, json.Unmarshal(capturedOutboxPayloads[0], &outboxEvt))
	var payload model.RoomVisibilityOutboxPayload
	require.NoError(t, json.Unmarshal(outboxEvt.Payload, &payload))
	assert.Equal(t, ownerAccount, payload.OwnerAccount)
	assert.True(t, payload.Restricted)
}
```

- [ ] **Step 26.2: Run, verify failure**

Run: `make test SERVICE=room-worker`
Expected: FAIL.

- [ ] **Step 26.3: Implement `processRoomVisibility`**

Append to `room-worker/handler.go`:

```go
func (h *Handler) processRoomVisibility(ctx context.Context, data []byte) (err error) {
	var requesterAccount, roomID string
	defer func() {
		h.publishAsyncJobResult(ctx, requesterAccount, model.AsyncJobOpRoomVisibility, roomID, err)
	}()

	requestID := natsutil.RequestIDFromContext(ctx)
	if requestID == "" {
		return newPermanent("missing X-Request-ID")
	}
	if !idgen.IsValidUUID(requestID) {
		return newPermanent("invalid X-Request-ID: must be a hyphenated UUID")
	}

	var req model.RoomVisibilityRequest
	if err = json.Unmarshal(data, &req); err != nil {
		return newPermanent("unmarshal visibility request: %s", err.Error())
	}
	requesterAccount = req.Account
	roomID = req.RoomID

	if err = h.store.UpdateRoomVisibility(ctx, req.RoomID, req.Restricted, req.ExternalAccess); err != nil {
		if errors.Is(err, ErrRoomNotFound) {
			return newPermanent("room not found")
		}
		if errors.Is(err, ErrNotChannelRoom) {
			return newPermanent("visibility change is only allowed in channel rooms")
		}
		return fmt.Errorf("update room visibility: %w", err)
	}

	if err = h.store.ApplySubscriptionVisibility(ctx, req.RoomID, req.Restricted, req.ExternalAccess, req.OwnerAccount); err != nil {
		return fmt.Errorf("apply subscription visibility: %w", err)
	}

	subs, err := h.store.ListByRoom(ctx, req.RoomID)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	h.publishSubscriptionEvents(ctx, subs, "visibility_changed")

	accounts := make([]string, 0, len(subs))
	for _, sub := range subs {
		accounts = append(accounts, sub.User.Account)
	}
	remoteSites, err := h.findRemoteSitesForAccounts(ctx, accounts)
	if err != nil {
		return fmt.Errorf("find remote sites for outbox fan-out: %w", err)
	}
	for _, remoteSiteID := range remoteSites {
		payload, marshalErr := json.Marshal(model.RoomVisibilityOutboxPayload{
			RoomID:         req.RoomID,
			Restricted:     req.Restricted,
			ExternalAccess: req.ExternalAccess,
			OwnerAccount:   req.OwnerAccount,
			Timestamp:      req.Timestamp,
		})
		if marshalErr != nil {
			return fmt.Errorf("marshal visibility outbox payload: %w", marshalErr)
		}
		evt := model.OutboxEvent{
			Type:       model.OutboxRoomVisibilityChanged,
			SiteID:     h.siteID,
			DestSiteID: remoteSiteID,
			Payload:    payload,
			Timestamp:  time.Now().UTC().UnixMilli(),
		}
		evtData, marshalErr := json.Marshal(evt)
		if marshalErr != nil {
			return fmt.Errorf("marshal visibility outbox event: %w", marshalErr)
		}
		payloadSeed := fmt.Sprintf("%s:%t:%t:%d", req.RoomID, req.Restricted, req.ExternalAccess, req.Timestamp)
		dedupID := natsutil.OutboxDedupID(ctx, remoteSiteID, payloadSeed)
		if err = h.publish(ctx, subject.Outbox(h.siteID, remoteSiteID, model.OutboxRoomVisibilityChanged), evtData, dedupID); err != nil {
			return fmt.Errorf("publish visibility outbox to %s: %w", remoteSiteID, err)
		}
	}

	return nil
}
```

- [ ] **Step 26.4: Run, verify pass**

Run: `make test SERVICE=room-worker`
Expected: PASS.

- [ ] **Step 26.5: Commit**

```bash
git add room-worker/handler.go room-worker/handler_test.go
git commit -m "feat(room-worker): implement processRoomVisibility"
```

---

### Task 27: Wire dispatch in `HandleJetStreamMsg`

**Files:**
- Modify: `room-worker/handler.go`

- [ ] **Step 27.1: Extend the dispatch switch**

In `room-worker/handler.go` `HandleJetStreamMsg` (around line 188), add two new cases to the `switch` statement (before `default`):

```go
	case strings.HasSuffix(subj, ".room.rename"):
		err = h.processRoomRename(ctx, msg.Data())
	case strings.HasSuffix(subj, ".room.visibility"):
		err = h.processRoomVisibility(ctx, msg.Data())
```

- [ ] **Step 27.2: Write unit test asserting dispatch**

Add a test in `room-worker/handler_test.go` that constructs a fake `jetstream.Msg` with subject `chat.room.canonical.site-a.room.rename` and minimal body, then verifies `processRoomRename` runs (use a recording test double or call `HandleJetStreamMsg` and check side effects). If the existing dispatch is tested elsewhere, follow that pattern.

- [ ] **Step 27.3: Run, verify pass**

Run: `make test SERVICE=room-worker`
Expected: PASS.

- [ ] **Step 27.4: Commit**

```bash
git add room-worker/handler.go room-worker/handler_test.go
git commit -m "feat(room-worker): dispatch room.rename and room.visibility canonical events"
```

---

### Task 28: room-worker integration test

**Files:**
- Modify: `room-worker/integration_test.go`

- [ ] **Step 28.1: Write end-to-end integration test for rename**

Append to `room-worker/integration_test.go`. Use the existing test harness (testcontainers Mongo + NATS via `testutil`). Steps:

1. Seed `rooms` and `subscriptions` collections (channel room with 2 local subs, 1 remote sub).
2. Seed `users` collection for `FindUsersByAccounts` to return correct SiteIDs.
3. Construct a NATS JetStream subscription on `chat.msg.canonical.<siteID>.created` AND `chat.user.*.event.subscription.update` AND `outbox.<siteID>.to.<remoteSite>.room_renamed` (collect messages with a wait group).
4. Publish a `RenameRoomRequest` to `ROOMS_<siteID>` with subject `chat.room.canonical.<siteID>.room.rename` and an `X-Request-ID` header.
5. Assert Mongo state: room name updated, every subscription's `Name` updated.
6. Assert published events: one canonical sys message with `Type=room_renamed`, two SubscriptionUpdateEvents, one outbox publish.

- [ ] **Step 28.2: Write parallel test for visibility (restrict transition)**

Similar harness; assert role rewrite in subscriptions (bob → owner, alice → member), restricted+externalAccess on room and all subscriptions, outbox publish with `OwnerAccount` populated.

- [ ] **Step 28.3: Run, verify pass**

Run: `make test-integration SERVICE=room-worker`
Expected: PASS.

- [ ] **Step 28.4: Commit**

```bash
git add room-worker/integration_test.go
git commit -m "test(room-worker): integration tests for processRoomRename and processRoomVisibility"
```

---

## Phase 8: inbox-worker handlers

### Task 29: Implement `handleRoomRenamed`

**Files:**
- Modify: `inbox-worker/handler.go`
- Modify: `inbox-worker/handler_test.go`

- [ ] **Step 29.1: Write failing unit tests**

Append to `inbox-worker/handler_test.go`:

```go
func TestHandleRoomRenamed_HappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockInboxStore(ctrl)

	store.EXPECT().UpdateSubscriptionNamesForRoom(gomock.Any(), "r1", "new").Return(nil)

	h := NewHandler(store)
	payload, _ := json.Marshal(model.RoomRenamedOutboxPayload{RoomID: "r1", NewName: "new", Timestamp: 1700000000000})
	evt := model.OutboxEvent{Type: model.OutboxRoomRenamed, SiteID: "site-a", DestSiteID: "site-b", Payload: payload, Timestamp: 1700000000000}
	data, _ := json.Marshal(evt)
	require.NoError(t, h.HandleEvent(context.Background(), data))
}

func TestHandleRoomRenamed_PermanentOnUnmarshal(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockInboxStore(ctrl)

	h := NewHandler(store)
	evt := model.OutboxEvent{Type: model.OutboxRoomRenamed, Payload: []byte("not json")}
	data, _ := json.Marshal(evt)
	err := h.HandleEvent(context.Background(), data)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errPermanent))
}
```

- [ ] **Step 29.2: Run, verify failure**

Run: `make test SERVICE=inbox-worker`
Expected: FAIL — `handleRoomRenamed` undefined.

- [ ] **Step 29.3: Implement the handler + add to dispatch switch**

In `inbox-worker/handler.go`, add a new case to the `HandleEvent` switch:

```go
	case "room_renamed":
		return h.handleRoomRenamed(ctx, &evt)
```

Append the handler function:

```go
func (h *Handler) handleRoomRenamed(ctx context.Context, evt *model.OutboxEvent) error {
	var payload model.RoomRenamedOutboxPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return newPermanent("unmarshal room_renamed payload: %s", err.Error())
	}
	if err := h.store.UpdateSubscriptionNamesForRoom(ctx, payload.RoomID, payload.NewName); err != nil {
		return fmt.Errorf("update subscription names for room %s: %w", payload.RoomID, err)
	}
	return nil
}
```

- [ ] **Step 29.4: Run, verify pass**

Run: `make test SERVICE=inbox-worker`
Expected: PASS.

- [ ] **Step 29.5: Commit**

```bash
git add inbox-worker/handler.go inbox-worker/handler_test.go
git commit -m "feat(inbox-worker): implement handleRoomRenamed"
```

---

### Task 30: Implement `handleRoomVisibilityChanged`

**Files:**
- Modify: `inbox-worker/handler.go`
- Modify: `inbox-worker/handler_test.go`

- [ ] **Step 30.1: Write failing unit tests**

Append to `inbox-worker/handler_test.go`:

```go
func TestHandleRoomVisibilityChanged_HappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockInboxStore(ctrl)

	store.EXPECT().ApplySubscriptionVisibility(gomock.Any(), "r1", true, false, "bob").Return(nil)

	h := NewHandler(store)
	payload, _ := json.Marshal(model.RoomVisibilityOutboxPayload{
		RoomID: "r1", Restricted: true, ExternalAccess: false, OwnerAccount: "bob", Timestamp: 1700000000000,
	})
	evt := model.OutboxEvent{Type: model.OutboxRoomVisibilityChanged, Payload: payload, Timestamp: 1700000000000}
	data, _ := json.Marshal(evt)
	require.NoError(t, h.HandleEvent(context.Background(), data))
}

func TestHandleRoomVisibilityChanged_PermanentOnUnmarshal(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockInboxStore(ctrl)

	h := NewHandler(store)
	evt := model.OutboxEvent{Type: model.OutboxRoomVisibilityChanged, Payload: []byte("not json")}
	data, _ := json.Marshal(evt)
	err := h.HandleEvent(context.Background(), data)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errPermanent))
}
```

- [ ] **Step 30.2: Run, verify failure**

Run: `make test SERVICE=inbox-worker`
Expected: FAIL.

- [ ] **Step 30.3: Implement the handler + add to dispatch switch**

In `inbox-worker/handler.go`, add to `HandleEvent` switch:

```go
	case "room_visibility_changed":
		return h.handleRoomVisibilityChanged(ctx, &evt)
```

Append:

```go
func (h *Handler) handleRoomVisibilityChanged(ctx context.Context, evt *model.OutboxEvent) error {
	var payload model.RoomVisibilityOutboxPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return newPermanent("unmarshal room_visibility_changed payload: %s", err.Error())
	}
	if err := h.store.ApplySubscriptionVisibility(ctx, payload.RoomID, payload.Restricted, payload.ExternalAccess, payload.OwnerAccount); err != nil {
		return fmt.Errorf("apply subscription visibility for room %s: %w", payload.RoomID, err)
	}
	return nil
}
```

- [ ] **Step 30.4: Run, verify pass**

Run: `make test SERVICE=inbox-worker`
Expected: PASS.

- [ ] **Step 30.5: Commit**

```bash
git add inbox-worker/handler.go inbox-worker/handler_test.go
git commit -m "feat(inbox-worker): implement handleRoomVisibilityChanged"
```

---

### Task 31: inbox-worker integration test

**Files:**
- Modify: `inbox-worker/integration_test.go`

- [ ] **Step 31.1: Write end-to-end integration tests**

Append two tests to `inbox-worker/integration_test.go`:

1. **`TestIntegration_HandleRoomRenamed`** — seed local subscriptions for federated users on `r1`, publish an outbox event onto the inbox subject, assert subscription names update.
2. **`TestIntegration_HandleRoomVisibilityChanged`** — seed subscriptions (alice=owner, bob=member, carol=member), publish a visibility outbox event with `OwnerAccount=bob`, assert bob's mirrored sub becomes owner, others demoted, flags set.

Use the existing inbox-worker integration test harness (testcontainers + NATS).

- [ ] **Step 31.2: Run, verify pass**

Run: `make test-integration SERVICE=inbox-worker`
Expected: PASS.

- [ ] **Step 31.3: Commit**

```bash
git add inbox-worker/integration_test.go
git commit -m "test(inbox-worker): integration tests for rename and visibility handlers"
```

---

## Phase 9: Docs, mock, and final checks

### Task 32: Update `mock-user-service` to seed `admin1` role

**Files:**
- Modify: `mock-user-service/handler.go`
- Modify: `mock-user-service/handler_test.go`

> **Discovery note:** `mock-user-service` today does not return `model.User` records directly — it serves status/profile/subscription endpoints. The spec calls for `admin1` → `[UserRoleAdmin]`. Before implementing, run:
>
> ```bash
> grep -rn "model.User\|users.InsertOne\|UpsertUser" mock-user-service/
> ```
>
> If a User-returning endpoint exists, add Roles to its response. Otherwise, add a small helper that returns `Roles=[UserRoleAdmin]` for `admin1` and `Roles=[UserRoleUser]` for all other accounts, exposed via any User-shaped RPC the service serves. If no such RPC exists, the seeding mechanism is implementation-defined — pair with the team to decide whether to add a `userProfileGet` endpoint that returns the full `model.User` (including Roles), or to seed `users` collection directly via a startup script.
>
> Verify the choice doesn't break the existing `room-service.GetUser` flow, which reads from local Mongo's `users` collection.

- [ ] **Step 32.1: Write failing test asserting `admin1` gets admin role**

In `mock-user-service/handler_test.go`, add a test that calls whichever endpoint surfaces user roles (or asserts the helper directly):

```go
func TestMockUser_AdminRole(t *testing.T) {
	// If a buildMockUser helper exists, assert it returns admin for admin1
	// and user otherwise.
	admin := buildMockUser("admin1", "site-a")
	assert.Equal(t, []model.UserRole{model.UserRoleAdmin}, admin.Roles)

	normal := buildMockUser("alice", "site-a")
	assert.Equal(t, []model.UserRole{model.UserRoleUser}, normal.Roles)
}
```

- [ ] **Step 32.2: Run, verify failure**

Run: `make test SERVICE=mock-user-service`
Expected: FAIL.

- [ ] **Step 32.3: Implement the helper / endpoint addition**

Add to `mock-user-service/handler.go`:

```go
// buildMockUser returns a User record with roles seeded for testing.
// "admin1" gets UserRoleAdmin; all others get UserRoleUser.
func buildMockUser(account, siteID string) model.User {
	roles := []model.UserRole{model.UserRoleUser}
	if account == "admin1" {
		roles = []model.UserRole{model.UserRoleAdmin}
	}
	return model.User{
		ID:      "mock-user-" + account,
		Account: account,
		SiteID:  siteID,
		Roles:   roles,
	}
}
```

If you discovered a User-returning endpoint, wire `buildMockUser` into it. If not, expose a new endpoint that downstream services (or seed scripts) can call:

```go
type userGetReq struct {
	Account string `json:"account"`
}

func (h *Handler) userGet(c *natsrouter.Context, req userGetReq) (*model.User, error) {
	if err := h.checkSite(c); err != nil {
		return nil, err
	}
	u := buildMockUser(req.Account, h.siteID)
	return &u, nil
}
```

Register it in `Register()` and add a corresponding `subject.UserGetPattern` if appropriate (or follow whatever the team confirms during discovery).

- [ ] **Step 32.4: Run, verify pass**

Run: `make test SERVICE=mock-user-service`
Expected: PASS.

- [ ] **Step 32.5: Commit**

```bash
git add mock-user-service/handler.go mock-user-service/handler_test.go
git commit -m "feat(mock-user-service): seed admin1 with admin role"
```

---

### Task 33: Update `docs/client-api.md`

**Files:**
- Modify: `docs/client-api.md`

- [ ] **Step 33.1: Add two new RPC sections under §3.1 (room-service)**

Following the format of the existing "Update Member Role" section (around line 573-653), add two sections:

#### Rename Room
- **Subject:** `chat.user.{account}.request.room.{roomID}.{siteID}.room.rename`
- **Request body:**
  | Field | Type | Required | Description |
  |-------|------|----------|-------------|
  | `roomId` | string | yes | Channel room ID (must match subject) |
  | `newName` | string | yes | New room name (1-100 chars after trim) |
- **Success response:**
  ```json
  {"status": "accepted", "requestId": "<uuid>"}
  ```
- **Error responses:** `"invalid name"`, `"room not found"`, `"rename is only allowed in channel rooms"`, `"only owners or admins can rename a channel"`, `"invalid request"`, `"missing X-Request-ID header"`, `"invalid X-Request-ID format"`.
- **Triggered events:**
  - One canonical sys-message of type `room_renamed` on the room's `chat.room.{roomID}.event` subject (via broadcast-worker fan-out from the canonical stream).
  - One `SubscriptionUpdateEvent{Action:"renamed"}` per subscription on `chat.user.{account}.event.subscription.update`.
  - One cross-site outbox event per remote site that has federated subscribers.
- **AsyncJobResult:** Operation `room.rename` on `chat.user.{account}.response.{requestId}`.

#### Set Room Visibility
- **Subject:** `chat.user.{account}.request.room.{roomID}.{siteID}.room.visibility`
- **Request body:**
  | Field | Type | Required | Description |
  |-------|------|----------|-------------|
  | `roomId` | string | yes | Channel room ID (must match subject) |
  | `restricted` | bool | yes | Final restricted state (read-modify-write semantics) |
  | `externalAccess` | bool | yes | Final externalAccess state |
  | `ownerAccount` | string | conditional | Required when transitioning false→true; optional otherwise (when supplied with `restricted=true`, that account becomes sole owner) |
- **Success response:** `{"status": "accepted", "requestId": "<uuid>"}`
- **Error responses:** `"only admins can change room visibility"`, `"room not found"`, `"visibility change is only allowed in channel rooms"`, `"owner account is required when restricting a room"`, `"owner account is not a member of this room"`, `"not enough members to restrict (need at least N)"`, `"invalid request"`, request-ID errors.
- **Triggered events:**
  - One `SubscriptionUpdateEvent{Action:"visibility_changed"}` per subscription (payload carries new `Restricted`/`ExternalAccess`/`Roles`).
  - One cross-site outbox event per remote site that has federated subscribers.
  - **No sys message.**
- **AsyncJobResult:** Operation `room.visibility` on `chat.user.{account}.response.{requestId}`.

- [ ] **Step 33.2: Commit**

```bash
git add docs/client-api.md
git commit -m "docs(client-api): document room.rename and room.visibility RPCs"
```

---

### Task 34: Final lint, full test, SAST

**Files:** none (validation only).

- [ ] **Step 34.1: Run lint**

Run: `make lint`
Expected: PASS.
If failures: fix and re-run.

- [ ] **Step 34.2: Run all unit tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 34.3: Run all integration tests**

Run: `make test-integration`
Expected: PASS (Docker required).

- [ ] **Step 34.4: Run SAST**

Run: `make sast`
Expected: PASS (no medium+ findings).
If any: triage with the `sast_triage` skill.

- [ ] **Step 34.5: Push**

```bash
git push -u origin <current-branch>
```

---

## Notes for Implementor

- **Don't combine commits.** Each task ends with one commit. Squashing the whole branch later is fine; mid-implementation it's the per-task commits that make review tractable.
- **Spec line numbers will drift.** Use the spec's section headings (e.g. "§Validation Rules", "§Processing") as anchors when re-reading the spec.
- **When ambiguity arises** in adapting code (e.g. exact existing variable names, helper function presence), prefer to match the existing patterns in the file rather than inventing new ones. Check 2-3 sibling handlers for shape before writing.
- **Per-test isolation in integration tests** is the caller's responsibility — every test in the same package shares Mongo databases / NATS via `testutil` helpers but uses per-test prefixes (`t.Name()` hashed) for isolation. Don't drop the `testutil.MongoDB(t, "...")` pattern.
- **No silent state.** If a step's expected outcome doesn't match what you see, stop and investigate — don't move on with a yellow checkmark.
- **Observability (spec §Observability):** Every new handler (`handleRoomRename`, `handleRoomVisibility`, `processRoomRename`, `processRoomVisibility`, `handleRoomRenamed`, `handleRoomVisibilityChanged`) MUST log an `slog.Info` at entry with structured fields `op`, `requester`, `roomID`, `requestID`. Validation rejects in `room-service` MUST log at INFO with a `reason` field. The plan's implementation snippets omit these for brevity — add them when implementing. Example for `processRoomRename` step 25.3:

  ```go
  slog.Info("processing room.rename",
      "op", model.AsyncJobOpRoomRename,
      "requester", req.Account,
      "roomID", req.RoomID,
      "requestID", requestID)
  ```
- **Retry semantics test (spec §Processing):** Task 25 includes an explicit error-then-ok sequence test. The pattern (call processor twice with different mock setups, assert two `AsyncJobResult` publishes with `Status:"error"` then `Status:"ok"`) MUST be mirrored for `processRoomVisibility` in Task 26.1. Test added in the rename task is the template.
