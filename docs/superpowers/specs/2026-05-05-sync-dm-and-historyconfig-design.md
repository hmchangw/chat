# Sync Server-to-Server DM Creation + HistoryConfig Cleanup

**Date:** 2026-05-05
**Status:** Draft
**Branch:** `claude/sync-dm-endpoint` (targets `main`)

## Summary

Two related-but-distinct changes batched into a single PR:

**Part 1 — Sync server-to-server DM creation.** A new NATS request/reply endpoint **served by `room-worker`** that lets internal services (e.g. `user-service` for its app-subscribe flow) create a DM or botDM **synchronously** and receive the **requester's `Subscription` doc** back in the reply. The endpoint is intentionally **persistence-only** — caller-side validation is the contract: `user-service` does the existence/policy/dedup checks before issuing the request, and `room-worker` trusts the validated payload, persists Room + subs inline, emits the same events the async path does (`subscription.update`, cross-site outbox), and replies with the requester's Subscription. No JetStream hop, no async-job-result correlation, and no `room-service` involvement on this path. Returning the requester's Subscription (rather than the Room) saves the caller a follow-up `GetSubscription` lookup: the Subscription is the per-user entity user-service ultimately needs, and `Subscription.RoomID` carries the room identifier for free.

**Part 2 — Remove `HistoryConfig.SharedSince`.** Per reviewer feedback on PR #142: `HistoryConfig` no longer carries a `SharedSince` field — it has only `Mode`. `Subscription.HistorySharedSince` is now derived exclusively from `acceptedAt` (the room-service request-acceptance time) when `HistoryMode == "none"`.

The two parts are independent in code but ship together because both touch `pkg/model` and the room-creation pipeline.

## Scope

In scope:

**Part 1 — Sync DM endpoint (room-worker-hosted)**
- New NATS subject `chat.server.request.room.{siteID}.create.dm` (queue group `room-worker`, server credentials).
- New handler `natsServerCreateDM` in **`room-worker`** running persistence + outbox + events inline. Reply is the **requester's `Subscription` doc** (not the Room — the Subscription carries `RoomID` and is the per-user state user-service consumes; this also matches the legacy old-repo flow where the caller queried the requester's subscription after creation).
- **Caller-side validation contract.** `user-service` is responsible for all data-integrity checks before issuing the request: requester/counterpart existence, requester/counterpart `EngName`/`ChineseName` (DM only), self-DM rejection, bot `Assistant.Enabled` (botDM only), and **dedup-existing via `FindDMSubscription`** (caller skips the call when an existing DM is found and uses the existing `RoomID` directly). `room-worker` performs only minimal request-shape sanity checks (non-empty IDs/accounts, `RoomType ∈ {dm, botDM}`) and trusts the rest.
- Reuses the per-type sub-building from `room-worker`'s existing `processCreateRoomDM` / `processCreateRoomBotDM` via a small internal extraction (`buildDMSubs` / `buildBotDMSubs` helpers shared between the async-path code and the new sync handler). No new shared `pkg/createroom` package; `room-service` is not touched on this path.
- Cross-site counterparts: home-side state (Room + both subs) is written inline before reply; the `room_created` outbox event to the counterpart's site fires before reply but its remote-side materialization is best-effort eventually-consistent (same semantics as the async path).
- Idempotent against retries and races: insert-time duplicate-key on the Room or subscriptions unique index is treated as a redelivery — fetch the requester's existing `Subscription` and reply with it (same envelope as the freshly-created case).

**Part 2 — HistoryConfig.SharedSince removal**
- `pkg/model/member.go`: drop `HistoryConfig.SharedSince`.
- `room-worker/handler.go.historySharedSincePtr`: signature reduces to `(mode HistoryMode, acceptedAt time.Time) *int64`. No more fallback chain, no caller-supplied branch.
- `room-service`: stop reading `req.History.SharedSince` (the field is gone).
- Tests across `pkg/model`, `room-service`, `room-worker`, `inbox-worker` updated to drop the field.

Out of scope:

- Channel sync creation (different surface — channel orchestration includes org expansion, capacity, sys-messages, broadcast; not needed for the immediate user-service flow). Channel sync remains a future follow-up.
- The user-service caller (the eventual consumer of the new sync endpoint) — separate PR. The validation contract this PR documents is enforced by user-service in that follow-up.
- DM cross-site simultaneous-create tiebreak (still recorded as a known gap from the original create-room spec).
- Restoration of any pre-existing `Restricted` channel semantics or other create-room behavior.
- A shared `pkg/createroom/validate` package. With user-service as the single sync caller, validation lives in user-service and the existing async-path validation stays in `room-service`. If a second sync caller appears, extract.

## Part 1: Sync Server-to-Server DM Creation

### Topology

```text
user-service ──validates──> NATS req/reply ──> room-worker ──persists──> Mongo
                                                    │
                                                    ├── publishes subscription.update events
                                                    └── publishes room_created outbox events
```

`room-service` is **not** in this path. The async user-facing path (`chat.user.{account}.request.room.{siteID}.create`) is unchanged.

### NATS subject

| Operation | Subject | Wildcard | Queue Group | Auth |
|-----------|---------|----------|-------------|------|
| Sync create DM/botDM | `chat.server.request.room.{siteID}.create.dm` | `chat.server.request.room.{siteID}.create.dm` | `room-worker` | Server credentials (same `chat.server.>` callout policy used by `RoomsInfoBatchSubject`) |

The subject lives in the `chat.server.*` namespace, which is gated by NATS callout to require server credentials. The callout policy must be updated to permit subscribe on this subject from `room-worker` (today the policy permits `chat.server.>` on `room-service` for `RoomsInfoBatchSubject`).

The subject does NOT carry the requester's account (unlike the user-facing `chat.user.{account}.request.room.{siteID}.create`); the requester is named in the request payload.

`X-Request-ID` header is mandatory on the inbound request — the handler rejects with `errMissingRequestID` if absent. The header is propagated through any subsequent NATS hop (subscription.update events, outbox events) via `natsutil.NewMsg(ctx, ...)`.

### Caller-side validation contract (binding on user-service)

`user-service` MUST perform the following checks before issuing this request. `room-worker` does not re-run them; bad data passed in lands directly in Mongo. The list mirrors today's `room-service/handler.go:147-286` validation for DM/botDM:

| Check | Source of truth | Rejection reason |
|---|---|---|
| Requester user exists, has `EngName` and `ChineseName` populated | users collection | invalid requester |
| Counterpart account != requester account | input | self-DM |
| Counterpart user exists | users collection | counterpart not found |
| For `dm`: counterpart has `EngName` and `ChineseName` populated | users collection | invalid counterpart |
| For `botDM`: app exists in apps collection AND `app.Assistant != nil` AND `app.Assistant.Enabled == true` | apps collection | bot not available |
| Dedup-existing: `subscriptions.findOne({u.account: requester, name: counterpart, roomType $in {dm, botDM}})` returns nothing | subscriptions collection | (not a rejection — caller short-circuits and returns the existing `RoomID` to its own caller without calling room-worker) |
| Account classification: `roomType = botDM` if counterpart account ends in `.bot` or starts with `p_`, else `dm` | input | (not a rejection — caller stamps `RoomType` in the request) |

The dedup check is owned by user-service so room-worker stays a pure writer. **However**, room-worker still handles the duplicate-key error from the unique index defensively — concurrent races between two simultaneous user-service calls (or a user-service retry colliding with itself) are caught at insert time and treated as success-on-redelivery (see step 6 in the validation pipeline below).

### Reply contract

The success reply schema lives at `pkg/model.SyncCreateDMReply`:

```go
// pkg/model/member.go
type SyncCreateDMReply struct {
    Success      bool               `json:"success"`      // always true on this path
    Subscription model.Subscription `json:"subscription"` // the requester's sub
}
```

**Success:**

```json
{
  "success":      true,
  "subscription": { /* full model.Subscription — the requester's sub */ }
}
```

The reply payload is the requester-scoped `Subscription` (the one keyed by `RequesterAccount`), not the counterpart's. `Subscription.RoomID` carries the room identifier; callers that need additional Room metadata can look it up via the existing `RoomsInfoBatchSubject`.

