# Combine Broadcast Events Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the multi-event broadcast system with a unified `RoomEvent` type — fan-out-on-read for group rooms, fan-out-on-write for DMs — with mention detection and consolidated room updates.

**Architecture:** Broadcast-worker becomes the single authority for room document updates (`lastMsgAt`, `lastMsgID`, `lastMentionAllAt`) and mention detection. Group rooms get one event published to a room subject; DM rooms get two personalized events to per-user subjects. Message-worker loses its room update responsibility.

**Tech Stack:** Go 1.24, NATS JetStream, MongoDB (`go.mongodb.org/mongo-driver/v2`), `go.uber.org/mock` (mockgen), `stretchr/testify`, `testcontainers-go`

**Spec:** `docs/superpowers/specs/2026-03-26-combine-broadcast-events-design.md`

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `pkg/model/room.go` | Modify | Add `Origin`, `LastMsgAt`, `LastMsgID`, `LastMentionAllAt` fields |
| `pkg/model/user.go` | Modify | Add `Username` field |
| `pkg/model/subscription.go` | Modify | Add `Username`, `LastSeenAt`, `HasMention` fields |
| `pkg/model/event.go` | Modify | Add `RoomEvent` type, `RoomEventType`, keep `RoomMetadataUpdateEvent` for now |
| `pkg/model/model_test.go` | Modify | Update round-trip tests for new fields, add `RoomEvent` test |
| `pkg/subject/subject.go` | Modify | Add `RoomEvent()`, `UserRoomEvent()` builders |
| `pkg/subject/subject_test.go` | Modify | Add test entries for new builders |
| `broadcast-worker/store.go` | Create | `Store` interface with read+write methods, mockgen directive |
| `broadcast-worker/store_mongo.go` | Create | `mongoStore` implementing `Store` |
| `broadcast-worker/handler.go` | Rewrite | New handler using `Store`, mention detection, split fan-out |
| `broadcast-worker/handler_test.go` | Rewrite | Full test coverage with mockgen `Store` |
| `broadcast-worker/integration_test.go` | Rewrite | Integration tests verifying MongoDB state + publish subjects |
| `broadcast-worker/main.go` | Modify | Replace `mongoRoomLookup` with `NewMongoStore`, update wiring |
| `message-worker/store.go` | Modify | Remove `UpdateRoomLastMessage` from interface |
| `message-worker/store_mongo.go` | Modify | Remove `UpdateRoomLastMessage` impl and `rooms` collection |
| `message-worker/handler.go` | Modify | Remove `UpdateRoomLastMessage` call |
| `message-worker/handler_test.go` | Modify | Remove `UpdateRoomLastMessage` mock expectation |

---

### Task 1: Update Data Models

**Files:**
- Modify: `pkg/model/room.go`
- Modify: `pkg/model/user.go`
- Modify: `pkg/model/subscription.go`
- Test: `pkg/model/model_test.go`

- [ ] **Step 1: Update `TestRoomJSON` to include the new fields (Red phase)**

Edit `pkg/model/model_test.go`, replacing the existing `TestRoomJSON` function:

```go
func TestRoomJSON(t *testing.T) {
	r := model.Room{
		ID: "r1", Name: "general", Type: model.RoomTypeGroup,
		CreatedBy: "u1", SiteID: "site-a", UserCount: 5,
		Origin:           "site-a",
		LastMsgAt:        time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		LastMsgID:        "m1",
		LastMentionAllAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		CreatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &r, &model.Room{})
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
make test SERVICE=pkg/model
```

Expected: compilation error — `model.Room` has no fields `Origin`, `LastMsgAt`, `LastMsgID`, `LastMentionAllAt`.

- [ ] **Step 3: Add the 4 new fields to `Room` in `pkg/model/room.go` (Green phase)**

Replace the `Room` struct:

```go
type Room struct {
	ID               string    `json:"id" bson:"_id"`
	Name             string    `json:"name" bson:"name"`
	Type             RoomType  `json:"type" bson:"type"`
	CreatedBy        string    `json:"createdBy" bson:"createdBy"`
	SiteID           string    `json:"siteId" bson:"siteId"`
	Origin           string    `json:"origin" bson:"origin"`
	UserCount        int       `json:"userCount" bson:"userCount"`
	LastMsgAt        time.Time `json:"lastMsgAt" bson:"lastMsgAt"`
	LastMsgID        string    `json:"lastMsgId" bson:"lastMsgId"`
	LastMentionAllAt time.Time `json:"lastMentionAllAt" bson:"lastMentionAllAt"`
	CreatedAt        time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt" bson:"updatedAt"`
}
```

- [ ] **Step 4: Run test to confirm it passes**

```bash
make test SERVICE=pkg/model
```

Expected: `TestRoomJSON` passes.

- [ ] **Step 5: Update `TestUserJSON` to include `Username` (Red phase)**

Replace the existing `TestUserJSON` function in `pkg/model/model_test.go`:

```go
func TestUserJSON(t *testing.T) {
	u := model.User{ID: "u1", Name: "alice", Username: "alice", SiteID: "site-a"}
	roundTrip(t, &u, &model.User{})
}
```

- [ ] **Step 6: Run test to confirm it fails**

```bash
make test SERVICE=pkg/model
```

Expected: compilation error — `model.User` has no field `Username`.

- [ ] **Step 7: Add `Username` field to `User` in `pkg/model/user.go`**

```go
type User struct {
	ID       string `json:"id" bson:"_id"`
	Name     string `json:"name" bson:"name"`
	Username string `json:"username" bson:"username"`
	SiteID   string `json:"siteId" bson:"siteId"`
}
```

- [ ] **Step 8: Run test to confirm it passes**

```bash
make test SERVICE=pkg/model
```

- [ ] **Step 9: Update `TestSubscriptionJSON` to include new fields (Red phase)**

Replace the existing `TestSubscriptionJSON` function in `pkg/model/model_test.go`:

```go
func TestSubscriptionJSON(t *testing.T) {
	s := model.Subscription{
		ID: "s1", UserID: "u1", Username: "alice", RoomID: "r1", SiteID: "site-a",
		Role:               model.RoleOwner,
		SharedHistorySince: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		JoinedAt:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt:         time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		HasMention:         true,
	}
	roundTrip(t, &s, &model.Subscription{})
}
```

- [ ] **Step 10: Run test to confirm it fails**

```bash
make test SERVICE=pkg/model
```

Expected: compilation error — `model.Subscription` has no fields `LastSeenAt`, `HasMention`.

- [ ] **Step 11: Add `Username`, `LastSeenAt`, `HasMention` fields to `Subscription` in `pkg/model/subscription.go` (Green phase)**

Replace the `Subscription` struct:

```go
type Subscription struct {
	ID                 string    `json:"id" bson:"_id"`
	UserID             string    `json:"userId" bson:"userId"`
	Username           string    `json:"username" bson:"username"`
	RoomID             string    `json:"roomId" bson:"roomId"`
	SiteID             string    `json:"siteId" bson:"siteId"`
	Role               Role      `json:"role" bson:"role"`
	SharedHistorySince time.Time `json:"sharedHistorySince" bson:"sharedHistorySince"`
	JoinedAt           time.Time `json:"joinedAt" bson:"joinedAt"`
	LastSeenAt         time.Time `json:"lastSeenAt" bson:"lastSeenAt"`
	HasMention         bool      `json:"hasMention" bson:"hasMention"`
}
```

- [ ] **Step 12: Run all model tests**

```bash
make test SERVICE=pkg/model
```

Expected: all tests pass.

- [ ] **Step 13: Lint and commit**

```bash
make fmt && make lint
git add pkg/model/room.go pkg/model/user.go pkg/model/subscription.go pkg/model/model_test.go
git commit -m "feat(model): add Username to User/Subscription, federation and activity fields to Room"
```

---

### Task 2: Add RoomEvent Type

**Files:**
- Modify: `pkg/model/event.go`
- Test: `pkg/model/model_test.go`

- [ ] **Step 1: Write `TestRoomEventJSON` and `TestRoomEventTypeValues` tests (Red phase)**

Add `"reflect"` to the import block in `pkg/model/model_test.go`, then append these tests:

```go
func TestRoomEventJSON(t *testing.T) {
	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)
	msg := model.Message{
		ID: "msg-1", RoomID: "room-1", UserID: "user-1",
		Content: "hello", CreatedAt: now,
	}

	t.Run("all fields populated", func(t *testing.T) {
		src := model.RoomEvent{
			Type:       model.RoomEventNewMessage,
			RoomID:     "room-1",
			Timestamp:  now,
			RoomName:   "General",
			RoomType:   model.RoomTypeGroup,
			Origin:     "site-a",
			UserCount:  5,
			LastMsgAt:  now,
			LastMsgID:  "msg-1",
			Mentions:   []string{"user-2", "user-3"},
			MentionAll: true,
			HasMention: true,
			Message:    &msg,
		}

		data, err := json.Marshal(src)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var dst model.RoomEvent
		if err := json.Unmarshal(data, &dst); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !reflect.DeepEqual(src, dst) {
			t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
		}
	})

	t.Run("nil message and empty mentions omitted", func(t *testing.T) {
		src := model.RoomEvent{
			Type:      model.RoomEventNewMessage,
			RoomID:    "room-2",
			Timestamp: now,
			RoomName:  "Lobby",
			RoomType:  model.RoomTypeGroup,
			Origin:    "site-b",
			UserCount: 3,
			LastMsgAt: now,
			LastMsgID: "msg-2",
		}

		data, err := json.Marshal(src)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		// Verify omitempty fields are absent from JSON
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal raw: %v", err)
		}
		for _, key := range []string{"mentions", "mentionAll", "hasMention", "message"} {
			if _, ok := raw[key]; ok {
				t.Errorf("expected %q to be omitted from JSON", key)
			}
		}

		var dst model.RoomEvent
		if err := json.Unmarshal(data, &dst); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !reflect.DeepEqual(src, dst) {
			t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
		}
	})
}

