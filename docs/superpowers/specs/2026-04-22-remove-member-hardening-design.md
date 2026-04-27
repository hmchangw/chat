# Remove-Member / Role-Update Hardening — Design

**Date:** 2026-04-22
**Status:** Approved (implemented in branch `claude/add-siteid-to-room-members-3q2gi`)
**Services:** `room-service`, `room-worker`

## Summary

Closes five defects identified in the remove-member / add-member / role-update
flows, all rooted in at-least-once JetStream delivery and a missing invariant
check. The remove-member spec (2026-04-14-remove-member-design.md) and the
role-update spec (2026-04-13-role-update-design.md) remain the authoritative
descriptions of those features. This document captures targeted corrections
that apply to both, plus the one-line invariant addition.

## Defects Addressed

| # | Defect | Root cause | Fix |
|---|--------|-----------|-----|
| 1 | `userCount` drifts under redelivery | `$inc`/`$dec` is non-idempotent; repeated deliveries either double-count or skip the update depending on where the crash lands | Replace `IncrementUserCount` / `DecrementUserCount` with `ReconcileUserCount(roomID)` — `$set` to the current `subscriptions.countDocuments(roomID)`. Idempotent. |
| 2 | Duplicate events on redelivery | JetStream publishes had no `Nats-Msg-Id` header; stream ingestion accepted duplicates | Extend `publish` signature with an `msgID` parameter. For publishes targeting JetStream streams (`MESSAGES_CANONICAL`, `OUTBOX`), derive a stable `Nats-Msg-Id` via `uuid.NewSHA1(dedupNamespace, seed)`. Core-NATS publishes pass `""`. |
| 3 | Duplicate system messages in Cassandra | `Message.ID` was `uuid.New()` per delivery; Cassandra PK `(room_id, created_at, message_id)` received distinct keys on retry | Derive `Message.ID` deterministically from stable request fields via `idgen.DeriveID(seed)` — a 17-char base62 ID (user-visible, fits inside Cassandra rows). The `Nats-Msg-Id` headers on the same flow still use `deriveDedupID` (UUID via `uuid.NewSHA1`). Both derivations consume the **same stable seed** (e.g., `roomID:account:req.Timestamp` on remove, sorted accounts + requester + `req.Timestamp` on add) — only the output shape differs. Added `RemoveMemberRequest.Timestamp` (set by room-service) so remove flows have the same stable seed add-member already uses. |
| 4 | Silent outbox failures break federation | `processRemoveIndividual` / `processRemoveOrg` logged outbox publish errors and continued; the ROOMS message was acked, so retry never happened and cross-site state drifted | Propagate outbox publish errors so JetStream NAKs and retries the ROOMS message. Aligns with add-member / role-update which already did this. |
| 5 | Role-update didn't enforce the "org members cannot be owners" invariant | Remove-member's dual-membership demote path assumes org-only users never hold the owner role. Role-update had no matching check; an owner could promote an org-only subscriber to owner. | In `handleUpdateRole`, after `errAlreadyOwner`, fetch `GetSubscriptionWithMembership` on the target when promoting. Reject with new sentinel `errPromoteRequiresIndividual` if `!HasIndividualMembership`. |

## Cross-spec corrections

### Remove-member spec (2026-04-14)

The following clauses should be read alongside the hardening:

- **Step 3c of individual removal** ("Decrement `userCount` by the actual
  `DeletedCount`") and **step 5 of org removal** ("Decrement by
  `len(toRemove)`") are superseded by a single `ReconcileUserCount` call in
  both paths. The counter is always set to the actual subscription count.
- **Step 3g of individual removal** and **step 9 of org removal** (outbox
  publish) are part of the "any failure NAKs" chain — failures must propagate
  errors out of `processRemoveMember`. This matches what the spec preamble
  already says; the implementation now honors it.
- **System-message publish** step (3f / 8): `Message.ID` is derived
  deterministically from `(roomID, account|orgID, req.Timestamp)` via
  `idgen.DeriveID` (17-char base62, stored as the Cassandra row PK). The
  same seed (prefixed to namespace per publish target) is separately passed
  through `deriveDedupID` to produce the `Nats-Msg-Id` (UUID) header on the
  `MESSAGES_CANONICAL` publish — these are two different ID shapes derived
  from one stable seed, not one shared ID. Outbox publishes set their own
  `Nats-Msg-Id` likewise, scoped by destination site so per-site fan-outs
  don't collide.
- **`RemoveMemberRequest`** gains a `Timestamp int64` field stamped by
  room-service just before publishing to the stream. This is the stable
  seed for all derived IDs downstream.

### Role-update spec (2026-04-13)

Validation rule table gains one row between step 7 (promote "already owner")
and step 8 (last-owner guard):

> **7c. Promote (`newRole == RoleOwner`):** reject only when the target is
> provably org-only — `HasOrgMembership == true && HasIndividualMembership
> == false`. Return `errPromoteRequiresIndividual` ("only individual
> members can be promoted to owner"). Subscription-only members (both
> flags false, e.g., a room with no org sources where membership is implied
> by the subscription row alone) are promotable; the guard only rejects
> users whose membership is demonstrably sourced from an org.
>
> Rationale: owners must hold an individual membership source. Remove-member's
> dual-membership demote path relies on this — stripping the owner role
> during individual-leave is only sound when the role can only be held
> alongside an individual entry. Without this check, an org-only owner can
> be promoted, after which removing their org silently deletes their
> subscription and can zero out the room's owner count.

This validation is a second `GetSubscriptionWithMembership` lookup on the
promote path. Demote remains a single query.

## Implementation surface

### `pkg/model`

```go
type RemoveMemberRequest struct {
    // ... existing fields ...
    Timestamp int64 `json:"timestamp" bson:"timestamp"` // NEW, set by room-service
}
```

### `room-service/helper.go`

```go
var errPromoteRequiresIndividual = errors.New(
    "only individual members can be promoted to owner")
```

Added to the `sanitizeError` whitelist.

### `room-service/handler.go`

- `handleRemoveMember`: stamp `req.Timestamp = time.Now().UTC().UnixMilli()`
  before `publishToStream` on both individual and org branches.
- `handleUpdateRole`: after the role-change validity checks, if promoting,
  call `GetSubscriptionWithMembership` and reject when
  `!HasIndividualMembership`.

### `room-worker/store.go`

```go
// Replace IncrementUserCount / DecrementUserCount
ReconcileUserCount(ctx context.Context, roomID string) error
```

`MongoStore.ReconcileUserCount` queries `subscriptions.count({roomId})` and
`$set`s `rooms.userCount` to that value. Idempotent.

### `room-worker/handler.go`

- `dedupNamespace`: fixed `uuid.UUID` used as the SHA1 namespace for all
  derived dedup IDs (sysMsg `Message.ID` and `Nats-Msg-Id` headers).
- `idgen.DeriveID(seed string) string`: SHA-256 → base62, 17 chars.
  Produces the user-visible `Message.ID` for system messages.
- `deriveDedupID(seed string) string`: wraps `uuid.NewSHA1`. Produces
  `Nats-Msg-Id` UUIDs for JetStream stream publishes (MESSAGES_CANONICAL,
  OUTBOX). Seeds are operation-scoped so different publishes from the same
  request don't collide:
  - `addmembers:{roomID}:{requesterAccount}:{req.Timestamp}:{sortedAccounts}:{sortedOrgs}` — add-member sysMsg Message.ID (includes sorted accounts / orgs / requester so two distinct requests in the same millisecond don't collide)
  - `addmembers-msg:<addmembers-seed>` — add-member MESSAGES_CANONICAL dedup (reuses the hardened seed)
  - `addmembers-outbox:{roomID}:{destSiteID}:{req.Timestamp}:{sortedSiteAccounts}` — add-member OUTBOX dedup (per destination site, scoped by that site's account slice)
  - `rmindiv:{roomID}:{account}:{req.Timestamp}` — individual remove sysMsg ID
  - `rmindiv-msg:{roomID}:{account}:{req.Timestamp}` — individual remove MESSAGES_CANONICAL dedup
  - `rmindiv-outbox:{roomID}:{account}:{destSiteID}:{req.Timestamp}` — individual remove OUTBOX dedup
  - `rmorg:{roomID}:{orgID}:{req.Timestamp}` — org remove sysMsg ID
  - `rmorg-msg:{roomID}:{orgID}:{req.Timestamp}` — org remove MESSAGES_CANONICAL dedup
  - `rmorg-outbox:{roomID}:{orgID}:{destSiteID}:{req.Timestamp}` — org remove OUTBOX dedup
  - `roleupd-outbox:{roomID}:{account}:{newRole}:{req.Timestamp}` — role-update OUTBOX dedup. `UpdateRoleRequest.Timestamp` is stamped by room-service (same pattern as `RemoveMemberRequest.Timestamp`) so the header is stable across JetStream redeliveries.

```go
type PublishFunc func(ctx context.Context, subj string, data []byte, msgID string) error
```

Replaces the 3-arg `publish` closure on `Handler`. Call sites pass `""` for
core-NATS (user-scoped events, room-event fanout) and a derived ID for
JetStream stream publishes.

### `room-worker/main.go`

```go
handler := NewHandler(store, cfg.SiteID, func(ctx context.Context, subj string, data []byte, msgID string) error {
    if msgID == "" {
        return nc.Publish(ctx, subj, data)
    }
    msg := &nats.Msg{
        Subject: subj, Data: data,
        Header:  nats.Header{"Nats-Msg-Id": []string{msgID}},
    }
    return nc.PublishMsg(ctx, msg)
})
```

## Why this design

- **Set-to-actual-count beats tracked deltas.** The `$inc` approach requires
  every flow to precisely account for its own write, and has no recovery if
  the counter write is separated from the subscription write by a crash.
  `$set` from an authoritative source (the subscription collection)
  converges to the correct value from any drifted state.
- **Stable seeds over stable UUIDs embedded in requests.** We already have
  `req.Timestamp` on add/remove. Deriving IDs from it at the worker is
  cheaper than threading new `RequestID` fields through every model and
  handler, and it keeps the wire format lean.
- **Dedup at the stream edge, not the consumer.** `Nats-Msg-Id` moves
  idempotency responsibility to the JetStream broker. Consumers
  (message-worker, broadcast-worker, inbox-worker) don't need to learn
  about dedup — they benefit automatically.
- **Invariant enforcement in room-service, not room-worker.** The
  `errPromoteRequiresIndividual` check belongs in the request path because
  validation is room-service's job. Room-worker's dual-membership demote
  path remains unchanged and continues to rely on the invariant.
- **No guards on org-remove.** Bug 6 (no last-owner / last-member guard on
  org removal) is obviated by bug 5: with the invariant "org members cannot
  be owners" now enforced at promotion time, org removal cannot remove an
  owner and therefore cannot zero-out the owner count. Adding guards would
  be defense-in-depth but also dead code under a maintained invariant.

## Testing

### Unit tests added

- `TestDeriveDedupID_StableAcrossCalls` — same seed yields same UUID;
  different seeds differ.
- `TestHandler_ProcessRemoveIndividual_OutboxFailurePropagates` — injects
  an outbox-only publish error; asserts `processRemoveMember` returns it.
- `TestHandler_ProcessRemoveOrg_OutboxFailurePropagates` — same for the
  org-removal path.
- `TestHandler_UpdateRole_PromoteOrgOnlyRejected` — org-only target; asserts
  `errPromoteRequiresIndividual`.

### Unit tests updated

- All `TestHandler_*` closures took `(ctx, subj, data) error`; now take a
  fourth `msgID string` parameter.
- `TestHandler_UpdateRole_Success` / `_PublishError` now mock
  `GetSubscriptionWithMembership` with `HasIndividualMembership: true`.
- `TestHandler_ProcessRemoveIndividual_ReconcileUserCountError` replaces the
  former `DecrementUserCountError` test.

### Integration test updated

- `TestMongoStore_ReconcileUserCount_Integration` — seeds 3 subscriptions
  against a stale `userCount=10` room, asserts reconciliation produces 3,
  and asserts a second run is idempotent.

## Deferred / not in scope

- **Core-NATS event payloads don't carry a stable event ID.** Clients that
  want to dedup duplicate `SubscriptionUpdateEvent` / `MemberRemoveEvent`
  toasts must do so on the payload's stable fields (e.g. the contained
  `Message.ID` when applicable, or the `Subscription.RoomID + Action` tuple).
  Adding a first-class `Event.ID` field to these structs is left as a future
  change.
- **DB-level assertion on invariant.** With the role-update guard in place,
  an "org-only owner" state shouldn't be reachable via the API. A startup
  sanity check that scans `subscriptions` for owner rows lacking a matching
  individual `room_members` entry would catch migrations or admin tooling
  that bypass the API. Left as future work.
