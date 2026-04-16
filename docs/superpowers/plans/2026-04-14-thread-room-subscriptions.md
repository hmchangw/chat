# Thread Room & Subscription Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a thread reply is processed by message-worker, create/update a ThreadRoom and upsert ThreadSubscriptions in MongoDB — idempotently.

**Architecture:** message-worker gains a new MongoDB `ThreadStore` alongside its existing Cassandra `Store`. On every thread reply, it attempts an idempotent ThreadRoom insert (duplicate key = already exists), upserts subscriptions for the parent author and replier, and updates last-message metadata. The parent author is resolved by reading the `messages_by_id` Cassandra table.

**Tech Stack:** Go 1.25, MongoDB (go.mongodb.org/mongo-driver/v2), Cassandra (gocql), go.uber.org/mock, testify, testcontainers-go

**Spec:** `docs/superpowers/specs/2026-04-14-thread-room-subscriptions-design.md`

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `pkg/model/threadroom.go` | Create | `ThreadRoom` struct |
| `pkg/model/threadsubscription.go` | Create | `ThreadSubscription` struct |
| `pkg/model/model_test.go` | Modify | Add round-trip tests for both new types |
| `message-worker/store.go` | Modify | Add `GetMessageSender` to `Store`, add `ThreadStore` interface, update mockgen directive |
| `message-worker/store_cassandra.go` | Modify | Add `GetMessageSender` method |
| `message-worker/store_mongo.go` | Create | `threadStoreMongo` implementing `ThreadStore` |
| `message-worker/handler.go` | Modify | Add `threadStore` field, `handleThreadRoomAndSubscriptions`, update `processMessage` |
| `message-worker/main.go` | Modify | Wire `threadStoreMongo` into handler |
| `message-worker/handler_test.go` | Modify | Add thread room/subscription test cases |
| `message-worker/integration_test.go` | Modify | Add thread store + GetMessageSender integration tests |
| `message-worker/mock_store_test.go` | Regenerated | Via `make generate SERVICE=message-worker` |

---

### Task 1: ThreadRoom and ThreadSubscription Models

**Files:**
- Create: `pkg/model/threadroom.go`
- Create: `pkg/model/threadsubscription.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing test for ThreadRoom round-trip**

Add to `pkg/model/model_test.go` (after `TestRoomJSON`, around line 39):

```go
func TestThreadRoomJSON(t *testing.T) {
	tr := model.ThreadRoom{
		ID:              "tr-1",
		ParentMessageID: "msg-parent",
		RoomID:          "r1",
		SiteID:          "site-a",
		LastMsgAt:       time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		LastMsgID:       "msg-2",
		CreatedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &tr, &model.ThreadRoom{})
}
```

- [ ] **Step 2: Write failing test for ThreadSubscription round-trip**

Add to `pkg/model/model_test.go` (after `TestThreadRoomJSON`):

```go
func TestThreadSubscriptionJSON(t *testing.T) {
	ts := model.ThreadSubscription{
		ID:              "ts-1",
		ParentMessageID: "msg-parent",
		RoomID:          "r1",
		ThreadRoomID:    "tr-1",
		UserID:          "u-1",
		UserAccount:     "alice",
		SiteID:          "site-a",
		LastSeenAt:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		CreatedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &ts, &model.ThreadSubscription{})
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — `model.ThreadRoom` and `model.ThreadSubscription` undefined

- [ ] **Step 4: Create `pkg/model/threadroom.go`**

```go
package model

import "time"

type ThreadRoom struct {
	ID              string    `json:"id"              bson:"_id"`
	ParentMessageID string    `json:"parentMessageId" bson:"parentMessageId"`
	RoomID          string    `json:"roomId"          bson:"roomId"`
	SiteID          string    `json:"siteId"          bson:"siteId"`
	LastMsgAt       time.Time `json:"lastMsgAt"       bson:"lastMsgAt"`
	LastMsgID       string    `json:"lastMsgId"       bson:"lastMsgId"`
	CreatedAt       time.Time `json:"createdAt"       bson:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"       bson:"updatedAt"`
}
```

- [ ] **Step 5: Create `pkg/model/threadsubscription.go`**

```go
package model

import "time"

