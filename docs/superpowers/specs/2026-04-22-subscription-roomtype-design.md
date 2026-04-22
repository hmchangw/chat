# Subscription RoomType Field Design

## Summary

Add an optional `RoomType` field to `model.Subscription`, typed as the existing `model.RoomType` defined in `pkg/model/room.go` (`group` / `channel` / `dm` / `discussion`). The field is a denormalization of `Room.Type` onto each subscription document so consumers reading a user's subscriptions do not have to perform a secondary `Room` lookup.

This spec covers the model struct change, accompanying model tests, and **populating `RoomType` at every site that creates or upserts a `Subscription`**. Backfill of pre-existing MongoDB documents is explicitly out of scope, and no consumer is changed to read `Subscription.RoomType` in this PR — those changes are deferred to follow-up PRs.

## Motivation

`Subscription` currently carries the minimum identity and per-user state (`RoomID`, `SiteID`, `Roles`, `JoinedAt`, `LastSeenAt`, `HasMention`, `HistorySharedSince`). Several call sites and downstream consumers need the owning room's type:

- **Read paths** listing a user's subscriptions (e.g. the frontend distinguishing DMs, groups, and channels) must currently issue a second query to the `rooms` collection keyed by `roomId`.
- **Workers** that vary behavior per room kind (broadcast, notification) would benefit from having `roomType` on the subscription payload directly, removing the need for a round-trip lookup.
- **Cross-site federation**: subscription-related events that cross the OUTBOX/INBOX boundary must currently be joined against a locally replicated room on the receiving site. Carrying `roomType` inline avoids that dependency.

A single denormalized field on `Subscription` addresses all three use cases with one small, safe change.

## Scope

**In scope**

- Add a `RoomType` field (type: `model.RoomType`) to `model.Subscription` in `pkg/model/subscription.go`, with `json:"roomType,omitempty"` and `bson:"roomType,omitempty"` tags.
- Add a new `RoomTypeDiscussion = "discussion"` constant to `model.RoomType`.
- Extend `TestSubscriptionJSON` in `pkg/model/model_test.go` so its fixture covers the new field; add a test asserting `roomType` is omitted when empty; add `TestRoomTypeDiscussion`.
- Populate `Subscription.RoomType` at every write site in this PR:
  - `room-service/handler.go` — owner subscription on room creation (room already in scope).
  - `room-worker` `processAddMembers` — bulk subscription insert (room already fetched).
  - `room-worker` `processInvite` — single subscription insert; adds a `GetRoom` lookup to obtain the type since the `InviteMemberRequest` payload doesn't carry it.
  - `inbox-worker` — cross-site subscription insert. Requires adding a new `RoomType` field to `model.MemberAddEvent` (the inner payload of `OutboxEvent` for `member_added`), populated by `room-worker` at both publish sites (where `room.Type` is already in scope). `inbox-worker` then reads `event.RoomType` and stores it on the subscription. No extra `GetRoom` lookup at the destination site.
- Add or extend handler tests for each of the four population sites to assert `sub.RoomType` is set correctly.

**Out of scope** (explicitly deferred to follow-up PRs)

- Backfilling existing MongoDB `subscriptions` documents to add `roomType` to rows written before this PR ships.
- Any consumer reading `Subscription.RoomType` (e.g. flipping `broadcast-worker/handler.go:73` from `switch room.Type` to `switch sub.RoomType`). This will be done in a follow-up PR after backfill has run.
- Adding `RoomType` to additional consumer events or OUTBOX/INBOX payloads beyond what already exists.
- Adding `RoomType` to `InviteMemberRequest` to avoid the new `GetRoom` call in `processInvite` (a possible future optimization).
- Any UI, broadcast, or notification logic that branches on the new field.

## Design

### Field placement and tags

The field is inserted immediately after `RoomID`, grouping the room-identity and room-metadata fields together. Any future denormalized room fields (e.g. room name) should be added in the same block.

```go
type Subscription struct {
    ID                 string           `json:"id" bson:"_id"`
    User               SubscriptionUser `json:"u" bson:"u"`
    RoomID             string           `json:"roomId" bson:"roomId"`
    RoomType           RoomType         `json:"roomType,omitempty" bson:"roomType,omitempty"`
    SiteID             string           `json:"siteId" bson:"siteId"`
    Roles              []Role           `json:"roles" bson:"roles"`
    HistorySharedSince *time.Time       `json:"historySharedSince,omitempty" bson:"historySharedSince,omitempty"`
    JoinedAt           time.Time        `json:"joinedAt" bson:"joinedAt"`
    LastSeenAt         time.Time        `json:"lastSeenAt" bson:"lastSeenAt"`
    HasMention         bool             `json:"hasMention" bson:"hasMention"`
}
```

