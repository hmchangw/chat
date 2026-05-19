# Valkey Cluster Support — Design Spec

**Date:** 2026-05-19
**Status:** Draft
**Scope:** `pkg/roomkeystore`, `pkg/valkeyutil`, `room-service`, `room-worker`, `broadcast-worker`, `history-service`, `search-service`, per-site `deploy/docker-compose.yml`

---

## Overview

Replace the single-node Valkey instance used by each site with a Valkey cluster (3-node minimum). Every site (`ftest`, `f18-dev`, production, etc.) runs its own independent Valkey cluster. All services on a site point at that site's cluster.

This spec covers the code changes required to make `pkg/roomkeystore` and `pkg/valkeyutil` work against a Valkey cluster, the service-level config changes to switch from a single address to a cluster address list, and the per-site docker-compose changes to run a cluster instead of a single node.

---

## Motivation

Room encryption keys stored in `pkg/roomkeystore` are critical data. If the single Valkey node goes down and restarts without restoring its data, every room on that site loses its key — subsequent `broadcast-worker` encryptions fail and clients cannot decrypt messages until keys are regenerated out of band. A Valkey cluster tolerates individual node failures without data loss, eliminating this operational risk.

The search subscription cache (`search-service` via `pkg/valkeyutil`) is less critical — it is ephemeral and rebuilds on cache miss — but running it on the same cluster keeps the infrastructure uniform across services and sites.

---

## Architecture

Each site runs **one Valkey cluster** shared by all services on that site. The cluster has a minimum of 3 master nodes (the Valkey cluster protocol requires at least 3 masters to elect a new primary after a failure). Replicas per master are a deployment decision; 0 replicas is acceptable for non-production sites.

```
site: ftest
  ├── valkey-cluster (3 masters, 0 replicas)
  │     node-1:6379
  │     node-2:6380
  │     node-3:6381
  ├── room-service      → VALKEY_ADDRS=node-1:6379,node-2:6380,node-3:6381
  ├── room-worker       → VALKEY_ADDRS=node-1:6379,node-2:6380,node-3:6381
  ├── broadcast-worker  → VALKEY_ADDRS=node-1:6379,node-2:6380,node-3:6381
  ├── history-service   → VALKEY_ADDRS=node-1:6379,node-2:6380,node-3:6381
  └── search-service    → VALKEY_ADDRS=node-1:6379,node-2:6380,node-3:6381
```

Sites are fully independent — no cross-site Valkey connection exists or is introduced by this spec.

---

## Change 1: Key Name Hash Tags — `pkg/roomkeystore`

### The Problem

The Lua rotate script in `adapter.go` operates on two keys per room in a single atomic call:

```
room:abc123:key
room:abc123:key:prev
```

In Valkey cluster mode, every key is assigned to one of 16384 slots based on a CRC16 hash of the key name. A Lua script that touches keys on different slots is rejected with `CROSSSLOT`. Without hash tags, `room:abc123:key` and `room:abc123:key:prev` hash to different slots.

The `deletePipeline` also issues a single `DEL` on both keys — same constraint applies.

### The Fix

Add a hash tag `{roomID}` to both key names. Valkey uses only the substring inside `{...}` for slot assignment, so both keys for the same room always land on the same slot regardless of the `roomID` value.

```go
// Current
func roomkey(roomID string) string     { return "room:" + roomID + ":key" }
func roomprevkey(roomID string) string { return "room:" + roomID + ":key:prev" }

// After
func roomkey(roomID string) string     { return "room:{" + roomID + "}:key" }
func roomprevkey(roomID string) string { return "room:{" + roomID + "}:key:prev" }
```

### Migration Impact

This is a **breaking change for any existing Valkey data**. Keys written under the old format (`room:abc123:key`) are not found after this change because the key name is different. Services will behave as if no key exists for that room — `keyStore.Get` returns `(nil, nil)`.

Rollout assumption for this spec: **all sites deploying cluster mode start with a fresh Valkey cluster**. No migration of old standalone keys is required. Rooms that were created before this change will have their keys regenerated the next time a member-remove triggers rotation, or via the existing `ErrNoCurrentKey` → `Set` fallback in `room-service`.

Production sites upgrading from standalone to cluster must account for this: either accept a key-loss window (rooms enter degraded state until next operation triggers regeneration) or run a backfill tool before switching. Backfill tooling is out of scope for this spec.

---

## Change 2: Cluster Adapter — `pkg/roomkeystore`

### `ClusterConfig`

A new config struct alongside the existing `Config`:

