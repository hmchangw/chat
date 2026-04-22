# Room Info Batch RPC — Design

**Date:** 2026-04-14
**Status:** Draft
**Scope:** `room-service`, `pkg/roomkeystore`, `pkg/model`, `pkg/subject`

## 1. Motivation

Other backend services need to resolve multiple rooms at once to aggregated
metadata: room name, site ID, last-message timestamp (from MongoDB), and the
current room private key (from Valkey, via `pkg/roomkeystore`). Today, callers
would have to issue `rooms.get.{roomID}` per-room against `room-service` and
then separately reach into Valkey for keys — N×2 round-trips and no clean
aggregation seam.

This spec adds a single batch RPC on `room-service` that returns aggregated
room info for a list of room IDs in one request/reply, with Mongo and Valkey
lookups fanned out in parallel and Valkey pipelined for minimal latency.

## 2. Contract

### 2.1 Subject

- **Request/reply subject:** `chat.server.request.room.{siteID}.info.batch`
- **Server subscription:** `chat.server.request.room.{siteID}.info.batch` (same as specific — no wildcard needed, server-to-server RPC with no user account in the subject)
- **Queue group:** `room-service` (same as existing CRUD RPCs).
- **Transport:** NATS request/reply (core, not JetStream).
- **Auth:** Internal subject — no additional auth beyond the NATS connection,
  consistent with other room-service RPCs.

The `{siteID}` token in the subject is the target site. A room-service
instance only subscribes to its own site's subject. Cross-site fan-out is the
caller's responsibility (one request per target site).

New builders in `pkg/subject/subject.go`:

```go
func RoomsInfoBatch(siteID string) string           // one param, no account
func RoomsInfoBatchSubscribe(siteID string) string   // same value as specific (no wildcard needed)
```

`RoomsInfoBatchPattern` has been removed (no natsrouter placeholder needed for server-scoped subjects).

### 2.2 Request

```go
// RoomsInfoBatchRequest is the NATS request body for the batch room info RPC.
type RoomsInfoBatchRequest struct {
    RoomIDs []string `json:"roomIds"`
}
```

Validation (whole-batch, reject via `natsutil.ReplyError`):

| Condition | Error message |
|---|---|
| Invalid JSON | `invalid request: <wrapped>` |
| `len(RoomIDs) == 0` | `roomIds must not be empty` |
| `len(RoomIDs) > MAX_BATCH_SIZE` | `batch size N exceeds limit M` |

Duplicate IDs are allowed and produce duplicate response entries (no dedup
in the server).

### 2.3 Response

```go
// RoomInfo is a single aggregated room record: Mongo metadata + Valkey key.
type RoomInfo struct {
    RoomID           string  `json:"roomId"`
    Found            bool    `json:"found"`
    SiteID           string  `json:"siteId,omitempty"`
    Name             string  `json:"name,omitempty"`
    LastMsgAt        int64   `json:"lastMsgAt,omitempty"`           // UTC millis; 0 = "no message", omitted from JSON
    LastMentionAllAt int64   `json:"lastMentionAllAt,omitempty"`    // UTC millis; 0 = "no @all mention", omitted from JSON
    PrivateKey       *string `json:"privateKey,omitempty"`          // base64; nil = no current key
    KeyVersion       *int    `json:"keyVersion,omitempty"`          // nil iff PrivateKey is nil
    Error            string  `json:"error,omitempty"`               // per-room failure (reserved)
}

// RoomsInfoBatchResponse — one entry per requested roomID, input order preserved.
type RoomsInfoBatchResponse struct {
    Rooms []RoomInfo `json:"rooms"`
}
```

Semantics:

- `len(Response.Rooms) == len(Request.RoomIDs)`, order preserved.
- `Found=false` → room not in Mongo for this site; other fields zero-valued
  (`lastMsgAt: 0`, `name: ""`, etc.). Callers should branch on `Found` first.
- `Found=true, PrivateKey=nil` → room exists in Mongo but Valkey has no current
  key (legitimate state, not an error).
