# Add Member Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the add-member feature end-to-end — from shared model types through room-service validation, room-worker persistence/event publishing, inbox-worker cross-site replication, and message-worker system message support.

**Architecture:** Two-phase processing: room-service validates and publishes to ROOMS canonical stream; room-worker consumes, persists, and fans out events. Cross-site members are replicated via outbox → inbox-worker. System messages flow through the existing MESSAGES_CANONICAL pipeline.

**Tech Stack:** Go 1.25, MongoDB aggregation pipelines, NATS JetStream, `go.uber.org/mock`, `testify`, `testcontainers-go`

**Spec:** `docs/superpowers/specs/2026-04-14-add-member-design.md`

---

## Section 1: Model & Subject Layer

### Task 1: Update Room model — replace RoomTypeGroup with RoomTypeChannel, add Restricted

**Files:**
- Modify: `pkg/model/room.go:7-10` (constants), `pkg/model/room.go:12-24` (Room struct)
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing test for new RoomType and Restricted field**

Add to `pkg/model/model_test.go`:

```go
func TestRoomTypeChannel(t *testing.T) {
	assert.Equal(t, model.RoomType("channel"), model.RoomTypeChannel)
}

func TestRoom_RestrictedJSON(t *testing.T) {
	room := model.Room{
		ID:         "r1",
		Name:       "general",
		Type:       model.RoomTypeChannel,
		Restricted: true,
		SiteID:     "site-a",
	}
	data, err := json.Marshal(room)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Equal(t, "channel", m["type"])
	assert.Equal(t, true, m["restricted"])

	// omitempty: false value should omit field
	room.Restricted = false
	data2, _ := json.Marshal(room)
	var m2 map[string]any
	require.NoError(t, json.Unmarshal(data2, &m2))
	_, exists := m2["restricted"]
	assert.False(t, exists, "restricted=false should be omitted")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: Compilation error — `model.RoomTypeChannel` undefined, `model.Room` has no field `Restricted`

- [ ] **Step 3: Update room.go**

In `pkg/model/room.go`, replace the constants block:

```go
const (
	RoomTypeDM      RoomType = "dm"
	RoomTypeChannel RoomType = "channel"
)
```

Add field to `Room` struct after `UpdatedAt`:

```go
	Restricted bool `json:"restricted,omitempty" bson:"restricted,omitempty"`
```

- [ ] **Step 4: Fix callers — replace RoomTypeGroup references**

Search for `RoomTypeGroup` across the codebase. Update any references to use `RoomTypeChannel` or `RoomTypeDM` as appropriate. If `model_test.go` has tests asserting `RoomTypeGroup == "group"`, update them to assert `RoomTypeChannel == "channel"`.

- [ ] **Step 5: Run tests**

Run: `make test SERVICE=pkg/model`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/model/room.go pkg/model/model_test.go
git commit -m "feat(model): replace RoomTypeGroup with RoomTypeChannel, add Restricted field"
```

---

### Task 2: Add SectID to User model

**Files:**
- Modify: `pkg/model/user.go:3-10`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing test**

Add to `pkg/model/model_test.go`:

```go
func TestUser_SectIDJSON(t *testing.T) {
	user := model.User{
		ID:      "u1",
		Account: "alice",
		SiteID:  "site-a",
		SectID:  "engineering",
	}
	var dst model.User
	data, err := json.Marshal(user)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, "engineering", dst.SectID)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: Compilation error — `model.User` has no field `SectID`

- [ ] **Step 3: Add SectID field to User struct**

In `pkg/model/user.go`, add after `SiteID`:

```go
	SectID      string `json:"sectId"       bson:"sectId"`
```

- [ ] **Step 4: Run tests**

Run: `make test SERVICE=pkg/model`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/model/user.go pkg/model/model_test.go
git commit -m "feat(model): add SectID field to User for org resolution"
```

---

### Task 3: Change Subscription.Role to Roles []Role

**Files:**
- Modify: `pkg/model/subscription.go:22` (field change)
- Modify: `room-service/handler.go` (callers)
- Modify: `room-worker/handler.go` (callers)
- Modify: `inbox-worker/handler.go` (callers)

- [ ] **Step 1: Write failing test**

Add to `pkg/model/model_test.go`:

```go
func TestSubscription_RolesJSON(t *testing.T) {
	sub := model.Subscription{
		ID:     "s1",
		RoomID: "r1",
		Roles:  []model.Role{model.RoleOwner, model.RoleMember},
	}
	data, err := json.Marshal(sub)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	roles := m["roles"].([]any)
	assert.Len(t, roles, 2)
	assert.Equal(t, "owner", roles[0])
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: Compilation error — `model.Subscription` has no field `Roles`

- [ ] **Step 3: Change field in subscription.go**

In `pkg/model/subscription.go`, replace line 22:

```go
	Role               Role             `json:"role" bson:"role"`
```

with:

```go
	Roles              []Role           `json:"roles" bson:"roles"`
```

- [ ] **Step 4: Fix all callers**

This is a breaking change. Fix every file that references `.Role` on a `Subscription`:

**room-service/handler.go:116** — `Role: model.RoleOwner` → `Roles: []model.Role{model.RoleOwner}`

**room-service/handler.go:139** — `sub.Role != model.RoleOwner` → add a temporary local helper:

```go
func containsRole(roles []model.Role, target model.Role) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}
```

Then replace `sub.Role != model.RoleOwner` with `!containsRole(sub.Roles, model.RoleOwner)`.

**room-worker/handler.go:49** — `Role: model.RoleMember` → `Roles: []model.Role{model.RoleMember}`

**inbox-worker/handler.go:69** — `Role: model.RoleMember` → `Roles: []model.Role{model.RoleMember}`

Also fix any test files that set `.Role` on a Subscription.

- [ ] **Step 5: Run full test suite**

Run: `make test`
Expected: PASS (all services compile and tests pass)

- [ ] **Step 6: Commit**

```bash
git add pkg/model/subscription.go room-service/handler.go room-worker/handler.go inbox-worker/handler.go
git commit -m "feat(model): change Subscription.Role to Roles slice"
```

---

### Task 4: Add Message.Type and Message.SysMsgData fields

**Files:**
- Modify: `pkg/model/message.go:5-14`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing test**

Add to `pkg/model/model_test.go`:

```go
func TestMessage_TypeAndSysMsgDataJSON(t *testing.T) {
	sysData := []byte(`{"individuals":["alice"]}`)
	msg := model.Message{
		ID:         "m1",
		RoomID:     "r1",
		Content:    "added members",
		Type:       "members_added",
		SysMsgData: sysData,
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var dst model.Message
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, "members_added", dst.Type)
	assert.Equal(t, sysData, dst.SysMsgData)

	// omitempty: regular message without type
	regular := model.Message{ID: "m2", RoomID: "r1", Content: "hello"}
	data2, _ := json.Marshal(regular)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data2, &m))
	_, hasType := m["type"]
	assert.False(t, hasType, "type should be omitted for regular messages")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: Compilation error

- [ ] **Step 3: Add fields to Message struct**

In `pkg/model/message.go`, add after the last existing field (before the closing `}`):

```go
	Type                         string     `json:"type,omitempty"       bson:"type,omitempty"`
	SysMsgData                   []byte     `json:"sysMsgData,omitempty" bson:"sysMsgData,omitempty"`
```

- [ ] **Step 4: Run tests**

Run: `make test SERVICE=pkg/model`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/model/message.go pkg/model/model_test.go
git commit -m "feat(model): add Type and SysMsgData fields to Message for system messages"
```

---

### Task 5: Create member.go with AddMembersRequest, RoomMember, HistoryConfig, MembersAdded

**Files:**
- Create: `pkg/model/member.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing tests**

Add to `pkg/model/model_test.go`:

