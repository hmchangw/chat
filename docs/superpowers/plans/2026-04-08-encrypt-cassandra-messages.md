# Encrypt Cassandra Message Content — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Encrypt message content at rest in Cassandra using per-room AES-256-GCM symmetric keys stored in Valkey.

**Architecture:** Two new `pkg/` libraries (`msgkeystore` for key management, `msgcrypto` for encrypt/decrypt), integrated into `message-worker` (write path) and `history-service` (read path). Plaintext passthrough for pre-encryption messages.

**Tech Stack:** Go, AES-256-GCM, Valkey (via `redis/go-redis`), `go.uber.org/mock`

**Spec:** `docs/superpowers/specs/2026-04-08-encrypt-cassandra-messages-design.md`

---

## File Structure

### New files
| File | Responsibility |
|------|---------------|
| `pkg/msgkeystore/msgkeystore.go` | `KeyStore` interface, `VersionedKey` type, `valkeyStore` implementation, `hashCommander` internal interface |
| `pkg/msgkeystore/adapter.go` | `redisAdapter` (Valkey client wrapper), `NewValkeyStore` constructor, Lua rotate script |
| `pkg/msgkeystore/msgkeystore_test.go` | Unit tests with `fakeHashClient` |
| `pkg/msgcrypto/msgcrypto.go` | `Encryptor` struct with `Encrypt`/`Decrypt` methods |
| `pkg/msgcrypto/msgcrypto_test.go` | Unit tests with mock `KeyStore` |

### Modified files
| File | Change |
|------|--------|
| `message-worker/handler.go` | Add `ContentEncryptor` interface, inject into `Handler`, encrypt before save |
| `message-worker/store.go` | Add `ContentEncryptor` to `mockgen` directive |
| `message-worker/handler_test.go` | Update tests for encryptor, add encrypt-error test |
| `message-worker/main.go` | Wire `msgkeystore` + `msgcrypto` |
| `history-service/internal/service/service.go` | Add `ContentDecryptor` interface, inject into `HistoryService` |
| `history-service/internal/service/messages.go` | Decrypt `Msg` field after each query |
| `history-service/internal/service/messages_test.go` | Update `newService` helper, add decrypt-error tests |
| `history-service/cmd/main.go` | Wire `msgkeystore` + `msgcrypto` |
| `history-service/internal/config/config.go` | Add Valkey config |

---

### Task 1: `pkg/msgkeystore` — Symmetric Key Store

Mirrors `pkg/roomkeystore` but stores a single 32-byte AES-256 key (not a key pair).
Valkey keys: `room:<roomID>:dbkey` (current), `room:<roomID>:dbkey:prev` (previous with TTL).

**Reference:** Read `pkg/roomkeystore/roomkeystore.go`, `pkg/roomkeystore/adapter.go`, and `pkg/roomkeystore/roomkeystore_test.go` — follow the same patterns exactly.

**Files:**
- Create: `pkg/msgkeystore/msgkeystore.go`
- Create: `pkg/msgkeystore/adapter.go`
- Test: `pkg/msgkeystore/msgkeystore_test.go`

- [ ] **Step 1: Write the test file with fakeHashClient and all tests**

Create `pkg/msgkeystore/msgkeystore_test.go`. The fake stores a single `key` field (not `pub`/`priv`).
Model after `pkg/roomkeystore/roomkeystore_test.go` — same table-driven structure, same error scenarios.

Key differences from roomkeystore tests:
- `fakeHashClient.hset` takes `(ctx, key, val string)` — stores `{"key": val, "ver": "0"}`
- `fakeHashClient.rotatePipeline` takes `(ctx, currentKey, prevKey, val string, grace)` — single value instead of pub+priv
- Test data uses `bytes.Repeat([]byte{0xAB}, 32)` (32-byte AES key) instead of 65+32 byte key pairs
- Assertions check `stored["key"]` instead of `stored["pub"]`/`stored["priv"]`

Tests to include (mirroring roomkeystore):
- `TestValkeyStore_Set` — happy path, hset error
- `TestValkeyStore_Get` — happy path, missing key (nil,nil), hgetall error, corrupted base64, non-numeric version
- `TestValkeyStore_GetByVersion` — matches current, matches previous after rotation, no match (nil,nil), no keys (nil,nil), hgetall error current, hgetall error previous, corrupted current base64, corrupted previous base64
- `TestValkeyStore_Rotate` — happy path (ver 0→1), no current key (ErrNoCurrentKey), replaces existing previous (ver 1→2), pipeline error
- `TestValkeyStore_Delete` — happy path, missing key no-op, deletePipeline error

