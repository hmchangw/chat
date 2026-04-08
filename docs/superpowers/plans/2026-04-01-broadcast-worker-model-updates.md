# Broadcast Worker Model Updates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enrich broadcast-worker room events with full message payloads, participant details from the employee collection, and remove the redundant Room.Origin field.

**Architecture:** Add `Participant`, `Employee`, and `ClientMessage` model types. Broadcast-worker batch-queries the `employee` MongoDB collection by `accountName` to resolve sender and mention details. `RoomEvent` carries `ClientMessage` (with `Sender`) for both group and DM events, and `Mentions` becomes `[]Participant`.

**Tech Stack:** Go 1.24, MongoDB (go.mongodb.org/mongo-driver/v2), go.uber.org/mock, testify

---

### Task 1: Add new model types (`Participant`, `Employee`, `ClientMessage`)

**Files:**
- Modify: `pkg/model/event.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing tests for new types**

Add round-trip tests to `pkg/model/model_test.go`:

```go
func TestParticipantJSON(t *testing.T) {
	t.Run("with userID", func(t *testing.T) {
		p := model.Participant{
			UserID:      "u1",
			Account:    "alice",
			ChineseName: "愛麗絲",
			EngName:     "Alice Wang",
		}
		roundTrip(t, &p, &model.Participant{})
	})

	t.Run("without userID omitted", func(t *testing.T) {
		p := model.Participant{
			Account:    "bob",
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
}

func TestClientMessageJSON(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cm := model.ClientMessage{
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", Account: "alice",
			Content: "hello", CreatedAt: now,
		},
		Sender: &model.Participant{
			UserID:      "u1",
			Account:    "alice",
			ChineseName: "愛麗絲",
			EngName:     "Alice Wang",
		},
	}
	data, err := json.Marshal(cm)
	require.NoError(t, err)

	var dst model.ClientMessage
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, cm, dst)

	// Verify inline embedding — message fields should be at top level
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw, "id")
	assert.Contains(t, raw, "roomId")
	assert.Contains(t, raw, "sender")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — `model.Participant` and `model.ClientMessage` do not exist.

- [ ] **Step 3: Add Participant, Employee, and ClientMessage types**

Add to `pkg/model/event.go`, before the `RoomEvent` struct:

```go
// Participant represents a user with display name info for client rendering.
type Participant struct {
	UserID      string `json:"userId,omitempty" bson:"userId,omitempty"`
	Account    string `json:"account" bson:"account"`
	ChineseName string `json:"chineseName" bson:"chineseName"`
	EngName     string `json:"engName" bson:"engName"`
}

// Employee holds employee data looked up from the employee MongoDB collection.
type Employee struct {
	AccountName string `bson:"accountName"`
	Name        string `bson:"name"`
	EngName     string `bson:"engName"`
}

// ClientMessage wraps Message with enriched sender info for client consumption.
type ClientMessage struct {
	Message `json:",inline" bson:",inline"`
	Sender  *Participant `json:"sender,omitempty"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/model/event.go pkg/model/model_test.go
git commit -m "feat: add Participant, Employee, and ClientMessage model types"
```

---

### Task 2: Remove `Room.Origin` and update `RoomEvent`

**Files:**
- Modify: `pkg/model/room.go`
- Modify: `pkg/model/event.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Update model tests for Room and RoomEvent**

In `pkg/model/model_test.go`:

Update `TestRoomJSON` — remove `Origin: "site-a"` from the test Room literal.

Update `TestRoomEventJSON` `"all fields populated"` subtest:
- Replace `Origin: "site-a"` with `SiteID: "site-a"`
- Change `Mentions` from `[]string{"user-2", "user-3"}` to `[]model.Participant{{Account: "user-2", ChineseName: "user-2", EngName: "user-2"}, {Account: "user-3", ChineseName: "user-3", EngName: "user-3"}}`
- Change `Message: &msg` to `Message: &model.ClientMessage{Message: msg, Sender: &model.Participant{UserID: "user-1", Account: "alice", ChineseName: "愛麗絲", EngName: "Alice Wang"}}`

Update `TestRoomEventJSON` `"nil message and empty mentions omitted"` subtest:
- Replace `Origin: "site-b"` with `SiteID: "site-b"`

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — `Room` still has `Origin`, `RoomEvent` still has `Origin`/`[]string` Mentions/`*Message`.

- [ ] **Step 3: Remove Origin from Room, update RoomEvent**

In `pkg/model/room.go`, remove line 18:
```go
Origin           string    `json:"origin" bson:"origin"`
```

In `pkg/model/event.go`, update `RoomEvent` to:
```go
type RoomEvent struct {
	Type      RoomEventType `json:"type"`
	RoomID    string        `json:"roomId"`
	Timestamp time.Time     `json:"timestamp"`

	RoomName  string    `json:"roomName"`
	RoomType  RoomType  `json:"roomType"`
	SiteID    string    `json:"siteId"`
	UserCount int       `json:"userCount"`
	LastMsgAt time.Time `json:"lastMsgAt"`
	LastMsgID string    `json:"lastMsgId"`

	Mentions   []Participant  `json:"mentions,omitempty"`
	MentionAll bool           `json:"mentionAll,omitempty"`

	HasMention bool           `json:"hasMention,omitempty"`

	Message *ClientMessage `json:"message,omitempty"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/model/room.go pkg/model/event.go pkg/model/model_test.go
git commit -m "refactor: remove Room.Origin, update RoomEvent with SiteID, Participant mentions, ClientMessage"
```

---

### Task 3: Add `FindEmployeesByAccountNames` to store interface and MongoDB implementation

**Files:**
- Modify: `broadcast-worker/store.go`
- Modify: `broadcast-worker/store_mongo.go`
- Modify: `broadcast-worker/main.go`

- [ ] **Step 1: Add new method to store interface**

In `broadcast-worker/store.go`, add the new method to the `Store` interface:

```go
type Store interface {
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
	UpdateRoomOnNewMessage(ctx context.Context, roomID string, msgID string, msgAt time.Time, mentionAll bool) error
	SetSubscriptionMentions(ctx context.Context, roomID string, accounts []string) error
	FindEmployeesByAccountNames(ctx context.Context, accountNames []string) ([]model.Employee, error)
}
```

- [ ] **Step 2: Add employee collection to mongoStore and implement the method**

In `broadcast-worker/store_mongo.go`, update the struct and constructor:

```go
type mongoStore struct {
	roomCol *mongo.Collection
	subCol  *mongo.Collection
	empCol  *mongo.Collection
}

func NewMongoStore(roomCol, subCol, empCol *mongo.Collection) *mongoStore {
	return &mongoStore{roomCol: roomCol, subCol: subCol, empCol: empCol}
}
```

Add the implementation:

```go
func (m *mongoStore) FindEmployeesByAccountNames(ctx context.Context, accountNames []string) ([]model.Employee, error) {
	filter := bson.M{"accountName": bson.M{"$in": accountNames}}
	projection := bson.M{"accountName": 1, "name": 1, "engName": 1, "_id": 0}
	opts := options.Find().SetProjection(projection)
	cursor, err := m.empCol.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("query employees by account names: %w", err)
	}
	defer cursor.Close(ctx)
	var employees []model.Employee
	if err := cursor.All(ctx, &employees); err != nil {
		return nil, fmt.Errorf("decode employees: %w", err)
	}
	return employees, nil
}
```

Note: Add `"go.mongodb.org/mongo-driver/v2/mongo/options"` to the imports if not already present.

- [ ] **Step 3: Update main.go to pass the employee collection**

In `broadcast-worker/main.go`, change the `NewMongoStore` call (line 46):

```go
store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
```

- [ ] **Step 4: Regenerate mocks**

Run: `make generate SERVICE=broadcast-worker`
Expected: `mock_store_test.go` is regenerated with the new `FindEmployeesByAccountNames` mock method.

- [ ] **Step 5: Verify compilation**

Run: `make build SERVICE=broadcast-worker`
Expected: Build succeeds (handler.go will have compilation errors at this point since it still references `Origin` — that's expected and will be fixed in Task 4).

Note: If compilation fails due to handler.go referencing `room.Origin`, temporarily comment out `Origin: room.Origin` in `buildRoomEvent` and replace with `SiteID: room.SiteID` to unblock. This will be properly addressed in Task 4.

- [ ] **Step 6: Commit**

```bash
git add broadcast-worker/store.go broadcast-worker/store_mongo.go broadcast-worker/main.go broadcast-worker/mock_store_test.go
git commit -m "feat: add FindEmployeesByAccountNames store method for employee lookup"
```

---

### Task 4: Update handler to use employee lookup and new event types

**Files:**
- Modify: `broadcast-worker/handler.go`
- Modify: `broadcast-worker/handler_test.go`

- [ ] **Step 1: Update handler unit tests**

Replace the full contents of `broadcast-worker/handler_test.go` test fixtures and test functions. Key changes:

Update test fixtures at the top of the file — remove `Origin` from test rooms:

```go
var (
	testGroupRoom = &model.Room{
		ID: "room-1", Name: "general", Type: model.RoomTypeGroup,
		SiteID: "site-a", UserCount: 5,
	}
	testDMRoom = &model.Room{
		ID: "dm-1", Name: "", Type: model.RoomTypeDM,
		SiteID: "site-a", UserCount: 2,
	}
	testDMSubs = []model.Subscription{
		{User: model.SubscriptionUser{ID: "alice-id", Account: "alice"}, RoomID: "dm-1"},
		{User: model.SubscriptionUser{ID: "bob-id", Account: "bob"}, RoomID: "dm-1"},
	}
	testEmployees = []model.Employee{
		{AccountName: "alice", Name: "愛麗絲", EngName: "Alice Wang"},
		{AccountName: "bob", Name: "鮑勃", EngName: "Bob Chen"},
	}
)
```

Update `makeMessageEvent` to include `Username`:

```go
func makeMessageEvent(roomID, content string, msgTime time.Time) []byte {
	evt := model.MessageEvent{
		SiteID: "site-a",
		Message: model.Message{
			ID: "msg-1", RoomID: roomID, UserID: "user-1", Account: "sender",
			Content: content, CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)
	return data
}
```

Add a helper for employee lookup expectations:

```go
func expectEmployeeLookup(store *MockStore, accountNames []string, employees []model.Employee) {
	store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.InAnyOrder(accountNames)).Return(employees, nil)
}
```

Update `TestHandler_HandleMessage_GroupRoom`:

In each subtest, add `FindEmployeesByAccountNames` mock expectation after the existing `SetSubscriptionMentions` expectation. The sender account is `"sender"` (from `makeMessageEvent`), so the lookup includes `"sender"` plus any mentioned accounts.

For the `"no mentions"` subtest:
```go
expectEmployeeLookup(store, []string{"sender"}, []model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}})
```

For the `"individual mentions"` subtest:
```go
expectEmployeeLookup(store, []string{"sender", "alice", "bob"}, append([]model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}}, testEmployees...))
```

For the `"mention all case insensitive"` subtest:
```go
expectEmployeeLookup(store, []string{"sender"}, []model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}})
```

For the `"mention all and individual"` subtest:
```go
expectEmployeeLookup(store, []string{"sender", "alice"}, []model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}, testEmployees[0]})
```

Update group room assertions — replace `assert.Equal(t, "site-a", evt.Origin)` with `assert.Equal(t, "site-a", evt.SiteID)`.

Remove `assert.Nil(t, evt.Message, "group room events must not carry Message payload")` and replace with:
```go
require.NotNil(t, evt.Message, "group room events must carry Message payload")
assert.Equal(t, "msg-1", evt.Message.ID)
require.NotNil(t, evt.Message.Sender)
assert.Equal(t, "user-1", evt.Message.Sender.UserID)
assert.Equal(t, "sender", evt.Message.Sender.Account)
assert.Equal(t, "寄件者", evt.Message.Sender.ChineseName)
assert.Equal(t, "Sender Lin", evt.Message.Sender.EngName)
```

Update mention assertions for the `"individual mentions"` subtest — check `Mentions` as `[]Participant`:
```go
if tc.wantMentions != nil {
	require.Len(t, evt.Mentions, len(tc.wantMentions))
	mentionUsernames := make([]string, len(evt.Mentions))
	for i, m := range evt.Mentions {
		mentionUsernames[i] = m.UserAccount
	}
	assert.ElementsMatch(t, tc.wantMentions, mentionUsernames)
	for _, m := range evt.Mentions {
		assert.Empty(t, m.UserID, "mention participants should not have userID")
		assert.NotEmpty(t, m.ChineseName)
		assert.NotEmpty(t, m.EngName)
	}
} else {
	assert.Empty(t, evt.Mentions)
}
```

Update `TestHandler_HandleMessage_DMRoom`:

Add `FindEmployeesByAccountNames` mock expectation in each subtest. For `"no mentions"`:
```go
expectEmployeeLookup(store, []string{"alice"}, testEmployees[:1])
```

Note: The DM test uses `UserID: "alice-id"` and `Account: "alice"` as sender (from the message event constructed inline), so adjust accordingly. Actually, looking at the existing test, the DM message event is constructed inline with `UserID: "alice-id"`, `Content: tc.content`. The `Username` field is not set in the existing test — it needs to be added: `Account: "alice"`.

For `"with mention"` (`@bob` is mentioned):
```go
expectEmployeeLookup(store, []string{"alice", "bob"}, testEmployees)
```

Update DM assertions to check `evt.Message.Sender` and use `evt.Message.ID` (the `Message` field is now `*ClientMessage`):
```go
require.NotNil(t, aliceEvt.Message)
assert.Equal(t, "msg-1", aliceEvt.Message.ID)
require.NotNil(t, aliceEvt.Message.Sender)
assert.Equal(t, "alice-id", aliceEvt.Message.Sender.UserID)
assert.Equal(t, "alice", aliceEvt.Message.Sender.Account)
```

Add a new error test case for employee lookup failure (fallback behavior):
```go
t.Run("employee lookup fails fallback to account", func(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	pub := &mockPublisher{}

	store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
	store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
	store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.Any()).Return(nil, errors.New("db error"))

	h := NewHandler(store, pub)
	err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
	require.NoError(t, err)

	require.Len(t, pub.records, 1)
	evt := decodeRoomEvent(t, pub.records[0].data)
	require.NotNil(t, evt.Message)
	require.NotNil(t, evt.Message.Sender)
	assert.Equal(t, "sender", evt.Message.Sender.Account)
	assert.Equal(t, "sender", evt.Message.Sender.ChineseName)
	assert.Equal(t, "sender", evt.Message.Sender.EngName)
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=broadcast-worker`
Expected: FAIL — handler still uses `Origin`, `[]string` mentions, `*Message`, and doesn't call `FindEmployeesByAccountNames`.

- [ ] **Step 3: Update handler implementation**

In `broadcast-worker/handler.go`, update the `HandleMessage` method. After the `SetSubscriptionMentions` block (after line 54) and before the switch statement, add employee lookup:

```go
	// Collect all accounts for employee lookup (sender + mentioned)
	lookupUsernames := make([]string, 0, 1+len(mentionedAccounts))
	lookupUsernames = append(lookupUsernames, msg.UserAccount)
	for _, u := range mentionedAccounts {
		if u != msg.UserAccount {
			lookupUsernames = append(lookupUsernames, u)
		}
	}

	employeeMap := make(map[string]model.Employee)
	employees, err := h.store.FindEmployeesByAccountNames(ctx, lookupUsernames)
	if err != nil {
		slog.Warn("employee lookup failed, falling back to accounts", "error", err)
	} else {
		for _, emp := range employees {
			employeeMap[emp.AccountName] = emp
		}
	}

	clientMsg := buildClientMessage(&msg, employeeMap)
	mentionParticipants := buildMentionParticipants(mentionedAccounts, employeeMap)
```

Update `publishGroupEvent` signature and body:

```go
func (h *Handler) publishGroupEvent(room *model.Room, clientMsg *model.ClientMessage, mentionAll bool, mentions []model.Participant) error {
	evt := buildRoomEvent(room, clientMsg)
	evt.MentionAll = mentionAll
	if len(mentions) > 0 {
		evt.Mentions = mentions
	}

	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal group room event: %w", err)
	}
	return h.pub.Publish(subject.RoomEvent(room.ID), payload)
}
```

Update `publishDMEvents` signature and body:

```go
func (h *Handler) publishDMEvents(ctx context.Context, room *model.Room, clientMsg *model.ClientMessage, mentionedAccounts []string) error {
	subs, err := h.store.ListSubscriptions(ctx, room.ID)
	if err != nil {
		return fmt.Errorf("list subscriptions for DM room %s: %w", room.ID, err)
	}

	mentionSet := make(map[string]struct{}, len(mentionedAccounts))
	for _, name := range mentionedAccounts {
		mentionSet[name] = struct{}{}
	}

	for i := range subs {
		_, hasMention := mentionSet[subs[i].User.Account]

		evt := buildRoomEvent(room, clientMsg)
		evt.HasMention = hasMention

		payload, err := json.Marshal(evt)
		if err != nil {
			return fmt.Errorf("marshal DM event for user %s: %w", subs[i].User.Account, err)
		}
		if err := h.pub.Publish(subject.UserRoomEvent(subs[i].User.Account), payload); err != nil {
			slog.Error("publish DM event failed", "error", err, "account", subs[i].User.Account)
		}
	}
	return nil
}
```

Update `buildRoomEvent` to accept `*model.ClientMessage` and use `SiteID`:

```go
func buildRoomEvent(room *model.Room, clientMsg *model.ClientMessage) model.RoomEvent {
	return model.RoomEvent{
		Type:      model.RoomEventNewMessage,
		RoomID:    room.ID,
		Timestamp: clientMsg.CreatedAt,
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

Add helper functions:

```go
func buildClientMessage(msg *model.Message, employeeMap map[string]model.Employee) *model.ClientMessage {
	sender := model.Participant{
		UserID:   msg.UserID,
		Account: msg.UserAccount,
	}
	if emp, ok := employeeMap[msg.UserAccount]; ok {
		sender.ChineseName = emp.Name
		sender.EngName = emp.EngName
	} else {
		sender.ChineseName = msg.UserAccount
		sender.EngName = msg.UserAccount
	}
	return &model.ClientMessage{
		Message: *msg,
		Sender:  &sender,
	}
}

func buildMentionParticipants(mentionedAccounts []string, employeeMap map[string]model.Employee) []model.Participant {
	if len(mentionedAccounts) == 0 {
		return nil
	}
	participants := make([]model.Participant, len(mentionedAccounts))
	for i, account := range mentionedAccounts {
		p := model.Participant{Account: account}
		if emp, ok := employeeMap[username]; ok {
			p.ChineseName = emp.Name
			p.EngName = emp.EngName
		} else {
			p.ChineseName = account
			p.EngName = account
		}
		participants[i] = p
	}
	return participants
}
```

Update the switch statement in `HandleMessage` to use the new variables:

```go
	switch room.Type {
	case model.RoomTypeGroup:
		return h.publishGroupEvent(room, clientMsg, mentionAll, mentionParticipants)
	case model.RoomTypeDM:
		return h.publishDMEvents(ctx, room, clientMsg, mentionedAccounts)
	default:
		slog.Warn("unknown room type, skipping fan-out", "type", room.Type, "roomID", room.ID)
		return nil
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=broadcast-worker`
Expected: PASS

- [ ] **Step 5: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add broadcast-worker/handler.go broadcast-worker/handler_test.go
git commit -m "feat: enrich RoomEvent with ClientMessage, Participant sender/mentions, employee lookup"
```

---

### Task 5: Update integration tests

**Files:**
- Modify: `broadcast-worker/integration_test.go`

- [ ] **Step 1: Update integration test setup and assertions**

In `broadcast-worker/integration_test.go`:

Update the `recordingPublisher` to also capture data for assertions:

```go
type recordingPublisher struct {
	mu      sync.Mutex
	records []publishRecord
}

type publishRecord struct {
	subject string
	data    []byte
}

func (p *recordingPublisher) Publish(subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, publishRecord{subject: subj, data: data})
	return nil
}

func (p *recordingPublisher) getRecords() []publishRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]publishRecord, len(p.records))
	copy(cp, p.records)
	return cp
}
```

Remove `Origin: "site-a"` from all room insertions — change every `model.Room{...Origin: "site-a"...}` to remove that field.

Update `NewMongoStore` calls to include employee collection:

```go
store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
```

Insert employee test data in each test that needs it. Add before `NewMongoStore` in each test:

```go
_, err = db.Collection("employee").InsertMany(ctx, []interface{}{
	bson.M{"accountName": "alice", "name": "愛麗絲", "engName": "Alice Wang"},
	bson.M{"accountName": "bob", "name": "鮑勃", "engName": "Bob Chen"},
})
require.NoError(t, err)
```

For `TestBroadcastWorker_GroupRoom_Integration`, update to verify the published event has `SiteID` and `Message` with `Sender`. Update the publisher to use `recordingPublisher` with records (it already captures subjects — extend to capture data). After the existing subject assertion, add:

```go
records := pub.getRecords()
var evt model.RoomEvent
require.NoError(t, json.Unmarshal(records[0].data, &evt))
assert.Equal(t, "site-a", evt.SiteID)
require.NotNil(t, evt.Message)
require.NotNil(t, evt.Message.Sender)
assert.Equal(t, "u1", evt.Message.Sender.UserID)
```

Note: The message in this test has `UserID: "u1"` but no `Username`. Add `Account: "alice"` to the test message so the employee lookup works:

```go
Message: model.Message{
	ID: "m1", RoomID: "r1", UserID: "u1", Account: "alice", Content: "hello", CreatedAt: msgTime,
},
```

For `TestBroadcastWorker_GroupRoom_IndividualMention_Integration`, verify mention participants have employee data:

```go
records := pub.getRecords()
var evt model.RoomEvent
require.NoError(t, json.Unmarshal(records[0].data, &evt))
require.Len(t, evt.Mentions, 1)
assert.Equal(t, "bob", evt.Mentions[0].Username)
assert.Equal(t, "鮑勃", evt.Mentions[0].ChineseName)
assert.Equal(t, "Bob Chen", evt.Mentions[0].EngName)
assert.Empty(t, evt.Mentions[0].UserID)
```

For the DM test, add `Account: "alice"` to the message and verify sender:

```go
records := pub.getRecords()
for _, rec := range records {
	var evt model.RoomEvent
	require.NoError(t, json.Unmarshal(rec.data, &evt))
	require.NotNil(t, evt.Message)
	require.NotNil(t, evt.Message.Sender)
	assert.Equal(t, "u1", evt.Message.Sender.UserID)
	assert.Equal(t, "alice", evt.Message.Sender.Account)
	assert.Equal(t, "愛麗絲", evt.Message.Sender.ChineseName)
}
```

- [ ] **Step 2: Run integration tests**

Run: `make test-integration SERVICE=broadcast-worker`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add broadcast-worker/integration_test.go
git commit -m "test: update broadcast-worker integration tests for model changes"
```

---

### Task 6: Run full test suite and lint

**Files:** None (verification only)

- [ ] **Step 1: Run all unit tests**

Run: `make test`
Expected: PASS — all packages compile and tests pass.

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 3: Push**

```bash
git push -u origin claude/update-broadcast-models-qNbhy
```
