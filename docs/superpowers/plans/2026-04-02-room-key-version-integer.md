# Room Key Version Integer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Change the room key version identifier from a caller-provided `string` to a store-managed auto-incrementing `int`, starting at `0`.

**Architecture:** The `RoomKeyStore` interface drops the `versionID` parameter from `Set` and `Rotate`, returning the assigned version instead. The Lua rotation script reads the current version from the hash, increments it atomically, and returns the new value. All downstream types (`VersionedKeyPair`, `RoomKeyEvent`) change from `string` to `int`.

**Tech Stack:** Go, Valkey (Redis-compatible), Lua scripting, NATS, TypeScript (test client)

**Spec:** `docs/superpowers/specs/2026-04-02-room-key-version-integer-design.md`

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `pkg/model/event.go` | Modify | `RoomKeyEvent.VersionID string` → `Version int`, JSON tag `"versionId"` → `"version"` |
| `pkg/model/model_test.go` | Modify | Update `TestRoomKeyEventJSON` to use `Version: 42` instead of `VersionID: "v-abc-123"` |
| `pkg/roomkeystore/roomkeystore.go` | Modify | `VersionedKeyPair.Version int`, updated `RoomKeyStore` interface, updated `hashCommander` interface, updated `valkeyStore` methods |
| `pkg/roomkeystore/adapter.go` | Modify | Updated `redisAdapter` methods, updated Lua rotation script |
| `pkg/roomkeystore/roomkeystore_test.go` | Modify | Updated `fakeHashClient`, updated all test cases |
| `pkg/roomkeystore/integration_test.go` | Modify | Updated all integration test cases |
| `pkg/roomkeysender/roomkeysender_test.go` | Modify | Updated `RoomKeyEvent` construction and assertions |
| `pkg/roomkeysender/integration_test.go` | Modify | Updated version ID usage from string to int |
| `pkg/roomkeysender/testdata/client.ts` | Modify | Updated TypeScript types and header parsing |

---

### Task 1: Data Model Changes

Update the two structs that carry version information and their corresponding tests.

**Files:**
- Modify: `pkg/model/event.go:71-76`
- Modify: `pkg/model/model_test.go:174-193`
- Modify: `pkg/roomkeystore/roomkeystore.go:20-24`

- [ ] **Step 1: Update `RoomKeyEvent` in `pkg/model/event.go`**

Change lines 71-76 from:

```go
type RoomKeyEvent struct {
	RoomID     string `json:"roomId"`
	VersionID  string `json:"versionId"`
	PublicKey  []byte `json:"publicKey"`
	PrivateKey []byte `json:"privateKey"`
}
```

to:

```go
type RoomKeyEvent struct {
	RoomID     string `json:"roomId"`
	Version    int    `json:"version"`
	PublicKey  []byte `json:"publicKey"`
	PrivateKey []byte `json:"privateKey"`
}
```

- [ ] **Step 2: Update `VersionedKeyPair` in `pkg/roomkeystore/roomkeystore.go`**

Change lines 20-24 from:

```go
// VersionedKeyPair pairs a key pair with its caller-assigned version identifier.
type VersionedKeyPair struct {
	VersionID string
	KeyPair   RoomKeyPair
}
```

to:

```go
// VersionedKeyPair pairs a key pair with its store-assigned version number.
type VersionedKeyPair struct {
	Version int
	KeyPair RoomKeyPair
}
```

- [ ] **Step 3: Update `TestRoomKeyEventJSON` in `pkg/model/model_test.go`**

Change lines 174-193 from:

```go
func TestRoomKeyEventJSON(t *testing.T) {
	src := model.RoomKeyEvent{
		RoomID:     "room-1",
		VersionID:  "v-abc-123",
		PublicKey:  []byte{0x04, 0x01, 0x02, 0x03},
		PrivateKey: []byte{0x0a, 0x0b, 0x0c},
	}
```

to:

```go
func TestRoomKeyEventJSON(t *testing.T) {
	src := model.RoomKeyEvent{
		RoomID:     "room-1",
		Version:    42,
		PublicKey:  []byte{0x04, 0x01, 0x02, 0x03},
		PrivateKey: []byte{0x0a, 0x0b, 0x0c},
	}
```

- [ ] **Step 4: Run model tests to verify**

```bash
make test SERVICE=pkg/model
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/model/event.go pkg/model/model_test.go pkg/roomkeystore/roomkeystore.go
git commit -m "refactor: change room key version from string to int in data models"
```

---

### Task 2: Internal Interface and Valkey Adapter

Update `hashCommander` interface, `redisAdapter` methods, and the Lua rotation script. After this task the code will not compile (callers still use old signatures) — that's expected and fixed in Task 3.

**Files:**
- Modify: `pkg/roomkeystore/roomkeystore.go:42-49` (hashCommander interface)
- Modify: `pkg/roomkeystore/adapter.go:17-59` (redisAdapter + Lua script)

- [ ] **Step 1: Update `hashCommander` interface in `pkg/roomkeystore/roomkeystore.go`**

Change lines 42-49 from:

```go
type hashCommander interface {
	hset(ctx context.Context, key string, pub, priv, ver string) error
	hgetall(ctx context.Context, key string) (map[string]string, error)
	rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv, ver string, gracePeriod time.Duration) error
	deletePipeline(ctx context.Context, currentKey, prevKey string) error
}
```

to:

```go
type hashCommander interface {
	hset(ctx context.Context, key string, pub, priv string) error
	hgetall(ctx context.Context, key string) (map[string]string, error)
	rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv string, gracePeriod time.Duration) (int, error)
	deletePipeline(ctx context.Context, currentKey, prevKey string) error
}
```

- [ ] **Step 2: Update `redisAdapter.hset` in `pkg/roomkeystore/adapter.go`**

Change lines 17-19 from:

```go
func (a *redisAdapter) hset(ctx context.Context, key string, pub, priv, ver string) error {
	return a.c.HSet(ctx, key, "pub", pub, "priv", priv, "ver", ver).Err()
}
```

to:

```go
func (a *redisAdapter) hset(ctx context.Context, key string, pub, priv string) error {
	return a.c.HSet(ctx, key, "pub", pub, "priv", priv, "ver", "0").Err()
}
```