```go
package msgkeystore

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHashClient struct {
	store             map[string]map[string]string
	hsetErr           error
	hgetallErr        error
	hgetallCallCount  int
	hgetallErrOnCall  int
	rotatePipelineErr error
	deletePipelineErr error
}

func (f *fakeHashClient) hset(_ context.Context, key string, val string) error {
	if f.hsetErr != nil {
		return f.hsetErr
	}
	if f.store == nil {
		f.store = make(map[string]map[string]string)
	}
	f.store[key] = map[string]string{"key": val, "ver": "0"}
	return nil
}

func (f *fakeHashClient) hgetall(_ context.Context, key string) (map[string]string, error) {
	f.hgetallCallCount++
	if f.hgetallErr != nil && (f.hgetallErrOnCall == 0 || f.hgetallCallCount == f.hgetallErrOnCall) {
		return nil, f.hgetallErr
	}
	if f.store == nil {
		return map[string]string{}, nil
	}
	m, ok := f.store[key]
	if !ok {
		return map[string]string{}, nil
	}
	return m, nil
}

func (f *fakeHashClient) rotatePipeline(_ context.Context, currentKey, prevKey string, val string, _ time.Duration) (int, error) {
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
	f.store[prevKey] = map[string]string{"key": cur["key"], "ver": cur["ver"]}
	f.store[currentKey] = map[string]string{"key": val, "ver": strconv.Itoa(newVer)}
	return newVer, nil
}

func (f *fakeHashClient) deletePipeline(_ context.Context, currentKey, prevKey string) error {
	if f.deletePipelineErr != nil {
		return f.deletePipelineErr
	}
	if f.store != nil {
		delete(f.store, currentKey)
		delete(f.store, prevKey)
	}
	return nil
}

func newTestStore(fake *fakeHashClient) *valkeyStore {
	return &valkeyStore{client: fake, gracePeriod: time.Hour}
}
```

Then write all `Test*` functions following `pkg/roomkeystore/roomkeystore_test.go` structure exactly, substituting:
- `RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey}` → `aesKey` (`[]byte`, 32 bytes)
- `got.KeyPair.PublicKey` → `got.Key`
- `stored["pub"]` / `stored["priv"]` → `stored["key"]`
- `roomkey()` → `dbkey()`
- `roomprevkey()` → `dbprevkey()`
- Error strings: `"set room key"` → `"set db key"`, `"get room key"` → `"get db key"`, etc.

