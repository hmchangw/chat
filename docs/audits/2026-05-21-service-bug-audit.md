# Service Bug Audit — 2026-05-21

**Scope:** All 12 services in the monorepo + `pkg/` shared code. Categories: correctness & data integrity, concurrency & resource leaks, error handling & NATS patterns. Security findings included opportunistically (not the primary lens).

**Method:** Read each service's `main.go`, `handler.go`, `store*.go`, and helpers; trace high-risk paths cross-file. No fixes were applied — this is a triage report.

**Branch:** `claude/identify-service-bugs-5blZP`. No code changes in this commit.

---

## Top-of-doc summary

| # | Severity | Service | Category | Finding |
|---|----------|---------|----------|---------|
| 1 | **High** | auth-service | Security/Authorization | `chat.room.>` subscribe permission granted to every authenticated user — any user can intercept channel room events for rooms they aren't a member of. Downgraded from Critical: room-key encryption clears the message body when `ENCRYPTION_ENABLED=true`, but envelope metadata (room name, mentions, traffic timing) still leaks |
| 2 | **High** | broadcast-worker | Correctness | `UpdateRoomLastMessage` is an unconditional `$set` with no timestamp guard — concurrent processing or redelivery can leave the room pointing at an older message |
| 3 | **High** | inbox-worker | Correctness | `UpsertRoom` `$sets` the entire federated Room document — clobbers locally-owned fields (lastMsgAt, lastMsgId, userCount, appCount, minUserLastSeenAt) every time a `room_sync` arrives |
| 4 | **High** | history-service | NATS / Correctness | Canonical `.updated` / `.deleted` events published via core NATS without `Nats-Msg-Id` — no JetStream dedup, so client edit retry produces duplicate fan-out events to every recipient |
| 5 | **High** | notification-worker | Correctness | No filter on `evt.Event` — handler emits a `new_message` notification for every event including `updated`/`deleted`/system messages, so deletes/edits produce phantom "new message" notifications |
| 6 | **High** | broadcast-worker, room-worker, notification-worker | Correctness | Per-recipient publish errors swallowed via `slog.Error(...); continue` then handler returns nil — partial fan-out failures get acked, JetStream never retries, recipients permanently miss the event |
| 7 | **High** | room-worker | Correctness | `fanOutKey` logs per-account `keySender.Send` failures but never returns an error — if NATS publish fails for some members, those members permanently miss the room key until the next rotation |
| 8 | **Medium** | inbox-worker | Correctness | `handleMemberAdded` silently drops accounts whose user doc doesn't yet exist locally (`slog.Warn + continue`) — a race between user-replication and member-add federation drops members permanently with no retry |
| 9 | **Medium** | inbox-worker, message-gatekeeper, broadcast-worker, message-worker, notification-worker | NATS | All `msg.Nak()` calls fire on any handler error (including unrecoverable ones like `json.Unmarshal` failure) — tight redelivery loops on poison messages until `MaxDeliver` then DLQ; should split permanent vs transient like `room-worker` does |
| 10 | **Medium** | inbox-worker | Correctness | `role_updated` event with empty `roles` returns a regular error → Nak → redelivers forever (same payload, will never succeed) — should be `permanent` |
| 11 | **Medium** | message-worker | NATS / Correctness | A real user missing in `userStore.FindUserByID` returns error → Nak → loops forever; hard-deleted users block thread/message persistence indefinitely |
| 12 | **Medium** | room-worker | Concurrency | Sync DM endpoint (`natsServerCreateDM`) registered via `nc.QueueSubscribe` — nats.go spawns a goroutine per delivered message with no concurrency cap; a burst can spawn 10k+ goroutines and exhaust resources |
| 13 | **Medium** | broadcast-worker | Correctness | `SetSubscriptionMentions` only fires when `len(resolved.Accounts) > 0` — an `@all`-only message updates `lastMentionAllAt` on the room but no per-subscription `hasMention=true`, so the per-user mention badge stays cold |
| 14 | **Medium** | room-service | Authorization / DoS | `natsListRooms` calls `store.ListRooms(ctx)` with no pagination and no per-user filter — every list-rooms request loads every room in Mongo; large deployments exposed to DoS and over-broad data return |
| 15 | **Medium** | room-service | Security | `natsListRooms` and `natsGetRoom` reply with raw `err.Error()` via `natsutil.ReplyError(m.Msg, err.Error())` — every other handler routes through `sanitizeError(err)` first. Internal error strings leak to clients |
| 16 | **Medium** | inbox-worker | Error handling | `mongoInboxStore.CreateSubscription` and `UpsertRoom` `return err` bare with no `fmt.Errorf` wrap — violates the repo-wide CLAUDE.md error-wrapping rule and makes incident logs unreadable |
| 17 | **Low** | message-gatekeeper | Observability | Invalid-subject path acks the message silently (only `slog.Warn`) — no metric to alert on a bad publisher; same in inbox-worker for unknown event types |
| 18 | **Low** | notification-worker | Style | Filter on `ListSubscriptions` uses `map[string]string` instead of `bson.M`; bare `return nil, err` in store; no impact but inconsistent with the rest of the codebase |
| 19 | **Low** | auth-service | Hardening | `http.Server` sets `ReadTimeout`/`WriteTimeout` but not `ReadHeaderTimeout` or `IdleTimeout` — small DoS surface on a public-facing endpoint |
| 20 | **Low** | doc-drift | Docs | `CLAUDE.md` says `MESSAGE_BUCKET_HOURS` defaults to 24; actual default is 72 in both `message-worker` and `history-service`. Risk: a new service copied from CLAUDE.md text would split-brain Cassandra partitions |

