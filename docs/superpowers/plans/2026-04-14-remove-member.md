# Remove Member Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the remove-member feature across room-service (validation), room-worker (processing), and inbox-worker (cross-site replication).

**Architecture:** room-service validates remove requests (authorization, org-only guard, last-owner guard) and publishes to the ROOMS JetStream stream. room-worker consumes and performs all DB writes, event fan-out, and system message publishing synchronously before ack. inbox-worker handles inbound member_removed events from remote sites.

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB (aggregation pipelines), go.uber.org/mock, testify, testcontainers-go

**Spec:** `docs/superpowers/specs/2026-04-14-remove-member-design.md`

---

## Section A: Model & Subject Changes

### Task 1: Add new model types for remove-member

**Files:**
- Create: `pkg/model/member.go`
- Modify: `pkg/model/user.go`
- Modify: `pkg/model/message.go`
- Modify: `pkg/model/event.go`
- Test: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing tests for new model types**

Add to `pkg/model/model_test.go`:

```go
func TestRemoveMemberRequestJSON(t *testing.T) {
	t.Run("with account", func(t *testing.T) {
		src := model.RemoveMemberRequest{
			RoomID:  "r1",
			Account: "alice",
		}
		roundTrip(t, &src, &model.RemoveMemberRequest{})
	})

	t.Run("with orgId", func(t *testing.T) {
		src := model.RemoveMemberRequest{
			RoomID: "r1",
			OrgID:  "eng-org",
		}
		roundTrip(t, &src, &model.RemoveMemberRequest{})
	})

	t.Run("account omitted when empty", func(t *testing.T) {
		src := model.RemoveMemberRequest{RoomID: "r1", OrgID: "eng-org"}
		data, err := json.Marshal(src)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, present := raw["account"]
		assert.False(t, present, "account should be omitted when empty")
	})

	t.Run("orgId omitted when empty", func(t *testing.T) {
		src := model.RemoveMemberRequest{RoomID: "r1", Account: "alice"}
		data, err := json.Marshal(src)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, present := raw["orgId"]
		assert.False(t, present, "orgId should be omitted when empty")
	})
}

func TestMemberChangeEventJSON(t *testing.T) {
	src := model.MemberChangeEvent{
		Type:     "member-removed",
		RoomID:   "r1",
		Accounts: []string{"alice", "bob"},
		SiteID:   "site-a",
	}
	data, err := json.Marshal(&src)
	require.NoError(t, err)
	var dst model.MemberChangeEvent
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, src, dst)
}

func TestRoomMemberJSON(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	t.Run("individual", func(t *testing.T) {
		src := model.RoomMember{
			ID:     "rm1",
			RoomID: "r1",
			Ts:     now,
			Member: model.RoomMemberEntry{
				ID:      "alice",
				Type:    model.RoomMemberIndividual,
				Account: "alice",
			},
		}
		data, err := json.Marshal(&src)
		require.NoError(t, err)
		var dst model.RoomMember
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, src, dst)
	})

	t.Run("org", func(t *testing.T) {
		src := model.RoomMember{
			ID:     "rm2",
			RoomID: "r1",
			Ts:     now,
			Member: model.RoomMemberEntry{
				ID:   "eng-org",
				Type: model.RoomMemberOrg,
			},
		}
		data, err := json.Marshal(&src)
		require.NoError(t, err)
		var dst model.RoomMember
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, src, dst)
	})

	t.Run("account omitted for org type", func(t *testing.T) {
		src := model.RoomMember{
			ID: "rm2", RoomID: "r1", Ts: now,
			Member: model.RoomMemberEntry{ID: "eng-org", Type: model.RoomMemberOrg},
		}
		data, err := json.Marshal(src)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		member := raw["member"].(map[string]any)
		_, present := member["account"]
		assert.False(t, present, "account should be omitted for org type")
	})
}

func TestSysMsgUserJSON(t *testing.T) {
	src := model.SysMsgUser{
		Account:     "alice",
		EngName:     "Alice Wang",
		ChineseName: "愛麗絲",
	}
	roundTrip(t, &src, &model.SysMsgUser{})
}

func TestMemberLeftJSON(t *testing.T) {
	src := model.MemberLeft{
		User: model.SysMsgUser{Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲"},
	}
	roundTrip(t, &src, &model.MemberLeft{})
}

func TestMemberRemovedJSON(t *testing.T) {
	t.Run("individual removal", func(t *testing.T) {
		src := model.MemberRemoved{
			User:              &model.SysMsgUser{Account: "bob", EngName: "Bob Chen", ChineseName: "鮑勃"},
			RemovedUsersCount: 1,
		}
		data, err := json.Marshal(&src)
		require.NoError(t, err)
		var dst model.MemberRemoved
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, src, dst)
	})

	t.Run("org removal", func(t *testing.T) {
		src := model.MemberRemoved{
			OrgID:             "eng-org",
			SectName:          "Engineering",
			RemovedUsersCount: 5,
		}
		data, err := json.Marshal(&src)
		require.NoError(t, err)
		var dst model.MemberRemoved
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, src, dst)
	})

	t.Run("user omitted for org removal", func(t *testing.T) {
		src := model.MemberRemoved{OrgID: "eng-org", SectName: "Engineering", RemovedUsersCount: 5}
		data, err := json.Marshal(src)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, present := raw["user"]
		assert.False(t, present, "user should be omitted for org removal")
	})
}

func TestRoomMemberTypeValues(t *testing.T) {
	assert.Equal(t, model.RoomMemberType("individual"), model.RoomMemberIndividual)
	assert.Equal(t, model.RoomMemberType("org"), model.RoomMemberOrg)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — types not defined yet

- [ ] **Step 3: Add SectID and SectName to User model**

In `pkg/model/user.go`, add two fields after SiteID:

```go
type User struct {
	ID          string `json:"id"           bson:"_id"`
	Account     string `json:"account"      bson:"account"`
	SiteID      string `json:"siteId"       bson:"siteId"`
	SectID      string `json:"sectId"       bson:"sectId"`
	SectName    string `json:"sectName"     bson:"sectName"`
	EngName     string `json:"engName"      bson:"engName"`
	ChineseName string `json:"chineseName"  bson:"chineseName"`
	EmployeeID  string `json:"employeeId"   bson:"employeeId"`
}
```

- [ ] **Step 4: Add Type and SysMsgData to Message model**

In `pkg/model/message.go`, add two fields after ThreadParentMessageCreatedAt:

```go
type Message struct {
	ID                           string        `json:"id"                                     bson:"_id"`
	RoomID                       string        `json:"roomId"                                 bson:"roomId"`
	UserID                       string        `json:"userId"                                 bson:"userId"`
	UserAccount                  string        `json:"userAccount"                            bson:"userAccount"`
	Content                      string        `json:"content"                                bson:"content"`
	Mentions                     []Participant `json:"mentions,omitempty"                     bson:"mentions,omitempty"`
	CreatedAt                    time.Time     `json:"createdAt"                              bson:"createdAt"`
	ThreadParentMessageID        string        `json:"threadParentMessageId,omitempty"        bson:"threadParentMessageId,omitempty"`
	ThreadParentMessageCreatedAt *time.Time    `json:"threadParentMessageCreatedAt,omitempty" bson:"threadParentMessageCreatedAt,omitempty"`
	Type                         string        `json:"type,omitempty"                         bson:"type,omitempty"`
	SysMsgData                   []byte        `json:"sysMsgData,omitempty"                   bson:"sysMsgData,omitempty"`
}
```

- [ ] **Step 5: Add MemberChangeEvent to event.go**

Append to `pkg/model/event.go`:

```go
type MemberChangeEvent struct {
	Type     string   `json:"type"     bson:"type"`
	RoomID   string   `json:"roomId"   bson:"roomId"`
	Accounts []string `json:"accounts" bson:"accounts"`
	SiteID   string   `json:"siteId"   bson:"siteId"`
}
```

- [ ] **Step 6: Create pkg/model/member.go with new types**

Create `pkg/model/member.go`:

```go
package model

