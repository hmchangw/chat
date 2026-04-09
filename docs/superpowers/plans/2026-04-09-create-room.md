# Create Room Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the bare-bones create-room flow with one that supports bulk member addition (users, orgs, channels), automatic room type derivation (DM vs group), DM duplicate prevention, and system message publishing.

**Architecture:** Room-service validates the payload, expands channels/orgs, deduplicates, derives room type and name, then publishes a `CreateRoomMessage` to the ROOMS JetStream stream. Room-worker creates the room doc, owner subscription, and member subscriptions synchronously (before ack), then asynchronously writes `room_members` docs (if orgs involved), publishes system messages (`room_created`, `members_added`), `SubscriptionUpdateEvent`s, `MemberChangeEvent`, and outbox events for federation.

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB, `go.uber.org/mock`, `stretchr/testify`

**Spec:** `docs/superpowers/specs/2026-04-09-create-room-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `pkg/model/room.go` | Replace `CreateRoomRequest`, remove `ListRoomsResponse`, change `Room.Type` bson tag to `"t"` |
| `pkg/model/subscription.go` | Add `Name` and `Type` fields to `Subscription` |
| `pkg/model/event.go` | Add `CreateRoomMessage` and `MembersAdded` structs |
| `pkg/model/model_test.go` | Round-trip tests for changed/new models |
| `pkg/subject/subject.go` | Add `RoomCreate` subject builder and `RoomCreateWildcard` |
| `pkg/subject/subject_test.go` | Test new subject builders |
| `pkg/stream/stream.go` | Update `Rooms` stream config to capture `room.create` subjects |
| `room-service/store.go` | Add `FindDMBetween`, `GetUser`, `GetOrgAccounts`, `GetRoomMembers`, `ListSubscriptionsByRoom`, `CountSubscriptions` to `RoomStore` |
| `room-service/store_mongo.go` | Implement new store methods |
| `room-service/handler.go` | Rewrite `handleCreateRoom`, add `expandChannels`, `resolveOrgs`, `deriveRoomType`, `deriveRoomName`, helpers |
| `room-service/handler_test.go` | Table-driven tests for all create-room scenarios |
| `room-service/mock_store_test.go` | Regenerated mock |
| `room-worker/store.go` | Add `CreateRoom`, `BulkCreateSubscriptions`, `CreateRoomMember`, `GetUser` to `SubscriptionStore` |
| `room-worker/store_mongo.go` | Implement new store methods |
| `room-worker/handler.go` | Add `processCreateRoom`, update `HandleJetStreamMsg` routing |
| `room-worker/handler_test.go` | Table-driven tests for create-room worker |
| `room-worker/mock_store_test.go` | Regenerated mock |

---

### Task 1: Model changes — CreateRoomRequest, Subscription, Room, new event types

**Files:**
- Modify: `pkg/model/room.go`
- Modify: `pkg/model/subscription.go`
- Modify: `pkg/model/event.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing round-trip tests for updated models**

Add to `pkg/model/model_test.go`:

```go
func TestCreateRoomRequestJSON(t *testing.T) {
	src := model.CreateRoomRequest{
		Name:     "general",
		Users:    []string{"alice", "bob"},
		Orgs:     []string{"eng-team"},
		Channels: []string{"room-1"},
	}
	roundTrip(t, &src, &model.CreateRoomRequest{})
}

func TestCreateRoomRequestJSON_NoName(t *testing.T) {
	src := model.CreateRoomRequest{
		Users: []string{"alice"},
	}
	data, err := json.Marshal(&src)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasName := raw["name"]
	assert.False(t, hasName, "name should be omitted when empty")
}

func TestSubscriptionJSON_WithNameAndType(t *testing.T) {
	hss := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := model.Subscription{
		ID:                 "s1",
		User:               model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID:             "r1",
		SiteID:             "site-a",
		Role:               model.RoleOwner,
		Name:               "general",
		Type:               model.RoomTypeGroup,
		HistorySharedSince: &hss,
		JoinedAt:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt:         time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		HasMention:         true,
	}
	data, err := json.Marshal(&s)
	require.NoError(t, err)
	var dst model.Subscription
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, s, dst)
}

func TestCreateRoomMessageJSON(t *testing.T) {
	src := model.CreateRoomMessage{
		RoomID:         "r1",
		Name:           "general",
		Type:           model.RoomTypeGroup,
		CreatorID:      "u1",
		CreatorAccount: "alice",
		CreatorName:    "Alice Wang",
		SiteID:         "site-a",
		Users:          []string{"bob", "carol"},
		Orgs:           []string{"eng-team"},
		Channels:       []string{"room-2"},
		RawIndividuals: []string{"bob"},
		HasOrgs:        true,
	}
	roundTrip(t, &src, &model.CreateRoomMessage{})
}

func TestMemberChangeEventJSON(t *testing.T) {
	src := model.MemberChangeEvent{
		Type:     "member-added",
		RoomID:   "r1",
		Accounts: []string{"alice", "bob"},
		SiteID:   "site-a",
	}
	roundTrip(t, &src, &model.MemberChangeEvent{})
}

func TestMembersAddedJSON(t *testing.T) {
	src := model.MembersAdded{
		Individuals:     []string{"alice", "bob"},
		Orgs:            []string{"eng-team"},
		Channels:        []string{"room-1"},
		AddedUsersCount: 5,
	}
	roundTrip(t, &src, &model.MembersAdded{})
}
```

Also update the existing `TestSubscriptionJSON` to include the new fields:

```go
func TestSubscriptionJSON(t *testing.T) {
	hss := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := model.Subscription{
		ID:                 "s1",
		User:               model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID:             "r1",
		SiteID:             "site-a",
		Role:               model.RoleOwner,
		Name:               "general",
		Type:               model.RoomTypeGroup,
		HistorySharedSince: &hss,
		JoinedAt:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt:         time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		HasMention:         true,
	}

	data, err := json.Marshal(&s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dst model.Subscription
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(s, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, s)
	}
}
```

Also update `TestSubscriptionUpdateEventJSON` to include the new fields in the embedded Subscription:

```go
func TestSubscriptionUpdateEventJSON(t *testing.T) {
	src := model.SubscriptionUpdateEvent{
		UserID: "u1",
		Subscription: model.Subscription{
			ID:       "s1",
			User:     model.SubscriptionUser{ID: "u1", Account: "alice"},
			RoomID:   "r1",
			SiteID:   "site-a",
			Role:     model.RoleMember,
			Name:     "general",
			Type:     model.RoomTypeGroup,
			JoinedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Action:    "added",
		Timestamp: 1735689600000,
	}
	data, err := json.Marshal(&src)
	require.NoError(t, err)
	var dst model.SubscriptionUpdateEvent
	require.NoError(t, json.Unmarshal(data, &dst))
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — `model.CreateRoomMessage`, `model.MembersAdded` undefined, `model.Subscription` missing `Name`/`Type` fields, `model.CreateRoomRequest` has wrong shape.

- [ ] **Step 3: Update model structs**

In `pkg/model/room.go`, replace the entire file:

```go
package model

