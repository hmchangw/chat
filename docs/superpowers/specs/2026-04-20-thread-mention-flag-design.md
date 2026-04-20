# Thread Mention Flag Design

## Summary

When a thread reply contains `@account` mentions, `message-worker` auto-subscribes each mentioned user (other than the sender) to the thread and sets `ThreadSubscription.hasMention = true`. `@all` inside a thread is intentionally ignored at the thread level; the top-level `Subscription.hasMention` flow handled by `broadcast-worker` is unchanged.

## Motivation

Today, `message-worker`:

- Parses mentions via `mention.Resolve` and stores them on `Message.Mentions`.
- Creates `ThreadRoom` and `ThreadSubscription` rows for the parent author + replier.

It does **not** record a per-user "you were mentioned in this thread" signal. Clients that render unread/mention indicators for threads have no field to read. The parallel flag for regular rooms (`Subscription.hasMention`) is set by `broadcast-worker` via `SetSubscriptionMentions`; threads need the equivalent at the `ThreadSubscription` level.

## Scope

**In scope**

- Add `HasMention` field to `model.ThreadSubscription`.
- In `message-worker`, for thread messages, upsert a `ThreadSubscription` with `hasMention = true` for each `@account` mentionee, excluding the sender.
- Auto-create a `ThreadSubscription` for a mentionee who has never participated in the thread.
- Handler, store, model, and integration tests.

**Out of scope**

- Clearing `hasMention` — belongs to a future "mark thread as read" API.
- Room-level `Subscription.hasMention` — already handled by `broadcast-worker`.
- `@all` handling inside threads — intentionally ignored at the thread level.
- Cross-site (OUTBOX/INBOX) propagation of thread mentions.

## Design

### Model (`pkg/model/threadsubscription.go`)

Add a single field:

```go
type ThreadSubscription struct {
    ID              string     `json:"id"              bson:"_id"`
    ParentMessageID string     `json:"parentMessageId" bson:"parentMessageId"`
    RoomID          string     `json:"roomId"          bson:"roomId"`
    ThreadRoomID    string     `json:"threadRoomId"    bson:"threadRoomId"`
    UserID          string     `json:"userId"          bson:"userId"`
    UserAccount     string     `json:"userAccount"     bson:"userAccount"`
    SiteID          string     `json:"siteId"          bson:"siteId"`
    LastSeenAt      *time.Time `json:"lastSeenAt"      bson:"lastSeenAt"`
    HasMention      bool       `json:"hasMention"      bson:"hasMention"`
    CreatedAt       time.Time  `json:"createdAt"       bson:"createdAt"`
    UpdatedAt       time.Time  `json:"updatedAt"       bson:"updatedAt"`
}
```

Default zero value (`false`) is correct for new, non-mention subscriptions.

### Handler flow (`message-worker/handler.go`)

`processMessage` already resolves mentions before branching into the thread path (line 50-54). Extend the thread branch:

```
if evt.Message.ThreadParentMessageID != "":
    threadRoomID = handleThreadRoomAndSubscriptions(...)     // existing: parent + replier subs
    markThreadMentions(ctx, &evt.Message, threadRoomID, siteID, now)   // NEW
    store.SaveThreadMessage(...)                              // existing
```

`markThreadMentions` iterates `msg.Mentions`:

- Skip if `p.UserID == msg.UserID` (sender exclusion).
- Skip if `p.Account == "all"` (the sentinel participant inserted by `mention.Resolve` when `@all` is present — `@all` is ignored at the thread level).
- Otherwise call `threadStore.MarkThreadSubscriptionMention(ctx, sub)` where `sub` is built via a helper analogous to `buildThreadSubscription` but with `HasMention: true`.

`markThreadMentions` returns an error if any upsert fails, causing the JetStream message to be NAK'd and redelivered. Because the upsert is idempotent, redelivery is safe.

### Store interface (`message-worker/store.go`)

Add one method to `ThreadStore`:

```go
MarkThreadSubscriptionMention(ctx context.Context, sub *model.ThreadSubscription) error
```

### Store implementation (`message-worker/store_mongo.go`)

