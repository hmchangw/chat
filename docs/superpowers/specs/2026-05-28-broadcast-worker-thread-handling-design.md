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
- **`SetSubscriptionMentions` — DM/BotDM branch only**: `handleThreadCreated` sets the room-subscription mention flag for @-mentioned members *only on the DM/BotDM path*. The channel path deliberately does **not** call `SetSubscriptionMentions`: a `TShow=false` reply is invisible in the main channel feed, so a room-level mention badge would appear with no visible message to explain it. (Thread-level mention state for channel replies is handled upstream by message-worker via `MarkThreadSubscriptionMention`.)
- **`channelThreadFanOut` helper**: extracted to avoid repeating the subscriber-query + dedup logic across all three channel-path handlers.
- **`evt.Timestamp` propagation**: `EditRoomEvent` and `DeleteRoomEvent` set `Timestamp` from `evt.Timestamp` (the canonical event's publish time), not from `time.Now()` at broadcast time. Using wall-clock time at broadcast would make the timestamp differ across redeliveries and lag behind the canonical timeline.
- **`shouldUseThreadFanOut` rename** (was `isThreadReply`): the predicate encodes a routing decision (fan-out to thread subscribers vs. room broadcast), not a structural "is a thread reply" test — the new name makes that intent explicit.
- **`buildEditRoomEvent` / `buildDeleteRoomEvent` helpers**: extracted to eliminate the duplicated 9-field struct literals that appeared independently in the non-thread and thread variants of each handler.
- **`publishThreadBadge` helper**: consolidates the `publishThreadMetadata` call + error-log pattern that was duplicated in `handleThreadDeleted` and the TShow=true path of `handleDeleted`.
- **`handleThreadDeleted` default-branch `return nil` removed**: the premature return was silently skipping the tcount badge block for unknown room types. The badge block now runs unconditionally after the switch; `publishThreadMetadata` handles unknown room types gracefully via its own default branch.
- **Nil guard in `handleThreadUpdated`**: added a defensive `EditedAt`/`UpdatedAt` nil check (mirrors the outer guard in `handleUpdated`) to prevent a panic if the function is ever called without the outer guard in a future refactor.

### message-worker additions
- **`SaveThreadMessage` returns `*int` (new tcount)**: the store method was extended to return the post-CAS tcount from Cassandra so message-worker can publish the `EventThreadReplyAdded` event with an authoritative count.
- **Idempotent CAS increment via `IF NOT EXISTS`**: `SaveThreadMessage` inserts the reply into `messages_by_id` with an `IF NOT EXISTS` LWT. On a JetStream redelivery the insert reports `applied=false`, so the handler reads back the existing tcount (`readParentTcount`) instead of re-incrementing (`incrementParentTcount`). This is what makes publish-error retries safe — a redelivered reply never double-counts the badge.
- **`publishThreadReplyEvent`**: new handler method that publishes `EventThreadReplyAdded` to `subject.MsgCanonicalThreadReply(siteID)`. Publish errors are **propagated** (NAK → JetStream redelivery), not swallowed — otherwise a dropped publish would permanently lose the badge update. Redelivery is safe because of the `IF NOT EXISTS` LWT above (the increment is idempotent), so there is no double-increment risk.
- **Thread subscription outbox**: `message-worker` publishes `OutboxThreadSubscriptionUpserted` events for remote-site thread subscribers.

### history-service additions
- **`SoftDeleteMessage` returns `newTcount`**: the cassrepo method now runs a CAS decrement on `messages_by_id.tcount` (and mirrors to `messages_by_room`) and returns the post-CAS value so the service layer can include it in the `EventDeleted` publish.
- **`decrementParentTcount`**: new cassrepo helper that does the Cassandra CAS tcount decrement for deleted thread replies. Returns `nil` for legacy rows with NULL tcount (nothing authoritative to publish).
- **Already-deleted retry path re-publishes with tcount**: the already-deleted short-circuit in `DeleteMessage` re-fetches the parent's current tcount and re-publishes the `EventDeleted` event so a lost badge update gets retried.
- **`EventDeleted` carries `Content`**: history-service populates `Message.Content` on `EventDeleted` payloads so broadcast-worker's mention parser can build the correct fan-out list for deleted thread replies.

### notification-worker — intentionally unchanged
notification-worker was scoped out of this PR. All planned changes to it are documented in [`docs/thread-reply-notifications.md`](../../thread-reply-notifications.md). The engineer who owns notification-worker should read that file before starting. The three things that need to be built there are: (1) filter to `EventCreated` only, (2) route thread replies to thread subscribers only, (3) notify @-mentioned non-subscribers via `EventThreadReplyAdded`.

### Known remaining gaps
- **`Subscription.ThreadUnread`** — the array that drives the unread thread badge on the client — is still not updated when a thread reply arrives. This was out of scope here (noted in the original design) and must be addressed in a separate task.
- **Parent-message mentionees are not subscribed.** A user @-mentioned only in the parent message who never replies is never added to the thread subscription set (only the parent author, repliers, and reply-mentionees are). They receive no thread events. Closing this requires carrying the parent's resolved mention list onto the reply event so message-worker can seed those subscriptions on thread-room creation.
- **Edit/delete fan-out uses the current subscriber set.** A user @-mentioned in the *original* reply but later un-mentioned (or whose mention was edited out) will not receive the edit/delete event, because the fan-out re-derives recipients from current subscribers + current content. Fixing this would require persisting the original recipient list per reply.

---

## Background

The broadcast-worker consumes from `MESSAGES_CANONICAL` and fans out message events (Created, Updated, Deleted) to room members. Currently it has no awareness of thread messages — thread reply messages are treated identically to regular room messages and broadcast to all room members, regardless of thread membership.

The message model already supports threads via `ThreadParentMessageID`, `ThreadParentMessageCreatedAt`, and `TShow` fields on `model.Message`. The message-worker already creates `ThreadRoom` and `ThreadSubscription` records when thread replies arrive. What is missing is real-time delivery of thread events to the correct set of recipients.

## What Message-Worker Already Handles

The following thread state is already managed by message-worker and is **not** broadcast-worker's responsibility:

- Creating `ThreadRoom` on the first reply to a parent message
- Inserting/upserting `ThreadSubscription` records for the **parent message author** and the **replier** (on every reply)
- Setting `ThreadSubscription.hasMention=true` (auto-creating the subscription if absent) for users **@-mentioned in a reply**, via `MarkThreadSubscriptionMention`
  - **Gap:** a user @-mentioned only in the *parent* message who never replies is **not** subscribed — no code path reads the parent's mention list. Such a user receives no thread events. Subscribing them would require carrying the parent's resolved mentions onto the reply event (see "Known remaining gap" below).
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

Three new unexported methods on the handler struct, mirroring the structure of the existing `handleCreated`, `handleUpdated`, `handleDeleted` methods. Each **branches on room type** (`meta.Type` / `room.Type`): channel rooms fan out to the thread-subscriber set, while DM/BotDM rooms fan out to all human members (see the DM/BotDM note in "Implementation Notes"). The channel-path subscriber-query + dedup logic is shared via the `channelThreadFanOut` helper.

> The numbered flows below describe the **channel path**. The DM/BotDM path delegates to `publishDMEvents` and is summarized in "Implementation Notes".

### `handleThreadCreated` (channel path)

1. Look up users — sender + mentioned accounts from the message payload (same user lookup as existing `handleCreated`).
2. `channelThreadFanOut(parentMessageID, siteID, sender, mentions)` → query `ListThreadSubscriptions`, then build the fan-out set via `threadFanOutAccounts`:
   - Union of thread subscriber accounts + mentioned accounts from the message payload.
   - Deduplicate using a `seen` map seeded with the sender's account (sender excluded).
   - **Bot accounts are excluded** (`isBot`), consistent with every other fan-out path (`publishMutation`, `publishDMEvents`).
3. Build `ClientMessage` — same enrichment flow as existing created handler (sender display name, user IDs, etc.).
4. `publishToThreadAccounts` → publish to `subject.UserRoomEvent(account)` for each account; on the first publish failure it **returns the error** so JetStream redelivers.

**Why union of DB subscribers + mentioned accounts?**
Message-worker and broadcast-worker consume `MESSAGES_CANONICAL` independently with no guaranteed ordering. If broadcast-worker processes the event before message-worker creates the `ThreadSubscription` for a newly @mentioned user, the DB query will not include that user. Including mentioned accounts directly from the message payload closes this race, ensuring @mentioned users always receive the real-time notification. All mentioned accounts are guaranteed to be room members (enforced by message-gatekeeper).

### `handleThreadUpdated` (channel path)

1. Defensive `EditedAt`/`UpdatedAt` nil guard (mirrors `handleUpdated`).
2. `channelThreadFanOut` → subscriber accounts (sender + bots excluded). Edits do not introduce new mentioned users, but the deleted/edited content is still parsed for @-mentions so the recipient set matches the original create fan-out.
3. Build the edit event via `buildEditRoomEvent` (timestamp from `evt.Timestamp`, not `time.Now()`).
4. `publishToThreadAccounts` → publish to each subscriber; returns error on failure for redelivery.

### `handleThreadDeleted` (channel path)

1. `channelThreadFanOut`, parsing @-mentions from the **deleted message content** so non-subscriber recipients who received the `EventCreated` (via mention fan-out) also receive the `EventDeleted`.
2. Build the delete event via `buildDeleteRoomEvent` (timestamp from `evt.Timestamp`).
3. `publishToThreadAccounts` → publish to each recipient; returns error on failure.
4. After the room-type switch, publish the reply-count badge (`publishThreadBadge`) when `evt.NewTCount != nil`, so clients see the decrement immediately.

## Delivery Subject

All thread events are published to `subject.UserRoomEvent(account)` — the same per-user subject used for DM deliveries. Clients already subscribe to this subject. The `ThreadParentMessageID` field in the payload distinguishes thread events from regular room events; no new subject namespace is required.

## Error Handling

| Error | Behavior |
|-------|----------|
| `ListThreadSubscriptions` fails | Log error, return error → JetStream redelivers the message |
| User lookup fails | Log warning, continue — sender display name falls back to the account string |
| NATS publish fails (channel thread path, `publishToThreadAccounts`) | Log error and **return the error** on the first failure → JetStream redelivers. Thread per-user events must have the same delivery guarantee as channel room events; redelivery is safe because the upstream CAS increment is idempotent. |
| NATS publish fails (DM/BotDM path, `publishDMEvents`/`publishMutation`) | Log error, continue to remaining members — partial delivery accepted (consistent with existing DM mutation handling). |
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