import "time"

type RoomType string

const (
	RoomTypeGroup RoomType = "group"
	RoomTypeDM    RoomType = "dm"
)

type Room struct {
	ID               string    `json:"id" bson:"_id"`
	Name             string    `json:"name" bson:"name"`
	Type             RoomType  `json:"type" bson:"t"`
	CreatedBy        string    `json:"createdBy" bson:"createdBy"`
	SiteID           string    `json:"siteId" bson:"siteId"`
	UserCount        int       `json:"userCount" bson:"userCount"`
	LastMsgAt        time.Time `json:"lastMsgAt" bson:"lastMsgAt"`
	LastMsgID        string    `json:"lastMsgId" bson:"lastMsgId"`
	LastMentionAllAt time.Time `json:"lastMentionAllAt" bson:"lastMentionAllAt"`
	CreatedAt        time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt" bson:"updatedAt"`
}

type CreateRoomRequest struct {
	Name     string   `json:"name,omitempty" bson:"name,omitempty"`
	Users    []string `json:"users"          bson:"users"`
	Orgs     []string `json:"orgs"           bson:"orgs"`
	Channels []string `json:"channels"       bson:"channels"`
}
```

In `pkg/model/subscription.go`, add `Name` and `Type` fields:

```go
package model

import "time"

type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

type SubscriptionUser struct {
	ID      string `json:"id" bson:"_id"`
	Account string `json:"account" bson:"account"`
}

type Subscription struct {
	ID                 string           `json:"id" bson:"_id"`
	User               SubscriptionUser `json:"u" bson:"u"`
	RoomID             string           `json:"roomId" bson:"roomId"`
	SiteID             string           `json:"siteId" bson:"siteId"`
	Role               Role             `json:"role" bson:"role"`
	Name               string           `json:"name" bson:"name"`
	Type               RoomType         `json:"type" bson:"t"`
	HistorySharedSince *time.Time       `json:"historySharedSince,omitempty" bson:"historySharedSince,omitempty"`
	JoinedAt           time.Time        `json:"joinedAt" bson:"joinedAt"`
	LastSeenAt         time.Time        `json:"lastSeenAt" bson:"lastSeenAt"`
	HasMention         bool             `json:"hasMention" bson:"hasMention"`
}
```

In `pkg/model/event.go`, add the two new structs at the end of the file (before the closing):

```go
// CreateRoomMessage is the internal message published to the ROOMS JetStream
// stream by room-service for room-worker to process.
type CreateRoomMessage struct {
	RoomID         string   `json:"roomId"`
	Name           string   `json:"name"`
	Type           RoomType `json:"type"`
	CreatorID      string   `json:"creatorId"`
	CreatorAccount string   `json:"creatorAccount"`
	CreatorName    string   `json:"creatorName"`
	SiteID         string   `json:"siteId"`
	Users          []string `json:"users"`          // resolved, deduped account list (excludes requester)
	Orgs           []string `json:"orgs"`           // org IDs (for room_members docs)
	Channels       []string `json:"channels"`       // original channel room IDs from payload
	RawIndividuals []string `json:"rawIndividuals"` // original individual accounts from payload
	HasOrgs        bool     `json:"hasOrgs"`        // controls whether room_members are written
}

// MemberChangeEvent notifies room subscribers of membership changes.
type MemberChangeEvent struct {
	Type     string   `json:"type"     bson:"type"` // "member-added" or "member-removed"
	RoomID   string   `json:"roomId"   bson:"roomId"`
	Accounts []string `json:"accounts" bson:"accounts"`
	SiteID   string   `json:"siteId"   bson:"siteId"`
}

