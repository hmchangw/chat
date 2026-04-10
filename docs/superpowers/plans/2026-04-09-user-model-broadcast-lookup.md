# User Model Update & Broadcast Worker User Lookup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Update the User model with `EngName`, `ChineseName`, `EmployeeID` fields (removing `Name`), create a shared `pkg/userstore` package, and migrate `broadcast-worker` from the `employee` collection to the `users` collection.

**Architecture:** The shared `pkg/userstore` package follows the `pkg/roomkeystore` precedent — interface + MongoDB implementation in a reusable package. The `broadcast-worker` handler receives a `userstore.UserStore` as a separate dependency alongside its existing `Store`, using it for user display-name enrichment instead of the employee collection.

**Tech Stack:** Go 1.25, MongoDB (`go.mongodb.org/mongo-driver/v2`), `go.uber.org/mock` (mockgen), `testcontainers-go` (integration tests)

**Spec:** `docs/superpowers/specs/2026-04-09-user-model-broadcast-lookup-design.md`

---

### Task 1: Update User Model

**Files:**
- Modify: `pkg/model/user.go:3-8`
- Modify: `pkg/model/model_test.go:15-17`

- [ ] **Step 1: Update the model test to use new fields (Red)**

In `pkg/model/model_test.go`, replace the `TestUserJSON` function:

```go
func TestUserJSON(t *testing.T) {
	u := model.User{ID: "u1", Account: "alice", SiteID: "site-a", EngName: "Alice Wang", ChineseName: "愛麗絲", EmployeeID: "E001"}
	roundTrip(t, &u, &model.User{})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: Compilation error — `model.User` has no field `EngName`, `ChineseName`, `EmployeeID`, and `Name` is still present.

- [ ] **Step 3: Update the User struct (Green)**

Replace the entire `pkg/model/user.go` contents:

```go
package model