type ThreadSubscription struct {
	ID              string    `json:"id"              bson:"_id"`
	ParentMessageID string    `json:"parentMessageId" bson:"parentMessageId"`
	RoomID          string    `json:"roomId"          bson:"roomId"`
	ThreadRoomID    string    `json:"threadRoomId"    bson:"threadRoomId"`
	UserID          string    `json:"userId"          bson:"userId"`
	UserAccount     string    `json:"userAccount"     bson:"userAccount"`
	SiteID          string    `json:"siteId"          bson:"siteId"`
	LastSeenAt      time.Time `json:"lastSeenAt"      bson:"lastSeenAt"`
	CreatedAt       time.Time `json:"createdAt"       bson:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"       bson:"updatedAt"`
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS — all model tests green including new round-trip tests

- [ ] **Step 7: Commit**

```bash
git add pkg/model/threadroom.go pkg/model/threadsubscription.go pkg/model/model_test.go
git commit -m "feat(model): add ThreadRoom and ThreadSubscription types"
```

---

### Task 2: Update Store Interface and Add ThreadStore Interface

**Files:**
- Modify: `message-worker/store.go`
- Regenerate: `message-worker/mock_store_test.go`

- [ ] **Step 1: Update `message-worker/store.go`**

Replace the entire file contents with:

```go
package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store,ThreadStore
//go:generate mockgen -destination=mock_userstore_test.go -package=main github.com/hmchangw/chat/pkg/userstore UserStore

// Store defines Cassandra persistence operations for the message worker.
type Store interface {
	SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error
	SaveThreadMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error
	GetMessageSender(ctx context.Context, messageID string) (*cassParticipant, error)
}

// ThreadStore defines MongoDB operations for thread room and subscription management.
type ThreadStore interface {
	CreateThreadRoom(ctx context.Context, room *model.ThreadRoom) error
	GetThreadRoomByParentMessageID(ctx context.Context, parentMessageID string) (*model.ThreadRoom, error)
	UpsertThreadSubscription(ctx context.Context, sub *model.ThreadSubscription) error
	UpdateThreadRoomLastMessage(ctx context.Context, threadRoomID string, lastMsgID string, lastMsgAt time.Time) error
}
```

- [ ] **Step 2: Regenerate mocks**

Run: `make generate SERVICE=message-worker`
Expected: `mock_store_test.go` regenerated with `MockStore` (now including `GetMessageSender`) and new `MockThreadStore`

- [ ] **Step 3: Verify compilation status**

Run: `go build ./message-worker/...`
Expected: FAIL — `CassandraStore` does not implement `Store` (missing `GetMessageSender`). This is expected; Task 3 will fix it.

- [ ] **Step 4: Commit**

```bash
git add message-worker/store.go message-worker/mock_store_test.go
git commit -m "feat(message-worker): add ThreadStore interface and GetMessageSender to Store"
```

---

### Task 3: Implement GetMessageSender on CassandraStore

**Files:**
- Modify: `message-worker/store_cassandra.go`
- Modify: `message-worker/integration_test.go`

- [ ] **Step 1: Write failing integration test for GetMessageSender**

Add to `message-worker/integration_test.go` (after `TestCassandraStore_SaveThreadMessage`, around line 227):

```go
func TestCassandraStore_GetMessageSender(t *testing.T) {
	cassSession := setupCassandra(t)
	store := NewCassandraStore(cassSession)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	sender := &cassParticipant{
		ID:          "u-1",
		EngName:     "Alice Wang",
		CompanyName: "愛麗絲",
		Account:     "alice",
	}
	msg := &model.Message{
		ID:          "m-sender-test",
		RoomID:      "r-1",
		UserID:      "u-1",
		UserAccount: "alice",
		Content:     "hello",
		CreatedAt:   now,
	}
	require.NoError(t, store.SaveMessage(ctx, msg, sender, "site-a"))

	t.Run("existing message returns sender", func(t *testing.T) {
		got, err := store.GetMessageSender(ctx, "m-sender-test")
		require.NoError(t, err)
		assert.Equal(t, "u-1", got.ID)
		assert.Equal(t, "alice", got.Account)
		assert.Equal(t, "Alice Wang", got.EngName)
		assert.Equal(t, "愛麗絲", got.CompanyName)
	})

	t.Run("non-existent message returns error", func(t *testing.T) {
		_, err := store.GetMessageSender(ctx, "does-not-exist")
		require.Error(t, err)
	})
}
```

- [ ] **Step 2: Run integration test to verify it fails**

Run: `make test-integration SERVICE=message-worker`
Expected: FAIL — `store.GetMessageSender` undefined (method not implemented yet)

- [ ] **Step 3: Implement GetMessageSender in `message-worker/store_cassandra.go`**

Add after the `SaveThreadMessage` method (after line 139):

```go
// GetMessageSender reads the sender UDT from messages_by_id for the given message ID.
// Returns an error if the message does not exist.
func (s *CassandraStore) GetMessageSender(ctx context.Context, messageID string) (*cassParticipant, error) {
	var sender cassParticipant
	if err := s.cassSession.Query(
		`SELECT sender FROM messages_by_id WHERE message_id = ? LIMIT 1`,
		messageID,
	).WithContext(ctx).Scan(&sender); err != nil {
		return nil, fmt.Errorf("get sender for message %s: %w", messageID, err)
	}
	return &sender, nil
}
```

- [ ] **Step 4: Verify compilation**

Run: `go build ./message-worker/...`
Expected: PASS — `CassandraStore` now satisfies `Store` interface

- [ ] **Step 5: Run integration test to verify it passes**

Run: `make test-integration SERVICE=message-worker`
Expected: PASS — all integration tests green including `TestCassandraStore_GetMessageSender`

- [ ] **Step 6: Commit**

```bash
git add message-worker/store_cassandra.go message-worker/integration_test.go
git commit -m "feat(message-worker): implement GetMessageSender on CassandraStore"
```

---

### Task 4: Implement threadStoreMongo

**Files:**
- Create: `message-worker/store_mongo.go`
- Modify: `message-worker/integration_test.go`

- [ ] **Step 1: Add setupMongo helper and failing integration tests**

In `message-worker/integration_test.go`, add the import `"go.mongodb.org/mongo-driver/v2/mongo"` to the import block if not already present.

Add the following helper after `setupCassandra` (around line 86):

```go
func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:7")
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	client, err := mongoutil.Connect(ctx, uri)
	require.NoError(t, err)
	t.Cleanup(func() { mongoutil.Disconnect(ctx, client) })

	return client.Database("chat_test")
}
```

Add the failing integration tests at the end of the file:

```go
func TestThreadStoreMongo_CreateThreadRoom(t *testing.T) {
	db := setupMongo(t)
	store := newThreadStoreMongo(db)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	room := &model.ThreadRoom{
		ID:              "tr-1",
		ParentMessageID: "msg-parent",
		RoomID:          "r-1",
		SiteID:          "site-a",
		LastMsgAt:       now,
		LastMsgID:       "msg-reply-1",
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	t.Run("first insert succeeds", func(t *testing.T) {
		err := store.CreateThreadRoom(ctx, room)
		require.NoError(t, err)

		got, err := store.GetThreadRoomByParentMessageID(ctx, "msg-parent")
		require.NoError(t, err)
		assert.Equal(t, "tr-1", got.ID)
		assert.Equal(t, "msg-parent", got.ParentMessageID)
		assert.Equal(t, "r-1", got.RoomID)
		assert.Equal(t, "site-a", got.SiteID)
		assert.Equal(t, "msg-reply-1", got.LastMsgID)
	})

	t.Run("duplicate insert returns errThreadRoomExists", func(t *testing.T) {
		dup := &model.ThreadRoom{
			ID:              "tr-2",
			ParentMessageID: "msg-parent",
			RoomID:          "r-1",
			SiteID:          "site-a",
			LastMsgAt:       now,
			LastMsgID:       "msg-reply-2",
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		err := store.CreateThreadRoom(ctx, dup)
		require.ErrorIs(t, err, errThreadRoomExists)
	})
}

func TestThreadStoreMongo_GetThreadRoomByParentMessageID(t *testing.T) {
	db := setupMongo(t)
	store := newThreadStoreMongo(db)
	ctx := context.Background()

	t.Run("not found returns error", func(t *testing.T) {
		_, err := store.GetThreadRoomByParentMessageID(ctx, "does-not-exist")
		require.Error(t, err)
	})
}

func TestThreadStoreMongo_UpsertThreadSubscription(t *testing.T) {
	db := setupMongo(t)
	store := newThreadStoreMongo(db)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	sub := &model.ThreadSubscription{
		ID:              "ts-1",
		ParentMessageID: "msg-parent",
		RoomID:          "r-1",
		ThreadRoomID:    "tr-1",
		UserID:          "u-1",
		UserAccount:     "alice",
		SiteID:          "site-a",
		LastSeenAt:      now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	t.Run("first upsert creates document", func(t *testing.T) {
		err := store.UpsertThreadSubscription(ctx, sub)
		require.NoError(t, err)

		var got model.ThreadSubscription
		err = db.Collection("threadSubscriptions").FindOne(ctx, bson.M{
			"threadRoomId": "tr-1",
			"userId":       "u-1",
		}).Decode(&got)
		require.NoError(t, err)
		assert.Equal(t, "ts-1", got.ID)
		assert.Equal(t, "alice", got.UserAccount)
		assert.Equal(t, now, got.CreatedAt.UTC().Truncate(time.Millisecond))
	})

	t.Run("second upsert updates fields but preserves createdAt and ID", func(t *testing.T) {
		later := now.Add(5 * time.Minute)
		sub2 := &model.ThreadSubscription{
			ID:              "ts-should-be-ignored",
			ParentMessageID: "msg-parent",
			RoomID:          "r-1",
			ThreadRoomID:    "tr-1",
			UserID:          "u-1",
			UserAccount:     "alice",
			SiteID:          "site-a",
			LastSeenAt:      later,
			CreatedAt:       later,
			UpdatedAt:       later,
		}
		err := store.UpsertThreadSubscription(ctx, sub2)
		require.NoError(t, err)

		var got model.ThreadSubscription
		err = db.Collection("threadSubscriptions").FindOne(ctx, bson.M{
			"threadRoomId": "tr-1",
			"userId":       "u-1",
		}).Decode(&got)
		require.NoError(t, err)
		assert.Equal(t, "ts-1", got.ID, "ID should not change on upsert")
		assert.Equal(t, now, got.CreatedAt.UTC().Truncate(time.Millisecond), "createdAt should not change on upsert")
		assert.Equal(t, later, got.LastSeenAt.UTC().Truncate(time.Millisecond))
		assert.Equal(t, later, got.UpdatedAt.UTC().Truncate(time.Millisecond))
	})
}

func TestThreadStoreMongo_UpdateThreadRoomLastMessage(t *testing.T) {
	db := setupMongo(t)
	store := newThreadStoreMongo(db)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	room := &model.ThreadRoom{
		ID:              "tr-update",
		ParentMessageID: "msg-parent-update",
		RoomID:          "r-1",
		SiteID:          "site-a",
		LastMsgAt:       now,
		LastMsgID:       "msg-1",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, store.CreateThreadRoom(ctx, room))

	later := now.Add(10 * time.Minute)
	err := store.UpdateThreadRoomLastMessage(ctx, "tr-update", "msg-5", later)
	require.NoError(t, err)

	got, err := store.GetThreadRoomByParentMessageID(ctx, "msg-parent-update")
	require.NoError(t, err)
	assert.Equal(t, "msg-5", got.LastMsgID)
	assert.Equal(t, later, got.LastMsgAt.UTC().Truncate(time.Millisecond))
}
```

- [ ] **Step 2: Run integration tests to verify they fail**

Run: `make test-integration SERVICE=message-worker`
Expected: FAIL — `newThreadStoreMongo` undefined

- [ ] **Step 3: Create `message-worker/store_mongo.go`**

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

var errThreadRoomExists = errors.New("thread room already exists")

type threadStoreMongo struct {
	threadRooms         *mongo.Collection
	threadSubscriptions *mongo.Collection
}

func newThreadStoreMongo(db *mongo.Database) *threadStoreMongo {
	ctx := context.Background()

	threadRooms := db.Collection("threadRooms")
	threadRooms.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "parentMessageId", Value: 1}},
		Options: options.Index().SetUnique(true),
	})

	threadSubs := db.Collection("threadSubscriptions")
	threadSubs.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "threadRoomId", Value: 1}, {Key: "userId", Value: 1}},
		Options: options.Index().SetUnique(true),
	})

	return &threadStoreMongo{
		threadRooms:         threadRooms,
		threadSubscriptions: threadSubs,
	}
}