import "time"

type RoomMemberType string

const (
	RoomMemberIndividual RoomMemberType = "individual"
	RoomMemberOrg        RoomMemberType = "org"
)

type RoomMember struct {
	ID     string          `json:"id"     bson:"_id"`
	RoomID string          `json:"rid"    bson:"rid"`
	Ts     time.Time       `json:"ts"     bson:"ts"`
	Member RoomMemberEntry `json:"member" bson:"member"`
}

type RoomMemberEntry struct {
	ID      string         `json:"id"                bson:"id"`
	Type    RoomMemberType `json:"type"              bson:"type"`
	Account string         `json:"account,omitempty" bson:"account,omitempty"`
}

type RemoveMemberRequest struct {
	RoomID  string `json:"roomId"            bson:"roomId"`
	Account string `json:"account,omitempty" bson:"account,omitempty"`
	OrgID   string `json:"orgId,omitempty"   bson:"orgId,omitempty"`
}

// SysMsgUser carries display name fields for system message rendering.
type SysMsgUser struct {
	Account     string `json:"account"`
	EngName     string `json:"engName"`
	ChineseName string `json:"chineseName"`
}

// MemberLeft is the SysMsgData payload for type "member_left".
type MemberLeft struct {
	User SysMsgUser `json:"user"`
}

// MemberRemoved is the SysMsgData payload for type "member_removed".
type MemberRemoved struct {
	User              *SysMsgUser `json:"user,omitempty"`
	OrgID             string      `json:"orgId,omitempty"`
	SectName          string      `json:"sectName,omitempty"`
	RemovedUsersCount int         `json:"removedUsersCount"`
}
```

- [ ] **Step 7: Update TestUserJSON for new fields**

In `pkg/model/model_test.go`, update the existing `TestUserJSON`:

```go
func TestUserJSON(t *testing.T) {
	u := model.User{
		ID:          "u1",
		Account:     "alice",
		SiteID:      "site-a",
		SectID:      "eng-org",
		SectName:    "Engineering",
		EngName:     "Alice Wang",
		ChineseName: "愛麗絲",
		EmployeeID:  "EMP001",
	}
	roundTrip(t, &u, &model.User{})
}
```

- [ ] **Step 8: Add TestMessageJSON_SystemMessage subtest**

Add a subtest to the existing `TestMessageJSON` in `pkg/model/model_test.go`:

```go
t.Run("with type and sysMsgData", func(t *testing.T) {
	m := model.Message{
		ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
		Content:    "",
		CreatedAt:  time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Type:       "member_left",
		SysMsgData: []byte(`{"user":{"account":"alice","engName":"Alice","chineseName":"愛麗絲"}}`),
	}
	data, err := json.Marshal(&m)
	require.NoError(t, err)
	var dst model.Message
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, m, dst)
})

t.Run("type and sysMsgData omitted when empty", func(t *testing.T) {
	m := model.Message{
		ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
		Content:   "hello",
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(&m)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasType := raw["type"]
	assert.False(t, hasType, "type should be omitted when empty")
	_, hasSysMsgData := raw["sysMsgData"]
	assert.False(t, hasSysMsgData, "sysMsgData should be omitted when empty")
})
```

- [ ] **Step 9: Run tests to verify they pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add pkg/model/member.go pkg/model/user.go pkg/model/message.go pkg/model/event.go pkg/model/model_test.go
git commit -m "feat(model): add remove-member types and update User/Message models"
```

### Task 2: Add subject builders for remove-member

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write failing tests for new subject builders**

Add to the table in `TestSubjectBuilders` in `pkg/subject/subject_test.go`:

```go
{"MemberRemove", subject.MemberRemove("alice", "r1", "site-a"),
	"chat.user.alice.request.room.r1.site-a.member.remove"},
{"MemberEvent", subject.MemberEvent("r1"),
	"chat.room.r1.event.member"},
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/subject`
Expected: FAIL — functions not defined yet

- [ ] **Step 3: Add subject builder functions**

Add to `pkg/subject/subject.go` in the specific subject builders section:

```go
func MemberRemove(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.remove", account, roomID, siteID)
}

func MemberEvent(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event.member", roomID)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/subject`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add MemberRemove and MemberEvent builders"
```

## Section B: room-service (Validation Layer)

### Task 3: Update room-service store interface and publishToStream signature

The current `publishToStream` callback takes `(ctx, data)` only. The remove-member handler needs to publish using the **original request subject** (not a hardcoded wildcard). We need to change the signature to `(ctx, subj, data)`.

**Files:**
- Modify: `room-service/handler.go`
- Modify: `room-service/store.go`
- Modify: `room-service/main.go`
- Modify: `room-service/handler_test.go`

- [ ] **Step 1: Update Handler struct and constructor**

In `room-service/handler.go`, change the `publishToStream` field and constructor:

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

- [ ] **Step 2: Update handleInvite to pass subject**

In `room-service/handler.go`, change the `publishToStream` call in `handleInvite` to pass the original subject:

```go
func (h *Handler) handleInvite(ctx context.Context, subj string, data []byte) ([]byte, error) {
	// ... existing validation code unchanged ...

	// Publish to ROOMS stream for room-worker processing
	if err := h.publishToStream(ctx, subj, timestampedData); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "ok"})
}
```

- [ ] **Step 3: Update main.go callback**

In `room-service/main.go`, update the `publishToStream` callback:

```go
handler := NewHandler(store, cfg.SiteID, cfg.MaxRoomSize, func(ctx context.Context, subj string, data []byte) error {
	_, err := js.Publish(ctx, subj, data)
	return err
})
```

- [ ] **Step 4: Update handler_test.go to match new signature**

In `room-service/handler_test.go`, update every `NewHandler` call to use the new `publishToStream` signature. For example, the invite test's publish callback changes from `func(ctx context.Context, data []byte) error` to:

```go
func(ctx context.Context, subj string, data []byte) error {
	published = data
	return nil
}
```

Apply the same change to all test functions that create a Handler.

- [ ] **Step 5: Add new store methods to interface**

In `room-service/store.go`, add the new methods needed for remove-member:

```go
//go:generate mockgen -destination=mock_store_test.go -package=main . RoomStore