---

## 1. auth-service

### 1.1 `chat.room.>` subscribe permission granted to every authenticated user — **High** (downgraded from Critical)

`auth-service/handler.go:171-177` builds the NATS user JWT with these subscribe grants:

```go
uc.Sub.Allow.Add(fmt.Sprintf("chat.user.%s.>", account))
uc.Sub.Allow.Add("chat.room.>")
uc.Sub.Allow.Add("_INBOX.>")
```

`pkg/subject/subject.go:164`:

```go
func RoomEvent(roomID string) string {
    return fmt.Sprintf("chat.room.%s.event", roomID)
}
```

`broadcast-worker/handler.go:178` publishes every channel-room message there:

```go
return h.pub.Publish(ctx, subject.RoomEvent(meta.ID), payload)
```

**Impact.** Any user with a valid SSO token can `nc.Subscribe("chat.room.>")` and receive every channel-room event broadcast on the NATS supercluster, regardless of subscription/membership. The application layer enforces membership for *posting* and for the `history-service` read path, but the live broadcast subject is not subject-ACL'd per room.

**Mitigation in place (basis for downgrade from Critical to High).** When `ENCRYPTION_ENABLED=true` on broadcast-worker (`broadcast-worker/main.go:27-28`, `envDefault:"false"`), `publishChannelEvent` (`handler.go:245-268`) encrypts the message body with the room key and clears `evt.Message`, so an eavesdropper can't read the plaintext. Same for edits in `encryptEditedContent` (`handler.go:207-223`), which clears `edited.NewContent`.

**What still leaks even with encryption on.** The `RoomEvent` envelope built by `buildRoomEvent` (`handler.go:307-320`) and the mutation payloads (`MessageEditedPayload`, `MessageDeletedPayload`) carry these fields in cleartext:

- `RoomID`, `SiteID`, `RoomType`, `UserCount` — full room enumeration and member-count inference
- `RoomName` (`meta.Name`) — often as sensitive as content
- `Timestamp`, `LastMsgAt`, `LastMsgID` — traffic analysis: which rooms are active, when, how often
- `Mentions []Participant` (`handler.go:242`) and `MentionAll` — list of accounts being @-mentioned, set even when the body is encrypted
- For `.edited`/`.deleted` events: `MessageID`, `EditedBy`/`DeletedBy` (the user account), `EditedAt`/`DeletedAt`/`UpdatedAt`

DMs route through `chat.user.{account}.event.room` (subject 168) so are not exposed; only channel-room events are.

**Conditions that re-elevate to Critical.**

- `ENCRYPTION_ENABLED=false` anywhere in production: full plaintext disclosure resumes.
- An attacker who only needs metadata (room directory, mention graph, traffic timing) gets it regardless of the encryption setting.