```go
func TestAddMembersRequestJSON(t *testing.T) {
	req := model.AddMembersRequest{
		RoomID:   "r1",
		Users:    []string{"alice", "bob"},
		Orgs:     []string{"engineering"},
		Channels: []string{"c1"},
		History:  model.HistoryConfig{Mode: model.HistoryModeNone},
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	var dst model.AddMembersRequest
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, req.RoomID, dst.RoomID)
	assert.Equal(t, model.HistoryModeNone, dst.History.Mode)
	assert.Equal(t, req.Users, dst.Users)
}

func TestHistoryModeConstants(t *testing.T) {
	assert.Equal(t, model.HistoryMode("none"), model.HistoryModeNone)
	assert.Equal(t, model.HistoryMode("all"), model.HistoryModeAll)
}

func TestRoomMemberJSON(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	rm := model.RoomMember{
		ID:     "rm1",
		RoomID: "r1",
		Ts:     now,
		Member: model.RoomMemberEntry{
			ID:   "org-eng",
			Type: model.RoomMemberOrg,
		},
	}
	data, err := json.Marshal(rm)
	require.NoError(t, err)
	var dst model.RoomMember
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, model.RoomMemberOrg, dst.Member.Type)
}

func TestMembersAddedJSON(t *testing.T) {
	ma := model.MembersAdded{
		Individuals:     []string{"alice"},
		Orgs:            []string{"eng"},
		Channels:        []string{"c1"},
		AddedUsersCount: 5,
	}
	data, err := json.Marshal(ma)
	require.NoError(t, err)
	var dst model.MembersAdded
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, 5, dst.AddedUsersCount)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: Compilation errors — all new types undefined

- [ ] **Step 3: Create pkg/model/member.go**

```go
package model

import "time"

type RoomMemberType string

const (
	RoomMemberIndividual RoomMemberType = "individual"
	RoomMemberOrg        RoomMemberType = "org"
)

type HistoryMode string

const (
	HistoryModeNone HistoryMode = "none"
	HistoryModeAll  HistoryMode = "all"
)

type HistoryConfig struct {
	Mode HistoryMode `json:"mode" bson:"mode"`
}

type AddMembersRequest struct {
	RoomID   string        `json:"roomId"   bson:"roomId"`
	Users    []string      `json:"users"    bson:"users"`
	Orgs     []string      `json:"orgs"     bson:"orgs"`
	Channels []string      `json:"channels" bson:"channels"`
	History  HistoryConfig `json:"history"  bson:"history"`
}

type RoomMemberEntry struct {
	ID      string         `json:"id"                bson:"id"`
	Type    RoomMemberType `json:"type"              bson:"type"`
	Account string         `json:"account,omitempty" bson:"account,omitempty"`
}

type RoomMember struct {
	ID     string          `json:"id"  bson:"_id"`
	RoomID string          `json:"rid" bson:"rid"`
	Ts     time.Time       `json:"ts"  bson:"ts"`
	Member RoomMemberEntry `json:"member" bson:"member"`
}

type MembersAdded struct {
	Individuals     []string `json:"individuals"`
	Orgs            []string `json:"orgs"`
	Channels        []string `json:"channels"`
	AddedUsersCount int      `json:"addedUsersCount"`
}
```

- [ ] **Step 4: Run tests**

Run: `make test SERVICE=pkg/model`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/model/member.go pkg/model/model_test.go
git commit -m "feat(model): add member types — AddMembersRequest, RoomMember, HistoryConfig, MembersAdded"
```

---

### Task 6: Add MemberAddEvent to event.go

**Files:**
- Modify: `pkg/model/event.go` (add after OutboxEvent, line 49)
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing test**

Add to `pkg/model/model_test.go`:

```go
func TestMemberAddEventJSON(t *testing.T) {
	evt := model.MemberAddEvent{
		Type:               "member_added",
		RoomID:             "r1",
		Accounts:           []string{"alice", "bob"},
		SiteID:             "site-a",
		JoinedAt:           1713200000000,
		HistorySharedSince: 1713200000000,
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)

	var dst model.MemberAddEvent
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, "member_added", dst.Type)
	assert.Equal(t, []string{"alice", "bob"}, dst.Accounts)
	assert.Equal(t, int64(1713200000000), dst.JoinedAt)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: Compilation error — `model.MemberAddEvent` undefined

- [ ] **Step 3: Add MemberAddEvent to event.go**

Add after `OutboxEvent` struct in `pkg/model/event.go`:

```go
type MemberAddEvent struct {
	Type               string   `json:"type"               bson:"type"`
	RoomID             string   `json:"roomId"             bson:"roomId"`
	Accounts           []string `json:"accounts"           bson:"accounts"`
	SiteID             string   `json:"siteId"             bson:"siteId"`
	JoinedAt           int64    `json:"joinedAt"           bson:"joinedAt"`
	HistorySharedSince int64    `json:"historySharedSince" bson:"historySharedSince"`
	Timestamp          int64    `json:"timestamp"          bson:"timestamp"`
}
```

- [ ] **Step 4: Run tests**

Run: `make test SERVICE=pkg/model`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/model/event.go pkg/model/model_test.go
git commit -m "feat(model): add MemberAddEvent with event-sourced subscription fields"
```

---

### Task 7: Add subject builders — MemberAdd, RoomCanonical, RoomMemberEvent

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write failing tests**

Add test cases to the existing `TestSubjectBuilders` table in `pkg/subject/subject_test.go`:

```go
{"MemberAdd", subject.MemberAdd("alice", "r1", "site-a"),
	"chat.user.alice.request.room.r1.site-a.member.add"},
{"RoomCanonical", subject.RoomCanonical("site-a", "member.add"),
	"chat.room.canonical.site-a.member.add"},
{"RoomMemberEvent", subject.RoomMemberEvent("r1"),
	"chat.room.r1.event.member"},
```

Add test cases to the existing `TestWildcardPatterns` table:

```go
{"MemberAddWild", subject.MemberAddWildcard("site-a"),
	"chat.user.*.request.room.*.site-a.member.add"},
{"RoomCanonicalWild", subject.RoomCanonicalWildcard("site-a"),
	"chat.room.canonical.site-a.>"},
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/subject`
Expected: Compilation error — undefined functions

- [ ] **Step 3: Add builders to subject.go**

Add to `pkg/subject/subject.go` after the existing `MemberInvite` function (around line 62):

```go
func MemberAdd(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.add", account, roomID, siteID)
}
```

Add after `MemberInviteWildcard` (around line 126):

```go
func MemberAddWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.add", siteID)
}
```

Add after the existing event builders:

```go
func RoomMemberEvent(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event.member", roomID)
}

func RoomCanonical(siteID, operation string) string {
	return fmt.Sprintf("chat.room.canonical.%s.%s", siteID, operation)
}

func RoomCanonicalWildcard(siteID string) string {
	return fmt.Sprintf("chat.room.canonical.%s.>", siteID)
}
```

- [ ] **Step 4: Run tests**

Run: `make test SERVICE=pkg/subject`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `make test`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add MemberAdd, RoomCanonical, RoomMemberEvent builders"
```

---

## Section 2: room-service Store Methods

### Task 8: Add roomMembers and users collections to MongoStore

**Files:**
- Modify: `room-service/store_mongo.go:13-23`

- [ ] **Step 1: Add collections to MongoStore struct and constructor**

In `room-service/store_mongo.go`, update `MongoStore`:

```go
type MongoStore struct {
	rooms         *mongo.Collection
	subscriptions *mongo.Collection
	roomMembers   *mongo.Collection
	users         *mongo.Collection
}
```

Update `NewMongoStore`:

```go
func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		rooms:         db.Collection("rooms"),
		subscriptions: db.Collection("subscriptions"),
		roomMembers:   db.Collection("room_members"),
		users:         db.Collection("users"),
	}
}
```

- [ ] **Step 2: Verify compilation**

Run: `make build SERVICE=room-service`
Expected: Compiles successfully

- [ ] **Step 3: Commit**

```bash
git add room-service/store_mongo.go
git commit -m "feat(room-service): add roomMembers and users collections to MongoStore"
```

---

### Task 9: Add CountSubscriptions and GetRoomMembersByRooms

**Files:**
- Modify: `room-service/store.go` (add to interface)
- Modify: `room-service/store_mongo.go` (add implementations)
- Modify: `room-service/integration_test.go`

- [ ] **Step 1: Write integration tests**

Add to `room-service/integration_test.go`:

```go
func TestMongoStore_CountSubscriptions(t *testing.T) {
	ctx := context.Background()
	store := setupMongo(t)

	roomID := "count-room"
	for _, acc := range []string{"alice", "bob", "helper.bot"} {
		_, err := store.(*MongoStore).subscriptions.InsertOne(ctx, model.Subscription{
			ID: uuid.New().String(), RoomID: roomID,
			User: model.SubscriptionUser{ID: acc, Account: acc},
		})
		require.NoError(t, err)
	}

	count, err := store.CountSubscriptions(ctx, roomID)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "bots should be excluded from count")
}