// MembersAdded is stored as JSON in the sys_msg_data column of messages_by_room.
type MembersAdded struct {
	Individuals     []string `json:"individuals"`
	Orgs            []string `json:"orgs"`
	Channels        []string `json:"channels"`
	AddedUsersCount int      `json:"addedUsersCount"`
}
```

- [ ] **Step 4: Fix compilation errors in other packages**

The `Room.Type` bson tag change and removal of `ListRoomsResponse` may break `room-service/handler.go`. Update `natsListRooms` in `room-service/handler.go` to return rooms directly:

```go
func (h *Handler) natsListRooms(m otelnats.Msg) {
	rooms, err := h.store.ListRooms(m.Context())
	if err != nil {
		natsutil.ReplyError(m.Msg, err.Error())
		return
	}
	natsutil.ReplyJSON(m.Msg, map[string][]model.Room{"rooms": rooms})
}
```

Also update `handleCreateRoom` temporarily to use the new `CreateRoomRequest` fields (it will be fully rewritten in Task 3, but needs to compile now):

```go
func (h *Handler) handleCreateRoom(ctx context.Context, data []byte) ([]byte, error) {
	var req model.CreateRoomRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	now := time.Now().UTC()
	room := model.Room{
		ID:        uuid.New().String(),
		Name:      req.Name,
		Type:      model.RoomTypeGroup,
		SiteID:    h.siteID,
		UserCount: 1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.store.CreateRoom(ctx, &room); err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}

	return json.Marshal(room)
}
```

Update `room-service/handler_test.go` — the existing `TestHandler_CreateRoom` test uses the old `CreateRoomRequest` shape. Replace it temporarily:

```go
func TestHandler_CreateRoom(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000}

	req := model.CreateRoomRequest{Name: "general", Users: []string{"bob"}}
	data, _ := json.Marshal(req)

	resp, err := h.handleCreateRoom(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var room model.Room
	json.Unmarshal(resp, &room)
	if room.Name != "general" {
		t.Errorf("got %+v", room)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `make test`
Expected: PASS — all model tests pass, existing service tests compile.

- [ ] **Step 6: Commit**

```bash
git add pkg/model/ room-service/handler.go room-service/handler_test.go
git commit -m "feat: update models for create-room — CreateRoomRequest, Subscription Name/Type, Room.Type bson tag, CreateRoomMessage, MembersAdded"
```

---

### Task 2: Subject builders and stream config

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`
- Modify: `pkg/stream/stream.go`

- [ ] **Step 1: Write failing test for new subject builders**

Add to `pkg/subject/subject_test.go`:

```go
func TestRoomCreate(t *testing.T) {
	got := subject.RoomCreate("alice", "site-a")
	want := "chat.user.alice.request.rooms.create.site-a"
	if got != want {
		t.Errorf("RoomCreate = %q, want %q", got, want)
	}
}

func TestRoomCreateWildcard(t *testing.T) {
	got := subject.RoomCreateWildcard("site-a")
	want := "chat.user.*.request.rooms.create.site-a"
	if got != want {
		t.Errorf("RoomCreateWildcard = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/subject`
Expected: FAIL — `subject.RoomCreate` and `subject.RoomCreateWildcard` undefined.

- [ ] **Step 3: Add subject builders**

Add to `pkg/subject/subject.go`:

```go
func RoomCreate(account, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.rooms.create.%s", account, siteID)
}

func RoomCreateWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.rooms.create.%s", siteID)
}

func RoomMemberEvent(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event.member", roomID)
}
```

Update `pkg/stream/stream.go` to capture both invite and create-room subjects:

```go
func Rooms(siteID string) Config {
	return Config{
		Name: fmt.Sprintf("ROOMS_%s", siteID),
		Subjects: []string{
			fmt.Sprintf("chat.user.*.request.room.*.%s.member.>", siteID),
			fmt.Sprintf("chat.user.*.request.rooms.create.%s", siteID),
		},
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/subject && make test SERVICE=pkg/stream`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/ pkg/stream/
git commit -m "feat: add RoomCreate subject builder and update ROOMS stream config"
```

---

### Task 3: Room-service store expansion

**Files:**
- Modify: `room-service/store.go`
- Modify: `room-service/store_mongo.go`

- [ ] **Step 1: Expand the store interface**

Replace `room-service/store.go`:

```go
package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . RoomStore

type RoomStore interface {
	CreateRoom(ctx context.Context, room *model.Room) error
	GetRoom(ctx context.Context, id string) (*model.Room, error)
	ListRooms(ctx context.Context) ([]model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	FindDMBetween(ctx context.Context, account1, account2 string) (*model.Room, error)
	GetUser(ctx context.Context, account string) (*model.User, error)
	GetOrgAccounts(ctx context.Context, orgID string) ([]string, error)
	GetRoomMembers(ctx context.Context, roomID string) ([]model.RoomMember, error)
	ListSubscriptionsByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
	CountSubscriptions(ctx context.Context, roomID string) (int, error)
}
```

- [ ] **Step 2: Implement new store methods**

Add to `room-service/store_mongo.go`:

```go
func (s *MongoStore) FindDMBetween(ctx context.Context, account1, account2 string) (*model.Room, error) {
	// Find rooms where both accounts have subscriptions and room type is "dm"
	pipeline := bson.A{
		bson.M{"$match": bson.M{"u.account": bson.M{"$in": bson.A{account1, account2}}}},
		bson.M{"$group": bson.M{"_id": "$roomId", "count": bson.M{"$sum": 1}}},
		bson.M{"$match": bson.M{"count": 2}},
	}
	cursor, err := s.subscriptions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate DM subscriptions: %w", err)
	}
	var results []struct {
		RoomID string `bson:"_id"`
	}
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode aggregate results: %w", err)
	}
	for _, r := range results {
		var room model.Room
		if err := s.rooms.FindOne(ctx, bson.M{"_id": r.RoomID, "t": model.RoomTypeDM}).Decode(&room); err == nil {
			return &room, nil
		}
	}
	return nil, nil
}

func (s *MongoStore) GetUser(ctx context.Context, account string) (*model.User, error) {
	var user model.User
	if err := s.users.FindOne(ctx, bson.M{"account": account}).Decode(&user); err != nil {
		return nil, fmt.Errorf("user %q not found: %w", account, err)
	}
	return &user, nil
}

func (s *MongoStore) GetOrgAccounts(ctx context.Context, orgID string) ([]string, error) {
	cursor, err := s.hrData.Find(ctx, bson.M{"orgId": orgID})
	if err != nil {
		return nil, fmt.Errorf("find org accounts: %w", err)
	}
	var docs []struct {
		Account string `bson:"account"`
	}
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode org accounts: %w", err)
	}
	accounts := make([]string, len(docs))
	for i, d := range docs {
		accounts[i] = d.Account
	}
	return accounts, nil
}

func (s *MongoStore) GetRoomMembers(ctx context.Context, roomID string) ([]model.RoomMember, error) {
	cursor, err := s.roomMembers.Find(ctx, bson.M{"rid": roomID})
	if err != nil {
		return nil, fmt.Errorf("find room members: %w", err)
	}
	var members []model.RoomMember
	if err := cursor.All(ctx, &members); err != nil {
		return nil, fmt.Errorf("decode room members: %w", err)
	}
	return members, nil
}

func (s *MongoStore) ListSubscriptionsByRoom(ctx context.Context, roomID string) ([]model.Subscription, error) {
	cursor, err := s.subscriptions.Find(ctx, bson.M{"roomId": roomID})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	var subs []model.Subscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, fmt.Errorf("decode subscriptions: %w", err)
	}
	return subs, nil
}

func (s *MongoStore) CountSubscriptions(ctx context.Context, roomID string) (int, error) {
	count, err := s.subscriptions.CountDocuments(ctx, bson.M{"roomId": roomID})
	if err != nil {
		return 0, fmt.Errorf("count subscriptions: %w", err)
	}
	return int(count), nil
}
```

Update the `MongoStore` struct and constructor to include new collections:

```go
type MongoStore struct {
	rooms         *mongo.Collection
	subscriptions *mongo.Collection
	users         *mongo.Collection
	hrData        *mongo.Collection
	roomMembers   *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		rooms:         db.Collection("rooms"),
		subscriptions: db.Collection("subscriptions"),
		users:         db.Collection("users"),
		hrData:        db.Collection("hr_data"),
		roomMembers:   db.Collection("room_members"),
	}
}
```

- [ ] **Step 3: Regenerate mocks**

Run: `make generate SERVICE=room-service`

- [ ] **Step 4: Run tests to verify compilation**

Run: `make test SERVICE=room-service`
Expected: PASS (existing tests still work with expanded interface via mock)

- [ ] **Step 5: Commit**

```bash
git add room-service/store.go room-service/store_mongo.go room-service/mock_store_test.go
git commit -m "feat: expand room-service store with FindDMBetween, GetUser, GetOrgAccounts, GetRoomMembers, ListSubscriptionsByRoom, CountSubscriptions"
```

---

### Task 4: Rewrite room-service handleCreateRoom

**Files:**
- Modify: `room-service/handler.go`

- [ ] **Step 1: Add helper functions**

Add these helper functions to `room-service/handler.go`:

```go
// filterBots removes accounts ending in ".bot" or starting with "p_".
func filterBots(accounts []string) []string {
	filtered := accounts[:0]
	for _, a := range accounts {
		if !strings.HasSuffix(a, ".bot") && !strings.HasPrefix(a, "p_") {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

// dedup returns a deduplicated copy of the input slice, preserving order.
func dedup(items []string) []string {
	seen := make(map[string]bool, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

// removeAccount removes a specific account from the slice.
func removeAccount(accounts []string, account string) []string {
	filtered := make([]string, 0, len(accounts))
	for _, a := range accounts {
		if a != account {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

// truncate truncates a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
```

- [ ] **Step 2: Update publishToStream signature**

The current `publishToStream` takes `func(ctx, data) error` but we need to publish to different subjects. Change the Handler and constructor:

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

Update `handleInvite` to pass the subject:

```go
	if err := h.publishToStream(ctx, subj, timestampedData); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}
```

Update `room-service/main.go` to match the new signature:

```go
	handler := NewHandler(store, cfg.SiteID, cfg.MaxRoomSize, func(ctx context.Context, subj string, data []byte) error {
		_, err := js.Publish(ctx, subj, data)
		return err
	})
```

- [ ] **Step 3: Rewrite handleCreateRoom**

Replace the `handleCreateRoom` method in `room-service/handler.go`:

```go
func (h *Handler) handleCreateRoom(ctx context.Context, subj string, data []byte) ([]byte, error) {
	var req model.CreateRoomRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	// Extract requester account from subject
	parts := strings.Split(subj, ".")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid subject: %s", subj)
	}
	requesterAccount := parts[2]

	// Determine room type from raw payload (before expansion)
	isDM := req.Name == "" && len(req.Users) == 1 && len(req.Orgs) == 0 && len(req.Channels) == 0
	var roomType model.RoomType
	if isDM {
		roomType = model.RoomTypeDM
	} else {
		roomType = model.RoomTypeGroup
	}

	// Look up requester
	requester, err := h.store.GetUser(ctx, requesterAccount)
	if err != nil {
		return nil, fmt.Errorf("requester not found: %w", err)
	}

	// DM duplicate check
	if isDM {
		existing, err := h.store.FindDMBetween(ctx, requesterAccount, req.Users[0])
		if err != nil {
			return nil, fmt.Errorf("check existing DM: %w", err)
		}
		if existing != nil {
			return nil, fmt.Errorf("DM already exists between these users")
		}
	}

	// Save raw payload breakdown for MembersAdded system message
	rawIndividuals := make([]string, len(req.Users))
	copy(rawIndividuals, req.Users)
	rawOrgs := make([]string, len(req.Orgs))
	copy(rawOrgs, req.Orgs)
	rawChannels := make([]string, len(req.Channels))
	copy(rawChannels, req.Channels)

	// Expand channels
	var channelNames []string
	for _, channelID := range req.Channels {
		room, err := h.store.GetRoom(ctx, channelID)
		if err != nil {
			return nil, fmt.Errorf("get channel room %q: %w", channelID, err)
		}
		channelNames = append(channelNames, room.Name)

		members, err := h.store.GetRoomMembers(ctx, channelID)
		if err != nil {
			return nil, fmt.Errorf("get room members for channel %q: %w", channelID, err)
		}
		for _, m := range members {
			switch m.Member.Type {
			case model.RoomMemberTypeOrg:
				req.Orgs = append(req.Orgs, m.Member.ID)
			case model.RoomMemberTypeIndividual:
				req.Users = append(req.Users, m.Member.Username)
			}
		}

		subs, err := h.store.ListSubscriptionsByRoom(ctx, channelID)
		if err != nil {
			return nil, fmt.Errorf("list subscriptions for channel %q: %w", channelID, err)
		}
		for i := range subs {
			req.Users = append(req.Users, subs[i].User.Account)
		}
	}

	// Resolve orgs
	orgIDs := dedup(req.Orgs)
	for _, orgID := range orgIDs {
		orgAccounts, err := h.store.GetOrgAccounts(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("resolve org %q: %w", orgID, err)
		}
		req.Users = append(req.Users, orgAccounts...)
	}

	// Merge, dedup, filter bots, remove requester
	users := removeAccount(filterBots(dedup(req.Users)), requesterAccount)
	hasOrgs := len(orgIDs) > 0

	// Look up user records for name generation
	var userNames []string
	for _, account := range users {
		u, err := h.store.GetUser(ctx, account)
		if err != nil {
			slog.Warn("user not found for name generation", "account", account)
			userNames = append(userNames, account)
			continue
		}
		userNames = append(userNames, u.Name)
	}

	// Derive room name
	roomName := req.Name
	if roomName == "" {
		if isDM && len(userNames) == 1 {
			roomName = userNames[0]
		} else {
			var nameParts []string
			nameParts = append(nameParts, userNames...)
			nameParts = append(nameParts, orgIDs...)
			nameParts = append(nameParts, channelNames...)
			roomName = truncate(strings.Join(nameParts, ", "), 100)
		}
	}

	// Capacity check
	if 1+len(users) > h.maxRoomSize {
		return nil, fmt.Errorf("room would exceed maximum capacity (%d)", h.maxRoomSize)
	}

	roomID := uuid.New().String()

	// Build and publish CreateRoomMessage
	msg := model.CreateRoomMessage{
		RoomID:         roomID,
		Name:           roomName,
		Type:           roomType,
		CreatorID:      requester.ID,
		CreatorAccount: requester.Account,
		CreatorName:    requester.Name,
		SiteID:         h.siteID,
		Users:          users,
		Orgs:           orgIDs,
		Channels:       rawChannels,
		RawIndividuals: rawIndividuals,
		HasOrgs:        hasOrgs,
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal create room message: %w", err)
	}

	publishSubj := subject.RoomCreate(requesterAccount, h.siteID)
	if err := h.publishToStream(ctx, publishSubj, msgData); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]any{"id": roomID, "name": roomName, "type": roomType})
}
```

Update `natsCreateRoom` to pass the subject:

```go
func (h *Handler) natsCreateRoom(m otelnats.Msg) {
	resp, err := h.handleCreateRoom(m.Context(), m.Msg.Subject, m.Msg.Data)
	if err != nil {
		natsutil.ReplyError(m.Msg, err.Error())
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to message", "error", err)
	}
}
```

- [ ] **Step 4: Run to verify compilation**

Run: `make lint`
Expected: PASS (may need `make fmt` first)

- [ ] **Step 5: Commit**

```bash
git add room-service/handler.go room-service/main.go
git commit -m "feat: rewrite room-service handleCreateRoom with channel/org expansion, type derivation, DM check"
```

---

### Task 5: Room-service handler tests

**Files:**
- Modify: `room-service/handler_test.go`

- [ ] **Step 1: Replace existing create-room test and add table-driven tests**

Replace `TestHandler_CreateRoom` and add comprehensive tests in `room-service/handler_test.go`:

```go
func TestHandler_CreateRoom(t *testing.T) {
	tests := []struct {
		name        string
		subj        string
		req         model.CreateRoomRequest
		setupStore  func(*MockRoomStore)
		wantErr     string
		checkResult func(t *testing.T, resp []byte, published []publishedMsg)
	}{
		{
			name: "group with name and users",
			subj: "chat.user.alice.request.rooms.create.site-a",
			req:  model.CreateRoomRequest{Name: "general", Users: []string{"bob", "carol"}},
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u1", Name: "Alice", Account: "alice", SiteID: "site-a"}, nil)
				s.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Name: "Bob", Account: "bob", SiteID: "site-a"}, nil)
				s.EXPECT().GetUser(gomock.Any(), "carol").Return(&model.User{ID: "u3", Name: "Carol", Account: "carol", SiteID: "site-a"}, nil)
			},
			checkResult: func(t *testing.T, resp []byte, published []publishedMsg) {
				var result map[string]any
				json.Unmarshal(resp, &result)
				if result["name"] != "general" {
					t.Errorf("name = %v, want general", result["name"])
				}
				if result["type"] != "group" {
					t.Errorf("type = %v, want group", result["type"])
				}
				if len(published) != 1 {
					t.Fatalf("expected 1 publish, got %d", len(published))
				}
				var msg model.CreateRoomMessage
				json.Unmarshal(published[0].data, &msg)
				if msg.Type != model.RoomTypeGroup {
					t.Errorf("msg.Type = %v, want group", msg.Type)
				}
				if len(msg.Users) != 2 {
					t.Errorf("msg.Users = %v, want 2 users", msg.Users)
				}
				if msg.HasOrgs {
					t.Error("HasOrgs should be false")
				}
			},
		},
		{
			name: "DM with single user",
			subj: "chat.user.alice.request.rooms.create.site-a",
			req:  model.CreateRoomRequest{Users: []string{"bob"}},
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u1", Name: "Alice", Account: "alice", SiteID: "site-a"}, nil)
				s.EXPECT().FindDMBetween(gomock.Any(), "alice", "bob").Return(nil, nil)
				s.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Name: "Bob", Account: "bob", SiteID: "site-a"}, nil)
			},
			checkResult: func(t *testing.T, resp []byte, published []publishedMsg) {
				var result map[string]any
				json.Unmarshal(resp, &result)
				if result["type"] != "dm" {
					t.Errorf("type = %v, want dm", result["type"])
				}
				if result["name"] != "Bob" {
					t.Errorf("name = %v, want Bob", result["name"])
				}
			},
		},
		{
			name: "DM duplicate returns error",
			subj: "chat.user.alice.request.rooms.create.site-a",
			req:  model.CreateRoomRequest{Users: []string{"bob"}},
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u1", Name: "Alice", Account: "alice", SiteID: "site-a"}, nil)
				s.EXPECT().FindDMBetween(gomock.Any(), "alice", "bob").Return(&model.Room{ID: "existing-dm"}, nil)
			},
			wantErr: "DM already exists",
		},
		{
			name: "name forces group even with single user",
			subj: "chat.user.alice.request.rooms.create.site-a",
			req:  model.CreateRoomRequest{Name: "project-chat", Users: []string{"bob"}},
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u1", Name: "Alice", Account: "alice", SiteID: "site-a"}, nil)
				s.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Name: "Bob", Account: "bob", SiteID: "site-a"}, nil)
			},
			checkResult: func(t *testing.T, resp []byte, published []publishedMsg) {
				var result map[string]any
				json.Unmarshal(resp, &result)
				if result["type"] != "group" {
					t.Errorf("type = %v, want group", result["type"])
				}
			},
		},
		{
			name: "group with orgs sets hasOrgs",
			subj: "chat.user.alice.request.rooms.create.site-a",
			req:  model.CreateRoomRequest{Name: "team", Users: []string{"bob"}, Orgs: []string{"eng-team"}},
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u1", Name: "Alice", Account: "alice", SiteID: "site-a"}, nil)
				s.EXPECT().GetOrgAccounts(gomock.Any(), "eng-team").Return([]string{"carol", "dave"}, nil)
				s.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Name: "Bob", Account: "bob", SiteID: "site-a"}, nil)
				s.EXPECT().GetUser(gomock.Any(), "carol").Return(&model.User{ID: "u3", Name: "Carol", Account: "carol", SiteID: "site-a"}, nil)
				s.EXPECT().GetUser(gomock.Any(), "dave").Return(&model.User{ID: "u4", Name: "Dave", Account: "dave", SiteID: "site-a"}, nil)
			},
			checkResult: func(t *testing.T, resp []byte, published []publishedMsg) {
				var msg model.CreateRoomMessage
				json.Unmarshal(published[0].data, &msg)
				if !msg.HasOrgs {
					t.Error("HasOrgs should be true")
				}
				if len(msg.Users) != 3 {
					t.Errorf("msg.Users = %v, want [bob, carol, dave]", msg.Users)
				}
			},
		},
		{
			name: "requester deduped from member list",
			subj: "chat.user.alice.request.rooms.create.site-a",
			req:  model.CreateRoomRequest{Name: "team", Users: []string{"alice", "bob"}},
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u1", Name: "Alice", Account: "alice", SiteID: "site-a"}, nil)
				s.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Name: "Bob", Account: "bob", SiteID: "site-a"}, nil)
			},
			checkResult: func(t *testing.T, resp []byte, published []publishedMsg) {
				var msg model.CreateRoomMessage
				json.Unmarshal(published[0].data, &msg)
				if len(msg.Users) != 1 || msg.Users[0] != "bob" {
					t.Errorf("msg.Users = %v, want [bob] (alice should be removed as requester)", msg.Users)
				}
			},
		},
		{
			name: "capacity exceeded",
			subj: "chat.user.alice.request.rooms.create.site-a",
			req:  model.CreateRoomRequest{Name: "big-room", Users: []string{"bob", "carol"}},
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u1", Name: "Alice", Account: "alice", SiteID: "site-a"}, nil)
				s.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Name: "Bob", Account: "bob", SiteID: "site-a"}, nil)
				s.EXPECT().GetUser(gomock.Any(), "carol").Return(&model.User{ID: "u3", Name: "Carol", Account: "carol", SiteID: "site-a"}, nil)
			},
			wantErr: "maximum capacity",
		},
		{
			name: "auto-generated name truncated to 100 chars",
			subj: "chat.user.alice.request.rooms.create.site-a",
			req:  model.CreateRoomRequest{Users: []string{"bob", "carol", "dave"}},
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u1", Name: "Alice", Account: "alice", SiteID: "site-a"}, nil)
				longName := strings.Repeat("X", 50)
				s.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Name: longName, Account: "bob", SiteID: "site-a"}, nil)
				s.EXPECT().GetUser(gomock.Any(), "carol").Return(&model.User{ID: "u3", Name: longName, Account: "carol", SiteID: "site-a"}, nil)
				s.EXPECT().GetUser(gomock.Any(), "dave").Return(&model.User{ID: "u4", Name: longName, Account: "dave", SiteID: "site-a"}, nil)
			},
			checkResult: func(t *testing.T, resp []byte, published []publishedMsg) {
				var result map[string]any
				json.Unmarshal(resp, &result)
				name := result["name"].(string)
				if len(name) > 100 {
					t.Errorf("name length = %d, want <= 100", len(name))
				}
			},
		},
		{
			name:    "invalid JSON",
			subj:    "chat.user.alice.request.rooms.create.site-a",
			wantErr: "invalid request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)

			if tt.setupStore != nil {
				tt.setupStore(store)
			}

			var published []publishedMsg
			maxSize := 1000
			if tt.name == "capacity exceeded" {
				maxSize = 2 // owner + 1 member max
			}
			h := &Handler{
				store:   store,
				siteID:  "site-a",
				maxRoomSize: maxSize,
				publishToStream: func(_ context.Context, subj string, data []byte) error {
					published = append(published, publishedMsg{subj: subj, data: data})
					return nil
				},
			}

			var data []byte
			if tt.name == "invalid JSON" {
				data = []byte("not json")
			} else {
				data, _ = json.Marshal(tt.req)
			}

			resp, err := h.handleCreateRoom(context.Background(), tt.subj, data)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.checkResult != nil {
				tt.checkResult(t, resp, published)
			}
		})
	}
}
```

Add necessary type and import at top of test file:

```go
import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