- [ ] **Step 2: Run tests — verify they fail (types don't exist yet)**

```bash
make test SERVICE=pkg/msgkeystore
```

Expected: compilation errors — `valkeyStore`, `ErrNoCurrentKey`, `dbkey`, `dbprevkey` undefined.

- [ ] **Step 3: Write `pkg/msgkeystore/msgkeystore.go`**

```go
package msgkeystore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"
)

var ErrNoCurrentKey = errors.New("no current key")

type VersionedKey struct {
	Version int
	Key     []byte // 32 bytes, AES-256
}

type KeyStore interface {
	Set(ctx context.Context, roomID string, key []byte) (int, error)
	Get(ctx context.Context, roomID string) (*VersionedKey, error)
	GetByVersion(ctx context.Context, roomID string, version int) ([]byte, error)
	Rotate(ctx context.Context, roomID string, newKey []byte) (int, error)
	Delete(ctx context.Context, roomID string) error
}

type Config struct {
	Addr        string        `env:"VALKEY_ADDR,required"`
	Password    string        `env:"VALKEY_PASSWORD" envDefault:""`
	GracePeriod time.Duration `env:"VALKEY_DBKEY_GRACE_PERIOD,required"`
}

type hashCommander interface {
	hset(ctx context.Context, key string, val string) error
	hgetall(ctx context.Context, key string) (map[string]string, error)
	rotatePipeline(ctx context.Context, currentKey, prevKey string, val string, gracePeriod time.Duration) (int, error)
	deletePipeline(ctx context.Context, currentKey, prevKey string) error
}

type valkeyStore struct {
	client      hashCommander
	gracePeriod time.Duration
}

func dbkey(roomID string) string {
	return "room:" + roomID + ":dbkey"
}

func dbprevkey(roomID string) string {
	return "room:" + roomID + ":dbkey:prev"
}

func (s *valkeyStore) Set(ctx context.Context, roomID string, key []byte) (int, error) {
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := s.client.hset(ctx, dbkey(roomID), encoded); err != nil {
		return 0, fmt.Errorf("set db key: %w", err)
	}
	return 0, nil
}

func (s *valkeyStore) Get(ctx context.Context, roomID string) (*VersionedKey, error) {
	fields, err := s.client.hgetall(ctx, dbkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get db key: %w", err)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	ver, err := strconv.Atoi(fields["ver"])
	if err != nil {
		return nil, fmt.Errorf("get db key: parse version: %w", err)
	}
	key, err := decodeKey(fields)
	if err != nil {
		return nil, fmt.Errorf("get db key: %w", err)
	}
	return &VersionedKey{Version: ver, Key: key}, nil
}

func (s *valkeyStore) GetByVersion(ctx context.Context, roomID string, version int) ([]byte, error) {
	versionID := strconv.Itoa(version)

	currentFields, err := s.client.hgetall(ctx, dbkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get db key by version: %w", err)
	}
	if len(currentFields) > 0 && currentFields["ver"] == versionID {
		key, err := decodeKey(currentFields)
		if err != nil {
			return nil, fmt.Errorf("get db key by version: %w", err)
		}
		return key, nil
	}

	prevFields, err := s.client.hgetall(ctx, dbprevkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get db key by version: %w", err)
	}
	if len(prevFields) > 0 && prevFields["ver"] == versionID {
		key, err := decodeKey(prevFields)
		if err != nil {
			return nil, fmt.Errorf("get db key by version: %w", err)
		}
		return key, nil
	}

	return nil, nil
}

func (s *valkeyStore) Rotate(ctx context.Context, roomID string, newKey []byte) (int, error) {
	encoded := base64.StdEncoding.EncodeToString(newKey)
	version, err := s.client.rotatePipeline(ctx, dbkey(roomID), dbprevkey(roomID), encoded, s.gracePeriod)
	if err != nil {
		return 0, fmt.Errorf("rotate db key: %w", err)
	}
	return version, nil
}

func (s *valkeyStore) Delete(ctx context.Context, roomID string) error {
	if err := s.client.deletePipeline(ctx, dbkey(roomID), dbprevkey(roomID)); err != nil {
		return fmt.Errorf("delete db key: %w", err)
	}
	return nil
}

func decodeKey(fields map[string]string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(fields["key"])
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	return key, nil
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
make test SERVICE=pkg/msgkeystore
```

Expected: all tests PASS.

- [ ] **Step 5: Write `pkg/msgkeystore/adapter.go`**

Model after `pkg/roomkeystore/adapter.go`. Key differences:
- `hset` stores `"key", val` instead of `"pub", pub, "priv", priv`
- Lua `rotateScript` writes `"key", newVal` instead of `"pub"/"priv"`
- `rotatePipeline` takes `val string` instead of `pub, priv string`

```go
package msgkeystore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type redisAdapter struct {
	c *redis.Client
}

func (a *redisAdapter) hset(ctx context.Context, key string, val string) error {
	return a.c.HSet(ctx, key, "key", val, "ver", "0").Err()
}

func (a *redisAdapter) hgetall(ctx context.Context, key string) (map[string]string, error) {
	return a.c.HGetAll(ctx, key).Result()
}

var rotateScript = redis.NewScript(`
local currentKey = KEYS[1]
local prevKey    = KEYS[2]
local newVal     = ARGV[1]
local graceSec   = tonumber(ARGV[2])

local cur = redis.call('HGETALL', currentKey)
if #cur == 0 then
    return redis.error_reply('no current key')
end

local curVer = tonumber(redis.call('HGET', currentKey, 'ver')) or 0
local newVer = curVer + 1

redis.call('DEL', prevKey)
redis.call('HSET', prevKey, unpack(cur))
redis.call('EXPIRE', prevKey, graceSec)

redis.call('HSET', currentKey, 'key', newVal, 'ver', tostring(newVer))
return newVer
`)

func (a *redisAdapter) rotatePipeline(ctx context.Context, currentKey, prevKey string, val string, gracePeriod time.Duration) (int, error) {
	graceSec := int(gracePeriod.Seconds())
	if graceSec < 1 {
		graceSec = 1
	}
	result, err := rotateScript.Run(ctx, a.c, []string{currentKey, prevKey}, val, graceSec).Int()
	if err != nil && strings.Contains(err.Error(), "no current key") {
		return 0, ErrNoCurrentKey
	}
	return result, err
}

func (a *redisAdapter) deletePipeline(ctx context.Context, currentKey, prevKey string) error {
	return a.c.Del(ctx, currentKey, prevKey).Err()
}

func NewValkeyStore(cfg Config) (KeyStore, error) {
	c := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("valkey connect: %w", err)
	}
	return &valkeyStore{client: &redisAdapter{c: c}, gracePeriod: cfg.GracePeriod}, nil
}
```

- [ ] **Step 6: Run lint and tests**

```bash
make lint && make test SERVICE=pkg/msgkeystore
```

Expected: lint clean, all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/msgkeystore/
git commit -m "feat: add pkg/msgkeystore for DB encryption key management"
```

---

### Task 2: `pkg/msgcrypto` — Encrypt/Decrypt Library

AES-256-GCM encryption using keys from `msgkeystore`. Format: `enc:v<version>:<base64(nonce+ciphertext+tag)>`.

**Reference:** Read `pkg/roomcrypto/roomcrypto.go` and `pkg/roomcrypto/roomcrypto_test.go` for AES-GCM patterns.

**Files:**
- Create: `pkg/msgcrypto/msgcrypto.go`
- Test: `pkg/msgcrypto/msgcrypto_test.go`

- [ ] **Step 1: Write `pkg/msgcrypto/msgcrypto_test.go`**

Use `go.uber.org/mock` to mock `msgkeystore.KeyStore`. Generate mock with:

```bash
# Add directive at top of msgcrypto_test.go:
//go:generate mockgen -destination=mock_keystore_test.go -package=msgcrypto github.com/hmchangw/chat/pkg/msgkeystore KeyStore
```

Tests to write (table-driven where appropriate):

```go
package msgcrypto

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/msgkeystore"
)

// testKey returns a deterministic 32-byte AES key for testing.
func testKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestEncryptor_RoundTrip(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	aesKey := testKey()
	keys.EXPECT().Get(gomock.Any(), "room-1").Return(&msgkeystore.VersionedKey{Version: 0, Key: aesKey}, nil)
	keys.EXPECT().GetByVersion(gomock.Any(), "room-1", 0).Return(aesKey, nil)

	ciphertext, err := enc.Encrypt(context.Background(), "room-1", "hello world")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(ciphertext, "enc:v0:"))

	plaintext, err := enc.Decrypt(context.Background(), "room-1", ciphertext)
	require.NoError(t, err)
	assert.Equal(t, "hello world", plaintext)
}

