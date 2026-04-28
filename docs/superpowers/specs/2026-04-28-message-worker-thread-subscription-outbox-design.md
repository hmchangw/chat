# Message-worker thread subscription outbox events

## Problem

When a user posts a thread reply, `message-worker` writes a `ThreadSubscription`
to MongoDB for the **parent author**, the **replier**, and any **`@account`
mentionee** in the reply. Today those writes happen only on the room's home
site. If any of those users' home sites differ from the room's home site, their
`subscriptions` list at home shows nothing — they have no record that they're
participating in the thread, can't see it in their UI, and won't receive a
mention indicator.

`room-worker` already solves the analogous problem for room subscriptions via
the OUTBOX/INBOX pattern. `message-worker` has no equivalent path: it doesn't
publish to JetStream at all today.

## Goal

Replicate `ThreadSubscription` upserts to each affected user's home site via the
existing OUTBOX/INBOX federation. After this change, when bob@site-b replies in
a thread on a room hosted at site-a, both bob's home site (site-b) and the
parent author's home site (site-c if applicable) end up with a
`ThreadSubscription` document in their local MongoDB. Mentionees get the same
treatment, including the `hasMention=true` flag.

## Non-goals

- **`ThreadRoom` replication.** No remote service queries `thread_rooms` today
  (verified: only message-worker reads from it). Replicating the lastMsgAt /
  lastMsgID pointers per reply would double cross-site traffic for no consumer.
  If a future consumer needs `ThreadRoom` at remote sites, layer it on then.
- **Client-facing UI events.** Nothing consumes a thread-subscription update
  subject today, so we don't introduce one. Adding it is a separate feature.
- **Delete / cleanup events.** Thread subscriptions are never deleted by the
  current code; that path is out of scope.
- **`@all` propagation.** `@all` is already filtered out at the thread level
  (see `markThreadMentions`), so it never produces an outbox event.

## Design

### Event shape

A single new outbox event type:

```
const OutboxThreadSubscriptionUpserted OutboxEventType = "thread_subscription_upserted"
```

`OutboxEvent.Payload` is a JSON-encoded `model.ThreadSubscription`. No new
payload struct — the existing `ThreadSubscription` already carries everything
the destination site needs:

```go
type ThreadSubscription struct {
    ID              string     // home-site-generated UUID; same value lands at remote site
    ParentMessageID string
    RoomID          string
    ThreadRoomID    string
    UserID          string
    UserAccount     string
    SiteID          string     // owner's home site (see Semantic change below)
    LastSeenAt      *time.Time // always nil from message-worker
    HasMention      bool       // true only on mention-marked events
    CreatedAt       time.Time
    UpdatedAt       time.Time
}
```

The destination site of the outbox is the user's home site. One outbox event
per (reply, affected user) tuple.

### Semantic change to `ThreadSubscription.SiteID`

Today `buildThreadSubscription` sets `SiteID = <message event siteID>` — i.e.
the room's home site. For subscriptions to round-trip across sites cleanly, the
field must reflect the **owner's** home site. The room context is already
preserved by `RoomID`. This brings `ThreadSubscription.SiteID` in line with
`Subscription.SiteID` and matches what inbox-worker stores when it consumes the
event.

Callers that don't currently have the owner's siteID in scope (the `markThreadMentions`
loop, parent author lookup) gain a small lookup or piggyback on existing `User`
data — see "Implementation" below.

### Subject + dedup

- Outbox subject: `subject.Outbox(homeSiteID, destSiteID, "thread_subscription_upserted")`
  → `outbox.{homeSite}.to.{destSite}.thread_subscription_upserted`
- `Nats-Msg-Id` seed: `thread-sub-outbox:{threadRoomID}:{userID}:{msg.ID}`.
  `msg.ID` is unique per reply; (msg.ID, userID) is unique within a reply. Stable
  across MESSAGES_CANONICAL redeliveries → JetStream stream-level dedup absorbs
  duplicates within the dedup window.

### Failure semantics