func TestRoomEventTypeValues(t *testing.T) {
	if model.RoomEventNewMessage != "new_message" {
		t.Errorf("RoomEventNewMessage = %q", model.RoomEventNewMessage)
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
make test SERVICE=pkg/model
```

Expected: compilation error — `model.RoomEvent`, `model.RoomEventType`, `model.RoomEventNewMessage` undefined.

- [ ] **Step 3: Add `RoomEventType`, `RoomEventNewMessage`, and `RoomEvent` to `pkg/model/event.go`**

Append to the end of `pkg/model/event.go`:

```go
type RoomEventType string

const (
	RoomEventNewMessage RoomEventType = "new_message"
)

type RoomEvent struct {
	Type      RoomEventType `json:"type"`
	RoomID    string        `json:"roomId"`
	Timestamp time.Time     `json:"timestamp"`

	RoomName  string   `json:"roomName"`
	RoomType  RoomType `json:"roomType"`
	Origin    string   `json:"origin"`
	UserCount int      `json:"userCount"`
	LastMsgAt time.Time `json:"lastMsgAt"`
	LastMsgID string   `json:"lastMsgId"`

	Mentions   []string `json:"mentions,omitempty"`
	MentionAll bool     `json:"mentionAll,omitempty"`

	HasMention bool `json:"hasMention,omitempty"`

	Message *Message `json:"message,omitempty"`
}
```

Do NOT remove `RoomMetadataUpdateEvent` yet — other services still reference it.

- [ ] **Step 4: Run tests to confirm they pass**

```bash
make test SERVICE=pkg/model
```

Expected: all tests pass including new `TestRoomEventJSON` and `TestRoomEventTypeValues`.

- [ ] **Step 5: Lint and commit**

```bash
make fmt && make lint
git add pkg/model/event.go pkg/model/model_test.go
git commit -m "feat(model): add RoomEvent type with JSON round-trip tests"
```

---

### Task 3: Add New Subject Builders

**Files:**
- Modify: `pkg/subject/subject.go`
- Test: `pkg/subject/subject_test.go`

- [ ] **Step 1: Add test entries for new subject builders (Red phase)**

Add two new rows to the `tests` table inside `TestSubjectBuilders` in `pkg/subject/subject_test.go`:

```go
{"RoomEvent", subject.RoomEvent("r1"), "chat.room.r1.event"},
{"UserRoomEvent", subject.UserRoomEvent("alice"), "chat.user.alice.event.room"},
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
make test SERVICE=pkg/subject
```

Expected: compilation error — `subject.RoomEvent` and `subject.UserRoomEvent` undefined.

- [ ] **Step 3: Add two new functions to `pkg/subject/subject.go`**

Add after the existing subject builders (e.g. after `Fanout`):

```go
func RoomEvent(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event", roomID)
}

func UserRoomEvent(username string) string {
	return fmt.Sprintf("chat.user.%s.event.room", username)
}
```

Do NOT remove the old functions (`RoomMetadataUpdate`, `RoomMsgStream`, `UserMsgStream`) — other services still reference them.

- [ ] **Step 4: Run tests to confirm they pass**

```bash
make test SERVICE=pkg/subject
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add RoomEvent and UserRoomEvent subject builders"
```

---

### Task 4: Create Broadcast-Worker Store

**Files:**
- Create: `broadcast-worker/store.go`
- Create: `broadcast-worker/store_mongo.go`

- [ ] **Step 1: Create `broadcast-worker/store.go`**

```go
package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store

// Store defines data access operations for the broadcast worker.
type Store interface {
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
	UpdateRoomOnNewMessage(ctx context.Context, roomID string, msgID string, msgAt time.Time, mentionAll bool) error
	SetSubscriptionMentions(ctx context.Context, roomID string, usernames []string) error
}
```

- [ ] **Step 2: Create `broadcast-worker/store_mongo.go`**

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

func (m *mongoStore) SetSubscriptionMentions(ctx context.Context, roomID string, usernames []string) error {
	filter := bson.M{
		"roomId":   roomID,
		"username": bson.M{"$in": usernames},
	}
	update := bson.M{"$set": bson.M{"hasMention": true}}
	_, err := m.subCol.UpdateMany(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("set subscription mentions for room %s: %w", roomID, err)
	}
	return nil
}
```

- [ ] **Step 3: Generate mocks**

```bash
make generate SERVICE=broadcast-worker
```

Expected: `broadcast-worker/mock_store_test.go` is created with `MockStore`.

Note: `go vet` may fail at this point because `handler.go` still references the old `RoomLookup` interface. That's expected — it will be fixed in Task 5.

- [ ] **Step 4: Commit**

```bash
git add broadcast-worker/store.go broadcast-worker/store_mongo.go broadcast-worker/mock_store_test.go
git commit -m "feat(broadcast-worker): add Store interface and mongoStore implementation"
```

---

### Task 5: Rewrite Broadcast-Worker Handler

**Files:**
- Rewrite: `broadcast-worker/handler.go`
- Rewrite: `broadcast-worker/handler_test.go`

- [ ] **Step 1: Write `broadcast-worker/handler_test.go` (Red phase)**

Replace the entire file:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// --- test helpers ---

type publishRecord struct {
	subject string
	data    []byte
}

type mockPublisher struct {
	records []publishRecord
}

func (m *mockPublisher) Publish(subj string, data []byte) error {
	m.records = append(m.records, publishRecord{subject: subj, data: data})
	return nil
}

func decodeRoomEvent(t *testing.T, data []byte) model.RoomEvent {
	t.Helper()
	var e model.RoomEvent
	require.NoError(t, json.Unmarshal(data, &e))
	return e
}

// --- fixtures ---

var (
	testGroupRoom = &model.Room{
		ID: "room-1", Name: "general", Type: model.RoomTypeGroup,
		SiteID: "site-a", Origin: "site-a", UserCount: 5,
	}
	testDMRoom = &model.Room{
		ID: "dm-1", Name: "", Type: model.RoomTypeDM,
		SiteID: "site-a", Origin: "site-a", UserCount: 2,
	}
	testDMSubs = []model.Subscription{
		{UserID: "alice-id", Username: "alice", RoomID: "dm-1"},
		{UserID: "bob-id", Username: "bob", RoomID: "dm-1"},
	}
)

func makeMessageEvent(roomID, content string, msgTime time.Time) []byte {
	evt := model.MessageEvent{
		RoomID: roomID,
		SiteID: "site-a",
		Message: model.Message{
			ID: "msg-1", RoomID: roomID, UserID: "user-1",
			Content: content, CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)
	return data
}

// --- Group room tests ---

func TestHandler_HandleMessage_GroupRoom(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name             string
		content          string
		wantMentionAll   bool
		wantMentions     []string
		wantSetMentions  bool
	}{
		{
			name:            "no mentions",
			content:         "hello group",
			wantMentionAll:  false,
			wantMentions:    nil,
			wantSetMentions: false,
		},
		{
			name:            "individual mentions",
			content:         "hey @alice and @bob",
			wantMentionAll:  false,
			wantMentions:    []string{"alice", "bob"},
			wantSetMentions: true,
		},
		{
			name:            "mention all case insensitive",
			content:         "attention @all",
			wantMentionAll:  true,
			wantMentions:    nil,
			wantSetMentions: false,
		},
		{
			name:            "mention all and individual",
			content:         "@All and @alice",
			wantMentionAll:  true,
			wantMentions:    []string{"alice"},
			wantSetMentions: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			pub := &mockPublisher{}

			store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
			store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, tc.wantMentionAll).Return(nil)

			if tc.wantSetMentions {
				store.EXPECT().SetSubscriptionMentions(gomock.Any(), "room-1", gomock.InAnyOrder(tc.wantMentions)).Return(nil)
			}

			h := NewHandler(store, pub)
			err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", tc.content, msgTime))
			require.NoError(t, err)

			require.Len(t, pub.records, 1)
			assert.Equal(t, subject.RoomEvent("room-1"), pub.records[0].subject)

			evt := decodeRoomEvent(t, pub.records[0].data)
			assert.Equal(t, model.RoomEventNewMessage, evt.Type)
			assert.Equal(t, "room-1", evt.RoomID)
			assert.Equal(t, "general", evt.RoomName)
			assert.Equal(t, "site-a", evt.Origin)
			assert.Equal(t, 5, evt.UserCount)
			assert.Equal(t, "msg-1", evt.LastMsgID)
			assert.Equal(t, tc.wantMentionAll, evt.MentionAll)
			assert.Nil(t, evt.Message, "group room events must not carry Message payload")

			if tc.wantMentions != nil {
				assert.ElementsMatch(t, tc.wantMentions, evt.Mentions) // usernames, not userIDs
			} else {
				assert.Empty(t, evt.Mentions)
			}
		})
	}
}