func TestMongoStore_GetRoomMembersByRooms(t *testing.T) {
	ctx := context.Background()
	store := setupMongo(t)

	now := time.Now().UTC()
	ms := store.(*MongoStore)
	ms.roomMembers.InsertOne(ctx, model.RoomMember{ID: "rm1", RoomID: "roomA", Ts: now,
		Member: model.RoomMemberEntry{ID: "eng", Type: model.RoomMemberOrg}})
	ms.roomMembers.InsertOne(ctx, model.RoomMember{ID: "rm2", RoomID: "roomA", Ts: now,
		Member: model.RoomMemberEntry{ID: "alice", Type: model.RoomMemberIndividual, Account: "alice"}})
	ms.roomMembers.InsertOne(ctx, model.RoomMember{ID: "rm3", RoomID: "roomB", Ts: now,
		Member: model.RoomMemberEntry{ID: "bob", Type: model.RoomMemberIndividual, Account: "bob"}})

	members, err := store.GetRoomMembersByRooms(ctx, []string{"roomA", "roomB"})
	require.NoError(t, err)
	assert.Len(t, members, 3)

	members2, err := store.GetRoomMembersByRooms(ctx, []string{})
	require.NoError(t, err)
	assert.Empty(t, members2)
}
```

- [ ] **Step 2: Run integration test to verify it fails**

Run: `make test-integration SERVICE=room-service`
Expected: Compilation error — methods not on interface

- [ ] **Step 3: Add methods to interface and implement**

Add to `RoomStore` interface in `room-service/store.go`:

```go
	CountSubscriptions(ctx context.Context, roomID string) (int, error)
	GetRoomMembersByRooms(ctx context.Context, roomIDs []string) ([]model.RoomMember, error)
```

Add to `room-service/store_mongo.go`:

```go
func (s *MongoStore) CountSubscriptions(ctx context.Context, roomID string) (int, error) {
	filter := bson.M{
		"roomId": roomID,
		"u.account": bson.M{
			"$not": bson.Regex{Pattern: `(\.bot$|^p_)`, Options: ""},
		},
	}
	count, err := s.subscriptions.CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("count subscriptions for room %q: %w", roomID, err)
	}
	return int(count), nil
}

func (s *MongoStore) GetRoomMembersByRooms(ctx context.Context, roomIDs []string) ([]model.RoomMember, error) {
	if len(roomIDs) == 0 {
		return nil, nil
	}
	cursor, err := s.roomMembers.Find(ctx, bson.M{"rid": bson.M{"$in": roomIDs}})
	if err != nil {
		return nil, fmt.Errorf("find room members: %w", err)
	}
	var members []model.RoomMember
	if err := cursor.All(ctx, &members); err != nil {
		return nil, fmt.Errorf("decode room members: %w", err)
	}
	return members, nil
}
```

- [ ] **Step 4: Regenerate mocks and run tests**

Run: `make generate SERVICE=room-service && make test-integration SERVICE=room-service`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add room-service/store.go room-service/store_mongo.go room-service/mock_store_test.go room-service/integration_test.go
git commit -m "feat(room-service): add CountSubscriptions and GetRoomMembersByRooms store methods"
```

---

### Task 10: Add GetAccountsByRooms (aggregation pipeline)

**Files:**
- Modify: `room-service/store.go`, `room-service/store_mongo.go`, `room-service/integration_test.go`

- [ ] **Step 1: Write integration test**

Add to `room-service/integration_test.go`:

```go
func TestMongoStore_GetAccountsByRooms(t *testing.T) {
	ctx := context.Background()
	store := setupMongo(t)
	ms := store.(*MongoStore)

	for _, s := range []model.Subscription{
		{ID: "s1", RoomID: "roomA", User: model.SubscriptionUser{ID: "u1", Account: "alice"}},
		{ID: "s2", RoomID: "roomA", User: model.SubscriptionUser{ID: "u2", Account: "bob"}},
		{ID: "s3", RoomID: "roomB", User: model.SubscriptionUser{ID: "u2", Account: "bob"}},
		{ID: "s4", RoomID: "roomB", User: model.SubscriptionUser{ID: "u3", Account: "charlie"}},
	} {
		_, err := ms.subscriptions.InsertOne(ctx, s)
		require.NoError(t, err)
	}

	accounts, err := store.GetAccountsByRooms(ctx, []string{"roomA", "roomB"})
	require.NoError(t, err)
	sort.Strings(accounts)
	assert.Equal(t, []string{"alice", "bob", "charlie"}, accounts)

	accounts2, err := store.GetAccountsByRooms(ctx, []string{})
	require.NoError(t, err)
	assert.Empty(t, accounts2)
}
```

- [ ] **Step 2: Run integration test to verify it fails**

Run: `make test-integration SERVICE=room-service`
Expected: Compilation error

- [ ] **Step 3: Add to interface and implement**

Add to `RoomStore` in `room-service/store.go`:

```go
	GetAccountsByRooms(ctx context.Context, roomIDs []string) ([]string, error)
```

Add to `room-service/store_mongo.go`:

```go
func (s *MongoStore) GetAccountsByRooms(ctx context.Context, roomIDs []string) ([]string, error) {
	if len(roomIDs) == 0 {
		return nil, nil
	}
	pipeline := bson.A{
		bson.M{"$match": bson.M{"roomId": bson.M{"$in": roomIDs}}},
		bson.M{"$group": bson.M{"_id": nil, "accounts": bson.M{"$addToSet": "$u.account"}}},
	}
	cursor, err := s.subscriptions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate accounts by rooms: %w", err)
	}
	var results []struct {
		Accounts []string `bson:"accounts"`
	}
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode accounts by rooms: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0].Accounts, nil
}
```

- [ ] **Step 4: Regenerate mocks and run tests**