**How to trigger.** Authenticate as any user, then `Subscribe("chat.room.>")`. You will receive `chat.room.<any_room_id>.event` payloads for every channel-room on the supercluster.

**Recommendation.** Either (a) move per-room delivery onto a user-scoped subject (`chat.user.{account}.event.room.<roomID>`) so the existing `chat.user.{account}.>` grant is sufficient, or (b) move to per-room JWT scoping driven by subscriptions (issue/renew JWTs on subscribe/unsubscribe). Option (a) is simpler and aligns with how DMs already work. Encryption is not a substitute — it doesn't cover envelope metadata and depends on a config flag that defaults off.

### 1.2 `http.Server` missing `ReadHeaderTimeout` / `IdleTimeout` — **Low**

`auth-service/main.go:85-90`. Slow-header DoS / slowloris-style attacks against a public-facing auth endpoint. Add `ReadHeaderTimeout: 5*time.Second` and `IdleTimeout: 60*time.Second` (see `search-service/main.go:183-189` for the right shape).

---

## 2. message-gatekeeper

### 2.1 Subscription lookup not scoped by `siteID` — **Low**

`message-gatekeeper/store_mongo.go:29`:

```go
filter := bson.M{"u.account": account, "roomId": roomID}
```

CLAUDE.md says subscriptions are scoped by `siteID`. The handler validates the subject's `siteID` against `h.siteID`, and each site has its own Mongo, so this is defensive-in-depth missing rather than a live bug. If sites ever share a database, this leaks subscriptions across sites.

### 2.2 Invalid-subject path silently acks — **Low**

`message-gatekeeper/handler.go:67-74`: poison messages are dropped with `slog.Warn`. No metric, no DLQ stash. An operator can't tell a misconfigured publisher from a healthy stream.

---

## 3. broadcast-worker

### 3.1 `UpdateRoomLastMessage` clobbers without a timestamp guard — **High**

`broadcast-worker/store_mongo.go:51-71`:

```go
fields := bson.M{
    "lastMsgAt": msgAt,
    "lastMsgId": msgID,
    "updatedAt": msgAt,
}
// ...
update := bson.M{"$set": fields}
res, err := m.roomCol.UpdateOne(ctx, filter, update)
```

Combined with `MaxWorkers=100` concurrent processing (`main.go:142`) and JetStream redelivery, two messages for the same room can race:

1. Msg A (createdAt=t1) handled, `UpdateRoomLastMessage(A, t1)` succeeds.
2. Worker crashes before publishing the broadcast for A.
3. Msg B (createdAt=t2 > t1) handled on a different worker, `UpdateRoomLastMessage(B, t2)` succeeds; broadcasts.
4. Msg A redelivered, `UpdateRoomLastMessage(A, t1)` runs again — overwrites `lastMsgAt`/`lastMsgId` with the older message.

End state: room's "last message" is A even though B is the actual latest.

**Fix.** Use a conditional update — either guard `lastMsgAt: bson.M{"$lt": msgAt}` in the filter, or `$max` operators on `lastMsgAt`/`lastMsgId`. `lastMentionAllAt` (line 58) has the same issue and the same fix.

### 3.2 DM/mutation fan-out errors are swallowed — **High**

`broadcast-worker/handler.go:189-198` (mutation events) and `handler.go:300-302` (DM new messages):

```go
if err := h.pub.Publish(ctx, subject.UserRoomEvent(account), payload); err != nil {
    slog.Error("publish DM event failed", "error", err, "account", subs[i].User.Account)
}
```

Loop continues, function returns `nil`, source message is acked. Any recipient whose publish failed gets no event, JetStream never redelivers (already acked). NATS publish is rare-but-not-zero on connection blips during redeploys.

**Fix.** Track `firstErr` across the loop; return it. JetStream redelivery is idempotent for receive-side broadcasts (NATS PublishMsg is fire-and-forget; downstream sees one or two copies, acceptable).

### 3.3 `@all` doesn't propagate to per-subscription `hasMention` — **Medium**

`broadcast-worker/handler.go:96-100`:

```go
if len(resolved.Accounts) > 0 {
    if err := h.store.SetSubscriptionMentions(ctx, meta.ID, resolved.Accounts); err != nil {
        return fmt.Errorf("set subscription mentions: %w", err)
    }
}
```