func TestEncryptor_Decrypt_PlaintextPassthrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)
	// No key store calls expected — plaintext has no "enc:" prefix.

	result, err := enc.Decrypt(context.Background(), "room-1", "just plain text")
	require.NoError(t, err)
	assert.Equal(t, "just plain text", result)
}

func TestEncryptor_Decrypt_EmptyString(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	result, err := enc.Decrypt(context.Background(), "room-1", "")
	require.NoError(t, err)
	assert.Equal(t, "", result)
}

func TestEncryptor_Encrypt_PrefixFormat(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	keys.EXPECT().Get(gomock.Any(), "room-1").Return(&msgkeystore.VersionedKey{Version: 3, Key: testKey()}, nil)

	ct, err := enc.Encrypt(context.Background(), "room-1", "test")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(ct, "enc:v3:"), "expected prefix enc:v3:, got: %s", ct)

	// Verify the payload is valid base64
	payload := ct[len("enc:v3:"):]
	_, err = base64.StdEncoding.DecodeString(payload)
	require.NoError(t, err, "payload should be valid base64")
}

func TestEncryptor_Encrypt_NoKey(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	keys.EXPECT().Get(gomock.Any(), "room-1").Return(nil, nil)

	_, err := enc.Encrypt(context.Background(), "room-1", "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no db encryption key")
}

func TestEncryptor_Encrypt_KeyStoreError(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	keys.EXPECT().Get(gomock.Any(), "room-1").Return(nil, fmt.Errorf("connection refused"))

	_, err := enc.Encrypt(context.Background(), "room-1", "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching db key")
}

func TestEncryptor_Decrypt_KeyVersionNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	// Build a valid-looking ciphertext with version 99
	keys.EXPECT().GetByVersion(gomock.Any(), "room-1", 99).Return(nil, nil)

	_, err := enc.Decrypt(context.Background(), "room-1", "enc:v99:"+base64.StdEncoding.EncodeToString(make([]byte, 28)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestEncryptor_Decrypt_MalformedCiphertext(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	tests := []struct {
		name  string
		input string
	}{
		{"missing version", "enc:hello"},
		{"missing data separator", "enc:v0"},
		{"invalid version", "enc:vABC:data"},
		{"invalid base64", "enc:v0:!!!notbase64!!!"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// GetByVersion may or may not be called depending on where parsing fails.
			keys.EXPECT().GetByVersion(gomock.Any(), gomock.Any(), gomock.Any()).Return(testKey(), nil).AnyTimes()
			_, err := enc.Decrypt(context.Background(), "room-1", tt.input)
			require.Error(t, err)
		})
	}
}

func TestEncryptor_Decrypt_TamperedCiphertext(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	aesKey := testKey()
	keys.EXPECT().Get(gomock.Any(), "room-1").Return(&msgkeystore.VersionedKey{Version: 0, Key: aesKey}, nil)

	ct, err := enc.Encrypt(context.Background(), "room-1", "hello")
	require.NoError(t, err)

	// Tamper with the base64 payload: flip a byte
	parts := strings.SplitN(ct, ":", 3)
	raw, _ := base64.StdEncoding.DecodeString(parts[2])
	raw[len(raw)-1] ^= 0xFF
	tampered := parts[0] + ":" + parts[1] + ":" + base64.StdEncoding.EncodeToString(raw)

	keys.EXPECT().GetByVersion(gomock.Any(), "room-1", 0).Return(aesKey, nil)
	_, err = enc.Decrypt(context.Background(), "room-1", tampered)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decrypting content")
}

func TestEncryptor_Decrypt_TruncatedCiphertext(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	// Payload too short for nonce (< 12 bytes)
	short := base64.StdEncoding.EncodeToString([]byte("tiny"))
	keys.EXPECT().GetByVersion(gomock.Any(), "room-1", 0).Return(testKey(), nil)

	_, err := enc.Decrypt(context.Background(), "room-1", "enc:v0:"+short)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ciphertext too short")
}

func TestEncryptor_RoundTrip_EmptyContent(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	aesKey := testKey()
	keys.EXPECT().Get(gomock.Any(), "room-1").Return(&msgkeystore.VersionedKey{Version: 0, Key: aesKey}, nil)
	keys.EXPECT().GetByVersion(gomock.Any(), "room-1", 0).Return(aesKey, nil)

	ct, err := enc.Encrypt(context.Background(), "room-1", "")
	require.NoError(t, err)

	pt, err := enc.Decrypt(context.Background(), "room-1", ct)
	require.NoError(t, err)
	assert.Equal(t, "", pt)
}

func TestEncryptor_RoundTrip_UnicodeContent(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	aesKey := testKey()
	content := "Hello, \U0001f30d! \u4f60\u597d"
	keys.EXPECT().Get(gomock.Any(), "room-1").Return(&msgkeystore.VersionedKey{Version: 0, Key: aesKey}, nil)
	keys.EXPECT().GetByVersion(gomock.Any(), "room-1", 0).Return(aesKey, nil)

	ct, err := enc.Encrypt(context.Background(), "room-1", content)
	require.NoError(t, err)

	pt, err := enc.Decrypt(context.Background(), "room-1", ct)
	require.NoError(t, err)
	assert.Equal(t, content, pt)
}

func TestEncryptor_Encrypt_UniqueNonces(t *testing.T) {
	ctrl := gomock.NewController(t)
	keys := NewMockKeyStore(ctrl)
	enc := NewEncryptor(keys)

	aesKey := testKey()
	keys.EXPECT().Get(gomock.Any(), "room-1").Return(&msgkeystore.VersionedKey{Version: 0, Key: aesKey}, nil).Times(2)

	ct1, _ := enc.Encrypt(context.Background(), "room-1", "same content")
	ct2, _ := enc.Encrypt(context.Background(), "room-1", "same content")
	assert.NotEqual(t, ct1, ct2, "two encryptions of the same content should produce different ciphertexts")
}
```

- [ ] **Step 2: Generate mock and run tests — verify they fail**

```bash
make generate SERVICE=pkg/msgcrypto
make test SERVICE=pkg/msgcrypto
```

Expected: mock generates, but tests fail — `Encryptor`, `NewEncryptor` undefined.

- [ ] **Step 3: Write `pkg/msgcrypto/msgcrypto.go`**

```go
package msgcrypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/hmchangw/chat/pkg/msgkeystore"
)

