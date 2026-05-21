# Valkey Cluster Support — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/superpowers/specs/2026-05-19-valkey-cluster-support-design.md`

**Goal:** Replace single-node Valkey with a cluster-mode deployment across all sites. Covers hash-tagged key names, cluster adapter, `valkeyutil` cluster support, service config migration from `VALKEY_ADDR` to `VALKEY_ADDRS`, docker-compose updates, and the new room key ensure RPC in `room-service`.

**Architecture:** `pkg/roomkeystore` gains a `clusterAdapter` + `NewValkeyClusterStore` parallel to the existing standalone path. `pkg/valkeyutil` gains `ConnectCluster`. All five services switch to `VALKEY_ADDRS` + cluster constructors. A new `NatsHandleEnsureRoomKey` handler in `room-service` lets external connectors get or generate a room key via NATS.

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `pkg/testutil/testimages/testimages.go` | Modify | Add `ValkeyCluster` image constant |
| `pkg/roomkeystore/roomkeystore.go` | Modify | Hash-tag `roomkey` and `roomprevkey` |
| `pkg/roomkeystore/roomkeystore_test.go` | Modify | Update key name assertions |
| `pkg/roomkeystore/adapter.go` | Modify | Add `clusterAdapter`, `ClusterConfig`, `NewValkeyClusterStore` |
| `pkg/roomkeystore/integration_test.go` | Modify | Add `setupValkeyCluster` + 3 cluster tests |
| `pkg/valkeyutil/valkey.go` | Modify | Add `clusterRedisClient`, `ConnectCluster` |
| `pkg/valkeyutil/valkey_test.go` | Modify | Add cluster integration test |
| `pkg/model/room.go` | Modify | Add `RoomKeyEnsureRequest` |
| `pkg/subject/subject.go` | Modify | Add `RoomKeyEnsure` |
| `room-service/main.go` | Modify | `ValkeyAddr` → `ValkeyAddrs`, cluster constructor |
| `room-service/handler.go` | Modify | Add `NatsHandleEnsureRoomKey`, register in `RegisterCRUD` |
| `room-service/handler_test.go` | Modify | TDD tests for `NatsHandleEnsureRoomKey` |
| `room-service/mock_store_test.go` | Regenerate | `make generate SERVICE=room-service` |
| `room-worker/main.go` | Modify | `ValkeyAddr` → `ValkeyAddrs`, cluster constructor |
| `broadcast-worker/main.go` | Modify | Same |
| `history-service/cmd/main.go` | Modify | Same |
| `search-service/main.go` | Modify | `Valkey.Addr` → `Valkey.Addrs`, `ConnectCluster` |
| `room-service/deploy/docker-compose.yml` | Modify | Single node → cluster, `VALKEY_ADDRS` |
| `room-worker/deploy/docker-compose.yml` | Modify | Same |
| `broadcast-worker/deploy/docker-compose.yml` | Modify | Same |
| `history-service/deploy/docker-compose.yml` | Modify | Same |
| `search-service/deploy/docker-compose.yml` | Modify | Same |

---

## Task 1: Pin ValkeyCluster image constant

**Files:**
- Modify: `pkg/testutil/testimages/testimages.go`

- [ ] **Step 1: Add `ValkeyCluster` constant**

```go
// ValkeyCluster is the image for cluster-mode Valkey integration tests.
ValkeyCluster = "bitnami/valkey-cluster:8"
```

- [ ] **Step 2: Verify compile**

```bash
make build SERVICE=pkg/testutil/testimages
```

- [ ] **Step 3: Commit**

```bash
git add pkg/testutil/testimages/testimages.go
git commit -m "chore(testimages): add ValkeyCluster image constant"
```

---

## Task 2: Hash-tag key names in `pkg/roomkeystore`

**Files:**
- Modify: `pkg/roomkeystore/roomkeystore.go`
- Modify: `pkg/roomkeystore/roomkeystore_test.go`

This is the foundational change that makes the Lua rotate script and `deletePipeline` safe in cluster mode. Both keys for the same room must land on the same slot.

