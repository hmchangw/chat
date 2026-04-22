# Design: Search-Sync-Worker Extension — User-Room Restricted Rooms

**Status:** Draft
**Companion spec:** `2026-04-21-search-service-design.md`

## Context

PR #109 (`search-sync-worker` with spotlight + user-room collections) currently **skips** any `InboxMemberEvent` where `HistorySharedSince != 0`, deferring restricted-room handling to the search service via Mongo+cache at query time.

We've now decided to move restricted-room tracking into the user-room ES doc itself, making `search-service` a pure ES+Valkey read path with **no Mongo dependency at query time**. This spec covers the `search-sync-worker` + `pkg/model` changes that make that possible.

**Scope boundary:** spotlight index is NOT changed here. Spotlight keeps its existing skip-restricted behavior for MVP — restricted rooms will not appear in room-search results. Lifting the spotlight skip is a post-MVP follow-up.

---

## Goals

1. Extend the `user-room` ES doc with a `restrictedRooms` map (`rid → historySharedSince`) alongside the existing `rooms []string`.
2. Update painless scripts so `member_added` routes events into either `rooms[]` or `restrictedRooms{}` based on `HistorySharedSince`.
3. Change `HistorySharedSince` on `InboxMemberEvent` and `MemberAddEvent` from `int64` to `*int64` with `omitempty`, ending the zero-vs-unset ambiguity.
4. Keep the LWW guard via `roomTimestamps` working across both paths.

## Non-Goals