```go
func (s *threadStoreMongo) MarkThreadSubscriptionMention(ctx context.Context, sub *model.ThreadSubscription) error {
    filter := bson.M{"threadRoomId": sub.ThreadRoomID, "userId": sub.UserID}
    update := bson.M{
        "$setOnInsert": bson.M{
            "_id":             sub.ID,
            "parentMessageId": sub.ParentMessageID,
            "roomId":          sub.RoomID,
            "threadRoomId":    sub.ThreadRoomID,
            "userId":          sub.UserID,
            "userAccount":     sub.UserAccount,
            "siteId":          sub.SiteID,
            "lastSeenAt":      sub.LastSeenAt, // nil for brand-new sub
            "createdAt":       sub.CreatedAt,
        },
        "$set": bson.M{
            "hasMention": true,
            "updatedAt":  sub.UpdatedAt,
        },
    }
    if _, err := s.threadSubscriptions.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true)); err != nil {
        return fmt.Errorf("mark thread subscription mention: %w", err)
    }
    return nil
}
```

Notes:

- `$set` and `$setOnInsert` must not overlap — `hasMention` and `updatedAt` live only in `$set` so they're applied on both insert and update.
- Existing `(threadRoomId, userId)` unique index already guarantees one row per user per thread, so the upsert is safe under concurrent deliveries.
- `options.UpdateOne().SetUpsert(true)` matches the existing upsert pattern in `UpsertThreadSubscription`.

### Ordering vs existing thread-subscription writes

`handleThreadRoomAndSubscriptions` runs **before** `markThreadMentions`. This means:

- On a first reply that mentions the parent author: `InsertThreadSubscription` creates the parent author's sub with `hasMention=false`, then `MarkThreadSubscriptionMention` updates it to `true`. Two writes, idempotent.
- On a subsequent reply that mentions a non-participant: `handleSubsequentThreadReply` upserts parent + replier (no-ops if already present), then `MarkThreadSubscriptionMention` inserts the mentionee with `hasMention=true`.
- On a first reply where the replier mentions themselves: parent author sub is created; sender is excluded from the mention path; no spurious flag.

This ordering keeps `markThreadMentions` independent of whether it's the first reply or a subsequent one.

### Mocks

`go:generate mockgen` directives in `store.go` already cover `ThreadStore`. Run `make generate SERVICE=message-worker` to regenerate `mock_store_test.go`.

## Testing

### Unit tests (`message-worker/handler_test.go`)

Extend the thread-message suite with table-driven cases asserting `MarkThreadSubscriptionMention` calls:

| Case | Mentions in content | Expected `MarkThreadSubscriptionMention` calls |
|------|---------------------|-----------------------------------------------|
| No mentions | — | 0 |
| `@bob` where bob ≠ sender, not in thread | `@bob` | 1, with `userId=bob`, `hasMention=true` |
| `@parent_author` | `@parent_author` | 1, with `userId=parent_author` |
| Sender self-mention | `@sender` | 0 (exclusion) |
| `@all` only | `@all` | 0 (thread-level ignore) |
| `@all` + `@bob` | mixed | 1 (bob only) |

Also assert the existing parent/replier subscription behavior is unchanged in each case.

### Model tests (`pkg/model/model_test.go`)

Update the `ThreadSubscription` roundTrip fixture to include `HasMention: true` and assert both JSON and BSON roundtrip preserve the field.

### Integration test (`message-worker/integration_test.go`)

Add a case: publish a thread reply whose content contains `@bob` where bob is neither the parent author nor the replier. Assert:

- `threadSubscriptions` collection has a row for bob with `hasMention: true` and `threadRoomId` matching the thread room.
- Parent author + replier subs also exist with `hasMention: false` (unless they were also mentioned).

## Risks

- **Double write to parent-author sub when parent is mentioned in the first reply.** Two sequential writes, both idempotent. Acceptable.
- **Mentioned user not in user directory.** `mention.Resolve` only returns `Participants` for users it resolved, so non-existent accounts are silently dropped upstream. No new handling needed.
- **Concurrent deliveries of the same message.** `(threadRoomId, userId)` unique index serializes; `$setOnInsert` + `$set` are safe under concurrent upserts.

## Rollout

No migration needed — adding a field with a `false` zero value is backward-compatible; existing rows decode with `HasMention = false`.