- [ ] **Step 1: Update `roomkey` and `roomprevkey` in `roomkeystore.go`**

```go
func roomkey(roomID string) string {
    return "room:{" + roomID + "}:key"
}

func roomprevkey(roomID string) string {
    return "room:{" + roomID + "}:key:prev"
}
```

- [ ] **Step 2: Check `roomkeystore_test.go` for any hardcoded key name assertions and update them**

Search for `room:` string literals in `roomkeystore_test.go`. Update any that assert the raw key format to use the hash-tagged form.

- [ ] **Step 3: Run unit tests and confirm they pass**

```bash
make test SERVICE=pkg/roomkeystore
```

Expected: all existing unit tests pass — the `fakeHashClient` does not validate key name format.

- [ ] **Step 4: Commit**

```bash
git add pkg/roomkeystore/roomkeystore.go pkg/roomkeystore/roomkeystore_test.go
git commit -m "fix(roomkeystore): hash-tag key names for Valkey cluster slot consistency"
```

---

## Task 3: Add `clusterAdapter`, `ClusterConfig`, `NewValkeyClusterStore`

**Files:**
- Modify: `pkg/roomkeystore/adapter.go`

- [ ] **Step 1: Add `ClusterConfig` to `adapter.go`**

```go
// ClusterConfig holds connection config for a Valkey cluster deployment.
// Addrs is a comma-separated list of seed node addresses; go-redis ClusterClient
// discovers all nodes automatically via CLUSTER SLOTS. One address is sufficient
// but listing all masters is more robust.
type ClusterConfig struct {
    Addrs       []string      `env:"VALKEY_ADDRS,required" envSeparator:","`
    Password    string        `env:"VALKEY_PASSWORD" envDefault:""`
    GracePeriod time.Duration `env:"VALKEY_KEY_GRACE_PERIOD,required"`
}
```

- [ ] **Step 2: Add `clusterAdapter` struct and all `hashCommander` methods**

```go
type clusterAdapter struct {
    c *redis.ClusterClient
}

func (a *clusterAdapter) hset(ctx context.Context, key string, pub, priv string) error {
    return a.c.HSet(ctx, key, "pub", pub, "priv", priv, "ver", "0").Err()
}

func (a *clusterAdapter) hsetWithVersion(ctx context.Context, key string, pub, priv string, version int) error {
    return a.c.HSet(ctx, key, "pub", pub, "priv", priv, "ver", strconv.Itoa(version)).Err()
}

func (a *clusterAdapter) hgetall(ctx context.Context, key string) (map[string]string, error) {
    return a.c.HGetAll(ctx, key).Result()
}

func (a *clusterAdapter) hgetallMany(ctx context.Context, keys []string) ([]map[string]string, error) {
    if len(keys) == 0 {
        return nil, nil
    }
    pipe := a.c.Pipeline()
    cmds := make([]*redis.MapStringStringCmd, len(keys))
    for i, k := range keys {
        cmds[i] = pipe.HGetAll(ctx, k)
    }
    if _, err := pipe.Exec(ctx); err != nil {
        return nil, err
    }
    out := make([]map[string]string, len(keys))
    for i, c := range cmds {
        m, err := c.Result()
        if err != nil {
            return nil, err
        }
        out[i] = m
    }
    return out, nil
}

func (a *clusterAdapter) rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv string, gracePeriod time.Duration) (int, error) {
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

func (a *clusterAdapter) deletePipeline(ctx context.Context, currentKey, prevKey string) error {
    return a.c.Del(ctx, currentKey, prevKey).Err()
}

func (a *clusterAdapter) closeClient() error {
    return a.c.Close()
}
```

- [ ] **Step 3: Add `NewValkeyClusterStore` constructor**

```go
// NewValkeyClusterStore creates a valkeyStore backed by a Valkey cluster,
// pings the cluster to verify connectivity, and returns it.
func NewValkeyClusterStore(cfg ClusterConfig) (RoomKeyStore, error) {
    c := redis.NewClusterClient(&redis.ClusterOptions{
        Addrs:    cfg.Addrs,
        Password: cfg.Password,
    })
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := c.Ping(ctx).Err(); err != nil {
        return nil, fmt.Errorf("valkey cluster connect: %w", err)
    }
    return &valkeyStore{
        client:      &clusterAdapter{c: c},
        closer:      c,
        gracePeriod: cfg.GracePeriod,
    }, nil
}
```