//go:generate mockgen -destination=mock_keystore_test.go -package=msgcrypto github.com/hmchangw/chat/pkg/msgkeystore KeyStore

const encPrefix = "enc:"

// Encryptor encrypts and decrypts message content using per-room AES-256-GCM keys.
type Encryptor struct {
	keys       msgkeystore.KeyStore
	randReader io.Reader
}

// NewEncryptor creates an Encryptor backed by the given key store.
func NewEncryptor(keys msgkeystore.KeyStore) *Encryptor {
	return &Encryptor{keys: keys, randReader: rand.Reader}
}

// Encrypt encrypts content using the current DB key for roomID.
// Returns format: enc:v<version>:<base64(nonce + ciphertext + tag)>
func (e *Encryptor) Encrypt(ctx context.Context, roomID, content string) (string, error) {
	vk, err := e.keys.Get(ctx, roomID)
	if err != nil {
		return "", fmt.Errorf("fetching db key: %w", err)
	}
	if vk == nil {
		return "", fmt.Errorf("no db encryption key for room %s", roomID)
	}

	block, err := aes.NewCipher(vk.Key)
	if err != nil {
		return "", fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating GCM wrapper: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(e.randReader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}

	// Seal appends ciphertext+tag to nonce, giving: nonce + ciphertext + tag
	sealed := gcm.Seal(nonce, nonce, []byte(content), nil)
	encoded := base64.StdEncoding.EncodeToString(sealed)
	return fmt.Sprintf("%sv%d:%s", encPrefix, vk.Version, encoded), nil
}

// Decrypt decrypts content encrypted by Encrypt. If content lacks the "enc:" prefix,
// it is returned as-is (plaintext passthrough for pre-encryption messages).
func (e *Encryptor) Decrypt(ctx context.Context, roomID, content string) (string, error) {
	if !strings.HasPrefix(content, encPrefix) {
		return content, nil
	}

	rest := content[len(encPrefix):]
	if len(rest) == 0 || rest[0] != 'v' {
		return "", fmt.Errorf("malformed encrypted content: missing version")
	}
	colonIdx := strings.Index(rest, ":")
	if colonIdx < 0 {
		return "", fmt.Errorf("malformed encrypted content: missing data separator")
	}
	version, err := strconv.Atoi(rest[1:colonIdx])
	if err != nil {
		return "", fmt.Errorf("malformed encrypted content: invalid version: %w", err)
	}
	data := rest[colonIdx+1:]

	key, err := e.keys.GetByVersion(ctx, roomID, version)
	if err != nil {
		return "", fmt.Errorf("fetching db key version %d: %w", version, err)
	}
	if key == nil {
		return "", fmt.Errorf("db encryption key version %d not found for room %s", version, roomID)
	}

	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("decoding ciphertext: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating GCM wrapper: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypting content: %w", err)
	}

	return string(plaintext), nil
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
make test SERVICE=pkg/msgcrypto
```

Expected: all tests PASS.

- [ ] **Step 5: Run lint**

```bash
make lint
```

Expected: lint clean.

- [ ] **Step 6: Commit**

```bash
git add pkg/msgcrypto/
git commit -m "feat: add pkg/msgcrypto for AES-256-GCM message content encryption"
```

---

### Task 3: `message-worker` — Encrypt on Write Path

Add a `ContentEncryptor` interface to the handler, call `Encrypt` before `SaveMessage`.

**Reference:** Read existing `message-worker/handler.go`, `handler_test.go`, `store.go`, `main.go`.

**Files:**
- Modify: `message-worker/store.go` — add `ContentEncryptor` interface and update mockgen directive
- Modify: `message-worker/handler.go` — inject encryptor, encrypt before save
- Modify: `message-worker/handler_test.go` — update tests, add encrypt-error test
- Modify: `message-worker/main.go` — wire `msgkeystore` + `msgcrypto`

- [ ] **Step 1: Update `message-worker/store.go` — add `ContentEncryptor` interface**

```go
//go:generate mockgen -destination=mock_store_test.go -package=main . Store,ContentEncryptor

// Store defines persistence operations for the message worker.
type Store interface {
	SaveMessage(ctx context.Context, msg model.Message) error
}

// ContentEncryptor encrypts message content before persistence.
type ContentEncryptor interface {
	Encrypt(ctx context.Context, roomID, content string) (string, error)
}
```

- [ ] **Step 2: Regenerate mocks**

```bash
make generate SERVICE=message-worker
```

This creates `mock_store_test.go` with both `MockStore` and `MockContentEncryptor`.

- [ ] **Step 3: Write new test and update existing tests in `handler_test.go`**

Update `Handler` construction in all tests to include the encryptor. Add one new test.

Changes to existing tests:

**`TestHandler_ProcessMessage_Success`:** Add encryptor mock that returns encrypted content. Verify `SaveMessage` receives the encrypted content.

```go
func TestHandler_ProcessMessage_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	enc := NewMockContentEncryptor(ctrl)

	msg := model.Message{
		ID: "msg-1", RoomID: "r1", UserID: "u1", UserAccount: "alice", Content: "hello",
	}
	evt := model.MessageEvent{Message: msg, SiteID: "site-a"}

	enc.EXPECT().Encrypt(gomock.Any(), "r1", "hello").Return("enc:v0:cipher", nil)

	expectedMsg := msg
	expectedMsg.Content = "enc:v0:cipher"
	store.EXPECT().SaveMessage(gomock.Any(), expectedMsg).Return(nil)

	h := NewHandler(store, enc)
	data, _ := json.Marshal(evt)
	require.NoError(t, h.processMessage(context.Background(), data))
}
```

**`TestHandler_ProcessMessage_SaveError`:** Same pattern — enc returns success, store returns error.

```go
func TestHandler_ProcessMessage_SaveError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	enc := NewMockContentEncryptor(ctrl)

	msg := model.Message{
		ID: "msg-1", RoomID: "r1", UserID: "u1", UserAccount: "alice", Content: "hello",
	}
	evt := model.MessageEvent{Message: msg, SiteID: "site-a"}

	enc.EXPECT().Encrypt(gomock.Any(), "r1", "hello").Return("enc:v0:cipher", nil)
	store.EXPECT().SaveMessage(gomock.Any(), gomock.Any()).Return(fmt.Errorf("cassandra unavailable"))

	h := NewHandler(store, enc)
	data, _ := json.Marshal(evt)
	err := h.processMessage(context.Background(), data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save message")
}
```

**`TestHandler_ProcessMessage_MalformedJSON`:** No change needed (fails before encrypt).

```go
func TestHandler_ProcessMessage_MalformedJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	enc := NewMockContentEncryptor(ctrl)

	h := NewHandler(store, enc)
	err := h.processMessage(context.Background(), []byte("{invalid"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal message event")
}
```

**New test — `TestHandler_ProcessMessage_EncryptError`:**

```go
func TestHandler_ProcessMessage_EncryptError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	enc := NewMockContentEncryptor(ctrl)

	msg := model.Message{
		ID: "msg-1", RoomID: "r1", UserID: "u1", UserAccount: "alice", Content: "hello",
	}
	evt := model.MessageEvent{Message: msg, SiteID: "site-a"}

	enc.EXPECT().Encrypt(gomock.Any(), "r1", "hello").Return("", fmt.Errorf("key not found"))

	h := NewHandler(store, enc)
	data, _ := json.Marshal(evt)
	err := h.processMessage(context.Background(), data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encrypt message content")
}
```

- [ ] **Step 4: Run tests — verify they fail (Handler constructor mismatch)**

```bash
make test SERVICE=message-worker
```

Expected: compilation error — `NewHandler` takes 1 arg, called with 2.

- [ ] **Step 5: Update `message-worker/handler.go`**

```go
type Handler struct {
	store     Store
	encryptor ContentEncryptor
}

func NewHandler(store Store, encryptor ContentEncryptor) *Handler {
	return &Handler{store: store, encryptor: encryptor}
}
```

Update `processMessage` to encrypt before save:

```go
func (h *Handler) processMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	encrypted, err := h.encryptor.Encrypt(ctx, evt.Message.RoomID, evt.Message.Content)
	if err != nil {
		return fmt.Errorf("encrypt message content: %w", err)
	}
	evt.Message.Content = encrypted

	if err := h.store.SaveMessage(ctx, evt.Message); err != nil {
		return fmt.Errorf("save message: %w", err)
	}

	return nil
}
```

- [ ] **Step 6: Update `message-worker/main.go` — wire dependencies**

Add imports:
```go
"github.com/hmchangw/chat/pkg/msgcrypto"
"github.com/hmchangw/chat/pkg/msgkeystore"
```

Add Valkey config fields to `config`:
```go
type config struct {
	NatsURL           string `env:"NATS_URL,required"`
	SiteID            string `env:"SITE_ID,required"`
	CassandraHosts    string `env:"CASSANDRA_HOSTS"    envDefault:"localhost"`
	CassandraKeyspace string `env:"CASSANDRA_KEYSPACE" envDefault:"chat"`
	MaxWorkers        int    `env:"MAX_WORKERS"        envDefault:"100"`
	ValkeyAddr        string `env:"VALKEY_ADDR,required"`
	ValkeyPassword    string `env:"VALKEY_PASSWORD"    envDefault:""`
	ValkeyGracePeriod string `env:"VALKEY_DBKEY_GRACE_PERIOD" envDefault:"720h"`
}
```

After Cassandra connect, add:

```go
dbKeyStore, err := msgkeystore.NewValkeyStore(msgkeystore.Config{
	Addr:     cfg.ValkeyAddr,
	Password: cfg.ValkeyPassword,
	GracePeriod: func() time.Duration {
		d, _ := time.ParseDuration(cfg.ValkeyGracePeriod)
		return d
	}(),
})
if err != nil {
	slog.Error("valkey connect failed", "error", err)
	os.Exit(1)
}
encryptor := msgcrypto.NewEncryptor(dbKeyStore)