type User struct {
	ID          string `json:"id" bson:"_id"`
	Account     string `json:"account" bson:"account"`
	SiteID      string `json:"siteId" bson:"siteId"`
	EngName     string `json:"engName" bson:"engName"`
	ChineseName string `json:"chineseName" bson:"chineseName"`
	EmployeeID  string `json:"employeeId" bson:"employeeId"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=pkg/model`
Expected: All tests PASS, including `TestUserJSON`.

- [ ] **Step 5: Commit**

```bash
git add pkg/model/user.go pkg/model/model_test.go
git commit -m "feat: update User model with EngName, ChineseName, EmployeeID; remove Name"
```

---

### Task 2: Create `pkg/userstore` Package

**Files:**
- Create: `pkg/userstore/userstore.go`
- Create: `pkg/userstore/integration_test.go`

- [ ] **Step 1: Write the integration test (Red)**

Create `pkg/userstore/integration_test.go`:

```go
//go:build integration

package userstore_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/userstore"
)

func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:8")
	if err != nil {
		t.Fatalf("start mongo: %v", err)
	}
	t.Cleanup(func() { container.Terminate(ctx) })

	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("get mongo uri: %v", err)
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	t.Cleanup(func() { client.Disconnect(ctx) })
	return client.Database("userstore_test")
}

func TestFindUsersByAccounts_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()
	col := db.Collection("users")

	_, err := col.InsertMany(ctx, []interface{}{
		model.User{ID: "u1", Account: "alice", SiteID: "site-a", EngName: "Alice Wang", ChineseName: "愛麗絲", EmployeeID: "E001"},
		model.User{ID: "u2", Account: "bob", SiteID: "site-a", EngName: "Bob Chen", ChineseName: "鮑勃", EmployeeID: "E002"},
		model.User{ID: "u3", Account: "charlie", SiteID: "site-a", EngName: "Charlie Li", ChineseName: "查理", EmployeeID: "E003"},
	})
	require.NoError(t, err)

	store := userstore.NewMongoStore(col)

	t.Run("find multiple users", func(t *testing.T) {
		users, err := store.FindUsersByAccounts(ctx, []string{"alice", "bob"})
		require.NoError(t, err)
		require.Len(t, users, 2)

		byAccount := map[string]model.User{}
		for _, u := range users {
			byAccount[u.Account] = u
		}
		assert.Equal(t, "Alice Wang", byAccount["alice"].EngName)
		assert.Equal(t, "愛麗絲", byAccount["alice"].ChineseName)
		assert.Equal(t, "E001", byAccount["alice"].EmployeeID)
		assert.Equal(t, "Bob Chen", byAccount["bob"].EngName)
		assert.Equal(t, "鮑勃", byAccount["bob"].ChineseName)
		assert.Equal(t, "E002", byAccount["bob"].EmployeeID)
	})

	t.Run("no matching accounts", func(t *testing.T) {
		users, err := store.FindUsersByAccounts(ctx, []string{"nonexistent"})
		require.NoError(t, err)
		assert.Empty(t, users)
	})

	t.Run("empty accounts list", func(t *testing.T) {
		users, err := store.FindUsersByAccounts(ctx, []string{})
		require.NoError(t, err)
		assert.Empty(t, users)
	})

	t.Run("partial match returns only found users", func(t *testing.T) {
		users, err := store.FindUsersByAccounts(ctx, []string{"alice", "nonexistent"})
		require.NoError(t, err)
		require.Len(t, users, 1)
		assert.Equal(t, "alice", users[0].Account)
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test-integration SERVICE=pkg/userstore`
Expected: Compilation error — package `github.com/hmchangw/chat/pkg/userstore` does not exist.

- [ ] **Step 3: Write the interface and implementation (Green)**

Create `pkg/userstore/userstore.go`:

```go
package userstore

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

// UserStore defines read operations for user records.
type UserStore interface {
	FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error)
}

type mongoStore struct {
	col *mongo.Collection
}

// NewMongoStore returns a UserStore backed by the given MongoDB collection.
func NewMongoStore(col *mongo.Collection) UserStore {
	return &mongoStore{col: col}
}

func (m *mongoStore) FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	filter := bson.M{"account": bson.M{"$in": accounts}}
	projection := bson.M{"_id": 1, "account": 1, "engName": 1, "chineseName": 1, "employeeId": 1}
	opts := options.Find().SetProjection(projection)
	cursor, err := m.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("query users by accounts: %w", err)
	}
	defer cursor.Close(ctx)
	var users []model.User
	if err := cursor.All(ctx, &users); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}
	return users, nil
}
```

- [ ] **Step 4: Run integration test to verify it passes**

Run: `make test-integration SERVICE=pkg/userstore`
Expected: All tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/userstore/userstore.go pkg/userstore/integration_test.go
git commit -m "feat: add shared pkg/userstore package with UserStore interface and MongoDB implementation"
```

---

### Task 3: Update broadcast-worker Store Interface and Regenerate Mocks

**Files:**
- Modify: `broadcast-worker/store.go:10-19`
- Modify: `broadcast-worker/store_mongo.go:15-23, 79-93`
- Regenerate: `broadcast-worker/mock_store_test.go`
- Create (generated): `broadcast-worker/mock_userstore_test.go`

- [ ] **Step 1: Update the store interface — remove `FindEmployeesByAccountNames`**

Replace the entire `broadcast-worker/store.go`:

```go
package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store
//go:generate mockgen -destination=mock_userstore_test.go -package=main github.com/hmchangw/chat/pkg/userstore UserStore

// Store defines data access operations for the broadcast worker.
type Store interface {
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
	UpdateRoomOnNewMessage(ctx context.Context, roomID string, msgID string, msgAt time.Time, mentionAll bool) error
	SetSubscriptionMentions(ctx context.Context, roomID string, accounts []string) error
}
```

- [ ] **Step 2: Update store_mongo.go — remove empCol and FindEmployeesByAccountNames**

Remove the `empCol` field from the struct and the `FindEmployeesByAccountNames` method. Update the constructor. Replace the entire `broadcast-worker/store_mongo.go`:

```go
package main

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

type mongoStore struct {
	roomCol *mongo.Collection
	subCol  *mongo.Collection
}

func NewMongoStore(roomCol, subCol *mongo.Collection) *mongoStore {
	return &mongoStore{roomCol: roomCol, subCol: subCol}
}

func (m *mongoStore) GetRoom(ctx context.Context, roomID string) (*model.Room, error) {
	filter := bson.M{"_id": roomID}
	var room model.Room
	if err := m.roomCol.FindOne(ctx, filter).Decode(&room); err != nil {
		return nil, fmt.Errorf("find room %s: %w", roomID, err)
	}
	return &room, nil
}

func (m *mongoStore) ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error) {
	filter := bson.M{"roomId": roomID}
	cursor, err := m.subCol.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("query subscriptions for room %s: %w", roomID, err)
	}
	defer cursor.Close(ctx)
	var subs []model.Subscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, fmt.Errorf("decode subscriptions: %w", err)
	}
	return subs, nil
}