type RoomStore interface {
	CreateRoom(ctx context.Context, room *model.Room) error
	GetRoom(ctx context.Context, id string) (*model.Room, error)
	ListRooms(ctx context.Context) ([]model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	GetSubscriptionWithMembership(ctx context.Context, roomID, account string) (*model.Subscription, bool, error)
	CountOwners(ctx context.Context, roomID string) (int, error)
}
```

`GetSubscriptionWithMembership` returns `(subscription, hasIndividualMembership, error)`.

- [ ] **Step 6: Regenerate mocks**

Run: `make generate SERVICE=room-service`

- [ ] **Step 7: Run existing tests to verify refactor is clean**

Run: `make test SERVICE=room-service`
Expected: PASS — all existing tests should still pass with the updated signature

- [ ] **Step 8: Commit**

```bash
git add room-service/handler.go room-service/store.go room-service/main.go room-service/handler_test.go room-service/mock_store_test.go
git commit -m "refactor(room-service): update publishToStream signature and add store methods"
```

### Task 4: Implement room-service store methods (MongoDB aggregation pipeline)

**Files:**
- Modify: `room-service/store_mongo.go`

- [ ] **Step 1: Add room_members and users collections to MongoStore**

```go
type MongoStore struct {
	rooms         *mongo.Collection
	subscriptions *mongo.Collection
	roomMembers   *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		rooms:         db.Collection("rooms"),
		subscriptions: db.Collection("subscriptions"),
		roomMembers:   db.Collection("room_members"),
	}
}
```

- [ ] **Step 2: Implement GetSubscriptionWithMembership**

This aggregation pipeline starts from `subscriptions`, does a `$lookup` into `room_members` to check for individual membership, and returns the subscription plus a flag.

```go
func (s *MongoStore) GetSubscriptionWithMembership(ctx context.Context, roomID, account string) (*model.Subscription, bool, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"roomId": roomID, "u.account": account}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "room_members",
			"let":  bson.M{"acct": "$u.account"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$rid", roomID}},
					bson.M{"$eq": bson.A{"$member.type", "individual"}},
					bson.M{"$eq": bson.A{"$member.account", "$$acct"}},
				}}}},
				bson.M{"$limit": 1},
			},
			"as": "individualMembership",
		}}},
		{{Key: "$addFields", Value: bson.M{
			"hasIndividualMembership": bson.M{"$gt": bson.A{bson.M{"$size": "$individualMembership"}, 0}},
		}}},
		{{Key: "$project", Value: bson.M{"individualMembership": 0}}},
	}

	cursor, err := s.subscriptions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, false, fmt.Errorf("aggregate subscription with membership: %w", err)
	}
	defer cursor.Close(ctx)

	var result struct {
		model.Subscription        `bson:",inline"`
		HasIndividualMembership bool `bson:"hasIndividualMembership"`
	}
	if !cursor.Next(ctx) {
		return nil, false, fmt.Errorf("subscription not found for account %q in room %q", account, roomID)
	}
	if err := cursor.Decode(&result); err != nil {
		return nil, false, fmt.Errorf("decode subscription with membership: %w", err)
	}
	return &result.Subscription, result.HasIndividualMembership, nil
}
```

- [ ] **Step 3: Implement CountOwners**

```go
func (s *MongoStore) CountOwners(ctx context.Context, roomID string) (int, error) {
	count, err := s.subscriptions.CountDocuments(ctx, bson.M{
		"roomId": roomID,
		"role":   model.RoleOwner,
	})
	if err != nil {
		return 0, fmt.Errorf("count owners for room %q: %w", roomID, err)
	}
	return int(count), nil
}
```

- [ ] **Step 4: Commit**

```bash
git add room-service/store_mongo.go
git commit -m "feat(room-service): implement aggregation pipeline and CountOwners store methods"
```

### Task 5: Implement room-service remove-member handler

**Files:**
- Modify: `room-service/handler.go`
- Modify: `room-service/main.go`

- [ ] **Step 1: Write failing tests for handleRemoveMember**

Add to `room-service/handler_test.go`:

```go
func TestHandler_RemoveMember_SelfLeave_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	hss := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", SiteID: "site-a", Role: model.RoleMember,
		HistorySharedSince: &hss, JoinedAt: hss,
	}

	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "alice").
		Return(sub, true, nil) // hasIndividualMembership = true

	var publishedSubj string
	var publishedData []byte
	handler := NewHandler(store, "site-a", 1000, func(ctx context.Context, subj string, data []byte) error {
		publishedSubj = subj
		publishedData = data
		return nil
	})

	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "alice"})

	resp, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, reqSubj, publishedSubj)

	var status map[string]string
	require.NoError(t, json.Unmarshal(resp, &status))
	assert.Equal(t, "accepted", status["status"])

	var published model.RemoveMemberRequest
	require.NoError(t, json.Unmarshal(publishedData, &published))
	assert.Equal(t, "alice", published.Account)
}

func TestHandler_RemoveMember_SelfLeave_OrgOnly_Rejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	sub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Role: model.RoleMember,
	}

	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "alice").
		Return(sub, false, nil) // hasIndividualMembership = false

	handler := NewHandler(store, "site-a", 1000, nil)

	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "alice"})

	_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "org members cannot leave individually")
}

func TestHandler_RemoveMember_SelfLeave_LastOwner_Rejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	sub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Role: model.RoleOwner,
	}

	store.EXPECT().GetSubscriptionWithMembership(gomock.Any(), "r1", "alice").
		Return(sub, true, nil)
	store.EXPECT().CountOwners(gomock.Any(), "r1").Return(1, nil)

	handler := NewHandler(store, "site-a", 1000, nil)

	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "alice"})

	_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "last owner")
}

func TestHandler_RemoveMember_OwnerRemovesOther_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	ownerSub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Role: model.RoleOwner,
	}

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(ownerSub, nil)

	var publishedData []byte
	handler := NewHandler(store, "site-a", 1000, func(ctx context.Context, subj string, data []byte) error {
		publishedData = data
		return nil
	})

	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "bob"})

	resp, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, publishedData)
}

func TestHandler_RemoveMember_NonOwnerRemovesOther_Rejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	memberSub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Role: model.RoleMember,
	}

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(memberSub, nil)

	handler := NewHandler(store, "site-a", 1000, nil)

	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "bob"})

	_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only owners can remove members")
}

func TestHandler_RemoveMember_OwnerRemovesOrg_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	ownerSub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Role: model.RoleOwner,
	}

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(ownerSub, nil)

	var publishedData []byte
	handler := NewHandler(store, "site-a", 1000, func(ctx context.Context, subj string, data []byte) error {
		publishedData = data
		return nil
	})

	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", OrgID: "eng-org"})

	resp, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.NoError(t, err)
	require.NotNil(t, resp)

	var published model.RemoveMemberRequest
	require.NoError(t, json.Unmarshal(publishedData, &published))
	assert.Equal(t, "eng-org", published.OrgID)
}

func TestHandler_RemoveMember_BothAccountAndOrgID_Rejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	handler := NewHandler(store, "site-a", 1000, nil)

	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1", Account: "bob", OrgID: "eng-org"})

	_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestHandler_RemoveMember_NeitherAccountNorOrgID_Rejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	handler := NewHandler(store, "site-a", 1000, nil)

	reqSubj := subject.MemberRemove("alice", "r1", "site-a")
	reqBody, _ := json.Marshal(model.RemoveMemberRequest{RoomID: "r1"})

	_, err := handler.handleRemoveMember(context.Background(), reqSubj, reqBody)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=room-service`
Expected: FAIL — `handleRemoveMember` not defined

- [ ] **Step 3: Implement handleRemoveMember and NatsHandleRemoveMember**

Add to `room-service/handler.go`:

```go
// NatsHandleRemoveMember handles remove-member requests via NATS request/reply.
func (h *Handler) NatsHandleRemoveMember(m otelnats.Msg) {
	resp, err := h.handleRemoveMember(m.Context(), m.Msg.Subject, m.Msg.Data)
	if err != nil {
		natsutil.ReplyError(m.Msg, err.Error())
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to remove member", "error", err)
	}
}

func (h *Handler) handleRemoveMember(ctx context.Context, subj string, data []byte) ([]byte, error) {
	requesterAccount, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid remove member subject: %s", subj)
	}

	var req model.RemoveMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	bothSet := req.Account != "" && req.OrgID != ""
	neitherSet := req.Account == "" && req.OrgID == ""
	if bothSet || neitherSet {
		return nil, fmt.Errorf("exactly one of account or orgId must be set")
	}

	isSelfLeave := req.Account != "" && requesterAccount == req.Account

	if isSelfLeave {
		sub, hasIndividual, err := h.store.GetSubscriptionWithMembership(ctx, roomID, requesterAccount)
		if err != nil {
			return nil, fmt.Errorf("lookup subscription: %w", err)
		}
		if !hasIndividual {
			return nil, fmt.Errorf("org members cannot leave individually; an owner must remove the org")
		}
		if sub.Role == model.RoleOwner {
			count, err := h.store.CountOwners(ctx, roomID)
			if err != nil {
				return nil, fmt.Errorf("count owners: %w", err)
			}
			if count <= 1 {
				return nil, fmt.Errorf("cannot leave: you are the last owner of this room")
			}
		}
	} else {
		sub, err := h.store.GetSubscription(ctx, requesterAccount, roomID)
		if err != nil {
			return nil, fmt.Errorf("requester not found: %w", err)
		}
		if sub.Role != model.RoleOwner {
			return nil, fmt.Errorf("only owners can remove members")
		}
	}

	if err := h.publishToStream(ctx, subj, data); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "accepted"})
}
```

- [ ] **Step 4: Register NATS subscription in main.go**

The remove-member subject is already captured by the existing `MemberInviteWildcard` pattern (`chat.user.*.request.room.*.{siteID}.member.>`). However, the current code routes that wildcard only to `NatsHandleInvite`. We need to subscribe the remove handler on its own more specific subject, OR change the wildcard handler to dispatch based on subject suffix.

The simplest approach: subscribe with the same wildcard but use a dispatcher. However, NATS does not allow two queue subscribers on the same subject+queue combination to split messages. Instead, replace the single `NatsHandleInvite` subscription with a dispatcher that inspects the subject suffix.

In `room-service/main.go`, replace the invite subscription block with:

```go
memberSubj := subject.MemberInviteWildcard(cfg.SiteID)
if _, err := nc.QueueSubscribe(memberSubj, "room-service", func(m otelnats.Msg) {
	if strings.HasSuffix(m.Msg.Subject, ".member.invite") {
		handler.NatsHandleInvite(m)
	} else if strings.HasSuffix(m.Msg.Subject, ".member.remove") {
		handler.NatsHandleRemoveMember(m)
	} else {
		slog.Warn("unknown member operation", "subject", m.Msg.Subject)
	}
}); err != nil {
	slog.Error("subscribe member operations failed", "error", err)
	os.Exit(1)
}
```

Add `"strings"` to the imports in `main.go`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `make test SERVICE=room-service`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add room-service/handler.go room-service/handler_test.go room-service/main.go
git commit -m "feat(room-service): implement remove-member validation handler"
```

