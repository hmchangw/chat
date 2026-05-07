# LoadHistory: return `minUserLastSeenAt`

**Date:** 2026-05-07
**Status:** Draft
**Owner:** history-service

## Problem

The `LoadHistory` NATS RPC (`chat.user.{account}.request.room.{roomID}.{siteID}.msg.history`) currently returns only the message page. Clients that render read-receipt UI (e.g. a "seen by everyone up to here" divider) need `room.minUserLastSeenAt` — the per-room read floor that `room-service` recomputes on `message.read`. Today the client has to make a second round-trip to fetch it. We want it delivered alongside the messages on initial-room-load so that LoadHistory becomes the single payload that carries both messages and room-level read-state metadata.

## Goal

Add a `minUserLastSeenAt` field to the `LoadHistory` response, sourced directly from the local `rooms` MongoDB collection, encoded as UTC milliseconds, omitted when no read floor is set, and degraded (logged + omitted) on read errors.

## Non-goals

- Adding the field to `LoadNextMessages`, `LoadSurroundingMessages`, or any other history-service RPC.
- Caching the value in history-service (a single projected `findOne` per call is cheap enough; revisit only if profiling shows it matters).
- Changing how `room-service` computes or persists `minUserLastSeenAt`.
- Parallelizing the new Mongo read with the existing Cassandra page read; if it turns out to matter, that's a localized follow-up that does not change the wire contract.

## Wire contract

`history-service/internal/models/message.go`:

```go
type LoadHistoryResponse struct {
    Messages          []Message `json:"messages"`
    MinUserLastSeenAt *int64    `json:"minUserLastSeenAt,omitempty"` // UTC millis; omitted when room has no read floor
}
```

- Pointer + `omitempty`: when no subscription has ever read the room (or `rooms.minUserLastSeenAt` is `$unset`), the field is absent from the JSON. Clients interpret absence as "no floor".
- Format mirrors `LoadHistoryRequest.Before` (also `*int64` UTC millis), keeping the LoadHistory RPC self-consistent on both sides of the wire.

### Client API documentation

`docs/client-api.md` — under the "Load History" success-response table, append:

| Field | Type | Notes |
|-------|------|-------|
| `minUserLastSeenAt` | number | Optional. UTC milliseconds since Unix epoch. The room's read floor — the minimum `lastSeenAt` across all subscribers whose `lastSeenAt` is set. Absent when no member has read yet, when the latest read is past `room.lastMsgAt`, or when the value cannot be retrieved. |

Update the example payload to include the field. Cross-link the Message Read RPC section that already explains the room-floor recompute semantics so clients know exactly what they're reading.

## Components

### New: `mongorepo.RoomRepo` in `history-service/internal/mongorepo/room.go`

```go
type RoomRepo struct{ coll *mongo.Collection }

func NewRoomRepo(db *mongo.Database) *RoomRepo {
    return &RoomRepo{coll: db.Collection("rooms")}
}

// GetMinUserLastSeenAt returns the per-room read floor.
// Returns (nil, nil) when the document is missing OR the field is unset —
// the caller treats both as "no floor".
func (r *RoomRepo) GetMinUserLastSeenAt(ctx context.Context, roomID string) (*time.Time, error)
```

- Single `findOne({_id: roomID})` with projection `{minUserLastSeenAt: 1}`. PK lookup, no extra indexes needed.
- Decodes into a tiny anonymous struct holding only the field; if the field is absent in the BSON, the pointer stays nil.
- `mongo.ErrNoDocuments` is mapped to `(nil, nil)`. All other driver errors bubble up.
- The repo never writes to the `rooms` collection. Reads only.

### Modified: `service.HistoryService`

`history-service/internal/service/service.go`:

```go
type RoomRepository interface {
    GetMinUserLastSeenAt(ctx context.Context, roomID string) (*time.Time, error)
}

type HistoryService struct {
    msgReader     MessageReader
    msgWriter     MessageWriter
    subscriptions SubscriptionRepository
    rooms         RoomRepository // new
    publisher     EventPublisher
    threadRooms   ThreadRoomRepository
    keyProvider   RoomKeyProvider
    encrypt       bool
}

func New(
    msgs MessageRepository,
    subs SubscriptionRepository,
    rooms RoomRepository, // new
    pub EventPublisher,
    threadRooms ThreadRoomRepository,
    keyProvider RoomKeyProvider,
    encrypt bool,
) *HistoryService
```

`go:generate` directive at the top of `service.go` is updated to include `RoomRepository` so the regenerated `mocks/mock_repository.go` ships a `MockRoomRepository`.

Compile-time check added next to the existing one:

```go
var _ RoomRepository = (*mongorepo.RoomRepo)(nil)
```

### Modified: `cmd/main.go` wiring

After the existing `mongorepo` initializations, add:

```go
roomRepo := mongorepo.NewRoomRepo(db)
```

Pass `roomRepo` as the new argument to `service.New(...)`. No new env vars, no new config fields — uses the same Mongo `*mongo.Database` already injected for `subscriptions` and `threadRooms`.

### Modified: `LoadHistory` handler in `history-service/internal/service/messages.go`

Insert the new read after the existing Cassandra page read, before the existing `redactUnavailableQuotes` call:

```go
var minMs *int64
if t, err := s.rooms.GetMinUserLastSeenAt(c, roomID); err != nil {
    slog.Warn("loading minUserLastSeenAt", "error", err, "roomID", roomID)
} else if t != nil {
    ms := t.UTC().UnixMilli()
    minMs = &ms
}

redactUnavailableQuotes(page.Data, accessSince)
return &models.LoadHistoryResponse{
    Messages:          page.Data,
    MinUserLastSeenAt: minMs,
}, nil
```

