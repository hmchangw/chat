# Nullable `RoomInfo` timestamps — design

> **Follow-up (2026-04-24):** The matching change on the Mongo domain type `model.Room` (`LastMsgAt` / `LastMentionAllAt` → `*time.Time`) is covered by a separate spec — `docs/superpowers/specs/2026-04-24-nullable-room-timestamps-design.md`. The "Out of scope" note below correctly reflected the boundary at the time this spec was implemented; it no longer reflects the current direction.

## Summary

Change `LastMsgAt` and `LastMentionAllAt` on `model.RoomInfo` from `int64` to `*int64` so the RPC response can unambiguously represent "no last message" / "never mentioned everyone". Today those fields carry `0` in two unrelated situations — the room was never used, or the room was not found — which is ambiguous. After this change, absent-in-JSON (nil pointer + `omitempty`) means "no value", and a present number always carries a real millisecond timestamp.

## Motivation

`model.RoomInfo` is returned by the rooms-info batch RPC in `room-service`. The current zero-value semantics collide with a legitimate timestamp value (Unix epoch millis `0`) and blur the distinction between "found=true with no activity" and "found=false / missing room". Pointer semantics make the meaning explicit and match the pattern already used on the same struct (`PrivateKey *string`, `KeyVersion *int`).

## Scope

### In scope

- `model.RoomInfo.LastMsgAt` → `*int64`
- `model.RoomInfo.LastMentionAllAt` → `*int64`
- Producer: `room-service/handler.go` `aggregateRoomInfo`
- Unit tests: `pkg/model/model_test.go`, `room-service/handler_test.go`
- Integration tests: `room-service/integration_test.go`

### Out of scope

- `model.Room.LastMsgAt` (Mongo domain type, `time.Time`) — unchanged
- `model.Room.LastMentionAllAt` — unchanged
- `model.ThreadRoom.LastMsgAt` (`pkg/model/threadroom.go`) — unchanged
- The event struct at `pkg/model/event.go:140` — unchanged
- `broadcast-worker` and `message-worker` — they consume `model.Room`, not `model.RoomInfo`, so no code there changes

## Wire compatibility

JSON output is unchanged for both existing shapes:

- When the value is "missing" — today the field has Go zero `0` and is omitted via `omitempty`; after the change the field is `nil` and is omitted via `omitempty`. Same on-the-wire bytes.
- When the value is present — today it's `int64`, after it's `*int64`; both marshal to a JSON `number`.

No consumer today reads `RoomInfo.LastMsgAt` / `LastMentionAllAt` outside the tests listed in Scope, so the Go-side type change is contained.

## Code changes

### `pkg/model/room.go`

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

### `room-service/handler.go` — `aggregateRoomInfo` (currently lines 664–669)

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

Behavior matches today: when the source `time.Time` is zero, the pointer stays nil and the field is omitted from JSON.

## Test changes

### `pkg/model/model_test.go`

- `TestRoomInfoJSON` / "happy path with all fields": switch `LastMsgAt: 1735689600000` / `LastMentionAllAt: 1735693200000` to pointers to local `int64` vars so round-trip equality still holds.
- `TestRoomInfoJSON` / "found=false omits all optional fields": no struct change required; the assertion that `"lastMsgAt"` / `"lastMentionAllAt"` are absent continues to hold (nil pointer + `omitempty` → absent).
- `TestRoomInfoJSON` / "found=true with nil PrivateKey omits zero-valued optional fields": keep existing assertions; add an explicit check that after round-trip `dst.LastMsgAt == nil` and `dst.LastMentionAllAt == nil` to pin down the new nullable semantics.
- `TestRoomsInfoBatchResponseJSON`: switch `LastMsgAt: 1735689600000` to `&v` where `v` is a local `int64`.
- **New subtest** `"nil LastMsgAt is omitted; non-nil zero LastMentionAllAt is emitted as 0"`: proves the pointer-based distinction between "absent" and "present zero" that this change introduces.

### `room-service/handler_test.go`

- Line 1445 (`missing room — Found=false, LastMsgAt=0` case): change the assertion from `assert.Equal(t, int64(0), resp.Rooms[0].LastMsgAt)` to `assert.Nil(t, resp.Rooms[0].LastMsgAt)`. Rename the case to `"missing room → Found=false, LastMsgAt=nil"`.
- Line 1523 (`LastMsgAt set → correct millis` case): change assertion to `require.NotNil(t, resp.Rooms[0].LastMsgAt); assert.Equal(t, now.UTC().UnixMilli(), *resp.Rooms[0].LastMsgAt)`.
- **New table case** `"LastMsgAt zero-time in Mongo → nil in response"`: seed a room whose `Room.LastMsgAt` is the zero `time.Time`, assert `resp.Rooms[0].LastMsgAt` is nil and `Found` is true.

### `room-service/integration_test.go`

- Line 1029: `assert.Equal(t, lastMsg.UnixMilli(), resp.Rooms[0].LastMsgAt)` → `require.NotNil(t, resp.Rooms[0].LastMsgAt); assert.Equal(t, lastMsg.UnixMilli(), *resp.Rooms[0].LastMsgAt)`.
- Lines 1037, 1048: `assert.Equal(t, int64(0), resp.Rooms[N].LastMsgAt)` → `assert.Nil(t, resp.Rooms[N].LastMsgAt)`.

## TDD discipline (per CLAUDE.md §4)

1. **Red:** update assertions and add the two new subtests / table cases first, with the struct fields still `int64`. Compilation fails at the `*int64` sites; the nullable-semantics test would fail. Confirm the red state by running `make test SERVICE=room-service` and `make test` scoped to `pkg/model`.
2. **Green:** flip the two fields in `pkg/model/room.go` to `*int64` and update `aggregateRoomInfo` to assign `&ms`. Update the existing test literals to pointer form.
3. **Refactor:** nothing beyond keeping the producer idiomatic. No interface changes, so no `make generate` needed, but run it once to confirm no drift.
4. **Commit** on branch `claude/nullable-room-timestamps-RQAqX`.

## Verification

- `make lint`
- `make test` (full suite — `pkg/model` tests must also pass)
- `make test-integration SERVICE=room-service`
- Coverage: this change does not reduce coverage; the producer branches (zero / non-zero) are already exercised, and the new cases strengthen them.

## Risks

- **Missed dereferences elsewhere.** Mitigation: a full `grep` for `RoomInfo` and `LastMsgAt` / `LastMentionAllAt` (already done during brainstorming) shows the tests above are the only consumers. `make test` + `make test-integration SERVICE=room-service` will catch anything missed.
- **Downstream consumers added after this spec.** None today. If any appear during implementation, handle them in the same change.
