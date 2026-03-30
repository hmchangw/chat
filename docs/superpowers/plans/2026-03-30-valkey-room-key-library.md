# Valkey Room Key Library Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `pkg/roomkeystore` — a shared library that stores and retrieves P-256 room encryption key pairs in Valkey using a hash-per-room layout with configurable TTL.

**Architecture:** A single package with a `RoomKeyStore` interface, a `valkeyStore` implementation backed by `go-redis/v9`, and an internal `hashCommander` interface that enables unit testing without a real Valkey connection. The `redisAdapter` bridges `*redis.Client` to `hashCommander`. Integration tests use `testcontainers-go` with a `valkey/valkey:8` generic container.

**Tech Stack:** Go 1.24, `github.com/redis/go-redis/v9`, `testcontainers-go` (generic container), `stretchr/testify`, `caarlos0/env`.

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `pkg/roomkeystore/roomkeystore.go` | Create | Types, interface, constructor, `redisAdapter`, `hashCommander`, all three methods |
| `pkg/roomkeystore/roomkeystore_test.go` | Create | Unit tests + `fakeHashClient` test double |
| `pkg/roomkeystore/integration_test.go` | Create | Integration tests with Valkey testcontainer |
| `go.mod` / `go.sum` | Modify | Add `github.com/redis/go-redis/v9` |

---

## Task 1: Add go-redis/v9 dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the dependency**

```bash
cd /home/user/chat && go get github.com/redis/go-redis/v9
```

Expected: `go.mod` and `go.sum` updated with `github.com/redis/go-redis/v9`.

- [ ] **Step 2: Verify module graph is clean**

```bash
cd /home/user/chat && go mod tidy
```

Expected: no errors, `go.sum` consistent.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add go-redis/v9 dependency for Valkey client"
```

---

## Task 2: Scaffold package and write failing Set unit tests

**Files:**
- Create: `pkg/roomkeystore/roomkeystore.go`
- Create: `pkg/roomkeystore/roomkeystore_test.go`

- [ ] **Step 1: Create `pkg/roomkeystore/roomkeystore.go` with types, stubs, and internal interface**

```go
package roomkeystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RoomKeyPair holds the raw P-256 key bytes for a room.
type RoomKeyPair struct {
	PublicKey  []byte // 65-byte uncompressed point
	PrivateKey []byte // 32-byte scalar
}

// RoomKeyStore defines storage operations for room encryption key pairs.
type RoomKeyStore interface {
	Set(ctx context.Context, roomID string, pair RoomKeyPair) error
	Get(ctx context.Context, roomID string) (*RoomKeyPair, error)
	Delete(ctx context.Context, roomID string) error
}

// Config holds Valkey connection and TTL configuration, parsed via caarlos0/env.
type Config struct {
	Addr     string        `env:"VALKEY_ADDR,required"`
	Password string        `env:"VALKEY_PASSWORD"`
	KeyTTL   time.Duration `env:"VALKEY_KEY_TTL,required"`
}

// hashCommander is a minimal internal interface over the Valkey hash commands used by valkeyStore.
// Unexported and command-specific so unit tests can inject a fake without a live Valkey connection.
type hashCommander interface {
	hset(ctx context.Context, key string, pub, priv string) error
	expire(ctx context.Context, key string, ttl time.Duration) error
	hgetall(ctx context.Context, key string) (map[string]string, error)
	del(ctx context.Context, key string) error
}

// valkeyStore is the Valkey-backed implementation of RoomKeyStore.
type valkeyStore struct {
	client hashCommander
	ttl    time.Duration
}

// redisAdapter wraps *redis.Client to satisfy hashCommander.
type redisAdapter struct {
	c *redis.Client
}

func (a *redisAdapter) hset(ctx context.Context, key string, pub, priv string) error {
	return a.c.HSet(ctx, key, "pub", pub, "priv", priv).Err()
}

func (a *redisAdapter) expire(ctx context.Context, key string, ttl time.Duration) error {
	return a.c.Expire(ctx, key, ttl).Err()
}

func (a *redisAdapter) hgetall(ctx context.Context, key string) (map[string]string, error) {
	return a.c.HGetAll(ctx, key).Result()
}

func (a *redisAdapter) del(ctx context.Context, key string) error {
	return a.c.Del(ctx, key).Err()
}

// NewValkeyStore creates a valkeyStore, pings Valkey to verify connectivity, and returns it.
func NewValkeyStore(cfg Config) (*valkeyStore, error) {
	c := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("valkey connect: %w", err)
	}
	return &valkeyStore{client: &redisAdapter{c: c}, ttl: cfg.KeyTTL}, nil
}