store := NewCassandraStore(cassSession)
handler := NewHandler(store, encryptor)
```

Add Valkey to shutdown (before Cassandra close):
No additional shutdown needed — Redis client doesn't require explicit close for the scope.

- [ ] **Step 7: Run tests and lint**

```bash
make test SERVICE=message-worker && make lint
```

Expected: all tests PASS, lint clean.

- [ ] **Step 8: Commit**

```bash
git add message-worker/
git commit -m "feat: encrypt message content before Cassandra persistence in message-worker"
```

---

### Task 4: `history-service` — Decrypt on Read Path

Add a `ContentDecryptor` interface to the service, decrypt `Msg` field after each query.

**Reference:** Read existing `history-service/internal/service/service.go`, `messages.go`, `messages_test.go`, `cmd/main.go`, `internal/config/config.go`.

**Files:**
- Modify: `history-service/internal/service/service.go` — add `ContentDecryptor` interface, inject into `HistoryService`
- Modify: `history-service/internal/service/messages.go` — decrypt after each query
- Modify: `history-service/internal/service/messages_test.go` — update `newService`, add decrypt-error tests
- Modify: `history-service/cmd/main.go` — wire `msgkeystore` + `msgcrypto`
- Modify: `history-service/internal/config/config.go` — add Valkey config

- [ ] **Step 1: Update `history-service/internal/service/service.go`**

Add `ContentDecryptor` interface and update the `HistoryService` struct and constructor. Update the mockgen directive to include it.

```go
//go:generate mockgen -destination=mocks/mock_repository.go -package=mocks . MessageRepository,SubscriptionRepository,ContentDecryptor