An `@all`-only message has `resolved.Accounts = []` (because `mention.ResolveFromParsed` separates `MentionAll` from `Accounts`). `lastMentionAllAt` is set on the room (via `UpdateRoomLastMessage` arg 4), but no `subscription.hasMention = true` write happens. If clients display the "@" badge from the subscription's `hasMention`, `@all` doesn't trigger it. Verify product intent before fixing.

---

## 4. inbox-worker

### 4.1 `UpsertRoom` `$sets` the entire federated room doc — **High**

`inbox-worker/main.go:52-58`:

```go
func (s *mongoInboxStore) UpsertRoom(ctx context.Context, room *model.Room) error {
    filter := bson.M{"_id": room.ID}
    update := bson.M{"$set": room}
    opts := options.UpdateOne().SetUpsert(true)
    _, err := s.roomCol.UpdateOne(ctx, filter, update, opts)
    return err
}
```

The federation `room_sync` payload carries the home-site snapshot of the Room. Locally-derived fields (`lastMsgAt`, `lastMsgId`, `userCount`, `appCount`, `updatedAt`, `minUserLastSeenAt`, `lastMentionAllAt`) are also on `model.Room`, so this update overwrites them with whatever the publisher had at snapshot time — often stale or zero.

**Impact.** Any cross-site `room_sync` (e.g. a room name edit) silently regresses every locally-maintained pointer for that room on every other site. Clients see "no messages" on rooms that had hundreds, until the next message arrives.

**Fix.** Either (a) project only metadata fields the home site actually owns (Name, Type, Restricted, member roster, `siteID`, `createdBy`, `createdAt`) via an explicit `$set` map, or (b) use `$setOnInsert` for the entire doc and a narrow `$set` for mutable home-site fields. (b) is what the existing federation contract implies.

### 4.2 Missing users silently dropped on `member_added` — **Medium-High**

`inbox-worker/handler.go:99-103`:

```go
for _, account := range event.Accounts {
    user, ok := userMap[account]
    if !ok {
        slog.Warn("user not found for account", "account", account)
        continue
    }
    ...
}
```

The local site needs the user doc to build the `Subscription.User` field. If user-replication hasn't completed yet for an account, that account is dropped silently and never retried — the federation message is acked. No backfill ever runs.

**Fix.** Either Nak with backoff so JetStream redelivers (paired with treating "all missing" as transient and "some missing" as permanent), or queue a backfill task. The current `slog.Warn + continue` is permanent data loss.

### 4.3 Bare error returns — **Medium**

`main.go:47-50` (`CreateSubscription`) and `main.go:52-58` (`UpsertRoom`) both end in `return err` with no `fmt.Errorf` wrap. Repo convention (CLAUDE.md §3.Error Handling) requires wrapping. The other methods in the same file follow the convention.

### 4.4 `role_updated` with empty roles loops forever — **Medium**

`handler.go:178-180`:

```go
if len(roles) == 0 {
    return fmt.Errorf("role_updated event has empty roles")
}
```

That error path Naks (`main.go:250`). Same payload will never have non-empty roles, so JetStream redelivers until `MaxDeliver` then DLQs — visible as a sustained Nak-rate alarm with no progress. Should be permanent: ack + log.

### 4.5 Generic Nak on any handler error — **Medium**

`main.go:246-258`: every handler error → Nak. Includes `json.Unmarshal` failures (poison messages) which will never succeed on retry. Adopt the `errPermanent` pattern from `room-worker/handler.go:188-197`.

---

## 5. room-worker

### 5.1 `fanOutKey` swallows per-account failures — **High**

`handler.go:1771-1802`:

```go
go func(acct string) {
    defer func() { <-sem; wg.Done() }()
    if err := h.keySender.Send(acct, *evt); err != nil {
        slog.Error("send room key", "error", err, "account", acct, "roomId", roomID)
        roomkeymetrics.FanoutErrors.Add(ctx, 1, ...)
    }
}(account)
```

The caller (`buildAndFanOutRoomKey`, `rotateAndFanOut`) never sees the error — `fanOutKey` returns `void`. The comment claims "JetStream redelivers on permanent failure," but redelivery only happens if `buildAndFanOutRoomKey` returns a non-nil error, which only fires when `keyStore.Get` fails. A NATS publish failure during fan-out is logged and forgotten.