type publishedMsg struct {
	subj string
	data []byte
}
```

- [ ] **Step 2: Run tests**

Run: `make test SERVICE=room-service`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add room-service/handler_test.go
git commit -m "test: add table-driven tests for room-service handleCreateRoom"
```

---

### Task 6: Room-worker store expansion

**Files:**
- Modify: `room-worker/store.go`
- Modify: `room-worker/store_mongo.go`

- [ ] **Step 1: Expand store interface**

Replace `room-worker/store.go`:

```go
package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . SubscriptionStore

type SubscriptionStore interface {
	CreateRoom(ctx context.Context, room *model.Room) error
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	BulkCreateSubscriptions(ctx context.Context, subs []*model.Subscription) error
	ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
	IncrementUserCount(ctx context.Context, roomID string) error
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	GetUser(ctx context.Context, account string) (*model.User, error)
	CreateRoomMember(ctx context.Context, member *model.RoomMember) error
}
```

- [ ] **Step 2: Implement new store methods**

Add to `room-worker/store_mongo.go`:

```go
func (s *MongoStore) CreateRoom(ctx context.Context, room *model.Room) error {
	_, err := s.rooms.InsertOne(ctx, room)
	return err
}

func (s *MongoStore) BulkCreateSubscriptions(ctx context.Context, subs []*model.Subscription) error {
	if len(subs) == 0 {
		return nil
	}
	docs := make([]any, len(subs))
	for i, sub := range subs {
		docs[i] = sub
	}
	_, err := s.subscriptions.InsertMany(ctx, docs)
	return err
}

func (s *MongoStore) GetUser(ctx context.Context, account string) (*model.User, error) {
	var user model.User
	if err := s.users.FindOne(ctx, bson.M{"account": account}).Decode(&user); err != nil {
		return nil, fmt.Errorf("user %q not found: %w", account, err)
	}
	return &user, nil
}

func (s *MongoStore) CreateRoomMember(ctx context.Context, member *model.RoomMember) error {
	_, err := s.roomMembers.InsertOne(ctx, member)
	return err
}
```