- [ ] **Step 4: Run unit tests and confirm they pass**

```bash
make test SERVICE=pkg/roomkeystore
```

Expected: all existing unit tests pass — `clusterAdapter` is not exercised by unit tests (fake is used instead).

- [ ] **Step 5: Lint**

```bash
make lint
```

- [ ] **Step 6: Commit**

```bash
git add pkg/roomkeystore/adapter.go
git commit -m "feat(roomkeystore): add ClusterConfig, clusterAdapter, NewValkeyClusterStore"
```

---

## Task 4: Cluster integration tests for `pkg/roomkeystore`

**Files:**
- Modify: `pkg/roomkeystore/integration_test.go`

- [ ] **Step 1: Add `setupValkeyCluster` helper**

```go
// setupValkeyCluster starts a bitnami/valkey-cluster:8 container (3 masters,
// 0 replicas) and returns a connected RoomKeyStore backed by NewValkeyClusterStore.
func setupValkeyCluster(t *testing.T, gracePeriod time.Duration) RoomKeyStore {
    t.Helper()
    ctx := context.Background()

    container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: testcontainers.ContainerRequest{
            Image:        testimages.ValkeyCluster,
            ExposedPorts: []string{"6379/tcp"},
            Env: map[string]string{
                "VALKEY_CLUSTER_REPLICAS": "0",
                "ALLOW_EMPTY_PASSWORD":    "yes",
            },
            WaitingFor: wait.ForLog("Cluster correctly created"),
        },
        Started: true,
    })
    require.NoError(t, err, "start valkey cluster container")
    t.Cleanup(func() { _ = container.Terminate(ctx) })

    host, err := container.Host(ctx)
    require.NoError(t, err)
    port, err := container.MappedPort(ctx, "6379")
    require.NoError(t, err)

    store, err := NewValkeyClusterStore(ClusterConfig{
        Addrs:       []string{fmt.Sprintf("%s:%s", host, port.Port())},
        GracePeriod: gracePeriod,
    })
    require.NoError(t, err, "create cluster store")
    return store
}
```

- [ ] **Step 2: Add `TestValkeyClusterStore_Integration_RoundTrip`**

```go
func TestValkeyClusterStore_Integration_RoundTrip(t *testing.T) {
    store := setupValkeyCluster(t, time.Hour)
    ctx := context.Background()

    pubKey := bytes.Repeat([]byte{0xAB}, 65)
    privKey := bytes.Repeat([]byte{0xCD}, 32)
    pair := RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey}

    ver, err := store.Set(ctx, "room-1", pair)
    require.NoError(t, err)
    assert.Equal(t, 0, ver)

    got, err := store.Get(ctx, "room-1")
    require.NoError(t, err)
    require.NotNil(t, got)
    assert.Equal(t, 0, got.Version)
    assert.Equal(t, pubKey, got.KeyPair.PublicKey)
    assert.Equal(t, privKey, got.KeyPair.PrivateKey)

    require.NoError(t, store.Delete(ctx, "room-1"))

    got, err = store.Get(ctx, "room-1")
    require.NoError(t, err)
    assert.Nil(t, got)
}
```

- [ ] **Step 3: Add `TestValkeyClusterStore_Integration_RotateRoundTrip`**