**Impact.** A member misses the new key. They can't decrypt subsequent encrypted messages in that room until the next rotation. For a 1k-member room with one transient NATS hiccup, that's one user permanently broken until somebody is added/removed again.

**Fix.** Collect first error per fan-out, return it. Make `buildAndFanOutRoomKey` and `rotateAndFanOut` propagate it. The current behavior is acceptable only if `keySender.Send` is itself idempotent and reliable across NATS reconnects — verify before deciding "log and continue" is right.

### 5.2 Sync DM endpoint has no concurrency cap — **Medium**

`main.go:128`:

```go
nc.QueueSubscribe(subject.RoomCreateDMSync(cfg.SiteID), "room-worker", handler.natsServerCreateDM)
```

`nats.go` schedules each delivered message on a new goroutine by default. The JetStream worker loop is bounded by `cfg.MaxWorkers` via the semaphore at `main.go:145`, but `natsServerCreateDM` is a regular `nc.QueueSubscribe` — it gets none of that protection. A burst of cross-site DM sync requests spawns one goroutine per request.

**Fix.** Wrap the handler in a shared semaphore or use a dedicated worker pool — the same pattern as the JetStream loop.

### 5.3 Inbox publish errors swallowed mid-handler — **High**

`handler.go:443-445` (and analogous spots in `processRemoveOrg`, `finishCreateRoom`):

```go
if err := h.publish(ctx, subject.InboxMemberRemoved(h.siteID), inboxData, ...); err != nil {
    slog.Error("local inbox member_removed publish failed", ...)
}
```

The function continues. Subsequent sys-msg publish (line 494) returns errors via Nak. If only the local-inbox publish failed and everything else succeeded, the handler returns nil — the message is acked, the inbox event is lost, and search-sync-worker's MV will be out of sync forever. Dedup IDs protect against double-applies on retry, so failing loudly here is safe.

**Fix.** Treat local inbox publish failure as a hard error (return it). The downstream `OutboxDedupID` makes this idempotent on retry.

---

## 6. message-worker

### 6.1 Missing user causes infinite Nak loop — **Medium**

`handler.go:71-78`:

```go
user, err := h.userStore.FindUserByID(ctx, evt.Message.UserID)
if err != nil {
    if evt.Message.Type != "" {
        slog.Warn("user not found for system message, using nil sender", ...)
    } else {
        return fmt.Errorf("lookup user %s: %w", evt.Message.UserID, err)
    }
}
```

System messages fall through cleanly. Real user messages return an error → `HandleJetStreamMsg` Naks (`handler.go:46`). If the user was hard-deleted, the same JetStream delivery loops until `MaxDeliver`, then sits in DLQ; meanwhile the message is never persisted to Cassandra. The user's last messages before deletion vanish.

**Fix.** Treat `errors.Is(err, userstore.ErrUserNotFound)` as permanent: persist with a placeholder/nil sender (same path the system-message branch already exercises), ack, move on. Persistence is the source of truth.

### 6.2 Generic Nak on unmarshal failure — **Medium**

`handler.go:43-50`: poison JSON Naks forever. Same fix as inbox-worker (permanent-vs-transient split).

---

## 7. notification-worker

### 7.1 Notifies on every event type, including updates/deletes/sys-msgs — **High**

`handler.go:36-72`. The handler does not inspect `evt.Event` or `evt.Message.Type`. It builds:

```go
notif := model.NotificationEvent{
    Type:      "new_message",
    RoomID:    evt.Message.RoomID,
    Message:   evt.Message,
    ...
}
```

…for every event on `MESSAGES_CANONICAL`, which includes `.updated`, `.deleted`, and system messages (member added/removed/role changed/room created/etc.). Every user in the room gets a `new_message` push when somebody edits a typo or deletes a message. Every member-join system message also fires a push.

**Fix.** Filter at the top of HandleMessage: `if evt.Event != model.EventCreated { return nil }`. Also gate on `evt.Message.Type` — system messages should likely not notify, or get their own notification type. Note: the consumer config doesn't filter to `.created` only (compare with message-worker which does — `message-worker/main.go:193`), so this is reachable.