- `Error != ""` → per-room failure, reserved for future use; initial
  implementation does not populate it (whole-batch errors bubble up via
  `ReplyError` instead).

Whole-batch errors (invalid JSON, empty or oversized `RoomIDs`, Mongo outage,
Valkey outage) reply with `model.ErrorResponse` via `natsutil.ReplyError`.
We deliberately fail loud on backend outages so callers can distinguish
"Valkey is healthy, this room has no key" from "Valkey is down".

### 2.4 Wire-level timestamp convention

`model.Room.LastMsgAt` is `time.Time` in Mongo. On the wire it is converted to
`UnixMilli()` per the codebase's "NATS wire structs use int64 millis"
convention (commit `deffcfa`). Zero time → `0`, which is omitted from JSON
via `omitempty` (0 means "no message"). Callers distinguish "found, never
messaged" (`lastMsgAt` absent / `found: true`) from "not found" (`found:
false`). `LastMentionAllAt` follows the same `omitempty` convention.

## 3. Architecture

### 3.1 Components touched

| Layer | Change |
|---|---|
| `pkg/subject` | Add `RoomsInfoBatch`, `RoomsInfoBatchSubscribe` builders (server-scoped, no wildcard/pattern). |
| `pkg/model` | Add `RoomsInfoBatchRequest`, `RoomInfo`, `RoomsInfoBatchResponse` + roundtrip tests. |
| `pkg/roomkeystore` | Add `GetMany(ctx, roomIDs) (map[string]*VersionedKeyPair, error)` to the `RoomKeyStore` interface and the Valkey adapter. |
| `room-service/store.go` | Extend `RoomStore` with `ListRoomsByIDs(ctx, ids []string) ([]model.Room, error)`. |
| `room-service/store_mongo.go` | Implement `ListRoomsByIDs` via `find({_id: {$in: ids}})`. |
| `room-service/store.go` | Define local `RoomKeyStore` interface with only `GetMany` (consumer-side). |
| `room-service/handler.go` | Add `handleRoomsInfoBatch`, `natsRoomsInfoBatch`, `aggregateRoomInfo`; inject `RoomKeyStore` (local interface) + `maxBatchSize`. |
| `room-service/main.go` | Wire Valkey (`roomkeystore.NewValkeyStore`); add `MAX_BATCH_SIZE`, `VALKEY_ADDR`, `VALKEY_PASSWORD`, `VALKEY_KEY_GRACE_PERIOD` config. |
| `room-service/deploy/docker-compose.yml` | Add Valkey service for local dev. |
| `room-service/mock_store_test.go` | Regenerate via `make generate SERVICE=room-service` (interface changed). |

### 3.2 Handler dependency graph

```
Handler
├── RoomStore            (existing, extended with ListRoomsByIDs)
└── RoomKeyStore         (new dep, from pkg/roomkeystore, extended with GetMany)
```

Existing `rooms.create` / `rooms.list` / `rooms.get` / invite flow is
untouched.

### 3.3 Request flow

```
NATS request on chat.server.request.room.{siteID}.info.batch
  → Handler.natsRoomsInfoBatch(m)
      - Uses sanitizeError(err) + slog.Error (not raw err.Error()) for error replies
  → Handler.handleRoomsInfoBatch(ctx, data) -> []byte
      1. Unmarshal RoomsInfoBatchRequest. Validate non-empty, ≤ MAX_BATCH_SIZE.
      2. ctx, cancel = context.WithTimeout(ctx, 5s); defer cancel()
      3. Parallel fan-out (errgroup, 2 goroutines):
           a. rooms = chunkedListRooms(ctx, ids)   // ceil(N/500) Mongo round-trips (queryChunkSize=500)
           b. keys  = chunkedGetKeys(ctx, ids)      // ceil(N/500) Valkey round-trips (queryChunkSize=500, pipelined)
         Either error aborts the batch with ReplyError.
      4. aggregateRoomInfo(ids, rooms, keys) → []RoomInfo (input order preserved).
      5. natsutil.ReplyJSON with RoomsInfoBatchResponse.
