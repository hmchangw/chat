# message-worker: Thread Room & Thread Subscription Creation

**Date:** 2026-04-14
**Status:** Approved

## Summary

When the first thread reply is created under a parent message, message-worker creates a `ThreadRoom` document and `ThreadSubscription` documents (for the parent author and the replier) in MongoDB. On every subsequent thread reply, it upserts a subscription for the replier and updates the thread room's last message metadata. All operations are idempotent.

## Motivation

- Thread rooms provide a lightweight metadata record per thread — tracking `lastMsgAt` and `lastMsgId` enables clients to display thread activity and ordering
- Thread subscriptions track which users are participating in a thread, enabling unread tracking via `lastSeenAt` and future notification scoping
- The message-worker already has full context (message, sender, site) and a MongoDB connection, making it the natural home for these writes

## Scope

This spec covers:
1. New `ThreadRoom` and `ThreadSubscription` models in `pkg/model/`
2. New `ThreadStore` interface and MongoDB implementation in `message-worker/`
3. New `GetMessageSender` method on the existing Cassandra store
4. Updated handler logic to orchestrate thread room/subscription creation on thread replies
5. Unit tests and integration tests

It does **not** cover:
- Client-facing APIs for querying thread rooms or subscriptions (separate task)
- Notification changes for thread replies
- broadcast-worker changes

## Design

### 1. `pkg/model/threadroom.go` — new file

```go
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

MongoDB collection: `threadRooms`
Unique index on `parentMessageId` — used for idempotent first-reply detection.

### 2. `pkg/model/threadsubscription.go` — new file

```go
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

MongoDB collection: `threadSubscriptions`
Unique compound index on `(threadRoomId, userId)` — prevents duplicate subscriptions and enables idempotent upserts.

### 3. `message-worker/store.go` — updated interfaces

```go
//go:generate mockgen -destination=mock_store_test.go -package=main . Store,UserStore,ThreadStore

type Store interface {
    SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error
    SaveThreadMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error
    GetMessageSender(ctx context.Context, messageID string) (*cassParticipant, error)
}

type ThreadStore interface {
    CreateThreadRoom(ctx context.Context, room *model.ThreadRoom) error
    GetThreadRoomByParentMessageID(ctx context.Context, parentMessageID string) (*model.ThreadRoom, error)
    UpsertThreadSubscription(ctx context.Context, sub *model.ThreadSubscription) error
    UpdateThreadRoomLastMessage(ctx context.Context, threadRoomID string, lastMsgID string, lastMsgAt time.Time) error
}
```

### 4. `message-worker/store_cassandra.go` — add `GetMessageSender`

Queries the `messages_by_id` table by `message_id`, returns the `sender` participant UDT (unmarshalled into `cassParticipant`). This provides the parent message author's `userID` and `account` for thread subscription creation.

```sql
SELECT sender FROM messages_by_id WHERE message_id = ? LIMIT 1
```

Returns a wrapped error if no row is found.

### 5. `message-worker/store_mongo.go` — new file, `ThreadStore` implementation

```go
type threadStoreMongo struct {
    threadRooms        *mongo.Collection
    threadSubscriptions *mongo.Collection
}

func newThreadStoreMongo(db *mongo.Database) *threadStoreMongo
```

Constructor creates unique indexes:
- `threadRooms`: unique index on `parentMessageId`
- `threadSubscriptions`: unique compound index on `(threadRoomId, userId)`

**`CreateThreadRoom`**: `InsertOne`. On `mongo.IsDuplicateKeyError` → return `errThreadRoomExists` (package-level sentinel). Caller uses this to distinguish first reply from subsequent.

**`GetThreadRoomByParentMessageID`**: `FindOne` by `{parentMessageId: parentMessageID}`. Returns the thread room document. Returns wrapped `mongo.ErrNoDocuments` if not found.

**`UpsertThreadSubscription`**: `UpdateOne` with `upsert: true`, filter `{threadRoomId, userId}`:
- `$set`: `parentMessageId`, `roomId`, `userAccount`, `siteId`, `lastSeenAt`, `updatedAt`
- `$setOnInsert`: `_id`, `createdAt`

**`UpdateThreadRoomLastMessage`**: `UpdateOne` by `_id`:
- `$set`: `lastMsgAt`, `lastMsgId`, `updatedAt`

### 6. `message-worker/handler.go` — updated

Handler struct gains `threadStore ThreadStore` field:

```go
type Handler struct {
    store      Store
    userStore  UserStore
    threadStore ThreadStore
}
```

New dedicated function `handleThreadRoomAndSubscriptions`:

```
handleThreadRoomAndSubscriptions(ctx, msg *model.Message, siteID string):
    now := time.Now().UTC()

    // Step 1: Attempt to create thread room (idempotent)
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
        // FIRST REPLY — thread room just created
        // Look up parent message author
        parentSender, err := h.store.GetMessageSender(ctx, msg.ThreadParentMessageID)
        // → error handling (return error to NAK)

        // Subscribe parent author
        h.threadStore.UpsertThreadSubscription(ctx, &model.ThreadSubscription{
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
        })

        // Subscribe replier (if different from parent author)
        if msg.UserID != parentSender.ID {
            h.threadStore.UpsertThreadSubscription(ctx, &model.ThreadSubscription{
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
            })
        }
        return nil

    } else if errors.Is(err, errThreadRoomExists) {
        // SUBSEQUENT REPLY — thread room already exists
        existingRoom, err := h.threadStore.GetThreadRoomByParentMessageID(ctx, msg.ThreadParentMessageID)
        // → error handling

        // Subscribe replier (idempotent upsert)
        h.threadStore.UpsertThreadSubscription(ctx, &model.ThreadSubscription{
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
        })

        // Update thread room last message
        h.threadStore.UpdateThreadRoomLastMessage(ctx, existingRoom.ID, msg.ID, msg.CreatedAt)

        return nil

    } else {
        return fmt.Errorf("create thread room: %w", err)
    }
```

`processMessage` calls `handleThreadRoomAndSubscriptions` after `SaveThreadMessage` when `ThreadParentMessageID != ""`.

### 7. `message-worker/main.go` — updated wiring

```go
threadStore := newThreadStoreMongo(db)
handler := NewHandler(store, userStore, threadStore)
```

No new config fields — reuses existing MongoDB connection.

### 8. Error Handling

| Scenario | Behavior |
|----------|----------|
| Thread room insert duplicate key | `errThreadRoomExists` — subsequent reply path |
| Parent message not found in Cassandra | Return error → NAK → JetStream redelivery |
| MongoDB write failure | Return error → NAK → JetStream redelivery |
| Thread subscription upsert failure | Return error → NAK → JetStream redelivery |
| Cassandra SaveThreadMessage succeeds but handleThreadRoomAndSubscriptions fails | Message is persisted in Cassandra; thread room/subscription creation retried on redelivery. SaveThreadMessage is idempotent (Cassandra upsert semantics). handleThreadRoomAndSubscriptions is fully idempotent. |

### 9. Testing

#### Unit tests (`handler_test.go`)

Table-driven tests for `handleThreadRoomAndSubscriptions`:

| Scenario | CreateThreadRoom | GetMessageSender | UpsertThreadSubscription | UpdateThreadRoomLastMessage | Expected |
|----------|-----------------|-----------------|-------------------------|---------------------------|----------|
| First reply, different users | success | returns sender | called twice (parent + replier) | not called | nil |
| First reply, same user | success | returns sender (same as replier) | called once | not called | nil |
| First reply, GetMessageSender fails | success | error | not called | not called | error |
| First reply, UpsertThreadSubscription fails | success | returns sender | error | not called | error |
| Subsequent reply | errThreadRoomExists | not called | called once (replier) | called once | nil |
| Subsequent reply, GetThreadRoomByParentMessageID fails | errThreadRoomExists | not called | not called | not called | error |
| Subsequent reply, UpsertThreadSubscription fails | errThreadRoomExists | not called | error | not called | error |
| Subsequent reply, UpdateThreadRoomLastMessage fails | errThreadRoomExists | not called | called once | error | error |
| CreateThreadRoom unexpected error | error | not called | not called | not called | error |

#### Integration tests (`integration_test.go`)

- `setupMongo(t)` — starts MongoDB testcontainer, returns `*mongo.Database`
- Test `threadStoreMongo.CreateThreadRoom` — insert + duplicate detection
- Test `threadStoreMongo.UpsertThreadSubscription` — insert + idempotent update
- Test `threadStoreMongo.UpdateThreadRoomLastMessage` — verify fields updated
- Test `threadStoreMongo.GetThreadRoomByParentMessageID` — found + not found
- Test `GetMessageSender` — with seeded Cassandra data

#### Model tests (`pkg/model/model_test.go`)

- `TestThreadRoomJSON` — round-trip marshal/unmarshal
- `TestThreadSubscriptionJSON` — round-trip marshal/unmarshal

## File Changes Summary

| File | Action |
|------|--------|
| `pkg/model/threadroom.go` | New |
| `pkg/model/threadsubscription.go` | New |
| `pkg/model/model_test.go` | Add round-trip tests |
| `message-worker/store.go` | Add `GetMessageSender` to `Store`, add `ThreadStore` interface, update `mockgen` directive |
| `message-worker/store_cassandra.go` | Add `GetMessageSender` method |
| `message-worker/store_mongo.go` | New — `threadStoreMongo` implementation |
| `message-worker/handler.go` | Add `threadStore` field, add `handleThreadRoomAndSubscriptions`, update `processMessage` |
| `message-worker/main.go` | Wire `threadStoreMongo` into handler |
| `message-worker/handler_test.go` | Add thread room/subscription test cases |
| `message-worker/integration_test.go` | Add thread store integration tests |
| `message-worker/mock_store_test.go` | Regenerated via `make generate` |