### 7.2 No sender skip leak — **Low (defended)**

`handler.go:59-64` does skip the sender. OK.

### 7.3 Bare error returns in store — **Low**

`main.go:44-57`: `return nil, err` twice without wrap.

---

## 8. history-service

### 8.1 Edit/delete canonical events published without dedup — **High**

`history-service/internal/service/messages.go:445-456`:

```go
func (s *HistoryService) publishCanonicalBestEffort(...) {
    payload, err := json.Marshal(evt)
    if err != nil { ... return }
    if err := s.publisher.Publish(c, subj, payload); err != nil {
        slog.Warn("canonical publish failed", ...)
    }
}
```

`internal/publisher/publisher.go`:

```go
func (p *Publisher) Publish(ctx context.Context, subj string, data []byte) error {
    return p.nc.Publish(ctx, subj, data)
}
```

This is *core NATS* `Publish`, not `js.PublishMsg(..., jetstream.WithMsgID(...))`. The JetStream MESSAGES_CANONICAL stream still receives the message via subject subscription, but:

1. There's no `PubAck` — we don't know if the stream accepted it.
2. There's no `Nats-Msg-Id` — JetStream stream-level dedup doesn't apply.

Compare with the `.created` path: `message-gatekeeper/handler.go:226` and `room-worker` both use `js.PublishMsg(..., WithMsgID(msg.ID))`.

**Impact.** A client retries an edit (timeout, network blip). Both attempts succeed. `broadcast-worker` and `search-sync-worker` each see two `.updated` events. Clients receive two `message_edited` pushes for the same edit; search-sync re-indexes twice (idempotent, fine, but wasted work).

For deletes: there's a CAS guard in `SoftDeleteMessage` (`messages.go:404-417`) that short-circuits the publish when a concurrent delete won. That handles concurrent delete-vs-delete but not retry-after-publish-failure on the original handler.

**Fix.** Switch `publisher.Publish` to JetStream with `WithMsgID(<msgID>:<editedAt>)` for edits and `WithMsgID(<msgID>:deleted)` for deletes. Choosing the right dedup ID matters — using just `msgID` would dedup multiple edits of the same message; using `msgID:editedAt` dedups retries-of-the-same-edit only.

### 8.2 Edit path has no double-publish guard on retry — **High (subset of 8.1)**

`messages.go:333-359` runs `UpdateMessageContent`, then publishes. If `UpdateMessageContent` succeeds and publish fails, the client sees the RPC fail and retries. Second call: `findMessage` returns the now-edited message, `canModify` still true, `UpdateMessageContent` runs again (Cassandra is idempotent on identical UPDATE), publish runs again. Two edits in the canonical stream.

Fix is covered by 8.1.

---

## 9. room-service

### 9.1 `ListRooms` has no pagination, no per-user filter — **Medium**

`handler.go:133-141`:

```go
func (h *Handler) natsListRooms(m otelnats.Msg) {
    ctx := wrappedCtx(m)
    rooms, err := h.store.ListRooms(ctx)
    ...
    natsutil.ReplyJSON(m.Msg, model.ListRoomsResponse{Rooms: rooms})
}
```

Unpaginated full-collection scan returned to any caller. Two problems: (a) at scale, every list-rooms request hammers Mongo and returns a multi-MB payload over NATS; (b) it returns every room regardless of whether the caller is a member. Compare with `handleListMembers` which checks subscription.

### 9.2 Raw error strings leak to clients — **Medium**

`handler.go:137`, `:149`:

```go
natsutil.ReplyError(m.Msg, err.Error())
```

Every other handler routes through `sanitizeError(err)` (e.g. `handler.go:111`, `:382`, `:395`, `:406`). These two skip sanitization, so a Mongo timeout or driver error surfaces verbatim to the client. CLAUDE.md §3 explicitly forbids this.

### 9.3 Bare `return err` in `RegisterCRUD` — **Low**

`handler.go:69`, `:71`, `:73`, `:75` — four `nc.QueueSubscribe` calls return `err` directly. Other branches in the same function correctly wrap. Inconsistent and breaks log readability when a subscribe fails at startup.

### 9.4 Race window on `MinUserLastSeenAt` recompute — **Low**