Both branches that succeed return the same envelope shape:
- **Newly inserted DM/botDM** — a fresh `Room` was inserted alongside two new subscriptions; the requester's new subscription is returned.
- **Dedup-existing** — an existing DM/botDM matched the dedup query (`subscriptions.findOne({u.account, name, roomType $in {dm,botDM}})`); the requester's existing `Subscription` is returned unchanged. No new room or subscriptions are written.

There is intentionally no `created` flag distinguishing the two cases. The caller (user-service app-subscribe) only needs the requester's Subscription to proceed; the new-vs-existing distinction is invisible by design — both cases mean "the DM exists and the requester is subscribed to it." If a future caller genuinely needs to distinguish, it can compare `Subscription.CreatedAt` against the request time.

**Error:** standard `model.ErrorResponse` via `natsutil.ReplyError` (codebase-wide pattern):

```json
{
  "error": "<sanitized human-readable message>"
}
```

The error path matches every other NATS request/reply handler in the repo — the handler calls `natsutil.ReplyError(msg, sanitizeError(err).Error())`, and callers TryParseError the reply before falling back to unmarshalling `SyncCreateDMReply`. No new error envelope is introduced. The `Success` bool on `SyncCreateDMReply` is retained as an explicit-positive signal (cheap to compare on the caller side) but on this path is always `true`.

### Inbound request

The sync endpoint uses a **dedicated request type** rather than reusing `CreateRoomRequest`. The latter carries fields (`Orgs`, `Channels`, `Name`, `History`, etc.) that have no meaning for a pre-validated DM/botDM create — accepting them would invite drift between what user-service sends and what room-worker honors.

```go
// pkg/model/member.go
type SyncCreateDMRequest struct {
    RoomType         RoomType `json:"roomType"`         // "dm" | "botDM"
    RequesterAccount string   `json:"requesterAccount"`
    OtherAccount     string   `json:"otherAccount"`
}
```

Three fields, all required. The request is intentionally minimal — room-worker re-resolves the User records from accounts to fetch the data it needs for persistence:

| What room-worker derives at handler entry | Source |
|---|---|
| `requester.ID`, `requester.SiteID` | `store.GetUser(req.RequesterAccount)` — note `requester.SiteID == h.siteID` is invariant (user-service routes to the requester's home-site room-worker) |
| `other.ID`, `other.SiteID` | `store.GetUser(req.OtherAccount)` |
| Whether to emit cross-site outbox | `other.SiteID != h.siteID` |
| `roomID` | `idgen.BuildDMRoomID(requester.ID, other.ID)` |

Sanity checks performed by room-worker:

| Check | Rejection |
|---|---|
| `RoomType ∈ {dm, botDM}` | `errInvalidSyncDMRequest` |
| `RequesterAccount` and `OtherAccount` non-empty | `errInvalidSyncDMRequest` |
| `RequesterAccount != OtherAccount` | `errInvalidSyncDMRequest` (defense in depth) |
| `requester.SiteID == h.siteID` | `errCrossSiteRequester` (room-worker on the wrong site for this requester — user-service routing bug) |

Notably absent: requester/counterpart `EngName`/`ChineseName` checks, bot `Assistant.Enabled` checks, and dedup-existing — those live in user-service per the validation contract above. The two `GetUser` calls room-worker makes are purely for **data resolution** (User.ID, User.SiteID), not validation. If `GetUser` returns `ErrUserNotFound`, room-worker treats it as an operational error (`errUserLookupFailed`) — user-service should have already verified existence; arriving here means a race or a caller-contract violation.

room-worker stamps its own `acceptedAt = time.Now().UTC()` at handler entry. There is no `Timestamp` field on the request; per the project-wide principle, the backend never trusts a client-supplied timestamp.

### Handler pipeline

```text
1. Read X-Request-ID from ctx (set by natsrouter middleware).
   If empty → reject errMissingRequestID.
   If not a valid UUID → reject errInvalidRequestID.

2. Unmarshal SyncCreateDMRequest from m.Msg.Data.
   Validate request shape (RoomType ∈ {dm, botDM}, RequesterAccount and
   OtherAccount non-empty, RequesterAccount != OtherAccount).
   Otherwise → reject errInvalidSyncDMRequest.

3. Resolve User records (data lookup, not validation):
   requester, err := h.store.GetUser(ctx, req.RequesterAccount)
   On ErrUserNotFound or any error → reject errUserLookupFailed.
   If requester.SiteID != h.siteID → reject errCrossSiteRequester
     (user-service routed to the wrong site).
   other, err := h.store.GetUser(ctx, req.OtherAccount)
   On ErrUserNotFound or any error → reject errUserLookupFailed.

4. acceptedAt := time.Now().UTC()
   roomID    := idgen.BuildDMRoomID(requester.ID, other.ID)

5. Persist Room:
   room := &model.Room{
     ID:        roomID,
     Name:      "",                    // DM/botDM Room.Name is empty
     Type:      req.RoomType,
     CreatedBy: requester.ID,
     SiteID:    h.siteID,
     CreatedAt: acceptedAt,
     UpdatedAt: acceptedAt,
   }
   h.store.CreateRoom(ctx, room).
   On mongo.ErrDuplicateKey → fetch existing Room. If existing matches
   on (Type, SiteID, Name, CreatedBy) → treat as redelivery and continue
   with existing room. On mismatch → reject errRoomIDCollision.

6. Persist subscriptions (per-type sub-building via buildDMSubs /
   buildBotDMSubs; same shape as room-worker's existing
   processCreateRoomDM / processCreateRoomBotDM):
   DM:
     subs = [
       newSub(requester, SiteID: requester.SiteID (== h.siteID),
              room, name: other.Account,
              isSubscribed: true, acceptedAt),
       newSub(other,     SiteID: other.SiteID,
              room, name: requester.Account,
              isSubscribed: true, acceptedAt),
     ]
   botDM:
     subs = [
       newSub(requester, SiteID: requester.SiteID, room,
              name: other.Account,     isSubscribed: true,  acceptedAt),
       newSub(other,     SiteID: other.SiteID,     room,
              name: requester.Account, isSubscribed: false, acceptedAt),
     ]
   h.store.BulkCreateSubscriptions(ctx, subs).
   On mongo.ErrDuplicateKey (race against a concurrent caller, or a
   user-service retry whose first attempt already landed) → fetch the
   requester's existing subscription via FindDMSubscription and reply
   with it (success-on-redelivery branch, see step 10).

7. Reconcile member counts:
   h.store.ReconcileMemberCounts(ctx, room.ID).
   On error: log and continue. Counts are recomputed by every membership
   change; transient failure self-heals.

8. Publish per-user subscription.update events:
   For each sub in subs, publish on subject.SubscriptionUpdate(sub.User.Account)
   with payload SubscriptionUpdateEvent{UserID, Subscription, Action:"added",
   Timestamp: acceptedAt.UnixMilli()}.
   NATS supercluster routes cross-site users' events to their home sites.
   On error: log and continue.

9. Publish cross-site room_created outbox event:
   If other.SiteID != h.siteID, emit one outbox event:
     subject:  outbox.{h.siteID}.to.{other.SiteID}.room_created
     payload:  RoomCreatedOutbox{
                 RoomID, RoomType: req.RoomType, RoomName: "",
                 HomeSiteID: h.siteID,
                 Accounts:[other.Account],
                 RequesterAccount: requester.Account,
                 Timestamp: acceptedAt.UnixMilli(),
               }
     Nats-Msg-Id: requestID + ":" + other.SiteID
   Fire-and-forget — the reply does NOT wait for the remote site's
   inbox-worker to materialize the sub. On error: log and continue.

10. Reply to the caller:
    Pick the requester-scoped subscription out of the subs slice (the
    one whose User.Account == req.RequesterAccount). On the duplicate-key
    redelivery branch in step 6, fetch via FindDMSubscription instead.
    natsutil.ReplyJSON(msg, model.SyncCreateDMReply{
      Success:      true,
      Subscription: requesterSub,
    })
```

Two `GetUser` calls on the happy path (steps 3) — for **data resolution**, not validation. `FindDMSubscription` is called only on the duplicate-key redelivery branch (step 6). Total handler ~100 LOC.

### Refactor — internal extraction in room-worker

room-worker hosts both the existing async path (`processCreateRoom` chain) and the new sync handler. The two share the per-type sub-building; everything else is path-specific. Two small extractions inside room-worker:

1. **`buildDMSubs(requester, other *model.User, room *model.Room, acceptedAt time.Time) []*model.Subscription`** — the two-line DM sub-building, called by both `processCreateRoomDM` (existing async path) and the new sync handler. Both paths have full `*model.User` records in scope at the call site (the sync handler resolves them via `GetUser` in step 3 of the handler pipeline).

2. **`buildBotDMSubs(requester, bot *model.User, room *model.Room, acceptedAt time.Time) []*model.Subscription`** — same for botDM.

Each User record provides `User.ID`, `User.Account`, and `User.SiteID` — everything the sub builder needs.

No new shared package. No `pkg/createroom`. The async path's existing tests (`TestProcessCreateRoomDM`, `TestProcessCreateRoomBotDM`, `TestFinishCreateRoom`) continue to pass without modification — only the sub-construction line changes from inline to a helper call.

### File layout

| Path | Change | What it owns |
|------|--------|--------------|
| `pkg/model/member.go` | modify | new `SyncCreateDMRequest` and `SyncCreateDMReply` types |
| `pkg/model/member_test.go` | modify | JSON/BSON round-trip for the new types |
| `pkg/subject/subject.go` | modify | `RoomCreateDMSync(siteID)` builder |
| `pkg/subject/subject_test.go` | modify | round-trip test for the new subject |
| `room-worker/handler_sync_create_dm.go` | new | `natsServerCreateDM` handler + `handleSyncCreateDM` business logic |
| `room-worker/handler_sync_create_dm_test.go` | new | unit tests for the sync handler (mocked store) |
| `room-worker/handler.go` | modify | extract `buildDMSubs` / `buildBotDMSubs` from `processCreateRoomDM` / `processCreateRoomBotDM`. No behavior change. |
| `room-worker/main.go` | modify | subscribe on `chat.server.request.room.{siteID}.create.dm` with queue group `room-worker` and server credentials; add to graceful-shutdown drain order |
| `room-worker/integration_test.go` | modify | add `TestSyncCreateDM_*` integration tests with testcontainers |
| **No changes** | — | `room-service/*` is **not** touched by Part 1 |
| NATS callout policy (ops/IaC) | modify | permit subscribe on `chat.server.request.room.>` from `room-worker` (today the policy permits this only on `room-service` for `RoomsInfoBatchSubject`) |

### Tests

**Unit (`room-worker/handler_sync_create_dm_test.go`):**
- DM happy path: handler returns `success:true` with the requester's `Subscription` (its `User.Account == req.RequesterAccount`, `RoomID == BuildDMRoomID(requester.ID, other.ID)`, `IsSubscribed=true`, `Name == req.OtherAccount`); Room + 2 subs persisted in store; `subscription.update` events captured for both users.
- botDM happy path: returns `success:true` with the requester's `Subscription` (`IsSubscribed=true`); the bot's sub (`IsSubscribed=false`) is persisted in store but NOT returned.
- Cross-site DM: `other.SiteID != h.siteID` (resolved via `GetUser`) → captured outbox publish goes to the counterpart's site with the right payload shape (`RoomName=""`, `Accounts:[other.Account]`, `Nats-Msg-Id = requestID:destSite`).
- Same-site DM: `other.SiteID == h.siteID` → no outbox publish.
- Duplicate-key redelivery on Room insert (matching existing room): returns `success:true` with the requester's existing Subscription; no second sub inserted.
- Duplicate-key on subscription bulk insert (race): handler calls `FindDMSubscription` to fetch the requester's existing sub and returns it.
- Error path: malformed request (`RoomType` outside `{dm,botDM}`, empty `RequesterAccount`/`OtherAccount`, `RequesterAccount == OtherAccount`) rejected with `errInvalidSyncDMRequest`; reply parses as `model.ErrorResponse`.
- Requester `GetUser` fails with `ErrUserNotFound` → rejected with `errUserLookupFailed` (operational error; user-service should have verified existence).
- Requester resolves but `requester.SiteID != h.siteID` → rejected with `errCrossSiteRequester`.
- Other `GetUser` fails with `ErrUserNotFound` → rejected with `errUserLookupFailed`.
- Missing `X-Request-ID` rejected with `errMissingRequestID`.
- `ReconcileMemberCounts` failure: logged, reply still succeeds.
- `subscription.update` publish failure: logged, reply still succeeds.
- Outbox publish failure: logged, reply still succeeds.

**Integration (`room-worker/integration_test.go`):**
- `TestSyncCreateDM_DM_PersistsRoomAndSubs`: real Mongo + testcontainers; asserts Room + 2 subs + `subscription.update` events fired.
- `TestSyncCreateDM_BotDM_CrossSiteOutbox`: cross-site botDM; asserts outbox event to the bot's home site.
- `TestSyncCreateDM_RetryIdempotent`: invoke twice with the same `X-Request-ID` (and same Room/Sub IDs); assert exactly one Room and two subs end up persisted; both replies return the same requester Subscription.
- `TestSyncCreateDM_ConcurrentRace`: two goroutines invoke simultaneously with the same `(requesterID, otherID)`; assert both succeed, exactly one Room and two subs persisted, both replies return the same Subscription.

## Part 2: Remove `HistoryConfig.SharedSince`

### Rule

`HistoryConfig` carries only `Mode` — there is no `SharedSince` field on it. `Subscription.HistorySharedSince` is derived **only** from `acceptedAt` (the request-acceptance time stamped by `room-service`). The client never supplies this value. When `HistoryMode == "none"` the subscription is restricted starting at `acceptedAt`; otherwise the field is `nil` (full history visible).

### Code changes

**`pkg/model/member.go`:**

```diff
 type HistoryConfig struct {
 	Mode HistoryMode `json:"mode" bson:"mode"`
-	// SharedSince (ms): when non-nil under HistoryModeNone, takes precedence over acceptedAt.
-	SharedSince *int64 `json:"sharedSince,omitempty" bson:"sharedSince,omitempty"`
 }
```

`MemberAddEvent.HistorySharedSince` (the cross-site outbox payload field) stays — it carries the server-derived value for `inbox-worker` to consume on remote sites.

**`room-worker/handler.go`:**

```diff
-// historySharedSincePtr resolves the History.SharedSince value for an
-// add-member request. Mode != HistoryModeNone → nil (history is unrestricted).
-// Otherwise prefers the caller-supplied req.History.SharedSince when non-nil
-// and positive, falling back to req.Timestamp. This single source of truth
-// keeps the local subscription's HistorySharedSince consistent with the
-// MemberAddEvent published to the user's subject and the cross-site outbox
-// payload — without it, an explicit caller cutoff would be silently
-// overwritten with the request acceptance timestamp at fan-out time.
-func historySharedSincePtr(history model.HistoryConfig, timestamp int64, roomID string) *int64 {
-	if history.Mode != model.HistoryModeNone {
-		return nil
-	}
-	if history.SharedSince != nil && *history.SharedSince > 0 {
-		ss := *history.SharedSince
-		return &ss
-	}
-	if timestamp <= 0 {
-		slog.Error("restricted history with missing timestamp, emitting nil", "roomID", roomID, "mode", history.Mode)
-		return nil
-	}
-	return &timestamp
-}
+// historySharedSincePtr returns &acceptedAt when history is restricted
+// (mode == HistoryModeNone), nil otherwise. Backend always uses
+// acceptedAt; clients never supply this timestamp.
+func historySharedSincePtr(mode model.HistoryMode, acceptedAt time.Time) *int64 {
+	if mode != model.HistoryModeNone {
+		return nil
+	}
+	ms := acceptedAt.UnixMilli()
+	return &ms
+}
```

Call sites (three of them) update from `historySharedSincePtr(req.History, req.Timestamp, req.RoomID)` to `historySharedSincePtr(req.History.Mode, acceptedAt)`. The `req.Timestamp <= 0 → time.Now()` defensive guard at the top of `processAddMembers` already normalizes the timestamp, so the helper no longer needs its own guard or the `roomID` parameter for logging.

**Shipped deviation:** the helper kept its original `(history model.HistoryConfig, timestamp int64, roomID string)` signature to minimize call-site churn — only the body collapsed to the single-branch form (`Mode != HistoryModeNone → nil; otherwise &timestamp`). Call sites still pass `req.History`, `req.Timestamp`, `req.RoomID`. The `roomID` parameter is no longer read but stayed in the signature.

**`room-service/handler.go`:**

The `handleAddMembers` flow forwards `req.History` (now without `SharedSince`) through to room-worker via JetStream. With the field gone from the model, it's no longer carried in the payload. No code change beyond the existing dedup/strip pass; the JSON unmarshal on the worker side simply doesn't see it.

**Tests:**

Sweep `grep -rn "SharedSince:" room-service/ room-worker/ inbox-worker/ pkg/`. Every test that sets `History: HistoryConfig{Mode: ..., SharedSince: &ts}` drops the `SharedSince` line; assertions that compared against the caller-supplied value rebase to `acceptedAt`'s value.

Estimated test changes: 5–10 lines across ~5 test files.

### Why the change is safe

- `HistoryConfig.SharedSince` is currently always set to either `nil` (the common case) or `&req.Timestamp` (the existing fallback in `historySharedSincePtr`). No production caller sends a meaningfully different value.
- The frontend (`chat-frontend`) doesn't read or set `HistoryConfig.SharedSince` directly; it only sets `Mode`.
- Cross-site `MemberAddEvent.HistorySharedSince` is server-computed in room-worker and remains unchanged on the wire (still an `int64` ms timestamp).

The drop is purely a cleanup of a misleading API surface. No migration is needed.

## Error Handling

### Sentinel errors (room-worker)

The sync handler defines a small set of new sentinels in `room-worker`. Validation-level sentinels from `room-service` (`errSelfDM`, `errUserNotFound`, `errInvalidUserData`, `errBotNotAvailable`) are NOT used here — those checks happen in user-service, which is responsible for surfacing its own error vocabulary to upstream callers.

```go
// room-worker/handler_sync_create_dm.go
var (
    errInvalidSyncDMRequest = errors.New("invalid sync DM request")
    errMissingRequestID     = errors.New("missing X-Request-ID header")
    errInvalidRequestID     = errors.New("invalid X-Request-ID header")
    errUserLookupFailed     = errors.New("user lookup failed")
    errCrossSiteRequester   = errors.New("requester is not on this site")
    errRoomIDCollision      = errors.New("room ID collision (existing room metadata mismatch)")
)
```

`errInvalidSyncDMRequest` covers shape failures: invalid `RoomType`, empty `RequesterAccount`/`OtherAccount`, or `RequesterAccount == OtherAccount`.

`errUserLookupFailed` is the operational error returned when `GetUser` fails for either account — including `ErrUserNotFound`. This is intentionally a single opaque sentinel: user-service is contractually required to have verified existence before calling, so any miss here is a contract violation or a race, not something the caller should branch on.

`errCrossSiteRequester` indicates user-service routed to the wrong site (the requester's home site is not this room-worker's site). It surfaces a routing bug rather than a data error.

### sanitizeError

room-worker does not have a `sanitizeError` helper today (it doesn't reply to clients on the async path — its outputs go to JetStream). The sync handler adds a thin `sanitizeSyncDMError` helper that maps known sentinels to user-displayable strings and wraps anything else as `"internal error"` (never leaks raw error text). Returned via `natsutil.ReplyError(msg, sanitizeSyncDMError(err).Error())`.

### Sync-path failure modes

| Scenario | Behavior |
|---|---|
| `BulkCreateSubscriptions` partially fails (e.g. one of two subs hits a duplicate-key race against another concurrent sync call) | Treat as redelivery: fetch the requester's existing subscription and reply with the success envelope. |
| `ReconcileMemberCounts` fails | Log and continue; the count is recomputed by every membership change, so a transient failure self-heals. The reply still succeeds. |
| `subscription.update` publish fails | Log and continue; the user's frontend reconnects and reads the durable subscription. The reply still succeeds. |
| Outbox publish fails (the `room_created` event to a remote site) | Log; the reply still succeeds. The remote site won't have the sub until the next reconciliation; this matches the async path's behavior. |

There is no error path that returns "partial success" to the caller. Either the room and subs are durably persisted on the home site (success envelope with the requester's Subscription), or the request fails and nothing is written (error envelope).

## Testing Strategy

### Unit tests

- `pkg/subject/subject_test.go`: round-trip `RoomCreateDMSync(siteID)`.
- `pkg/model/member_test.go`: JSON/BSON round-trip for `SyncCreateDMRequest` and `SyncCreateDMReply`. Also assert `HistoryConfig` no longer marshals or unmarshals `sharedSince` (Part 2). Add an explicit assertion that `{"mode":"none","sharedSince":1740000000000}` JSON unmarshals to `HistoryConfig{Mode: HistoryModeNone}` with the extra field silently ignored — backward-compatible with any in-flight requests.
- `room-worker/handler_sync_create_dm_test.go`: see Part 1's test list above.
- `room-worker/handler_test.go`: regression coverage for `buildDMSubs` / `buildBotDMSubs` extraction — async path's existing `TestProcessCreateRoomDM` / `TestProcessCreateRoomBotDM` still pass without modification.

### Integration tests

- `room-worker/integration_test.go` adds the `TestSyncCreateDM_*` integration tests listed in Part 1.
- `room-worker/integration_test.go` and `inbox-worker/integration_test.go` get the test cleanups for Part 2 (`SharedSince:` removal).

### Coverage targets

- `room-worker` sync DM handler: ≥ 90%.
- `room-worker.buildDMSubs` / `buildBotDMSubs`: 100% (pure functions, two cases each).
- `room-worker.historySharedSincePtr`: 100% (only two branches).

### Out-of-band verification

- `make lint` clean.
- `make test` (full repo, race) green.
- `make test-integration SERVICE=room-worker` green.
- `make test-integration SERVICE=inbox-worker` green.
- Manual NATS request to local docker-compose (server credentials, targeting `chat.server.request.room.{siteID}.create.dm`) creates a botDM end-to-end and the reply contains `success:true` with the requester's `Subscription`. Re-issuing the same request returns the same Subscription (also `success:true`, indistinguishable from the first call).

## Implementation deviations from spec

| Spec said | Shipped |
|-----------|---------|
| Call `ReconcileMemberCounts`; log on error | Skip; set `Room.UserCount`/`AppCount` inline at create (DM=2,0; botDM=1,1). DMs have fixed rosters — no future membership change to self-heal. |
| Outbox publish: log and continue | Fail the request. Sync RPC has no auto-retry; silent federation break otherwise. |
| `FindDMSubscription` only on dup-key fallback | Always re-read after `BulkCreateSubscriptions` (the store swallows dup-key, in-memory sub may diverge from persisted). |
| `errUserLookupFailed` on any `GetUser` error | Only on `ErrUserNotFound`; transient errors wrapped with `%w err` → surface as `"internal error"`. |
| `errRoomIDCollision` surfaces verbatim | Masked as `"internal error"`; mismatch details logged. |
| New file `handler_sync_create_dm.go` | Sync code lives at the bottom of `room-worker/handler.go`; tests stay in `handler_sync_create_dm_test.go`. |

Out-of-spec fix in same PR: `room-service/store_mongo.go` partial-unique DM dedup index used `roomType: {$in:…}` which MongoDB rejects in `partialFilterExpression`. Split into two `$eq`-filtered indexes (one per room type).

---

## Follow-ups

- **Sync caller in user-service** — the user-service-side caller of the new sync endpoint (for the app-subscribe flow) is a separate PR. That PR is responsible for implementing the validation contract documented above (existence checks, EngName/ChineseName checks, bot-enabled check, dedup-existing). This PR delivers the room-worker surface only.
- **Shared validation package** — if a second internal sync caller appears in the future, extract the validation list documented above into `pkg/createroom/validate` so callers don't drift.
- **DM cross-site simultaneous-create tiebreak** — recorded as a known gap in the original create-room spec. Not addressed here; equally affects the sync and async paths.
- **Channel sync creation** — if a future internal flow needs to create a channel synchronously, the same shape applies but with an expanded subject (`...create.channel`) and the per-type loop from `processCreateRoomChannel`. Out of scope here.

---

*End of design document.*