Run: `make generate SERVICE=room-service && make test-integration SERVICE=room-service`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add room-service/store.go room-service/store_mongo.go room-service/mock_store_test.go room-service/integration_test.go
git commit -m "feat(room-service): add GetAccountsByRooms aggregation pipeline"
```

---

### Task 11: Add ResolveAccounts (aggregation pipeline with $lookup)

**Files:**
- Modify: `room-service/store.go`, `room-service/store_mongo.go`, `room-service/integration_test.go`

- [ ] **Step 1: Write integration tests**

Add to `room-service/integration_test.go`:

```go
func TestMongoStore_ResolveAccounts(t *testing.T) {
	ctx := context.Background()
	store := setupMongo(t)
	ms := store.(*MongoStore)

	for _, u := range []model.User{
		{ID: "u1", Account: "alice", SiteID: "site-a", SectID: "eng"},
		{ID: "u2", Account: "bob", SiteID: "site-a", SectID: "eng"},
		{ID: "u3", Account: "charlie", SiteID: "site-a", SectID: "sales"},
		{ID: "u4", Account: "helper.bot", SiteID: "site-a", SectID: "eng"},
		{ID: "u5", Account: "p_pipeline", SiteID: "site-a", SectID: "eng"},
	} {
		_, err := ms.users.InsertOne(ctx, u)
		require.NoError(t, err)
	}

	// alice already has a subscription in room1
	_, err := ms.subscriptions.InsertOne(ctx, model.Subscription{
		ID: "existing", RoomID: "room1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
	})
	require.NoError(t, err)

	t.Run("resolves orgs, filters bots, excludes existing", func(t *testing.T) {
		accounts, err := store.ResolveAccounts(ctx, []string{"eng"}, []string{"charlie"}, "room1")
		require.NoError(t, err)
		sort.Strings(accounts)
		assert.Equal(t, []string{"bob", "charlie"}, accounts)
	})

	t.Run("empty inputs", func(t *testing.T) {
		accounts, err := store.ResolveAccounts(ctx, nil, nil, "room1")
		require.NoError(t, err)
		assert.Empty(t, accounts)
	})

	t.Run("dedup across orgs and direct", func(t *testing.T) {
		accounts, err := store.ResolveAccounts(ctx, []string{"sales"}, []string{"charlie"}, "room1")
		require.NoError(t, err)
		assert.Equal(t, []string{"charlie"}, accounts)
	})
}
```

- [ ] **Step 2: Run integration test to verify it fails**

Run: `make test-integration SERVICE=room-service`
Expected: Compilation error

- [ ] **Step 3: Add to interface and implement**

Add to `RoomStore` in `room-service/store.go`:

```go
	ResolveAccounts(ctx context.Context, orgIDs, directAccounts []string, roomID string) ([]string, error)
```

Add to `room-service/store_mongo.go`:

```go
func (s *MongoStore) ResolveAccounts(ctx context.Context, orgIDs, directAccounts []string, roomID string) ([]string, error) {
	if len(orgIDs) == 0 && len(directAccounts) == 0 {
		return nil, nil
	}

	orFilter := bson.A{}
	if len(orgIDs) > 0 {
		orFilter = append(orFilter, bson.M{"sectId": bson.M{"$in": orgIDs}})
	}
	if len(directAccounts) > 0 {
		orFilter = append(orFilter, bson.M{"account": bson.M{"$in": directAccounts}})
	}

	pipeline := bson.A{
		bson.M{"$match": bson.M{
			"$or":     orFilter,
			"account": bson.M{"$not": bson.Regex{Pattern: `(\.bot$|^p_)`, Options: ""}},
		}},
		bson.M{"$lookup": bson.M{
			"from": "subscriptions",
			"let":  bson.M{"userAccount": "$account"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$roomId", roomID}},
					bson.M{"$eq": bson.A{"$u.account", "$$userAccount"}},
				}}}},
				bson.M{"$limit": 1},
			},
			"as": "existingSub",
		}},
		bson.M{"$match": bson.M{"existingSub": bson.M{"$eq": bson.A{}}}},
		bson.M{"$group": bson.M{"_id": nil, "accounts": bson.M{"$addToSet": "$account"}}},
	}

	cursor, err := s.users.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("resolve accounts: %w", err)
	}
	var results []struct {
		Accounts []string `bson:"accounts"`
	}
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode resolved accounts: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0].Accounts, nil
}
```

- [ ] **Step 4: Regenerate mocks and run tests**

Run: `make generate SERVICE=room-service && make test-integration SERVICE=room-service`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add room-service/store.go room-service/store_mongo.go room-service/mock_store_test.go room-service/integration_test.go
git commit -m "feat(room-service): add ResolveAccounts aggregation pipeline with bot filter and existing member exclusion"
```

---

## Section 3: room-service Handler

### Task 12: Create helper.go with utility functions

**Files:**
- Create: `room-service/helper.go`
- Create: `room-service/helper_test.go`

- [ ] **Step 1: Write failing tests**

Create `room-service/helper_test.go`:

```go
package main

import (
	"fmt"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/stretchr/testify/assert"
)

func TestHasRole(t *testing.T) {
	tests := []struct {
		name   string
		roles  []model.Role
		target model.Role
		want   bool
	}{
		{"owner in list", []model.Role{model.RoleOwner}, model.RoleOwner, true},
		{"not in list", []model.Role{model.RoleMember}, model.RoleOwner, false},
		{"empty list", nil, model.RoleOwner, false},
		{"multiple roles", []model.Role{model.RoleMember, model.RoleOwner}, model.RoleOwner, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HasRole(tt.roles, tt.target))
		})
	}
}

func TestIsBot(t *testing.T) {
	tests := []struct {
		account string
		want    bool
	}{
		{"alice", false},
		{"helper.bot", true},
		{"p_pipeline", true},
		{"robot", false},
	}
	for _, tt := range tests {
		t.Run(tt.account, func(t *testing.T) {
			assert.Equal(t, tt.want, isBot(tt.account))
		})
	}
}

func TestFilterBots(t *testing.T) {
	filtered := filterBots([]string{"alice", "helper.bot", "bob", "p_pipeline"})
	assert.Equal(t, []string{"alice", "bob"}, filtered)
}

func TestDedup(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, dedup([]string{"a", "b", "a", "c", "b"}))
	assert.Empty(t, dedup(nil))
}

func TestSanitizeError(t *testing.T) {
	assert.Equal(t, "only owners can add members to this room",
		sanitizeError(fmt.Errorf("only owners can add members to this room")))
	assert.Equal(t, "internal error",
		sanitizeError(fmt.Errorf("mongo connection: timeout")))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=room-service`
Expected: Compilation error — functions undefined

- [ ] **Step 3: Create room-service/helper.go**

```go
package main

import (
	"regexp"
	"strings"

	"github.com/hmchangw/chat/pkg/model"
)

var botPattern = regexp.MustCompile(`\.bot$|^p_`)

func HasRole(roles []model.Role, target model.Role) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}

func isBot(account string) bool {
	return botPattern.MatchString(account)
}

func filterBots(accounts []string) []string {
	var filtered []string
	for _, a := range accounts {
		if !isBot(a) {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

func dedup(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	var result []string
	for _, item := range items {
		if _, ok := seen[item]; !ok {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

func sanitizeError(err error) string {
	msg := err.Error()
	for _, safe := range []string{
		"only owners can",
		"cannot add members",
		"room is at maximum capacity",
		"requester not in room",
		"invalid",
	} {
		if strings.Contains(msg, safe) {
			return msg
		}
	}
	return "internal error"
}
```

- [ ] **Step 4: Remove temporary containsRole from handler.go** (if added in Task 3) and replace with `HasRole`.

- [ ] **Step 5: Run tests**

Run: `make test SERVICE=room-service`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add room-service/helper.go room-service/helper_test.go room-service/handler.go
git commit -m "feat(room-service): add helper.go with HasRole, isBot, filterBots, dedup, sanitizeError"
```

---

### Task 13: Update Handler — publishToStream takes subject parameter

**Files:**
- Modify: `room-service/handler.go:19-28` (struct + constructor)
- Modify: `room-service/handler.go:167` (existing handleInvite call)
- Modify: `room-service/main.go` (closure)
- Modify: `room-service/handler_test.go` (test helpers)

- [ ] **Step 1: Update publishToStream signature**

In `room-service/handler.go`, change struct field and constructor:

```go
type Handler struct {
	store           RoomStore
	siteID          string
	maxRoomSize     int
	publishToStream func(ctx context.Context, subj string, data []byte) error
}