```go
func TestValkeyClusterStore_Integration_RotateRoundTrip(t *testing.T) {
    store := setupValkeyCluster(t, time.Hour)
    ctx := context.Background()

    oldPub := bytes.Repeat([]byte{0xAA}, 65)
    oldPriv := bytes.Repeat([]byte{0xBB}, 32)
    newPub := bytes.Repeat([]byte{0xCC}, 65)
    newPriv := bytes.Repeat([]byte{0xDD}, 32)

    ver, err := store.Set(ctx, "room-rot", RoomKeyPair{PublicKey: oldPub, PrivateKey: oldPriv})
    require.NoError(t, err)
    assert.Equal(t, 0, ver)

    ver, err = store.Rotate(ctx, "room-rot", RoomKeyPair{PublicKey: newPub, PrivateKey: newPriv})
    require.NoError(t, err)
    assert.Equal(t, 1, ver)

    got, err := store.Get(ctx, "room-rot")
    require.NoError(t, err)
    require.NotNil(t, got)
    assert.Equal(t, 1, got.Version)
    assert.Equal(t, newPub, got.KeyPair.PublicKey)

    oldPair, err := store.GetByVersion(ctx, "room-rot", 0)
    require.NoError(t, err)
    require.NotNil(t, oldPair)
    assert.Equal(t, oldPub, oldPair.PublicKey)
}
```

- [ ] **Step 4: Add `TestValkeyClusterStore_Integration_HashTagSlotConsistency`**

```go
func TestValkeyClusterStore_Integration_HashTagSlotConsistency(t *testing.T) {
    // Verifies that both Valkey keys for a room hash to the same cluster slot,
    // which is required for the Lua rotate script to execute without CROSSSLOT error.
    store := setupValkeyCluster(t, time.Hour)
    ctx := context.Background()

    // Set a key so both slots are populated, then verify via CLUSTER KEYSLOT.
    _, err := store.Set(ctx, "room-slot-test", RoomKeyPair{
        PublicKey:  bytes.Repeat([]byte{0x01}, 65),
        PrivateKey: bytes.Repeat([]byte{0x02}, 32),
    })
    require.NoError(t, err)

    // Use the underlying ClusterClient to call CLUSTER KEYSLOT directly.
    vs := store.(*valkeyStore)
    ca := vs.client.(*clusterAdapter)
    slot1, err := ca.c.Do(ctx, "CLUSTER", "KEYSLOT", roomkey("room-slot-test")).Int()
    require.NoError(t, err)
    slot2, err := ca.c.Do(ctx, "CLUSTER", "KEYSLOT", roomprevkey("room-slot-test")).Int()
    require.NoError(t, err)

    assert.Equal(t, slot1, slot2, "current and previous key must be on the same cluster slot")
}
```

- [ ] **Step 5: Run cluster integration tests**

```bash
make test-integration SERVICE=pkg/roomkeystore
```

Expected: all three new cluster tests pass alongside the existing standalone tests.

- [ ] **Step 6: Commit**

```bash
git add pkg/roomkeystore/integration_test.go
git commit -m "test(roomkeystore): add cluster integration tests with bitnami/valkey-cluster"
```

---

## Task 5: `pkg/valkeyutil` cluster support

**Files:**
- Modify: `pkg/valkeyutil/valkey.go`
- Modify: `pkg/valkeyutil/valkey_test.go`

- [ ] **Step 1: Add `clusterRedisClient` and `ConnectCluster` to `valkey.go`**

```go
type clusterRedisClient struct {
    c *redis.ClusterClient
}

func (r *clusterRedisClient) Get(ctx context.Context, key string) (string, error) {
    val, err := r.c.Get(ctx, key).Result()
    if errors.Is(err, redis.Nil) {
        return "", ErrCacheMiss
    }
    if err != nil {
        return "", fmt.Errorf("valkey get: %w", err)
    }
    return val, nil
}

func (r *clusterRedisClient) Set(ctx context.Context, key, value string, ttl time.Duration) error {
    return r.c.Set(ctx, key, value, ttl).Err()
}

func (r *clusterRedisClient) Del(ctx context.Context, keys ...string) error {
    return r.c.Del(ctx, keys...).Err()
}

func (r *clusterRedisClient) Close() error {
    return r.c.Close()
}

// ConnectCluster dials a Valkey cluster, verifies connectivity with PING,
// and returns a Client. Seed addresses are discovered via CLUSTER SLOTS so
// a single address is sufficient; listing all masters is more robust.
func ConnectCluster(ctx context.Context, addrs []string, password string) (Client, error) {
    c := redis.NewClusterClient(&redis.ClusterOptions{
        Addrs:    addrs,
        Password: password,
    })
    pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()
    if err := c.Ping(pingCtx).Err(); err != nil {
        if closeErr := c.Close(); closeErr != nil {
            slog.Warn("valkey cluster close after failed connect", "error", closeErr)
        }
        return nil, fmt.Errorf("valkey cluster connect: %w", err)
    }
    slog.Info("connected to Valkey cluster", "addrs", addrs)
    return &clusterRedisClient{c: c}, nil
}
```

