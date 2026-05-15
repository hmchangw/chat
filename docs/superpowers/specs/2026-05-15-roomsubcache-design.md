# Room Subscription Cache Library

## Summary

Add `pkg/roomsubcache`, a thin Valkey-backed cache for room member lists. Fan-out workers (notably notification-worker, once implemented) can look up the recipients of a room broadcast without a Mongo round-trip per message. v1 is library-only with TTL-only freshness; no service is wired in this scope.

## Motivation

Notification-worker is intended to publish one NATS message per room member after every canonical message — for mobile push delivery. Today that path would call `ListSubscriptions(roomID)` against MongoDB on every published message. For a large channel (10k members), the `Find` plus BSON decode costs ~50–200ms per message and contends on the same Mongo collection that handles every other subscription operation.

Multiple notification-worker replicas processing the same hot rooms would each load the member list independently. A shared cache amortizes this across replicas.

## Non-goals

- **Active invalidation.** v1 relies on TTL; a membership change is reflected after at most `ttl` seconds. The consequence of staleness is bounded: an ex-member receives push notifications for ~TTL seconds after leaving, and a new member misses pushes for ~TTL seconds after joining. Neither corrupts data, and both self-heal at next refresh.
- **Notification-worker integration.** Wiring is deferred to a follow-up PR; this scope is library-only.
- **Notification payload encryption.** Orthogonal concern; broadcast-worker's pattern (`pkg/roomkeystore` + per-room encrypt) will be mirrored when notification-worker is implemented.
- **A general room-membership cache** for non-fan-out callers (e.g. room-service's member-list RPC). Different access pattern (paginated, role-aware) — not addressed here.

## Design

### Storage backend

Valkey, via the existing `pkg/valkeyutil` helpers. Justification:

- Infrastructure already deployed. `pkg/roomkeystore` and `search-service` use Valkey today; `docker-local/compose.deps.yaml` defines the service.
- Shared across notification-worker replicas, so the cache is populated once per hot room.
- One-line invalidation path (`Del`) when the future invalidator is built.

In-process LRU was considered and rejected: each replica would maintain its own copy, requiring NATS-pub-based eviction broadcasts to stay consistent — added complexity for marginal latency gain over a ~1ms cross-rack Valkey hop.

### Value shape

JSON blob of `[]Member{ID, Account}`, **not** a Valkey Hash.

Hash is attractive when membership churns incrementally — `HSET`/`HDEL` on join/leave avoids rewriting the full member list. v1 is TTL-only, so the cache never mutates incrementally: a miss rebuilds the whole entry from Mongo with a single `SET`. Under that access pattern, the blob has:

- Simpler cold-load semantics (atomic `SET` overwrite — no race between concurrent populators)
- Smaller memory footprint (10k members in a ziplist-promoted hashtable is ~1.5–2× the equivalent blob)
- No new `Client` interface methods (reuses `valkeyutil.GetJSON` / `SetJSONWithTTL`)
- One server-side `HGETALL` on a 10k-field hash is a known anti-pattern; one `GET` of a 600KB blob is not

If a later iteration moves to event-driven invalidation, Hash becomes the right shape — the package can swap internals without changing the `Cache` interface.

### Projection: `Member`, not `model.Subscription`

Notification-worker only reads two fields from each subscription: `User.ID` (to skip the sender) and `User.Account` (to build the NATS subject). Caching the full `model.Subscription` would store ~10 extra fields (roles, joinedAt, lastSeenAt, mentions, …) and bloat a 10k-member room from ~600KB to ~5MB. The projection lives in the cache package to avoid coupling `pkg/model` to a Valkey-specific layout.

### Empty-list semantics

`Set(nil)` is stored as `"[]"`, not `"null"`. `Get` returns a non-nil empty slice on this cache hit and `valkeyutil.ErrCacheMiss` only on absence. This lets callers negative-cache empty or deleted rooms without re-hitting Mongo on every read.

### Key shape

`room:{roomID}:subs`. Single-site Valkey per deployment, so no site prefix. Coexists with `pkg/roomkeystore`'s `room:{roomID}:key` and `:key:prev` under a shared `room:` namespace; future additions should follow the same `room:{roomID}:<aspect>` pattern.

### Input guards and size cap

- **Empty `roomID` is rejected at every entry point** (`Get`, `Set`, `Invalidate`). Closes a logical-collision case where multiple callers would share the `room::subs` slot, and surfaces programmer errors at the call site rather than silently caching under a degenerate key.
- **`Get` caps blob size at `DefaultMaxValueBytes` (16 MiB)** before unmarshaling — defense-in-depth against a compromised Valkey writer trying to OOM a reader via a multi-GB JSON array. Configurable per-instance via `WithMaxValueBytes`; pass `0` to disable. The cap does not prevent the wire-level transfer of an oversized value (Valkey already returned it before we check), only the unmarshal-time allocation explosion.

## API

```go
type Member struct {
    ID      string `json:"id"`
    Account string `json:"account"`
}

type Cache interface {
    Get(ctx context.Context, roomID string) ([]Member, error)
    Set(ctx context.Context, roomID string, members []Member, ttl time.Duration) error
    Invalidate(ctx context.Context, roomID string) error
}

func NewValkeyCache(client valkeyutil.Client) Cache
```

`Invalidate` is included even though v1 doesn't use it — the future invalidator subscribes to membership-change events and calls `Invalidate(roomID)`. Exposing it now means that wiring lands without requiring a library change.

## Performance baseline

Measured with the unit benchmarks against the fake Valkey client (marshal/unmarshal cost in isolation from network):

| Members | Get ns/op | Get allocs/op | Set ns/op |
|---------|-----------|---------------|-----------|
| 10 | 7.3µs | 33 | 1.2µs |
| 100 | 60µs | 216 | 9.4µs |
| 1000 | 588µs | 2019 | 100µs |
| 10000 | 6.5ms | 20027 | 1.1ms |

`Get` is dominated by `json.Unmarshal` (one alloc per `Member` plus slice growth). `Set` is much cheaper because `json.Marshal` writes into a single buffer.

At 10k members, the cache layer alone costs ~6.5ms per `Get`. A Mongo `Find` + decode over the same data is in the 50–200ms range, so the cache remains an order of magnitude faster even before counting Mongo connection-pool contention.

## Future work

- **Wire into notification-worker** once that service is implemented. Adds env config for Valkey connection and TTL, threads a `roomsubcache.Cache` through the handler, falls back to Mongo on miss.
- **Invalidator.** When room-service starts publishing subscribe/unsubscribe events on a known subject, a small subscriber (either in notification-worker itself or a standalone service) calls `cache.Invalidate(roomID)` to evict eagerly. Reduces effective staleness from `ttl` to event-propagation latency.
- **Reconsider Hash representation** if write amplification under high-churn rooms becomes measurable.
- **Adopt for room-service's member-list RPC** if profiling shows it's worth the indirection. Probably not — that path is paginated and role-aware, doesn't match this cache's shape.