func NewHandler(store RoomStore, siteID string, maxRoomSize int, publishToStream func(context.Context, string, []byte) error) *Handler {
	return &Handler{store: store, siteID: siteID, maxRoomSize: maxRoomSize, publishToStream: publishToStream}
}
```

- [ ] **Step 2: Fix handleInvite publish call**

Update `handleInvite` (around line 167) to pass subject:

```go
	if err := h.publishToStream(ctx, subject.MemberInvite(inviterAccount, roomID, h.siteID), timestampedData); err != nil {
```

Also update the role check to use `HasRole`:

```go
	if !HasRole(sub.Roles, model.RoleOwner) {
```

- [ ] **Step 3: Update main.go closure**

In `room-service/main.go`, update the `publishToStream` closure:

```go
	publishToStream := func(ctx context.Context, subj string, data []byte) error {
		_, err := js.Publish(ctx, subj, data)
		return err
	}
```

- [ ] **Step 4: Fix handler_test.go publish function signatures**

Update all test publish functions from `func(ctx context.Context, data []byte) error` to `func(ctx context.Context, subj string, data []byte) error`.

- [ ] **Step 5: Run tests**

Run: `make test SERVICE=room-service`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add room-service/handler.go room-service/main.go room-service/handler_test.go
git commit -m "refactor(room-service): update publishToStream to accept subject parameter"
```

---

### Task 14: Implement handleAddMembers and wire NATS subscription

**Files:**
- Modify: `room-service/handler.go`
- Modify: `room-service/handler_test.go`

- [ ] **Step 1: Write failing unit tests**

Add to `room-service/handler_test.go`:

```go
func TestHandler_AddMembers_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	var publishedSubj string
	var publishedData []byte
	publish := func(_ context.Context, subj string, data []byte) error {
		publishedSubj = subj
		publishedData = data
		return nil
	}

	h := NewHandler(store, "site-a", 100, publish)

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{
		Roles: []model.Role{model.RoleMember},
	}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{
		ID: "r1", Type: model.RoomTypeChannel, Restricted: false,
	}, nil)
	store.EXPECT().ResolveAccounts(gomock.Any(), []string{"eng"}, []string{"bob"}, "r1").
		Return([]string{"bob"}, nil)
	store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(5, nil)

	req := model.AddMembersRequest{
		RoomID: "r1", Users: []string{"bob"}, Orgs: []string{"eng"},
		History: model.HistoryConfig{Mode: model.HistoryModeNone},
	}
	reqData, _ := json.Marshal(req)

	resp, err := h.handleAddMembers(context.Background(), subject.MemberAdd("alice", "r1", "site-a"), reqData)
	require.NoError(t, err)

	var status map[string]string
	require.NoError(t, json.Unmarshal(resp, &status))
	assert.Equal(t, "accepted", status["status"])
	assert.Equal(t, subject.RoomCanonical("site-a", "member.add"), publishedSubj)

	var published model.AddMembersRequest
	require.NoError(t, json.Unmarshal(publishedData, &published))
	assert.Equal(t, []string{"bob"}, published.Users)
	assert.Empty(t, published.Channels)
}

func TestHandler_AddMembers_DMRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	h := NewHandler(store, "site-a", 100, nil)

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{
		Roles: []model.Role{model.RoleMember},
	}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{
		ID: "r1", Type: model.RoomTypeDM,
	}, nil)

	reqData, _ := json.Marshal(model.AddMembersRequest{RoomID: "r1", Users: []string{"bob"}})
	_, err := h.handleAddMembers(context.Background(), subject.MemberAdd("alice", "r1", "site-a"), reqData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot add members to a DM room")
}

func TestHandler_AddMembers_RestrictedNonOwnerRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	h := NewHandler(store, "site-a", 100, nil)

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{
		Roles: []model.Role{model.RoleMember},
	}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{
		ID: "r1", Type: model.RoomTypeChannel, Restricted: true,
	}, nil)

	reqData, _ := json.Marshal(model.AddMembersRequest{RoomID: "r1", Users: []string{"bob"}})
	_, err := h.handleAddMembers(context.Background(), subject.MemberAdd("alice", "r1", "site-a"), reqData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only owners can add members")
}

func TestHandler_AddMembers_CapacityExceeded(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	h := NewHandler(store, "site-a", 10, nil)

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{
		Roles: []model.Role{model.RoleOwner},
	}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{
		ID: "r1", Type: model.RoomTypeChannel,
	}, nil)
	store.EXPECT().ResolveAccounts(gomock.Any(), gomock.Any(), gomock.Any(), "r1").
		Return([]string{"b", "c", "d", "e", "f"}, nil)
	store.EXPECT().CountSubscriptions(gomock.Any(), "r1").Return(8, nil)

	reqData, _ := json.Marshal(model.AddMembersRequest{RoomID: "r1", Users: []string{"b", "c", "d", "e", "f"}})
	_, err := h.handleAddMembers(context.Background(), subject.MemberAdd("alice", "r1", "site-a"), reqData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum capacity")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=room-service`
Expected: Compilation error — `handleAddMembers` undefined

- [ ] **Step 3: Implement handleAddMembers + expandChannels + natsAddMembers**

Add to `room-service/handler.go`:

```go
func (h *Handler) natsAddMembers(m otelnats.Msg) {
	resp, err := h.handleAddMembers(m.Context(), m.Msg.Subject, m.Msg.Data)
	if err != nil {
		slog.Error("add members failed", "error", err)
		natsutil.ReplyError(m.Msg, sanitizeError(err))
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to add-members message", "error", err)
	}
}

func (h *Handler) handleAddMembers(ctx context.Context, subj string, data []byte) ([]byte, error) {
	requester, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid subject: %s", subj)
	}

	sub, err := h.store.GetSubscription(ctx, requester, roomID)
	if err != nil {
		return nil, fmt.Errorf("requester not in room: %w", err)
	}

	room, err := h.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("room not found: %w", err)
	}
	if room.Type != model.RoomTypeChannel {
		return nil, fmt.Errorf("cannot add members to a DM room")
	}
	if room.Restricted && !HasRole(sub.Roles, model.RoleOwner) {
		return nil, fmt.Errorf("only owners can add members to this room")
	}

	var req model.AddMembersRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	channelOrgIDs, channelAccounts, err := h.expandChannels(ctx, req.Channels)
	if err != nil {
		return nil, err
	}
	orgIDs := dedup(append(req.Orgs, channelOrgIDs...))
	directAccounts := dedup(append(req.Users, channelAccounts...))

	accounts, err := h.store.ResolveAccounts(ctx, orgIDs, directAccounts, roomID)
	if err != nil {
		return nil, fmt.Errorf("resolve accounts: %w", err)
	}
	if len(accounts) == 0 {
		return json.Marshal(map[string]string{"status": "accepted"})
	}

	existingCount, err := h.store.CountSubscriptions(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("count subscriptions: %w", err)
	}
	if existingCount+len(accounts) > h.maxRoomSize {
		return nil, fmt.Errorf("room is at maximum capacity (%d)", h.maxRoomSize)
	}

	normalizedReq := model.AddMembersRequest{
		RoomID: roomID, Users: accounts, Orgs: orgIDs, History: req.History,
	}
	normalizedData, err := json.Marshal(normalizedReq)
	if err != nil {
		return nil, fmt.Errorf("marshal normalized request: %w", err)
	}
	if err := h.publishToStream(ctx, subject.RoomCanonical(h.siteID, "member.add"), normalizedData); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "accepted"})
}

func (h *Handler) expandChannels(ctx context.Context, channelIDs []string) (orgIDs, accounts []string, err error) {
	if len(channelIDs) == 0 {
		return nil, nil, nil
	}

	members, err := h.store.GetRoomMembersByRooms(ctx, channelIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("get room members for channels: %w", err)
	}

	channelHasMembers := make(map[string]bool)
	for _, m := range members {
		channelHasMembers[m.RoomID] = true
		switch m.Member.Type {
		case model.RoomMemberOrg:
			orgIDs = append(orgIDs, m.Member.ID)
		case model.RoomMemberIndividual:
			accounts = append(accounts, m.Member.Account)
		}
	}

	var noMemberChannels []string
	for _, ch := range channelIDs {
		if !channelHasMembers[ch] {
			noMemberChannels = append(noMemberChannels, ch)
		}
	}
	if len(noMemberChannels) > 0 {
		subAccounts, err := h.store.GetAccountsByRooms(ctx, noMemberChannels)
		if err != nil {
			return nil, nil, fmt.Errorf("get accounts for channels: %w", err)
		}
		accounts = append(accounts, subAccounts...)
	}

	return orgIDs, accounts, nil
}
```

- [ ] **Step 4: Wire NATS subscription in RegisterCRUD**

Add before `return nil` in `RegisterCRUD`:

```go
	if _, err := nc.QueueSubscribe(subject.MemberAddWildcard(h.siteID), queue, h.natsAddMembers); err != nil {
		return err
	}
```

- [ ] **Step 5: Run tests**

Run: `make test SERVICE=room-service`
Expected: PASS

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: No errors

- [ ] **Step 7: Commit**

```bash
git add room-service/handler.go room-service/handler_test.go
git commit -m "feat(room-service): implement handleAddMembers with authorization, channel resolution, and capacity check"
```

---

## Section 4: room-worker processAddMembers

### Task 15: Extend SubscriptionStore with new methods

**Files:**
- Modify: `room-worker/store.go`
- Modify: `room-worker/store_mongo.go`
- Modify: `room-worker/handler.go` (fix IncrementUserCount caller)

- [ ] **Step 1: Update interface**

Replace `room-worker/store.go` interface:

```go
type SubscriptionStore interface {
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	BulkCreateSubscriptions(ctx context.Context, subs []*model.Subscription) error
	ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
	IncrementUserCount(ctx context.Context, roomID string, count int) error
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	CreateRoomMember(ctx context.Context, member *model.RoomMember) error
	FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error)
}
```

Note: `IncrementUserCount` gains a `count int` parameter. Fix existing `processInvite` call: `h.store.IncrementUserCount(ctx, req.RoomID)` → `h.store.IncrementUserCount(ctx, req.RoomID, 1)`.

- [ ] **Step 2: Add implementations to store_mongo.go**

Add `roomMembers` and `users` collections to `MongoStore`:

```go
type MongoStore struct {
	subscriptions *mongo.Collection
	rooms         *mongo.Collection
	roomMembers   *mongo.Collection
	users         *mongo.Collection
}
```

Update `NewMongoStore` to initialize all four. Add implementations:

```go
func (s *MongoStore) BulkCreateSubscriptions(ctx context.Context, subs []*model.Subscription) error {
	if len(subs) == 0 {
		return nil
	}
	docs := make([]any, len(subs))
	for i, sub := range subs {
		docs[i] = sub
	}
	opts := options.InsertMany().SetOrdered(false)
	_, err := s.subscriptions.InsertMany(ctx, docs, opts)
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("bulk create subscriptions: %w", err)
	}
	return nil
}

func (s *MongoStore) CreateRoomMember(ctx context.Context, member *model.RoomMember) error {
	_, err := s.roomMembers.InsertOne(ctx, member)
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("create room member: %w", err)
	}
	return nil
}

func (s *MongoStore) FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	cursor, err := s.users.Find(ctx, bson.M{"account": bson.M{"$in": accounts}})
	if err != nil {
		return nil, fmt.Errorf("find users by accounts: %w", err)
	}
	var users []model.User
	if err := cursor.All(ctx, &users); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}
	return users, nil
}
```

Update `IncrementUserCount` to accept `count int`:

```go
func (s *MongoStore) IncrementUserCount(ctx context.Context, roomID string, count int) error {
	_, err := s.rooms.UpdateByID(ctx, roomID, bson.M{"$inc": bson.M{"userCount": count}})
	return err
}
```

- [ ] **Step 3: Regenerate mocks and fix existing tests**

Run: `make generate SERVICE=room-worker`

Fix `room-worker/handler_test.go` — update `IncrementUserCount` mock expectation to include count parameter (add `1` as second arg).

- [ ] **Step 4: Run tests**

Run: `make test SERVICE=room-worker`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add room-worker/store.go room-worker/store_mongo.go room-worker/mock_store_test.go room-worker/handler.go room-worker/handler_test.go
git commit -m "feat(room-worker): extend SubscriptionStore with BulkCreateSubscriptions, CreateRoomMember, FindUsersByAccounts"
```

---

### Task 16: Implement processAddMembers + update HandleJetStreamMsg routing

**Files:**
- Modify: `room-worker/handler.go`
- Modify: `room-worker/handler_test.go`

- [ ] **Step 1: Write failing unit tests**

Add to `room-worker/handler_test.go`:

```go
func TestHandler_ProcessAddMembers(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	var published []publishedMsg
	publish := func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}
	h := NewHandler(store, "site-a", publish)

	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
	store.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob", "charlie"}).Return([]model.User{
		{ID: "u2", Account: "bob", SiteID: "site-a"},
		{ID: "u3", Account: "charlie", SiteID: "site-b"},
	}, nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, subs []*model.Subscription) error {
			assert.Len(t, subs, 2)
			for _, s := range subs {
				assert.Equal(t, "site-a", s.SiteID)
				assert.Equal(t, []model.Role{model.RoleMember}, s.Roles)
				require.NotNil(t, s.HistorySharedSince)
				assert.Equal(t, s.JoinedAt, *s.HistorySharedSince)
			}
			return nil
		})
	store.EXPECT().IncrementUserCount(gomock.Any(), "r1", 2).Return(nil)

	req := model.AddMembersRequest{
		RoomID: "r1", Users: []string{"bob", "charlie"},
		History: model.HistoryConfig{Mode: model.HistoryModeNone},
	}
	reqData, _ := json.Marshal(req)

	err := h.processAddMembers(context.Background(), reqData)
	require.NoError(t, err)

	// Should have: 2 SubscriptionUpdate + 1 MemberAddEvent + 1 system msg + 1 outbox (batched for site-b)
	assert.GreaterOrEqual(t, len(published), 4)

	// Verify exactly 1 outbox event for site-b (batched, not per-member)
	var outboxCount int
	for _, p := range published {
		if strings.Contains(p.subj, "outbox") {
			outboxCount++
			assert.Contains(t, p.subj, "site-b")
			// Verify the outbox payload contains charlie's account
			var outboxEvt model.OutboxEvent
			require.NoError(t, json.Unmarshal(p.data, &outboxEvt))
			var change model.MemberAddEvent
			require.NoError(t, json.Unmarshal(outboxEvt.Payload, &change))
			assert.Equal(t, []string{"charlie"}, change.Accounts)
		}
	}
	assert.Equal(t, 1, outboxCount, "should publish exactly 1 batched outbox event per destination site")
}

