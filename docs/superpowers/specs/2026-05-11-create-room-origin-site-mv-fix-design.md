# Create-Room Origin-Site MV Fix — Design

**Date:** 2026-05-11
**Status:** Draft
**Services:** `room-worker`, `inbox-worker`
**Related specs:**
- `2026-04-09-room-spotlight-user-room-design.md` (user-room and spotlight collections)
- `2026-04-21-search-service-sync-worker-extension-design.md` (search-sync-worker INBOX consumer)
- `2026-05-01-federated-room-origin-site-mv-fix-design.md` (sibling fix for add/remove flows; this spec applies the same pattern to the create flow)

## Problem

PR #145 closed the origin-site MV gap for `member.add` / `member.remove` operations by adding a local `chat.inbox.{siteID}.member_added` / `member_removed` publish from `room-worker`. That publish drives `search-sync-worker`'s `user-room-{siteID}` and `spotlight-{siteID}` ES indexes.

The room-creation path still has the same gap. When a room is created — channel, DM, or botDM — `room-worker.finishCreateRoom` writes the auto-enrolled `Subscription` rows (creator + DM recipient + every initial channel member) but never publishes a `member_added` event for them. `inbox-worker.handleRoomCreated` has the symmetric gap on remote sites: it upserts the local-side `Subscription` rows for federated rooms but never publishes a `member_added` event for the locally-resolved members.

Result: a freshly-created room is invisible to search until the next add/remove operation re-emits the event.

### User-visible consequences