Update MongoStore struct and constructor:

```go
type MongoStore struct {
	subscriptions *mongo.Collection
	rooms         *mongo.Collection
	users         *mongo.Collection
	roomMembers   *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		subscriptions: db.Collection("subscriptions"),
		rooms:         db.Collection("rooms"),
		users:         db.Collection("users"),
		roomMembers:   db.Collection("room_members"),
	}
}
```

- [ ] **Step 3: Regenerate mocks**

Run: `make generate SERVICE=room-worker`

- [ ] **Step 4: Run tests**

Run: `make test SERVICE=room-worker`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add room-worker/store.go room-worker/store_mongo.go room-worker/mock_store_test.go
git commit -m "feat: expand room-worker store with CreateRoom, BulkCreateSubscriptions, GetUser, CreateRoomMember"
```

---

### Task 7: Room-worker processCreateRoom handler

**Files:**
- Modify: `room-worker/handler.go`

- [ ] **Step 1: Update HandleJetStreamMsg with routing**

Replace `HandleJetStreamMsg` to route by subject pattern:

```go
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	subj := msg.Subject()
	var err error

	if strings.Contains(subj, "rooms.create") {
		err = h.processCreateRoom(ctx, msg.Data())
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

Add the `"strings"` import.

- [ ] **Step 2: Add processCreateRoom**

Add to `room-worker/handler.go`:

```go
func (h *Handler) processCreateRoom(ctx context.Context, data []byte) error {
	var msg model.CreateRoomMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("unmarshal create room message: %w", err)
	}

	now := time.Now().UTC()

	// Sync: create room document
	room := model.Room{
		ID:        msg.RoomID,
		Name:      msg.Name,
		Type:      msg.Type,
		CreatedBy: msg.CreatorID,
		SiteID:    msg.SiteID,
		UserCount: 1 + len(msg.Users),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.store.CreateRoom(ctx, &room); err != nil {
		return fmt.Errorf("create room: %w", err)
	}

	isDM := msg.Type == model.RoomTypeDM

	// Sync: create owner subscription
	ownerSubName := msg.Name
	if isDM && len(msg.Users) == 1 {
		// For DM, look up target user's name for the owner's subscription
		targetUser, err := h.store.GetUser(ctx, msg.Users[0])
		if err == nil {
			ownerSubName = targetUser.Name
		}
	}
	ownerSub := model.Subscription{
		ID:     uuid.New().String(),
		User:   model.SubscriptionUser{ID: msg.CreatorID, Account: msg.CreatorAccount},
		RoomID: msg.RoomID,
		SiteID: msg.SiteID,
		Role:   model.RoleOwner,
		Name:   ownerSubName,
		Type:   msg.Type,
		JoinedAt: now,
	}
	if err := h.store.CreateSubscription(ctx, &ownerSub); err != nil {
		return fmt.Errorf("create owner subscription: %w", err)
	}

	// Sync: bulk-create member subscriptions
	var memberSubs []*model.Subscription
	for _, account := range msg.Users {
		memberSubName := msg.Name
		if isDM {
			memberSubName = msg.CreatorName
		}
		user, err := h.store.GetUser(ctx, account)
		userID := account
		siteID := msg.SiteID
		if err == nil {
			userID = user.ID
			siteID = user.SiteID
		}
		sub := &model.Subscription{
			ID:       uuid.New().String(),
			User:     model.SubscriptionUser{ID: userID, Account: account},
			RoomID:   msg.RoomID,
			SiteID:   siteID,
			Role:     model.RoleMember,
			Name:     memberSubName,
			Type:     msg.Type,
			JoinedAt: now,
		}
		memberSubs = append(memberSubs, sub)
	}
	if err := h.store.BulkCreateSubscriptions(ctx, memberSubs); err != nil {
		return fmt.Errorf("bulk create subscriptions: %w", err)
	}

	// Async work after ack is handled by the caller (HandleJetStreamMsg acks, then we continue)
	// We launch a goroutine for async work
	asyncMsg := msg
	asyncOwnerSub := ownerSub
	asyncMemberSubs := make([]*model.Subscription, len(memberSubs))
	copy(asyncMemberSubs, memberSubs)

	go func() {
		bgCtx := context.Background()

		// Write room_members docs if orgs involved
		if asyncMsg.HasOrgs {
			for _, account := range asyncMsg.Users {
				m := &model.RoomMember{
					ID: uuid.New().String(), RoomID: asyncMsg.RoomID, Ts: now,
					Member: model.RoomMemberEntry{ID: account, Type: model.RoomMemberTypeIndividual, Username: account},
				}
				if err := h.store.CreateRoomMember(bgCtx, m); err != nil {
					slog.Error("create room member failed", "error", err, "account", account)
				}
			}
			// Add requester as individual room member
			m := &model.RoomMember{
				ID: uuid.New().String(), RoomID: asyncMsg.RoomID, Ts: now,
				Member: model.RoomMemberEntry{ID: asyncMsg.CreatorAccount, Type: model.RoomMemberTypeIndividual, Username: asyncMsg.CreatorAccount},
			}
			if err := h.store.CreateRoomMember(bgCtx, m); err != nil {
				slog.Error("create requester room member failed", "error", err)
			}
			for _, orgID := range asyncMsg.Orgs {
				m := &model.RoomMember{
					ID: uuid.New().String(), RoomID: asyncMsg.RoomID, Ts: now,
					Member: model.RoomMemberEntry{ID: orgID, Type: model.RoomMemberTypeOrg},
				}
				if err := h.store.CreateRoomMember(bgCtx, m); err != nil {
					slog.Error("create org room member failed", "error", err, "orgID", orgID)
				}
			}
		}

		// Publish system messages to MESSAGES_CANONICAL stream
		roomCreatedMsg := model.MessageEvent{
			Message: model.Message{
				ID:          uuid.New().String(),
				RoomID:      asyncMsg.RoomID,
				UserID:      asyncMsg.CreatorID,
				UserAccount: asyncMsg.CreatorAccount,
				Content:     "A new room has been created",
				CreatedAt:   now,
			},
			SiteID:    asyncMsg.SiteID,
			Timestamp: now.UnixMilli(),
		}
		if data, err := json.Marshal(roomCreatedMsg); err == nil {
			if err := h.publish(bgCtx, subject.MsgCanonicalCreated(asyncMsg.SiteID), data); err != nil {
				slog.Error("publish room_created message failed", "error", err)
			}
		}

		membersAddedData := model.MembersAdded{
			Individuals:     asyncMsg.RawIndividuals,
			Orgs:            asyncMsg.Orgs,
			Channels:        asyncMsg.Channels,
			AddedUsersCount: len(asyncMsg.Users),
		}
		sysData, _ := json.Marshal(membersAddedData)
		membersAddedMsg := model.MessageEvent{
			Message: model.Message{
				ID:          uuid.New().String(),
				RoomID:      asyncMsg.RoomID,
				UserID:      asyncMsg.CreatorID,
				UserAccount: asyncMsg.CreatorAccount,
				Content:     fmt.Sprintf("%s added members to the channel", asyncMsg.CreatorName),
				CreatedAt:   now,
			},
			SiteID:    asyncMsg.SiteID,
			Timestamp: now.UnixMilli(),
		}
		_ = sysData // sys_msg_data will be used when Message model adds type/sys_msg_data fields
		if data, err := json.Marshal(membersAddedMsg); err == nil {
			if err := h.publish(bgCtx, subject.MsgCanonicalCreated(asyncMsg.SiteID), data); err != nil {
				slog.Error("publish members_added message failed", "error", err)
			}
		}

		// Publish SubscriptionUpdateEvent per member
		for _, sub := range asyncMemberSubs {
			evt := model.SubscriptionUpdateEvent{
				UserID:       sub.User.ID,
				Subscription: *sub,
				Action:       "added",
				Timestamp:    now.UnixMilli(),
			}
			evtData, _ := json.Marshal(evt)
			if err := h.publish(bgCtx, subject.SubscriptionUpdate(sub.User.Account), evtData); err != nil {
				slog.Error("subscription update publish failed", "error", err, "account", sub.User.Account)
			}
		}
		// Also publish for the owner
		ownerEvt := model.SubscriptionUpdateEvent{
			UserID:       asyncOwnerSub.User.ID,
			Subscription: asyncOwnerSub,
			Action:       "added",
			Timestamp:    now.UnixMilli(),
		}
		ownerEvtData, _ := json.Marshal(ownerEvt)
		if err := h.publish(bgCtx, subject.SubscriptionUpdate(asyncOwnerSub.User.Account), ownerEvtData); err != nil {
			slog.Error("owner subscription update publish failed", "error", err)
		}

		// Publish MemberChangeEvent to room
		memberEvt := model.MemberChangeEvent{
			Type:     "member-added",
			RoomID:   asyncMsg.RoomID,
			Accounts: asyncMsg.Users,
			SiteID:   h.siteID,
		}
		memberEvtData, _ := json.Marshal(memberEvt)
		if err := h.publish(bgCtx, subject.RoomMemberEvent(asyncMsg.RoomID), memberEvtData); err != nil {
			slog.Error("room member event publish failed", "error", err)
		}

		// Cross-site federation: if room is on a different site, publish to outbox
		if asyncMsg.SiteID != h.siteID {
			outbox := model.OutboxEvent{
				Type:       "member_added",
				SiteID:     h.siteID,
				DestSiteID: asyncMsg.SiteID,
				Payload:    memberEvtData,
				Timestamp:  now.UnixMilli(),
			}
			outboxData, _ := json.Marshal(outbox)
			if err := h.publish(bgCtx, subject.Outbox(h.siteID, asyncMsg.SiteID, "member_added"), outboxData); err != nil {
				slog.Error("outbox publish failed", "error", err)
			}
		}
	}()

	return nil
}
```

Add the `"fmt"` and `"strings"` imports to the import block.

- [ ] **Step 3: Run to verify compilation**

Run: `make lint`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add room-worker/handler.go
git commit -m "feat: add room-worker processCreateRoom with sync persistence and async event publishing"
```

---

### Task 8: Room-worker handler tests

**Files:**
- Modify: `room-worker/handler_test.go`

- [ ] **Step 1: Add processCreateRoom tests**

Add to `room-worker/handler_test.go`:

```go
func TestHandler_ProcessCreateRoom_Group(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, r *model.Room) error {
		if r.Name != "general" || r.Type != model.RoomTypeGroup || r.UserCount != 3 {
			t.Errorf("unexpected room: %+v", r)
		}
		return nil
	})
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Name: "Bob", Account: "bob", SiteID: "site-a"}, nil).AnyTimes()
	store.EXPECT().GetUser(gomock.Any(), "carol").Return(&model.User{ID: "u3", Name: "Carol", Account: "carol", SiteID: "site-a"}, nil).AnyTimes()
	store.EXPECT().CreateSubscription(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, s *model.Subscription) error {
		if s.Role != model.RoleOwner {
			t.Errorf("owner sub role = %v, want owner", s.Role)
		}
		if s.Name != "general" {
			t.Errorf("owner sub name = %q, want general", s.Name)
		}
		if s.Type != model.RoomTypeGroup {
			t.Errorf("owner sub type = %v, want group", s.Type)
		}
		return nil
	})
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
		if len(subs) != 2 {
			t.Errorf("expected 2 member subs, got %d", len(subs))
		}
		for _, s := range subs {
			if s.Name != "general" {
				t.Errorf("member sub name = %q, want general", s.Name)
			}
		}
		return nil
	})

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}}

	msg := model.CreateRoomMessage{
		RoomID: "r1", Name: "general", Type: model.RoomTypeGroup,
		CreatorID: "u1", CreatorAccount: "alice", CreatorName: "Alice",
		SiteID: "site-a", Users: []string{"bob", "carol"},
		RawIndividuals: []string{"bob", "carol"}, HasOrgs: false,
	}
	data, _ := json.Marshal(msg)

	if err := h.processCreateRoom(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Give async goroutine time to complete
	time.Sleep(50 * time.Millisecond)

	// Verify system messages + subscription updates published
	if len(published) < 2 {
		t.Errorf("expected at least 2 publishes (system msgs), got %d", len(published))
	}
}