// --- DM room tests ---

func TestHandler_HandleMessage_DMRoom(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)

	tests := []struct {
		name            string
		content         string
		wantSetMentions bool
		mentionedUsers  []string
		aliceHasMention bool
		bobHasMention   bool
	}{
		{
			name:            "no mentions",
			content:         "hey bob",
			wantSetMentions: false,
			aliceHasMention: false,
			bobHasMention:   false,
		},
		{
			name:            "with mention",
			content:         "hey @bob",
			wantSetMentions: true,
			mentionedUsers:  []string{"bob"},
			aliceHasMention: false,
			bobHasMention:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			pub := &mockPublisher{}

			evt := model.MessageEvent{
				RoomID: "dm-1", SiteID: "site-a",
				Message: model.Message{
					ID: "msg-1", RoomID: "dm-1", UserID: "alice-id",
					Content: tc.content, CreatedAt: msgTime,
				},
			}
			data, _ := json.Marshal(evt)

			store.EXPECT().GetRoom(gomock.Any(), "dm-1").Return(testDMRoom, nil)
			store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "dm-1", "msg-1", msgTime, false).Return(nil)
			store.EXPECT().ListSubscriptions(gomock.Any(), "dm-1").Return(testDMSubs, nil)

			if tc.wantSetMentions {
				store.EXPECT().SetSubscriptionMentions(gomock.Any(), "dm-1", gomock.InAnyOrder(tc.mentionedUsers)).Return(nil)
			}

			h := NewHandler(store, pub)
			err := h.HandleMessage(context.Background(), data)
			require.NoError(t, err)

			require.Len(t, pub.records, 2)

			evtBySubject := map[string]model.RoomEvent{}
			for _, rec := range pub.records {
				evtBySubject[rec.subject] = decodeRoomEvent(t, rec.data)
			}

			// DM events route to username-based subjects
			aliceEvt := evtBySubject[subject.UserRoomEvent("alice")]
			assert.Equal(t, model.RoomEventNewMessage, aliceEvt.Type)
			require.NotNil(t, aliceEvt.Message, "DM events must carry Message payload")
			assert.Equal(t, "msg-1", aliceEvt.Message.ID)
			assert.Equal(t, tc.aliceHasMention, aliceEvt.HasMention)

			bobEvt := evtBySubject[subject.UserRoomEvent("bob")]
			require.NotNil(t, bobEvt.Message)
			assert.Equal(t, "msg-1", bobEvt.Message.ID)
			assert.Equal(t, tc.bobHasMention, bobEvt.HasMention)
		})
	}
}