// roomkey returns the Valkey hash key for a room's key pair.
func roomkey(roomID string) string {
	return "room:" + roomID + ":key"
}

// Set stores pair in Valkey and (re)sets the TTL on the hash key.
func (s *valkeyStore) Set(_ context.Context, _ string, _ RoomKeyPair) error {
	return errors.New("not implemented")
}

// Get retrieves the key pair for roomID. Returns (nil, nil) if the key does not exist.
func (s *valkeyStore) Get(_ context.Context, _ string) (*RoomKeyPair, error) {
	return nil, errors.New("not implemented")
}

// Delete removes the key pair for roomID. No-op if the key does not exist.
func (s *valkeyStore) Delete(_ context.Context, _ string) error {
	return errors.New("not implemented")
}
```

- [ ] **Step 2: Create `pkg/roomkeystore/roomkeystore_test.go` with fake and Set tests**

```go
package roomkeystore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHashClient is a test double for hashCommander.
// It simulates an in-memory Valkey hash store with injectable per-method errors.
type fakeHashClient struct {
	store      map[string]map[string]string
	hsetErr    error
	expireErr  error
	hgetallErr error
	delErr     error
}

func (f *fakeHashClient) hset(_ context.Context, key string, pub, priv string) error {
	if f.hsetErr != nil {
		return f.hsetErr
	}
	if f.store == nil {
		f.store = make(map[string]map[string]string)
	}
	f.store[key] = map[string]string{"pub": pub, "priv": priv}
	return nil
}

func (f *fakeHashClient) expire(_ context.Context, _ string, _ time.Duration) error {
	return f.expireErr
}

