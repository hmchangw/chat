# Room Key Storage Library — Design Spec

**Date:** 2026-03-27
**Status:** Approved

---

## Overview

A general-purpose Go library at `pkg/roomkeys/` that manages P-256 room key pairs in Valkey. Any service that needs to encrypt or decrypt room messages can use this library to fetch the current key, rotate keys, retrieve a past key by ID, or expose a JWKS document to clients. Key pairs are stored as JWK entries in a Valkey sorted set. The library generates key pairs internally; callers do not supply raw key material.

---

## Out of Scope

- TTL-based key expiry in Valkey — deferred to a follow-up spec.
- Server-side message decryption — the existing `pkg/roomcrypto` package handles encryption; decryption is not yet in scope for any service.
- Access control — the library is a storage primitive; callers are responsible for authorising who may call `Rotate`.

---

## Package Structure

```
pkg/roomkeys/
  roomkeys.go          — Store type, Options, NewStore, public API methods
  jwk.go               — internal JWK/JWKS types and serialisation helpers
  roomkeys_test.go     — unit tests (mocked Valkey client)
  mock_valkey_test.go  — generated mock for the Valkey client interface
  integration_test.go  — integration tests (//go:build integration, Valkey container)
```

### New Dependency

`github.com/valkey-io/valkey-go` — Valkey's native Go client (Redis-protocol compatible). This spec serves as explicit approval to add it as a direct dependency per the project's "ask before adding dependencies" rule.

---

## API Surface

```go
package roomkeys

// Options configures the Store.
type Options struct {
    MaxVersions int // total keys retained per room (current + previous); default 3
}

// Store manages room key pairs in Valkey.
type Store struct { /* unexported fields */ }

// NewStore creates a Store with the given Valkey client and options.
func NewStore(client valkey.Client, opts Options) *Store

// GetCurrentKey returns the current (newest) key for the room.
// If no key exists yet, one is generated and stored automatically.
func (s *Store) GetCurrentKey(ctx context.Context, roomID string) (*RoomKey, error)

// Rotate generates a new key for the room, demoting the current key to previous.
// Retains at most Options.MaxVersions keys total; oldest are pruned.
func (s *Store) Rotate(ctx context.Context, roomID string) (*RoomKey, error)

// GetKeyByID returns a specific retained key by its kid.
// Returns an error wrapping ErrKeyNotFound if the kid is not in the room's key ring.
func (s *Store) GetKeyByID(ctx context.Context, roomID, kid string) (*RoomKey, error)

// GetJWKS returns a JWKS document with the public portions of all retained keys,
// newest first. Suitable for serving to clients over HTTP.
func (s *Store) GetJWKS(ctx context.Context, roomID string) (*JWKS, error)

// RoomKey is a P-256 key pair with metadata.
type RoomKey struct {
    KID        string    // UUID key identifier
    PublicKey  []byte    // 65-byte uncompressed P-256 point
    PrivateKey []byte    // 32-byte P-256 scalar
    CreatedAt  time.Time
}

// JWKS is a JSON Web Key Set containing public keys only.
type JWKS struct {
    Keys []JWK `json:"keys"`
}

// JWK is a single EC public key in JWK format (RFC 7517).
type JWK struct {
    KTY string `json:"kty"` // always "EC"
    CRV string `json:"crv"` // always "P-256"
    KID string `json:"kid"`
    Use string `json:"use"` // always "enc"
    X   string `json:"x"`  // base64url-encoded X coordinate
    Y   string `json:"y"`  // base64url-encoded Y coordinate
}

// ErrKeyNotFound is returned by GetKeyByID when the kid is not present in the ring.
var ErrKeyNotFound = errors.New("key not found")
```

`RoomKey` carries the private key for services that need to decrypt. `GetJWKS` strips the private `d` field — only `x` and `y` (public coordinates) are returned to clients.

---

## Data Model

**Valkey key:** `roomkeys:{roomID}` — a sorted set.

- **Score:** Unix timestamp in seconds at key creation time (determines ordering and pruning).
- **Member:** JSON-encoded internal JWK entry including the private `d` field.