- [ ] **Step 3: Update the Lua rotation script in `pkg/roomkeystore/adapter.go`**

Change lines 28-47 from:

```go
var rotateScript = redis.NewScript(`
local currentKey = KEYS[1]
local prevKey    = KEYS[2]
local newPub     = ARGV[1]
local newPriv    = ARGV[2]
local newVer     = ARGV[3]
local graceSec   = tonumber(ARGV[4])

local cur = redis.call('HGETALL', currentKey)
if #cur == 0 then
    return redis.error_reply('no current key')
end

redis.call('DEL', prevKey)
redis.call('HSET', prevKey, unpack(cur))
redis.call('EXPIRE', prevKey, graceSec)

redis.call('HSET', currentKey, 'pub', newPub, 'priv', newPriv, 'ver', newVer)
return 1
`)
```

to:

```go
var rotateScript = redis.NewScript(`
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
`)
```

- [ ] **Step 4: Update `redisAdapter.rotatePipeline` in `pkg/roomkeystore/adapter.go`**

Change lines 49-59 from:

```go
func (a *redisAdapter) rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv, ver string, gracePeriod time.Duration) error {
	graceSec := int(gracePeriod.Seconds())
	if graceSec < 1 {
		graceSec = 1
	}
	err := rotateScript.Run(ctx, a.c, []string{currentKey, prevKey}, pub, priv, ver, graceSec).Err()
	if err != nil && strings.Contains(err.Error(), "no current key") {
		return ErrNoCurrentKey
	}
	return err
}
```

to:

```go
func (a *redisAdapter) rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv string, gracePeriod time.Duration) (int, error) {
	graceSec := int(gracePeriod.Seconds())
	if graceSec < 1 {
		graceSec = 1
	}
	result, err := rotateScript.Run(ctx, a.c, []string{currentKey, prevKey}, pub, priv, graceSec).Int()
	if err != nil && strings.Contains(err.Error(), "no current key") {
		return 0, ErrNoCurrentKey
	}
	return result, err
}
```