func TestHandler_ProcessCreateRoom_DM(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Name: "Bob", Account: "bob", SiteID: "site-a"}, nil).AnyTimes()
	var ownerSub *model.Subscription
	store.EXPECT().CreateSubscription(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, s *model.Subscription) error {
		ownerSub = s
		return nil
	})
	var memberSubs []*model.Subscription
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
		memberSubs = subs
		return nil
	})

	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, _ string, _ []byte) error { return nil }}

	msg := model.CreateRoomMessage{
		RoomID: "r1", Name: "Bob", Type: model.RoomTypeDM,
		CreatorID: "u1", CreatorAccount: "alice", CreatorName: "Alice",
		SiteID: "site-a", Users: []string{"bob"},
		RawIndividuals: []string{"bob"}, HasOrgs: false,
	}
	data, _ := json.Marshal(msg)

	if err := h.processCreateRoom(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Owner's subscription name should be the other user's name
	if ownerSub.Name != "Bob" {
		t.Errorf("owner sub name = %q, want Bob", ownerSub.Name)
	}
	// Member's subscription name should be the creator's name
	if len(memberSubs) != 1 || memberSubs[0].Name != "Alice" {
		t.Errorf("member sub name = %q, want Alice", memberSubs[0].Name)
	}
}

func TestHandler_ProcessCreateRoom_WithOrgs_WritesRoomMembers(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().GetUser(gomock.Any(), gomock.Any()).Return(&model.User{ID: "u2", Name: "Bob", Account: "bob", SiteID: "site-a"}, nil).AnyTimes()
	store.EXPECT().CreateSubscription(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)

	var roomMembers []model.RoomMember
	store.EXPECT().CreateRoomMember(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, m *model.RoomMember) error {
		roomMembers = append(roomMembers, *m)
		return nil
	}).AnyTimes()

	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, _ string, _ []byte) error { return nil }}

	msg := model.CreateRoomMessage{
		RoomID: "r1", Name: "team", Type: model.RoomTypeGroup,
		CreatorID: "u1", CreatorAccount: "alice", CreatorName: "Alice",
		SiteID: "site-a", Users: []string{"bob"},
		Orgs: []string{"eng-team"}, RawIndividuals: []string{"bob"}, HasOrgs: true,
	}
	data, _ := json.Marshal(msg)

	if err := h.processCreateRoom(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Should have room_members: bob (individual), alice (individual, requester), eng-team (org)
	if len(roomMembers) != 3 {
		t.Fatalf("expected 3 room members, got %d", len(roomMembers))
	}

	types := map[model.RoomMemberType]int{}
	for _, m := range roomMembers {
		types[m.Member.Type]++
	}
	if types[model.RoomMemberTypeIndividual] != 2 {
		t.Errorf("expected 2 individual room members, got %d", types[model.RoomMemberTypeIndividual])
	}
	if types[model.RoomMemberTypeOrg] != 1 {
		t.Errorf("expected 1 org room member, got %d", types[model.RoomMemberTypeOrg])
	}
}