func (m *mongoStore) UpdateRoomOnNewMessage(ctx context.Context, roomID string, msgID string, msgAt time.Time, mentionAll bool) error {
	fields := bson.M{
		"lastMsgAt": msgAt,
		"lastMsgId": msgID,
		"updatedAt": msgAt,
	}
	if mentionAll {
		fields["lastMentionAllAt"] = msgAt
	}
	filter := bson.M{"_id": roomID}
	update := bson.M{"$set": fields}
	_, err := m.roomCol.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update room %s on new message: %w", roomID, err)
	}
	return nil
}

func (m *mongoStore) SetSubscriptionMentions(ctx context.Context, roomID string, accounts []string) error {
	filter := bson.M{
		"roomId":    roomID,
		"u.account": bson.M{"$in": accounts},
	}
	update := bson.M{"$set": bson.M{"hasMention": true}}
	_, err := m.subCol.UpdateMany(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("set subscription mentions for room %s: %w", roomID, err)
	}
	return nil
}
```

- [ ] **Step 3: Regenerate mocks**

Run: `make generate SERVICE=broadcast-worker`
Expected: Generates `mock_store_test.go` (without `FindEmployeesByAccountNames`) and new `mock_userstore_test.go` (with `MockUserStore`).

- [ ] **Step 4: Verify mocks generated correctly**

Run: `ls broadcast-worker/mock_*_test.go`
Expected: Both `mock_store_test.go` and `mock_userstore_test.go` exist.

- [ ] **Step 5: Commit**

```bash
git add broadcast-worker/store.go broadcast-worker/store_mongo.go broadcast-worker/mock_store_test.go broadcast-worker/mock_userstore_test.go
git commit -m "refactor: remove employee lookup from broadcast-worker Store, add UserStore mock generation"
```

---

### Task 4: Update broadcast-worker Unit Tests (Red)

**Files:**
- Modify: `broadcast-worker/handler_test.go`

- [ ] **Step 1: Update test fixtures and helpers**

In `broadcast-worker/handler_test.go`, replace the `testEmployees` variable and `expectEmployeeLookup` helper. Find this block (lines 52-72):

```go
	testEmployees = []model.Employee{
		{AccountName: "alice", Name: "愛麗絲", EngName: "Alice Wang"},
		{AccountName: "bob", Name: "鮑勃", EngName: "Bob Chen"},
	}