// --- Error tests ---

func TestHandler_HandleMessage_Errors(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)

	t.Run("invalid json", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}
		h := NewHandler(store, pub)

		err := h.HandleMessage(context.Background(), []byte("not json"))
		require.Error(t, err)
		assert.Empty(t, pub.records)
	})

	t.Run("room not found", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(nil, errors.New("not found"))

		h := NewHandler(store, pub)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.Empty(t, pub.records)
	})

	t.Run("update room fails", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(errors.New("db error"))

		h := NewHandler(store, pub)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.Empty(t, pub.records)
	})
}

// --- Mention detection tests ---

func TestDetectMentionAll(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"@All uppercase", "attention @All everyone", true},
		{"@all lowercase", "hey @all", true},
		{"@HERE uppercase", "look @HERE please", true},
		{"@here lowercase", "look @here please", true},
		{"no mentions", "just a normal message", false},
		{"partial match not detected", "email@all.com", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, detectMentionAll(tc.content))
		})
	}
}

func TestExtractMentionedUsernames(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"two mentions", "hey @Alice and @Bob", []string{"alice", "bob"}},
		{"no mentions", "no mentions here", nil},
		{"dedup case insensitive", "@alice @Alice", []string{"alice"}},
		{"@all excluded", "hey @all and @alice", []string{"alice"}},
		{"@here excluded", "@here @bob", []string{"bob"}},
		{"mixed case", "hey @BOB", []string{"bob"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractMentionedUsernames(tc.content)
			if tc.want == nil {
				assert.Empty(t, got)
			} else {
				assert.ElementsMatch(t, tc.want, got)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail (Red phase)**

```bash
make test SERVICE=broadcast-worker
```

Expected: compilation errors — `NewHandler` signature doesn't match, `detectMentionAll`/`extractMentionedUserIDs` don't exist.

- [ ] **Step 3: Rewrite `broadcast-worker/handler.go` (Green phase)**

Replace the entire file:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// Publisher abstracts NATS publishing so the handler is testable.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Handler processes fanout messages and broadcasts room events.
type Handler struct {
	store Store
	pub   Publisher
}

func NewHandler(store Store, pub Publisher) *Handler {
	return &Handler{store: store, pub: pub}
}

// HandleMessage processes a single fanout message payload.
func (h *Handler) HandleMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	msg := evt.Message

	room, err := h.store.GetRoom(ctx, evt.RoomID)
	if err != nil {
		return fmt.Errorf("get room %s: %w", evt.RoomID, err)
	}

	mentionAll := detectMentionAll(msg.Content)
	mentionedUsernames := extractMentionedUsernames(msg.Content)

	if err := h.store.UpdateRoomOnNewMessage(ctx, room.ID, msg.ID, msg.CreatedAt, mentionAll); err != nil {
		return fmt.Errorf("update room on new message: %w", err)
	}

	if len(mentionedUsernames) > 0 {
		if err := h.store.SetSubscriptionMentions(ctx, room.ID, mentionedUsernames); err != nil {
			return fmt.Errorf("set subscription mentions: %w", err)
		}
	}

	switch room.Type {
	case model.RoomTypeGroup:
		return h.publishGroupEvent(room, msg, mentionAll, mentionedUsernames)
	case model.RoomTypeDM:
		return h.publishDMEvents(ctx, room, msg, mentionedUsernames)
	default:
		slog.Warn("unknown room type, skipping fan-out", "type", room.Type, "roomID", room.ID)
		return nil
	}
}

func (h *Handler) publishGroupEvent(room *model.Room, msg model.Message, mentionAll bool, mentions []string) error {
	evt := buildRoomEvent(room, msg)
	evt.MentionAll = mentionAll
	if len(mentions) > 0 {
		evt.Mentions = mentions
	}
	// Message intentionally nil — group room subject is publicly subscribable.

	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal group room event: %w", err)
	}
	return h.pub.Publish(subject.RoomEvent(room.ID), payload)
}

func (h *Handler) publishDMEvents(ctx context.Context, room *model.Room, msg model.Message, mentionedUsernames []string) error {
	subs, err := h.store.ListSubscriptions(ctx, room.ID)
	if err != nil {
		return fmt.Errorf("list subscriptions for DM room %s: %w", room.ID, err)
	}

	mentionSet := make(map[string]struct{}, len(mentionedUsernames))
	for _, name := range mentionedUsernames {
		mentionSet[name] = struct{}{}
	}

	for _, sub := range subs {
		_, hasMention := mentionSet[sub.Username]

		evt := buildRoomEvent(room, msg)
		evt.HasMention = hasMention
		evt.Message = &msg

		payload, err := json.Marshal(evt)
		if err != nil {
			return fmt.Errorf("marshal DM event for user %s: %w", sub.Username, err)
		}
		if err := h.pub.Publish(subject.UserRoomEvent(sub.Username), payload); err != nil {
			slog.Error("publish DM event failed", "error", err, "username", sub.Username)
		}
	}
	return nil
}

func buildRoomEvent(room *model.Room, msg model.Message) model.RoomEvent {
	return model.RoomEvent{
		Type:      model.RoomEventNewMessage,
		RoomID:    room.ID,
		Timestamp: msg.CreatedAt,
		RoomName:  room.Name,
		RoomType:  room.Type,
		Origin:    room.Origin,
		UserCount: room.UserCount,
		LastMsgAt: msg.CreatedAt,
		LastMsgID: msg.ID,
	}
}

// detectMentionAll checks for @all or @here as exact tokens (case-insensitive).
func detectMentionAll(content string) bool {
	for _, token := range strings.Fields(content) {
		lower := strings.ToLower(token)
		if lower == "@all" || lower == "@here" {
			return true
		}
	}
	return false
}

// extractMentionedUsernames extracts @-prefixed tokens from content as usernames.
// Returns lowercased, deduplicated usernames. Excludes "all" and "here" keywords.
func extractMentionedUsernames(content string) []string {
	seen := make(map[string]struct{})
	var usernames []string
	for _, token := range strings.Fields(content) {
		if !strings.HasPrefix(token, "@") || len(token) == 1 {
			continue
		}
		name := strings.ToLower(token[1:])
		if name == "all" || name == "here" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		usernames = append(usernames, name)
	}
	return usernames
}
```

- [ ] **Step 4: Run tests to confirm they pass (Green phase)**

```bash
make test SERVICE=broadcast-worker
```

Expected: all tests pass. If mock is stale, run `make generate SERVICE=broadcast-worker` first.

- [ ] **Step 5: Lint and commit**

```bash
make fmt && make lint
git add broadcast-worker/handler.go broadcast-worker/handler_test.go
git commit -m "feat(broadcast-worker): rewrite handler with unified RoomEvent and mention detection"
```

---

### Task 6: Rewrite Broadcast-Worker Integration Tests

**Files:**
- Rewrite: `broadcast-worker/integration_test.go`

- [ ] **Step 1: Replace `broadcast-worker/integration_test.go`**

```go
//go:build integration

package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
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
	return client.Database("broadcast_worker_test")
}