- [ ] **Step 2: Run existing unit tests — confirm they pass**

```bash
make test SERVICE=pkg/valkeyutil
```

- [ ] **Step 3: Add `TestConnectCluster_Integration` to `valkey_test.go`**

```go
//go:build integration

func TestConnectCluster_Integration_RoundTrip(t *testing.T) {
    ctx := context.Background()

    container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: testcontainers.ContainerRequest{
            Image:        testimages.ValkeyCluster,
            ExposedPorts: []string{"6379/tcp"},
            Env: map[string]string{
                "VALKEY_CLUSTER_REPLICAS": "0",
                "ALLOW_EMPTY_PASSWORD":    "yes",
            },
            WaitingFor: wait.ForLog("Cluster correctly created"),
        },
        Started: true,
    })
    require.NoError(t, err)
    t.Cleanup(func() { _ = container.Terminate(ctx) })

    host, err := container.Host(ctx)
    require.NoError(t, err)
    port, err := container.MappedPort(ctx, "6379")
    require.NoError(t, err)

    client, err := valkeyutil.ConnectCluster(ctx,
        []string{fmt.Sprintf("%s:%s", host, port.Port())}, "")
    require.NoError(t, err)
    defer valkeyutil.Disconnect(client)

    type payload struct{ V string }
    require.NoError(t, valkeyutil.SetJSONWithTTL(ctx, client, "k1", payload{V: "hello"}, time.Minute))

    var got payload
    require.NoError(t, valkeyutil.GetJSON(ctx, client, "k1", &got))
    assert.Equal(t, "hello", got.V)

    require.NoError(t, client.Del(ctx, "k1"))
    err = valkeyutil.GetJSON(ctx, client, "k1", &got)
    assert.ErrorIs(t, err, valkeyutil.ErrCacheMiss)
}
```

- [ ] **Step 4: Run integration test**

```bash
make test-integration SERVICE=pkg/valkeyutil
```

- [ ] **Step 5: Lint**

```bash
make lint
```

- [ ] **Step 6: Commit**

```bash
git add pkg/valkeyutil/valkey.go pkg/valkeyutil/valkey_test.go
git commit -m "feat(valkeyutil): add clusterRedisClient and ConnectCluster for cluster mode"
```

---

## Task 6: Migrate service configs to `VALKEY_ADDRS`

**Files:**
- Modify: `room-service/main.go`
- Modify: `room-worker/main.go`
- Modify: `broadcast-worker/main.go`
- Modify: `history-service/cmd/main.go`
- Modify: `search-service/main.go`

For each service, three changes:
1. Config field: `ValkeyAddr string` → `ValkeyAddrs []string` with `env:"VALKEY_ADDRS,required" envSeparator:","`
2. Validation: empty check becomes `len(cfg.ValkeyAddrs) == 0`
3. Constructor call: `NewValkeyStore(Config{Addr: ...})` → `NewValkeyClusterStore(ClusterConfig{Addrs: ...})`

- [ ] **Step 1: Update `room-service/main.go`**

```go
// Config change
ValkeyAddrs       []string        `env:"VALKEY_ADDRS,required"         envSeparator:","`

// Constructor change
keyStore, err := roomkeystore.NewValkeyClusterStore(roomkeystore.ClusterConfig{
    Addrs:       cfg.ValkeyAddrs,
    Password:    cfg.ValkeyPassword,
    GracePeriod: cfg.ValkeyGracePeriod,
})
```

- [ ] **Step 2: Update `room-worker/main.go`** — same pattern

