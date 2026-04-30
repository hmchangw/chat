# Add Model Fields: Room.MinUserLastSeenAt and Subscription.ThreadUnread/Alert

**Date:** 2026-04-30
**Branch:** `claude/add-model-fields-njdU6`

## Goal

Extend two existing domain models in `pkg/model/` with new fields to support
upcoming features:

- `Room.MinUserLastSeenAt` — earliest "last seen" timestamp across all
  subscribers of a room (used to bound retention/cleanup decisions on a room).
- `Subscription.ThreadUnread` — the set of `ThreadParentMessageID`s in this
  room that have unread replies for this user.
- `Subscription.Alert` — per-subscription flag indicating whether this user
  should be alerted (e.g., desktop/mobile notification preference) for activity
  in the room.

This spec covers only the model definitions and their JSON/BSON round-trip
tests. Store implementations, indexes, handler logic, and migrations are out
of scope.

## Field Definitions

### `Room` (in `pkg/model/room.go`)

Add one field:

```go
MinUserLastSeenAt *time.Time `json:"minUserLastSeenAt,omitempty" bson:"minUserLastSeenAt,omitempty"`
```

- Pointer-to-time so absence is distinguishable from the zero time.
- `omitempty` on both tags so a nil value is omitted on the wire and in
  Mongo, matching the existing `LastMsgAt` and `LastMentionAllAt` pattern.

### `Subscription` (in `pkg/model/subscription.go`)

Add two fields:

```go
ThreadUnread []string `json:"threadUnread,omitempty" bson:"threadUnread,omitempty"`
Alert        bool     `json:"alert" bson:"alert"`
```

- `ThreadUnread` is a slice of `ThreadParentMessageID` strings. `omitempty`
  on both tags so empty/nil slices are not emitted, matching the project's
  preference for compact wire/storage representations.
- `Alert` is always emitted (no `omitempty`), matching the existing
  `HasMention` pattern. A missing field on legacy documents decodes to
  `false`, which is the desired default.

## Backward Compatibility

- Existing Mongo documents that pre-date these fields decode cleanly:
  `MinUserLastSeenAt` becomes `nil`, `ThreadUnread` becomes `nil`, `Alert`
  becomes `false`. No migration required.
- JSON consumers that ignore unknown fields are unaffected. New consumers
  that need these fields can read them from the existing model structs.

## Testing (TDD)

Follow Red → Green → Refactor in `pkg/model/model_test.go`. All new tests use
the existing `roundTrip` helper or follow the existing
"omitted-when-zero/nil" subtest pattern.

### Red phase tests

1. **Extend `TestRoomJSON`** — populate `MinUserLastSeenAt` and verify it
   round-trips through JSON.
2. **Extend `TestRoomJSON_NilTimestampsOmitted`** — assert `minUserLastSeenAt`
   is omitted from JSON when nil and decodes back to nil.
3. **Extend `TestSubscriptionJSON`** — populate `ThreadUnread` with a couple
   of parent message IDs and set `Alert: true`; verify round-trip equality.
4. **New subtest under `TestSubscriptionJSON`** (or a sibling `TestSubscriptionJSON_*`):
   - `threadUnread` is omitted from JSON when nil/empty.
   - `alert` is present in JSON even when `false` (no `omitempty`).

Run the tests and confirm they fail before adding the struct fields.

### Green phase

Add the fields exactly as defined above. Re-run `make test SERVICE=pkg/model`
(or the equivalent invocation) and confirm all tests pass with `-race`.

### Coverage

These are pure data-shape additions to a heavily-tested model package. The
new tests give meaningful coverage of every new field's serialization
behavior on both populated and zero-value paths, satisfying the project's
80% minimum and aligning with the 90%+ target for `pkg/`.

## Out of Scope

- Store interfaces, MongoDB filters/projections, indexes
- Handlers, NATS subjects, event payloads
- Cassandra schema (these fields do not flow into message history)
- Population logic (when/how `MinUserLastSeenAt` is computed, when
  `ThreadUnread` is mutated, who toggles `Alert`)

These will be designed separately when the consuming features land.

## Workflow

1. Red: add the failing tests in `pkg/model/model_test.go`.
2. Green: add the three fields.
3. Run lint and tests (`make lint`, `make test`).
4. Commit on `claude/add-model-fields-njdU6`.
5. Push to origin with `-u`.