func (s *threadStoreMongo) CreateThreadRoom(ctx context.Context, room *model.ThreadRoom) error {
	_, err := s.threadRooms.InsertOne(ctx, room)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return errThreadRoomExists
		}
		return fmt.Errorf("insert thread room: %w", err)
	}
	return nil
}

func (s *threadStoreMongo) GetThreadRoomByParentMessageID(ctx context.Context, parentMessageID string) (*model.ThreadRoom, error) {
	var room model.ThreadRoom
	if err := s.threadRooms.FindOne(ctx, bson.M{"parentMessageId": parentMessageID}).Decode(&room); err != nil {
		return nil, fmt.Errorf("find thread room by parent %s: %w", parentMessageID, err)
	}
	return &room, nil
}

func (s *threadStoreMongo) UpsertThreadSubscription(ctx context.Context, sub *model.ThreadSubscription) error {
	filter := bson.M{"threadRoomId": sub.ThreadRoomID, "userId": sub.UserID}
	update := bson.M{
		"$set": bson.M{
			"parentMessageId": sub.ParentMessageID,
			"roomId":          sub.RoomID,
			"userAccount":     sub.UserAccount,
			"siteId":          sub.SiteID,
			"lastSeenAt":      sub.LastSeenAt,
			"updatedAt":       sub.UpdatedAt,
		},
		"$setOnInsert": bson.M{
			"_id":       sub.ID,
			"createdAt": sub.CreatedAt,
		},
	}
	_, err := s.threadSubscriptions.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert thread subscription: %w", err)
	}
	return nil
}