1. **Spotlight (room typeahead) returns nothing for the new room.** The creator types the room name; `search-sync-worker` has no spotlight doc; no result.
2. **Cross-site message search returns empty for the new room.** CCS terms-lookup against the user's `user-room-{siteID}` doc reports the user as not subscribed; message hits are filtered out as unauthorized.
3. **Self-corrects on churn.** Both indexes catch up on the next `member.add` or `member.remove` against the room (PR #145's publish fires there). Until then, the room is silently invisible to search.

### Concrete trace

Alice on `s1` creates a channel `r1` with `Orgs: [eng-org]` (org expands to `[bob@s1, charlie@s1, dave@s2]`). Today:

| Subject | Stream | Effect |
|---|---|---|
| `chat.user.{account}.event.subscription.update` × 4 | core | Frontend left-panel updates for alice, bob, charlie, dave (cross-site routes via supercluster) |
| `chat.room.canonical.s1.create` | core (sys-message only, channel-only) | "alice created the room" |
| `outbox.s1.to.s2.room_created` | OUTBOX_s1 | Federation: dave's site receives `room_created`, `inbox-worker.handleRoomCreated` upserts dave's local `Subscription` row |

`s1`'s `user-room-s1` index gains zero entries. `s2`'s `user-room-s2` gains zero entries. Alice CCS-querying `r1` from any site fans out to `s1` and `s2` and finds no MV doc for any user → empty result.

## Goals

- `user-room-{siteID}` and `spotlight-{siteID}` on both the origin site and every federated remote site contain correct entries for every member auto-enrolled at room creation, regardless of room type.
- Fix lives in `room-worker.finishCreateRoom` (origin) and `inbox-worker.handleRoomCreated` (remote) — no new services, no new model types, no new subjects, no changes to `search-sync-worker` or stream config.
- Wire format byte-for-byte compatible with PR #145's existing publishes, so `search-sync-worker/inbox_stream.go::parseMemberEvent` decodes the new events identically.

## Non-Goals

- **Backfilling pre-fix rooms.** Forward-only deployment per agreement with team. Rooms created before this fix lands stay missing in their origin/federated MV until any later add/remove churn re-emits the event. Not documenting under "Known Limitations" because the operational expectation is "if the rooms matter, run any add-member operation on them".
- **Changing `chat.user.{account}.event.subscription.update` or `chat.room.canonical.{siteID}.create`.** UI fan-out and sys-message paths are correct; not in scope.
- **Refactoring `finishCreateRoom` or `handleRoomCreated`.** Both are narrow helpers; we're adding one publish to each.
- **Mint-on-create for the room encryption key.** Separate concern, deferred until `ENCRYPTION_ENABLED=true` is required in prod.

## Design

### NATS Subjects

No new subjects. PR #145 already exposed:

```go
// chat.inbox.{siteID}.member_added — local lane
subject.InboxMemberAdded(siteID)
```

Both `room-worker` and `inbox-worker` already import `pkg/subject`.

### Wire format

The publish wraps `model.MemberAddEvent` in `model.OutboxEvent`, matching PR #145's federated-lane wire format byte-for-byte:

```go
inner := model.MemberAddEvent{
    Type:               "member_added",
    RoomID:             room.ID,
    RoomName:           room.Name,
    Accounts:           accounts,        // see "Accounts list" below
    SiteID:             room.SiteID,     // origin
    JoinedAt:           req.Timestamp,
    HistorySharedSince: nil,             // see "HistorySharedSince" below
    Timestamp:          now.UnixMilli(),
}
outbox := model.OutboxEvent{
    Type:       "member_added",
    SiteID:     room.SiteID,             // origin
    DestSiteID: room.SiteID,             // self — local publish
    Payload:    mustMarshal(inner),
    Timestamp:  now.UnixMilli(),
}
```

### Accounts list

For every room type, the `Accounts` slice carries **expanded individual account names**, not org IDs. This matches PR #145's `processAddMembers` behavior; `search-sync-worker`'s per-user MV needs accounts.

| Room type | Accounts source |
|---|---|
| Channel (with or without orgs) | `subs[].User.Account` — org expansion already happened upstream in `room-worker.processCreateRoomChannel` |
| DM | `subs[].User.Account` — both creator and recipient |
| botDM | `subs[].User.Account` — creator only (the bot account doesn't get a sub by design) |

In all three cases the source is the same: the `subs []*model.Subscription` already passed to `finishCreateRoom`.

### HistorySharedSince

Always `nil` (unrestricted) for the create-time event. No prior history exists at room creation, so "share history since this timestamp" is meaningless — every member sees everything from t=0. Channel restricted-history kicks in only at later add-member events, which are already covered by PR #145.

### Dedup ID

Same shape as PR #145's cross-site OUTBOX publishes. Reuse the existing `outboxDedupID` helper:

```go
payloadSeed := fmt.Sprintf("%s:%s:%d", req.RoomID, requester.Account, req.Timestamp)
dedupID := outboxDedupID(ctx, room.SiteID, payloadSeed)
```

`outboxDedupID` prefers `X-Request-ID` from `ctx`, falling back to the seed. `destSiteID = room.SiteID` (self-loop) namespaces it so it can't collide with the per-remote-site OUTBOX dedup IDs the same `finishCreateRoom` invocation will emit.

For `inbox-worker.handleRoomCreated`, the seed mirrors the room-worker side but uses the locally-resolved info:

```go
payloadSeed := fmt.Sprintf("%s:%s:%d", data.RoomID, data.RequesterAccount, data.Timestamp)
dedupID := outboxDedupID(ctx, h.siteID, payloadSeed)
```

`X-Request-ID` is preserved across federation by `inbox-worker`'s consumer setup, so the dedup ID is stable across redeliveries.

### End-to-end flow after the fix

For the same `[bob@s1, charlie@s1, dave@s2]` channel-create on `s1`:

| Subject | Stream | Effect |
|---|---|---|
| `chat.user.{account}.event.subscription.update` × 4 | core | UI fan-out (unchanged) |
| `chat.room.canonical.s1.create` (sys-message only) | core | "alice created the room" (unchanged) |
| **`chat.inbox.s1.member_added`** (NEW) | INBOX_s1 (local lane) | s1's `user-room-sync` + `spotlight-sync` → s1's MV/spotlight gain docs for alice + bob + charlie + dave |
| `outbox.s1.to.s2.room_created` | OUTBOX_s1 | SubjectTransform → `chat.inbox.s2.aggregate.room_created` → s2's `inbox-worker.handleRoomCreated` |
| **`chat.inbox.s2.member_added`** (NEW, from inbox-worker) | INBOX_s2 (local lane) | s2's `user-room-sync` + `spotlight-sync` → s2's MV/spotlight gain a doc for dave |

End state: every site's MV/spotlight indexes have docs for every locally-affected member. CCS terms-lookup queries from any user resolve correctly.

### Ordering

The local-INBOX publish goes **after** the existing `subscription.update` loop and **before** the per-remote-site OUTBOX loop in `finishCreateRoom`. In `inbox-worker.handleRoomCreated`, the publish goes **after** `BulkCreateSubscriptions` (so the subs are durable before the search-sync event fires).

### Idempotency

`outboxDedupID` produces a stable JetStream `Nats-Msg-Id` per (room, requester, timestamp, destSiteID) tuple. JetStream stream-level dedup drops redeliveries within its dedup window (default 2 minutes, configured per stream). Beyond that window, `search-sync-worker`'s Painless last-write-wins guard makes replay idempotent on the ES side.

## Code Changes

### Change 1 — `room-worker/handler.go::finishCreateRoom`

After the existing `subscription.update` loop (line ~1117) and before the per-remote-site OUTBOX loop (line ~1129):

```go
// NEW — local INBOX publish for search-sync-worker (origin-site MV update).
// Mirrors PR #145's wire format; see docs/superpowers/specs/
// 2026-05-11-create-room-origin-site-mv-fix-design.md.
accounts := make([]string, 0, len(subs))
for _, sub := range subs {
    accounts = append(accounts, sub.User.Account)
}
inner := model.MemberAddEvent{
    Type:               "member_added",
    RoomID:             room.ID,
    RoomName:           room.Name,
    Accounts:           accounts,
    SiteID:             room.SiteID,
    JoinedAt:           req.Timestamp,
    HistorySharedSince: nil,
    Timestamp:          now.UnixMilli(),
}
innerData, _ := json.Marshal(inner)
outbox := model.OutboxEvent{
    Type:       "member_added",
    SiteID:     room.SiteID,
    DestSiteID: room.SiteID,
    Payload:    innerData,
    Timestamp:  now.UnixMilli(),
}
outboxData, _ := json.Marshal(outbox)
payloadSeed := fmt.Sprintf("%s:%s:%d", room.ID, requester.Account, req.Timestamp)
dedupID := outboxDedupID(ctx, room.SiteID, payloadSeed)
if err := h.publish(ctx, subject.InboxMemberAdded(room.SiteID), outboxData, dedupID); err != nil {
    slog.Error("local inbox member_added publish failed",
        "error", err, "roomID", room.ID, "requestID", requestID)
}
```

`subs []*model.Subscription` is already in scope.

### Change 2 — `inbox-worker/handler.go::handleRoomCreated`

After the existing `BulkCreateSubscriptions` call (line ~310), before the function returns:

```go
// NEW — local INBOX publish for search-sync-worker. Same wire format as
// room-worker.finishCreateRoom; the locally-resolved member set is
// data.Accounts (every account FindUsersByAccounts returned).
accounts := make([]string, 0, len(data.Accounts))
for _, account := range data.Accounts {
    accounts = append(accounts, account)
}
inner := model.MemberAddEvent{
    Type:               "member_added",
    RoomID:             data.RoomID,
    RoomName:           data.RoomName,
    Accounts:           accounts,
    SiteID:             data.HomeSiteID,
    JoinedAt:           data.Timestamp,
    HistorySharedSince: nil,
    Timestamp:          time.Now().UTC().UnixMilli(),
}
innerData, _ := json.Marshal(inner)
outbox := model.OutboxEvent{
    Type:       "member_added",
    SiteID:     h.siteID,
    DestSiteID: h.siteID,
    Payload:    innerData,
    Timestamp:  inner.Timestamp,
}
outboxData, _ := json.Marshal(outbox)
payloadSeed := fmt.Sprintf("%s:%s:%d", data.RoomID, data.RequesterAccount, data.Timestamp)
dedupID := outboxDedupID(ctx, h.siteID, payloadSeed)
if err := h.publish(ctx, subject.InboxMemberAdded(h.siteID), outboxData, dedupID); err != nil {
    slog.Error("local inbox member_added publish failed",
        "error", err, "roomID", data.RoomID, "requestID", requestID)
}
```

`inbox-worker` does not currently import `pkg/subject`; add the import.

`inbox-worker.Handler` does not currently have a `publish` field or an `outboxDedupID` helper — both will be added:
- `Handler.publish PublishFunc` injected via `NewHandler`, signature `func(ctx, subj, data, msgID) error`. Wired in `inbox-worker/main.go` to `nc.Publish` on the existing `*otelnats.Conn`.
- `outboxDedupID(ctx, destSiteID, payloadSeed)` — copy verbatim from `room-worker/handler.go:39`. Trivially small and avoids creating a new shared package for one helper.

### Error handling

Both publishes use the log-and-continue pattern from PR #145's existing local-INBOX publishes. The local publish failing must not fail the user-facing create request — JetStream redelivery (federation path) and `search-sync-worker`'s Painless guard (ES path) handle transient failures self-correctingly. Failing the whole create because the search index didn't update would be the wrong trade-off.

### What is NOT changed

- `pkg/subject` — `InboxMemberAdded` already exists.
- `pkg/stream` — `INBOX_{siteID}` already accepts `chat.inbox.{siteID}.*`.
- `pkg/model` — no new types; reuses `OutboxEvent`, `MemberAddEvent`, `RoomCreatedOutbox`.
- `inbox-worker` consumer FilterSubjects — PR #145 already scopes it to `chat.inbox.{siteID}.aggregate.>`, so the new local-lane publish stays out of `inbox-worker`'s own consumption (avoids self-feedback).
- `search-sync-worker`, `message-worker`, `broadcast-worker`, `history-service` — untouched.

### Diff size estimate

- `room-worker/handler.go`: +~22 lines.
- `inbox-worker/handler.go`: +~28 lines (publish block + import).
- `inbox-worker/main.go`: +~3 lines (wire publish).
- `inbox-worker/handler_test.go` and `inbox-worker/mock_*.go` (if any): updated for `NewHandler` signature.
- Tests: see Testing.

## Testing

Unit tests only, mirroring PR #145's pattern. Each handler injects `publish` as a field already (room-worker) or after this change (inbox-worker), so tests capture publishes into a slice and assert on the captured entries.

### Unit tests — `room-worker/handler_test.go`

Two new tests against `processCreateRoom*` paths:

- `TestHandler_processCreateRoom_Channel_PublishesLocalInbox`:
  - Mixed-site channel create with orgs and direct accounts.
  - Assert exactly one publish to `subject.InboxMemberAdded(room.SiteID)`.
  - Decode the payload as `OutboxEvent` → inner `MemberAddEvent`.
  - Verify: `OutboxEvent.{Type, SiteID, DestSiteID}` match origin self-loop; inner `Accounts` is the expanded set covering creator + every org-resolved account; inner `HistorySharedSince` is nil; inner `RoomName` matches `room.Name`; `Nats-Msg-Id` matches `outboxDedupID(ctx, siteID, "{rid}:{requester}:{ts}")`.

- `TestHandler_processCreateRoom_DM_PublishesLocalInbox`:
  - DM create across sites (creator local, recipient remote).
  - Assert one publish to `subject.InboxMemberAdded(room.SiteID)`.
  - Verify `Accounts` contains both creator and recipient; `HistorySharedSince` is nil; `RoomName` is empty (DM convention).

### Unit tests — `inbox-worker/handler_test.go`

Two new tests against `handleRoomCreated`:

- `TestHandler_handleRoomCreated_Channel_PublishesLocalInbox`:
  - Channel arrives via federation with two locally-resolved accounts.
  - Mock `FindUsersByAccounts` to return both.
  - Assert one publish to `subject.InboxMemberAdded(h.siteID)`.
  - Verify wire format matches the room-worker side; `Accounts` is the locally-resolved subset; `OutboxEvent.SiteID == DestSiteID == h.siteID`.

- `TestHandler_handleRoomCreated_DM_PublishesLocalInbox`:
  - DM arrives via federation with one locally-resolved account (the recipient on this site).
  - Same assertions, single account in `Accounts`.

Add a third negative test:
- `TestHandler_handleRoomCreated_EmptyAccounts_NoPublish` — when `data.Accounts` is empty (early return on line 261), assert zero publishes.

### Out of scope for new tests

- Integration tests against real NATS / Mongo — not in this PR's scope per agreement (would double diff size for marginal coverage gain).
- `search-sync-worker`'s ES write path — already covered by `search-sync-worker/inbox_integration_test.go` against the federated lane. The local lane uses identical payload shape decoded by the same `parseMemberEvent`, so duplicating that test against the local lane adds little.
- `processAddMembers` / `processRemoveIndividual` / `processRemoveOrg` paths — covered by PR #145's existing tests; this change does not touch them.

### Coverage target

Combined unit coverage for the two modified handler functions stays above the 80% project minimum (CLAUDE.md). Spot-check via `go tool cover -func=coverage.out` after implementation.

## Rollout

Both changes are backward-compatible:

- `room-worker` publishing to `chat.inbox.{site}.member_added` is purely additive. PR #145 already created the subject and `search-sync-worker` already filters on it.
- `inbox-worker` publishing to the same subject is also additive. The `inbox-worker` consumer's `FilterSubjects` (PR #145 / commit `c779ede`) is scoped to `chat.inbox.{siteID}.aggregate.>`, so its own publish stays off its own consumer — no self-feedback loop.

Recommended: deploy both services in the same release per site so the create-time MV update is symmetric across origin and remote on day one. No coordinated multi-site rollout needed.

### Per-site verification after deploy

1. Create a federated room (channel or DM) with members on at least one remote site.
2. Within seconds, query each site's `user-room-{siteID}` ES index and confirm:
   - The creator's doc on the origin site contains the new room ID.
   - Every channel member's / DM recipient's doc on their respective home site contains the new room ID.
3. Confirm spotlight typeahead returns the new room for the creator on the origin site.

## Observability

- **Logs:** new publishes use `slog.Error` log-and-continue. Failure messages: `"local inbox member_added publish failed"` (matches PR #145's exact string for grep parity). Search post-deploy to confirm zero failures.
- **Metrics:** none added. Existing JetStream stream-level metrics on `INBOX_{siteID}` will show throughput on `chat.inbox.{siteID}.member_added` rise from "PR #145's add/remove rate" to "that plus the create rate" — that rise is itself the success signal.
- **Traces:** the new publishes inherit the request context, so OTel trace IDs propagate end-to-end (`room-worker` → INBOX → `search-sync-worker` → ES bulk write all under one trace; `inbox-worker` extends the same trace from the federated arrival side).

## Risks

- **Federation duplicate publishes if a future bug routes a `room_created` event back to its origin site.** Today the OUTBOX → INBOX SubjectTransform plus the consumer FilterSubjects exclude the origin from the federated path, so this can't happen. Mitigation: the dedup ID uses `room.SiteID` (origin) on the room-worker side and `h.siteID` (this site) on the inbox-worker side — if a future config bug delivered the origin's own event to itself via federation, the two dedup IDs would still differ (origin vs. self), so JetStream would not collapse them. The `search-sync-worker` Painless last-write-wins guard would still produce the correct end state, but at the cost of one duplicate publish per federation cycle. Acceptable.
- **Stale spec drift if room-worker's create path adds a new sub source.** If a future change adds members to a room outside the `subs []*model.Subscription` slice passed to `finishCreateRoom` (e.g., a parallel "channel preset members" feature), the new INBOX publish would miss them. Mitigation: keep `subs` as the single source of truth for "who got auto-enrolled at create time" — every code path that adds a sub to a freshly-created room must go through `subs` and therefore through the publish. Document this invariant inline as a one-line comment above the publish.