```

Replace with:

```go
	testUsers = []model.User{
		{ID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲", EmployeeID: "E001", SiteID: "site-a"},
		{ID: "u-bob", Account: "bob", EngName: "Bob Chen", ChineseName: "鮑勃", EmployeeID: "E002", SiteID: "site-a"},
	}
```

Replace the `expectEmployeeLookup` helper (line 70-72):

```go
func expectEmployeeLookup(store *MockStore, accountNames []string, employees []model.Employee) {
	store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.InAnyOrder(accountNames)).Return(employees, nil)
}
```

With:

```go
func expectUserLookup(us *MockUserStore, accounts []string, users []model.User) {
	us.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.InAnyOrder(accounts)).Return(users, nil)
}
```

- [ ] **Step 2: Update all test functions to use `MockUserStore`**

In every test that creates a handler, add a `MockUserStore` and pass it to `NewHandler`. The pattern changes from:

```go
ctrl := gomock.NewController(t)
store := NewMockStore(ctrl)
pub := &mockPublisher{}
// ...
h := NewHandler(store, pub)
```

To:

```go
ctrl := gomock.NewController(t)
store := NewMockStore(ctrl)
us := NewMockUserStore(ctrl)
pub := &mockPublisher{}
// ...
h := NewHandler(store, us, pub)
```

Apply this change to ALL test functions:

**`TestHandler_HandleMessage_GroupRoom`** (line 114-180): Add `us := NewMockUserStore(ctrl)` after `store` creation. Change `NewHandler(store, pub)` to `NewHandler(store, us, pub)`. Replace all `expectEmployeeLookup(store, ...)` calls with `expectUserLookup(us, ...)` using `model.User` data:

```go
// Replace the switch block (lines 128-137) with:
switch tc.name {
case "no mentions":
	expectUserLookup(us, []string{"sender"}, []model.User{{ID: "u-sender", Account: "sender", EngName: "Sender Lin", ChineseName: "寄件者", SiteID: "site-a"}})
case "individual mentions":
	expectUserLookup(us, []string{"sender", "alice", "bob"}, append([]model.User{{ID: "u-sender", Account: "sender", EngName: "Sender Lin", ChineseName: "寄件者", SiteID: "site-a"}}, testUsers...))
case "mention all case insensitive":
	expectUserLookup(us, []string{"sender"}, []model.User{{ID: "u-sender", Account: "sender", EngName: "Sender Lin", ChineseName: "寄件者", SiteID: "site-a"}})
case "mention all and individual":
	expectUserLookup(us, []string{"sender", "alice"}, []model.User{{ID: "u-sender", Account: "sender", EngName: "Sender Lin", ChineseName: "寄件者", SiteID: "site-a"}, testUsers[0]})
}
```

**`TestHandler_HandleMessage_DMRoom`** (line 211-270): Add `us := NewMockUserStore(ctrl)`. Change `NewHandler(store, pub)` to `NewHandler(store, us, pub)`. Replace employee lookup expectations:

```go
// Replace the switch block (lines 235-240) with:
switch tc.name {
case "no mentions":
	expectUserLookup(us, []string{"alice"}, testUsers[:1])
case "with mention":
	expectUserLookup(us, []string{"alice", "bob"}, testUsers)
}
```

**`TestHandler_HandleMessage_Errors`** — update each subtest:

- `"invalid json"` (line 276-285): Add `us := NewMockUserStore(ctrl)`, change to `NewHandler(store, us, pub)`.
- `"room not found"` (line 287-298): Add `us := NewMockUserStore(ctrl)`, change to `NewHandler(store, us, pub)`.
- `"update room fails"` (line 300-312): Add `us := NewMockUserStore(ctrl)`, change to `NewHandler(store, us, pub)`.
- `"set subscription mentions fails"` (line 314-328): Add `us := NewMockUserStore(ctrl)`, change to `NewHandler(store, us, pub)`.
- `"unknown room type"` (line 330-347): Add `us := NewMockUserStore(ctrl)`, change to `NewHandler(store, us, pub)`. Replace `store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.Any()).Return(nil, nil)` with `us.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).Return(nil, nil)`.
- `"list subscriptions fails for DM"` (line 349-372): Add `us := NewMockUserStore(ctrl)`, change to `NewHandler(store, us, pub)`. Replace `store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.Any()).Return(nil, nil)` with `us.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).Return(nil, nil)`.
- `"sender mentioned deduplicates lookup"` (line 374-393): Add `us := NewMockUserStore(ctrl)`, change to `NewHandler(store, us, pub)`. Replace `expectEmployeeLookup(store, ...)` with `expectUserLookup(us, []string{"sender"}, []model.User{{ID: "u-sender", Account: "sender", EngName: "Sender Lin", ChineseName: "寄件者", SiteID: "site-a"}})`.
- `"employee lookup fails fallback to account"` (line 395-415): Rename to `"user lookup fails fallback to account"`. Add `us := NewMockUserStore(ctrl)`, change to `NewHandler(store, us, pub)`. Replace `store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.Any()).Return(nil, errors.New("db error"))` with `us.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).Return(nil, errors.New("db error"))`.

**`TestHandler_HandleMessage_DMRoom_PublishError`** (line 433-458): Add `us := NewMockUserStore(ctrl)`, change to `NewHandler(store, us, pub)`. Replace `store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.Any()).Return(testEmployees, nil)` with `us.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).Return(testUsers, nil)`.

**`TestBuildMentionParticipants`** (line 460-493): Change the `employees` map from `map[string]model.Employee` to `map[string]model.User`:

```go
func TestBuildMentionParticipants(t *testing.T) {
	users := map[string]model.User{
		"alice": {ID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲"},
	}

	t.Run("empty accounts returns nil", func(t *testing.T) {
		result := buildMentionParticipants(nil, users)
		assert.Nil(t, result)
	})

	t.Run("user found uses user data", func(t *testing.T) {
		result := buildMentionParticipants([]string{"alice"}, users)
		require.Len(t, result, 1)
		assert.Equal(t, "alice", result[0].Account)
		assert.Equal(t, "愛麗絲", result[0].ChineseName)
		assert.Equal(t, "Alice Wang", result[0].EngName)
		assert.Empty(t, result[0].UserID)
	})

	t.Run("user not found falls back to account", func(t *testing.T) {
		result := buildMentionParticipants([]string{"unknown"}, users)
		require.Len(t, result, 1)
		assert.Equal(t, "unknown", result[0].Account)
		assert.Equal(t, "unknown", result[0].ChineseName)
		assert.Equal(t, "unknown", result[0].EngName)
	})

	t.Run("mixed found and not found", func(t *testing.T) {
		result := buildMentionParticipants([]string{"alice", "unknown"}, users)
		require.Len(t, result, 2)
		assert.Equal(t, "愛麗絲", result[0].ChineseName)
		assert.Equal(t, "unknown", result[1].ChineseName)
	})
}
```

**`TestBuildClientMessage`** (line 495-520): Change from `map[string]model.Employee` to `map[string]model.User`:

```go
func TestBuildClientMessage(t *testing.T) {
	msg := &model.Message{
		ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
		Content: "hello", CreatedAt: time.Now(),
	}

	t.Run("user found", func(t *testing.T) {
		users := map[string]model.User{
			"alice": {ID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲"},
		}
		cm := buildClientMessage(msg, users)
		assert.Equal(t, "m1", cm.ID)
		require.NotNil(t, cm.Sender)
		assert.Equal(t, "u1", cm.Sender.UserID)
		assert.Equal(t, "alice", cm.Sender.Account)
		assert.Equal(t, "愛麗絲", cm.Sender.ChineseName)
		assert.Equal(t, "Alice Wang", cm.Sender.EngName)
	})

	t.Run("user not found", func(t *testing.T) {
		cm := buildClientMessage(msg, map[string]model.User{})
		require.NotNil(t, cm.Sender)
		assert.Equal(t, "alice", cm.Sender.ChineseName)
		assert.Equal(t, "alice", cm.Sender.EngName)
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=broadcast-worker`
Expected: Compilation errors — `NewHandler` has wrong signature, `buildClientMessage` and `buildMentionParticipants` expect different parameter types.

- [ ] **Step 3: Commit test changes**

Do NOT commit yet — proceed to Task 5 to make the tests pass.

---

### Task 5: Update broadcast-worker Handler (Green)

**Files:**
- Modify: `broadcast-worker/handler.go:11-12, 20-28, 57-77, 147-182`

- [ ] **Step 1: Update imports and handler struct**

In `broadcast-worker/handler.go`, add the `userstore` import:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/userstore"
)
```

Update the `Handler` struct and constructor:

```go
// Handler processes MESSAGES_CANONICAL messages and broadcasts room events.
type Handler struct {
	store     Store
	userStore userstore.UserStore
	pub       Publisher
}

func NewHandler(store Store, userStore userstore.UserStore, pub Publisher) *Handler {
	return &Handler{store: store, userStore: userStore, pub: pub}
}
```

- [ ] **Step 2: Update HandleMessage — user lookup section**

Replace lines 57-77 (the employee lookup section in `HandleMessage`) with:

```go
	// Collect all user accounts for lookup (sender + mentioned)
	lookupAccounts := make([]string, 0, 1+len(mentionedAccounts))
	lookupAccounts = append(lookupAccounts, msg.UserAccount)
	for _, u := range mentionedAccounts {
		if u != msg.UserAccount {
			lookupAccounts = append(lookupAccounts, u)
		}
	}

	userMap := make(map[string]model.User)
	users, err := h.userStore.FindUsersByAccounts(ctx, lookupAccounts)
	if err != nil {
		slog.Warn("user lookup failed, falling back to accounts", "error", err)
	} else {
		for _, u := range users {
			userMap[u.Account] = u
		}
	}

	clientMsg := buildClientMessage(&msg, userMap)
	mentionParticipants := buildMentionParticipants(mentionedAccounts, userMap)
```

- [ ] **Step 3: Update buildClientMessage**

Replace the `buildClientMessage` function (lines 147-163):

```go
func buildClientMessage(msg *model.Message, userMap map[string]model.User) *model.ClientMessage {
	sender := model.Participant{
		UserID:  msg.UserID,
		Account: msg.UserAccount,
	}
	if u, ok := userMap[msg.UserAccount]; ok {
		sender.ChineseName = u.ChineseName
		sender.EngName = u.EngName
	} else {
		sender.ChineseName = msg.UserAccount
		sender.EngName = msg.UserAccount
	}
	return &model.ClientMessage{
		Message: *msg,
		Sender:  &sender,
	}
}
```

- [ ] **Step 4: Update buildMentionParticipants**

Replace the `buildMentionParticipants` function (lines 165-182):

```go
func buildMentionParticipants(mentionedAccounts []string, userMap map[string]model.User) []model.Participant {
	if len(mentionedAccounts) == 0 {
		return nil
	}
	participants := make([]model.Participant, len(mentionedAccounts))
	for i, account := range mentionedAccounts {
		p := model.Participant{Account: account}
		if u, ok := userMap[account]; ok {
			p.ChineseName = u.ChineseName
			p.EngName = u.EngName
		} else {
			p.ChineseName = account
			p.EngName = account
		}
		participants[i] = p
	}
	return participants
}
```

- [ ] **Step 5: Run unit tests to verify they pass**

Run: `make test SERVICE=broadcast-worker`
Expected: All tests PASS.

- [ ] **Step 6: Commit**

```bash
git add broadcast-worker/store.go broadcast-worker/store_mongo.go broadcast-worker/mock_store_test.go broadcast-worker/mock_userstore_test.go broadcast-worker/handler.go broadcast-worker/handler_test.go
git commit -m "feat: migrate broadcast-worker from employee lookup to shared userstore.UserStore"
```

---

### Task 6: Update broadcast-worker main.go Wiring

**Files:**
- Modify: `broadcast-worker/main.go:17, 53-54, 87`

- [ ] **Step 1: Add userstore import**

In `broadcast-worker/main.go`, add the import:

```go
"github.com/hmchangw/chat/pkg/userstore"
```

- [ ] **Step 2: Update store and handler wiring**

Replace lines 53-54:

```go
	db := mongoClient.Database(cfg.MongoDB)
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
```

With:

```go
	db := mongoClient.Database(cfg.MongoDB)
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
```

Replace line 87:

```go
	handler := NewHandler(store, publisher)
```

With:

```go
	handler := NewHandler(store, us, publisher)
```

- [ ] **Step 3: Verify compilation**

Run: `make build SERVICE=broadcast-worker`
Expected: Build succeeds.

- [ ] **Step 4: Commit**

```bash
git add broadcast-worker/main.go
git commit -m "feat: wire userstore.UserStore in broadcast-worker main.go"
```

---

### Task 7: Update broadcast-worker Integration Tests

**Files:**
- Modify: `broadcast-worker/integration_test.go`

- [ ] **Step 1: Add userstore import**

In `broadcast-worker/integration_test.go`, add the import:

```go
"github.com/hmchangw/chat/pkg/userstore"
```

- [ ] **Step 2: Fix recordingPublisher to match Publisher interface**

The current `recordingPublisher.Publish` is missing the `ctx context.Context` parameter. Replace lines 49-53:

```go
func (p *recordingPublisher) Publish(subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, publishRecord{subject: subj, data: data})
	return nil
}
```

With:

```go
func (p *recordingPublisher) Publish(_ context.Context, subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, publishRecord{subject: subj, data: data})
	return nil
}
```

- [ ] **Step 3: Update TestBroadcastWorker_GroupRoom_Integration**

Replace employee collection inserts and store wiring (lines 77-85):

```go
	_, err = db.Collection("employee").InsertMany(ctx, []interface{}{
		bson.M{"accountName": "alice", "name": "愛麗絲", "engName": "Alice Wang"},
		bson.M{"accountName": "bob", "name": "鮑勃", "engName": "Bob Chen"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)
```

With:

```go
	_, err = db.Collection("users").InsertMany(ctx, []interface{}{
		model.User{ID: "u-alice", Account: "alice", SiteID: "site-a", EngName: "Alice Wang", ChineseName: "愛麗絲", EmployeeID: "E001"},
		model.User{ID: "u-bob", Account: "bob", SiteID: "site-a", EngName: "Bob Chen", ChineseName: "鮑勃", EmployeeID: "E002"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, us, pub)
```

- [ ] **Step 4: Update TestBroadcastWorker_GroupRoom_MentionAll_Integration**

Replace employee collection inserts and store wiring (lines 123-130):

```go
	_, err = db.Collection("employee").InsertMany(ctx, []interface{}{
		bson.M{"accountName": "alice", "name": "愛麗絲", "engName": "Alice Wang"},
		bson.M{"accountName": "bob", "name": "鮑勃", "engName": "Bob Chen"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)
```

With:

```go
	_, err = db.Collection("users").InsertMany(ctx, []interface{}{
		model.User{ID: "u-alice", Account: "alice", SiteID: "site-a", EngName: "Alice Wang", ChineseName: "愛麗絲", EmployeeID: "E001"},
		model.User{ID: "u-bob", Account: "bob", SiteID: "site-a", EngName: "Bob Chen", ChineseName: "鮑勃", EmployeeID: "E002"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, us, pub)
```

- [ ] **Step 5: Update TestBroadcastWorker_GroupRoom_IndividualMention_Integration**

Replace employee collection inserts and store wiring (lines 162-169):

```go
	_, err = db.Collection("employee").InsertMany(ctx, []interface{}{
		bson.M{"accountName": "alice", "name": "愛麗絲", "engName": "Alice Wang"},
		bson.M{"accountName": "bob", "name": "鮑勃", "engName": "Bob Chen"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)
```

With:

```go
	_, err = db.Collection("users").InsertMany(ctx, []interface{}{
		model.User{ID: "u-alice", Account: "alice", SiteID: "site-a", EngName: "Alice Wang", ChineseName: "愛麗絲", EmployeeID: "E001"},
		model.User{ID: "u-bob", Account: "bob", SiteID: "site-a", EngName: "Bob Chen", ChineseName: "鮑勃", EmployeeID: "E002"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, us, pub)
```

- [ ] **Step 6: Update TestBroadcastWorker_DMRoom_Integration**

Replace employee collection inserts and store wiring (lines 214-221):

```go
	_, err = db.Collection("employee").InsertMany(ctx, []interface{}{
		bson.M{"accountName": "alice", "name": "愛麗絲", "engName": "Alice Wang"},
		bson.M{"accountName": "bob", "name": "鮑勃", "engName": "Bob Chen"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)
```

With:

```go
	_, err = db.Collection("users").InsertMany(ctx, []interface{}{
		model.User{ID: "u-alice", Account: "alice", SiteID: "site-a", EngName: "Alice Wang", ChineseName: "愛麗絲", EmployeeID: "E001"},
		model.User{ID: "u-bob", Account: "bob", SiteID: "site-a", EngName: "Bob Chen", ChineseName: "鮑勃", EmployeeID: "E002"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, us, pub)
```

- [ ] **Step 7: Remove unused `bson` import if present**

After replacing all `bson.M{...}` employee inserts with `model.User{...}` structs, remove the `bson` import only if no other usage remains in the file. Check — `bson` is still used in the room assertions (e.g., `bson.M{"_id": "r1"}` on line 110) and subscription mentions assertions (e.g., `bson.M{"u.account": "bob", "roomId": "r3"}` on line 193), so keep the import.

- [ ] **Step 8: Run unit tests to confirm no regressions**

Run: `make test SERVICE=broadcast-worker`
Expected: All tests PASS.

- [ ] **Step 9: Commit**

```bash
git add broadcast-worker/integration_test.go
git commit -m "test: update broadcast-worker integration tests to use users collection via userstore"
```

---

### Task 8: Final Verification

- [ ] **Step 1: Run lint**

Run: `make lint`
Expected: No lint errors.

- [ ] **Step 2: Run all unit tests**

Run: `make test`
Expected: All tests PASS across the entire project.

- [ ] **Step 3: Verify Employee model and collection untouched**

Run: `cat pkg/model/employee.go`
Expected: File is unchanged — still has `AccountName`, `Name`, `EngName` fields.

- [ ] **Step 4: Push**

```bash
git push -u origin claude/update-user-model-broadcast-5vu37
```