- [ ] **Step 3: Update `broadcast-worker/main.go`** — same pattern

- [ ] **Step 4: Update `history-service/cmd/main.go`** — same pattern

- [ ] **Step 5: Update `search-service/main.go`**

```go
// Config change
Addrs    []string `env:"VALKEY_ADDRS,required" envSeparator:","`

// Constructor change
valkey, err := valkeyutil.ConnectCluster(ctx, cfg.Valkey.Addrs, cfg.Valkey.Password)
```

- [ ] **Step 6: Build all affected services to confirm compilation**

```bash
make build SERVICE=room-service
make build SERVICE=room-worker
make build SERVICE=broadcast-worker
make build SERVICE=history-service
make build SERVICE=search-service
```

- [ ] **Step 7: Run unit tests for all affected services**

```bash
make test SERVICE=room-service
make test SERVICE=room-worker
make test SERVICE=broadcast-worker
make test SERVICE=history-service
make test SERVICE=search-service
```

- [ ] **Step 8: Lint**

```bash
make lint
```

- [ ] **Step 9: Commit**

```bash
git add room-service/main.go room-worker/main.go broadcast-worker/main.go \
        history-service/cmd/main.go search-service/main.go
git commit -m "feat: migrate all services from VALKEY_ADDR to VALKEY_ADDRS cluster config"
```

---

## Task 7: Update docker-compose files

**Files:**
- Modify: `room-service/deploy/docker-compose.yml`
- Modify: `room-worker/deploy/docker-compose.yml`
- Modify: `broadcast-worker/deploy/docker-compose.yml`
- Modify: `history-service/deploy/docker-compose.yml`
- Modify: `search-service/deploy/docker-compose.yml`

For each docker-compose file:
1. Replace the single `valkey` service with `bitnami/valkey-cluster:8`
2. Replace `VALKEY_ADDR=valkey:6379` with `VALKEY_ADDRS=valkey-cluster:6379`

- [ ] **Step 1: Update each docker-compose.yml**

Replace:
```yaml
valkey:
  image: valkey/valkey:8
  ports:
    - "6379:6379"
```

With:
```yaml
valkey-cluster:
  image: bitnami/valkey-cluster:8
  environment:
    - VALKEY_CLUSTER_REPLICAS=0
    - ALLOW_EMPTY_PASSWORD=yes
  ports:
    - "6379:6379"
```

And replace:
```yaml
- VALKEY_ADDR=valkey:6379
```

With:
```yaml
- VALKEY_ADDRS=valkey-cluster:6379
```

Update any `depends_on: valkey` references to `depends_on: valkey-cluster`.

- [ ] **Step 2: Commit**

```bash
git add room-service/deploy/docker-compose.yml \
        room-worker/deploy/docker-compose.yml \
        broadcast-worker/deploy/docker-compose.yml \
        history-service/deploy/docker-compose.yml \
        search-service/deploy/docker-compose.yml
git commit -m "chore: replace single-node Valkey with bitnami/valkey-cluster in docker-compose files"
```

---

## Task 8: Room key ensure RPC — model + subject (Red)

**Files:**
- Modify: `pkg/model/room.go`
- Modify: `pkg/subject/subject.go`

- [ ] **Step 1: Add `RoomKeyEnsureRequest` to `pkg/model/room.go`**

```go
// RoomKeyEnsureRequest is the request payload for the room key ensure RPC.
// The RPC returns an existing key if one exists, or generates and stores a
// new one if the room has no key yet.
type RoomKeyEnsureRequest struct {
    RoomID string `json:"roomId" bson:"roomId"`
}
```

- [ ] **Step 2: Add `RoomKeyEnsure` to `pkg/subject/subject.go`**

```go
// RoomKeyEnsure returns the NATS subject for the room key ensure RPC.
// External services use this subject to get or generate a room key.
func RoomKeyEnsure(siteID string) string {
    return fmt.Sprintf("chat.server.request.room.%s.key.ensure", siteID)
}
```

- [ ] **Step 3: Compile check**

```bash
make build SERVICE=pkg/model
make build SERVICE=pkg/subject
```

