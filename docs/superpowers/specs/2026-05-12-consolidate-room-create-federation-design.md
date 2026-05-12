# Consolidate Cross-Site Room-Creation Federation Event — Design

**Date:** 2026-05-12
**Status:** Draft
**Services:** `room-worker`, `inbox-worker`, `search-sync-worker` (test-only)
**Related specs:**
- `2026-05-01-federated-room-origin-site-mv-fix-design.md` (PR #145 — added cross-site `member_added` for add-members)
- `2026-05-11-create-room-origin-site-mv-fix-design.md` (PR #169 — added cross-site `member_added` for room creation, leaving `room_created` in place)

## Problem

Today every room-creation publishes two cross-site OUTBOX events per remote site:

| Event | Consumer on remote site | Purpose |
|---|---|---|
| `outbox.{origin}.to.{remote}.room_created` | `inbox-worker.handleRoomCreated` | Build correctly-shaped `Subscription` rows (DM/botDM/channel) |
| `outbox.{origin}.to.{remote}.member_added` | `search-sync-worker` (MV update) and `inbox-worker.handleMemberAdded` (dup-key no-op for fresh rooms) | Search index update |

The redundancy is a layering artifact: PR #142 (Vinayak, `feat(create-room): DM, botDM, and channel creation across sites`) introduced `room_created` as the cross-site federation event before the `member_added` lane existed end-to-end. PR #145 then added the `member_added` cross-site event for the add-members path, and PR #169 extended `member_added` to the create path. At that point `room_created` became redundant for everything except the per-room-type sub-shape logic inside `inbox-worker.handleMemberAdded`, which is hardcoded to `RoomType: channel`.

### Two concrete consequences

1. **2× outbound publish per remote site per room creation.** `room-worker.finishCreateRoom` emits both events for every remote member's site.
2. **Latent search bug in `search-sync-worker/spotlight.go`**: it writes `RoomType: string(evt.RoomType)` into the spotlight ES doc, but today's `MemberAddEvent` wire payload doesn't carry `RoomType` — so for every room indexed via `member_added` since PR #145, the spotlight doc's `roomType` field has been the empty string. Room typeahead UI loses type info. This consolidation incidentally heals the bug because room-worker will start populating `RoomType` on every `member_added` publish.

## Goals

- One cross-site federation event per room creation. `member_added` does double duty: drives sub creation in `inbox-worker` and MV updates in `search-sync-worker`.
- `inbox-worker.handleMemberAdded` builds the right `Subscription` shape for every room type (channel, DM, botDM), reusing the existing per-type helpers (`subscriptionName`, `rolesForType`, `subscriptionIsSubscribed`) that `handleRoomCreated` uses today.
- Full removal of the `room_created` event type, its model, its handler, and its tests. No shim, no transition flag.
- No subject or stream changes — same wire lanes, fewer wire formats.

## Non-Goals

- **Backward compatibility for cross-tree consumers of `RoomCreatedOutbox` / `OutboxTypeRoomCreated`.** Grep confirms no out-of-tree consumer exists today.
- **Bridging in-flight `room_created` events during the deploy window.** Both ends ship in the same release per site; any straggler federated event arriving at a new `inbox-worker` pod hits the existing `default` case in the event-type switch and logs `"unknown event type, skipping"`. The corresponding `member_added` for the same room arrives moments later and does the right thing.
- **Refactoring `MemberRemoveEvent`** the same way. Remove is already type-agnostic in `inbox-worker.handleMemberRemoved`; only `member_added` needs the per-type sub-shape logic.
- **Search-sync-worker code changes.** Zero structural change required. One regression test added to lock in the latent-bug fix.

## Design

### Wire format

Extend `model.MemberAddEvent` with two `omitempty` fields:

```go
type MemberAddEvent struct {
    Type               string   `json:"type"`
    RoomID             string   `json:"roomId"`
    RoomName           string   `json:"roomName"`
    Accounts           []string `json:"accounts"`
    SiteID             string   `json:"siteId"`
    JoinedAt           int64    `json:"joinedAt"`
    HistorySharedSince *int64   `json:"historySharedSince,omitempty"`
    Timestamp          int64    `json:"timestamp"`

    // NEW
    RoomType         RoomType `json:"roomType,omitempty"`
    RequesterAccount string   `json:"requesterAccount,omitempty"`
}
```

- `RoomType`: drives `inbox-worker.handleMemberAdded`'s sub-shape branch and `search-sync-worker.spotlight.go`'s ES doc population. Zero value (`""`) is treated as `RoomTypeChannel` for backward compatibility with any in-flight publishes during deploy.
- `RequesterAccount`: only meaningful when `RoomType` is `DM` or `BotDM` — used by `subscriptionName` to compute the counterpart account for the recipient sub's `Name`. Empty/unused for channels.

The two added fields are `omitempty` so wire payloads from older publishers and from add-member operations (which don't need the fields) stay unchanged.

### Publisher side — `room-worker/handler.go`

Two publish call sites populate the new fields:

1. **`finishCreateRoom`** — the per-remote-site `member_added` OUTBOX publish (added in #169) sets `RoomType: room.Type` and `RequesterAccount: requester.Account`. The origin-local INBOX publish does the same for consistency (search-sync-worker on the origin reads it).
2. **`finishCreateRoom`** — delete the per-remote-site `room_created` OUTBOX publish entirely (the block constructing `RoomCreatedOutbox`).
3. **`processAddMembers`** — both publish sites (local INBOX + per-remote-site OUTBOX) get `RoomType: room.Type` and `RequesterAccount: req.RequesterAccount`. Channels only, so `RequesterAccount` won't actually be read on the consumer side, but keeping it consistent across all `member_added` emissions reduces special-case surface in publisher code.

### Consumer side — `inbox-worker/handler.go`

`handleMemberAdded` dispatches on `event.RoomType` and uses the existing helpers, refactored to take primitives so they're callable from both old-and-removed `handleRoomCreated` and the new `handleMemberAdded` path:

**Before** (lives in handler.go around line 227):
```go
func subscriptionName(d *model.RoomCreatedOutbox, u *model.User) string { ... }
func subscriptionIsSubscribed(d *model.RoomCreatedOutbox, u *model.User) bool { ... }
```

**After** (primitives, no struct dependency):
```go
func subscriptionName(roomType model.RoomType, roomName, requesterAccount string, u *model.User) string { ... }
func subscriptionIsSubscribed(roomType model.RoomType, u *model.User) bool { ... }
```

`rolesForType(t model.RoomType) []Role` already takes a primitive — unchanged.

`handleMemberAdded` post-refactor:

```go
func (h *Handler) handleMemberAdded(ctx context.Context, evt *model.OutboxEvent) error {
    var event model.MemberAddEvent
    if err := json.Unmarshal(evt.Payload, &event); err != nil {
        return fmt.Errorf("unmarshal member_added payload: %w", err)
    }

    roomType := event.RoomType
    if roomType == "" {
        roomType = model.RoomTypeChannel  // backward-compat for older publishers
    }

    users, err := h.store.FindUsersByAccounts(ctx, event.Accounts)
    if err != nil {
        return fmt.Errorf("find users by accounts: %w", err)
    }
    userMap := make(map[string]model.User, len(users))
    for i := range users {
        userMap[users[i].Account] = users[i]
    }

    joinedAt := time.UnixMilli(event.JoinedAt).UTC()
    var historySharedSince *time.Time
    if event.HistorySharedSince != nil && *event.HistorySharedSince > 0 {
        t := time.UnixMilli(*event.HistorySharedSince).UTC()
        historySharedSince = &t
    }

    subs := make([]*model.Subscription, 0, len(event.Accounts))
    for _, account := range event.Accounts {
        u, ok := userMap[account]
        if !ok {
            slog.Warn("user not found for account", "account", account)
            continue
        }
        sub := &model.Subscription{
            ID:                 idgen.GenerateUUIDv7(),
            User:               model.SubscriptionUser{ID: u.ID, Account: u.Account},
            RoomID:             event.RoomID,
            RoomType:           roomType,
            SiteID:             event.SiteID,
            Roles:              rolesForType(roomType),
            Name:               subscriptionName(roomType, event.RoomName, event.RequesterAccount, &u),
            IsSubscribed:       subscriptionIsSubscribed(roomType, &u),
            HistorySharedSince: historySharedSince,
            JoinedAt:           joinedAt,
        }
        subs = append(subs, sub)
    }

    if err := h.store.BulkCreateSubscriptions(ctx, subs); err != nil {
        return fmt.Errorf("bulk create subscriptions: %w", err)
    }
    return nil
}
```

The old `handleRoomCreated` function is **deleted**. The `case model.MessageTypeRoomCreated:` arm in `HandleEvent`'s switch is also deleted.

### Removal sweep

- `pkg/model/event.go`:
  - Delete `type RoomCreatedOutbox struct`.
  - Delete `OutboxTypeRoomCreated` constant (cross-site OUTBOX event type).
  - **Do not delete** `MessageTypeRoomCreated` constant — it's a distinct constant for the channel-create system-message type ("alice created the room"), used by `room-worker`'s `publishChannelSysMessages` and unrelated to federation. The two constants happen to share the string value `"room_created"` but live on different code paths.
- `inbox-worker/handler.go`:
  - Delete `handleRoomCreated`.
  - Delete the `case model.MessageTypeRoomCreated:` arm.
  - Refactor `subscriptionName` / `subscriptionIsSubscribed` to take primitives.
- `inbox-worker/handler_test.go`: delete `TestHandleRoomCreated*` cases (5 of them per grep). Replace with new `TestHandleMemberAdded_DM*` / `TestHandleMemberAdded_BotDM*` cases.
- `inbox-worker/integration_test.go`: delete `TestHandleRoomCreatedPersistsRemoteSubs` and `TestHandleRoomCreatedDM_PersistsRemoteCounterpartSub` (2 cases). Replace with `TestHandleMemberAddedDM_PersistsCorrectShape_Integration`.
- `room-worker/handler.go::finishCreateRoom`: delete the per-remote-site `room_created` publish block. Add `RoomType` + `RequesterAccount` to the existing `member_added` publishes (both local-INBOX and cross-site OUTBOX).
- `room-worker/handler.go::processAddMembers`: add `RoomType` + `RequesterAccount` to all three publish sites (local INBOX, cross-site OUTBOX, the older add-members publish if separate).
- `room-worker/handler_test.go`: assert `RoomType` is populated on every `member_added` publish capture; delete any test that asserted on the `room_created` outbox publishes.
- `room-worker/integration_test.go`: `TestProcessCreateRoomChannel_OutboxPerRemoteSite` and `TestProcessCreateRoomDM_OutboxToCounterpartSite` updated to assert only one outbox per dest site (the `member_added`), drop the `room_created` assertions.

### Search-sync-worker impact

Zero structural change. `model.InboxMemberEvent` (the struct `search-sync-worker.parseMemberEvent` decodes into) already has `RoomType` — it's the publisher side that didn't populate it. Once `room-worker` starts setting `RoomType` on every `member_added` publish, the existing `string(evt.RoomType)` write at `spotlight.go:120` produces the correct value for the first time.

One regression test added to `search-sync-worker/spotlight_test.go`:

- `TestSpotlightCollection_BuildAction_PopulatesRoomType` — builds an `InboxMemberEvent` with `RoomType: model.RoomTypeChannel`, runs `BuildAction`, asserts the resulting ES doc carries `roomType: "channel"`. Pure regression guard against the publishers reverting to empty `RoomType`.

### Idempotency

No change. The unique index on `subscriptions.(roomId, u.account)` already handles concurrent or redelivered creates idempotently. The dedup-key fall-through fix from PR #169 (CodeRabbit's catch) still applies — `mongo.IsDuplicateKeyError` is swallowed and execution continues so search-sync-worker's MV update still fires on replays.

Wait — that fix is in `handleRoomCreated`. After deleting `handleRoomCreated`, the dup-key handling needs to be present in `handleMemberAdded` too. Today `handleMemberAdded` returns the bulk-create error on any failure, including dup-key:

```go
if err := h.store.BulkCreateSubscriptions(ctx, subs); err != nil {
    return fmt.Errorf("bulk create subscriptions: %w", err)
}
```

Apply PR #169's fix here as well: treat `mongo.IsDuplicateKeyError` as idempotent and continue (no publish to fall through to in this handler, but the explicit nil return matches the intent and prevents JetStream nak-loops).

```go
if err := h.store.BulkCreateSubscriptions(ctx, subs); err != nil {
    if !mongo.IsDuplicateKeyError(err) {
        return fmt.Errorf("bulk create subscriptions: %w", err)
    }
}
```

## Testing

Unit only, per spec.

### `inbox-worker/handler_test.go`

New tests covering per-room-type sub-shape via `handleMemberAdded`:

- `TestHandleMemberAdded_Channel_BuildsChannelSub` (regression — covers the previous behavior, with `RoomType` field set explicitly on the event).
- `TestHandleMemberAdded_Channel_DefaultsWhenRoomTypeEmpty` — backward-compat: event with empty `RoomType` is treated as channel (older publishers).
- `TestHandleMemberAdded_DM_BuildsRecipientSubWithCounterpartName` — asserts `Name = RequesterAccount`, `Roles = nil`, `IsSubscribed = false`.
- `TestHandleMemberAdded_BotDM_BuildsBotSub` — asserts `IsSubscribed` depends on `isBot(u.Account)`.
- `TestHandleMemberAdded_DuplicateKey_IsIdempotent` — bulk-create returns dup-key; handler returns nil.

Delete `TestHandleRoomCreated*` cases (5 total per current `grep`).

### `inbox-worker/integration_test.go`

Delete `TestHandleRoomCreatedPersistsRemoteSubs` and `TestHandleRoomCreatedDM_PersistsRemoteCounterpartSub`. Replace with `TestHandleMemberAddedDM_PersistsCorrectShape_Integration` — exercises the full DM flow against real Mongo: `member_added` event with `RoomType=DM`, asserts the persisted Subscription row has `Name = RequesterAccount`, `Roles = nil`, `IsSubscribed = false`.

### `room-worker/handler_test.go`

Update existing PR #169 tests to assert `RoomType` and `RequesterAccount` are populated on the captured publishes. New test:

- `TestProcessCreateRoom_DM_OutboxCarriesRoomTypeAndRequester` — DM create across sites, captures the cross-site `member_added` publish, asserts inner `RoomType = DM` and `RequesterAccount = alice`.

### `room-worker/integration_test.go`

- `TestProcessCreateRoomChannel_OutboxPerRemoteSite` — drop the `room_created` outbox assertions. Assert exactly one outbox per dest site (`member_added`), with `RoomType=channel` on the payload.
- `TestProcessCreateRoomDM_OutboxToCounterpartSite` — same shape. Assert `RoomType=DM`, `RequesterAccount=alice` on the payload.

### `search-sync-worker/spotlight_test.go`

- `TestSpotlightCollection_BuildAction_PopulatesRoomType` (NEW): builds an `InboxMemberEvent` with `RoomType: model.RoomTypeChannel`, runs `BuildAction`, asserts the resulting ES doc carries `roomType: "channel"`. Locks in the consolidation's latent-bug fix.

## Rollout

⚠️ **Genuine tradeoff in this PR's scope** — see "Open rollout question" at the bottom of this section.

Same-release deploy with no ordering is **unsafe**. Walking the two possible orders:

| Deploy order | What happens during the rollout window |
|---|---|
| `room-worker` first | New room-worker stops publishing `room_created` and starts publishing `member_added` with `RoomType` + `RequesterAccount`. Old `inbox-worker` still has `handleMemberAdded` channel-hardcoded — when it receives a DM `member_added` from new room-worker, it builds the recipient sub with `Name=""`, `RoomType=channel`. **Malformed DM subs on the remote side.** |
| `inbox-worker` first | Old room-worker still publishes `room_created` for DM creations. New `inbox-worker` no longer handles `room_created` (default-arm warn-log). The corresponding `member_added` from old room-worker arrives without `RoomType` set; new `handleMemberAdded` defaults empty → channel. **Same malformed DM subs.** |

There is **no deploy order** that fully avoids the malformed-DM window in a single PR, because the wire-shape contract for DM federation changes between old and new schemas. The only correctness-preserving rollouts require keeping either the `room_created` publisher OR the `handleRoomCreated` handler alive through the transition — meaning a 2-PR split.

### Severity of the malformed-DM window

- **Defect**: cross-site DM created during the deploy window has `Name=""` (empty counterpart name) on the recipient site instead of the requester's account.
- **Impact**: frontend shows an unnamed DM in the recipient's left panel until manually fixed.
- **Scope**: bounded by the deploy duration (typically minutes per site). For a large org with multiple federated sites, the affected window per pair-of-sites is each site's individual deploy.
- **Recovery**: identifiable via `db.subscriptions.find({roomType: 'channel', name: ''})` on each remote site; fixable by either a one-shot script that re-derives `Name` from the room's home-site `Subscription` record, or by churn (any later add-member on the room re-emits with correct schema).

### Open rollout question

The Q1 design decision was "full removal in one PR". CodeRabbit (correctly) flagged that this isn't safe without one of these adjustments:

| Option | Single PR? | DM malformation window? | Trade-off |
|---|---|---|---|
| **(A)** Ship as-is, accept the malformed-DM window, document recovery | Yes | Yes (bounded, recoverable) | Simplest; one operator burden during deploy |
| **(B)** Split: PR1 adds new fields + handleMemberAdded RoomType dispatch + populates publishes; PR2 removes `room_created` publish + handler + model | No (2 PRs) | No | Cleanest correctness; slower delivery |
| **(C)** Single PR but room-worker keeps publishing `room_created` AND adds the new fields to `member_added`; inbox-worker deletes `handleRoomCreated` and learns RoomType dispatch. Deploy order: room-worker first, then inbox-worker. Follow-up PR later deletes the `room_created` publish from room-worker. | Yes (this PR), with follow-up | No | One transition PR + one cleanup PR; intermediate state is wire-compatible |

**Recommendation: option (C).** This PR removes the consumer's reliance on `room_created` and prepares the publisher to be removable in a follow-up, without any data-loss window. The follow-up PR to remove the publisher is mechanical (delete-only) once everyone's on the new inbox-worker.

This requires the user to confirm whether to amend the spec to (C) before proceeding to implementation. The sections below describe option (A) for completeness; flip to (C) on user confirmation.

### Step 1 — Deploy `room-worker` globally first (option A)

Roll the new `room-worker` image to every replica on every site. After this step, every `member_added` publish from any healthy `room-worker` pod carries the new `RoomType` + `RequesterAccount` fields, and every room creation emits the cross-site `member_added` event (not just channels).

Old `room-worker` pods are still publishing `room_created` for DM/botDM creations; that's fine because old `inbox-worker` pods (yet to be replaced) still handle it.

### Step 2 — Verify step 1 completion before starting step 3

Per site, all sites:

- **Replica check**: confirm the new `room-worker` image tag is uniform across every replica.
- **Schema check**: `nats stream get OUTBOX_{site} --last-by-subject 'outbox.*.to.*.member_added'` — the inner `MemberAddEvent` payload must contain a non-empty `roomType` field, and for DM/botDM creations a non-empty `requesterAccount`. If `roomType` is empty, a pre-deploy `room-worker` pod is still publishing — block step 3 until resolved.
- **Lane check**: `nats stream info OUTBOX_{site}` shows `member_added` subject traffic at the room-creation rate.

Step 3 MUST NOT begin until every site is green on the three checks above. This minimizes (but does not eliminate) the malformed-DM window.

### Step 3 — Deploy `inbox-worker` globally second

Roll the new `inbox-worker` image. This release deletes the `case model.MessageTypeRoomCreated:` switch arm and the `handleRoomCreated` function. After this step, new `inbox-worker` pods route every `member_added` through `handleMemberAdded` with RoomType dispatch.

### Per-site verification after step 3

1. Create a federated DM (alice@s1 → bob@s2). Verify on s2 the recipient's `Subscription` doc has `Name = "alice"`, `Roles = nil`, `IsSubscribed = false`.
2. Create a federated channel with one cross-site member. Verify on the remote site the member's `Subscription` doc has `Name = roomName`, `Roles = ["member"]`, `IsSubscribed = false`.
3. Query the spotlight ES index for the new rooms — confirm `roomType` is `"channel"` / `"dm"` / `"botDM"` (not empty).
4. `nats stream info OUTBOX_{site}` should show `member_added` events flowing at room-creation rate; `room_created` subject should report zero new messages.

## Observability

- **Logs**: one new `slog.Warn` per straggler `room_created` event during the deploy window — expected, transient. After deploy completes, zero.
- **Metrics**: existing JetStream metrics on `OUTBOX_{site}` will show `member_added` subject throughput rise (it now carries create-time too), `room_created` subject throughput drop to zero.
- **Traces**: unchanged — `member_added` was already on the trace path for create via PR #169.

## Risks

- **Federated event missing `RoomType` during deploy window.** Older publishers (pre-deploy) emit `member_added` without `RoomType`. The `roomType == ""` → `RoomTypeChannel` defaulting in `handleMemberAdded` keeps channel-create-and-add-member paths correct. The only risk is a pre-deploy DM federation: an old publisher sends `room_created` (which the new consumer drops as unknown) but no `member_added` for DMs (because the old publisher doesn't emit `member_added` for DMs at all — that's net-new in this PR). Mitigation: deploy `room-worker` first so all DM creates after that point emit the new event.
- **Search-sync-worker reading old InboxMemberEvent payloads from before the publisher rolled out.** Spotlight docs indexed during the window keep their empty `roomType` until the next event touches the room. Acceptable — consistent with the pre-deploy state, and the next add/remove on the room re-emits with the correct type.
- **Removal of `RoomCreatedOutbox` is a one-way ratchet.** No out-of-tree consumer exists today (`grep` confirms). If we ever want to reintroduce the dedicated event, the cost is just adding the type back; the lane is unchanged.