func TestHandler_ProcessCreateRoom_StoreError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(fmt.Errorf("db failure"))

	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, _ string, _ []byte) error { return nil }}

	msg := model.CreateRoomMessage{
		RoomID: "r1", Name: "general", Type: model.RoomTypeGroup,
		CreatorID: "u1", CreatorAccount: "alice", CreatorName: "Alice",
		SiteID: "site-a", Users: []string{"bob"},
	}
	data, _ := json.Marshal(msg)

	err := h.processCreateRoom(context.Background(), data)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "create room") {
		t.Errorf("error = %q, want containing 'create room'", err.Error())
	}
}
```

Add imports at top: `"fmt"`, `"strings"`, `"time"`.

- [ ] **Step 2: Run tests**

Run: `make test SERVICE=room-worker`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add room-worker/handler.go room-worker/handler_test.go
git commit -m "test: add room-worker processCreateRoom tests for group, DM, orgs, and store errors"
```

---

### Task 9: Final integration — run full test suite and lint

**Files:** None (verification only)

- [ ] **Step 1: Run full linter**

Run: `make fmt && make lint`
Expected: 0 issues

- [ ] **Step 2: Run full test suite**

Run: `make test`
Expected: All packages PASS

- [ ] **Step 3: Final commit if any fixes needed**

```bash
git add -A
git commit -m "chore: fix lint and test issues from create-room feature"
```