func (s *threadStoreMongo) UpdateThreadRoomLastMessage(ctx context.Context, threadRoomID string, lastMsgID string, lastMsgAt time.Time) error {
	_, err := s.threadRooms.UpdateOne(ctx, bson.M{"_id": threadRoomID}, bson.M{
		"$set": bson.M{
			"lastMsgAt": lastMsgAt,
			"lastMsgId": lastMsgID,
			"updatedAt": lastMsgAt,
		},
	})
	if err != nil {
		return fmt.Errorf("update thread room last message: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run integration tests to verify they pass**

Run: `make test-integration SERVICE=message-worker`
Expected: PASS — all thread store integration tests green

- [ ] **Step 5: Commit**

```bash
git add message-worker/store_mongo.go message-worker/integration_test.go
git commit -m "feat(message-worker): implement threadStoreMongo with integration tests"
```

---

### Task 5: Add handleThreadRoomAndSubscriptions and Update processMessage

**Files:**
- Modify: `message-worker/handler.go`
- Modify: `message-worker/handler_test.go`

- [ ] **Step 1: Write failing unit tests for handleThreadRoomAndSubscriptions**

Add to `message-worker/handler_test.go` (after `TestParseMentions`, around line 242):

```go
func TestHandler_HandleThreadRoomAndSubscriptions(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	parentSender := &cassParticipant{
		ID:      "u-parent",
		Account: "parent-user",
	}

	msg := &model.Message{
		ID:                    "msg-reply",
		RoomID:                "r1",
		UserID:                "u-replier",
		UserAccount:           "replier",
		Content:               "thread reply",
		CreatedAt:             now,
		ThreadParentMessageID: "msg-parent",
	}

	tests := []struct {
		name       string
		msg        *model.Message
		siteID     string
		setupMocks func(store *MockStore, ts *MockThreadStore)
		wantErr    bool
	}{
		{
			name:   "first reply — different users — creates room and two subscriptions",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, room *model.ThreadRoom) error {
						assert.Equal(t, "msg-parent", room.ParentMessageID)
						assert.Equal(t, "r1", room.RoomID)
						assert.Equal(t, "site-a", room.SiteID)
						assert.Equal(t, "msg-reply", room.LastMsgID)
						return nil
					})
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(parentSender, nil)
				// First call: parent author subscription
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, sub *model.ThreadSubscription) error {
						assert.Equal(t, "u-parent", sub.UserID)
						assert.Equal(t, "parent-user", sub.UserAccount)
						return nil
					})
				// Second call: replier subscription
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, sub *model.ThreadSubscription) error {
						assert.Equal(t, "u-replier", sub.UserID)
						assert.Equal(t, "replier", sub.UserAccount)
						return nil
					})
			},
		},
		{
			name: "first reply — same user — creates room and one subscription",
			msg: &model.Message{
				ID:                    "msg-reply",
				RoomID:                "r1",
				UserID:                "u-parent",
				UserAccount:           "parent-user",
				Content:               "self reply",
				CreatedAt:             now,
				ThreadParentMessageID: "msg-parent",
			},
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(parentSender, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, sub *model.ThreadSubscription) error {
						assert.Equal(t, "u-parent", sub.UserID)
						return nil
					})
			},
		},
		{
			name:   "first reply — GetMessageSender fails — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(nil, errors.New("cassandra: read timeout"))
			},
			wantErr: true,
		},
		{
			name:   "first reply — UpsertThreadSubscription fails — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(parentSender, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					Return(errors.New("mongo: write error"))
			},
			wantErr: true,
		},
		{
			name:   "subsequent reply — upserts subscription and updates last message",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					Return(errThreadRoomExists)
				ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-parent").
					Return(&model.ThreadRoom{ID: "tr-existing"}, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, sub *model.ThreadSubscription) error {
						assert.Equal(t, "tr-existing", sub.ThreadRoomID)
						assert.Equal(t, "u-replier", sub.UserID)
						return nil
					})
				ts.EXPECT().UpdateThreadRoomLastMessage(gomock.Any(), "tr-existing", "msg-reply", now).
					Return(nil)
			},
		},
		{
			name:   "subsequent reply — GetThreadRoomByParentMessageID fails — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					Return(errThreadRoomExists)
				ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-parent").
					Return(nil, errors.New("mongo: connection refused"))
			},
			wantErr: true,
		},
		{
			name:   "subsequent reply — UpsertThreadSubscription fails — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					Return(errThreadRoomExists)
				ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-parent").
					Return(&model.ThreadRoom{ID: "tr-existing"}, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					Return(errors.New("mongo: write error"))
			},
			wantErr: true,
		},
		{
			name:   "subsequent reply — UpdateThreadRoomLastMessage fails — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					Return(errThreadRoomExists)
				ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-parent").
					Return(&model.ThreadRoom{ID: "tr-existing"}, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
				ts.EXPECT().UpdateThreadRoomLastMessage(gomock.Any(), "tr-existing", "msg-reply", now).
					Return(errors.New("mongo: write error"))
			},
			wantErr: true,
		},
		{
			name:   "CreateThreadRoom unexpected error — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					Return(errors.New("mongo: connection refused"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockStore := NewMockStore(ctrl)
			mockThreadStore := NewMockThreadStore(ctrl)
			mockUserStore := NewMockUserStore(ctrl)
			tt.setupMocks(mockStore, mockThreadStore)

			h := NewHandler(mockStore, mockUserStore, mockThreadStore)
			err := h.handleThreadRoomAndSubscriptions(context.Background(), tt.msg, tt.siteID)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=message-worker`
Expected: FAIL — `NewHandler` wrong number of arguments, `h.handleThreadRoomAndSubscriptions` undefined

- [ ] **Step 3: Update `message-worker/handler.go`**

Replace the entire file contents with:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/userstore"
)

// mentionRe matches @mention tokens in message content.
// Note: a bare @ not preceded by whitespace (e.g. "hello@bob") also matches —
// this is intentional per spec. Non-existent accounts are silently skipped by
// resolveMentions during the MongoDB lookup.
var mentionRe = regexp.MustCompile(`(^|\s|>?)@([0-9a-zA-Z_-]+(\.[0-9a-zA-Z_-]+)*(@[0-9a-zA-Z_-]+(\.[0-9a-zA-Z_-]+)*)?)`)

// parseMentions returns the unique mention targets found in content (without the @ prefix).
// Returns nil when content has no mentions.
func parseMentions(content string) []string {
	matches := mentionRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	var out []string
	for _, m := range matches {
		account := m[2]
		if _, exists := seen[account]; !exists {
			seen[account] = struct{}{}
			out = append(out, account)
		}
	}
	return out
}

type Handler struct {
	store       Store
	userStore   userstore.UserStore
	threadStore ThreadStore
}

func NewHandler(store Store, userStore userstore.UserStore, threadStore ThreadStore) *Handler {
	return &Handler{store: store, userStore: userStore, threadStore: threadStore}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGES_CANONICAL stream.
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	if err := h.processMessage(ctx, msg.Data()); err != nil {
		slog.Error("process message failed", "error", err)
		if err := msg.Nak(); err != nil {
			slog.Error("failed to nack message", "error", err)
		}
		return
	}

	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "err", err)
	}
}