## Section C: room-worker (Processing Layer)

### Task 6: Update room-worker store interface

**Files:**
- Modify: `room-worker/store.go`

- [ ] **Step 1: Expand the SubscriptionStore interface**

Replace `room-worker/store.go` with:

```go
package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . SubscriptionStore

// UserWithOrgMembership is the result of the GetUserWithOrgMembership aggregation pipeline.
type UserWithOrgMembership struct {
	model.User       `bson:",inline"`
	HasOrgMembership bool `bson:"hasOrgMembership"`
}

// OrgMemberStatus is one element returned by GetOrgMembersWithIndividualStatus.
type OrgMemberStatus struct {
	Account                 string `bson:"account"`
	SiteID                  string `bson:"siteId"`
	SectName                string `bson:"sectName"`
	HasIndividualMembership bool   `bson:"hasIndividualMembership"`
}

type SubscriptionStore interface {
	// --- existing methods (invite flow) ---
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
	IncrementUserCount(ctx context.Context, roomID string) error
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)

	// --- aggregation pipelines (remove flow) ---
	GetUserWithOrgMembership(ctx context.Context, roomID, account string) (*UserWithOrgMembership, error)
	GetOrgMembersWithIndividualStatus(ctx context.Context, roomID, orgID string) ([]OrgMemberStatus, error)

	// --- write operations (remove flow) ---
	DeleteSubscription(ctx context.Context, roomID, account string) error
	DeleteSubscriptionsByAccounts(ctx context.Context, roomID string, accounts []string) error
	DeleteRoomMember(ctx context.Context, roomID string, memberType model.RoomMemberType, memberID string) error
	DeleteRoomMembersByAccount(ctx context.Context, roomID, account string) error
	DecrementUserCount(ctx context.Context, roomID string, count int) error
}
```

- [ ] **Step 2: Regenerate mocks**

Run: `make generate SERVICE=room-worker`

- [ ] **Step 3: Verify compilation**

Run: `go build ./room-worker/...`
Expected: PASS (handler still compiles — only interface grew)

- [ ] **Step 4: Commit**

```bash
git add room-worker/store.go room-worker/mock_store_test.go
git commit -m "feat(room-worker): expand store interface for remove-member"
```

### Task 7: Implement room-worker store MongoDB methods

**Files:**
- Modify: `room-worker/store_mongo.go`

- [ ] **Step 1: Add collections to MongoStore**

Update the struct and constructor in `room-worker/store_mongo.go`:

```go
type MongoStore struct {
	subscriptions *mongo.Collection
	rooms         *mongo.Collection
	roomMembers   *mongo.Collection
	users         *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		subscriptions: db.Collection("subscriptions"),
		rooms:         db.Collection("rooms"),
		roomMembers:   db.Collection("room_members"),
		users:         db.Collection("users"),
	}
}
```

- [ ] **Step 2: Implement GetUserWithOrgMembership pipeline**

```go
func (s *MongoStore) GetUserWithOrgMembership(ctx context.Context, roomID, account string) (*UserWithOrgMembership, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"account": account}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "room_members",
			"let":  bson.M{"sectId": "$sectId"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$rid", roomID}},
					bson.M{"$eq": bson.A{"$member.type", "org"}},
					bson.M{"$eq": bson.A{"$member.id", "$$sectId"}},
				}}}},
				bson.M{"$limit": 1},
			},
			"as": "orgMembership",
		}}},
		{{Key: "$addFields", Value: bson.M{
			"hasOrgMembership": bson.M{"$gt": bson.A{bson.M{"$size": "$orgMembership"}, 0}},
		}}},
		{{Key: "$project", Value: bson.M{"orgMembership": 0}}},
	}

	cursor, err := s.users.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate user with org membership: %w", err)
	}
	defer cursor.Close(ctx)

	var result UserWithOrgMembership
	if !cursor.Next(ctx) {
		return nil, fmt.Errorf("user %q not found", account)
	}
	if err := cursor.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode user with org membership: %w", err)
	}
	return &result, nil
}
```

- [ ] **Step 3: Implement GetOrgMembersWithIndividualStatus pipeline**

```go
func (s *MongoStore) GetOrgMembersWithIndividualStatus(ctx context.Context, roomID, orgID string) ([]OrgMemberStatus, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"sectId": orgID}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "room_members",
			"let":  bson.M{"acct": "$account"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$rid", roomID}},
					bson.M{"$eq": bson.A{"$member.type", "individual"}},
					bson.M{"$eq": bson.A{"$member.account", "$$acct"}},
				}}}},
				bson.M{"$limit": 1},
			},
			"as": "individualMembership",
		}}},
		{{Key: "$project", Value: bson.M{
			"account":                 1,
			"siteId":                  1,
			"sectName":                1,
			"hasIndividualMembership": bson.M{"$gt": bson.A{bson.M{"$size": "$individualMembership"}, 0}},
		}}},
	}

	cursor, err := s.users.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate org members with individual status: %w", err)
	}
	defer cursor.Close(ctx)

	var results []OrgMemberStatus
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode org members: %w", err)
	}
	return results, nil
}
```

- [ ] **Step 4: Implement write operations**

```go
func (s *MongoStore) DeleteSubscription(ctx context.Context, roomID, account string) error {
	_, err := s.subscriptions.DeleteOne(ctx, bson.M{"roomId": roomID, "u.account": account})
	if err != nil {
		return fmt.Errorf("delete subscription for %q in room %q: %w", account, roomID, err)
	}
	return nil
}

func (s *MongoStore) DeleteSubscriptionsByAccounts(ctx context.Context, roomID string, accounts []string) error {
	_, err := s.subscriptions.DeleteMany(ctx, bson.M{"roomId": roomID, "u.account": bson.M{"$in": accounts}})
	if err != nil {
		return fmt.Errorf("delete subscriptions for room %q: %w", roomID, err)
	}
	return nil
}

func (s *MongoStore) DeleteRoomMember(ctx context.Context, roomID string, memberType model.RoomMemberType, memberID string) error {
	_, err := s.roomMembers.DeleteOne(ctx, bson.M{"rid": roomID, "member.type": memberType, "member.id": memberID})
	if err != nil {
		return fmt.Errorf("delete room member: %w", err)
	}
	return nil
}

func (s *MongoStore) DeleteRoomMembersByAccount(ctx context.Context, roomID, account string) error {
	_, err := s.roomMembers.DeleteMany(ctx, bson.M{"rid": roomID, "member.account": account})
	if err != nil {
		return fmt.Errorf("delete room members for %q: %w", account, err)
	}
	return nil
}

func (s *MongoStore) DecrementUserCount(ctx context.Context, roomID string, count int) error {
	_, err := s.rooms.UpdateOne(ctx, bson.M{"_id": roomID}, bson.M{"$inc": bson.M{"userCount": -count}})
	if err != nil {
		return fmt.Errorf("decrement user count for room %q: %w", roomID, err)
	}
	return nil
}
```