- [ ] **Step 4: Commit**

```bash
git add pkg/model/room.go pkg/subject/subject.go
git commit -m "feat: add RoomKeyEnsureRequest model and RoomKeyEnsure subject"
```

---

## Task 9: Room key ensure RPC — failing tests (Red)

**Files:**
- Modify: `room-service/handler_test.go`

Write the tests before the implementation so they fail first.

- [ ] **Step 1: Add table-driven tests for `NatsHandleEnsureRoomKey` in `handler_test.go`**

```go
func TestHandler_NatsHandleEnsureRoomKey(t *testing.T) {
    pubKey := bytes.Repeat([]byte{0xAB}, 65)
    privKey := bytes.Repeat([]byte{0xCD}, 32)
    existingPair := &roomkeystore.VersionedKeyPair{
        Version: 2,
        KeyPair: roomkeystore.RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey},
    }

    tests := []struct {
        name        string
        payload     []byte
        setupMock   func(store *MockRoomKeyStore)
        wantErr     bool
        errContains string
        checkReply  func(t *testing.T, reply []byte)
    }{
        {
            name:    "key exists — returns existing RoomKeyEvent, no Set called",
            payload: mustMarshal(model.RoomKeyEnsureRequest{RoomID: "room-1"}),
            setupMock: func(s *MockRoomKeyStore) {
                s.EXPECT().Get(gomock.Any(), "room-1").Return(existingPair, nil)
                // Set must NOT be called
            },
            checkReply: func(t *testing.T, reply []byte) {
                var evt model.RoomKeyEvent
                require.NoError(t, json.Unmarshal(reply, &evt))
                assert.Equal(t, "room-1", evt.RoomID)
                assert.Equal(t, 2, evt.Version)
                assert.Equal(t, pubKey, evt.PublicKey)
                assert.Equal(t, privKey, evt.PrivateKey)
            },
        },
        {
            name:    "key missing — generates and sets new key, returns RoomKeyEvent",
            payload: mustMarshal(model.RoomKeyEnsureRequest{RoomID: "room-2"}),
            setupMock: func(s *MockRoomKeyStore) {
                s.EXPECT().Get(gomock.Any(), "room-2").Return(nil, nil)
                s.EXPECT().Set(gomock.Any(), "room-2", gomock.Any()).Return(0, nil)
            },
            checkReply: func(t *testing.T, reply []byte) {
                var evt model.RoomKeyEvent
                require.NoError(t, json.Unmarshal(reply, &evt))
                assert.Equal(t, "room-2", evt.RoomID)
                assert.Equal(t, 0, evt.Version)
                assert.Len(t, evt.PublicKey, 65)
                assert.Len(t, evt.PrivateKey, 32)
            },
        },
        {
            name:        "Get error — replies with error, no Set called",
            payload:     mustMarshal(model.RoomKeyEnsureRequest{RoomID: "room-3"}),
            setupMock: func(s *MockRoomKeyStore) {
                s.EXPECT().Get(gomock.Any(), "room-3").Return(nil, errors.New("valkey down"))
            },
            wantErr:     true,
            errContains: "get room key",
        },
        {
            name:    "Set error — replies with error",
            payload: mustMarshal(model.RoomKeyEnsureRequest{RoomID: "room-4"}),
            setupMock: func(s *MockRoomKeyStore) {
                s.EXPECT().Get(gomock.Any(), "room-4").Return(nil, nil)
                s.EXPECT().Set(gomock.Any(), "room-4", gomock.Any()).Return(0, errors.New("valkey write failed"))
            },
            wantErr:     true,
            errContains: "store room key",
        },
        {
            name:        "malformed payload — replies with error",
            payload:     []byte(`{bad json`),
            setupMock:   func(s *MockRoomKeyStore) {},
            wantErr:     true,
            errContains: "decode",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            ctrl := gomock.NewController(t)
            defer ctrl.Finish()
            mockStore := NewMockRoomKeyStore(ctrl)
            tt.setupMock(mockStore)

            h := newTestHandler(mockStore) // use existing test helper
            reply, err := h.handleEnsureRoomKey(context.Background(), tt.payload)
            if tt.wantErr {
                require.Error(t, err)
                assert.Contains(t, err.Error(), tt.errContains)
                return
            }
            require.NoError(t, err)
            tt.checkReply(t, reply)
        })
    }
}
```

- [ ] **Step 2: Run tests and confirm they fail (Red)**

```bash
make test SERVICE=room-service
```

Expected: compile error or test failure — `handleEnsureRoomKey` does not exist yet.

---

## Task 10: Room key ensure RPC — implementation (Green)

**Files:**
- Modify: `room-service/handler.go`

- [ ] **Step 1: Add `handleEnsureRoomKey` internal method**

```go
func (h *Handler) handleEnsureRoomKey(ctx context.Context, data []byte) ([]byte, error) {
    var req model.RoomKeyEnsureRequest
    if err := json.Unmarshal(data, &req); err != nil {
        return nil, fmt.Errorf("decode room key ensure request: %w", err)
    }

    pair, err := h.keyStore.Get(ctx, req.RoomID)
    if err != nil {
        return nil, fmt.Errorf("get room key: %w", err)
    }

    if pair == nil {
        newPair, err := roomkeystore.GenerateKeyPair()
        if err != nil {
            return nil, fmt.Errorf("generate room key: %w", err)
        }
        ver, err := h.keyStore.Set(ctx, req.RoomID, newPair)
        if err != nil {
            return nil, fmt.Errorf("store room key: %w", err)
        }
        pair = &roomkeystore.VersionedKeyPair{Version: ver, KeyPair: newPair}
    }

    evt := model.RoomKeyEvent{
        RoomID:     req.RoomID,
        Version:    pair.Version,
        PublicKey:  pair.KeyPair.PublicKey,
        PrivateKey: pair.KeyPair.PrivateKey,
        Timestamp:  time.Now().UTC().UnixMilli(),
    }
    return json.Marshal(evt)
}
```

- [ ] **Step 2: Add exported `NatsHandleEnsureRoomKey` NATS wrapper**

```go
func (h *Handler) NatsHandleEnsureRoomKey(msg *nats.Msg) {
    ctx := natsutil.ContextWithRequestIDFromHeaders(context.Background(), msg.Header)
    reply, err := h.handleEnsureRoomKey(ctx, msg.Data)
    if err != nil {
        natsutil.ReplyError(msg, err)
        return
    }
    if err := msg.Respond(reply); err != nil {
        slog.ErrorContext(ctx, "respond to room key ensure", "error", err)
    }
}
```

- [ ] **Step 3: Register in `RegisterCRUD`**

```go
if _, err := nc.QueueSubscribe(subject.RoomKeyEnsure(h.siteID), queue, h.NatsHandleEnsureRoomKey); err != nil {
    return fmt.Errorf("subscribe room key ensure: %w", err)
}
```

- [ ] **Step 4: Run tests and confirm they pass (Green)**

```bash
make test SERVICE=room-service
```

Expected: all `TestHandler_NatsHandleEnsureRoomKey` subtests pass.

- [ ] **Step 5: Check coverage**

```bash
cd /home/user/chat && go test -race -coverprofile=coverage.out ./room-service/... && go tool cover -func=coverage.out | grep handler
```

Expected: `handler.go` coverage ≥ 80%.

- [ ] **Step 6: Lint**

```bash
make lint
```

- [ ] **Step 7: Commit**

```bash
git add room-service/handler.go room-service/handler_test.go
git commit -m "feat(room-service): add NatsHandleEnsureRoomKey RPC for external connector key access"
```

---

## Task 11: Final verification

- [ ] **Step 1: Run all unit tests**

```bash
make test
```

Expected: all pass.

- [ ] **Step 2: Run integration tests for changed packages**

```bash
make test-integration SERVICE=pkg/roomkeystore
make test-integration SERVICE=pkg/valkeyutil
make test-integration SERVICE=room-service
```

- [ ] **Step 3: Final lint**

```bash
make lint
```

- [ ] **Step 4: Push**

```bash
git push -u origin <branch>
```