func TestHandler_ProcessAddMembers_HistoryAll(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	publish := func(_ context.Context, _ string, _ []byte) error { return nil }
	h := NewHandler(store, "site-a", publish)

	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-a"}, nil)
	store.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob"}).Return([]model.User{
		{ID: "u2", Account: "bob", SiteID: "site-a"},
	}, nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, subs []*model.Subscription) error {
			assert.Nil(t, subs[0].HistorySharedSince)
			return nil
		})
	store.EXPECT().IncrementUserCount(gomock.Any(), "r1", 1).Return(nil)

	req := model.AddMembersRequest{
		RoomID: "r1", Users: []string{"bob"},
		History: model.HistoryConfig{Mode: model.HistoryModeAll},
	}
	reqData, _ := json.Marshal(req)

	err := h.processAddMembers(context.Background(), reqData)
	require.NoError(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=room-worker`
Expected: Compilation error — `processAddMembers` undefined

- [ ] **Step 3: Update HandleJetStreamMsg to route by operation**

Replace `HandleJetStreamMsg` in `room-worker/handler.go`:

```go
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	var err error
	subj := msg.Subject()

	if strings.HasSuffix(subj, "member.add") {
		err = h.processAddMembers(ctx, msg.Data())
	} else {
		err = h.processInvite(ctx, msg.Data())
	}

	if err != nil {
		slog.Error("process message failed", "error", err, "subject", subj)
		if nakErr := msg.Nak(); nakErr != nil {
			slog.Error("failed to nak message", "err", nakErr)
		}
		return
	}
	if ackErr := msg.Ack(); ackErr != nil {
		slog.Error("failed to ack message", "err", ackErr)
	}
}
```

- [ ] **Step 4: Implement processAddMembers**

Add to `room-worker/handler.go` (full implementation):

```go
func (h *Handler) processAddMembers(ctx context.Context, data []byte) error {
	var req model.AddMembersRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal add members request: %w", err)
	}

	now := time.Now().UTC()

	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err != nil {
		return fmt.Errorf("get room %q: %w", req.RoomID, err)
	}

	users, err := h.store.FindUsersByAccounts(ctx, req.Users)
	if err != nil {
		return fmt.Errorf("find users: %w", err)
	}
	userMap := make(map[string]*model.User, len(users))
	for i := range users {
		userMap[users[i].Account] = &users[i]
	}

	var subs []*model.Subscription
	var accounts []string
	var userIDs []string
	userSiteIDs := make(map[string]string)

	for _, account := range req.Users {
		user, ok := userMap[account]
		if !ok {
			slog.Warn("user not found, skipping", "account", account)
			continue
		}
		sub := &model.Subscription{
			ID:       uuid.New().String(),
			User:     model.SubscriptionUser{ID: user.ID, Account: user.Account},
			RoomID:   req.RoomID,
			SiteID:   room.SiteID,
			Roles:    []model.Role{model.RoleMember},
			JoinedAt: now,
		}
		if req.History.Mode == model.HistoryModeNone {
			sub.HistorySharedSince = &now
		}
		subs = append(subs, sub)
		accounts = append(accounts, user.Account)
		userIDs = append(userIDs, user.ID)
		userSiteIDs[user.Account] = user.SiteID
	}

	if len(subs) == 0 {
		return nil
	}

	if err := h.store.BulkCreateSubscriptions(ctx, subs); err != nil {
		return fmt.Errorf("bulk create subscriptions: %w", err)
	}

	// Write individual room_members if request has orgs OR room already has org members
	writeIndividuals := len(req.Orgs) > 0
	if !writeIndividuals {
		hasOrgs, err := h.store.HasOrgRoomMembers(ctx, req.RoomID)
		if err != nil {
			slog.Warn("check existing org room members failed", "error", err, "roomID", req.RoomID)
		}
		writeIndividuals = hasOrgs
	}
	if writeIndividuals {
		for _, sub := range subs {
			m := &model.RoomMember{
				ID: uuid.New().String(), RoomID: req.RoomID, Ts: now,
				Member: model.RoomMemberEntry{ID: sub.User.ID, Type: model.RoomMemberIndividual, Account: sub.User.Account},
			}
			if err := h.store.CreateRoomMember(ctx, m); err != nil {
				slog.Error("create room member failed", "error", err, "account", sub.User.Account)
			}
		}
	}
	for _, orgID := range req.Orgs {
		m := &model.RoomMember{
			ID: uuid.New().String(), RoomID: req.RoomID, Ts: now,
			Member: model.RoomMemberEntry{ID: orgID, Type: model.RoomMemberOrg},
		}
		if err := h.store.CreateRoomMember(ctx, m); err != nil {
			slog.Error("create org room member failed", "error", err, "orgID", orgID)
		}
	}

	// Note: room_members stay on the room's origin site only — not replicated cross-site

	if err := h.store.IncrementUserCount(ctx, req.RoomID, len(accounts)); err != nil {
		return fmt.Errorf("increment user count: %w", err)
	}

	for _, sub := range subs {
		evt := model.SubscriptionUpdateEvent{
			UserID: sub.User.ID, Subscription: *sub, Action: "added", Timestamp: now.UnixMilli(),
		}
		evtData, _ := json.Marshal(evt)
		if err := h.publish(ctx, subject.SubscriptionUpdate(sub.User.Account), evtData); err != nil {
			slog.Error("subscription update publish failed", "error", err, "account", sub.User.Account)
		}
	}

	var historySharedSince int64
	if req.History.Mode == model.HistoryModeNone {
		historySharedSince = now.UnixMilli()
	}
	memberEvt := model.MemberAddEvent{
		Type: "member_added", RoomID: req.RoomID, Accounts: accounts, SiteID: room.SiteID,
		JoinedAt: now.UnixMilli(), HistorySharedSince: historySharedSince,
	}
	memberEvtData, _ := json.Marshal(memberEvt)
	if err := h.publish(ctx, subject.RoomMemberEvent(req.RoomID), memberEvtData); err != nil {
		slog.Error("room member event publish failed", "error", err, "roomID", req.RoomID)
	}

	sysMsgData, _ := json.Marshal(model.MembersAdded{
		Individuals: req.Users, Orgs: req.Orgs, Channels: req.Channels,
		AddedUsersCount: len(accounts),
	})
	sysMsg := model.Message{
		ID: uuid.New().String(), RoomID: req.RoomID,
		Content: "added members to the channel", CreatedAt: now,
		Type: "members_added", SysMsgData: sysMsgData,
	}
	sysMsgEvt := model.MessageEvent{Message: sysMsg, SiteID: room.SiteID, Timestamp: now.UnixMilli()}
	sysMsgEvtData, _ := json.Marshal(sysMsgEvt)
	if err := h.publish(ctx, subject.MsgCanonicalCreated(room.SiteID), sysMsgEvtData); err != nil {
		slog.Error("system message publish failed", "error", err, "roomID", req.RoomID)
	}

	// 10. Outbox for cross-site members — batched by destination site.
	// Room member data (orgs/individuals) stays on the room's site — only accounts
	// are replicated so remote sites can create subscriptions.
	remoteSiteMembers := make(map[string][]string)
	for _, sub := range subs {
		user, ok := userMap[sub.User.Account]
		if !ok || user.SiteID == room.SiteID {
			continue
		}
		remoteSiteMembers[user.SiteID] = append(remoteSiteMembers[user.SiteID], sub.User.Account)
	}
	for destSiteID, accounts := range remoteSiteMembers {
		siteEvt := model.MemberAddEvent{
			Type: "member_added", RoomID: req.RoomID, Accounts: accounts,
			SiteID: room.SiteID,
			JoinedAt: now.UnixMilli(), HistorySharedSince: historySharedSince,
		}
		siteEvtData, _ := json.Marshal(siteEvt)
		outbox := model.OutboxEvent{
			Type: "member_added", SiteID: room.SiteID, DestSiteID: destSiteID,
			Payload: siteEvtData, Timestamp: now.UnixMilli(),
		}
		outboxData, _ := json.Marshal(outbox)
		if err := h.publish(ctx, subject.Outbox(room.SiteID, destSiteID, "member_added"), outboxData); err != nil {
			slog.Error("outbox publish failed", "error", err, "destSiteID", destSiteID)
		}
	}

	return nil
}
```

- [ ] **Step 5: Run tests**

Run: `make test SERVICE=room-worker`
Expected: PASS

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: No errors

- [ ] **Step 7: Commit**

```bash
git add room-worker/handler.go room-worker/handler_test.go
git commit -m "feat(room-worker): implement processAddMembers with event publishing and cross-site outbox"
```

---

## Section 5: inbox-worker + message-worker

### Task 17: Update inbox-worker handleMemberAdded to use MemberAddEvent

**Files:**
- Modify: `inbox-worker/handler.go:57-96`
- Modify: `inbox-worker/handler_test.go`

- [ ] **Step 1: Write failing tests**

Add to `inbox-worker/handler_test.go`:

```go
func TestHandleEvent_MemberAdded_EventSourcedFields(t *testing.T) {
	store := &stubInboxStore{
		users: []model.User{
			{ID: "u1", Account: "alice", SiteID: "site-a"},
			{ID: "u2", Account: "bob", SiteID: "site-a"},
		},
	}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	joinedAt := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)

	change := model.MemberAddEvent{
		Type: "member_added", RoomID: "r1",
		Accounts: []string{"alice", "bob"}, SiteID: "site-a",
		JoinedAt: joinedAt.UnixMilli(), HistorySharedSince: joinedAt.UnixMilli(),
	}
	changeData, _ := json.Marshal(change)
	evt := model.OutboxEvent{Type: "member_added", Payload: changeData}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	require.NoError(t, err)

	subs := store.getSubscriptions()
	require.Len(t, subs, 2)

	assert.Equal(t, "u1", subs[0].User.ID)
	assert.Equal(t, "site-a", subs[0].SiteID)
	assert.Equal(t, joinedAt, subs[0].JoinedAt)
	require.NotNil(t, subs[0].HistorySharedSince)
	assert.Equal(t, joinedAt, *subs[0].HistorySharedSince)
	assert.Equal(t, []model.Role{model.RoleMember}, subs[0].Roles)

	assert.Equal(t, "u2", subs[1].User.ID)
}