- [ ] **Step 5: Commit**

```bash
git add room-worker/store_mongo.go
git commit -m "feat(room-worker): implement MongoDB aggregation pipelines and write operations"
```

### Task 8: Add subject dispatch to HandleJetStreamMsg

**Files:**
- Modify: `room-worker/handler.go`

- [ ] **Step 1: Update HandleJetStreamMsg to dispatch by subject suffix**

The ROOMS stream captures all `member.*` subjects. The handler must inspect the subject to decide which processor to call. For remove-member, failures must NAK (not ACK).

```go
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	subj := msg.Subject()

	var err error
	switch {
	case strings.HasSuffix(subj, ".member.invite"):
		err = h.processInvite(ctx, msg.Data())
	case strings.HasSuffix(subj, ".member.remove"):
		err = h.processRemoveMember(ctx, subj, msg.Data())
	default:
		slog.Warn("unknown member operation", "subject", subj)
	}

	if err != nil {
		slog.Error("process message failed", "error", err, "subject", subj)
		if nakErr := msg.Nak(); nakErr != nil {
			slog.Error("failed to nak message", "error", nakErr)
		}
		return
	}
	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "error", err)
	}
}
```

Add `"strings"` to the imports in `handler.go`.

- [ ] **Step 2: Verify existing tests still pass**

Run: `make test SERVICE=room-worker`
Expected: PASS (processInvite path unchanged, processRemoveMember not yet called in tests)

- [ ] **Step 3: Commit**

```bash
git add room-worker/handler.go
git commit -m "feat(room-worker): add subject-based dispatch in HandleJetStreamMsg"
```

### Task 9: Implement processRemoveMember handler

**Files:**
- Modify: `room-worker/handler.go`

- [ ] **Step 1: Write failing tests**

Add to `room-worker/handler_test.go`. Note: tests use `gomock` expectations on the expanded `SubscriptionStore` interface.

```go
func TestHandler_ProcessRemoveMember_SelfLeave_IndividualOnly(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetUserWithOrgMembership(gomock.Any(), "r1", "alice").
		Return(&UserWithOrgMembership{
			User:             model.User{ID: "u1", Account: "alice", SiteID: "site-a", EngName: "Alice Wang", ChineseName: "愛麗絲"},
			HasOrgMembership: false,
		}, nil)
	store.EXPECT().DeleteSubscription(gomock.Any(), "r1", "alice").Return(nil)
	store.EXPECT().DeleteRoomMember(gomock.Any(), "r1", model.RoomMemberIndividual, "alice").Return(nil)
	store.EXPECT().DecrementUserCount(gomock.Any(), "r1", 1).Return(nil)

	var published []publishedMsg
	h := NewHandler(store, "site-a", func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	})

	subj := subject.MemberRemove("alice", "r1", "site-a")
	req := model.RemoveMemberRequest{RoomID: "r1", Account: "alice"}
	data, _ := json.Marshal(req)

	err := h.processRemoveMember(context.Background(), subj, data)
	require.NoError(t, err)

	// Expect: SubscriptionUpdateEvent + MemberChangeEvent + system message = 3 publishes
	assert.GreaterOrEqual(t, len(published), 3)

	subjectSet := make(map[string]bool)
	for _, p := range published {
		subjectSet[p.subj] = true
	}
	assert.True(t, subjectSet["chat.user.alice.event.subscription.update"])
	assert.True(t, subjectSet["chat.room.r1.event.member"])
	assert.True(t, subjectSet["chat.msg.canonical.site-a.created"])
}

func TestHandler_ProcessRemoveMember_SelfLeave_DualMembership(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetUserWithOrgMembership(gomock.Any(), "r1", "alice").
		Return(&UserWithOrgMembership{
			User:             model.User{ID: "u1", Account: "alice", SiteID: "site-a"},
			HasOrgMembership: true,
		}, nil)
	// Only individual room_members doc deleted; no subscription deletion
	store.EXPECT().DeleteRoomMember(gomock.Any(), "r1", model.RoomMemberIndividual, "alice").Return(nil)

	var published []publishedMsg
	h := NewHandler(store, "site-a", func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	})

	subj := subject.MemberRemove("alice", "r1", "site-a")
	req := model.RemoveMemberRequest{RoomID: "r1", Account: "alice"}
	data, _ := json.Marshal(req)

	err := h.processRemoveMember(context.Background(), subj, data)
	require.NoError(t, err)

	// Dual membership: no events, no system message
	assert.Empty(t, published)
}

func TestHandler_ProcessRemoveMember_OwnerRemovesIndividual(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetUserWithOrgMembership(gomock.Any(), "r1", "bob").
		Return(&UserWithOrgMembership{
			User:             model.User{ID: "u2", Account: "bob", SiteID: "site-a", EngName: "Bob Chen", ChineseName: "鮑勃"},
			HasOrgMembership: false,
		}, nil)
	store.EXPECT().DeleteSubscription(gomock.Any(), "r1", "bob").Return(nil)
	store.EXPECT().DeleteRoomMembersByAccount(gomock.Any(), "r1", "bob").Return(nil)
	store.EXPECT().DecrementUserCount(gomock.Any(), "r1", 1).Return(nil)

	var published []publishedMsg
	h := NewHandler(store, "site-a", func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	})

	// alice (owner) removes bob
	subj := subject.MemberRemove("alice", "r1", "site-a")
	req := model.RemoveMemberRequest{RoomID: "r1", Account: "bob"}
	data, _ := json.Marshal(req)

	err := h.processRemoveMember(context.Background(), subj, data)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, len(published), 3)
	subjectSet := make(map[string]bool)
	for _, p := range published {
		subjectSet[p.subj] = true
	}
	assert.True(t, subjectSet["chat.user.bob.event.subscription.update"])
	assert.True(t, subjectSet["chat.room.r1.event.member"])
	assert.True(t, subjectSet["chat.msg.canonical.site-a.created"])

	// Verify system message type is "member_removed"
	for _, p := range published {
		if p.subj == "chat.msg.canonical.site-a.created" {
			var evt model.MessageEvent
			require.NoError(t, json.Unmarshal(p.data, &evt))
			assert.Equal(t, "member_removed", evt.Message.Type)
		}
	}
}

func TestHandler_ProcessRemoveMember_OwnerRemovesOrg(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetOrgMembersWithIndividualStatus(gomock.Any(), "r1", "eng-org").
		Return([]OrgMemberStatus{
			{Account: "bob", SiteID: "site-a", SectName: "Engineering", HasIndividualMembership: false},
			{Account: "carol", SiteID: "site-a", SectName: "Engineering", HasIndividualMembership: true},
			{Account: "dave", SiteID: "site-a", SectName: "Engineering", HasIndividualMembership: false},
		}, nil)
	// Only bob and dave get subscriptions deleted (carol has individual membership)
	store.EXPECT().DeleteSubscriptionsByAccounts(gomock.Any(), "r1", gomock.InAnyOrder([]string{"bob", "dave"})).Return(nil)
	store.EXPECT().DeleteRoomMember(gomock.Any(), "r1", model.RoomMemberOrg, "eng-org").Return(nil)
	store.EXPECT().DecrementUserCount(gomock.Any(), "r1", 2).Return(nil)

	var published []publishedMsg
	h := NewHandler(store, "site-a", func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	})

	subj := subject.MemberRemove("alice", "r1", "site-a")
	req := model.RemoveMemberRequest{RoomID: "r1", OrgID: "eng-org"}
	data, _ := json.Marshal(req)

	err := h.processRemoveMember(context.Background(), subj, data)
	require.NoError(t, err)

	// bob + dave get SubscriptionUpdateEvent (2) + MemberChangeEvent (1) + system msg (1) = 4
	assert.GreaterOrEqual(t, len(published), 4)

	subjectSet := make(map[string]bool)
	for _, p := range published {
		subjectSet[p.subj] = true
	}
	assert.True(t, subjectSet["chat.user.bob.event.subscription.update"])
	assert.True(t, subjectSet["chat.user.dave.event.subscription.update"])
	// carol should NOT get a subscription update
	assert.False(t, subjectSet["chat.user.carol.event.subscription.update"])
	assert.True(t, subjectSet["chat.room.r1.event.member"])
	assert.True(t, subjectSet["chat.msg.canonical.site-a.created"])
}

func TestHandler_ProcessRemoveMember_CrossSite_PublishesOutbox(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetUserWithOrgMembership(gomock.Any(), "r1", "bob").
		Return(&UserWithOrgMembership{
			User:             model.User{ID: "u2", Account: "bob", SiteID: "site-b", EngName: "Bob", ChineseName: "鮑勃"},
			HasOrgMembership: false,
		}, nil)
	store.EXPECT().DeleteSubscription(gomock.Any(), "r1", "bob").Return(nil)
	store.EXPECT().DeleteRoomMembersByAccount(gomock.Any(), "r1", "bob").Return(nil)
	store.EXPECT().DecrementUserCount(gomock.Any(), "r1", 1).Return(nil)

	var published []publishedMsg
	h := NewHandler(store, "site-a", func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	})

	subj := subject.MemberRemove("alice", "r1", "site-a")
	req := model.RemoveMemberRequest{RoomID: "r1", Account: "bob"}
	data, _ := json.Marshal(req)

	err := h.processRemoveMember(context.Background(), subj, data)
	require.NoError(t, err)

	subjectSet := make(map[string]bool)
	for _, p := range published {
		subjectSet[p.subj] = true
	}
	assert.True(t, subjectSet["outbox.site-a.to.site-b.member_removed"])
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=room-worker`
Expected: FAIL — `processRemoveMember` not defined