type recordingPublisher struct {
	mu       sync.Mutex
	subjects []string
}

func (p *recordingPublisher) Publish(subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subjects = append(p.subjects, subj)
	return nil
}

func (p *recordingPublisher) getSubjects() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]string, len(p.subjects))
	copy(cp, p.subjects)
	return cp
}

func TestBroadcastWorker_GroupRoom_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r1", Name: "general", Type: model.RoomTypeGroup, UserCount: 2, Origin: "site-a",
	})
	require.NoError(t, err)
	_, err = db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s1", UserID: "u1", Username: "alice", RoomID: "r1"},
		model.Subscription{ID: "s2", UserID: "u2", Username: "bob", RoomID: "r1"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)

	msgTime := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		RoomID: "r1", SiteID: "site-a",
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", Content: "hello", CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)

	require.NoError(t, handler.HandleMessage(ctx, data))

	subjects := pub.getSubjects()
	require.Len(t, subjects, 1)
	assert.Equal(t, subject.RoomEvent("r1"), subjects[0])

	// Verify room doc updated
	var room model.Room
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r1"}).Decode(&room))
	assert.Equal(t, "m1", room.LastMsgID)
	assert.WithinDuration(t, msgTime, room.LastMsgAt, time.Millisecond)
}

func TestBroadcastWorker_GroupRoom_MentionAll_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r2", Name: "announcements", Type: model.RoomTypeGroup, UserCount: 2, Origin: "site-a",
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)

	msgTime := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		RoomID: "r2", SiteID: "site-a",
		Message: model.Message{
			ID: "m2", RoomID: "r2", UserID: "u1", Content: "hello @All", CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)

	require.NoError(t, handler.HandleMessage(ctx, data))

	var room model.Room
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r2"}).Decode(&room))
	assert.WithinDuration(t, msgTime, room.LastMentionAllAt, time.Millisecond)
}