### Internal JWK stored in Valkey

```json
{
  "kty": "EC",
  "crv": "P-256",
  "kid": "01954f3a-c1b2-7000-8e3f-abcdef012345",
  "use": "enc",
  "x": "<base64url X coordinate>",
  "y": "<base64url Y coordinate>",
  "d": "<base64url private scalar>",
  "iat": 1743033600
}
```

`d` is the base64url-encoded 32-byte P-256 private scalar. `iat` (issued-at, Unix seconds) mirrors the sorted set score and is used to reconstruct `CreatedAt` on deserialisation. `iat` is an internal field — it is not included in the JWKS response.

### Valkey Operations

| Operation | Commands |
|---|---|
| `GetCurrentKey` | `ZREVRANGE roomkeys:{roomID} 0 0` |
| `Rotate` / lazy-init | `ZADD` new entry + `ZREMRANGEBYRANK 0 -(MaxVersions+1)` in a pipeline |
| `GetKeyByID` | `ZREVRANGE roomkeys:{roomID} 0 -1`, linear scan for matching `kid` |
| `GetJWKS` | `ZREVRANGE roomkeys:{roomID} 0 -1`, strip `d` and `iat` from each entry |

`GetKeyByID` scans all retained members (at most `MaxVersions`, default 3) — no secondary index is needed at this scale.

---

## Key Generation

Key pairs are generated internally using `crypto/ecdh`:

```go
privKey, err := ecdh.P256().GenerateKey(rand.Reader)
```

The `kid` is a UUID generated via `github.com/google/uuid` (already a project dependency). `PublicKey` is the 65-byte uncompressed point (`privKey.PublicKey().Bytes()`). `PrivateKey` is the 32-byte scalar (`privKey.Bytes()`). Both are base64url-encoded for JWK storage.

---

## Error Handling

All errors are wrapped with context per project convention. No raw Valkey or crypto errors are surfaced to callers.

| Situation | Wrapped message |
|---|---|
| Valkey read command fails | `"fetching key ring for room %q: %w"` |
| Valkey write command fails | `"storing key for room %q: %w"` |
| JWK JSON marshal fails | `"marshalling key for room %q: %w"` |
| JWK JSON unmarshal fails | `"unmarshalling key entry: %w"` |
| P-256 key generation fails | `"generating P-256 key pair: %w"` |
| Kid not in ring | `fmt.Errorf("kid %q: %w", kid, ErrKeyNotFound)` |

`ErrKeyNotFound` is the only sentinel error. All others are opaque wrapped errors. Callers check for missing keys with `errors.Is(err, ErrKeyNotFound)`.

---

## Testing

### Unit Tests (`roomkeys_test.go`, `package roomkeys`)

Mock the Valkey client interface using `go.uber.org/mock`. Table-driven tests for each method:

| Method | Scenarios |
|---|---|
| `GetCurrentKey` | key exists → returns newest; no key exists → auto-generates and stores; Valkey error → returns wrapped error |
| `Rotate` | existing keys → new key stored, old key demoted, pruning fires when at `MaxVersions`; no existing keys → generates first key; Valkey error → returns wrapped error |
| `GetKeyByID` | kid found → returns correct key; kid not found → returns `ErrKeyNotFound`; Valkey error → returns wrapped error |
| `GetJWKS` | multiple retained keys → returned newest-first, `d` field absent from all entries; empty ring → returns empty `Keys` slice; Valkey error → returns wrapped error |

### Integration Tests (`integration_test.go`, `//go:build integration`)

Use `testcontainers-go` with the official `valkey/valkey` Docker image. A `setupValkey(t *testing.T)` helper starts the container, registers `t.Cleanup`, and returns a connected client.

Full round-trip scenarios:
- Generate first key via `GetCurrentKey` on an empty room.
- Rotate → `GetKeyByID` returns both current and previous key.
- `GetJWKS` shows all retained keys newest-first, no `d` field present.
- Rotate until `MaxVersions` is exceeded → oldest key is pruned and no longer returned by `GetKeyByID`.

### Coverage Target

90%+ in line with the project's `pkg/` standard.