// resolveMentions parses @mention tokens from content, looks up real users in
// MongoDB, and returns them as Participants. @all is always included as a
// special entry without a DB lookup. Accounts not found in MongoDB are skipped.
// Returns nil when content has no mentions.
func (h *Handler) resolveMentions(ctx context.Context, content string) ([]model.Participant, error) {
	parsed := parseMentions(content)
	if len(parsed) == 0 {
		return nil, nil
	}

	var mentionAll bool
	var userAccounts []string
	for _, account := range parsed {
		if account == "all" {
			mentionAll = true
		} else {
			userAccounts = append(userAccounts, account)
		}
	}

	var participants []model.Participant

	if len(userAccounts) > 0 {
		users, err := h.userStore.FindUsersByAccounts(ctx, userAccounts)
		if err != nil {
			return nil, fmt.Errorf("find mentioned users: %w", err)
		}
		for _, u := range users {
			participants = append(participants, model.Participant{
				UserID:      u.ID,
				Account:     u.Account,
				ChineseName: u.ChineseName,
				EngName:     u.EngName,
			})
		}
	}

	if mentionAll {
		participants = append(participants, model.Participant{
			Account: "all",
			EngName: "all",
		})
	}

	if len(participants) == 0 {
		return nil, nil
	}
	return participants, nil
}