func TestBroadcastWorker_GroupRoom_IndividualMention_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r3", Name: "dev", Type: model.RoomTypeGroup, UserCount: 2, Origin: "site-a",
	})
	require.NoError(t, err)
	_, err = db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s5", UserID: "u1", Username: "alice", RoomID: "r3"},
		model.Subscription{ID: "s6", UserID: "u2", Username: "bob", RoomID: "r3"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)

	msgTime := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		RoomID: "r3", SiteID: "site-a",
		Message: model.Message{
			ID: "m3", RoomID: "r3", UserID: "u1", Content: "hey @bob", CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)

	require.NoError(t, handler.HandleMessage(ctx, data))

	// bob should have hasMention = true (matched by username)
	var subBob model.Subscription
	require.NoError(t, db.Collection("subscriptions").FindOne(ctx, bson.M{"username": "bob", "roomId": "r3"}).Decode(&subBob))
	assert.True(t, subBob.HasMention)

	// alice should have hasMention = false (unchanged)
	var subAlice model.Subscription
	require.NoError(t, db.Collection("subscriptions").FindOne(ctx, bson.M{"username": "alice", "roomId": "r3"}).Decode(&subAlice))
	assert.False(t, subAlice.HasMention)
}

func TestBroadcastWorker_DMRoom_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "dm-1", Name: "", Type: model.RoomTypeDM, UserCount: 2, Origin: "site-a",
	})
	require.NoError(t, err)
	_, err = db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s7", UserID: "u1", Username: "alice", RoomID: "dm-1"},
		model.Subscription{ID: "s8", UserID: "u2", Username: "bob", RoomID: "dm-1"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)

	msgTime := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		RoomID: "dm-1", SiteID: "site-a",
		Message: model.Message{
			ID: "m4", RoomID: "dm-1", UserID: "u1", Content: "hey", CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)

	require.NoError(t, handler.HandleMessage(ctx, data))

	subjects := pub.getSubjects()
	require.Len(t, subjects, 2)
	assert.ElementsMatch(t, []string{
		subject.UserRoomEvent("alice"),
		subject.UserRoomEvent("bob"),
	}, subjects)

	// Verify room doc updated
	var room model.Room
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "dm-1"}).Decode(&room))
	assert.Equal(t, "m4", room.LastMsgID)
	assert.WithinDuration(t, msgTime, room.LastMsgAt, time.Millisecond)
}
```

- [ ] **Step 2: Run integration tests**

```bash
make test-integration SERVICE=broadcast-worker
```

Expected: all 4 tests pass.

- [ ] **Step 3: Commit**

```bash
git add broadcast-worker/integration_test.go
git commit -m "test(broadcast-worker): rewrite integration tests for unified RoomEvent"
```

---

### Task 7: Update Broadcast-Worker main.go

**Files:**
- Modify: `broadcast-worker/main.go`

- [ ] **Step 1: Replace `broadcast-worker/main.go`**

Remove the `mongoRoomLookup` struct and its methods (now in `store_mongo.go`). Replace `roomLookup` with `store`:

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type config struct {
	NatsURL    string `env:"NATS_URL"    envDefault:"nats://localhost:4222"`
	SiteID     string `env:"SITE_ID"     envDefault:"default"`
	MongoURI   string `env:"MONGO_URI"   envDefault:"mongodb://localhost:27017"`
	MongoDB    string `env:"MONGO_DB"    envDefault:"chat"`
	MaxWorkers int    `env:"MAX_WORKERS" envDefault:"100"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}
	db := mongoClient.Database(cfg.MongoDB)
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))

	nc, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		slog.Error("jetstream init failed", "error", err)
		os.Exit(1)
	}

	fanoutCfg := stream.Fanout(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     fanoutCfg.Name,
		Subjects: fanoutCfg.Subjects,
	}); err != nil {
		slog.Error("create fanout stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, fanoutCfg.Name, jetstream.ConsumerConfig{
		Durable:   "broadcast-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		slog.Error("create consumer failed", "error", err)
		os.Exit(1)
	}

	publisher := &natsPublisher{nc: nc}
	handler := NewHandler(store, publisher)

	iter, err := cons.Messages(jetstream.PullMaxMessages(2 * cfg.MaxWorkers))
	if err != nil {
		slog.Error("messages failed", "error", err)
		os.Exit(1)
	}

	sem := make(chan struct{}, cfg.MaxWorkers)
	var wg sync.WaitGroup

	go func() {
		for {
			msg, err := iter.Next()
			if err != nil {
				return
			}
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() {
					<-sem
					wg.Done()
				}()
				if err := handler.HandleMessage(ctx, msg.Data()); err != nil {
					slog.Error("handle message failed", "error", err)
					if err := msg.Nak(); err != nil {
						slog.Error("failed to nak message", "error", err)
					}
					return
				}
				if err := msg.Ack(); err != nil {
					slog.Error("failed to ack message", "error", err)
				}
			}()
		}
	}()

	slog.Info("broadcast-worker started", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error {
			iter.Stop()
			return nil
		},
		func(ctx context.Context) error {
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("worker drain timed out: %w", ctx.Err())
			}
		},
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	)
}