- [ ] **Step 3: Implement processRemoveMember**

Add to `room-worker/handler.go`:

```go
func (h *Handler) processRemoveMember(ctx context.Context, subj string, data []byte) error {
	requesterAccount, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return fmt.Errorf("invalid remove member subject: %s", subj)
	}

	var req model.RemoveMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal remove request: %w", err)
	}

	now := time.Now().UTC()

	if req.OrgID != "" {
		return h.processRemoveOrg(ctx, requesterAccount, roomID, req.OrgID, now)
	}
	return h.processRemoveIndividual(ctx, requesterAccount, roomID, req.Account, now)
}

func (h *Handler) processRemoveIndividual(ctx context.Context, requester, roomID, targetAccount string, now time.Time) error {
	isSelfLeave := requester == targetAccount

	userResult, err := h.store.GetUserWithOrgMembership(ctx, roomID, targetAccount)
	if err != nil {
		return fmt.Errorf("get user with org membership: %w", err)
	}

	// Dual membership: only delete individual room_members entry, keep subscription
	if userResult.HasOrgMembership {
		if err := h.store.DeleteRoomMember(ctx, roomID, model.RoomMemberIndividual, targetAccount); err != nil {
			return fmt.Errorf("delete individual room member: %w", err)
		}
		return nil
	}

	// Full removal
	if err := h.store.DeleteSubscription(ctx, roomID, targetAccount); err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}

	if isSelfLeave {
		if err := h.store.DeleteRoomMember(ctx, roomID, model.RoomMemberIndividual, targetAccount); err != nil {
			return fmt.Errorf("delete individual room member: %w", err)
		}
	} else {
		if err := h.store.DeleteRoomMembersByAccount(ctx, roomID, targetAccount); err != nil {
			return fmt.Errorf("delete room members by account: %w", err)
		}
	}

	if err := h.store.DecrementUserCount(ctx, roomID, 1); err != nil {
		return fmt.Errorf("decrement user count: %w", err)
	}

	// Publish SubscriptionUpdateEvent
	subEvt := model.SubscriptionUpdateEvent{
		UserID:       userResult.ID,
		Subscription: model.Subscription{RoomID: roomID, User: model.SubscriptionUser{ID: userResult.ID, Account: targetAccount}},
		Action:       "removed",
		Timestamp:    now.UnixMilli(),
	}
	subEvtData, _ := json.Marshal(subEvt)
	if err := h.publish(ctx, subject.SubscriptionUpdate(targetAccount), subEvtData); err != nil {
		return fmt.Errorf("publish subscription update: %w", err)
	}

	// Publish MemberChangeEvent
	memberEvt := model.MemberChangeEvent{
		Type:     "member-removed",
		RoomID:   roomID,
		Accounts: []string{targetAccount},
		SiteID:   h.siteID,
	}
	memberEvtData, _ := json.Marshal(memberEvt)
	if err := h.publish(ctx, subject.MemberEvent(roomID), memberEvtData); err != nil {
		return fmt.Errorf("publish member change event: %w", err)
	}

	// Publish system message
	sysUser := model.SysMsgUser{Account: targetAccount, EngName: userResult.EngName, ChineseName: userResult.ChineseName}
	var msgType string
	var sysMsgData []byte
	if isSelfLeave {
		msgType = "member_left"
		sysMsgData, _ = json.Marshal(model.MemberLeft{User: sysUser})
	} else {
		msgType = "member_removed"
		sysMsgData, _ = json.Marshal(model.MemberRemoved{User: &sysUser, RemovedUsersCount: 1})
	}

	sysMsg := model.Message{
		ID: uuid.New().String(), RoomID: roomID, Type: msgType,
		SysMsgData: sysMsgData, CreatedAt: now,
	}
	msgEvt := model.MessageEvent{Message: sysMsg, SiteID: h.siteID, Timestamp: now.UnixMilli()}
	msgEvtData, _ := json.Marshal(msgEvt)
	if err := h.publish(ctx, subject.MsgCanonicalCreated(h.siteID), msgEvtData); err != nil {
		return fmt.Errorf("publish system message: %w", err)
	}

	// Cross-site outbox
	if userResult.SiteID != h.siteID {
		outbox := model.OutboxEvent{
			Type: "member_removed", SiteID: h.siteID, DestSiteID: userResult.SiteID,
			Payload: memberEvtData, Timestamp: now.UnixMilli(),
		}
		outboxData, _ := json.Marshal(outbox)
		if err := h.publish(ctx, subject.Outbox(h.siteID, userResult.SiteID, "member_removed"), outboxData); err != nil {
			return fmt.Errorf("publish outbox: %w", err)
		}
	}

	return nil
}

func (h *Handler) processRemoveOrg(ctx context.Context, requester, roomID, orgID string, now time.Time) error {
	members, err := h.store.GetOrgMembersWithIndividualStatus(ctx, roomID, orgID)
	if err != nil {
		return fmt.Errorf("get org members: %w", err)
	}

	var toRemove []OrgMemberStatus
	var sectName string
	for _, m := range members {
		if sectName == "" {
			sectName = m.SectName
		}
		if !m.HasIndividualMembership {
			toRemove = append(toRemove, m)
		}
	}

	if len(toRemove) > 0 {
		accounts := make([]string, len(toRemove))
		for i, m := range toRemove {
			accounts[i] = m.Account
		}
		if err := h.store.DeleteSubscriptionsByAccounts(ctx, roomID, accounts); err != nil {
			return fmt.Errorf("delete subscriptions by accounts: %w", err)
		}
	}

	if err := h.store.DeleteRoomMember(ctx, roomID, model.RoomMemberOrg, orgID); err != nil {
		return fmt.Errorf("delete org room member: %w", err)
	}

	if len(toRemove) > 0 {
		if err := h.store.DecrementUserCount(ctx, roomID, len(toRemove)); err != nil {
			return fmt.Errorf("decrement user count: %w", err)
		}
	}

	// Publish SubscriptionUpdateEvent per removed account
	for _, m := range toRemove {
		subEvt := model.SubscriptionUpdateEvent{
			Subscription: model.Subscription{RoomID: roomID, User: model.SubscriptionUser{Account: m.Account}},
			Action:       "removed",
			Timestamp:    now.UnixMilli(),
		}
		subEvtData, _ := json.Marshal(subEvt)
		if err := h.publish(ctx, subject.SubscriptionUpdate(m.Account), subEvtData); err != nil {
			return fmt.Errorf("publish subscription update for %s: %w", m.Account, err)
		}
	}

	// Publish MemberChangeEvent (only actually removed accounts)
	if len(toRemove) > 0 {
		removedAccounts := make([]string, len(toRemove))
		for i, m := range toRemove {
			removedAccounts[i] = m.Account
		}
		memberEvt := model.MemberChangeEvent{
			Type: "member-removed", RoomID: roomID, Accounts: removedAccounts, SiteID: h.siteID,
		}
		memberEvtData, _ := json.Marshal(memberEvt)
		if err := h.publish(ctx, subject.MemberEvent(roomID), memberEvtData); err != nil {
			return fmt.Errorf("publish member change event: %w", err)
		}
	}

	// Publish system message
	sysMsgData, _ := json.Marshal(model.MemberRemoved{
		OrgID: orgID, SectName: sectName, RemovedUsersCount: len(toRemove),
	})
	sysMsg := model.Message{
		ID: uuid.New().String(), RoomID: roomID, Type: "member_removed",
		SysMsgData: sysMsgData, CreatedAt: now,
	}
	msgEvt := model.MessageEvent{Message: sysMsg, SiteID: h.siteID, Timestamp: now.UnixMilli()}
	msgEvtData, _ := json.Marshal(msgEvt)
	if err := h.publish(ctx, subject.MsgCanonicalCreated(h.siteID), msgEvtData); err != nil {
		return fmt.Errorf("publish system message: %w", err)
	}

	// Cross-site outbox per removed remote member
	for _, m := range toRemove {
		if m.SiteID != h.siteID {
			evt := model.MemberChangeEvent{
				Type: "member-removed", RoomID: roomID, Accounts: []string{m.Account}, SiteID: h.siteID,
			}
			evtData, _ := json.Marshal(evt)
			outbox := model.OutboxEvent{
				Type: "member_removed", SiteID: h.siteID, DestSiteID: m.SiteID,
				Payload: evtData, Timestamp: now.UnixMilli(),
			}
			outboxData, _ := json.Marshal(outbox)
			if err := h.publish(ctx, subject.Outbox(h.siteID, m.SiteID, "member_removed"), outboxData); err != nil {
				return fmt.Errorf("publish outbox for %s: %w", m.Account, err)
			}
		}
	}

	return nil
}
```