func (h *Handler) processMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	mentions, err := h.resolveMentions(ctx, evt.Message.Content)
	if err != nil {
		return fmt.Errorf("resolve mentions: %w", err)
	}
	evt.Message.Mentions = mentions

	user, err := h.userStore.FindUserByID(ctx, evt.Message.UserID)
	if err != nil {
		return fmt.Errorf("lookup user %s: %w", evt.Message.UserID, err)
	}

	sender := cassParticipant{
		ID:          user.ID,
		EngName:     user.EngName,
		CompanyName: user.ChineseName,
		Account:     evt.Message.UserAccount,
	}

	if evt.Message.ThreadParentMessageID != "" {
		if err := h.store.SaveThreadMessage(ctx, &evt.Message, &sender, evt.SiteID); err != nil {
			return fmt.Errorf("save thread message: %w", err)
		}
		if err := h.handleThreadRoomAndSubscriptions(ctx, &evt.Message, evt.SiteID); err != nil {
			return fmt.Errorf("handle thread room and subscriptions: %w", err)
		}
	} else {
		if err := h.store.SaveMessage(ctx, &evt.Message, &sender, evt.SiteID); err != nil {
			return fmt.Errorf("save message: %w", err)
		}
	}

	return nil
}

// handleThreadRoomAndSubscriptions creates the ThreadRoom on first reply, and
// upserts ThreadSubscriptions for the parent author and the replier. On subsequent
// replies it updates the existing ThreadRoom's last message and ensures the replier
// has a subscription. All operations are idempotent.
func (h *Handler) handleThreadRoomAndSubscriptions(ctx context.Context, msg *model.Message, siteID string) error {
	now := msg.CreatedAt

	threadRoom := model.ThreadRoom{
		ID:              uuid.NewString(),
		ParentMessageID: msg.ThreadParentMessageID,
		RoomID:          msg.RoomID,
		SiteID:          siteID,
		LastMsgAt:       msg.CreatedAt,
		LastMsgID:       msg.ID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	err := h.threadStore.CreateThreadRoom(ctx, &threadRoom)

	if err == nil {
		parentSender, err := h.store.GetMessageSender(ctx, msg.ThreadParentMessageID)
		if err != nil {
			return fmt.Errorf("get parent message sender: %w", err)
		}

		if err := h.threadStore.UpsertThreadSubscription(ctx, &model.ThreadSubscription{
			ID:              uuid.NewString(),
			ParentMessageID: msg.ThreadParentMessageID,
			RoomID:          msg.RoomID,
			ThreadRoomID:    threadRoom.ID,
			UserID:          parentSender.ID,
			UserAccount:     parentSender.Account,
			SiteID:          siteID,
			LastSeenAt:      now,
			CreatedAt:       now,
			UpdatedAt:       now,
		}); err != nil {
			return fmt.Errorf("upsert parent author thread subscription: %w", err)
		}

		if msg.UserID != parentSender.ID {
			if err := h.threadStore.UpsertThreadSubscription(ctx, &model.ThreadSubscription{
				ID:              uuid.NewString(),
				ParentMessageID: msg.ThreadParentMessageID,
				RoomID:          msg.RoomID,
				ThreadRoomID:    threadRoom.ID,
				UserID:          msg.UserID,
				UserAccount:     msg.UserAccount,
				SiteID:          siteID,
				LastSeenAt:      now,
				CreatedAt:       now,
				UpdatedAt:       now,
			}); err != nil {
				return fmt.Errorf("upsert replier thread subscription: %w", err)
			}
		}

		return nil
	}

	if errors.Is(err, errThreadRoomExists) {
		existingRoom, err := h.threadStore.GetThreadRoomByParentMessageID(ctx, msg.ThreadParentMessageID)
		if err != nil {
			return fmt.Errorf("get existing thread room: %w", err)
		}

		if err := h.threadStore.UpsertThreadSubscription(ctx, &model.ThreadSubscription{
			ID:              uuid.NewString(),
			ParentMessageID: msg.ThreadParentMessageID,
			RoomID:          msg.RoomID,
			ThreadRoomID:    existingRoom.ID,
			UserID:          msg.UserID,
			UserAccount:     msg.UserAccount,
			SiteID:          siteID,
			LastSeenAt:      now,
			CreatedAt:       now,
			UpdatedAt:       now,
		}); err != nil {
			return fmt.Errorf("upsert replier thread subscription: %w", err)
		}

		if err := h.threadStore.UpdateThreadRoomLastMessage(ctx, existingRoom.ID, msg.ID, msg.CreatedAt); err != nil {
			return fmt.Errorf("update thread room last message: %w", err)
		}

		return nil
	}

	return fmt.Errorf("create thread room: %w", err)
}
```

- [ ] **Step 4: Update existing tests in `TestHandler_ProcessMessage` to pass new threadStore argument**

In `message-worker/handler_test.go`, change the test table struct (around line 104) to include the thread store:

```go
tests := []struct {
	name       string
	data       []byte
	setupMocks func(store *MockStore, userStore *MockUserStore, threadStore *MockThreadStore)
	wantErr    bool
}{
```

Update every existing `setupMocks` lambda to accept the third `ts *MockThreadStore` parameter (unused in non-thread cases). For example:

```go
setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
	us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
	store.EXPECT().SaveMessage(gomock.Any(), &msg, &expectedSender, "site-a").Return(nil)
},
```

For the `"thread message — calls SaveThreadMessage not SaveMessage"` case, replace with:

```go
{
	name: "thread message — calls SaveThreadMessage not SaveMessage",
	data: threadData,
	setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
		us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
		store.EXPECT().SaveThreadMessage(gomock.Any(), &threadMsg, &expectedSender, "site-a").Return(nil)
		ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(errThreadRoomExists)
		ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-1").
			Return(&model.ThreadRoom{ID: "tr-1"}, nil)
		ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
		ts.EXPECT().UpdateThreadRoomLastMessage(gomock.Any(), "tr-1", "msg-2", now).Return(nil)
	},
},
```

The `"thread message save error — NAK after user lookup"` case keeps its existing expectations (the SaveThreadMessage error happens before the thread store is touched):

```go
{
	name: "thread message save error — NAK after user lookup",
	data: threadData,
	setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
		us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
		store.EXPECT().SaveThreadMessage(gomock.Any(), &threadMsg, &expectedSender, "site-a").
			Return(errors.New("cassandra: write timeout"))
	},
	wantErr: true,
},
```

Update the test loop (around line 200):

```go
for _, tt := range tests {
	t.Run(tt.name, func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockStore := NewMockStore(ctrl)
		mockUserStore := NewMockUserStore(ctrl)
		mockThreadStore := NewMockThreadStore(ctrl)
		tt.setupMocks(mockStore, mockUserStore, mockThreadStore)

		h := NewHandler(mockStore, mockUserStore, mockThreadStore)
		err := h.processMessage(context.Background(), tt.data)
		if tt.wantErr {
			require.Error(t, err)
		} else {
			assert.NoError(t, err)
		}
	})
}
```

- [ ] **Step 5: Run unit tests to verify they pass**

Run: `make test SERVICE=message-worker`
Expected: PASS — all unit tests green

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go
git commit -m "feat(message-worker): add handleThreadRoomAndSubscriptions with unit tests"
```

