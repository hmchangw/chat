# `message.thread.read` RPC — Design

**Status:** Approved for planning
**Date:** 2026-05-20
**Service:** `room-service` (with cross-site sync via `inbox-worker`)

## 1. Goal

Add an RPC that lets a client mark a single thread as read for a given user. The handler must:

1. Validate the user has access to the room (room-level `Subscription` exists).
2. Validate the user has a `ThreadSubscription` for the supplied `threadId` (the thread's `ParentMessageID`).
3. Remove the `threadId` from `Subscription.ThreadUnread` and recompute `Subscription.Alert`.
4. Update the `ThreadSubscription`: set `LastSeenAt`, `UpdatedAt`, clear `HasMention`.
5. Perform the two updates above concurrently.
6. If the user's home site differs from the handler's site, publish a federated outbox event so the user's home-site `inbox-worker` can mirror both updates.

The room's home site is the source of truth for `Subscription` and `ThreadSubscription`; the user's home site is a cache kept in sync via the outbox/inbox pattern.

## 2. Wire contract

### 2.1 NATS subject

| Concrete | Wildcard |
| --- | --- |
| `chat.user.{account}.request.room.{roomID}.{siteID}.message.thread.read` | `chat.user.*.request.room.*.{siteID}.message.thread.read` |

The handler runs in `room-service` and is queue-subscribed under the existing `room-service` queue group. `{siteID}` in the subject is the room's home site (consistent with `message.read` and `member.role-update`).

New builders in `pkg/subject/subject.go`:

```go
func MessageThreadRead(account, roomID, siteID string) string
func MessageThreadReadWildcard(siteID string) string
```

`subject.ParseUserRoomSubject` already extracts `account` and `roomID` from this subject shape — it walks tokens looking for `room` regardless of what follows. No new parser is required.

### 2.2 Request body

In `pkg/model/subscription.go`:

```go
type MessageThreadReadRequest struct {
    ThreadID string `json:"threadId"`
}
```

The subject already carries `account` and `roomID`. Empty/whitespace `ThreadID` is a hard error (`errInvalidThreadID`).

### 2.3 Response body

```json
{"status": "accepted"}
```

Matches the convention used by sibling RPCs (`message.read`, `member.add`, `member.remove`).

## 3. Handler logic

`Handler.handleMessageThreadRead(ctx, subj, data) ([]byte, error)` in `room-service/handler.go`. NATS wrapper `natsMessageThreadRead(m otelnats.Msg)` follows the established pattern (`wrappedCtx`, `natsutil.ReplyError(m.Msg, sanitizeError(err))` on failure, `m.Msg.Respond(resp)` on success).

Registration line in `Handler.RegisterCRUD`:

```go
if _, err := nc.QueueSubscribe(subject.MessageThreadReadWildcard(h.siteID), queue, h.natsMessageThreadRead); err != nil {
    return fmt.Errorf("subscribe message thread read: %w", err)
}
```

Flow:

1. **Parse subject** via `subject.ParseUserRoomSubject(subj)` → `account`, `roomID`. `!ok` → `fmt.Errorf("invalid message-thread-read subject: %s", subj)`.
2. **Unmarshal** request body into `MessageThreadReadRequest`. Empty `req.ThreadID` → `errInvalidThreadID`.
3. **Room-access check:** `sub, err := h.store.GetSubscription(ctx, account, roomID)`.
   - `errors.Is(err, model.ErrSubscriptionNotFound)` → `errNotRoomMember` (existing sentinel).
   - other error → wrap.
4. **Thread-sub existence check:** `tsub, err := h.store.GetThreadSubscriptionByParent(ctx, account, req.ThreadID)`.
   - `errors.Is(err, model.ErrThreadSubscriptionNotFound)` → `errThreadSubNotFound`.
   - other error → wrap.
5. **Compute new state:**
   - `newThreadUnread` is `sub.ThreadUnread` with `req.ThreadID` removed (idempotent — absence is allowed).
   - `newAlert := sub.Alert && len(newThreadUnread) > 0` (mirrors `handleMessageRead`'s formula — a thread-read can only clear an alert, never set one).
   - `now := time.Now().UTC()`.
6. **Concurrent writes** via `errgroup.WithContext(ctx)`:
   - Goroutine A: `store.UpdateSubscriptionThreadRead(ctx, roomID, account, newThreadUnread, newAlert)`.
   - Goroutine B: `store.UpdateThreadSubscriptionRead(ctx, tsub.ThreadRoomID, account, now)` (sets `lastSeenAt=now`, `updatedAt=now`, `hasMention=false`).
   - `g.Wait()` — first error wins, wrapped.
7. **Cross-site outbox** (only when needed):
   - `userSiteID, err := store.GetUserSiteID(ctx, account)`. Wrap on error.
   - `userSiteID == ""` → `slog.Warn` and skip publish — local writes have already succeeded.
   - `userSiteID != "" && userSiteID != h.siteID`:
     - Build `model.ThreadReadEvent{Account, RoomID, ThreadRoomID: tsub.ThreadRoomID, ParentMessageID: req.ThreadID, NewThreadUnread: newThreadUnread, Alert: newAlert, LastSeenAt: now.UnixMilli(), Timestamp: now.UnixMilli()}`.
     - Wrap in `model.OutboxEvent{Type: model.OutboxThreadRead, SiteID: h.siteID, DestSiteID: userSiteID, Payload, Timestamp: now.UnixMilli()}`.
     - Publish to `subject.Outbox(h.siteID, userSiteID, model.OutboxThreadRead)` via `h.publishToStream`. Errors wrap.
8. **Return** `{"status":"accepted"}`.

### 3.1 Step ordering rationale

- Local writes go before the outbox so a publish failure cannot leave the local state stale and the federation gap is what gets reported.
- The two local writes are independent (different collections, different documents) — `errgroup` runs them in parallel per your spec.
- No room-floor recompute: `Room.MinUserLastSeenAt` is driven by `Subscription.LastSeenAt`, which thread reads do not touch.

### 3.2 Error sanitization

The wrapper invokes `natsutil.ReplyError(m.Msg, sanitizeError(err))`. New sentinels (`errInvalidThreadID`, `errThreadSubNotFound`) are added to the existing `sanitizeError` allow-list so they pass through to the client; everything else is sanitized to a generic internal-error string.

## 4. Model changes

### 4.1 New request type in `pkg/model/subscription.go`

```go
type MessageThreadReadRequest struct {
    ThreadID string `json:"threadId"`
}
```

### 4.2 New sentinel in `pkg/model/threadsubscription.go`

```go
var ErrThreadSubscriptionNotFound = errors.New("thread subscription not found")
```

### 4.3 New outbox event type and payload in `pkg/model/event.go`

```go
const OutboxThreadRead OutboxEventType = "thread_read"

// ThreadReadEvent is the OutboxEvent.Payload for type "thread_read".
// Sent from the room's home site to the user's home site when a user
// marks a thread as read. The source site computes the authoritative
// result (NewThreadUnread, Alert); the destination applies values
// directly rather than re-deriving. LastSeenAt is UnixMilli (UTC);
// Timestamp is the publish time (UnixMilli, UTC).
type ThreadReadEvent struct {
    Account         string   `json:"account"`
    RoomID          string   `json:"roomId"`
    ThreadRoomID    string   `json:"threadRoomId"`
    ParentMessageID string   `json:"parentMessageId"`
    NewThreadUnread []string `json:"newThreadUnread"`
    Alert           bool     `json:"alert"`
    LastSeenAt      int64    `json:"lastSeenAt"`
    Timestamp       int64    `json:"timestamp"`
}
```

`int64` UnixMilli is used on the wire for cross-site safety (matches `SubscriptionReadEvent`, `MemberAddEvent.JoinedAt`). Local Mongo writes still use `time.Time`.

### 4.4 Round-trip tests

Add `roundTrip` cases in `pkg/model/model_test.go` for `MessageThreadReadRequest` and `ThreadReadEvent`, plus a full `OutboxEvent` wrap/unwrap test (`TestOutboxEventJSON_ThreadRead`) following the existing `TestOutboxEventJSON_ThreadSubscriptionUpserted` pattern.

### 4.5 New room-service sentinels (`room-service/handler.go`)

```go
var (
    errInvalidThreadID   = errors.New("threadId is required")
    errThreadSubNotFound = errors.New("thread subscription not found")
)
```

Both added to `sanitizeError`'s allow-list.

## 5. Room-service store changes

### 5.1 New `RoomStore` methods (`room-service/store.go`)

```go
// GetThreadSubscriptionByParent looks up the user's ThreadSubscription
// by (parentMessageID, account). Returns model.ErrThreadSubscriptionNotFound
// (wrapped) when no document matches.
GetThreadSubscriptionByParent(ctx context.Context, account, parentMessageID string) (*model.ThreadSubscription, error)

// UpdateSubscriptionThreadRead overwrites threadUnread and alert on the
// subscription keyed by (roomID, account). When threadUnread is empty,
// the field is removed via $unset so JSON round-trip matches the
// omitempty contract documented in pkg/model. Returns
// model.ErrSubscriptionNotFound (wrapped) when no subscription matches.
UpdateSubscriptionThreadRead(ctx context.Context, roomID, account string, threadUnread []string, alert bool) error

// UpdateThreadSubscriptionRead sets lastSeenAt, updatedAt and
// hasMention=false on the ThreadSubscription keyed by
// (threadRoomID, userAccount). Returns
// model.ErrThreadSubscriptionNotFound (wrapped) when no document matches.
UpdateThreadSubscriptionRead(ctx context.Context, threadRoomID, account string, lastSeenAt time.Time) error
```

`GetUserSiteID` already exists on `RoomStore` (added by the `message.read` work) and is reused as-is.

### 5.2 Mongo implementations (`room-service/store_mongo.go`)

Room-service does not currently hold a handle to the `thread_subscriptions` collection. Add it to `roomStoreMongo` alongside the existing `subscriptions`, `rooms`, `users` handles, and wire it in the constructor — mirror what `inbox-worker` and `message-worker` already do.

- **`GetThreadSubscriptionByParent`** — `s.threadSubscriptions.FindOne(ctx, bson.M{"parentMessageId": parentMessageID, "userAccount": account}).Decode(...)`. On `mongo.ErrNoDocuments` return wrapped `model.ErrThreadSubscriptionNotFound`.
- **`UpdateSubscriptionThreadRead`** — filter `bson.M{"roomId": roomID, "u.account": account}`. When `len(threadUnread) == 0`: update is `bson.M{"$set": bson.M{"alert": alert}, "$unset": bson.M{"threadUnread": ""}}` so the field is removed and round-trips to `nil`. When non-empty: `bson.M{"$set": bson.M{"threadUnread": threadUnread, "alert": alert}}`. `MatchedCount == 0` → wrapped `ErrSubscriptionNotFound`.
- **`UpdateThreadSubscriptionRead`** — filter `bson.M{"threadRoomId": threadRoomID, "userAccount": account}`; update `bson.M{"$set": bson.M{"lastSeenAt": lastSeenAt, "updatedAt": lastSeenAt, "hasMention": false}}`. `MatchedCount == 0` → wrapped `ErrThreadSubscriptionNotFound`.

No `$lt` order-safety guard on the source-site writes — `time.Now()` is monotonically increasing within a process. The guard exists only on the inbox-worker side where federation can deliver out-of-order.

### 5.3 Indexes

- `subscriptions(roomId, u.account)` unique — already exists; covers both the access check and the thread-unread overwrite.
- `thread_subscriptions(threadRoomId, userAccount)` unique — already exists; covers `UpdateThreadSubscriptionRead`.
- `thread_subscriptions(parentMessageId, userAccount)` — **NEW** non-unique compound index needed for `GetThreadSubscriptionByParent`. Created in room-service's `EnsureIndexes` routine. Background build on a populated collection is non-blocking.

### 5.4 Mocks

`make generate SERVICE=room-service` regenerates `mock_store_test.go`.

## 6. Inbox-worker integration

### 6.1 Event routing (`inbox-worker/handler.go`)

Extend `HandleEvent`:

```go
case "thread_read":
    return h.handleThreadRead(ctx, &evt)
```

New handler:

```go
func (h *Handler) handleThreadRead(ctx context.Context, evt *model.OutboxEvent) error {
    var e model.ThreadReadEvent
    if err := json.Unmarshal(evt.Payload, &e); err != nil {
        return fmt.Errorf("unmarshal thread_read payload: %w", err)
    }
    lastSeenAt := time.UnixMilli(e.LastSeenAt).UTC()
    if err := h.store.ApplyThreadRead(ctx, e.RoomID, e.ThreadRoomID, e.Account, e.NewThreadUnread, e.Alert, lastSeenAt); err != nil {
        return fmt.Errorf("apply thread read (room %q, parent %q, account %q): %w",
            e.RoomID, e.ParentMessageID, e.Account, err)
    }
    return nil
}
```

### 6.2 New method on `InboxStore`

```go
// ApplyThreadRead mirrors a remote site's thread-read on the local cache.
// Subscription: authoritative overwrite of threadUnread + alert (idempotent
// under repeated delivery — the source ships the resulting state). When
// newThreadUnread is empty, threadUnread is $unset to match the omitempty
// contract. ThreadSubscription: sets lastSeenAt, updatedAt, hasMention=false,
// guarded by $lt lastSeenAt so out-of-order deliveries cannot regress.
// Missing documents on either side are silent no-ops (replays may arrive
// before the corresponding member_added / thread_subscription_upserted has
// been processed).
ApplyThreadRead(ctx context.Context, roomID, threadRoomID, account string, newThreadUnread []string, alert bool, lastSeenAt time.Time) error
```

### 6.3 Mongo implementation (`inbox-worker/store_mongo.go`)

Two updates, not transactional — each idempotent, both silent on miss.

**Subscription write:**
- Filter: `bson.M{"roomId": roomID, "u.account": account}`.
- When `len(newThreadUnread) == 0`: `bson.M{"$set": bson.M{"alert": alert}, "$unset": bson.M{"threadUnread": ""}}`.
- Otherwise: `bson.M{"$set": bson.M{"threadUnread": newThreadUnread, "alert": alert}}`.
- `MatchedCount == 0` is a silent no-op.

**ThreadSubscription write (order-safety guarded):**
```go
tsFilter := bson.M{
    "threadRoomId": threadRoomID,
    "userAccount":  account,
    "$or": bson.A{
        bson.M{"lastSeenAt": nil},
        bson.M{"lastSeenAt": bson.M{"$lt": lastSeenAt}},
    },
}
tsUpdate := bson.M{"$set": bson.M{
    "lastSeenAt": lastSeenAt,
    "updatedAt":  lastSeenAt,
    "hasMention": false,
}}
```

**Why no `$pull` on the destination:** the source already computed `NewThreadUnread` ("source of truth = room's origin site"). The destination just mirrors.

**Why a guard on ThreadSubscription but not Subscription:** ThreadSubscription has a single `lastSeenAt` per row that's a natural monotonic gate. The Subscription update overwrites two array/bool fields that don't have such a gate; idempotency comes from authoritative shipping, and out-of-order delivery converges on the last event sent (acceptable cache drift on a non-authoritative replica).

### 6.4 Indexes

All required indexes already exist on the inbox-worker side:
- `subscriptions(roomId, u.account)` unique — for the sub overwrite.
- `thread_subscriptions(threadRoomId, userAccount)` unique — for the thread-sub guarded update.

No new inbox-worker index is required.

### 6.5 Mocks

`make generate SERVICE=inbox-worker` regenerates `mock_store_test.go`.

## 7. Testing

### 7.1 TDD

Per `CLAUDE.md`, all new code follows Red → Green → Refactor. Write tests first, confirm they fail, then implement.

### 7.2 Subject tests (`pkg/subject/subject_test.go`)

1. `TestMessageThreadRead` — concrete subject shape.
2. `TestMessageThreadReadWildcard` — wildcard shape.
3. `TestMessageThreadRead_ParseUserRoomSubject` — confirms the existing parser extracts `account` + `roomID` from the new subject.

### 7.3 Model tests (`pkg/model/model_test.go`)

1. `roundTrip` for `MessageThreadReadRequest`.
2. `roundTrip` for `ThreadReadEvent` — assert `int64` UnixMilli fields and JSON tags.
3. `TestOutboxEventJSON_ThreadRead` — full `OutboxEvent` wrap → unwrap with `ThreadReadEvent` payload.

### 7.4 Room-service unit tests (`room-service/handler_test.go`)

Mock `RoomStore`; capture `publishToStream`. Table-driven cases for `handleMessageThreadRead`:

1. Invalid subject → error, no store calls.
2. Empty `threadId` → `errInvalidThreadID`, no store calls.
3. Malformed JSON body → unmarshal error, no store calls.
4. Not a room member (`GetSubscription` → `ErrSubscriptionNotFound`) → `errNotRoomMember`. No thread-sub lookup.
5. Thread subscription missing (`GetThreadSubscriptionByParent` → `ErrThreadSubscriptionNotFound`) → `errThreadSubNotFound`. No writes.
6. Happy path, alert clears — `Subscription.ThreadUnread = ["t1"]`, `Alert = true`; request clears `"t1"` → store called with `threadUnread = []`, `alert = false`. Both writes invoked. Local user → no outbox.
7. Happy path, alert stays — `ThreadUnread = ["t1","t2"]`, request clears `"t1"` → `threadUnread = ["t2"]`, `alert = true`. No outbox.
8. Idempotent — threadID not in array (`ThreadUnread = ["t2"]`, request clears `"t1"`) → `threadUnread = ["t2"]`, `alert = old.Alert && len > 0`. Both writes still invoked. No outbox.
9. Alert already false (`Alert = false`, `ThreadUnread = ["t1"]`, clear `"t1"`) → `newAlert = false`.
10. Cross-site user (`GetUserSiteID == "site-b"`, handler at `"site-a"`) → outbox publish; assert subject `outbox.site-a.to.site-b.thread_read`; decode payload and assert every field including `NewThreadUnread` and `Alert`.
11. Same-site user (`GetUserSiteID == h.siteID`) → no outbox.
12. `GetUserSiteID` returns `("", nil)` — `slog.Warn`, no outbox, no error.
13. `GetUserSiteID` returns error → wrapped error after local writes succeeded.
14. Cross-site outbox publish failure → handler returns wrapped error.
15. `UpdateSubscriptionThreadRead` error → wrapped (errgroup short-circuit).
16. `UpdateThreadSubscriptionRead` error → wrapped (errgroup short-circuit).

### 7.5 Room-service integration tests (`room-service/integration_test.go`)

- `GetThreadSubscriptionByParent` — hit returns the document; miss returns wrapped `ErrThreadSubscriptionNotFound`.
- `UpdateSubscriptionThreadRead`:
  - Non-empty array path: both fields written.
  - Empty array path: `threadUnread` field absent in BSON (matches the existing `omitempty` round-trip test).
  - Missing sub returns wrapped `ErrSubscriptionNotFound`.
- `UpdateThreadSubscriptionRead`: writes `lastSeenAt`, `updatedAt`, `hasMention=false`; missing thread sub returns wrapped `ErrThreadSubscriptionNotFound`.
- Index `(parentMessageId, userAccount)` is present on `thread_subscriptions` after store init.

### 7.6 Inbox-worker unit tests (`inbox-worker/handler_test.go`)

1. Happy path: `thread_read` outbox event → `ApplyThreadRead` called with correctly converted `time.UnixMilli(...).UTC()`, `NewThreadUnread`, `Alert`.
2. Malformed inner JSON → wrapped error.
3. Store error → wrapped error.
4. Existing "unknown type → skip" test stays green (no regression).

### 7.7 Inbox-worker integration tests (`inbox-worker/integration_test.go`)

- Happy path: seed Subscription + ThreadSubscription; apply event → both updated. Subscription's `threadUnread` overwritten to event's `NewThreadUnread`, `alert` set; ThreadSubscription gets new `lastSeenAt`, `updatedAt`, `hasMention=false`.
- Empty `NewThreadUnread`: the Subscription document has `threadUnread` field removed (verified by raw BSON read).
- Out-of-order ThreadSubscription: seed `lastSeenAt = T2`; apply event with `LastSeenAt = T1 < T2`; ThreadSubscription unchanged. **Subscription still overwrites** (no guard) — asserted explicitly so the looser semantic is documented.
- Equal `lastSeenAt`: idempotent replay → ThreadSubscription unchanged (guard is `$lt`).
- Missing Subscription: no error; ThreadSubscription still updated.
- Missing ThreadSubscription: no error; Subscription still updated.
- Both missing: no error; both no-op.

### 7.8 Coverage

Project minimum 80%; handler and store paths target ≥90% per `CLAUDE.md`.

## 8. Client-API docs

Per `CLAUDE.md` Section 5, the same PR must update `docs/client-api.md` adding a "Mark Thread as Read" subsection under the existing "Mark Messages Read" entry. It documents:

- Subject (concrete + reply).
- Request body (`{ "threadId": "..." }`).
- Success reply (`{ "status": "accepted" }`).
- Error cases (`only room members can list members`, `thread subscription not found`, `threadId is required`, invalid subject).
- Behaviour notes: alert recomputation formula, cross-site federation via `outbox.{handlerSite}.to.{userSite}.thread_read`, concurrent local writes.

## 9. Out of scope

- No system message ("X read up to here") — thread reads are silent.
- No room-floor recompute (`Room.MinUserLastSeenAt`) — that's a per-room aggregate driven by `Subscription.LastSeenAt`; thread reads do not touch it.
- No inverse "mark as unread" RPC.
- No batch thread-read (one thread per request).
- No write-back from inbox-worker to other sites — the user's home site is a cache, not authoritative.
- No update to `Subscription.LastSeenAt` — that's room-level and owned by `message.read`.

## 10. Migration / rollout

- New subject — no existing client conflicts.
- New outbox event type `thread_read` — older `inbox-worker` deployments hit the existing `default` arm and `slog.Warn("unknown event type, skipping")`. No crash, no poison-pill. Roll out `inbox-worker` before `room-service` to avoid the warning window.
- New non-unique index `(parentMessageId, userAccount)` on `thread_subscriptions` — created at room-service startup via `EnsureIndexes`. Background build on a populated collection is non-blocking.
- No schema migration on existing collections.