```go
// ClusterConfig holds connection config for a Valkey cluster deployment.
// Addrs is a comma-separated list of seed node addresses; the go-redis
// ClusterClient discovers all nodes automatically via CLUSTER SLOTS.
// One address is sufficient but listing all masters is more robust.
type ClusterConfig struct {
    Addrs       []string      `env:"VALKEY_ADDRS,required" envSeparator:","`
    Password    string        `env:"VALKEY_PASSWORD" envDefault:""`
    GracePeriod time.Duration `env:"VALKEY_KEY_GRACE_PERIOD,required"`
}
```

The existing `Config` (single `Addr string`) is retained unchanged for standalone deployments.

### `clusterAdapter`

A new adapter wrapping `*redis.ClusterClient` that satisfies the existing `hashCommander` interface:

```go
type clusterAdapter struct {
    c *redis.ClusterClient
}
```

All method signatures are identical to `redisAdapter`. `redis.ClusterClient` exposes the same command API as `redis.Client` (`HSet`, `HGetAll`, `Del`, `Pipeline`, `NewScript().Run`) so the implementation is a direct parallel — the only difference is the underlying client type.

The Lua `rotateScript` is registered the same way via `redis.NewScript` and executes correctly on `ClusterClient` as long as both keys are hash-tagged to the same slot (guaranteed by Change 1).

### `NewValkeyClusterStore`

```go
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

`valkeyStore` and all its methods (`Set`, `Get`, `GetMany`, `GetByVersion`, `Rotate`, `Delete`, `SetWithVersion`, `Close`) are **unchanged**. The cluster is entirely transparent at the `valkeyStore` level.

### `GetMany` pipeline in cluster mode

`hgetallMany` issues a pipeline of `HGETALL` commands. `go-redis` `ClusterClient` handles cross-slot pipelines automatically — it groups commands by slot and issues separate round-trips to each node in parallel, then reassembles results in order. Because each `HGETALL room:{roomID}:key` touches only one key per room, this works correctly in cluster mode without any code change to `hgetallMany`.

---

## Change 3: `pkg/valkeyutil` Cluster Support

`search-service` connects via `valkeyutil.Connect` which uses `redis.NewClient` (standalone only). A parallel `ConnectCluster` function is added:

```go
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
    return &redisClient{c: c.(*redis.Client)}, nil
}
```

Wait — `redis.ClusterClient` and `redis.Client` are different types. The existing `redisClient` wrapper in `valkeyutil` wraps `*redis.Client`. A second wrapper `clusterRedisClient` is introduced for `*redis.ClusterClient`, satisfying the same `Client` interface. Both wrappers implement `Get`, `Set`, `Del`, `Close`.

The existing `Connect` (standalone) is retained unchanged.

---

## Change 4: Service Config Changes

Each service switches from `VALKEY_ADDR` (single address) to `VALKEY_ADDRS` (comma-separated list). The `env` tag uses `envSeparator:","` to parse into `[]string`. Services call `NewValkeyClusterStore` instead of `NewValkeyStore`.

| Service | Config field change | Constructor change |
|---|---|---|
| `room-service` | `ValkeyAddr string` → `ValkeyAddrs []string` | `NewValkeyStore` → `NewValkeyClusterStore` |
| `room-worker` | `ValkeyAddr string` → `ValkeyAddrs []string` | `NewValkeyStore` → `NewValkeyClusterStore` |
| `broadcast-worker` | `ValkeyAddr string` → `ValkeyAddrs []string` | `NewValkeyStore` → `NewValkeyClusterStore` |
| `history-service` | `ValkeyAddr string` → `ValkeyAddrs []string` | `NewValkeyStore` → `NewValkeyClusterStore` |
| `search-service` | `Valkey.Addr string` → `Valkey.Addrs []string` | `valkeyutil.Connect` → `valkeyutil.ConnectCluster` |

Validation: services that currently fail-fast on empty `VALKEY_ADDR` now fail-fast on empty `VALKEY_ADDRS` (zero-length slice).

---

## Change 5: Per-Site Docker Compose

Each service's `deploy/docker-compose.yml` currently declares a single Valkey node. This is replaced with a `bitnami/valkey-cluster` service that initialises a 3-master cluster internally.

```yaml
valkey-cluster:
  image: bitnami/valkey-cluster:8
  environment:
    - VALKEY_CLUSTER_REPLICAS=0
    - ALLOW_EMPTY_PASSWORD=yes
  ports:
    - "6379:6379"