If any outbox publish fails, `processMessage` returns the error → `HandleJetStreamMsg`
NAKs → JetStream redelivers from MESSAGES_CANONICAL. On redelivery:

- `CreateThreadRoom` returns `errThreadRoomExists` → goes to subsequent-reply path.
- `UpsertThreadSubscription` and `MarkThreadSubscriptionMention` are idempotent.
- Outbox publishes carry stable dedup IDs → JetStream filters duplicates.
- Net effect: at-least-once delivery on the wire, exactly-once observable state
  on the destination after dedup.

### Inbox-worker dispatch

`inbox-worker/handler.go` adds a case:

```go
case "thread_subscription_upserted":
    return h.handleThreadSubscriptionUpserted(ctx, &evt)
```

The handler unmarshals `evt.Payload` into a `ThreadSubscription` and calls
`store.UpsertThreadSubscription(ctx, sub)`.

The Mongo implementation:

```
filter:  { threadRoomId: sub.ThreadRoomID, userId: sub.UserID }
update:
  $setOnInsert: { _id, parentMessageId, roomId, threadRoomId, userId,
                  userAccount, siteId, createdAt, lastSeenAt: null }
  $set:         { updatedAt }
  $bit:         { hasMention: { or: <1 if sub.HasMention else 0> } }
opts:    upsert: true
```

The `$bit:or` (or equivalent `$max` on a 0/1 int) makes the merge monotonic:
once `hasMention` flips true at the destination, no later non-mention event can
clear it. `_id` and `createdAt` come from `$setOnInsert` so the first event to
land defines them; later events update only `updatedAt` (and possibly the
mention bit).

`LastSeenAt` is never written by inbox-worker (it's a per-user-action field
owned by whatever path lets a user mark a thread as seen — out of scope here).

### Stream ownership

- **OUTBOX_{siteID}** is owned by ops/IaC entirely. Verified: `room-worker`
  publishes to `outbox.{siteID}.>` today but does **not** bootstrap the stream
  in its `bootstrap.go`; only `MESSAGES_CANONICAL` / `ROOMS` / `INBOX` are
  bootstrapped by their respective owning services. `message-worker` follows
  the same convention — it publishes to OUTBOX subjects but its `bootstrap.go`
  is unchanged with respect to OUTBOX. (If single-site dev needs an OUTBOX
  stream to absorb publishes, that's a pre-existing gap that affects
  room-worker equally and is out of scope for this spec.)
- **MESSAGES_CANONICAL_{siteID}** continues to be bootstrapped by
  `message-worker/bootstrap.go` exactly as today.