func TestHandleEvent_MemberAdded_HistoryAll(t *testing.T) {
	store := &stubInboxStore{
		users: []model.User{{ID: "u1", Account: "alice", SiteID: "site-a"}},
	}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	change := model.MemberAddEvent{
		Type: "member_added", RoomID: "r1",
		Accounts: []string{"alice"}, SiteID: "site-a",
		JoinedAt: time.Now().UnixMilli(),
		HistorySharedSince: 0,
	}
	changeData, _ := json.Marshal(change)
	evt := model.OutboxEvent{Type: "member_added", Payload: changeData}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	require.NoError(t, err)

	subs := store.getSubscriptions()
	require.Len(t, subs, 1)
	assert.Nil(t, subs[0].HistorySharedSince)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=inbox-worker`
Expected: FAIL — existing handler unmarshals `InviteMemberRequest`

- [ ] **Step 3: Rewrite handleMemberAdded**

Replace `handleMemberAdded` in `inbox-worker/handler.go`. The handler should only:
1. Unmarshal `MemberAddEvent`
2. Look up users locally via `FindUsersByAccounts`
3. Bulk-create subscriptions
4. Publish `SubscriptionUpdateEvent` per new member

Room member data (orgs/individuals) stays on the room's origin site — no room_members writes, no backfill.

```go
func (h *Handler) handleMemberAdded(ctx context.Context, evt *model.OutboxEvent) error {
	var event model.MemberAddEvent
	if err := json.Unmarshal(evt.Payload, &event); err != nil {
		return fmt.Errorf("unmarshal member_added payload: %w", err)
	}

	// 1. Look up users locally
	users, err := h.store.FindUsersByAccounts(ctx, event.Accounts)
	if err != nil {
		return fmt.Errorf("find users by accounts: %w", err)
	}
	userMap := make(map[string]model.User, len(users))
	for _, u := range users {
		userMap[u.Account] = u
	}

	joinedAt := time.UnixMilli(event.JoinedAt).UTC()
	var historySharedSince *time.Time
	if event.HistorySharedSince > 0 {
		t := time.UnixMilli(event.HistorySharedSince).UTC()
		historySharedSince = &t
	}

	// 2. Build subscriptions
	subs := make([]*model.Subscription, 0, len(event.Accounts))
	for _, account := range event.Accounts {
		user, ok := userMap[account]
		if !ok {
			slog.Warn("user not found for account", "account", account)
			continue
		}
		sub := &model.Subscription{
			ID:                 uuid.New().String(),
			User:               model.SubscriptionUser{ID: user.ID, Account: user.Account},
			RoomID:             event.RoomID,
			SiteID:             event.SiteID,
			Roles:              []model.Role{model.RoleMember},
			HistorySharedSince: historySharedSince,
			JoinedAt:           joinedAt,
		}
		subs = append(subs, sub)
	}

	// 3. Bulk create subscriptions
	if err := h.store.BulkCreateSubscriptions(ctx, subs); err != nil {
		return fmt.Errorf("bulk create subscriptions: %w", err)
	}

	// 4. Publish SubscriptionUpdateEvent per new member
	publishNow := time.Now().UTC().UnixMilli()
	for _, sub := range subs {
		updateEvt := model.SubscriptionUpdateEvent{
			UserID: sub.User.ID, Subscription: *sub, Action: "added",
			Timestamp: publishNow,
		}
		updateData, err := natsutil.MarshalResponse(updateEvt)
		if err != nil {
			return fmt.Errorf("marshal subscription update event: %w", err)
		}
		if err := h.pub.Publish(ctx, subject.SubscriptionUpdate(sub.User.Account), updateData); err != nil {
			slog.Error("publish subscription update failed", "error", err, "account", sub.User.Account)
		}
	}

	return nil
}
```

The `InboxStore` interface for this handler is:

```go
type InboxStore interface {
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	BulkCreateSubscriptions(ctx context.Context, subs []*model.Subscription) error
	UpsertRoom(ctx context.Context, room *model.Room) error
	UpdateSubscriptionRoles(ctx context.Context, account, roomID string, roles []model.Role) error
	FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error)
}
```

No `CreateRoomMember`, `HasOrgRoomMembers`, or `GetSubscriptionAccounts` — room_members data is not synced cross-site.

- [ ] **Step 4: Update existing tests that used InviteMemberRequest payload**

Change existing test data setup from `model.InviteMemberRequest{...}` to `model.MemberAddEvent{...}` and update assertions for `Roles` instead of `Role`.

- [ ] **Step 5: Run tests**

Run: `make test SERVICE=inbox-worker`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add inbox-worker/handler.go inbox-worker/handler_test.go
git commit -m "feat(inbox-worker): update handleMemberAdded to use MemberAddEvent with event-sourced fields"
```

