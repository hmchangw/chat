# Room Key Version: String to Integer

## Summary

Change the room key version identifier from a caller-provided `string` to a store-managed auto-incrementing `int`. This enables numeric comparisons ("is this key newer?") and reduces storage/transmission overhead.

## Motivation

- Versions are always sequential; an integer type reflects this and enables ordering comparisons.
- Integer versions are smaller on the wire and in storage compared to UUID strings.
- Auto-increment in the store eliminates version-generation burden from callers.

## Design

### Data Model

**`pkg/model/event.go` -- `RoomKeyEvent`:**

```go
type RoomKeyEvent struct {
    RoomID     string `json:"roomId"`
    Version    int    `json:"version"`
    PublicKey  []byte `json:"publicKey"`
    PrivateKey []byte `json:"privateKey"`
}
```

- Field renamed from `VersionID` to `Version`.
- JSON tag changes from `"versionId"` to `"version"`.

**`pkg/roomkeystore/roomkeystore.go` -- `VersionedKeyPair`:**

```go
type VersionedKeyPair struct {
    Version int
    KeyPair RoomKeyPair
}
```

- Field renamed from `VersionID string` to `Version int`.

**Valkey storage:** The `"ver"` hash field stores the version as a decimal string (`"0"`, `"1"`, ...). Parsed with `strconv.Atoi` on read.

### Interface

```go
type RoomKeyStore interface {
    Set(ctx context.Context, roomID string, pair RoomKeyPair) (int, error)
    Get(ctx context.Context, roomID string) (*VersionedKeyPair, error)
    GetByVersion(ctx context.Context, roomID string, version int) (*RoomKeyPair, error)
    Rotate(ctx context.Context, roomID string, newPair RoomKeyPair) (int, error)
    Delete(ctx context.Context, roomID string) error
}
```

- `Set` -- drops `versionID` parameter, returns `(int, error)` with assigned version `0`.
- `Rotate` -- drops `versionID` parameter, returns `(int, error)` with new version (previous + 1).
- `GetByVersion` -- accepts `version int` instead of `versionID string`.
- `Get` and `Delete` -- signatures unchanged (but `Get` returns `Version int` in the struct).

### Internal Interface (`hashCommander`)

```go
type hashCommander interface {
    hset(ctx context.Context, key string, pub, priv string) error
    hgetall(ctx context.Context, key string) (map[string]string, error)
    rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv string, gracePeriod time.Duration) (int, error)
    deletePipeline(ctx context.Context, currentKey, prevKey string) error
}
```

- `hset` -- drops `ver` parameter; always writes `"ver"` as `"0"`.
- `rotatePipeline` -- drops `ver` parameter; returns `(int, error)` with the new version from the Lua script.

### Implementation

**`valkeyStore.Set`:** Encodes keys, calls `hset` (which writes version `"0"`), returns `0, nil`.

**`valkeyStore.Get`:** Parses `fields["ver"]` with `strconv.Atoi`, populates `VersionedKeyPair.Version`.

**`valkeyStore.GetByVersion`:** Converts the `int` parameter to string with `strconv.Itoa` for comparison against `fields["ver"]`.

**`valkeyStore.Rotate`:** Calls `rotatePipeline` without a version parameter. Parses and returns the new version from the Lua script result.

**Lua script (`rotateScript`):**

```lua
local currentKey = KEYS[1]
local prevKey    = KEYS[2]
local newPub     = ARGV[1]
local newPriv    = ARGV[2]
local graceSec   = tonumber(ARGV[3])

local cur = redis.call('HGETALL', currentKey)
if #cur == 0 then
    return redis.error_reply('no current key')
end

local curVer = tonumber(redis.call('HGET', currentKey, 'ver'))
local newVer = curVer + 1

redis.call('DEL', prevKey)
redis.call('HSET', prevKey, unpack(cur))
redis.call('EXPIRE', prevKey, graceSec)

redis.call('HSET', currentKey, 'pub', newPub, 'priv', newPriv, 'ver', tostring(newVer))
return newVer
```

- `ARGV[3]` is now `graceSec` (previously `ARGV[4]`).
- Script reads the current version, increments, writes atomically, and returns the new version.

**`redisAdapter.rotatePipeline`:** Parses the Lua script's integer return value and returns `(int, error)`.

### Downstream Consumers

**`pkg/roomkeysender/roomkeysender.go`:** No code change needed. JSON serialization of `RoomKeyEvent` automatically outputs `"version": 0` instead of `"versionId": "v1"`.

**NATS header `X-Room-Key-Version`:** Callers that set this header use `strconv.Itoa(event.Version)` instead of passing a string directly.

**TypeScript test client (`pkg/roomkeysender/testdata/client.ts`):**

- `versionId: string` becomes `version: number` in the `RoomKeyEvent` interface.
- Key lookup map: `Map<string, KeyPair>` becomes `Map<number, KeyPair>`.
- Header parsing: read `X-Room-Key-Version`; if absent/empty, the message is unencrypted (skip decryption); if present, `parseInt()` and look up the key pair.

### Version Semantics

- First key for a room is version `0`.
- Each rotation increments by 1: 0, 1, 2, ...
- The store is the sole authority for version assignment; callers never specify a version.

## Testing

**Unit tests (`roomkeystore_test.go`):**
- Remove version parameters from `Set` and `Rotate` calls.
- Assert returned version values (`0` from `Set`, incremented values from `Rotate`).
- Update `GetByVersion` calls to pass `int` instead of `string`.
- Test data: `"v1"` becomes `0`, `"v2"` becomes `1`, etc.

**Integration tests (`roomkeystore/integration_test.go`):**
- Same parameter and assertion updates as unit tests.
- Verify auto-increment across multiple rotations (0, 1, 2).

**RoomKeySender tests (`roomkeysender_test.go`):**
- Update `RoomKeyEvent` construction to use `Version: 0` instead of `VersionID: "v-abc-123"`.
- Assert JSON output uses `"version"` key with integer value.

**RoomKeySender integration tests (`roomkeysender/integration_test.go`):**
- Update header assertions to expect decimal string representation.
- Verify unencrypted message path (absent header) still works.

## Files Changed

| File | Change |
|------|--------|
| `pkg/model/event.go` | `VersionID string` to `Version int`, update JSON tag |
| `pkg/roomkeystore/roomkeystore.go` | `VersionedKeyPair.Version int`, updated interface signatures, updated `valkeyStore` methods |
| `pkg/roomkeystore/adapter.go` | Updated `hashCommander` signatures, updated Lua script, updated `redisAdapter` methods |
| `pkg/roomkeystore/roomkeystore_test.go` | Updated test cases for new signatures and types |
| `pkg/roomkeystore/integration_test.go` | Updated integration tests |
| `pkg/roomkeysender/roomkeysender_test.go` | Updated event construction |
| `pkg/roomkeysender/integration_test.go` | Updated header and event assertions |
| `pkg/roomkeysender/testdata/client.ts` | Updated TypeScript types and header parsing |
| `pkg/roomkeystore/mock_store_test.go` | Regenerated via `make generate` |