- **INBOX_{siteID}** is owned by `inbox-worker` (per CLAUDE.md "Stream
  bootstrap ownership" — INBOX has a single owning service). No change there.

### Implementation outline

#### `pkg/model/event.go`

Add the new constant alongside existing ones:

```go
const (
    OutboxMemberAdded                OutboxEventType = "member_added"
    OutboxMemberRemoved              OutboxEventType = "member_removed"
    OutboxThreadSubscriptionUpserted OutboxEventType = "thread_subscription_upserted"
)
```

Add a model-test case for the new constant in `pkg/model/model_test.go` (round-trip
existing `OutboxEvent` with the new type tag — no new struct to test).

#### `message-worker/handler.go`

`Handler` gains two new fields:

```go
type Handler struct {
    store       Store
    userStore   userstore.UserStore
    threadStore ThreadStore
    siteID      string         // h.siteID — same role as in room-worker
    publish     PublishFunc    // same signature as room-worker
}

type PublishFunc func(ctx context.Context, subj string, data []byte, msgID string) error
```

`NewHandler` updates accordingly. `main.go` wires `cfg.SiteID` and a closure
that calls `js.Publish(ctx, subj, data, jetstream.WithMsgID(msgID))` exactly as
room-worker does.

`buildThreadSubscription` keeps its parameter list but the `siteID` argument
now means "**owner's** site", not the room's. The three callers update:

1. **handleFirstThreadReply** — replier: pass the replier's `User.SiteID` (we
   already have `User` from `processMessage`'s `userStore.FindUserByID`). Parent:
   look up `userStore.FindUserByID(parentSender.ID)` to get parent's siteID.
2. **handleSubsequentThreadReply** — same as above.
3. **markThreadMentions** — mentionees: `mention.Resolve` returns
   `[]Participant`, which today doesn't carry `SiteID`. Verified:
   `mention.Resolve` already calls `LookupFunc` (= `userStore.FindUsersByAccounts`),
   and that store query already projects `siteId`. So the `User` slice inside
   `Resolve` already has the data — we just don't propagate it onto
   `Participant`.

   Add a `SiteID string \`json:"siteId,omitempty" bson:"siteId,omitempty"\``
   field to `model.Participant` and populate it in `mention.Resolve` from
   `users[i].SiteID`. The new field is `omitempty` and additive — existing
   consumers that JSON-encode `Participant` (e.g. on `Message.Mentions`) emit
   the extra field but otherwise behave identically. Update
   `pkg/model/model_test.go` to round-trip `SiteID` on `Participant`.

After every `InsertThreadSubscription` / `UpsertThreadSubscription` /
`MarkThreadSubscriptionMention` succeeds, call:

```go
h.publishThreadSubOutboxIfRemote(ctx, sub, msg.ID)
```

The helper:

```go
func (h *Handler) publishThreadSubOutboxIfRemote(ctx context.Context, sub *model.ThreadSubscription, msgID string) error {
    if sub.SiteID == h.siteID {
        return nil
    }
    payload, err := json.Marshal(sub)
    if err != nil {
        return fmt.Errorf("marshal thread subscription: %w", err)
    }
    outbox := model.OutboxEvent{
        Type:       model.OutboxThreadSubscriptionUpserted,
        SiteID:     h.siteID,
        DestSiteID: sub.SiteID,
        Payload:    payload,
        Timestamp:  time.Now().UTC().UnixMilli(),
    }
    data, err := json.Marshal(outbox)
    if err != nil {
        return fmt.Errorf("marshal outbox event: %w", err)
    }
    dedupID := idgen.DeriveID(fmt.Sprintf("thread-sub-outbox:%s:%s:%s", sub.ThreadRoomID, sub.UserID, msgID))
    if err := h.publish(ctx, subject.Outbox(h.siteID, sub.SiteID, model.OutboxThreadSubscriptionUpserted), data, dedupID); err != nil {
        return fmt.Errorf("publish thread subscription outbox to %s: %w", sub.SiteID, err)
    }
    return nil
}
```

Errors propagate to `processMessage`, which propagates to `HandleJetStreamMsg`,
which NAKs.

#### `message-worker/main.go`

- Inject `cfg.SiteID` and the JetStream publish closure into `NewHandler`. The
  closure is a copy of room-worker's: when `msgID == ""` use core
  `nc.Publish` (none of message-worker's publishes use that branch yet), and
  otherwise use `js.Publish(ctx, subj, data, jetstream.WithMsgID(msgID))`.
- `bootstrap.go` is **unchanged** with respect to OUTBOX (per "Stream
  ownership" — OUTBOX is owned by ops/IaC).

#### `inbox-worker/handler.go`

- Add `UpsertThreadSubscription(ctx context.Context, sub *model.ThreadSubscription) error`
  to the `InboxStore` interface.
- Add a `case "thread_subscription_upserted"` to `HandleEvent` calling
  `handleThreadSubscriptionUpserted`.
- New `handleThreadSubscriptionUpserted(ctx, evt)` unmarshals payload into
  `ThreadSubscription`, calls `store.UpsertThreadSubscription`.

#### `inbox-worker/main.go`

- Add `threadSubCol *mongo.Collection` to `mongoInboxStore`, initialized from
  `db.Collection("threadSubscriptions")` (same collection name message-worker
  uses).
- Implement `UpsertThreadSubscription` with the `$setOnInsert` + `$set` + `$bit:or`
  shape described above. Filter is `{threadRoomId, userId}` to match the
  message-worker's natural key.

### Testing

#### Unit tests

`message-worker/handler_test.go`:

- Extend each happy-path table case to assert `publish` was (or wasn't) called
  with the right subject, payload, and dedup ID. Capture publishes via a
  recorder closure injected in tests instead of mocking `nats.Conn`.
- New cases:
  - First reply, replier remote, parent local → one outbox to replier's site.
  - First reply, replier local, parent remote → one outbox to parent's site,
    with parent siteID resolved via `FindUserByID`.
  - First reply, both remote, different sites → two outboxes, one per site.
  - First reply, both remote, same site → two outboxes to same site (separate
    events, distinct dedup IDs).
  - Subsequent reply variants (Upsert path, same matrix as above).
  - Mention path: mentionee remote → one outbox with `HasMention=true`.
  - Mention path: mentionee local → no outbox.
  - Outbox publish error → returned error from `processMessage`.
  - Parent's `FindUserByID` returns `userstore.ErrUserNotFound` → log warn,
    skip parent subscription + outbox, replier still processed (parallels the
    `errMessageNotFound` branch in `handleFirstThreadReply`).
  - Parent's `FindUserByID` returns a non-NotFound error (DB unreachable etc.)
    → returned error from `processMessage` → NAK.

`inbox-worker/handler_test.go`:

- Dispatch case for `thread_subscription_upserted` calls `UpsertThreadSubscription`
  with the unmarshalled payload.
- Unmarshal error propagated.
- `UpsertThreadSubscription` error propagated.

`inbox-worker/integration_test.go`:

- Two cases:
  - Insert path: empty collection → upsert lands a complete document.
  - Update path with prior `hasMention=true` → second event with
    `hasMention=false` does **not** clear the flag.

#### Coverage targets

Per CLAUDE.md, ≥80% with 90%+ on handler logic. The added paths are tightly
scoped — branch coverage for the local-vs-remote split, the three publish call
sites, and the `hasMention` OR-merge.

### Migration / rollout

- The new outbox type is additive; old inbox-worker deployments will hit the
  `default: slog.Warn("unknown event type, skipping")` branch and ack the
  message. To avoid silently dropping events during a rolling upgrade, deploy
  inbox-worker first, then message-worker. (The OUTBOX stream's MaxAge
  determines how long unhandled events persist; if it's bounded short, the
  ordering matters less, but the deploy order is still the safe default.)
- No data migration: existing `thread_subscriptions` rows are unaffected.

### Risks

- **`SiteID` on `model.Participant` is a wire-format addition.** It's
  `omitempty` and additive — existing JSON consumers decode unchanged, and the
  one path that reads it (this spec's `markThreadMentions`) always sees fresh
  data because `processMessage` re-runs `mention.Resolve` on every delivery,
  re-populating `Mentions` from the live userstore. Old persisted `Mentions`
  arrays in Cassandra are never re-emitted as outbox events.
- **Defensive guard against empty `SiteID`.** The publish helper must skip +
  log warn if `sub.SiteID == ""`. This prevents an upstream bug (e.g., a future
  caller forgetting to set the field) from emitting an outbox to a `dest=""`
  subject. The empty case is otherwise unreachable in the paths added here.
- **Parent user lookup may fail.** `GetMessageSender` already handles
  `errMessageNotFound` for the parent **message**. Adding
  `userStore.FindUserByID(parentSender.ID)` introduces a new "parent user
  not found" branch — treat it the same as the parent-message-not-found case:
  log warn and skip the parent subscription (and its outbox) but still process
  the replier. This avoids hard-failing a thread reply when the parent author's
  user record has been removed.
- **Cross-site siteID coverage in tests.** The `User` test fixtures in
  `handler_test.go` all set `SiteID = "site-a"`. Add fixtures with differing
  `SiteID` values to exercise both branches of the local-vs-remote check.