- Sequential, not parallel. The handler is already two sequential I/Os (subscription, then Cassandra page); adding a third PK-projected Mongo read keeps the code linear and trivially readable. Profiling can drive a later parallelization if needed; the wire contract does not change.
- Error severity is `Warn`, not `Error`: this is a degraded — not failed — path. The messages still load, the field just stays nil.
- No changes to `LoadNextMessages`, `LoadSurroundingMessages`, `GetMessageByID`, `EditMessage`, `DeleteMessage`, `GetThreadMessages`, or `GetThreadParentMessages`.

## Data flow

1. Client publishes request to `chat.user.{account}.request.room.{roomID}.{siteID}.msg.history`.
2. `LoadHistory` resolves `accessSince` via `subscriptions.GetHistorySharedSince` (existing behaviour — unchanged auth).
3. `LoadHistory` reads the message page from Cassandra (existing behaviour).
4. `LoadHistory` reads `rooms.minUserLastSeenAt` from local Mongo via `rooms.GetMinUserLastSeenAt(roomID)`. On error: log + continue with nil. On `(nil, nil)`: continue with nil.
5. Existing `redactUnavailableQuotes` runs on the page.
6. Handler replies with `LoadHistoryResponse{Messages: page.Data, MinUserLastSeenAt: minMs}`.

## Failure modes

| Failure | Behaviour |
|---------|-----------|
| Mongo `rooms.findOne` errors (network, timeout, unmarshal) | `slog.Warn`, response omits `minUserLastSeenAt`, messages still returned. |
| Room document missing in `rooms` collection | `(nil, nil)` returned by repo, response omits `minUserLastSeenAt`. |
| `rooms.minUserLastSeenAt` field absent (unset) | Decode leaves pointer nil, repo returns `(nil, nil)`, response omits the field. |
| Subscription check fails (pre-existing) | Existing `ErrInternal` / `ErrForbidden` paths unchanged. |
| Cassandra page read fails (pre-existing) | Existing `ErrInternal` path unchanged. |

The new read MUST NOT promote any new failure into an outage of the existing happy path. That's the single safety invariant of this change.

## Tests

All new tests follow the project's TDD red-green-refactor cycle. Run `make generate SERVICE=history-service` after the `RoomRepository` interface lands so `MockRoomRepository` is available.

### Unit tests — `history-service/internal/service/messages_test.go`

Existing `LoadHistory` tests are updated to set up a `rooms.GetMinUserLastSeenAt(...)` expectation that returns `(nil, nil)` so prior assertions on `resp.Messages` and error paths keep passing without churn.

New table-friendly cases:

1. `TestHistoryService_LoadHistory_ReturnsMinUserLastSeenAt` — repo returns `(&t, nil)`; assert `*resp.MinUserLastSeenAt == t.UTC().UnixMilli()` and `resp.Messages` matches.
2. `TestHistoryService_LoadHistory_NoMinUserLastSeenAt` — repo returns `(nil, nil)`; assert `resp.MinUserLastSeenAt == nil`. Sub-assertion: `json.Marshal(resp)` does NOT contain the `minUserLastSeenAt` key (proves `omitempty` works end-to-end).
3. `TestHistoryService_LoadHistory_RoomReadError_DegradesGracefully` — repo returns `(nil, errors.New("mongo down"))`; assert `err == nil`, `resp.Messages` is the expected page, `resp.MinUserLastSeenAt == nil`.

Negative-coupling guards (one per RPC, just enough to catch accidental future regressions):

4. `TestHistoryService_LoadNextMessages_DoesNotReadRoom` — `rooms.EXPECT().GetMinUserLastSeenAt(gomock.Any(), gomock.Any()).Times(0)`.
5. `TestHistoryService_LoadSurroundingMessages_DoesNotReadRoom` — same `.Times(0)` guard.

The RPCs that don't return message pages (Edit, Delete, GetMessageByID, threads) inherit the same guard implicitly — no explicit test needed since they don't construct a `LoadHistoryResponse` and `Times(0)` would be a maintenance burden across seven handlers. The two list-style siblings are the realistic regression targets.

### Integration tests — `history-service/internal/mongorepo/room_test.go`

`//go:build integration`. Uses the existing testcontainers Mongo helper pattern from `subscription_test.go`.

1. `TestRoomRepo_GetMinUserLastSeenAt_Set` — insert a room doc with `minUserLastSeenAt: <t>`; assert returned `*time.Time` matches `t` within a second.
2. `TestRoomRepo_GetMinUserLastSeenAt_Unset` — insert a room doc without `minUserLastSeenAt`; assert `(nil, nil)`.
3. `TestRoomRepo_GetMinUserLastSeenAt_MissingDocument` — query an unknown roomID; assert `(nil, nil)`, no error.

### Coverage

Project minimum: 80%. The repo has one method and three test cases (set, unset, missing) covering all branches. The handler delta is three statements covered by cases 1–3 above. Verify with `go tool cover -func=coverage.out` against the history-service package.

## Rollout

- Backward compatible: pure response field addition with `omitempty`. Existing clients that don't read the field see no change. Clients that do read it must tolerate absence.
- No migration. `rooms.minUserLastSeenAt` is already maintained by `room-service` as of the message-read-rpc work (commit `61f128a`).
- No new env vars, no stream/subject changes, no NATS subject contract change.
- Deploy order: history-service can ship independently. No coordination with room-service or clients required.

## Out of scope / follow-ups

- Parallelizing the rooms read with the Cassandra page read inside `LoadHistory`.
- Extending the same field to `LoadNextMessages` / `LoadSurroundingMessages` if a client use case appears.
- Caching `minUserLastSeenAt` in history-service.
- Pushing the field on the room-event subject so live updates don't require another LoadHistory call.