// natsPublisher adapts *nats.Conn to the Publisher interface.
type natsPublisher struct {
	nc *nats.Conn
}

func (p *natsPublisher) Publish(subject string, data []byte) error {
	return p.nc.Publish(subject, data)
}
```

- [ ] **Step 2: Verify build**

```bash
make build SERVICE=broadcast-worker
```

Expected: compiles cleanly.

- [ ] **Step 3: Commit**

```bash
git add broadcast-worker/main.go
git commit -m "refactor(broadcast-worker): replace mongoRoomLookup with NewMongoStore"
```

---

### Task 8: Remove Room Update from Message-Worker

**Files:**
- Modify: `message-worker/handler.go`
- Modify: `message-worker/handler_test.go`
- Modify: `message-worker/store.go`
- Modify: `message-worker/store_mongo.go`

- [ ] **Step 1: Update test — remove `UpdateRoomLastMessage` expectation (Red phase)**

In `message-worker/handler_test.go`, remove these lines from `TestHandler_ProcessMessage_Success`:

```go
	store.EXPECT().
		UpdateRoomLastMessage(gomock.Any(), "r1", gomock.Any()).
		Return(nil)
```

The updated test function:

```go
func TestHandler_ProcessMessage_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockMessageStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "u1", "r1").
		Return(&model.Subscription{UserID: "u1", RoomID: "r1", Role: model.RoleMember}, nil)
	store.EXPECT().
		SaveMessage(gomock.Any(), gomock.Any()).
		Return(nil)

	var published []*nats.Msg
	publisher := func(msg *nats.Msg) error {
		published = append(published, msg)
		return nil
	}

	h := &Handler{store: store, siteID: "site-a", publishMsg: publisher}

	req := model.SendMessageRequest{RoomID: "r1", Content: "hello", RequestID: "req-1"}
	data, _ := json.Marshal(req)

	reply, err := h.processMessage(context.Background(), "u1", "r1", "site-a", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var msg model.Message
	if err := json.Unmarshal(reply, &msg); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if msg.Content != "hello" || msg.UserID != "u1" || msg.RoomID != "r1" {
		t.Errorf("got %+v", msg)
	}
	if msg.ID == "" {
		t.Error("message ID should be set")
	}
	if len(published) == 0 {
		t.Fatal("expected fanout publish")
	}
}
```

`TestHandler_ProcessMessage_NotSubscribed` remains unchanged.

- [ ] **Step 2: Remove `UpdateRoomLastMessage` from interface in `message-worker/store.go`**

```go
package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . MessageStore

// MessageStore defines persistence operations for the message worker.
type MessageStore interface {
	GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
	SaveMessage(ctx context.Context, msg *model.Message) error
}
```

- [ ] **Step 3: Remove `UpdateRoomLastMessage` and `rooms` field from `message-worker/store_mongo.go`**

```go
package main

import (
	"context"
	"fmt"

	"github.com/gocql/gocql"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

type MongoStore struct {
	subscriptions *mongo.Collection
	cassSession   *gocql.Session
}

func NewMongoStore(db *mongo.Database, cassSession *gocql.Session) *MongoStore {
	return &MongoStore{
		subscriptions: db.Collection("subscriptions"),
		cassSession:   cassSession,
	}
}

func (s *MongoStore) GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"userId": userID, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		return nil, fmt.Errorf("subscription not found: %w", err)
	}
	return &sub, nil
}

func (s *MongoStore) SaveMessage(ctx context.Context, msg *model.Message) error {
	return s.cassSession.Query(
		`INSERT INTO messages (room_id, created_at, id, user_id, content) VALUES (?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, msg.UserID, msg.Content,
	).WithContext(ctx).Exec()
}
```

- [ ] **Step 4: Remove `UpdateRoomLastMessage` call from `message-worker/handler.go`**

Remove these lines from `processMessage`:

```go
	if err := h.store.UpdateRoomLastMessage(ctx, roomID, now); err != nil {
		slog.Warn("update room last message failed", "error", err, "roomID", roomID)
	}
```

- [ ] **Step 5: Regenerate mocks**

```bash
make generate SERVICE=message-worker
```

- [ ] **Step 6: Run tests**

```bash
make test SERVICE=message-worker
```

Expected: all tests pass.

- [ ] **Step 7: Lint and commit**

```bash
make fmt && make lint
git add message-worker/store.go message-worker/store_mongo.go message-worker/handler.go message-worker/handler_test.go message-worker/mock_store_test.go
git commit -m "refactor(message-worker): remove UpdateRoomLastMessage, now handled by broadcast-worker"
```