```

Services that previously had `VALKEY_ADDR=valkey:6379` are updated to:
```yaml
VALKEY_ADDRS=valkey-cluster:6379
```

One seed address is sufficient — `ClusterClient` calls `CLUSTER SLOTS` on connect and discovers all nodes automatically.

---

## Error Handling

No new error types are introduced. Existing error wrapping conventions apply:

- `NewValkeyClusterStore` ping failure: `fmt.Errorf("valkey cluster connect: %w", err)`
- All `valkeyStore` method errors are unchanged — they wrap at the `hashCommander` level, cluster vs standalone is invisible above that layer
- `ConnectCluster` ping failure: `fmt.Errorf("valkey cluster connect: %w", err)` — consistent with standalone `Connect`'s `"valkey connect: %w"` pattern

---

## Configuration

### New env vars

| Env var | Replaces | Services | Description |
|---|---|---|---|
| `VALKEY_ADDRS` | `VALKEY_ADDR` | all | Comma-separated cluster seed addresses e.g. `node1:6379,node2:6380,node3:6381` |

`VALKEY_PASSWORD` and `VALKEY_KEY_GRACE_PERIOD` are unchanged.

### Backward compatibility

`VALKEY_ADDR` (standalone) is removed from service configs when cluster mode is adopted. There is no fallback — a service is either standalone or cluster, not both. Deployments that do not need cluster mode continue using the existing `NewValkeyStore` + `VALKEY_ADDR` path; only sites adopting cluster mode switch to `NewValkeyClusterStore` + `VALKEY_ADDRS`.

---

## Testing

### Unit tests — no change

All existing unit tests in `pkg/roomkeystore/roomkeystore_test.go` use the `fakeHashClient` test double which is independent of the real client type. No changes needed.

### Integration tests — `pkg/roomkeystore/integration_test.go`

A new `setupValkeyCluster` helper alongside the existing `setupValkey`:

```go
func setupValkeyCluster(t *testing.T, gracePeriod time.Duration) RoomKeyStore {
    // starts bitnami/valkey-cluster:8 with VALKEY_CLUSTER_REPLICAS=0
    // waits for "Cluster correctly created" log line
    // calls NewValkeyClusterStore with the mapped port as seed address
}
```

New cluster-specific tests:
- `TestValkeyClusterStore_Integration_RoundTrip` — Set → Get → Delete
- `TestValkeyClusterStore_Integration_RotateRoundTrip` — Set → Rotate → Get + GetByVersion
- `TestValkeyClusterStore_Integration_HashTagSlotConsistency` — verifies both key slots are the same (uses `CLUSTER KEYSLOT` command to assert `room:{x}:key` and `room:{x}:key:prev` hash to identical slots)

### Integration tests — `pkg/valkeyutil`

A new `TestConnectCluster_Integration` test using a `bitnami/valkey-cluster:8` container: connect → `SetJSONWithTTL` → `GetJSON` → `Del` round-trip.

### Image constant — `pkg/testutil/testimages/testimages.go`

```go
// ValkeyCluster is the image for cluster-mode Valkey integration tests.
ValkeyCluster = "bitnami/valkey-cluster:8"
```

### Coverage target

≥ 90% for new code in `pkg/roomkeystore` and `pkg/valkeyutil`. Cluster integration tests run under `//go:build integration` tag.

---

## Files Changed

| File | Change |
|---|---|
| `pkg/roomkeystore/roomkeystore.go` | Hash-tag `roomkey` and `roomprevkey` key name functions |
| `pkg/roomkeystore/adapter.go` | Add `clusterAdapter`, `ClusterConfig`, `NewValkeyClusterStore` |
| `pkg/roomkeystore/integration_test.go` | Add `setupValkeyCluster` + 3 cluster integration tests |
| `pkg/roomkeystore/roomkeystore_test.go` | Update key name assertions to expect hash-tagged format |
| `pkg/valkeyutil/valkey.go` | Add `clusterRedisClient`, `ConnectCluster` |
| `pkg/valkeyutil/valkey_test.go` | Add `TestConnectCluster_Integration` |
| `pkg/testutil/testimages/testimages.go` | Add `ValkeyCluster` constant |
| `room-service/main.go` | `ValkeyAddr` → `ValkeyAddrs`, `NewValkeyStore` → `NewValkeyClusterStore` |
| `room-worker/main.go` | Same |
| `broadcast-worker/main.go` | Same |
| `history-service/cmd/main.go` | Same |
| `search-service/main.go` | `Valkey.Addr` → `Valkey.Addrs`, `Connect` → `ConnectCluster` |
| `room-service/deploy/docker-compose.yml` | Single node → `bitnami/valkey-cluster`, `VALKEY_ADDRS` |
| `room-worker/deploy/docker-compose.yml` | Same |
| `broadcast-worker/deploy/docker-compose.yml` | Same |
| `history-service/deploy/docker-compose.yml` | Same |
| `search-service/deploy/docker-compose.yml` | Same |

---

## Out of Scope

- Migration tooling for existing standalone Valkey data to cluster key format
- Sentinel (HA without sharding) as an alternative — cluster mode is the chosen approach
- Per-purpose Valkey separation (keys vs cache on separate clusters)
- Production Kubernetes manifests — docker-compose covers local and ftest; production infra is managed separately
- Valkey persistence configuration (AOF/RDB) — required for production but an ops/infra concern, not a code concern