---

### Task 6: Wire threadStoreMongo in main.go and Add End-to-End Integration Test

**Files:**
- Modify: `message-worker/main.go`
- Modify: `message-worker/integration_test.go`

- [ ] **Step 1: Update `message-worker/main.go`**

In the wiring section (lines 74-79), replace:

```go
store := NewCassandraStore(cassSession)
handler := NewHandler(store, us)
```

with:

```go
store := NewCassandraStore(cassSession)
threadStore := newThreadStoreMongo(db)
handler := NewHandler(store, us, threadStore)
```

No new imports needed — `db` is already defined on line 75.

- [ ] **Step 2: Verify compilation**

Run: `go build ./message-worker/...`
Expected: PASS

- [ ] **Step 3: Update existing `TestHandler_Integration` to pass threadStore**

In `message-worker/integration_test.go`, update `TestHandler_Integration` (around line 229-289). Replace:

```go
store := NewCassandraStore(cassSession)
us := userstore.NewMongoStore(userCol)
h := NewHandler(store, us)
```

with:

```go
store := NewCassandraStore(cassSession)
us := userstore.NewMongoStore(userCol)
db := mongoClient.Database("chat_test")
ts := newThreadStoreMongo(db)
h := NewHandler(store, us, ts)
```

- [ ] **Step 4: Add end-to-end integration test for thread reply flow**

