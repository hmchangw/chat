# Subscription RoomType Field Design

## Summary

Add an optional `RoomType` field to `model.Subscription`, typed as the existing `model.RoomType` defined in `pkg/model/room.go` (`group` / `channel` / `dm`). The field is a denormalization of `Room.Type` onto each subscription document so consumers reading a user's subscriptions do not have to perform a secondary `Room` lookup.

This spec covers only the model struct change and accompanying model tests. Population at subscription-creation time and backfill of existing documents are explicitly deferred to follow-up PRs.

## Motivation

`Subscription` currently carries the minimum identity and per-user state (`RoomID`, `SiteID`, `Roles`, `JoinedAt`, `LastSeenAt`, `HasMention`, `HistorySharedSince`). Several call sites and downstream consumers need the owning room's type:

- **Read paths** listing a user's subscriptions (e.g. the frontend distinguishing DMs, groups, and channels) must currently issue a second query to the `rooms` collection keyed by `roomId`.
- **Workers** that vary behavior per room kind (broadcast, notification) would benefit from having `roomType` on the subscription payload directly, removing the need for a round-trip lookup.
- **Cross-site federation**: subscription-related events that cross the OUTBOX/INBOX boundary must currently be joined against a locally replicated room on the receiving site. Carrying `roomType` inline avoids that dependency.

A single denormalized field on `Subscription` addresses all three use cases with one small, safe change.

## Scope

**In scope**

- Add a `RoomType` field (type: `model.RoomType`) to `model.Subscription` in `pkg/model/subscription.go`, with `json:"roomType,omitempty"` and `bson:"roomType,omitempty"` tags.
- Extend `TestSubscriptionJSON` in `pkg/model/model_test.go` so its fixture covers the new field.
- Add a new subtest asserting that when `RoomType` is the zero value (`""`), the `roomType` key is omitted from marshaled JSON.

**Out of scope** (explicitly deferred to follow-up PRs)

- Populating `RoomType` in `room-service/handler.go` when a new `Subscription` is constructed.
- Populating `RoomType` in any other service that writes subscriptions.
- Backfilling existing MongoDB `subscriptions` documents to add `roomType`.
- Adding `RoomType` to any store interface methods, consumer events, or OUTBOX/INBOX payloads.
- Any UI, broadcast, or notification logic that reads the new field.

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

The field uses the existing `model.RoomType` type (a defined string type in `pkg/model/room.go`), so valid values are constrained to the existing constants `RoomTypeGroup`, `RoomTypeChannel`, and `RoomTypeDM`. No new type is introduced.

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

### No handler or store tests

No production code paths read, write, or branch on `Subscription.RoomType` in this PR, so no handler or store tests are added. When the follow-up PR wires population in `room-service`, it will add the relevant handler-level tests.

## Risks

- **Empty `roomType` surfacing to downstream readers.** Until the follow-up PR lands, all new and existing subscriptions decode with `RoomType == ""`. Any consumer that switches on `roomType` before the follow-up PR lands will see only the zero value. Mitigation: this PR does not add any new consumer code paths that read the field; document it as deferred.
- **MongoDB index hitting a now-missing field.** No existing index on the `subscriptions` collection references `roomType`, so `omitempty` documents are fully compatible. No migration needed.
- **BSON unmarshal of existing documents.** `RoomType` is a defined string type; existing documents that have no `roomType` key unmarshal into the zero value (`""`). No crash, no surprise. Safe.

## Rollout

No migration required for this PR. Existing MongoDB documents remain valid — Go struct unmarshal fills `RoomType` with `""` when the field is absent. Deploy order is irrelevant for this change because no runtime reader is introduced.

The follow-up PR that populates `RoomType` at subscription-creation time will include its own backfill plan for pre-existing documents.