### Type choice

The field uses the existing `model.RoomType` type (a defined string type in `pkg/model/room.go`), so valid values are constrained to the existing constants `RoomTypeGroup`, `RoomTypeChannel`, `RoomTypeDM`, and `RoomTypeDiscussion`. No new type is introduced.

### `omitempty` rationale

During the transition period (between this PR and the follow-up PR that populates the field), new and existing subscription documents in MongoDB will not have `roomType` set. `omitempty` ensures:

- Documents read from MongoDB that lack the field decode with `RoomType == ""` (Go zero value for a string alias) — safe.
- Documents written back out do not explicitly overwrite or introduce empty `roomType` fields on existing rows.
- JSON payloads crossing service boundaries remain compact when the field is not yet populated.

This matches the existing optional-field pattern used by `HistorySharedSince` on `Subscription` and `Restricted` on `Room`.

## Testing

### Model tests (`pkg/model/model_test.go`)

Follow the project TDD discipline: tests are written and seen failing before the struct change.

1. **Red — update `TestSubscriptionJSON` (around line 320)**: extend the constructed `Subscription` fixture with `RoomType: model.RoomTypeGroup`. Keep the existing round-trip assertion: marshal, unmarshal, `reflect.DeepEqual`. Run tests and confirm the file fails to compile because `Subscription.RoomType` does not exist yet.
2. **Red — add `TestSubscriptionJSON_RoomTypeOmittedWhenEmpty`**: construct a `Subscription` with the zero value for `RoomType`, marshal to JSON, unmarshal into `map[string]any`, assert `roomType` key is absent. Uses the same pattern as the existing `Restricted`-omitted assertion in `TestRoom_RestrictedJSON`.
3. **Green — add the field** to `pkg/model/subscription.go`. Tests pass.

### Handler tests for population sites

Each of the four write sites gets a unit test (TDD: red first, then green) asserting `sub.RoomType` matches the owning room's type:

- **room-service**: extend the `handleCreateRoom` test to assert the captured owner subscription has `RoomType` equal to the room's `Type`.
- **room-worker `processAddMembers`**: assert each subscription passed to `BulkCreateSubscriptions` has `RoomType` equal to the fetched room's `Type`.
- **room-worker `processInvite`**: assert the new `GetRoom` mock is called and the inserted subscription carries `RoomType` from that fetched room.
- **inbox-worker**: assert subscriptions inserted from an `InboxMemberEvent` carry the event's `RoomType`.

No store-implementation changes are required; the new field rides on the existing `bson` mapping.

## Risks

- **Mixed-population state in MongoDB.** After this PR ships, *new* subscriptions carry `roomType`; *existing* subscriptions do not. No reader depends on the field yet, so this is benign. The follow-up PR that wires `broadcast-worker` (or any other reader) MUST be preceded by a backfill, otherwise the type switch will fall through to `default` for pre-existing subscriptions and silently drop fan-out.
- **Extra `GetRoom` call in `processInvite`.** Adds one MongoDB read per invite. Acceptable cost; can be removed in a follow-up by extending `InviteMemberRequest` to carry `RoomType` at publish time.
- **MongoDB index hitting a now-missing field.** No existing index on the `subscriptions` collection references `roomType`, so `omitempty` documents are fully compatible. No migration needed.
- **BSON unmarshal of existing documents.** `RoomType` is a defined string type; existing documents that have no `roomType` key unmarshal into the zero value (`""`). No crash, no surprise. Safe.

## Rollout

No migration required for this PR. Existing MongoDB documents remain valid — Go struct unmarshal fills `RoomType` with `""` when the field is absent. Deploy order is irrelevant for this change because no runtime reader is introduced.

The follow-up PR that wires `broadcast-worker` to read `Subscription.RoomType` will include a one-shot Mongo backfill (idempotent: for every subscription where `roomType` is missing, look up the owning room and set `roomType = room.type`). The backfill MUST run before the new reader is enabled; otherwise pre-existing subscriptions decode with `RoomType == ""` and fan-out silently drops.