`handler.go:1059-1064`: `MinSubscriptionLastSeenByRoomID` (read) then `UpdateRoomMinUserLastSeenAt` (write). Another user's read in between → stale `minUserLastSeenAt`. The next read corrects it. Accept as documented.

---

## 10. search-sync-worker

Clean overall. Two notes:

### 10.1 `runConsumer` uses `context.Background()` for Flush — **Low**

`main.go:218`: `go runConsumer(ctx, ...)` is passed `ctx := context.Background()`. On shutdown, `close(stopCh)` is signaled, but the `Flush(ctx)` call inside `runConsumer` runs with a context that never times out. If the ES bulk request hangs on shutdown, the goroutine leaks until process exit (which `shutdown.Wait`'s 25s timeout eventually triggers anyway). Mild observation, not a real bug — the ES client typically has its own timeouts.

### 10.2 Otherwise sound

The ack/nak accounting in `handler.go:126-146` and the `isBulkItemSuccess` matrix in `handler.go:182-213` are well-commented and handle the idempotency story correctly.

---

## 11. search-service

Cursory pass; nothing jumped out. The `metricsServer` binding pattern in `main.go:181-200` is the right shape (synchronous Listen so port conflicts are loud).

---

## 12. mock-user-service

Test scaffolding; not in scope.

---

## Cross-cutting / `pkg/` observations

### C.1 `MESSAGE_BUCKET_HOURS` documentation drift — **Low**

`CLAUDE.md` (§6, Cassandra section) states:

> The window is configured per service via `MESSAGE_BUCKET_HOURS` (default 24).

Actual defaults:

- `message-worker/main.go:37`: `envDefault:"72"`
- `history-service/internal/config/config.go:39`: `envDefault:"72"`

The two services are consistent with each other (so no live data loss), but a new service copied from CLAUDE.md will default to 24 and silently split-brain Cassandra partitions. Fix the docs.

### C.2 `isBot` predicate triplicated, no shared package — **Low (style)**

`message-gatekeeper/helper.go:9` (regex), `broadcast-worker/helper.go:10` (string ops), `room-worker/handler.go:1213` (string ops), `inbox-worker/handler.go:237` (string ops), plus implicit copies in `room-service`. All implementations agree today (`\.bot$|^p_`). Each file has a comment acknowledging the duplication. Promote to `pkg/botid`.

### C.3 Generic Nak-on-any-error pattern is repo-wide — **Medium**

Five workers (`broadcast-worker`, `notification-worker`, `inbox-worker`, `message-worker`, `message-gatekeeper`'s infraError path is OK) treat every handler error as transient. Only `room-worker` distinguishes via `errPermanent`. Adopt the `room-worker` pattern across the worker fleet.

### C.4 `mention.ResolveFromParsed` empty-userMap behavior — **Verify**

`broadcast-worker/handler.go:79-86` falls back to an empty `userByAccount` map on lookup failure. Whether `mention.ResolveFromParsed` then returns the literal account strings as mentions, or returns empty, determines whether the mention badge is silently dropped on user-lookup hiccups. Not deep-dived in this audit.

---

## Recommended next steps

Order by impact:

1. **Fix #3 (inbox-worker UpsertRoom clobber).** Latent data loss on every federation event; mitigated only by how rare `room_sync` is in practice.
2. **Fix #2 (broadcast-worker UpdateRoomLastMessage guard) + #7 (fanOutKey error propagation).** Both compound under load.
3. **Fix #4/#8.1 (history-service edit/delete dedup).** Switch publisher to JetStream `WithMsgID`.
4. **Fix #5 (notification-worker event filter).** One-line filter; ships a quieter UX immediately.
5. **Fix #1 (auth-service `chat.room.>`).** Real authorization weakness; encryption mitigates body disclosure but envelope metadata (room names, mentions, timing) still leaks. Promote back to top of list if `ENCRYPTION_ENABLED` is ever off in prod or if metadata leakage is in scope of your threat model.
6. **Fix #9 (Nak-on-any-error) once and apply to all five workers using `room-worker`'s `errPermanent` pattern.**
7. **Audit follow-up:** `mention.ResolveFromParsed` empty-map behavior (#C.4) and a deeper read of `pkg/roomkeysender` for the `keySender.Send` reliability story (relevant to #7).
