# Nullable `Room` timestamps — design

## Summary

Change `LastMsgAt` and `LastMentionAllAt` on the Mongo domain type `model.Room` from `time.Time` to `*time.Time`, with `json:"...,omitempty"` **and** `bson:"...,omitempty"` on both fields. A freshly created room stores neither field in BSON (rather than writing the `time.Time` zero value `0001-01-01T00:00:00Z`), and downstream code can tell "never had a message" (nil) from "there was a message at time X" (non-nil pointer) without having to rely on an `.IsZero()` convention.

This is the domain-side follow-up to the `RoomInfo` RPC change in `docs/superpowers/specs/2026-04-23-nullable-room-info-timestamps-design.md`; together they give the system consistent nullable semantics from storage through the wire.

## Motivation

The Go zero value for `time.Time` (`0001-01-01T00:00:00Z`) is not a meaningful "no value" marker — it's a real timestamp that happens to be rare. Today, a newly created `model.Room` that has never received a message stores `lastMsgAt: ISODate("0001-01-01T00:00:00Z")` in Mongo. Readers must remember to call `.IsZero()` to interpret that as absence. Pointer semantics make absence explicit at the type level.

This also aligns `Room`'s storage shape with `RoomInfo`'s wire shape: both represent "no last message" as the field being absent.

## Scope

### In scope

- `model.Room.LastMsgAt` → `*time.Time` with `json:"lastMsgAt,omitempty" bson:"lastMsgAt,omitempty"`
- `model.Room.LastMentionAllAt` → `*time.Time` with `json:"lastMentionAllAt,omitempty" bson:"lastMentionAllAt,omitempty"`
- Single production reader: `aggregateRoomInfo` in `room-service/handler.go` — add nil guard before `.IsZero()` / pointer deref
- `pkg/model/model_test.go` — fix `TestRoomJSON` to use pointer literals; rewrite the `roundTrip[T comparable]` helper to use `reflect.DeepEqual` (dropping the `comparable` constraint) so it supports pointer-field types; add a new `TestRoomJSON_NilTimestampsOmitted` test
- `broadcast-worker/integration_test.go` — three `assert.WithinDuration(…, room.LastMsgAt, …)` / `…, room.LastMentionAllAt, …` sites gain `require.NotNil` + pointer deref
- `room-service/handler_test.go` — seed `LastMsgAt: now` literal becomes `LastMsgAt: &now`
- `room-service/integration_test.go` — six seed literals become pointer form; one `.IsZero()` read becomes `nil || IsZero()`

### Out of scope (explicit)