- Indexing restricted rooms in the spotlight index.
- Push-based cache invalidation when subscriptions change (covered in the companion search-service spec's follow-ups).
- Changes to the `messages-*` index schema.

---

## Key Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| `HistorySharedSince` type | `*int64` with `omitempty` | Disambiguates "unrestricted" (nil) from "restricted from some timestamp" (non-nil) at the Go/wire layer. **Note:** this disambiguation is Go-layer only — the painless script boundary canonically treats any `hss <= 0` (including the nil-translated 0 and a hypothetical `&0`) as unrestricted. Publishers MUST NOT emit `HistorySharedSince = &0` for a genuinely restricted room; use nil for unrestricted, use a real past-millisecond timestamp for restricted. See "Behavioral Properties" below. |
| user-room schema | Add `restrictedRooms map[rid]hss` alongside `rooms []string` | Mirrors the original Mongo user-room doc shape. |
| ES field mapping | `"type": "flattened"` | Same approach as `roomTimestamps`; avoids mapping explosion from arbitrary rid keys. |
| LWW guard | Unchanged — `roomTimestamps` keyed per-rid with event `Timestamp` | Same mechanism protects both paths. |
| Sync-worker event-level HSS skip | Removed from user-room; kept in spotlight | user-room now stores both; spotlight is minimal for MVP. |
| Painless "restricted?" check | `params.hss > 0` | 0 is the documented sentinel meaning "unrestricted"; publisher translates nil pointer → 0 at the script boundary. |
| Transition `rooms[] ↔ restrictedRooms{}` | Same script moves rid between the two on each `member_added` | Natural handling of admin lifting/applying restrictions. |

---

## Schema Changes

### `pkg/model.InboxMemberEvent`

```go
// Before (PR #109)
type InboxMemberEvent struct {
    // ...
    HistorySharedSince int64 `json:"historySharedSince,omitempty"`
    // ...
}

// After
type InboxMemberEvent struct {
    RoomID             string   `json:"roomId"`
    RoomName           string   `json:"roomName"`
    RoomType           RoomType `json:"roomType"`
    SiteID             string   `json:"siteId"`
    Accounts           []string `json:"accounts"`
    HistorySharedSince *int64   `json:"historySharedSince,omitempty"`
    JoinedAt           int64    `json:"joinedAt,omitempty"`
    Timestamp          int64    `json:"timestamp" bson:"timestamp"`
}
```

Doc comment rewrites to: `HistorySharedSince == nil means the entire bulk is unrestricted; non-nil means all Accounts in the bulk are restricted from that timestamp. The user-room collection routes these into restrictedRooms{}; the spotlight collection skips non-nil events entirely for MVP.`

### `pkg/model.MemberAddEvent`

```go
// Before
type MemberAddEvent struct {
    // ...
    HistorySharedSince int64 `json:"historySharedSince" bson:"historySharedSince"`
    // ...
}

// After
type MemberAddEvent struct {
    Type               string   `json:"type"               bson:"type"`
    RoomID             string   `json:"roomId"             bson:"roomId"`
    Accounts           []string `json:"accounts"           bson:"accounts"`
    SiteID             string   `json:"siteId"             bson:"siteId"`
    JoinedAt           int64    `json:"joinedAt"           bson:"joinedAt"`
    HistorySharedSince *int64   `json:"historySharedSince,omitempty" bson:"historySharedSince,omitempty"`
    Timestamp          int64    `json:"timestamp"          bson:"timestamp"`
}
```

### user-room ES doc (produced by `search-sync-worker/user_room.go`)

```go
// Before (PR #109)
type userRoomUpsertDoc struct {
    UserAccount    string           `json:"userAccount"`
    Rooms          []string         `json:"rooms"`
    RoomTimestamps map[string]int64 `json:"roomTimestamps"`
    CreatedAt      string           `json:"createdAt"`
    UpdatedAt      string           `json:"updatedAt"`
}

// After
type userRoomUpsertDoc struct {
    UserAccount     string           `json:"userAccount"`
    Rooms           []string         `json:"rooms"`            // unrestricted rids
    RestrictedRooms map[string]int64 `json:"restrictedRooms"`  // rid -> historySharedSince (millis)
    RoomTimestamps  map[string]int64 `json:"roomTimestamps"`   // rid -> event ts (LWW guard)
    CreatedAt       string           `json:"createdAt"`
    UpdatedAt       string           `json:"updatedAt"`
}
```

### user-room index template mapping delta

Add:

```json
{
  "mappings": {
    "dynamic": false,
    "properties": {
      "restrictedRooms": { "type": "flattened" }
    }
  }
}
```

---

## Painless Script Changes (`search-sync-worker/user_room.go`)

### `addRoomScript` (member_added)

```painless
// LWW guard (unchanged)
if (ctx._source.roomTimestamps != null
    && ctx._source.roomTimestamps[params.rid] != null
    && params.ts <= ctx._source.roomTimestamps[params.rid]) {
  ctx.op = 'none';
  return;
}

// Init as needed
if (ctx._source.rooms == null)           { ctx._source.rooms = []; }
if (ctx._source.restrictedRooms == null) { ctx._source.restrictedRooms = [:]; }
if (ctx._source.roomTimestamps == null)  { ctx._source.roomTimestamps = [:]; }

// Route by hss (params.hss == 0 means unrestricted; >0 means restricted)
if (params.hss > 0) {
  ctx._source.restrictedRooms[params.rid] = params.hss;
  ctx._source.rooms.removeIf(r -> r == params.rid);
} else {
  if (!ctx._source.rooms.contains(params.rid)) {
    ctx._source.rooms.add(params.rid);
  }
  ctx._source.restrictedRooms.remove(params.rid);
}

ctx._source.roomTimestamps[params.rid] = params.ts;
ctx._source.updatedAt = params.updatedAt;
```

### `removeRoomScript` (member_removed)

```painless
// LWW guard (unchanged)
if (ctx._source.roomTimestamps != null
    && ctx._source.roomTimestamps[params.rid] != null
    && params.ts <= ctx._source.roomTimestamps[params.rid]) {
  ctx.op = 'none';
  return;
}

if (ctx._source.rooms != null) {
  ctx._source.rooms.removeIf(r -> r == params.rid);
}
if (ctx._source.restrictedRooms != null) {
  ctx._source.restrictedRooms.remove(params.rid);
}
if (ctx._source.roomTimestamps != null) {
  ctx._source.roomTimestamps[params.rid] = params.ts;
}
ctx._source.updatedAt = params.updatedAt;
```

### Go-side param translation (sync-worker publishing to painless)

```go
// hss == 0 is the painless sentinel for "unrestricted" — painless has no
// nullable primitives in script params, so we translate *int64 here.
var hss int64 = 0
if event.HistorySharedSince != nil {
    hss = *event.HistorySharedSince
}

params := map[string]any{
    "rid":       event.RoomID,
    "hss":       hss,
    "ts":        event.Timestamp,
    "updatedAt": time.UnixMilli(event.Timestamp).UTC().Format(time.RFC3339Nano),
}
```

The `hss == 0 means unrestricted` sentinel is local to the Go↔painless boundary and MUST be documented with an inline comment. Never leaks to the wire.

---

## Behavioral Properties

1. **Natural transition** — if an admin lifts a restriction, the next `member_added` event has `HistorySharedSince == nil`, `params.hss == 0`, and the add-script moves the rid from `restrictedRooms{}` to `rooms[]`. Symmetric for the opposite transition.

2. **LWW preserved across both paths** — stale events still no-op regardless of which field they'd touch.

3. **Bulk events** — all `Accounts` in a single `InboxMemberEvent` share the same event-level HSS, so every account is routed to the same slot.

4. **Cross-transition atomicity** — both maps + `roomTimestamps` are updated in one painless script execution, so a doc never appears in both `rooms[]` and `restrictedRooms{}` for the same rid.

5. **Publisher contract for `HistorySharedSince`** — the Go `*int64` disambiguation is a source-of-truth layer, but the painless script only sees an `int64` parameter where `hss > 0` means restricted and `hss <= 0` means unrestricted. Concretely: `nil` → `hss = 0` (unrestricted), `&N` for `N > 0` → `hss = N` (restricted). **Publishers MUST emit nil for unrestricted rooms — never `&0` and never a non-positive timestamp**. A `&0` would be structurally non-nil but painless would treat it as unrestricted, creating a silent contract violation. Unit tests on the publisher side assert this (see Publisher Tests below).

---

## Publisher-Side Changes

### `room-worker/`

`processAddMembers`, `processRemoveIndividual`, `processRemoveOrg` currently set `HistorySharedSince` as a plain `int64`. They change to:

```go
var hssPtr *int64
if room.HistorySharedSince != 0 {
    v := room.HistorySharedSince
    hssPtr = &v
}

evt := model.InboxMemberEvent{
    // ...
    HistorySharedSince: hssPtr,
    // ...
}
```

Unrestricted rooms omit the field on the wire; restricted rooms carry a pointer.

### `inbox-worker/`

If it just forwards the field: no code change (the JSON wire format still accepts `"historySharedSince": N` and populates `*int64`).
If it reads the value: add `if evt.HistorySharedSince != nil { ... }`.

### `search-sync-worker/inbox_stream.go`

The shared `parseMemberEvent` skip-precondition changes from `event.HistorySharedSince != 0` → `event.HistorySharedSince != nil`. Callers decide:
- **Spotlight collection** — keeps skipping on non-nil HSS (MVP).
- **User-room collection** — no longer skips; passes HSS through with the sentinel translation.

---

## Testing

### Unit tests (`user_room_test.go`)

- `TestUserRoomSync_RestrictedRoomAdd` — add with non-nil HSS → rid in `restrictedRooms{}`, absent from `rooms[]`.
- `TestUserRoomSync_UnrestrictedAdd` — add with nil HSS → rid in `rooms[]`, absent from `restrictedRooms{}`.
- `TestUserRoomSync_RestrictedToUnrestrictedTransition` — two sequential adds (first restricted, later unrestricted with larger `ts`) → rid ends in `rooms[]` only.
- `TestUserRoomSync_UnrestrictedToRestrictedTransition` — inverse.
- `TestUserRoomSync_BulkMixed_AllRestricted` — one event, N accounts, non-nil HSS → all N docs have entry in `restrictedRooms{}`.
- `TestUserRoomSync_BulkMixed_AllUnrestricted` — one event, N accounts, nil HSS → all N docs have entry in `rooms[]`.
- `TestUserRoomSync_RemoveEvictsFromBoth` — remove with rid present in `restrictedRooms{}` (then in `rooms[]`) evicts correctly.
- `TestUserRoomSync_LWWGuard_RestrictedPath` — stale add on restricted-current doc no-ops.
- `TestUserRoomSync_LWWGuard_UnrestrictedPath` — existing test parameterized over both paths.

### Integration tests (`inbox_integration_test.go`)

- `TestUserRoomSyncIntegration_RestrictedRooms` — publish `InboxMemberEvent` with non-nil HSS pointer to local INBOX; assert doc has correct entry in `restrictedRooms{}`, none in `rooms[]`.
- `TestUserRoomSyncIntegration_TransitionRestrictedToUnrestricted` — publish two sequential events, assert final shape.
- Existing `TestUserRoomSync_BulkInvite` "all-restricted event no-op" — inverts: all accounts now appear in `restrictedRooms{}` instead of being a no-op.

### Model tests (`pkg/model/model_test.go`)

- Round-trip `InboxMemberEvent` with nil HSS — marshals without the field, unmarshals back to nil.
- Round-trip with non-nil HSS — marshals to `"historySharedSince":N`, unmarshals back equal.
- Same two cases for `MemberAddEvent`.

### Publisher tests (`room-worker/`)

- `TestHandler_ProcessAddMembers_RestrictedPropagatesPointer` — restricted-room member-add produces `InboxMemberEvent.HistorySharedSince = &hss`.
- `TestHandler_ProcessAddMembers_UnrestrictedOmits` — unrestricted-room member-add marshaled JSON does not contain `"historySharedSince"`.
- Existing `TestHandler_ProcessAddMembers_PublishesToInbox` updated to use pointer construction.

---

## Rollout Considerations

1. **Template upsert on startup** — the new `restrictedRooms` field is added via `UpsertTemplate` on sync-worker startup. Existing docs without the field keep working (scripts initialize lazily).

2. **Backfill** — existing `user-room` docs lack `restrictedRooms`. Two options:
   - **(a)** Do nothing; restricted-room access is eventually repopulated when a future event re-emits for those subscriptions.
   - **(b)** One-shot rebuild — drop+recreate the `user-room` index and replay INBOX events.

   Recommend **(b)** for production rollout; document the procedure in the runbook.

3. **Companion search-service spec** requires this spec to land first, since `search-service` query-time restricted-room handling reads from `restrictedRooms{}`.

---

## Decision Log

- **Why `*int64` instead of a negative sentinel?** Pointer is Go-idiomatic, self-documenting, and serializes cleanly with `omitempty`.
- **Why `hss=0` sentinel on the painless side?** Painless doesn't have nullable primitives in script params. Sentinel is local to the Go↔painless boundary.
- **Why keep `rooms` as `[]string` instead of promoting to `map[rid]joinedAt`?** Matches the original Mongo user-room shape; terms-lookup (`path: "rooms"`) works identically; expanding to a map would widen this spec without clear benefit.
- **Why not lift the spotlight restriction-skip in this spec?** Different ergonomics — spotlight is per-(user, room) doc, and each would need its own HSS. Worth a focused follow-up.