---

### Task 18: Update message-worker Cassandra insert for type and sys_msg_data

**Files:**
- Modify: `message-worker/store_cassandra.go:57-75`
- Modify: `message-worker/handler.go` (handle nil user for system messages)

- [ ] **Step 1: Update SaveMessage queries**

In `message-worker/store_cassandra.go`, update both insert queries to include `type` and `sys_msg_data`:

```go
func (s *CassandraStore) SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error {
	if err := s.cassSession.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, site_id, updated_at, type, sys_msg_data)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, sender, msg.Content, siteID, msg.CreatedAt, msg.Type, msg.SysMsgData,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_room %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO messages_by_id (message_id, created_at, sender, msg, site_id, updated_at, type, sys_msg_data)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, sender, msg.Content, siteID, msg.CreatedAt, msg.Type, msg.SysMsgData,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_id %s: %w", msg.ID, err)
	}

	return nil
}
```

- [ ] **Step 2: Handle nil user for system messages in handler.go**

In `message-worker/handler.go:processMessage`, the user lookup may fail for system messages (no `UserID`). Add a nil check — if user is not found and `msg.Type != ""`, use a nil sender instead of failing:

```go
	user, err := h.userStore.FindUserByID(ctx, evt.Message.UserID)
	var sender *cassParticipant
	if err != nil {
		if evt.Message.Type != "" {
			// System message — no sender
			slog.Info("system message, no sender", "type", evt.Message.Type)
		} else {
			return fmt.Errorf("find user %q: %w", evt.Message.UserID, err)
		}
	} else {
		sender = &cassParticipant{
			ID: user.ID, EngName: user.EngName, CompanyName: user.ChineseName, Account: user.Account,
		}
	}
```

- [ ] **Step 3: Run tests**

Run: `make test SERVICE=message-worker`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add message-worker/store_cassandra.go message-worker/handler.go
git commit -m "feat(message-worker): add type and sys_msg_data columns to Cassandra insert"
```

---

### Task 19: Final verification and push

- [ ] **Step 1: Run full test suite with race detector**

Run: `make test`
Expected: PASS

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: No errors

- [ ] **Step 3: Push**

```bash
git push -u origin claude/extract-add-member-spec-BxwAt
```