Add `"fmt"` and `"strings"` to handler.go imports if not already present. The `uuid` and `subject` imports are already used by `processInvite`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=room-worker`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add room-worker/handler.go room-worker/handler_test.go
git commit -m "feat(room-worker): implement processRemoveMember with 3 flows"
```

## Section D: inbox-worker (Cross-Site Replication)

### Task 10: Add member_removed handling to inbox-worker

**Files:**
- Modify: `inbox-worker/handler.go`
- Modify: `inbox-worker/handler_test.go`

- [ ] **Step 1: Add DeleteSubscription to InboxStore interface**

In `inbox-worker/handler.go`, expand the `InboxStore` interface:

```go
type InboxStore interface {
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	UpsertRoom(ctx context.Context, room *model.Room) error
	DeleteSubscription(ctx context.Context, roomID, account string) error
}
```

- [ ] **Step 2: Write failing tests**

Add to `inbox-worker/handler_test.go`. First add a `DeleteSubscription` method to the existing `stubInboxStore`:

```go
func (s *stubInboxStore) DeleteSubscription(_ context.Context, roomID, account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.subscriptions[:0]
	for _, sub := range s.subscriptions {
		if !(sub.RoomID == roomID && sub.User.Account == account) {
			filtered = append(filtered, sub)
		}
	}
	s.subscriptions = filtered
	return nil
}
```

Then add the test:

```go
func TestHandleEvent_MemberRemoved(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	// Pre-populate a subscription that will be deleted
	store.mu.Lock()
	store.subscriptions = append(store.subscriptions, model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
		RoomID: "r1", SiteID: "site-a", Role: model.RoleMember,
	})
	store.mu.Unlock()

	memberEvt := model.MemberChangeEvent{
		Type: "member-removed", RoomID: "r1", Accounts: []string{"bob"}, SiteID: "site-a",
	}
	payload, _ := json.Marshal(memberEvt)
	evt := model.OutboxEvent{
		Type: "member_removed", SiteID: "site-a", DestSiteID: "site-b",
		Payload: payload, Timestamp: time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), data)
	require.NoError(t, err)

	// Subscription should be deleted
	subs := store.getSubscriptions()
	assert.Empty(t, subs)

	// SubscriptionUpdateEvent should be published
	records := pub.getRecords()
	require.Len(t, records, 1)
	assert.Equal(t, "chat.user.bob.event.subscription.update", records[0].subject)

	var subEvt model.SubscriptionUpdateEvent
	require.NoError(t, json.Unmarshal(records[0].data, &subEvt))
	assert.Equal(t, "removed", subEvt.Action)
}

func TestHandleEvent_MemberRemoved_InvalidPayload(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	evt := model.OutboxEvent{
		Type: "member_removed", SiteID: "site-a", DestSiteID: "site-b",
		Payload: []byte(`{invalid`), Timestamp: time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), data)
	require.Error(t, err)
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `make test SERVICE=inbox-worker`
Expected: FAIL — `member_removed` case not handled

- [ ] **Step 4: Implement member_removed handling**

In `inbox-worker/handler.go`, add a case to the event type switch in `HandleEvent`:

```go
case "member_removed":
	var memberEvt model.MemberChangeEvent
	if err := json.Unmarshal(evt.Payload, &memberEvt); err != nil {
		return fmt.Errorf("unmarshal member removed payload: %w", err)
	}

	now := time.Now().UTC()
	for _, account := range memberEvt.Accounts {
		if err := h.store.DeleteSubscription(ctx, memberEvt.RoomID, account); err != nil {
			return fmt.Errorf("delete subscription for %s: %w", account, err)
		}

		subEvt := model.SubscriptionUpdateEvent{
			Subscription: model.Subscription{
				RoomID: memberEvt.RoomID,
				User:   model.SubscriptionUser{Account: account},
			},
			Action:    "removed",
			Timestamp: now.UnixMilli(),
		}
		subEvtData, _ := json.Marshal(subEvt)
		if err := h.pub.Publish(ctx, subject.SubscriptionUpdate(account), subEvtData); err != nil {
			slog.Error("subscription update publish failed", "error", err, "account", account)
		}
	}
	return nil
```

Add `subject` import: `"github.com/hmchangw/chat/pkg/subject"`.

- [ ] **Step 5: Implement DeleteSubscription in mongoInboxStore**

In `inbox-worker/main.go`, add to `mongoInboxStore`:

```go
func (s *mongoInboxStore) DeleteSubscription(ctx context.Context, roomID, account string) error {
	_, err := s.subCol.DeleteOne(ctx, bson.M{"roomId": roomID, "u.account": account})
	if err != nil {
		return fmt.Errorf("delete subscription for %q in room %q: %w", account, roomID, err)
	}
	return nil
}
```

Add `bson` import if not already present: `"go.mongodb.org/mongo-driver/v2/bson"`.

- [ ] **Step 6: Run tests to verify they pass**

Run: `make test SERVICE=inbox-worker`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add inbox-worker/handler.go inbox-worker/handler_test.go inbox-worker/main.go
git commit -m "feat(inbox-worker): handle member_removed outbox events"
```

## Section E: Integration Tests

### Task 11: room-service integration test for remove-member store methods

**Files:**
- Modify: `room-service/integration_test.go`

- [ ] **Step 1: Add integration test for GetSubscriptionWithMembership**

Add to `room-service/integration_test.go` (which already has `setupMongo` helper and `//go:build integration` tag):

```go
func TestMongoStore_GetSubscriptionWithMembership_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed subscription
	sub := &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", SiteID: "site-a", Role: model.RoleOwner,
		JoinedAt: time.Now().UTC(),
	}
	require.NoError(t, store.CreateSubscription(ctx, sub))

	t.Run("no individual membership", func(t *testing.T) {
		result, hasIndividual, err := store.GetSubscriptionWithMembership(ctx, "r1", "alice")
		require.NoError(t, err)
		assert.Equal(t, "alice", result.User.Account)
		assert.False(t, hasIndividual)
	})

	t.Run("with individual membership", func(t *testing.T) {
		// Seed individual room_members doc
		_, err := db.Collection("room_members").InsertOne(ctx, model.RoomMember{
			ID: "rm1", RoomID: "r1", Ts: time.Now().UTC(),
			Member: model.RoomMemberEntry{ID: "alice", Type: model.RoomMemberIndividual, Account: "alice"},
		})
		require.NoError(t, err)

		result, hasIndividual, err := store.GetSubscriptionWithMembership(ctx, "r1", "alice")
		require.NoError(t, err)
		assert.Equal(t, "alice", result.User.Account)
		assert.True(t, hasIndividual)
	})
}

func TestMongoStore_CountOwners_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Role: model.RoleOwner, JoinedAt: time.Now().UTC(),
	}))
	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
		RoomID: "r1", Role: model.RoleMember, JoinedAt: time.Now().UTC(),
	}))

	count, err := store.CountOwners(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}
```

- [ ] **Step 2: Run integration tests**

Run: `make test-integration SERVICE=room-service`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add room-service/integration_test.go
git commit -m "test(room-service): add integration tests for remove-member store methods"
```

### Task 12: room-worker integration tests for remove-member store methods

**Files:**
- Modify: `room-worker/integration_test.go`

- [ ] **Step 1: Add integration tests**

Add to `room-worker/integration_test.go` (which already has `setupMongo` helper and `//go:build integration` tag):

```go
func TestMongoStore_GetUserWithOrgMembership_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed user
	_, err := db.Collection("users").InsertOne(ctx, model.User{
		ID: "u1", Account: "alice", SiteID: "site-a", SectID: "eng-org", SectName: "Engineering",
		EngName: "Alice Wang", ChineseName: "愛麗絲",
	})
	require.NoError(t, err)

	t.Run("no org membership in room", func(t *testing.T) {
		result, err := store.GetUserWithOrgMembership(ctx, "r1", "alice")
		require.NoError(t, err)
		assert.Equal(t, "alice", result.Account)
		assert.False(t, result.HasOrgMembership)
	})

	t.Run("with org membership in room", func(t *testing.T) {
		_, err := db.Collection("room_members").InsertOne(ctx, model.RoomMember{
			ID: "rm1", RoomID: "r1", Ts: time.Now().UTC(),
			Member: model.RoomMemberEntry{ID: "eng-org", Type: model.RoomMemberOrg},
		})
		require.NoError(t, err)

		result, err := store.GetUserWithOrgMembership(ctx, "r1", "alice")
		require.NoError(t, err)
		assert.True(t, result.HasOrgMembership)
	})
}

func TestMongoStore_GetOrgMembersWithIndividualStatus_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed users in same org
	users := db.Collection("users")
	_, err := users.InsertOne(ctx, model.User{ID: "u1", Account: "alice", SiteID: "site-a", SectID: "eng-org", SectName: "Engineering"})
	require.NoError(t, err)
	_, err = users.InsertOne(ctx, model.User{ID: "u2", Account: "bob", SiteID: "site-a", SectID: "eng-org", SectName: "Engineering"})
	require.NoError(t, err)

	// alice has individual membership, bob does not
	_, err = db.Collection("room_members").InsertOne(ctx, model.RoomMember{
		ID: "rm1", RoomID: "r1", Ts: time.Now().UTC(),
		Member: model.RoomMemberEntry{ID: "alice", Type: model.RoomMemberIndividual, Account: "alice"},
	})
	require.NoError(t, err)

	results, err := store.GetOrgMembersWithIndividualStatus(ctx, "r1", "eng-org")
	require.NoError(t, err)
	require.Len(t, results, 2)

	statusMap := make(map[string]bool)
	for _, r := range results {
		statusMap[r.Account] = r.HasIndividualMembership
	}
	assert.True(t, statusMap["alice"])
	assert.False(t, statusMap["bob"])
}

func TestMongoStore_DeleteSubscription_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Role: model.RoleMember, JoinedAt: time.Now().UTC(),
	}))

	require.NoError(t, store.DeleteSubscription(ctx, "r1", "alice"))

	subs, err := store.ListByRoom(ctx, "r1")
	require.NoError(t, err)
	assert.Empty(t, subs)
}

func TestMongoStore_DeleteSubscriptionsByAccounts_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1", Role: model.RoleMember, JoinedAt: time.Now().UTC(),
	}))
	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
		RoomID: "r1", Role: model.RoleMember, JoinedAt: time.Now().UTC(),
	}))
	require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
		ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "carol"},
		RoomID: "r1", Role: model.RoleMember, JoinedAt: time.Now().UTC(),
	}))

	require.NoError(t, store.DeleteSubscriptionsByAccounts(ctx, "r1", []string{"alice", "bob"}))

	subs, err := store.ListByRoom(ctx, "r1")
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, "carol", subs[0].User.Account)
}

func TestMongoStore_DecrementUserCount_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	room := &model.Room{ID: "r1", Name: "general", UserCount: 10, SiteID: "site-a", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	_, err := db.Collection("rooms").InsertOne(ctx, room)
	require.NoError(t, err)

	require.NoError(t, store.DecrementUserCount(ctx, "r1", 3))

	updated, err := store.GetRoom(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 7, updated.UserCount)
}
```

- [ ] **Step 2: Run integration tests**

Run: `make test-integration SERVICE=room-worker`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add room-worker/integration_test.go
git commit -m "test(room-worker): add integration tests for remove-member store methods"
```

### Task 13: inbox-worker integration test for member_removed

**Files:**
- Modify: `inbox-worker/integration_test.go`

- [ ] **Step 1: Add integration test**

Add to `inbox-worker/integration_test.go`:

```go
func TestInboxWorker_MemberRemoved_Integration(t *testing.T) {
	db := setupMongo(t)
	store := &mongoInboxStore{
		subCol:  db.Collection("subscriptions"),
		roomCol: db.Collection("rooms"),
	}
	pub := &recordingPublisher{}
	h := NewHandler(store, pub)

	ctx := context.Background()

	// Pre-populate subscription
	_, err := store.subCol.InsertOne(ctx, model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "bob"},
		RoomID: "r1", SiteID: "site-a", Role: model.RoleMember,
		JoinedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	// Process member_removed event
	memberEvt := model.MemberChangeEvent{
		Type: "member-removed", RoomID: "r1", Accounts: []string{"bob"}, SiteID: "site-a",
	}
	payload, _ := json.Marshal(memberEvt)
	evt := model.OutboxEvent{
		Type: "member_removed", SiteID: "site-a", DestSiteID: "site-b",
		Payload: payload, Timestamp: time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(evt)

	require.NoError(t, h.HandleEvent(ctx, data))

	// Verify subscription deleted from MongoDB
	count, err := store.subCol.CountDocuments(ctx, bson.M{"u._id": "u1", "roomId": "r1"})
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}
```

- [ ] **Step 2: Run integration tests**

Run: `make test-integration SERVICE=inbox-worker`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add inbox-worker/integration_test.go
git commit -m "test(inbox-worker): add integration test for member_removed handling"
```
