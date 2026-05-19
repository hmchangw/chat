# BotDM Re-subscribe Upsert + `DisableNotification` Field

**Date:** 2026-05-19
**Branch:** `claude/fix-room-notification-settings-RqyO1`
**Scope:** `pkg/model`, `room-worker`

## Problem

A user can mute notifications on a botDM room (the user-facing toggle for that
field will land in a follow-up PR). When the same user later tries to "open the
app" again — which on the backend re-issues `chat.user.<account>.request.room.create`
or `chat.user.<account>.request.room.syncCreateDM` for the bot — the
subscription document already exists, so the current `BulkCreateSubscriptions`
swallows the duplicate-key error and returns silently. The pre-existing sub
keeps its old state, including `DisableNotification = true`, so the user
remains muted and the re-activation is a no-op.

The desired behaviour is: re-creating a botDM for a user who already has a
subscription **refreshes the mute/active state** on that subscription —
`DisableNotification` is cleared, `IsSubscribed` is set to `true`, and
`JoinedAt` is updated to the new acceptance timestamp. Runtime state
(`LastSeenAt`, `HasMention`, `ThreadUnread`, `Alert`) is preserved.

`IsSubscribed = true` only appears on the human side of a botDM today
(`inbox-worker/handler.go:241-246` hard-codes `false` everywhere else), so the
"re-join" semantics described above are conceptually botDM-only. All other
membership write paths (channel rooms, regular DMs, add-member) keep their
current safe insert-only behaviour, which is the right semantics for JetStream
redelivery idempotency in those flows.

## Scope

In scope:

- Add `DisableNotification bool` to `model.Subscription`.
- Add a new store method `BulkUpsertSubscriptions` that implements the re-join
  refresh semantics described above.
- Wire the two botDM call sites in `room-worker` to use the new method.
- Re-read canonical subs after the async-path upsert so the
  `subscription.update` / `MemberAddEvent` fan-out carries persisted state.

Explicitly out of scope (deferred to follow-up PRs):

- A user-facing toggle endpoint to set `DisableNotification`.
- `notification-worker` filtering: this PR does **not** change
  `notification-worker`. It will still fan notifications to every sub
  regardless of `DisableNotification`. End-to-end mute behaviour is wired up
  separately.
- Any change to channel-room, regular-DM, or add-member persistence semantics.

## Design

### 1. Model — `pkg/model/subscription.go`

Add a single field next to `Alert`:

```go
Alert               bool `json:"alert" bson:"alert"`
DisableNotification bool `json:"disableNotification" bson:"disableNotification"`
```

No `omitempty`. Matches the convention of `Alert` and `HasMention` and ensures
the bson document always carries an explicit value. Existing Mongo records
without the field decode to Go zero value (`false`) — the desired default
("notifications enabled") — so no migration is required.

Extend the `Subscription` round-trip case in `pkg/model/model_test.go` to set
`DisableNotification: true` so JSON and BSON marshalling are covered.

### 2. Subscription builder — `room-worker/handler.go`

`newSub` (`handler.go:1040`) constructs every subscription used by every
membership write path. The zero value of `DisableNotification` already gives
the desired default (`false`) on fresh inserts, so the `newSub` signature does
not change. Adding a parameter that is always `false` would be noise.

### 3. Store — `room-worker/store.go` + `room-worker/store_mongo.go`

Add a new interface method alongside `BulkCreateSubscriptions`:

```go
// BulkUpsertSubscriptions inserts the given subscriptions and, on a
// (roomId, u.account) collision with an existing document, refreshes the
// re-activation fields (DisableNotification → false, IsSubscribed,
// JoinedAt) while preserving the existing document's runtime state
// (LastSeenAt, HasMention, ThreadUnread, Alert) and identity (_id, u).
// Intended for botDM re-creation paths only; channel/DM/add-member paths
// must continue to use BulkCreateSubscriptions for safe redelivery
// idempotency.
BulkUpsertSubscriptions(ctx context.Context, subs []*model.Subscription) error
```

Implementation in `store_mongo.go` uses `BulkWrite` with one
`UpdateOneModel` per sub:

- **Filter:** `{"roomId": sub.RoomID, "u.account": sub.User.Account}`.
- **`$set`:** `disableNotification: false`, `isSubscribed: sub.IsSubscribed`,
  `joinedAt: sub.JoinedAt`. These are the re-activation fields.
- **`$setOnInsert`:** `_id, u, roomId, siteId, roomType, name, roles,
  hasMention: false, alert: false`. These are immutable identity fields plus
  zero-value initialisers for the runtime fields, applied only on first
  insert.
- **Upsert:** `true`. Unordered so partial collisions don't halt the batch.

On the update branch, runtime fields (`LastSeenAt`, `HasMention`,
`ThreadUnread`, `Alert`) are not in `$set`, so Mongo preserves whatever it
currently holds — an unread mention that arrived between mute and
re-subscribe is not erased. On the insert branch, `$setOnInsert`
initialises `hasMention` and `alert` to `false`; `lastSeenAt` and
`threadUnread` remain unset (their bson tags are `omitempty`).

`mock_store_test.go` is regenerated via `make generate`.

### 4. Wire into botDM paths — `room-worker/handler.go`

Two call sites switch from `BulkCreateSubscriptions` to
`BulkUpsertSubscriptions`:

1. `processCreateRoom` botDM branch — `handler.go:1163`. The regular-DM
   branch immediately above (`buildDMSubs` at `handler.go:1161`) stays on
   `BulkCreateSubscriptions`.
2. `processSyncCreateDM` botDM branch — `handler.go:1568`. The regular-DM
   path in the same function stays on `BulkCreateSubscriptions`.

Concretely, both sites become a conditional dispatch:

```go
if roomType == model.RoomTypeBotDM {
    err = h.store.BulkUpsertSubscriptions(ctx, subs)
} else {
    err = h.store.BulkCreateSubscriptions(ctx, subs)
}
```

All other call sites (`processCreateRoomChannel` at `handler.go:1229`,
add-member flow) are unchanged.

### 5. Re-read after upsert (async path)

The sync path (`processSyncCreateDM`) already re-reads via
`FindDMSubscription` after the bulk call (`handler.go:1574-1582`) so the
fan-out carries canonical `_id` and `JoinedAt`. No change there.

The async `processCreateRoom` botDM branch currently hands the in-memory
`subs` directly to `finishCreateRoom` (`handler.go:1166`). On an upsert that
hit an existing row, the in-memory `sub.ID` and `sub.JoinedAt` may not match
the persisted document. Mirror the sync path: after a successful
`BulkUpsertSubscriptions` in the async botDM branch, call
`FindDMSubscription` for `(requester.Account, counterpart.Account)` and
`(counterpart.Account, requester.Account)`, replace the in-memory subs with
the canonical pair, and pass that into `finishCreateRoom`. This keeps
`subscription.update` events, `MemberAddEvent`s, and reconcile counts
operating on persisted state.

## Testing

Per CLAUDE.md, all new code goes through the Red-Green-Refactor TDD cycle.

### Unit tests — `room-worker/handler_test.go`

- `processCreateRoom` botDM happy path: expect `BulkUpsertSubscriptions`
  (not `BulkCreateSubscriptions`) and the two new `FindDMSubscription`
  re-reads. Verify the events published downstream carry the re-read sub
  values (different `_id` than the in-memory pre-upsert sub).
- `processCreateRoom` regular-DM, `processCreateRoomChannel`, and
  add-member paths: regression-guard that they still expect
  `BulkCreateSubscriptions` and **not** `BulkUpsertSubscriptions`.
- `processSyncCreateDM` botDM happy path: expect
  `BulkUpsertSubscriptions`. Regular-DM branch still expects
  `BulkCreateSubscriptions`.

### Integration tests — `room-worker/integration_test.go`

- **Re-join refresh:** Pre-seed a botDM subscription with
  `DisableNotification = true`, `IsSubscribed = false`, and an old
  `JoinedAt`. Also set `HasMention = true`, `Alert = true`, and a
  `LastSeenAt`. Run `processCreateRoom` for the same (roomID, account)
  pair with a new `acceptedAt`. Assert:
  - `_id` unchanged from the pre-seeded row.
  - `DisableNotification == false`, `IsSubscribed == true`,
    `JoinedAt == new acceptedAt`.
  - `HasMention == true`, `Alert == true`, `LastSeenAt` unchanged
    (runtime state preserved).
- **Fresh insert:** No pre-seeded row. Run `processCreateRoom` botDM.
  Assert sub is created with `DisableNotification == false`,
  `IsSubscribed == true` (human side) / `false` (bot side),
  `HasMention == false`, `Alert == false`.
- **Regular-DM regression:** Pre-seed a regular-DM sub with
  `DisableNotification = true`. Run `processCreateRoom` for the same
  pair (regular DM, not botDM). Assert `DisableNotification` is still
  `true` — regular-DM path does **not** upsert, so the pre-existing
  state is preserved (current behaviour, regression guard).

### Model tests

`pkg/model/model_test.go` Subscription round-trip case extended to set
`DisableNotification = true`, verifying both JSON and BSON tag correctness.

### Coverage

Target ≥90% on the touched files (`room-worker/handler.go`,
`room-worker/store_mongo.go`) per the project's coverage rules. Cover both
the upsert-as-insert and upsert-as-update branches, the re-read failure
path, and the regular-DM no-upsert regression.

## Risks & Open Questions

- **Concurrent mute + re-subscribe race.** If the user mutes
  (`DisableNotification = true`) at the same instant they re-open the app
  (triggering this upsert), the upsert can overwrite the just-muted value.
  Mitigation: the user-facing mute endpoint (out of scope here) lands later
  and will be the dominant source of mutes; an accidental clear from a
  racing re-creation is recoverable by re-muting. Calling this out so the
  follow-up PR's design can address it if needed.

- **`JoinedAt` semantics drift.** Refreshing `JoinedAt` on every botDM
  re-creation means it is now "last re-join time", not "first join time".
  No current consumer treats `JoinedAt` as "first join" for botDMs, but
  worth flagging in case downstream analytics rely on it.

## Out of Scope (Follow-Up PRs)

- User-facing endpoint to toggle `DisableNotification` (probably an HTTP
  PATCH on the subscription, or a NATS RPC; routed through `room-worker`
  or `auth-service`).
- `notification-worker` filter: skip subs where
  `DisableNotification == true` before fan-out.
- Frontend mute toggle UI.
- `docs/client-api.md` update — only required once the client-facing
  toggle endpoint lands (this PR exposes no new request/response schema).
