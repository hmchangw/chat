# Broadcast Worker: Thread Message Handling

**Date:** 2026-05-28
**Status:** Implemented — PR #245 (`claude/gallant-galileo-ice0C`)

## Implementation Notes (what changed from this design)

The PR delivered everything in this spec plus several additions discovered during implementation. Key divergences:

### broadcast-worker additions
- **DM/BotDM thread handling**: all three thread handlers (`handleThreadCreated`, `handleThreadUpdated`, `handleThreadDeleted`) gained a DM/BotDM branch. Channel thread replies fan out to thread subscribers only; DM thread replies fan out to all DM members via `publishDMEvents` (DMs have no concept of "only thread subscribers — deliver to everyone"). `handleCreated` delegates to `handleThreadCreated`, which routes by room type.
- **`EventThreadReplyAdded` badge handler**: broadcast-worker gained `handleThreadTCountUpdated` to process the new `EventThreadReplyAdded` event published by message-worker, sending a `ThreadMetadataUpdatedEvent` (reply count badge update) to channel rooms and DM members.
- **Thread delete badge**: `handleThreadDeleted` also publishes the badge update (`publishThreadMetadata`) when `evt.NewTCount != nil`, so clients see the reply count decrement immediately.
- **TShow=true thread-reply badge on delete**: when a `TShow=true` thread reply is deleted it flows through `handleDeleted` (normal room broadcast), bypassing `handleThreadDeleted`. `handleDeleted` detects `ThreadParentMessageID != ""` and publishes the tcount badge there, so the reply-count decrement reaches clients regardless of TShow.
- **@-mention fan-out on delete**: `handleThreadDeleted` channel path parses @-mentions from the deleted message content so non-subscriber recipients who received the `EventCreated` (via mention fan-out) also receive the `EventDeleted`.
- **`SetSubscriptionMentions` in `handleThreadCreated`**: when a thread reply @-mentions room members, those accounts' room subscriptions get the mention flag set (same as regular messages).
- **`channelThreadFanOut` helper**: extracted to avoid repeating the subscriber-query + dedup logic across all three channel-path handlers.
- **`evt.Timestamp` propagation**: `EditRoomEvent` and `DeleteRoomEvent` set `Timestamp` from `evt.Timestamp` (the canonical event's publish time), not from `time.Now()` at broadcast time. Using wall-clock time at broadcast would make the timestamp differ across redeliveries and lag behind the canonical timeline.
- **`shouldUseThreadFanOut` rename** (was `isThreadReply`): the predicate encodes a routing decision (fan-out to thread subscribers vs. room broadcast), not a structural "is a thread reply" test — the new name makes that intent explicit.
- **`buildEditRoomEvent` / `buildDeleteRoomEvent` helpers**: extracted to eliminate the duplicated 9-field struct literals that appeared independently in the non-thread and thread variants of each handler.
- **`publishThreadBadge` helper**: consolidates the `publishThreadMetadata` call + error-log pattern that was duplicated in `handleThreadDeleted` and the TShow=true path of `handleDeleted`.
- **`handleThreadDeleted` default-branch `return nil` removed**: the premature return was silently skipping the tcount badge block for unknown room types. The badge block now runs unconditionally after the switch; `publishThreadMetadata` handles unknown room types gracefully via its own default branch.
- **Nil guard in `handleThreadUpdated`**: added a defensive `EditedAt`/`UpdatedAt` nil check (mirrors the outer guard in `handleUpdated`) to prevent a panic if the function is ever called without the outer guard in a future refactor.

### message-worker additions
- **`SaveThreadMessage` returns `*int` (new tcount)**: the store method was extended to return the post-CAS tcount from Cassandra so message-worker can publish the `EventThreadReplyAdded` event with an authoritative count.
- **`publishThreadReplyEvent`**: new handler method that publishes `EventThreadReplyAdded` to `subject.MsgCanonicalThreadReply(siteID)`. Publish errors are intentionally swallowed (Cassandra write already committed; propagating would cause JetStream nack → retry → double-increment).
- **Thread subscription outbox**: `message-worker` publishes `OutboxThreadSubscriptionUpserted` events for remote-site thread subscribers.

### history-service additions
- **`SoftDeleteMessage` returns `newTcount`**: the cassrepo method now runs a CAS decrement on `messages_by_id.tcount` (and mirrors to `messages_by_room`) and returns the post-CAS value so the service layer can include it in the `EventDeleted` publish.
- **`decrementParentTcount`**: new cassrepo helper that does the Cassandra CAS tcount decrement for deleted thread replies. Returns `nil` for legacy rows with NULL tcount (nothing authoritative to publish).
- **Already-deleted retry path re-publishes with tcount**: the already-deleted short-circuit in `DeleteMessage` re-fetches the parent's current tcount and re-publishes the `EventDeleted` event so a lost badge update gets retried.
- **`EventDeleted` carries `Content`**: history-service populates `Message.Content` on `EventDeleted` payloads so broadcast-worker's mention parser can build the correct fan-out list for deleted thread replies.

### notification-worker — intentionally unchanged
notification-worker was scoped out of this PR. All planned changes to it are documented in [`docs/thread-reply-notifications.md`](../../thread-reply-notifications.md). The engineer who owns notification-worker should read that file before starting. The three things that need to be built there are: (1) filter to `EventCreated` only, (2) route thread replies to thread subscribers only, (3) notify @-mentioned non-subscribers via `EventThreadReplyAdded`.

### Known remaining gap
`Subscription.ThreadUnread` — the array that drives the unread thread badge on the client — is still not updated when a thread reply arrives. This was out of scope here (noted in the original design) and must be addressed in a separate task.

---

## Background

The broadcast-worker consumes from `MESSAGES_CANONICAL` and fans out message events (Created, Updated, Deleted) to room members. Currently it has no awareness of thread messages — thread reply messages are treated identically to regular room messages and broadcast to all room members, regardless of thread membership.

The message model already supports threads via `ThreadParentMessageID`, `ThreadParentMessageCreatedAt`, and `TShow` fields on `model.Message`. The message-worker already creates `ThreadRoom` and `ThreadSubscription` records when thread replies arrive. What is missing is real-time delivery of thread events to the correct set of recipients.

## What Message-Worker Already Handles

The following thread state is already managed by message-worker and is **not** broadcast-worker's responsibility:

- Creating `ThreadRoom` on the first reply to a parent message
- Inserting/upserting `ThreadSubscription` records for the parent author, replier, and @mentioned users
- Setting `ThreadSubscription.hasMention=true` for @mentioned users via `MarkThreadSubscriptionMention`
- Updating `ThreadRoom.LastMsgAt` and `ThreadRoom.LastMsgID`
- Publishing cross-site outbox events (`thread_subscription_upserted`)

## Known Gap in Message-Worker (Out of Scope Here)

`Subscription.ThreadUnread` — the array on a user's room subscription that tracks which parent message IDs have unread thread replies — is **not currently updated** when a thread reply arrives. This is the field that drives the unread thread badge on the client. Message-worker must be updated (in a separate task) to add the `parentMessageID` to `Subscription.ThreadUnread` for all thread subscribers who are not the sender.

## Design Goals

1. Fan-out thread reply events in real time to the correct set of recipients.
2. Leave all persistent state mutations to message-worker.
3. Reuse existing subjects, helpers, and patterns — no new NATS subject namespace.
4. Keep broadcast-worker as pure real-time delivery: no MongoDB writes.

## Routing Gate

Thread messages are identified by a non-empty `ThreadParentMessageID`. The `TShow` flag determines which broadcast path to use:

| Condition | Path |
|-----------|------|
| `ThreadParentMessageID == ""` | Existing room/DM broadcast (unchanged) |
| `ThreadParentMessageID != ""` AND `TShow=true` | Existing room/DM broadcast (unchanged) — message appears in main room feed; all thread subscribers receive it as room members |
| `ThreadParentMessageID != ""` AND `TShow=false` | New thread subscriber fan-out |

For `TShow=true`: thread subscribers are always room members, so the room broadcast already delivers the message to them. The `ClientMessage` payload retains `ThreadParentMessageID`, allowing clients to render the message in both the main room feed and the thread view. No duplicate fan-out is needed.

The routing check is placed at the top of each event handler method (`handleCreated`, `handleUpdated`, `handleDeleted`), delegating to a dedicated thread handler method when conditions are met.

## Store Interface Change

One new method added to the `Store` interface in `store.go`:

```go
ListThreadSubscriptions(ctx context.Context, parentMessageID, siteID string) ([]model.ThreadSubscription, error)
```

Implemented in `store_mongo.go` against the existing `thread_subscriptions` collection, filtered by `parentMessageId` and `siteId`. The existing index on `(parentMessageId, userAccount)` covers this query — no new indexes required.

The `//go:generate mockgen` directive in `store.go` regenerates `mock_store_test.go` via `make generate`.

## New Handler Methods

Three new unexported methods on the handler struct, mirroring the structure of the existing `handleCreated`, `handleUpdated`, `handleDeleted` methods.

### `handleThreadCreated`

1. Look up users — sender + mentioned accounts from the message payload (same user lookup as existing `handleCreated`).
2. Query `ListThreadSubscriptions(parentMessageID, siteID)` → collect subscriber accounts.
3. Build fan-out set:
   - Union of thread subscriber accounts + mentioned accounts from the message payload.
   - Deduplicate using a `seen` map seeded with the sender's account (sender is excluded from fan-out). `dedupedAccounts` is not used here — it puts the sender first, which is the opposite of what's needed.
   - Exclude the sender's account.
4. Build `ClientMessage` — same enrichment flow as existing created handler (sender display name, user IDs, etc.).
5. Publish to `subject.UserRoomEvent(account)` for each account in the fan-out set.

**Why union of DB subscribers + mentioned accounts?**
Message-worker and broadcast-worker consume `MESSAGES_CANONICAL` independently with no guaranteed ordering. If broadcast-worker processes the event before message-worker creates the `ThreadSubscription` for a newly @mentioned user, the DB query will not include that user. Including mentioned accounts directly from the message payload closes this race, ensuring @mentioned users always receive the real-time notification. All mentioned accounts are guaranteed to be room members (enforced by message-gatekeeper).

### `handleThreadUpdated`

1. Query `ListThreadSubscriptions(parentMessageID, siteID)` → subscriber accounts.
2. Fan-out set: thread subscriber accounts only, sender excluded. (Edits do not introduce new mentioned users.)
3. Build edit event payload.
4. Publish to `subject.UserRoomEvent(account)` for each subscriber.

### `handleThreadDeleted`

1. Query `ListThreadSubscriptions(parentMessageID, siteID)` → subscriber accounts.
2. Fan-out set: thread subscriber accounts only, sender excluded.
3. Build delete event payload.
4. Publish to `subject.UserRoomEvent(account)` for each subscriber.

## Delivery Subject

All thread events are published to `subject.UserRoomEvent(account)` — the same per-user subject used for DM deliveries. Clients already subscribe to this subject. The `ThreadParentMessageID` field in the payload distinguishes thread events from regular room events; no new subject namespace is required.

## Error Handling

| Error | Behavior |
|-------|----------|
| `ListThreadSubscriptions` fails | Log error, return error → JetStream redelivers the message |
| User lookup fails | Log warning, continue — sender display name falls back to the account string |
| Individual NATS publish fails | Log error, continue to remaining subscribers — partial delivery is acceptable (consistent with existing DM publish error handling) |
| No thread subscribers found | Log at debug level, return nil — nothing to fan-out |

## Encryption

Thread messages in encrypted channel rooms use the same `RoomKeyProvider` path as the existing `publishChannelEvent`: encrypt once, then publish the same ciphertext to each subscriber. DM rooms are never encrypted, so the DM delivery path has no encryption — thread replies follow the channel path, not the DM path. No new design is needed.

## Testing Plan

All tests follow TDD: tests written and confirmed failing before implementation is written.

### Unit Tests (`handler_test.go`)

Table-driven tests for each new handler method:

**`handleThreadCreated`:**
- Thread reply with @mentioned user not yet a subscriber → mentioned user included in fan-out
- Thread reply to existing thread with multiple subscribers → all receive the event
- Mentioned user already a thread subscriber → deduped, not double-published
- Sender is excluded from fan-out
- `TShow=true` → falls through to room broadcast, thread handler not called
- `GetRoomMeta` error → error returned, no publish
- `ListThreadSubscriptions` error → error returned, no publish
- User lookup error → log warning, continue (sender display name falls back to account string)
- No subscribers and no mentions → no publish, no error

**`handleThreadUpdated`:**
- All thread subscribers receive the edit event, sender excluded
- Empty subscriber list → no publish, no error
- `GetRoom` error → error returned, no publish
- `ListThreadSubscriptions` error → error returned, no publish
- `TShow=true` → falls through to room broadcast, thread handler not called

**`handleThreadDeleted`:**
- All thread subscribers receive the delete event, sender excluded
- Empty subscriber list → no publish, no error
- `GetRoom` error → error returned, no publish
- `ListThreadSubscriptions` error → error returned, no publish
- `TShow=true` → falls through to room broadcast, thread handler not called

### Mock Regeneration

`make generate` must be run after the store interface change to regenerate `mock_store_test.go` with the `ListThreadSubscriptions` mock method.

## Commit Strategy

Small, reviewable commits in this order:

1. **Store** — add `ListThreadSubscriptions` to `Store`, implement in `store_mongo.go`, regenerate mocks, update all `NewMongoStore` call sites, add integration test.
2. **`handleThreadCreated`** — write failing tests, add routing gate to `handleCreated`, implement method, commit once green.
3. **`handleThreadUpdated`** — write failing tests, add routing gate to `handleUpdated`, implement method, commit once green.
4. **`handleThreadDeleted`** — write failing tests, add routing gate to `handleDeleted`, implement method, commit once green.
5. **Final** — `make lint`, full test run, push.

Each commit is independently compilable and does not break existing tests.

## Files Changed

| File | Change |
|------|--------|
| `broadcast-worker/store.go` | Add `ListThreadSubscriptions` to `Store` interface |
| `broadcast-worker/store_mongo.go` | Implement `ListThreadSubscriptions` |
| `broadcast-worker/main.go` | Pass `db.Collection("thread_subscriptions")` to `NewMongoStore` |
| `broadcast-worker/mock_store_test.go` | Regenerated — do not edit manually |
| `broadcast-worker/handler.go` | Add routing gate + three new thread handler methods |
| `broadcast-worker/handler_test.go` | New table-driven tests for thread handler methods |
| `broadcast-worker/integration_test.go` | Update `NewMongoStore` calls + integration test for `ListThreadSubscriptions` |