- `model.ThreadRoom.LastMsgAt` (`pkg/model/threadroom.go:10`) — stays `time.Time`; a thread room always has a last message by construction (it's created on the first reply)
- `model.RoomEvent.LastMsgAt` (`pkg/model/event.go:140`) — stays `time.Time`; event DTOs are always emitted for a specific message, so "no value" doesn't occur
- `model.Subscription` timestamp fields — not touched
- `broadcast-worker/store_mongo.go` `UpdateRoomOnNewMessage` — writes raw `bson.M{"lastMsgAt": msgAt}`; doesn't go through the struct, so the type change has zero effect there
- `message-worker` — all `LastMsgAt` references in this service are on `model.ThreadRoom`, not `model.Room`
- No Mongo data migration — pre-existing rooms that stored `lastMsgAt: ISODate("0001-01-01T00:00:00Z")` continue to work (see "Compatibility")

## Compatibility

### Read path (Mongo → Go)

| Existing document state | Decoded `*time.Time` | `aggregateRoomInfo` output |
|-------------------------|----------------------|----------------------------|
| Field absent | `nil` | absent in `RoomInfo` (JSON omitted) |
| Field present, zero time | non-nil pointer to zero-time | absent in `RoomInfo` — the `!r.LastMsgAt.IsZero()` guard catches it |
| Field present, real timestamp | non-nil pointer to real time | present in `RoomInfo` as Unix millis |

The producer's guard is `if r.LastMsgAt != nil && !r.LastMsgAt.IsZero()` — both checks are needed: the first for new (post-change) rooms with nil, the second for legacy rooms carrying a stored zero-time.

### Write path (Go → Mongo)

- `room-service` `handleCreateRoom` leaves both pointer fields unset (nil). With `bson:"...,omitempty"`, they're absent from the inserted BSON document. New rooms no longer accumulate zero-time pollution.
- `broadcast-worker` `UpdateRoomOnNewMessage` writes via raw `bson.M{"lastMsgAt": msgAt}`. No code path there changes; it still writes the real timestamp via `$set`.

### JSON wire

`model.Room` is internal — it is not served to external clients. The only JSON marshaling of `Room` happens in tests. That said, the wire change for zero-time values differs between before and after:

- Before: `"lastMsgAt":"0001-01-01T00:00:00Z"` (RFC3339 zero emitted)
- After: field absent (nil pointer + `omitempty`)

No external consumer depends on the pre-change behavior.

## Code changes

### `pkg/model/room.go` (lines 20, 22)

```go
type Room struct {
    ID               string     `json:"id" bson:"_id"`
    Name             string     `json:"name" bson:"name"`
    Type             RoomType   `json:"type" bson:"type"`
    CreatedBy        string     `json:"createdBy" bson:"createdBy"`
    SiteID           string     `json:"siteId" bson:"siteId"`
    UserCount        int        `json:"userCount" bson:"userCount"`
    LastMsgAt        *time.Time `json:"lastMsgAt,omitempty" bson:"lastMsgAt,omitempty"`
    LastMsgID        string     `json:"lastMsgId" bson:"lastMsgId"`
    LastMentionAllAt *time.Time `json:"lastMentionAllAt,omitempty" bson:"lastMentionAllAt,omitempty"`
    CreatedAt        time.Time  `json:"createdAt" bson:"createdAt"`
    UpdatedAt        time.Time  `json:"updatedAt" bson:"updatedAt"`
    Restricted       bool       `json:"restricted,omitempty" bson:"restricted,omitempty"`
}
```

### `room-service/handler.go` (`aggregateRoomInfo`, currently lines 664–670)

```go
if r.LastMsgAt != nil && !r.LastMsgAt.IsZero() {
    ms := r.LastMsgAt.UTC().UnixMilli()
    entry.LastMsgAt = &ms
}
if r.LastMentionAllAt != nil && !r.LastMentionAllAt.IsZero() {
    ms := r.LastMentionAllAt.UTC().UnixMilli()
    entry.LastMentionAllAt = &ms
}
```

## Test changes

### `pkg/model/model_test.go`

**Rewrite the `roundTrip` helper** (currently lines 1280–1293):

```go
// roundTrip marshals src to JSON, unmarshals into dst, and compares.
func roundTrip[T any](t *testing.T, src *T, dst *T) {
    t.Helper()
    data, err := json.Marshal(src)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    if err := json.Unmarshal(data, dst); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if !reflect.DeepEqual(*src, *dst) {
        t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", *dst, *src)
    }
}
```

The `comparable` constraint is dropped in favor of `any`, and the body uses `reflect.DeepEqual`. This is strictly more permissive than `==`; every existing caller (`TestUserJSON`, `TestThreadRoomJSON`, etc.) continues to pass because their structs are still deeply equal after a round-trip.

**Fix `TestRoomJSON`** (currently lines 31–42):

```go
func TestRoomJSON(t *testing.T) {
    lastMsg := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
    lastMention := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
    r := model.Room{
        ID: "r1", Name: "general", Type: model.RoomTypeGroup,
        CreatedBy: "u1", SiteID: "site-a", UserCount: 5,
        LastMsgAt:        &lastMsg,
        LastMsgID:        "m1",
        LastMentionAllAt: &lastMention,
        CreatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
        UpdatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
    }
    roundTrip(t, &r, &model.Room{})
}
```

**Add `TestRoomJSON_NilTimestampsOmitted`** — pins down the new nullable semantics at the `Room` JSON level:

```go
func TestRoomJSON_NilTimestampsOmitted(t *testing.T) {
    r := model.Room{
        ID: "r1", Name: "general", Type: model.RoomTypeGroup,
        CreatedBy: "u1", SiteID: "site-a", UserCount: 1,
        CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
        UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
    }
    data, err := json.Marshal(&r)
    require.NoError(t, err)
    var raw map[string]any
    require.NoError(t, json.Unmarshal(data, &raw))
    _, hasMsg := raw["lastMsgAt"]
    assert.False(t, hasMsg, "nil LastMsgAt must be omitted")
    _, hasMention := raw["lastMentionAllAt"]
    assert.False(t, hasMention, "nil LastMentionAllAt must be omitted")
}
```

### `broadcast-worker/integration_test.go`

Lines 129, 162, 271 — three sites of the form

```go
assert.WithinDuration(t, msgTime, room.LastMsgAt, time.Millisecond)
```

become

```go
require.NotNil(t, room.LastMsgAt)
assert.WithinDuration(t, msgTime, *room.LastMsgAt, time.Millisecond)
```

(Line 162 is the `LastMentionAllAt` variant — same transform.)

### `room-service/handler_test.go`

Line 1516 (`LastMsgAt set → correct millis` case): seed `LastMsgAt: now` becomes `LastMsgAt: &now`. The existing assertion (`require.NotNil(...); assert.Equal(..., *resp.Rooms[0].LastMsgAt)`) is already pointer-aware and continues to work.

The `LastMsgAt zero-time in Mongo → nil in response` case (added in the prior plan) already omits `LastMsgAt` from the seeded `model.Room` — a nil `*time.Time` — which is exactly the condition we now want to test, so no change there.

### `room-service/integration_test.go`

- Lines 928–932, 988, 990: six `LastMsgAt: now` / `LastMsgAt: lastMsg` / `LastMsgAt: lastMsg.Add(...)` seeds. Each becomes pointer-valued. Recommended pattern — hoist pointers to local variables before the slice literal:

```go
t1 := now
t2 := now.Add(1 * time.Second)
...
seed := []model.Room{
    {ID: "r1", ..., LastMsgAt: &t1},
    {ID: "r2", ..., LastMsgAt: &t2},
    ...
}
```

Do NOT take the address of a loop-variable-style expression like `&now.Add(...)` — that isn't valid Go. Explicit locals avoid aliasing bugs.

- Line 958 (`if r.LastMsgAt.IsZero()`) becomes `if r.LastMsgAt == nil || r.LastMsgAt.IsZero()`.

## TDD discipline (per CLAUDE.md §4)

1. **Red:** update all test sites (seed literals take `&`, asserts `require.NotNil` + deref, `roundTrip` still referenced as-is). Struct still `time.Time` → compile fails at every pointer site.
2. **Green:** flip the two struct fields in `pkg/model/room.go`, add nil guards in `aggregateRoomInfo`, rewrite `roundTrip` body to use `reflect.DeepEqual`. All tests compile and pass.
3. **Refactor:** none needed.
4. **Commit** in two stages (see Implementation Ordering).

## Implementation ordering

Two commits, mirroring the previous `RoomInfo` change:

1. `refactor(model): make Room timestamps nullable` — struct + bson tag `omitempty` additions + producer nil guards + all compile-affected tests (including `roundTrip` helper rewrite).
2. `test(model): assert nil Room timestamps are omitted` — additive `TestRoomJSON_NilTimestampsOmitted` test.

The type flip must be atomic at Go compile level: flipping the struct breaks every reader/writer simultaneously, so the commit includes the struct, producer, and all test compile fixes. The second commit is a pure test addition.

## Verification

- `make lint`
- `make test` — covers `pkg/model` and all service unit suites
- `make test-integration SERVICE=room-service`
- `make test-integration SERVICE=broadcast-worker`

## Risks

- **Missed pointer-unaware code path elsewhere.** Mitigation: an exhaustive `grep` for `LastMsgAt` / `LastMentionAllAt` across the repo has already been done during brainstorming; the sites listed in "Scope" are exhaustive as of this commit. Full `make test` + `make test-integration` on both affected services will catch anything missed.
- **`roundTrip` helper rewrite side effects.** The change from `==` to `reflect.DeepEqual` is strictly more permissive. Every existing caller's structs are value-equal after round-trip, so nothing regresses. Risk: effectively zero.
- **Pre-existing Mongo zero-time data.** Documented in "Compatibility". The `.IsZero()` guard is retained specifically to handle this. No migration needed; behavior is drop-in.
- **Future developer writing `room.LastMsgAt.IsZero()` without a nil check.** `IsZero` is defined on `time.Time`, not `*time.Time`, so Go auto-dereferences the pointer — meaning a nil `LastMsgAt` will panic with a nil-pointer deref, not silently misbehave. The panic is a clear signal to add the nil guard. No additional mitigation proposed; code review is sufficient.