- [ ] **Step 5: Commit (code does not compile yet — that's OK)**

```bash
git add pkg/roomkeystore/roomkeystore.go pkg/roomkeystore/adapter.go
git commit -m "refactor: update hashCommander interface and Lua script for integer versions"
```

---

### Task 3: RoomKeyStore Interface and valkeyStore Methods

Update the public interface and all `valkeyStore` method implementations. After this task the production code compiles but tests do not.

**Files:**
- Modify: `pkg/roomkeystore/roomkeystore.go:27-33` (interface)
- Modify: `pkg/roomkeystore/roomkeystore.go:67-77` (Set)
- Modify: `pkg/roomkeystore/roomkeystore.go:79-96` (Get)
- Modify: `pkg/roomkeystore/roomkeystore.go:98-128` (GetByVersion)
- Modify: `pkg/roomkeystore/roomkeystore.go:130-139` (Rotate)

- [ ] **Step 1: Update `RoomKeyStore` interface**

Change lines 26-33 from:

```go
// RoomKeyStore defines storage operations for room encryption key pairs.
type RoomKeyStore interface {
	Set(ctx context.Context, roomID string, versionID string, pair RoomKeyPair) error
	Get(ctx context.Context, roomID string) (*VersionedKeyPair, error)
	GetByVersion(ctx context.Context, roomID, versionID string) (*RoomKeyPair, error)
	Rotate(ctx context.Context, roomID string, versionID string, newPair RoomKeyPair) error
	Delete(ctx context.Context, roomID string) error
}
```

to:

```go
// RoomKeyStore defines storage operations for room encryption key pairs.
type RoomKeyStore interface {
	Set(ctx context.Context, roomID string, pair RoomKeyPair) (int, error)
	Get(ctx context.Context, roomID string) (*VersionedKeyPair, error)
	GetByVersion(ctx context.Context, roomID string, version int) (*RoomKeyPair, error)
	Rotate(ctx context.Context, roomID string, newPair RoomKeyPair) (int, error)
	Delete(ctx context.Context, roomID string) error
}
```

- [ ] **Step 2: Add `strconv` to imports**

Add `"strconv"` to the import block at the top of `pkg/roomkeystore/roomkeystore.go`:

```go
import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"
)
```

- [ ] **Step 3: Update `valkeyStore.Set`**

Change lines 67-77 from:

```go
// Set stores pair in Valkey as a hash with no TTL.
// Does not touch the previous key slot.
func (s *valkeyStore) Set(ctx context.Context, roomID string, versionID string, pair RoomKeyPair) error {
	pub := base64.StdEncoding.EncodeToString(pair.PublicKey)
	priv := base64.StdEncoding.EncodeToString(pair.PrivateKey)
	key := roomkey(roomID)
	if err := s.client.hset(ctx, key, pub, priv, versionID); err != nil {
		return fmt.Errorf("set room key: %w", err)
	}
	return nil
}
```

to:

```go
// Set stores pair in Valkey as a hash with version 0 and no TTL.
// Does not touch the previous key slot.
func (s *valkeyStore) Set(ctx context.Context, roomID string, pair RoomKeyPair) (int, error) {
	pub := base64.StdEncoding.EncodeToString(pair.PublicKey)
	priv := base64.StdEncoding.EncodeToString(pair.PrivateKey)
	key := roomkey(roomID)
	if err := s.client.hset(ctx, key, pub, priv); err != nil {
		return 0, fmt.Errorf("set room key: %w", err)
	}
	return 0, nil
}
```

- [ ] **Step 4: Update `valkeyStore.Get`**

Change lines 79-96 from:

```go
// Get retrieves the current key pair for roomID. Returns (nil, nil) if the key does not exist.
func (s *valkeyStore) Get(ctx context.Context, roomID string) (*VersionedKeyPair, error) {
	fields, err := s.client.hgetall(ctx, roomkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get room key: %w", err)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	pair, err := decodeKeyPair(fields)
	if err != nil {
		return nil, fmt.Errorf("get room key: %w", err)
	}
	return &VersionedKeyPair{
		VersionID: fields["ver"],
		KeyPair:   *pair,
	}, nil
}
```

to:

```go
// Get retrieves the current key pair for roomID. Returns (nil, nil) if the key does not exist.
func (s *valkeyStore) Get(ctx context.Context, roomID string) (*VersionedKeyPair, error) {
	fields, err := s.client.hgetall(ctx, roomkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get room key: %w", err)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	pair, err := decodeKeyPair(fields)
	if err != nil {
		return nil, fmt.Errorf("get room key: %w", err)
	}
	ver, err := strconv.Atoi(fields["ver"])
	if err != nil {
		return nil, fmt.Errorf("get room key: parse version: %w", err)
	}
	return &VersionedKeyPair{
		Version: ver,
		KeyPair: *pair,
	}, nil
}
```

- [ ] **Step 5: Update `valkeyStore.GetByVersion`**

Change lines 98-128 from:

```go
// GetByVersion retrieves the key pair matching versionID from either the current or previous slot.
// Returns (nil, nil) if neither matches or both are absent.
func (s *valkeyStore) GetByVersion(ctx context.Context, roomID, versionID string) (*RoomKeyPair, error) {
	// Check current key.
	currentFields, err := s.client.hgetall(ctx, roomkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get room key by version: %w", err)
	}
	if len(currentFields) > 0 && currentFields["ver"] == versionID {
		pair, err := decodeKeyPair(currentFields)
		if err != nil {
			return nil, fmt.Errorf("get room key by version: %w", err)
		}
		return pair, nil
	}

	// Check previous key.
	prevFields, err := s.client.hgetall(ctx, roomprevkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get room key by version: %w", err)
	}
	if len(prevFields) > 0 && prevFields["ver"] == versionID {
		pair, err := decodeKeyPair(prevFields)
		if err != nil {
			return nil, fmt.Errorf("get room key by version: %w", err)
		}
		return pair, nil
	}

	return nil, nil
}
```

to:

```go
// GetByVersion retrieves the key pair matching version from either the current or previous slot.
// Returns (nil, nil) if neither matches or both are absent.
func (s *valkeyStore) GetByVersion(ctx context.Context, roomID string, version int) (*RoomKeyPair, error) {
	verStr := strconv.Itoa(version)

	// Check current key.
	currentFields, err := s.client.hgetall(ctx, roomkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get room key by version: %w", err)
	}
	if len(currentFields) > 0 && currentFields["ver"] == verStr {
		pair, err := decodeKeyPair(currentFields)
		if err != nil {
			return nil, fmt.Errorf("get room key by version: %w", err)
		}
		return pair, nil
	}

	// Check previous key.
	prevFields, err := s.client.hgetall(ctx, roomprevkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get room key by version: %w", err)
	}
	if len(prevFields) > 0 && prevFields["ver"] == verStr {
		pair, err := decodeKeyPair(prevFields)
		if err != nil {
			return nil, fmt.Errorf("get room key by version: %w", err)
		}
		return pair, nil
	}

	return nil, nil
}
```

- [ ] **Step 6: Update `valkeyStore.Rotate`**

Change lines 130-139 from:

```go
// Rotate atomically moves the current key to the previous slot (with grace period TTL)
// and writes newPair as the current key. Returns ErrNoCurrentKey if no current key exists.
func (s *valkeyStore) Rotate(ctx context.Context, roomID string, versionID string, newPair RoomKeyPair) error {
	pub := base64.StdEncoding.EncodeToString(newPair.PublicKey)
	priv := base64.StdEncoding.EncodeToString(newPair.PrivateKey)
	if err := s.client.rotatePipeline(ctx, roomkey(roomID), roomprevkey(roomID), pub, priv, versionID, s.gracePeriod); err != nil {
		return fmt.Errorf("rotate room key: %w", err)
	}
	return nil
}
```

to:

```go
// Rotate atomically moves the current key to the previous slot (with grace period TTL)
// and writes newPair as the current key with an auto-incremented version.
// Returns the new version number. Returns ErrNoCurrentKey if no current key exists.
func (s *valkeyStore) Rotate(ctx context.Context, roomID string, newPair RoomKeyPair) (int, error) {
	pub := base64.StdEncoding.EncodeToString(newPair.PublicKey)
	priv := base64.StdEncoding.EncodeToString(newPair.PrivateKey)
	ver, err := s.client.rotatePipeline(ctx, roomkey(roomID), roomprevkey(roomID), pub, priv, s.gracePeriod)
	if err != nil {
		return 0, fmt.Errorf("rotate room key: %w", err)
	}
	return ver, nil
}
```

- [ ] **Step 7: Verify compilation**

```bash
cd /home/user/chat && go build ./pkg/roomkeystore/
```

Expected: Compilation succeeds for the package itself (tests will fail until Task 4).

- [ ] **Step 8: Commit**

```bash
git add pkg/roomkeystore/roomkeystore.go
git commit -m "refactor: update RoomKeyStore interface and valkeyStore for integer versions"
```

---

### Task 4: Unit Tests

Update `fakeHashClient` and all unit test cases in `pkg/roomkeystore/roomkeystore_test.go`.

**Files:**
- Modify: `pkg/roomkeystore/roomkeystore_test.go`

- [ ] **Step 1: Update `fakeHashClient.hset` signature and implementation**

Change:

```go
func (f *fakeHashClient) hset(_ context.Context, key string, pub, priv, ver string) error {
	if f.hsetErr != nil {
		return f.hsetErr
	}
	if f.store == nil {
		f.store = make(map[string]map[string]string)
	}
	f.store[key] = map[string]string{"pub": pub, "priv": priv, "ver": ver}
	return nil
}
```

to:

```go
func (f *fakeHashClient) hset(_ context.Context, key string, pub, priv string) error {
	if f.hsetErr != nil {
		return f.hsetErr
	}
	if f.store == nil {
		f.store = make(map[string]map[string]string)
	}
	f.store[key] = map[string]string{"pub": pub, "priv": priv, "ver": "0"}
	return nil
}
```

- [ ] **Step 2: Update `fakeHashClient.rotatePipeline` signature and implementation**

Change:

```go
func (f *fakeHashClient) rotatePipeline(_ context.Context, currentKey, prevKey string, pub, priv, ver string, _ time.Duration) error {
	if f.rotatePipelineErr != nil {
		return f.rotatePipelineErr
	}
	if f.store == nil {
		return ErrNoCurrentKey
	}
	cur, ok := f.store[currentKey]
	if !ok {
		return ErrNoCurrentKey
	}
	// Copy current to prev.
	f.store[prevKey] = map[string]string{"pub": cur["pub"], "priv": cur["priv"], "ver": cur["ver"]}
	// Write new current.
	f.store[currentKey] = map[string]string{"pub": pub, "priv": priv, "ver": ver}
	return nil
}
```

to:

```go
func (f *fakeHashClient) rotatePipeline(_ context.Context, currentKey, prevKey string, pub, priv string, _ time.Duration) (int, error) {
	if f.rotatePipelineErr != nil {
		return 0, f.rotatePipelineErr
	}
	if f.store == nil {
		return 0, ErrNoCurrentKey
	}
	cur, ok := f.store[currentKey]
	if !ok {
		return 0, ErrNoCurrentKey
	}
	curVer, _ := strconv.Atoi(cur["ver"])
	newVer := curVer + 1
	// Copy current to prev.
	f.store[prevKey] = map[string]string{"pub": cur["pub"], "priv": cur["priv"], "ver": cur["ver"]}
	// Write new current.
	f.store[currentKey] = map[string]string{"pub": pub, "priv": priv, "ver": strconv.Itoa(newVer)}
	return newVer, nil
}
```

Also add `"strconv"` to the import block at the top of the file.

- [ ] **Step 3: Update `TestValkeyStore_Set`**

Replace the test table and assertions. Key changes:
- Remove `versionID` field from test cases.
- Change `store.Set` call from `store.Set(ctx, tt.roomID, tt.versionID, pair)` to `ver, err := store.Set(ctx, tt.roomID, pair)`.
- Assert `ver == 0` on success.
- Assert `stored["ver"] == "0"` instead of `stored["ver"] == tt.versionID`.

Full replacement for the test function:

```go
func TestValkeyStore_Set(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	pair := RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey}

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		wantErr     bool
		errContains string
	}{
		{
			name:   "happy path — stores key pair with version 0",
			fake:   &fakeHashClient{},
			roomID: "room-1",
		},
		{
			name:        "hset error — returns wrapped error",
			fake:        &fakeHashClient{hsetErr: errors.New("connection refused")},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "set room key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			ver, err := store.Set(context.Background(), tt.roomID, pair)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 0, ver)
			// Verify the hash was written under the correct Valkey key.
			stored := tt.fake.store[roomkey(tt.roomID)]
			require.NotNil(t, stored, "hash should exist in fake store")
			assert.NotEmpty(t, stored["pub"], "pub field should be set")
			assert.NotEmpty(t, stored["priv"], "priv field should be set")
			assert.Equal(t, "0", stored["ver"], "ver field should be 0")
		})
	}
}
```

- [ ] **Step 4: Update `TestValkeyStore_Get`**

Key changes:
- All `store.Set` calls lose the version parameter.
- `wantVer` changes from `string` to `int`.
- Assertion changes from `got.VersionID` to `got.Version`.
- Corrupted data test cases keep `"ver": "v1"` (non-numeric) — but now they should also trigger a parse-version error. Update the corrupted-base64 test cases to use numeric version strings instead, and add a new test case for non-numeric version.

Full replacement:

```go
func TestValkeyStore_Get(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		wantPair    *RoomKeyPair
		wantVer     int
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path — returns VersionedKeyPair with correct Version",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				store := newTestStore(f)
				_, _ = store.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				return f
			}(),
			roomID:   "room-1",
			wantPair: &RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey},
			wantVer:  0,
		},
		{
			name:   "missing key — returns nil, nil",
			fake:   &fakeHashClient{},
			roomID: "nonexistent",
		},
		{
			name:        "hgetall error — returns wrapped error",
			fake:        &fakeHashClient{hgetallErr: errors.New("io timeout")},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "get room key",
		},
		{
			name: "corrupted pub base64 — returns error",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					roomkey("room-1"): {"pub": "!!!notbase64!!!", "priv": "AQID", "ver": "0"},
				},
			},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "get room key",
		},
		{
			name: "corrupted priv base64 — returns error",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					roomkey("room-1"): {"pub": "AQID", "priv": "!!!notbase64!!!", "ver": "0"},
				},
			},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "get room key",
		},
		{
			name: "non-numeric version — returns parse error",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					roomkey("room-1"): {"pub": "AQID", "priv": "AQID", "ver": "not-a-number"},
				},
			},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "parse version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			got, err := store.Get(context.Background(), tt.roomID)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tt.wantPair == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantVer, got.Version)
			assert.Equal(t, tt.wantPair.PublicKey, got.KeyPair.PublicKey)
			assert.Equal(t, tt.wantPair.PrivateKey, got.KeyPair.PrivateKey)
		})
	}
}
```

- [ ] **Step 5: Update `TestValkeyStore_GetByVersion`**

Key changes:
- All `store.Set` and `store.Rotate` calls lose the version parameter.
- `versionID string` field becomes `version int` in test cases.
- Version values: `"v1"` → `0`, `"v2"` → `1`, `"nonexistent-version"` → `999`, `"v-old"` → `99`.
- Corrupted base64 test cases use integer versions for the `"ver"` hash field.

Full replacement:

```go
func TestValkeyStore_GetByVersion(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	pubKey2 := bytes.Repeat([]byte{0x11}, 65)
	privKey2 := bytes.Repeat([]byte{0x22}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		version     int
		wantPair    *RoomKeyPair
		wantErr     bool
		errContains string
	}{
		{
			name: "matches current key",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_, _ = s.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				return f
			}(),
			roomID:   "room-1",
			version:  0,
			wantPair: &RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey},
		},
		{
			name: "matches previous key after rotation",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_, _ = s.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				_, _ = s.Rotate(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey2, PrivateKey: privKey2})
				return f
			}(),
			roomID:   "room-1",
			version:  0,
			wantPair: &RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey},
		},
		{
			name: "no match — returns nil, nil",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_, _ = s.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				return f
			}(),
			roomID:  "room-1",
			version: 999,
		},
		{
			name:    "no keys at all — returns nil, nil",
			fake:    &fakeHashClient{},
			roomID:  "room-1",
			version: 0,
		},
		{
			name:        "hgetall error on current key — returns wrapped error",
			fake:        &fakeHashClient{hgetallErr: errors.New("connection reset")},
			roomID:      "room-1",
			version:     0,
			wantErr:     true,
			errContains: "get room key by version",
		},
		{
			name: "hgetall error on previous key — returns wrapped error",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_, _ = s.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				f.hgetallErr = errors.New("connection reset")
				f.hgetallErrOnCall = 2
				return f
			}(),
			roomID:      "room-1",
			version:     99,
			wantErr:     true,
			errContains: "get room key by version",
		},
		{
			name: "corrupted previous key base64 — returns error",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					roomkey("room-1"):     {"pub": "AQID", "priv": "AQID", "ver": "0"},
					roomprevkey("room-1"): {"pub": "!!!bad!!!", "priv": "AQID", "ver": "99"},
				},
			},
			roomID:      "room-1",
			version:     99,
			wantErr:     true,
			errContains: "get room key by version",
		},
		{
			name: "corrupted current key base64 — returns error when version matches current",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					roomkey("room-1"): {"pub": "!!!bad!!!", "priv": "AQID", "ver": "0"},
				},
			},
			roomID:      "room-1",
			version:     0,
			wantErr:     true,
			errContains: "get room key by version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			got, err := store.GetByVersion(context.Background(), tt.roomID, tt.version)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tt.wantPair == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantPair.PublicKey, got.PublicKey)
			assert.Equal(t, tt.wantPair.PrivateKey, got.PrivateKey)
		})
	}
}
```

- [ ] **Step 6: Update `TestValkeyStore_Rotate`**

Key changes:
- All `store.Set` and `store.Rotate` calls lose the version parameter.
- `store.Rotate` returns `(int, error)` — assert returned version.
- `got.VersionID` → `got.Version`, assert `got.Version == 1` (after one rotation from 0).

Full replacement:

```go
func TestValkeyStore_Rotate(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	newPubKey := bytes.Repeat([]byte{0x11}, 65)
	newPrivKey := bytes.Repeat([]byte{0x22}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		setupFn     func(s *valkeyStore)
		wantVer     int
		wantErr     bool
		errContains string
		errIs       error
	}{
		{
			name:   "happy path — new pair becomes current, old becomes previous",
			fake:   &fakeHashClient{},
			roomID: "room-1",
			setupFn: func(s *valkeyStore) {
				_, _ = s.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
			},
			wantVer: 1,
		},
		{
			name:        "no current key — returns ErrNoCurrentKey",
			fake:        &fakeHashClient{},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "rotate room key",
			errIs:       ErrNoCurrentKey,
		},
		{
			name:   "replaces existing previous key",
			fake:   &fakeHashClient{},
			roomID: "room-1",
			setupFn: func(s *valkeyStore) {
				_, _ = s.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				_, _ = s.Rotate(context.Background(), "room-1", RoomKeyPair{PublicKey: newPubKey, PrivateKey: newPrivKey})
			},
			wantVer: 2,
		},
		{
			name:   "pipeline error — returns wrapped error",
			fake:   &fakeHashClient{rotatePipelineErr: errors.New("pipeline broken")},
			roomID: "room-1",
			setupFn: func(s *valkeyStore) {
				s.client.(*fakeHashClient).rotatePipelineErr = nil
				_, _ = s.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				s.client.(*fakeHashClient).rotatePipelineErr = errors.New("pipeline broken")
			},
			wantErr:     true,
			errContains: "rotate room key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			if tt.setupFn != nil {
				tt.setupFn(store)
			}
			ver, err := store.Rotate(context.Background(), tt.roomID, RoomKeyPair{PublicKey: newPubKey, PrivateKey: newPrivKey})
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				if tt.errIs != nil {
					assert.True(t, errors.Is(err, tt.errIs), "expected errors.Is match for %v", tt.errIs)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantVer, ver)

			// Verify new pair is current with correct version.
			got, err := store.Get(context.Background(), tt.roomID)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, tt.wantVer, got.Version)
			assert.Equal(t, newPubKey, got.KeyPair.PublicKey)
			assert.Equal(t, newPrivKey, got.KeyPair.PrivateKey)
		})
	}
}
```

- [ ] **Step 7: Update `TestValkeyStore_Delete`**

Key changes:
- All `store.Set` and `store.Rotate` calls lose the version parameter.

Full replacement:

```go
func TestValkeyStore_Delete(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path — deletes both current and previous keys",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_, _ = s.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				_, _ = s.Rotate(context.Background(), "room-1", RoomKeyPair{
					PublicKey:  bytes.Repeat([]byte{0x11}, 65),
					PrivateKey: bytes.Repeat([]byte{0x22}, 32),
				})
				return f
			}(),
			roomID: "room-1",
		},
		{
			name:   "missing key — no-op, no error",
			fake:   &fakeHashClient{},
			roomID: "nonexistent",
		},
		{
			name:        "deletePipeline error — returns wrapped error",
			fake:        &fakeHashClient{deletePipelineErr: errors.New("connection lost")},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "delete room key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			err := store.Delete(context.Background(), tt.roomID)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			_, exists := tt.fake.store[roomkey(tt.roomID)]
			assert.False(t, exists, "current key should be removed after Delete")
			_, exists = tt.fake.store[roomprevkey(tt.roomID)]
			assert.False(t, exists, "previous key should be removed after Delete")
		})
	}
}
```

- [ ] **Step 8: Run unit tests**

```bash
make test SERVICE=pkg/roomkeystore
```

Expected: All tests PASS.

- [ ] **Step 9: Commit**

```bash
git add pkg/roomkeystore/roomkeystore_test.go
git commit -m "test: update roomkeystore unit tests for integer version"
```

---

### Task 5: Integration Tests

Update `pkg/roomkeystore/integration_test.go` to use the new signatures.

**Files:**
- Modify: `pkg/roomkeystore/integration_test.go`

- [ ] **Step 1: Update `TestValkeyStore_Integration_RoundTrip`**

Change:

```go
func TestValkeyStore_Integration_RoundTrip(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	pair := RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey}

	// Set
	err := store.Set(ctx, "room-1", "v1", pair)
	require.NoError(t, err)

	// Get — should return the stored pair with version
	got, err := store.Get(ctx, "room-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "v1", got.VersionID)
	assert.Equal(t, pubKey, got.KeyPair.PublicKey)
	assert.Equal(t, privKey, got.KeyPair.PrivateKey)

	// Delete
	err = store.Delete(ctx, "room-1")
	require.NoError(t, err)

	// Get after delete — should return nil, nil
	got, err = store.Get(ctx, "room-1")
	require.NoError(t, err)
	assert.Nil(t, got)
}
```

to:

```go
func TestValkeyStore_Integration_RoundTrip(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	pair := RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey}

	// Set
	ver, err := store.Set(ctx, "room-1", pair)
	require.NoError(t, err)
	assert.Equal(t, 0, ver)

	// Get — should return the stored pair with version
	got, err := store.Get(ctx, "room-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 0, got.Version)
	assert.Equal(t, pubKey, got.KeyPair.PublicKey)
	assert.Equal(t, privKey, got.KeyPair.PrivateKey)

	// Delete
	err = store.Delete(ctx, "room-1")
	require.NoError(t, err)

	// Get after delete — should return nil, nil
	got, err = store.Get(ctx, "room-1")
	require.NoError(t, err)
	assert.Nil(t, got)
}
```

- [ ] **Step 2: Update `TestValkeyStore_Integration_RotateRoundTrip`**

Change:

```go
func TestValkeyStore_Integration_RotateRoundTrip(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	oldPub := bytes.Repeat([]byte{0xAA}, 65)
	oldPriv := bytes.Repeat([]byte{0xBB}, 32)
	newPub := bytes.Repeat([]byte{0xCC}, 65)
	newPriv := bytes.Repeat([]byte{0xDD}, 32)

	// Set initial key pair.
	err := store.Set(ctx, "room-rot", "v1", RoomKeyPair{PublicKey: oldPub, PrivateKey: oldPriv})
	require.NoError(t, err)

	// Rotate to new key pair.
	err = store.Rotate(ctx, "room-rot", "v2", RoomKeyPair{PublicKey: newPub, PrivateKey: newPriv})
	require.NoError(t, err)

	// Get — should return new key pair as current.
	got, err := store.Get(ctx, "room-rot")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "v2", got.VersionID)
	assert.Equal(t, newPub, got.KeyPair.PublicKey)
	assert.Equal(t, newPriv, got.KeyPair.PrivateKey)

	// GetByVersion with old version — should return old key pair from previous slot.
	oldPair, err := store.GetByVersion(ctx, "room-rot", "v1")
	require.NoError(t, err)
	require.NotNil(t, oldPair)
	assert.Equal(t, oldPub, oldPair.PublicKey)
	assert.Equal(t, oldPriv, oldPair.PrivateKey)

	// GetByVersion with new version — should return new key pair from current slot.
	newPair, err := store.GetByVersion(ctx, "room-rot", "v2")
	require.NoError(t, err)
	require.NotNil(t, newPair)
	assert.Equal(t, newPub, newPair.PublicKey)
	assert.Equal(t, newPriv, newPair.PrivateKey)

	// GetByVersion with unknown version — should return nil, nil.
	unknown, err := store.GetByVersion(ctx, "room-rot", "v-unknown")
	require.NoError(t, err)
	assert.Nil(t, unknown)
}
```

to:

```go
func TestValkeyStore_Integration_RotateRoundTrip(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	oldPub := bytes.Repeat([]byte{0xAA}, 65)
	oldPriv := bytes.Repeat([]byte{0xBB}, 32)
	newPub := bytes.Repeat([]byte{0xCC}, 65)
	newPriv := bytes.Repeat([]byte{0xDD}, 32)

	// Set initial key pair.
	ver, err := store.Set(ctx, "room-rot", RoomKeyPair{PublicKey: oldPub, PrivateKey: oldPriv})
	require.NoError(t, err)
	assert.Equal(t, 0, ver)

	// Rotate to new key pair.
	ver, err = store.Rotate(ctx, "room-rot", RoomKeyPair{PublicKey: newPub, PrivateKey: newPriv})
	require.NoError(t, err)
	assert.Equal(t, 1, ver)

	// Get — should return new key pair as current.
	got, err := store.Get(ctx, "room-rot")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 1, got.Version)
	assert.Equal(t, newPub, got.KeyPair.PublicKey)
	assert.Equal(t, newPriv, got.KeyPair.PrivateKey)

	// GetByVersion with old version — should return old key pair from previous slot.
	oldPair, err := store.GetByVersion(ctx, "room-rot", 0)
	require.NoError(t, err)
	require.NotNil(t, oldPair)
	assert.Equal(t, oldPub, oldPair.PublicKey)
	assert.Equal(t, oldPriv, oldPair.PrivateKey)

	// GetByVersion with new version — should return new key pair from current slot.
	newPair, err := store.GetByVersion(ctx, "room-rot", 1)
	require.NoError(t, err)
	require.NotNil(t, newPair)
	assert.Equal(t, newPub, newPair.PublicKey)
	assert.Equal(t, newPriv, newPair.PrivateKey)

	// GetByVersion with unknown version — should return nil, nil.
	unknown, err := store.GetByVersion(ctx, "room-rot", 999)
	require.NoError(t, err)
	assert.Nil(t, unknown)
}
```

- [ ] **Step 3: Update `TestValkeyStore_Integration_GracePeriodExpiry`**

Change:

```go
func TestValkeyStore_Integration_GracePeriodExpiry(t *testing.T) {
	// Use a 1-second grace period so the test completes quickly.
	store := setupValkey(t, 1*time.Second)
	ctx := context.Background()

	oldPub := bytes.Repeat([]byte{0x01}, 65)
	oldPriv := bytes.Repeat([]byte{0x02}, 32)
	newPub := bytes.Repeat([]byte{0x03}, 65)
	newPriv := bytes.Repeat([]byte{0x04}, 32)

	err := store.Set(ctx, "room-grace", "v1", RoomKeyPair{PublicKey: oldPub, PrivateKey: oldPriv})
	require.NoError(t, err)

	err = store.Rotate(ctx, "room-grace", "v2", RoomKeyPair{PublicKey: newPub, PrivateKey: newPriv})
	require.NoError(t, err)

	// Immediately after rotate, old key should still be retrievable.
	oldPair, err := store.GetByVersion(ctx, "room-grace", "v1")
	require.NoError(t, err)
	require.NotNil(t, oldPair, "old key should be retrievable during grace period")

	// Wait for grace period to elapse. This sleep is intentional — we are
	// waiting for an external Valkey TTL, not synchronising goroutines.
	time.Sleep(2 * time.Second)

	// Old key should now be expired.
	oldPair, err = store.GetByVersion(ctx, "room-grace", "v1")
	require.NoError(t, err)
	assert.Nil(t, oldPair, "old key should be expired after grace period")

	// Current key should still be present (no TTL).
	got, err := store.Get(ctx, "room-grace")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "v2", got.VersionID)
}
```

to:

```go
func TestValkeyStore_Integration_GracePeriodExpiry(t *testing.T) {
	// Use a 1-second grace period so the test completes quickly.
	store := setupValkey(t, 1*time.Second)
	ctx := context.Background()

	oldPub := bytes.Repeat([]byte{0x01}, 65)
	oldPriv := bytes.Repeat([]byte{0x02}, 32)
	newPub := bytes.Repeat([]byte{0x03}, 65)
	newPriv := bytes.Repeat([]byte{0x04}, 32)

	_, err := store.Set(ctx, "room-grace", RoomKeyPair{PublicKey: oldPub, PrivateKey: oldPriv})
	require.NoError(t, err)

	_, err = store.Rotate(ctx, "room-grace", RoomKeyPair{PublicKey: newPub, PrivateKey: newPriv})
	require.NoError(t, err)

	// Immediately after rotate, old key should still be retrievable.
	oldPair, err := store.GetByVersion(ctx, "room-grace", 0)
	require.NoError(t, err)
	require.NotNil(t, oldPair, "old key should be retrievable during grace period")

	// Wait for grace period to elapse. This sleep is intentional — we are
	// waiting for an external Valkey TTL, not synchronising goroutines.
	time.Sleep(2 * time.Second)

	// Old key should now be expired.
	oldPair, err = store.GetByVersion(ctx, "room-grace", 0)
	require.NoError(t, err)
	assert.Nil(t, oldPair, "old key should be expired after grace period")

	// Current key should still be present (no TTL).
	got, err := store.Get(ctx, "room-grace")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 1, got.Version)
}
```

- [ ] **Step 4: Update `TestValkeyStore_Integration_RotateNoCurrentKey`**

Change:

```go
func TestValkeyStore_Integration_RotateNoCurrentKey(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	err := store.Rotate(ctx, "room-empty", "v1", RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0x01}, 65),
		PrivateKey: bytes.Repeat([]byte{0x02}, 32),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoCurrentKey), "should return ErrNoCurrentKey")
}
```

to:

```go
func TestValkeyStore_Integration_RotateNoCurrentKey(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	_, err := store.Rotate(ctx, "room-empty", RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0x01}, 65),
		PrivateKey: bytes.Repeat([]byte{0x02}, 32),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoCurrentKey), "should return ErrNoCurrentKey")
}
```

- [ ] **Step 5: Update `TestValkeyStore_Integration_DeleteBothKeys`**

Change:

```go
func TestValkeyStore_Integration_DeleteBothKeys(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	// Set + Rotate to create both current and previous keys.
	err := store.Set(ctx, "room-del", "v1", RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0xAA}, 65),
		PrivateKey: bytes.Repeat([]byte{0xBB}, 32),
	})
	require.NoError(t, err)

	err = store.Rotate(ctx, "room-del", "v2", RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0xCC}, 65),
		PrivateKey: bytes.Repeat([]byte{0xDD}, 32),
	})
	require.NoError(t, err)

	// Delete should remove both.
	err = store.Delete(ctx, "room-del")
	require.NoError(t, err)

	// Current key should be gone.
	got, err := store.Get(ctx, "room-del")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Previous key should also be gone.
	prev, err := store.GetByVersion(ctx, "room-del", "v1")
	require.NoError(t, err)
	assert.Nil(t, prev)
}
```

to:

```go
func TestValkeyStore_Integration_DeleteBothKeys(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	// Set + Rotate to create both current and previous keys.
	_, err := store.Set(ctx, "room-del", RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0xAA}, 65),
		PrivateKey: bytes.Repeat([]byte{0xBB}, 32),
	})
	require.NoError(t, err)

	_, err = store.Rotate(ctx, "room-del", RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0xCC}, 65),
		PrivateKey: bytes.Repeat([]byte{0xDD}, 32),
	})
	require.NoError(t, err)

	// Delete should remove both.
	err = store.Delete(ctx, "room-del")
	require.NoError(t, err)

	// Current key should be gone.
	got, err := store.Get(ctx, "room-del")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Previous key should also be gone.
	prev, err := store.GetByVersion(ctx, "room-del", 0)
	require.NoError(t, err)
	assert.Nil(t, prev)
}
```

- [ ] **Step 6: Remove unused `fmt` import if needed**

Check the import block. If `fmt` is only used by `setupValkey` (which still uses it), keep it. Verify compilation:

```bash
cd /home/user/chat && go vet -tags integration ./pkg/roomkeystore/
```

- [ ] **Step 7: Commit**

```bash
git add pkg/roomkeystore/integration_test.go
git commit -m "test: update roomkeystore integration tests for integer version"
```

---

### Task 6: RoomKeySender Tests, Integration Tests, and TypeScript Client

Update the roomkeysender package tests and TypeScript test client.

**Files:**
- Modify: `pkg/roomkeysender/roomkeysender_test.go`
- Modify: `pkg/roomkeysender/integration_test.go`
- Modify: `pkg/roomkeysender/testdata/client.ts`

- [ ] **Step 1: Update `TestSender_Send` in `pkg/roomkeysender/roomkeysender_test.go`**

Replace the full test function. Key changes:
- `VersionID: "v-abc-123"` → `Version: 0`
- `VersionID: "v-def-456"` → `Version: 1`
- `VersionID: "v-ghi-789"` → `Version: 2`
- Assertion `tt.evt.VersionID` → `tt.evt.Version`

```go
func TestSender_Send(t *testing.T) {
	pub65 := make([]byte, 65)
	pub65[0] = 0x04
	for i := 1; i < 65; i++ {
		pub65[i] = byte(i)
	}
	priv32 := make([]byte, 32)
	for i := range priv32 {
		priv32[i] = byte(i + 100)
	}

	tests := []struct {
		name       string
		username   string
		evt        model.RoomKeyEvent
		publishErr error
		wantSubj   string
		wantErr    string
	}{
		{
			name:     "valid send",
			username: "alice",
			evt: model.RoomKeyEvent{
				RoomID:     "room-1",
				Version:    0,
				PublicKey:  pub65,
				PrivateKey: priv32,
			},
			wantSubj: "chat.user.alice.event.room.key",
		},
		{
			name:     "different user produces different subject",
			username: "bob",
			evt: model.RoomKeyEvent{
				RoomID:     "room-2",
				Version:    1,
				PublicKey:  []byte{0x04, 0x01},
				PrivateKey: []byte{0x0a},
			},
			wantSubj: "chat.user.bob.event.room.key",
		},
		{
			name:     "publish error is wrapped and returned",
			username: "carol",
			evt: model.RoomKeyEvent{
				RoomID:     "room-3",
				Version:    2,
				PublicKey:  []byte{0x04},
				PrivateKey: []byte{0x01},
			},
			publishErr: errors.New("connection lost"),
			wantErr:    "publish room key event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &mockPublisher{err: tt.publishErr}
			sender := roomkeysender.NewSender(pub)

			err := sender.Send(tt.username, &tt.evt)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.ErrorIs(t, err, tt.publishErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantSubj, pub.subject)

			// Verify payload round-trips correctly.
			var got model.RoomKeyEvent
			require.NoError(t, json.Unmarshal(pub.data, &got))
			assert.Equal(t, tt.evt.RoomID, got.RoomID)
			assert.Equal(t, tt.evt.Version, got.Version)
			assert.Equal(t, tt.evt.PublicKey, got.PublicKey)
			assert.Equal(t, tt.evt.PrivateKey, got.PrivateKey)
		})
	}
}
```

- [ ] **Step 2: Run roomkeysender unit tests**

```bash
make test SERVICE=pkg/roomkeysender
```

Expected: PASS.

- [ ] **Step 3: Update `TestRoomKeySender_TypeScriptClient` in `pkg/roomkeysender/integration_test.go`**

Key changes:
- `versionID := "v-test-001"` → `version := 0`
- `VersionID: versionID` → `Version: version`
- NATS header: `nats.Header{"X-Room-Key-Version": []string{versionID}}` → `nats.Header{"X-Room-Key-Version": []string{strconv.Itoa(version)}}`
- Add `"strconv"` to the import block.

Change lines 186-190:

```go
	versionID := "v-test-001"
	plaintext := "hello from Go integration test"
```

to:

```go
	version := 0
	plaintext := "hello from Go integration test"
```

Change lines 217-222:

```go
	evt := &model.RoomKeyEvent{
		RoomID:     roomID,
		VersionID:  versionID,
		PublicKey:  pubKeyBytes,
		PrivateKey: privKeyBytes,
	}
```

to:

```go
	evt := &model.RoomKeyEvent{
		RoomID:     roomID,
		Version:    version,
		PublicKey:  pubKeyBytes,
		PrivateKey: privKeyBytes,
	}
```

Change line 240:

```go
		Header:  nats.Header{"X-Room-Key-Version": []string{versionID}},
```

to:

```go
		Header:  nats.Header{"X-Room-Key-Version": []string{strconv.Itoa(version)}},
```

- [ ] **Step 4: Update TypeScript client `pkg/roomkeysender/testdata/client.ts`**

Change the `RoomKeyEvent` interface (lines 5-9):

```typescript
interface RoomKeyEvent {
  roomId: string;
  versionId: string;
  publicKey: string;  // base64-encoded 65-byte uncompressed P-256 point
  privateKey: string; // base64-encoded 32-byte scalar
}
```

to:

```typescript
interface RoomKeyEvent {
  roomId: string;
  version: number;
  publicKey: string;  // base64-encoded 65-byte uncompressed P-256 point
  privateKey: string; // base64-encoded 32-byte scalar
}
```

Change the keys map type and key event handler (lines 119, 132):

```typescript
  const keys = new Map<string, { publicKey: string; privateKey: string }>();
```

to:

```typescript
  const keys = new Map<number, { publicKey: string; privateKey: string }>();
```

```typescript
      keys.set(evt.versionId, { publicKey: evt.publicKey, privateKey: evt.privateKey });
```

to:

```typescript
      keys.set(evt.version, { publicKey: evt.publicKey, privateKey: evt.privateKey });
```

Change the message handler (lines 138-148) to handle absent header as unencrypted:

```typescript
  for await (const msg of msgSub) {
    const versionId = msg.headers?.get("X-Room-Key-Version");
    if (!versionId) {
      process.stderr.write("missing X-Room-Key-Version header\n");
      process.exit(1);
    }

    const keyPair = keys.get(versionId);
    if (!keyPair) {
      process.stderr.write(`no key found for version ${versionId}\n`);
      process.exit(1);
    }
```

to:

```typescript
  for await (const msg of msgSub) {
    const versionStr = msg.headers?.get("X-Room-Key-Version");
    if (!versionStr) {
      // No header means the message is not encrypted — print raw and exit.
      process.stdout.write(new TextDecoder().decode(msg.data));
      break;
    }

    const version = parseInt(versionStr, 10);
    const keyPair = keys.get(version);
    if (!keyPair) {
      process.stderr.write(`no key found for version ${version}\n`);
      process.exit(1);
    }
```

- [ ] **Step 5: Commit**

```bash
git add pkg/roomkeysender/roomkeysender_test.go pkg/roomkeysender/integration_test.go pkg/roomkeysender/testdata/client.ts
git commit -m "refactor: update roomkeysender tests and TypeScript client for integer version"
```

---

### Task 7: Final Verification

Run lint and full unit test suite to confirm everything compiles and passes.

- [ ] **Step 1: Run lint**

```bash
make lint
```

Expected: No errors.

- [ ] **Step 2: Run all unit tests**

```bash
make test
```

Expected: All tests PASS.

- [ ] **Step 3: Push to remote**

```bash
git push -u origin claude/room-key-version-integer-XnZi3
```
