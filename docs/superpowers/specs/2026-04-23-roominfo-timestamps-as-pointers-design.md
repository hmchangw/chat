# RoomInfo timestamps as `*int64` — Design

## Context

`model.RoomInfo` (defined in `pkg/model/room.go`) is the single aggregated record returned by the `room-service` batch-info RPC. It combines Mongo room metadata with the Valkey-backed private key. Two timestamp fields — `LastMsgAt` and `LastMentionAllAt` — are currently typed as `int64` with `omitempty`:

```go
LastMsgAt        int64 `json:"lastMsgAt,omitempty"`
LastMentionAllAt int64 `json:"lastMentionAllAt,omitempty"`
```

Because `int64` has no tri-state, `0` means both "never had a message / mention all" and "stored value is literally zero". The producer (`room-service/handler.go:aggregateRoomInfo`) already guards on `r.LastMsgAt.IsZero()` from the source `time.Time`, so in practice the value is always either an unset zero or a real millis, but consumers can't tell those apart at the Go level, and the field disagrees with the already-pointerized `PrivateKey` / `KeyVersion` siblings.

## Goal

Change the two fields to `*int64` so `nil` unambiguously means "no last message / no mention all yet". This aligns with how `PrivateKey` and `KeyVersion` already express optionality, and it gives future consumers a reliable way to distinguish "unset" from an explicit zero.

`model.Room` is unaffected — its `time.Time` fields already express optionality via `IsZero()`.

## Wire-format impact

None. `omitempty` on a `*int64` omits when nil and emits the number otherwise, which is exactly what the existing `int64 + omitempty` did for the zero and non-zero cases. External JSON consumers see identical payloads.

## Code changes

### 1. `pkg/model/room.go` — struct definition

```go
type RoomInfo struct {
    RoomID           string  `json:"roomId"`
    Found            bool    `json:"found"`
    SiteID           string  `json:"siteId,omitempty"`
    Name             string  `json:"name,omitempty"`
    LastMsgAt        *int64  `json:"lastMsgAt,omitempty"`
    LastMentionAllAt *int64  `json:"lastMentionAllAt,omitempty"`
    PrivateKey       *string `json:"privateKey,omitempty"`
    KeyVersion       *int    `json:"keyVersion,omitempty"`
    Error            string  `json:"error,omitempty"`
}
```

### 2. `room-service/handler.go` — `aggregateRoomInfo` (lines 664–669)

Keep the `IsZero()` guards; declare the millis variable **inside** each branch so each `RoomInfo` holds its own pointer (a variable declared outside the loop would be aliased across iterations):

```go
if !r.LastMsgAt.IsZero() {
    ms := r.LastMsgAt.UTC().UnixMilli()
    entry.LastMsgAt = &ms
}
if !r.LastMentionAllAt.IsZero() {
    ms := r.LastMentionAllAt.UTC().UnixMilli()
    entry.LastMentionAllAt = &ms
}
```

### 3. `room-service/handler_test.go` — test updates

- Subtest `"missing room — Found=false, LastMsgAt=0"` (line ~1433):
  - Rename to `"missing room — Found=false, LastMsgAt=nil"`.
  - Replace `assert.Equal(t, int64(0), resp.Rooms[0].LastMsgAt)` with `assert.Nil(t, resp.Rooms[0].LastMsgAt)`.
- Subtest `"LastMsgAt set → correct millis"` (line ~1511):
  - Add `require.NotNil(t, resp.Rooms[0].LastMsgAt)` before dereferencing, then compare `*resp.Rooms[0].LastMsgAt` to `now.UTC().UnixMilli()`.

### 4. `room-service/integration_test.go` — test updates

Three assertions in `TestRoomsInfoBatchRPC`:
- Line ~1029 (`r1`, has `LastMsgAt`): `require.NotNil` then deref before comparing to `lastMsg.UnixMilli()`.
- Line ~1037 (`r2`, no `LastMsgAt`): switch from `assert.Equal(t, int64(0), …)` to `assert.Nil(t, …)`.
- Line ~1048 (`missing`, not found): same `assert.Nil` change.

### 5. `pkg/model/model_test.go` — test updates

- `TestRoomInfoJSON/"happy path with all fields"` (line ~1041):
  - The literals `1735689600000` and `1735693200000` must become `*int64`. Declare local `int64` vars (e.g., `lastMsg := int64(1735689600000)`, `lastMen := int64(1735693200000)`) and take their addresses (`&lastMsg`, `&lastMen`) in the struct literal — this mirrors how `pk` and `kv` are already handled in the same subtest. The round-tripped struct still `DeepEqual`s the source because `json.Unmarshal` produces a fresh pointer to the same numeric value.
- `TestRoomInfoJSON/"found=true with nil PrivateKey omits zero-valued optional fields"` (line ~1088):
  - No literal to change — the test already leaves `LastMsgAt` / `LastMentionAllAt` unset. The assertion that `lastMsgAt` / `lastMentionAllAt` keys are absent from the marshaled map still holds.
- `TestRoomsInfoBatchResponseJSON` (line ~1108):
  - Same pointer treatment for the `LastMsgAt: 1735689600000` literal in the first element.
- **New subtest** in `TestRoomInfoJSON`: `"explicit zero pointer is not omitted"` — set `LastMsgAt` to a pointer to `int64(0)`, marshal, unmarshal into `map[string]any`, and assert the `lastMsgAt` key is present with value `float64(0)` (JSON numbers unmarshal to `float64` in `map[string]any`). This locks in the tri-state contract so a future regression back to value-type cannot slip through.

### Files deliberately not touched

- `pkg/model/event.go`, `pkg/model/threadroom.go` — their `LastMsgAt` fields are `time.Time` on different types (not `RoomInfo`).
- `pkg/model/room.go:Room` — its `LastMsgAt` / `LastMentionAllAt` are `time.Time`; consumers use `IsZero()`. Out of scope.
- `broadcast-worker/*`, `message-worker/*` — reference `Room.LastMsgAt` (`time.Time`), not `RoomInfo`.
- Generated `mock_store_test.go` files — the `RoomInfo` struct is not part of any store interface, so no regeneration is required. `make generate SERVICE=room-service` still run as a safety check.

## Verification

Commands, in order:
1. `make generate SERVICE=room-service` — safety run; expected no-op.
2. `make test SERVICE=pkg/model` — model round-trip tests (including the new explicit-zero subtest) pass.
3. `make test SERVICE=room-service` — handler unit tests pass.
4. `make test-integration SERVICE=room-service` — integration test `TestRoomsInfoBatchRPC` passes.
5. `make test` — full unit suite green (catches any missed reference).
6. `make lint` — clean.

## Risks

- **Pointer aliasing in the loop.** Declaring `ms` outside the `for i, id := range ids` loop, or outside the `if`, would cause every `RoomInfo` to point at the same (last-written) value. The design explicitly scopes `ms` inside each `if` branch.
- **Missed call site.** Only `room-service/handler.go` currently produces `RoomInfo`, and only tests consume its fields. `grep -rn "\.LastMsgAt\|\.LastMentionAllAt" --include="*.go"` before committing will confirm no other producer has appeared.
- **External JSON consumers.** None — wire format is identical in both omit and present cases.

## Out of scope

- Changing `Room.LastMsgAt` / `Room.LastMentionAllAt` to pointers.
- Adding mention-all tests to the integration suite (they don't exist today and adding them is a separate feature).
- Reshaping the `RoomInfo` JSON contract in any other way.