Add to `message-worker/integration_test.go` (after `TestHandler_Integration`):

```go
func TestHandler_Integration_ThreadReply(t *testing.T) {
	ctx := context.Background()

	cassSession := setupCassandra(t)
	db := setupMongo(t)

	userCol := db.Collection("users")
	_, err := userCol.InsertMany(ctx, []interface{}{
		bson.M{
			"_id":         "u-parent",
			"account":     "parent-user",
			"siteId":      "site-a",
			"engName":     "Parent User",
			"chineseName": "家長",
			"employeeId":  "EMP001",
		},
		bson.M{
			"_id":         "u-replier",
			"account":     "replier",
			"siteId":      "site-a",
			"engName":     "Replier User",
			"chineseName": "回覆者",
			"employeeId":  "EMP002",
		},
	})
	require.NoError(t, err)

	store := NewCassandraStore(cassSession)
	us := userstore.NewMongoStore(userCol)
	ts := newThreadStoreMongo(db)
	h := NewHandler(store, us, ts)

	now := time.Now().UTC().Truncate(time.Millisecond)

	// First: save the parent message to Cassandra so GetMessageSender can find it
	parentMsg := &model.Message{
		ID:          "msg-parent",
		RoomID:      "r-1",
		UserID:      "u-parent",
		UserAccount: "parent-user",
		Content:     "parent message",
		CreatedAt:   now.Add(-1 * time.Minute),
	}
	parentSender := &cassParticipant{
		ID:      "u-parent",
		EngName: "Parent User",
		Account: "parent-user",
	}
	require.NoError(t, store.SaveMessage(ctx, parentMsg, parentSender, "site-a"))

	// Second: process a thread reply (first reply path)
	replyEvt := model.MessageEvent{
		Message: model.Message{
			ID:                    "msg-reply-1",
			RoomID:                "r-1",
			UserID:                "u-replier",
			UserAccount:           "replier",
			Content:               "first thread reply",
			CreatedAt:             now,
			ThreadParentMessageID: "msg-parent",
		},
		SiteID:    "site-a",
		Timestamp: now.UnixMilli(),
	}
	data, err := json.Marshal(replyEvt)
	require.NoError(t, err)
	require.NoError(t, h.processMessage(ctx, data))

	t.Run("thread room created", func(t *testing.T) {
		var room model.ThreadRoom
		err := db.Collection("threadRooms").FindOne(ctx, bson.M{
			"parentMessageId": "msg-parent",
		}).Decode(&room)
		require.NoError(t, err)
		assert.Equal(t, "msg-parent", room.ParentMessageID)
		assert.Equal(t, "r-1", room.RoomID)
		assert.Equal(t, "site-a", room.SiteID)
		assert.Equal(t, "msg-reply-1", room.LastMsgID)
	})

	t.Run("parent author subscribed", func(t *testing.T) {
		count, err := db.Collection("threadSubscriptions").CountDocuments(ctx, bson.M{
			"userId":          "u-parent",
			"parentMessageId": "msg-parent",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
	})

	t.Run("replier subscribed", func(t *testing.T) {
		count, err := db.Collection("threadSubscriptions").CountDocuments(ctx, bson.M{
			"userId":          "u-replier",
			"parentMessageId": "msg-parent",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
	})

	// Third: process a second thread reply (subsequent path)
	reply2Evt := model.MessageEvent{
		Message: model.Message{
			ID:                    "msg-reply-2",
			RoomID:                "r-1",
			UserID:                "u-replier",
			UserAccount:           "replier",
			Content:               "second thread reply",
			CreatedAt:             now.Add(5 * time.Minute),
			ThreadParentMessageID: "msg-parent",
		},
		SiteID:    "site-a",
		Timestamp: now.Add(5 * time.Minute).UnixMilli(),
	}
	data2, err := json.Marshal(reply2Evt)
	require.NoError(t, err)
	require.NoError(t, h.processMessage(ctx, data2))

	t.Run("thread room lastMsgId updated", func(t *testing.T) {
		var room model.ThreadRoom
		err := db.Collection("threadRooms").FindOne(ctx, bson.M{
			"parentMessageId": "msg-parent",
		}).Decode(&room)
		require.NoError(t, err)
		assert.Equal(t, "msg-reply-2", room.LastMsgID)
	})

	t.Run("still only two subscriptions after second reply", func(t *testing.T) {
		count, err := db.Collection("threadSubscriptions").CountDocuments(ctx, bson.M{
			"parentMessageId": "msg-parent",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})
}
```

- [ ] **Step 5: Run all integration tests**

Run: `make test-integration SERVICE=message-worker`
Expected: PASS — all integration tests green

- [ ] **Step 6: Run all unit tests**

Run: `make test SERVICE=message-worker`
Expected: PASS

- [ ] **Step 7: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add message-worker/main.go message-worker/integration_test.go
git commit -m "feat(message-worker): wire threadStoreMongo and add end-to-end integration test"
```

---