```

Both `ListRoomsByIDs` (Mongo) and `GetMany` (Valkey pipeline) are chunked at
`queryChunkSize=500` via `chunkedListRooms` and `chunkedGetKeys` helpers.

**Total wire cost for N rooms:** ceil(N/500) Mongo round-trips + ceil(N/500)
Valkey round-trips (still parallel between Mongo and Valkey).

### 3.4 Observability

- OpenTelemetry span `rooms.info.batch` created automatically by
  `otelnats.QueueSubscribe`; add attributes `batch_size`, `site_id`.
- `slog.Debug` per request with `site_id`, `batch_size`,
  `found_count`, `keyed_count`, `latency_ms` (Debug, not Info, to reduce production log volume).
- **Key material is never logged**, at any level.

## 4. `pkg/roomkeystore.GetMany` details

### 4.1 Interface addition

```go
// GetMany fetches current key pairs for multiple roomIDs in a single pipelined
// round-trip. Returns a map keyed by roomID containing only rooms that had a
// current key; rooms with no key are omitted. Returns an empty map when
// roomIDs is empty.
GetMany(ctx context.Context, roomIDs []string) (map[string]*VersionedKeyPair, error)
```

### 4.2 hashCommander extension

```go
// hgetallMany executes HGETALL for each key in a single pipelined round-trip.
// Results are returned in the same order as keys. Absent keys yield empty maps.
hgetallMany(ctx context.Context, keys []string) ([]map[string]string, error)
```

### 4.3 Redis adapter

Single `c.Pipeline()` with one `HGetAll` per `roomkey(roomID)`, then one
`Exec`. `HGetAll` on a missing hash returns an empty map (not an error); the
adapter passes that through as an empty map in the corresponding slot. Real
errors (connection failure, protocol error) fail the whole batch.

### 4.4 valkeyStore.GetMany

- Build `keys` by mapping `roomID → roomkey(roomID)`.
- Call `hgetallMany`.
- For each returned fields map: empty → omit from result map; populated →
  parse `ver`, decode base64 public/private → insert into result.
- Any decode or version-parse error fails the whole batch with a wrapped error
  that includes the offending roomID (storage-integrity bug, not a per-room
  missing-key state).

## 5. Handler implementation

### 5.1 Struct and constructor

```go
type Handler struct {
    store           RoomStore
    keyStore        RoomKeyStore                                             // local consumer-side interface (not roomkeystore.RoomKeyStore)
    siteID          string
    maxRoomSize     int
    maxBatchSize    int
    publishToStream func(ctx context.Context, subj string, data []byte) error  // 3 args: ctx, subject, data
}