func (f *fakeHashClient) hgetall(_ context.Context, key string) (map[string]string, error) {
	if f.hgetallErr != nil {
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

func (f *fakeHashClient) del(_ context.Context, key string) error {
	if f.delErr != nil {
		return f.delErr
	}
	if f.store != nil {
		delete(f.store, key)
	}
	return nil
}

// newTestStore creates a valkeyStore backed by the given fake for unit tests.
func newTestStore(fake *fakeHashClient) *valkeyStore {
	return &valkeyStore{client: fake, ttl: time.Hour}
}

func TestValkeyStore_Set(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	pair := RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey}

	tests := []struct {
		name      string
		fake      *fakeHashClient
		roomID    string
		wantErr   bool
		errContains string
	}{
		{
			name:   "happy path — stores key pair",
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
		{
			name:        "expire error — returns wrapped error",
			fake:        &fakeHashClient{expireErr: errors.New("timeout")},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "set room key ttl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			err := store.Set(context.Background(), tt.roomID, pair)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			// Verify the hash was written under the correct Valkey key.
			stored := tt.fake.store[roomkey(tt.roomID)]
			require.NotNil(t, stored, "hash should exist in fake store")
			assert.NotEmpty(t, stored["pub"], "pub field should be set")
			assert.NotEmpty(t, stored["priv"], "priv field should be set")
		})
	}
}
```

- [ ] **Step 3: Run Set tests and confirm they fail (Red)**

```bash
cd /home/user/chat && make test SERVICE=pkg/roomkeystore
```

Expected: FAIL — `"not implemented"` error from the stub.

---

## Task 3: Implement Set and make tests pass

**Files:**
- Modify: `pkg/roomkeystore/roomkeystore.go`

- [ ] **Step 1: Replace the Set stub with the real implementation**

Replace the `Set` method in `pkg/roomkeystore/roomkeystore.go`:

```go
// Set stores pair in Valkey as a hash and (re)sets the TTL on the hash key.
func (s *valkeyStore) Set(ctx context.Context, roomID string, pair RoomKeyPair) error {
	pub := base64.StdEncoding.EncodeToString(pair.PublicKey)
	priv := base64.StdEncoding.EncodeToString(pair.PrivateKey)
	key := roomkey(roomID)
	if err := s.client.hset(ctx, key, pub, priv); err != nil {
		return fmt.Errorf("set room key: %w", err)
	}
	if err := s.client.expire(ctx, key, s.ttl); err != nil {
		return fmt.Errorf("set room key ttl: %w", err)
	}
	return nil
}
```

Also add `"encoding/base64"` to the import block in `roomkeystore.go`. Remove `"errors"` from imports if Set was the only user (Get and Delete stubs still use it — leave it for now).

- [ ] **Step 2: Run Set tests and confirm they pass (Green)**

```bash
cd /home/user/chat && make test SERVICE=pkg/roomkeystore
```

Expected: PASS for all `TestValkeyStore_Set` subtests.

- [ ] **Step 3: Commit**

```bash
git add pkg/roomkeystore/roomkeystore.go pkg/roomkeystore/roomkeystore_test.go
git commit -m "feat(roomkeystore): implement Set with base64 encoding and TTL"
```

---

## Task 4: Write failing Get unit tests

**Files:**
- Modify: `pkg/roomkeystore/roomkeystore_test.go`

- [ ] **Step 1: Append Get tests to `roomkeystore_test.go`**

Add the following after `TestValkeyStore_Set`:

```go
func TestValkeyStore_Get(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		want        *RoomKeyPair
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path — returns stored key pair",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				store := newTestStore(f)
				err := store.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				if err != nil {
					panic("test setup failed: " + err.Error())
				}
				return f
			}(),
			roomID: "room-1",
			want:   &RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey},
		},
		{
			name:   "missing key — returns nil, nil",
			fake:   &fakeHashClient{},
			roomID: "nonexistent",
			want:   nil,
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
					roomkey("room-1"): {"pub": "!!!notbase64!!!", "priv": "AQID"},
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
					roomkey("room-1"): {"pub": "AQID", "priv": "!!!notbase64!!!"},
				},
			},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "get room key",
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
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.want.PublicKey, got.PublicKey)
			assert.Equal(t, tt.want.PrivateKey, got.PrivateKey)
		})
	}
}
```

- [ ] **Step 2: Run Get tests and confirm they fail (Red)**

```bash
cd /home/user/chat && make test SERVICE=pkg/roomkeystore
```

Expected: `TestValkeyStore_Get` subtests FAIL with `"not implemented"`.

---

## Task 5: Implement Get and make tests pass

**Files:**
- Modify: `pkg/roomkeystore/roomkeystore.go`

- [ ] **Step 1: Replace the Get stub with the real implementation**

Replace the `Get` method in `pkg/roomkeystore/roomkeystore.go`:

```go
// Get retrieves the key pair for roomID.
// Returns (nil, nil) if the key does not exist in Valkey.
func (s *valkeyStore) Get(ctx context.Context, roomID string) (*RoomKeyPair, error) {
	fields, err := s.client.hgetall(ctx, roomkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get room key: %w", err)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	pub, err := base64.StdEncoding.DecodeString(fields["pub"])
	if err != nil {
		return nil, fmt.Errorf("get room key: decode public key: %w", err)
	}
	priv, err := base64.StdEncoding.DecodeString(fields["priv"])
	if err != nil {
		return nil, fmt.Errorf("get room key: decode private key: %w", err)
	}
	return &RoomKeyPair{PublicKey: pub, PrivateKey: priv}, nil
}
```

- [ ] **Step 2: Run Get tests and confirm they pass (Green)**

```bash
cd /home/user/chat && make test SERVICE=pkg/roomkeystore
```

Expected: PASS for all `TestValkeyStore_Set` and `TestValkeyStore_Get` subtests.

- [ ] **Step 3: Commit**

```bash
git add pkg/roomkeystore/roomkeystore.go pkg/roomkeystore/roomkeystore_test.go
git commit -m "feat(roomkeystore): implement Get with base64 decoding and nil-on-miss"
```

---

## Task 6: Write failing Delete unit tests

**Files:**
- Modify: `pkg/roomkeystore/roomkeystore_test.go`

- [ ] **Step 1: Append Delete tests to `roomkeystore_test.go`**

Add the following after `TestValkeyStore_Get`:

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
			name: "happy path — deletes existing key",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				store := newTestStore(f)
				err := store.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				if err != nil {
					panic("test setup failed: " + err.Error())
				}
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
			name:        "del error — returns wrapped error",
			fake:        &fakeHashClient{delErr: errors.New("connection lost")},
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
			// Verify the key is gone from the fake store.
			_, exists := tt.fake.store[roomkey(tt.roomID)]
			assert.False(t, exists, "key should be removed after Delete")
		})
	}
}
```

- [ ] **Step 2: Run Delete tests and confirm they fail (Red)**

```bash
cd /home/user/chat && make test SERVICE=pkg/roomkeystore
```

Expected: `TestValkeyStore_Delete` subtests FAIL with `"not implemented"`.

---

## Task 7: Implement Delete and make tests pass

**Files:**
- Modify: `pkg/roomkeystore/roomkeystore.go`

- [ ] **Step 1: Replace the Delete stub with the real implementation**

Replace the `Delete` method in `pkg/roomkeystore/roomkeystore.go`:

```go
// Delete removes the key pair for roomID from Valkey.
// No-op if the key does not exist.
func (s *valkeyStore) Delete(ctx context.Context, roomID string) error {
	if err := s.client.del(ctx, roomkey(roomID)); err != nil {
		return fmt.Errorf("delete room key: %w", err)
	}
	return nil
}
```

Now that all stubs are replaced, remove the `"errors"` import from `roomkeystore.go` (it was only used by the stubs).

- [ ] **Step 2: Run all unit tests and confirm they pass (Green)**

```bash
cd /home/user/chat && make test SERVICE=pkg/roomkeystore
```

Expected: PASS for all subtests across `TestValkeyStore_Set`, `TestValkeyStore_Get`, and `TestValkeyStore_Delete`.

- [ ] **Step 3: Check coverage**

```bash
cd /home/user/chat && go test -race -coverprofile=coverage.out ./pkg/roomkeystore/... && go tool cover -func=coverage.out | tail -5
```

Expected: `roomkeystore.go` total coverage ≥ 90%.

- [ ] **Step 4: Commit**

```bash
git add pkg/roomkeystore/roomkeystore.go pkg/roomkeystore/roomkeystore_test.go
git commit -m "feat(roomkeystore): implement Delete; all unit tests passing"
```

---

## Task 8: Integration tests with Valkey testcontainer

**Files:**
- Create: `pkg/roomkeystore/integration_test.go`

- [ ] **Step 1: Create `pkg/roomkeystore/integration_test.go`**

```go
//go:build integration

package roomkeystore

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// setupValkey starts a valkey/valkey:8 container and returns a connected valkeyStore.
// The container is terminated via t.Cleanup.
func setupValkey(t *testing.T, ttl time.Duration) *valkeyStore {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "valkey/valkey:8",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForLog("Ready to accept connections"),
		},
		Started: true,
	})
	require.NoError(t, err, "start valkey container")
	t.Cleanup(func() {
		_ = container.Terminate(ctx) // best-effort; ignore cleanup errors
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379")
	require.NoError(t, err)

	store, err := NewValkeyStore(Config{
		Addr:   fmt.Sprintf("%s:%s", host, port.Port()),
		KeyTTL: ttl,
	})
	require.NoError(t, err, "create valkeyStore")
	return store
}

func TestValkeyStore_Integration_RoundTrip(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	pair := RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey}

	// Set
	err := store.Set(ctx, "room-1", pair)
	require.NoError(t, err)

	// Get — should return the stored pair
	got, err := store.Get(ctx, "room-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, pubKey, got.PublicKey)
	assert.Equal(t, privKey, got.PrivateKey)

	// Delete
	err = store.Delete(ctx, "room-1")
	require.NoError(t, err)

	// Get after delete — should return nil, nil
	got, err = store.Get(ctx, "room-1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestValkeyStore_Integration_MissingKey(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	got, err := store.Get(ctx, "nonexistent-room")
	require.NoError(t, err)
	assert.Nil(t, got, "Get on missing key must return nil, nil")
}

func TestValkeyStore_Integration_TTLExpiry(t *testing.T) {
	// Use a 1-second TTL so the test completes quickly.
	store := setupValkey(t, 1*time.Second)
	ctx := context.Background()

	pubKey := bytes.Repeat([]byte{0x01}, 65)
	privKey := bytes.Repeat([]byte{0x02}, 32)

	err := store.Set(ctx, "room-ttl", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
	require.NoError(t, err)

	// Confirm key exists before expiry.
	got, err := store.Get(ctx, "room-ttl")
	require.NoError(t, err)
	require.NotNil(t, got)

	// Wait for TTL to elapse. This sleep is intentional — we are waiting for
	// an external Valkey TTL, not synchronising goroutines.
	time.Sleep(2 * time.Second)

	// Key should now be gone.
	got, err = store.Get(ctx, "room-ttl")
	require.NoError(t, err)
	assert.Nil(t, got, "key should be expired after TTL")
}
```

- [ ] **Step 2: Run integration tests**

```bash
cd /home/user/chat && make test-integration SERVICE=pkg/roomkeystore
```

Expected: PASS for all three integration test functions. If the Valkey image needs pulling, Docker will do so automatically on first run.

- [ ] **Step 3: Run unit tests one final time to confirm nothing broken**

```bash
cd /home/user/chat && make test SERVICE=pkg/roomkeystore
```

Expected: PASS.

- [ ] **Step 4: Lint**

```bash
cd /home/user/chat && make lint
```

Expected: no lint errors. Fix any `goimports` or `errcheck` issues before committing.

- [ ] **Step 5: Commit and push**

```bash
git add pkg/roomkeystore/integration_test.go
git commit -m "feat(roomkeystore): add integration tests with Valkey testcontainer"
git push -u origin claude/valkey-room-key-library-y3GEg
```