// ContentDecryptor decrypts message content after reading from storage.
type ContentDecryptor interface {
	Decrypt(ctx context.Context, roomID, ciphertext string) (string, error)
}

type HistoryService struct {
	messages      MessageRepository
	subscriptions SubscriptionRepository
	decryptor     ContentDecryptor
}

func New(msgs MessageRepository, subs SubscriptionRepository, dec ContentDecryptor) *HistoryService {
	return &HistoryService{messages: msgs, subscriptions: subs, decryptor: dec}
}
```

- [ ] **Step 2: Regenerate mocks**

```bash
make generate SERVICE=history-service
```

This creates `mocks/mock_repository.go` with `MockContentDecryptor`.

- [ ] **Step 3: Update `messages_test.go` — update `newService` helper**

Change `newService` to return 4 values and set up a default passthrough decryptor:

```go
func newService(t *testing.T) (*service.HistoryService, *mocks.MockMessageRepository, *mocks.MockSubscriptionRepository, *mocks.MockContentDecryptor) {
	ctrl := gomock.NewController(t)
	msgs := mocks.NewMockMessageRepository(ctrl)
	subs := mocks.NewMockSubscriptionRepository(ctrl)
	dec := mocks.NewMockContentDecryptor(ctrl)
	// Default passthrough — existing tests don't need individual decrypt expectations.
	dec.EXPECT().Decrypt(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _, content string) (string, error) { return content, nil },
	).AnyTimes()
	return service.New(msgs, subs, dec), msgs, subs, dec
}
```

Update ALL existing test functions that call `newService(t)` to capture the 4th return value with `_`:

```go
// Before:
svc, msgs, subs := newService(t)
// After:
svc, msgs, subs, _ := newService(t)
```

This applies to every `Test*` function in the file. No other changes needed to existing tests — the passthrough mock handles decrypt transparently.

- [ ] **Step 4: Add new decrypt-error tests**

Add tests verifying that decryption errors propagate correctly:

```go
func TestHistoryService_LoadHistory_DecryptError(t *testing.T) {
	ctrl := gomock.NewController(t)
	msgs := mocks.NewMockMessageRepository(ctrl)
	subs := mocks.NewMockSubscriptionRepository(ctrl)
	dec := mocks.NewMockContentDecryptor(ctrl)
	svc := service.New(msgs, subs, dec)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

	messages := []models.Message{
		{MessageID: "m1", RoomID: "r1", CreatedAt: time.Now(), Msg: "enc:v0:data"},
	}
	msgs.EXPECT().GetMessagesBefore(gomock.Any(), "r1", gomock.Any(), gomock.Any()).Return(makePage(messages, false), nil)
	dec.EXPECT().Decrypt(gomock.Any(), "r1", "enc:v0:data").Return("", fmt.Errorf("key expired"))

	_, err := svc.LoadHistory(c, models.LoadHistoryRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decrypting message")
}

func TestHistoryService_GetMessageByID_DecryptError(t *testing.T) {
	ctrl := gomock.NewController(t)
	msgs := mocks.NewMockMessageRepository(ctrl)
	subs := mocks.NewMockSubscriptionRepository(ctrl)
	dec := mocks.NewMockContentDecryptor(ctrl)
	svc := service.New(msgs, subs, dec)
	c := testContext()

	createdAt := joinTime.Add(1 * time.Minute)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(&joinTime, true, nil)
	msg := &models.Message{MessageID: "m1", RoomID: "r1", CreatedAt: createdAt, Msg: "enc:v0:data"}
	msgs.EXPECT().GetMessage(gomock.Any(), "r1", createdAt, "m1").Return(msg, nil)
	dec.EXPECT().Decrypt(gomock.Any(), "r1", "enc:v0:data").Return("", fmt.Errorf("key expired"))

	_, err := svc.GetMessageByID(c, models.GetMessageByIDRequest{MessageID: "m1", CreatedAt: millis(createdAt)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decrypting message")
}
```

- [ ] **Step 5: Run tests — verify they fail (constructor mismatch)**

```bash
make test SERVICE=history-service
```

Expected: compilation errors — `service.New` takes 2 args, called with 3.

- [ ] **Step 6: Update `messages.go` — add decrypt calls**

Add a helper method to `HistoryService`:

```go
func (s *HistoryService) decryptMessages(ctx context.Context, messages []models.Message) error {
	for i := range messages {
		plaintext, err := s.decryptor.Decrypt(ctx, messages[i].RoomID, messages[i].Msg)
		if err != nil {
			return fmt.Errorf("decrypting message %s: %w", messages[i].MessageID, err)
		}
		messages[i].Msg = plaintext
	}
	return nil
}
```

Then add decrypt calls to each handler:

**`LoadHistory`** — after the query, before return:
```go
	if err := s.decryptMessages(c, page.Data); err != nil {
		return nil, err
	}
	return &models.LoadHistoryResponse{Messages: page.Data}, nil
```

**`LoadNextMessages`** — after the query, before return:
```go
	if err := s.decryptMessages(c, page.Data); err != nil {
		return nil, err
	}
	return &models.LoadNextMessagesResponse{...}, nil
```

**`LoadSurroundingMessages`** — after assembling the `messages` slice, before return:
```go
	if err := s.decryptMessages(c, messages); err != nil {
		return nil, err
	}
	return &models.LoadSurroundingMessagesResponse{Messages: messages, ...}, nil
```

**`GetMessageByID`** — after access check, before return:
```go
	plaintext, err := s.decryptor.Decrypt(c, msg.RoomID, msg.Msg)
	if err != nil {
		return nil, fmt.Errorf("decrypting message %s: %w", msg.MessageID, err)
	}
	msg.Msg = plaintext
	return msg, nil
```

- [ ] **Step 7: Update `history-service/internal/config/config.go` — add Valkey config**

```go
type ValkeyConfig struct {
	Addr        string `env:"ADDR"         required:"true"`
	Password    string `env:"PASSWORD"     envDefault:""`
	GracePeriod string `env:"DBKEY_GRACE_PERIOD" envDefault:"720h"`
}

type Config struct {
	SiteID    string          `env:"SITE_ID" envDefault:"site-local"`
	Cassandra CassandraConfig `envPrefix:"CASSANDRA_"`
	Mongo     MongoConfig     `envPrefix:"MONGO_"`
	NATS      NATSConfig      `envPrefix:"NATS_"`
	Valkey    ValkeyConfig    `envPrefix:"VALKEY_"`
}
```

- [ ] **Step 8: Update `history-service/cmd/main.go` — wire dependencies**

Add imports:
```go
"github.com/hmchangw/chat/pkg/msgcrypto"
"github.com/hmchangw/chat/pkg/msgkeystore"
```

After MongoDB connect, before `service.New`:
```go
gracePeriod, err := time.ParseDuration(cfg.Valkey.GracePeriod)
if err != nil {
	slog.Error("invalid grace period", "error", err)
	os.Exit(1)
}
dbKeyStore, err := msgkeystore.NewValkeyStore(msgkeystore.Config{
	Addr:        cfg.Valkey.Addr,
	Password:    cfg.Valkey.Password,
	GracePeriod: gracePeriod,
})
if err != nil {
	slog.Error("valkey connect failed", "error", err)
	os.Exit(1)
}
encryptor := msgcrypto.NewEncryptor(dbKeyStore)
```

Update service constructor:
```go
svc := service.New(cassRepo, mongoRepo, encryptor)
```

- [ ] **Step 9: Run tests and lint**

```bash
make test SERVICE=history-service && make lint
```

Expected: all tests PASS, lint clean.

- [ ] **Step 10: Commit**

```bash
git add history-service/
git commit -m "feat: decrypt message content on read in history-service"
```

---

### Task 5: Final verification and push

- [ ] **Step 1: Run full test suite**

```bash
make test
```

Expected: all unit tests across all services PASS.

- [ ] **Step 2: Run full lint**

```bash
make lint
```

Expected: lint clean.

- [ ] **Step 3: Push**

```bash
git push -u origin claude/encrypt-cassandra-messages-RCNdg
```