func NewHandler(
    store RoomStore,
    keyStore RoomKeyStore,
    siteID string,
    maxRoomSize, maxBatchSize int,
    publishToStream func(ctx context.Context, subj string, data []byte) error,
) *Handler
```

`RoomKeyStore` is a local interface defined in `room-service/store.go` with
only the `GetMany` method the handler needs (consumer-side interface pattern).

### 5.2 Registration

Inside `RegisterCRUD`:

```go
if _, err := nc.QueueSubscribe(
    subject.RoomsInfoBatchWildcard(h.siteID), queue, h.natsRoomsInfoBatch,
); err != nil {
    return err
}
```

### 5.3 Handler methods

- `natsRoomsInfoBatch(m otelnats.Msg)` — dispatcher: call
  `handleRoomsInfoBatch(m.Context(), m.Msg.Data)`, `ReplyError` or `Respond`.
- `handleRoomsInfoBatch(ctx, data) ([]byte, error)` — validation, 5s deadline,
  errgroup fan-out, aggregation, JSON marshal.
- `aggregateRoomInfo(ids, rooms, keys) []RoomInfo` — build `byID` map from
  rooms, walk input ids in order, produce `RoomInfo` entries. Converts
  `LastMsgAt` via `.UTC().UnixMilli()` (0 when zero-time). Base64-encodes
  private key when the Valkey map has an entry.

Uses `golang.org/x/sync/errgroup` (already in `go.sum`).

## 6. `main.go` wiring and config

### 6.1 Config

```go
type config struct {
    NatsURL      string `env:"NATS_URL"       envDefault:"nats://localhost:4222"`
    SiteID       string `env:"SITE_ID"        envDefault:"site-local"`
    MongoURI     string `env:"MONGO_URI"      envDefault:"mongodb://localhost:27017"`
    MongoDB      string `env:"MONGO_DB"       envDefault:"chat"`
    MaxRoomSize  int    `env:"MAX_ROOM_SIZE"  envDefault:"1000"`
    MaxBatchSize int    `env:"MAX_BATCH_SIZE" envDefault:"500"`

    ValkeyAddr        string        `env:"VALKEY_ADDR,required"`
    ValkeyPassword    string        `env:"VALKEY_PASSWORD" envDefault:""`
    ValkeyGracePeriod time.Duration `env:"VALKEY_KEY_GRACE_PERIOD,required"`
}
```

`VALKEY_ADDR` and `VALKEY_KEY_GRACE_PERIOD` are `required` per the
connection-string-and-secrets rule; `MAX_BATCH_SIZE` and `VALKEY_PASSWORD`
have defaults.

### 6.2 Valkey wiring

```go
keyStore, err := roomkeystore.NewValkeyStore(roomkeystore.Config{
    Addr:        cfg.ValkeyAddr,
    Password:    cfg.ValkeyPassword,
    GracePeriod: cfg.ValkeyGracePeriod,
})
if err != nil {
    slog.Error("valkey connect failed", "error", err)
    os.Exit(1)
}
```

Handler constructor takes the new `keyStore` and `MaxBatchSize`.

### 6.3 Shutdown

No change. `roomkeystore` does not yet expose a close hook; process exit
releases the redis client. If a close hook is added later, this spec's
shutdown chain can be updated then.

### 6.4 docker-compose

`room-service/deploy/docker-compose.yml`:

- Add a `valkey` service (`valkey/valkey:8-alpine`, port `6379:6379`,
  healthcheck).
- Add `VALKEY_ADDR=valkey:6379` and `VALKEY_KEY_GRACE_PERIOD=24h` to the
  `room-service` env.
- `depends_on: valkey` with `condition: service_healthy`.

Dockerfile and Azure pipeline are unchanged (no new build-time deps; deployment
env vars are managed outside this repo).

## 7. Testing strategy

TDD order. Each layer: write tests first, confirm red, implement, confirm
green, refactor, commit.

### 7.1 `pkg/subject`

Unit tests in `subject_test.go` for `RoomsInfoBatch`,
`RoomsInfoBatchWildcard`, `RoomsInfoBatchPattern`. Assert exact string
output; assert wildcard ≠ specific form.

### 7.2 `pkg/model` roundtrip

Add the three new types to the generic `roundTrip` helper in `model_test.go`.
Dedicated tests for:

- `PrivateKey=nil` → field omitted.
- `LastMsgAt=0` → field present with value `0` (no `omitempty`).
- `Found=false` → `siteId`, `name`, `privateKey`, `keyVersion`, `error` all
  omitted; `lastMsgAt` present as `0`.
- Base64 roundtrip on a 32-byte private key.

### 7.3 `pkg/roomkeystore.GetMany` unit tests

In `roomkeystore_test.go` (extend existing fake `hashCommander`):

- Empty input → `(map{}, nil)`, no pipeline call.
- All present → all returned with correct versions.
- All absent → empty map, no error.
- Mixed present/absent → only present keys returned.
- Decode error on one slot → whole-batch error, roomID included in wrap.
- Version parse error → whole-batch error.
- Verify input order does not affect map contents.

### 7.4 `pkg/roomkeystore` integration test

Extend `pkg/roomkeystore/integration_test.go` with `TestValkeyStore_GetMany`
against the existing testcontainers Valkey: `Set` 3 rooms with distinct key
pairs, `GetMany` with those 3 IDs + 1 absent → 3 present with correct
versions, absent omitted.

### 7.5 `room-service` Mongo integration test

In `room-service/integration_test.go`:

- `TestMongoStore_ListRoomsByIDs`: insert 5 rooms, query 3 by ID + 1 missing
  → returns the 3 matches, `LastMsgAt` populated, no error.
- Empty IDs slice → empty result, no error.

### 7.6 `room-service` handler unit tests

Regenerate mocks first (`make generate SERVICE=room-service`). Add mockgen
for `RoomKeyStore` (from `pkg/roomkeystore`).

In `handler_test.go`, table-driven `TestHandler_handleRoomsInfoBatch`:

- Happy path: 3 rooms requested, all exist, 2 have keys, 1 has no key →
  correct `Found`/`PrivateKey`/`KeyVersion`, input order preserved.
- One room missing from Mongo → `Found=false`.
- Empty `RoomIDs` → batch-level error.
- `RoomIDs` > `maxBatchSize` → batch-level error.
- Invalid JSON → batch-level error.
- Mongo error → batch-level error.
- Valkey error → batch-level error.
- Duplicate IDs → duplicate entries.
- `LastMsgAt` zero → field present with value `0` in marshaled JSON.
- `LastMsgAt` set → equals `.UTC().UnixMilli()`.

Tests target `handleRoomsInfoBatch` (the bytes-returning helper), not the
dispatcher, so errors are returned rather than sent via the NATS connection.

### 7.7 `room-service` end-to-end integration test

Add `TestRoomsInfoBatchRPC` to `room-service/integration_test.go`:

- Spin up testcontainers Mongo + Valkey + NATS.
- Start the handler with real stores.
- Seed 3 rooms in Mongo with differing `LastMsgAt`; seed 2 rooms' keys in
  Valkey.
- Issue a NATS request on `chat.user.tester.request.rooms.<siteID>.info.batch`
  with all 3 IDs + 1 missing ID.
- Assert response has 4 entries, input order preserved, correct `Found`,
  `PrivateKey` present on 2, `PrivateKey` nil on 1, `Found=false` on the
  missing one.

### 7.8 Coverage targets

- Handler aggregation logic: ≥ 90%.
- `pkg/roomkeystore.GetMany`: ≥ 90%.
- `ListRoomsByIDs` on Mongo store: covered by integration test (unit coverage
  not meaningful for a one-line `Find`).

## 8. Out of scope

- Cross-site aggregation inside room-service (caller fans out by site via the
  `{siteID}` subject token).
- Per-room `Error` population for partial backend failures (reserved in the
  model; initial implementation fails the whole batch on backend errors).
- Strict auth gating on this RPC beyond existing NATS trust (if stricter auth
  is needed, that's a separate project scoped with the auth-callout service).
- Close-hook support in `pkg/roomkeystore` (revisit when the package adds
  one).

## 9. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Accidental logging of private keys. | Structured logging fields are explicitly named in the spec (no `req`/`resp` dump); code review checks for `slog.Info("...", "privateKey", ...)` anti-patterns. |
| Request storm (a caller sends N=500 repeatedly). | `MAX_BATCH_SIZE` cap + 5s deadline + queue-group replicas for horizontal scaling. Future: add a rate-limit or per-caller quota if needed. |
| Corrupt Valkey data fails an entire batch. | Intentional — storage-integrity bug should be loud, not silently zero. Error is wrapped with roomID so operators can triage. |
| Mongo and Valkey falling out of sync (room exists, key missing, or vice versa). | The contract explicitly distinguishes `Found=false` (no Mongo doc) from `Found=true, PrivateKey=nil` (no Valkey key). Both states are valid; callers are responsible for interpreting them. |
