# Message At-Rest Encryption Design

**Date:** 2026-05-05
**Status:** Approved (pending implementation plan)

## Background and goal

Cassandra currently stores message bodies and other user-authored content in
plaintext. The existing `pkg/roomcrypto` and `pkg/roomkeystore` libraries
implement an ECIES-style scheme for **broadcast/transit** encryption to
clients; they do not protect data at rest in the database.

This spec adds server-side at-rest encryption of user-authored message
content in Cassandra using **envelope encryption**:

- Each room has a single 256-bit **Data Encryption Key (DEK)** used to
  encrypt the message payload with AES-256-GCM.
- Every DEK is itself wrapped by HashiCorp Vault's **transit secrets
  engine** before being stored in MongoDB. The plaintext KEK never
  leaves the Vault server.
- Vault's transit engine handles key versioning and rotation natively
  via its `rotate` API; ciphertext blobs are self-describing
  (`vault:vN:...`) so older versions decrypt automatically until the
  operator advances `min_decryption_version`.

The existing broadcast-side encryption is unchanged and continues to operate
as a separate layer.

## Non-goals

- **Backfill of pre-cutover plaintext rows.** A hybrid read path supports
  both encrypted and legacy plaintext rows. A backfill of historical rows is
  a separate future spec.
- **Encryption of the search index.** The search index will continue to
  hold plaintext content until a separate future spec addresses it. This is
  an acknowledged plaintext exposure until then.
- **Encryption of MongoDB collections** other than the new
  `room_data_keys` collection introduced here. Rooms, subscriptions, and
  related metadata stay plaintext.
- **Per-message or per-room DEK rotation.** Each room has exactly one DEK
  for its lifetime. KEK rotation is the rotation story.

## Decisions summary

| # | Decision | Choice |
|---|---|---|
| 1 | Scope of encryption | All Cassandra message PII: `msg`, `attachments`, `card.data`, `cardAction.data`, `sysMsgData`, and content fields of `quoted_parent_message` |
| 2 | KEK rotation | Versioned with online rotation support |
| 3 | DEK rotation | One DEK per room, no rotation |
| 4 | DEK store location | New dedicated MongoDB collection `room_data_keys` |
| 5 | Search index | Out of scope (future spec) |
| 6 | KEK delivery | HashiCorp Vault transit secrets engine (Kubernetes auth in prod, static token for local dev) |
| 7 | Legacy plaintext rows | Hybrid read path; no backfill in this spec |
| 8 | Cassandra column layout | Single bundled `enc_payload` blob plus `enc_meta` UDT |
| 9 | Default rollout posture | `ATREST_ENABLED=true` by default; explicit opt-out only |

## Architecture overview

```
                     ┌──────────────────────────┐
                     │  HashiCorp Vault         │
                     │  transit/encrypt/<key>   │
                     │  transit/decrypt/<key>   │
                     │  (KEK never leaves)      │
                     └────────────┬─────────────┘
                                  │ HTTPS, k8s-auth or token
                                  ▼
                     ┌──────────────────────────┐
                     │  pkg/atrest              │
                     │  - KeyWrapper (Vault)    │
                     │  - DEKStore (Mongo)      │
                     │  - Cipher (encrypt/      │
                     │    decrypt msg payloads) │
                     │  - in-process DEK cache  │
                     └──┬─────────┬─────────────┘
                        │         │
        ┌───────────────┘         └────────────────┐
        ▼                                          ▼
┌────────────────┐                        ┌────────────────────┐
│ message-worker │  encrypt → Cassandra   │  history-service   │
│ (write path)   │                        │  (read + edit +    │
└────────────────┘                        │   delete paths)    │
                                          └────────────────────┘
```

Key invariants:

1. **`pkg/atrest`** is the only place that calls crypto primitives. Both
   message-worker and history-service depend on the same library.
2. **DEKs are created lazily** at first encrypted write to a room. No
   changes to room-service.
3. **Federation is unaffected.** Cross-site events flow through OUTBOX/INBOX
   as plaintext; each site lazily creates its own local DEK and encrypts
   independently when its message-worker first writes to its local
   Cassandra. KEK material never crosses sites.
4. **`pkg/roomcrypto` / `pkg/roomkeystore` are not touched.** They cover a
   different concern (broadcast-side encryption to clients) and continue to
   operate as-is.

## Data shapes

### Vault transit key

The KEK is a Vault transit key (default `chat-kek` under the `transit/`
mount). The transit engine handles AES-256-GCM internally; the wrapped
DEK is the opaque "vault:vN:..." string that Vault returns from
`transit/encrypt/<key>`. Older key versions remain decryptable until
the operator advances the key's `min_decryption_version`, so
operational rotation is a single API call (`vault write -f
transit/keys/chat-kek/rotate`) — no application code changes required.

Vault is reached over HTTPS. Production services authenticate via
**Kubernetes auth**: a Vault role is bound to the service's
ServiceAccount, and the wrapper exchanges the projected SA token for a
short-lived Vault token at startup, renewing it via Vault's
`LifetimeWatcher`. Local docker-compose passes a static `VAULT_TOKEN`
instead.

### MongoDB collection `room_data_keys`

```go
type RoomDataKey struct {
    ID         string    `bson:"_id"`         // roomID
    WrappedDEK []byte    `bson:"wrappedDEK"`  // Vault transit ciphertext (e.g. "vault:v1:...")
    CreatedAt  time.Time `bson:"createdAt"`
}
```

One row per room. `_id = roomID`, so DEK creation is naturally idempotent
via `$setOnInsert`. Rows are never deleted in normal operation. The
KEK version is encoded inside `WrappedDEK` itself (as the "vN" prefix in
the Vault ciphertext), so no version field is stored alongside.

### Cassandra schema additions

New UDT (added to keyspace init in `docker-local/cassandra/init/*.cql` and
documented in `docs/cassandra_message_model.md`):

```cql
CREATE TYPE IF NOT EXISTS enc_meta (
    nonce blob   -- 12 bytes, AES-GCM nonce for enc_payload
);
```

A UDT (rather than a plain `enc_nonce blob` column) is chosen for forward
compatibility: future additions like a `dek_version int` (if per-room DEK
rotation is ever introduced) or an `alg` discriminator (if a second cipher
is ever supported) can extend the UDT without another column-level
migration.

`kek_version` is intentionally **not** stored on the message row. The KEK
version that wraps a room's DEK is authoritative on the
`room_data_keys` document and is used only at unwrap time. Recording it
per-message would denormalize a value that changes whenever the rotation
worker re-wraps the DEK, with no decryption benefit.

Two new columns on each of the four message tables (`messages_by_room`,
`messages_by_id`, `thread_messages_by_room`, `pinned_messages_by_room`):

```cql
ALTER TABLE messages_by_room          ADD enc_payload blob;
ALTER TABLE messages_by_room          ADD enc_meta    frozen<enc_meta>;
ALTER TABLE messages_by_id            ADD enc_payload blob;
ALTER TABLE messages_by_id            ADD enc_meta    frozen<enc_meta>;
ALTER TABLE thread_messages_by_room   ADD enc_payload blob;
ALTER TABLE thread_messages_by_room   ADD enc_meta    frozen<enc_meta>;
ALTER TABLE pinned_messages_by_room   ADD enc_payload blob;
ALTER TABLE pinned_messages_by_room   ADD enc_meta    frozen<enc_meta>;
```

The legacy plaintext columns (`msg`, `attachments`, `card`, `card_action`,
`sys_msg_data`, and the relevant content fields inside the
`quoted_parent_message` UDT) remain in the schema for backward compatibility
with pre-cutover rows. New encrypted rows write only the new columns and
leave the legacy ones null.

`docs/cassandra_message_model.md` is the single source of truth for the
Cassandra schema. The implementing PR updates that document plus its two
downstream mirrors per project policy: the Go UDT/row structs in
`pkg/model/cassandra/` and the init DDL under `docker-local/cassandra/init/`.

### Encrypted payload struct

The bundled struct serialized → encrypted → stored in `enc_payload`:

```go
type EncryptedFields struct {
    Msg                 string                  `json:"msg,omitempty"`
    Attachments         [][]byte                `json:"attachments,omitempty"`
    Card                *cassandra.Card         `json:"card,omitempty"`
    CardAction          *cassandra.CardAction   `json:"cardAction,omitempty"`
    SysMsgData          []byte                  `json:"sysMsgData,omitempty"`
    QuotedParentContent *QuotedParentEncrypted  `json:"quotedParentContent,omitempty"`
}

type QuotedParentEncrypted struct {
    Msg         string   `json:"msg,omitempty"`
    Attachments [][]byte `json:"attachments,omitempty"`
    // mentions, sender, timestamps, IDs stay plaintext on the
    // quoted_parent_message UDT — they are not user-authored body.
}
```

Serialization uses `encoding/json`, matching the project-wide convention.

### Algorithm choices

- **Symmetric cipher:** AES-256-GCM for both DEK wrapping and message
  payload encryption. Keys are 32 bytes; nonces are 12 bytes generated from
  `crypto/rand` per operation; the 16-byte GCM auth tag is appended to each
  ciphertext.
- **DEK generation:** 32 bytes from `crypto/rand`.
- AES-GCM is consistent with `pkg/roomcrypto`; no new crypto primitives are
  introduced.

### Detecting "is this row encrypted?"

`enc_payload IS NOT NULL`. The hybrid read path branches on this single
check.

## `pkg/atrest` library design

```
pkg/atrest/
    atrest.go            // public types: EncryptedFields, EncMeta, RoomDataKey, Config, errors
    keywrapper.go        // KeyWrapper / KeyWrapperCloser interfaces
    vault_wrapper.go     // Vault transit-engine implementation (k8s + token auth)
    dek_store.go         // DEKStore interface + Mongo implementation
    dek_store_test.go
    cipher.go            // Cipher implementation with cache
    cipher_test.go
    cache.go             // unexported LRU (caches cipher.AEAD per room)
    metrics.go           // prometheus counters
    split.go             // SplitForEncryption / Strip / Apply helpers
    integration_test.go  // testcontainers Mongo + Vault
```

### KeyWrapper

```go
type KeyWrapper interface {
    Wrap(ctx context.Context, dek []byte) ([]byte, error)
    Unwrap(ctx context.Context, ciphertext []byte) ([]byte, error)
}

type KeyWrapperCloser interface {
    KeyWrapper
    io.Closer
}
```

- The blob format is implementation-specific and opaque to callers
  (Vault returns `vault:vN:...` strings).
- The Vault implementation logs in via Kubernetes auth when
  `VAULT_K8S_ROLE` is set; otherwise it falls back to a static
  `VAULT_TOKEN`. The k8s path renews its token via `LifetimeWatcher`
  in a background goroutine which `Close` stops.
- Encryption boundary: the plaintext KEK never enters the Go process —
  Wrap and Unwrap are network calls to Vault.

### DEKStore

```go
type DEKStore interface {
    Get(ctx context.Context, roomID string) (*RoomDataKey, error)  // nil if absent
    Upsert(ctx context.Context, key RoomDataKey) error              // $setOnInsert semantics
    Replace(ctx context.Context, key RoomDataKey) error             // reserved for future use; not invoked on the Vault rotation path
}
```

The Mongo implementation uses:

- `Get`: `FindOne({_id: roomID})`, returns `(nil, nil)` on
  `mongo.ErrNoDocuments`.
- `Upsert`: `UpdateOne({_id: roomID}, {$setOnInsert: {...}}, upsert=true)`.
  Race-safe: concurrent first writers all converge on the row inserted by
  the winner.
- `Replace`: `ReplaceOne({_id: roomID}, full doc)`. Reserved for future
  use (e.g. moving wrapped DEKs between Vault keys); not invoked on the
  in-place Vault transit rotation path.

The interface is defined in the consumer (`pkg/atrest`) per project
convention (interfaces in consumer, not implementer).

### Cipher

```go
type Cipher interface {
    Encrypt(ctx context.Context, roomID string, fields EncryptedFields) ([]byte, EncMeta, error)
    Decrypt(ctx context.Context, roomID string, encPayload []byte, encMeta EncMeta) (EncryptedFields, error)
}
```

Encrypt logic:

1. Look up cached `cipher.AEAD` by `roomID`.
2. On cache miss: `DEKStore.Get`. If absent, generate a new 32-byte DEK,
   call `KeyWrapper.Wrap(ctx, dek)`, then `DEKStore.Upsert`. A re-`Get`
   detects a concurrent winner — if our wrapped bytes don't match, the
   winner's `WrappedDEK` is unwrapped via `KeyWrapper.Unwrap` instead.
   Build an AES-256-GCM AEAD around the unwrapped DEK and cache it.
3. Marshal `fields` to JSON.
4. `aead.Seal` with a fresh 12-byte random nonce.
5. Return `(ciphertext, EncMeta{Nonce: nonce})`.

Decrypt logic:

1. Look up cached AEAD (same as encrypt).
2. `aead.Open` with `encMeta.Nonce`. Auth tag failure returns
   `ErrAuthFailed`.
3. Unmarshal JSON into `EncryptedFields`. Return.

### DEK cache

- Unexported, owned by the Cipher implementation.
- LRU keyed by `roomID` holding the constructed `cipher.AEAD` so batch
  reads/writes for the same room skip the AES key schedule + GHASH
  setup that `aes.NewCipher` + `cipher.NewGCM` would otherwise repeat.
- Capacity from `ATREST_DEK_CACHE_SIZE` (default 10_000).
- TTL from `ATREST_DEK_CACHE_TTL` (default 1h). Since the DEK bytes never
  change for the lifetime of a room, TTL is not required for correctness;
  it exists only as a defensive bound on memory retention for cold rooms
  in long-lived processes. LRU eviction is the primary cap.
- Vault key rotation does not invalidate cache entries — Vault rotates
  the KEK, not the DEK. The application's unwrapped DEK is unchanged
  across rotations.

### Errors

Typed sentinel errors exported from the package, including:

- `ErrAuthFailed` — GCM tag mismatch on decrypt (tampering or wrong key).
- `ErrPayloadMalformed` — JSON unmarshal failure after decrypt.

Vault errors (network, auth, missing key) propagate through `Wrap` /
`Unwrap` wrapped with `vault transit encrypt:` / `vault transit
decrypt:` context.

All wrap-style errors from internal sources are wrapped per project
convention (`fmt.Errorf("short description: %w", err)`).

## Service integration

### message-worker (write path)

1. Add `cipher atrest.Cipher` to the handler struct, wired in `main.go`
   from a new `Atrest atrestConfig` block on the service config.
2. Before persisting, split the message into "metadata" fields (room_id,
   sender, timestamps, tshow flags, thread/quote IDs, reactions, etc.) and
   `EncryptedFields`.
3. Call `cipher.Encrypt(ctx, roomID, fields)`.
4. Pass `(metadata, encPayload, encMeta)` to a new
   `store.InsertEncryptedMessage` method that writes the new columns and
   leaves the legacy ones null.

The legacy `InsertMessage` method stays available so the change can be
landed behind `ATREST_ENABLED` and toggled per environment if needed,
even though the default is `true`.

### history-service (read path)

The Cassandra read path lives in `internal/cassrepo`. Today it scans rows
into a `cassandra.Message` struct via a `structScan` helper.

1. Add `enc_payload blob` and `enc_meta frozen<enc_meta>` to the `cql`
   struct (`EncPayload []byte` and `EncMeta *EncMeta`).
2. After scan, branch:
   - `EncPayload != nil` → call
     `cipher.Decrypt(ctx, roomID, EncPayload, *EncMeta)` and copy the
     resulting fields back into the `cassandra.Message`.
   - `EncPayload == nil` → row is legacy plaintext; leave fields as
     scanned.
3. The service layer (handlers) sees a fully-populated
   `cassandra.Message` regardless of which branch fired. No handler-level
   changes are required.

### history-service (edit and delete paths)

- **Edit:** Mirror the write path — split into metadata + `EncryptedFields`,
  encrypt, write the new columns. The DEK is reused since DEKs do not
  rotate per Decision 3.
- **Delete:** New encrypted rows tombstone by setting `enc_payload = null`
  alongside `deleted = true`. No crypto operation runs on delete.

### Configuration

Both message-worker and history-service get two identical config blocks
parsed via `caarlos0/env`:

```go
type Config struct {
    Enabled      bool          `env:"ATREST_ENABLED"        envDefault:"true"`
    DEKCacheSize int           `env:"ATREST_DEK_CACHE_SIZE" envDefault:"10000"`
    DEKCacheTTL  time.Duration `env:"ATREST_DEK_CACHE_TTL"  envDefault:"1h"`
}

type VaultConfig struct {
    Address      string `env:"VAULT_ADDR"                envDefault:""`
    TransitMount string `env:"ATREST_VAULT_TRANSIT_MOUNT" envDefault:"transit"`
    TransitKey   string `env:"ATREST_VAULT_TRANSIT_KEY"  envDefault:"chat-kek"`
    K8sRole      string `env:"VAULT_K8S_ROLE"            envDefault:""`
    K8sAuthPath  string `env:"VAULT_K8S_AUTH_PATH"       envDefault:"kubernetes"`
    Token        string `env:"VAULT_TOKEN"               envDefault:""`
}
```

When `K8sRole` is set the wrapper authenticates via Kubernetes auth and
renews its Vault token in the background; otherwise it uses `Token`
directly. Local docker-compose passes `VAULT_TOKEN=dev-only-token`;
production sets `VAULT_K8S_ROLE` instead and leaves `Token` empty.

When `Enabled=false`, the cipher is left `nil` and the store/repo
branches on `s.cipher == nil` to take the legacy plaintext path. Tests
that do not exercise crypto either inject a real cipher (against a test
Mongo and the shared dev Vault container) or set `ATREST_ENABLED=false`.

A misconfigured deploy with `Enabled=true` and an unreachable Vault
fails closed: the service exits at startup. Better than silently writing
plaintext.

## Rollout

One PR per step, each independently revertable:

1. Land `pkg/atrest` with full unit + integration tests. No service
   consumes it yet.
2. Cassandra schema migration: add the `enc_meta` UDT and the two new
   columns on the three message tables. Update
   `docs/cassandra_message_model.md`, `pkg/model/cassandra/message.go`,
   and `docker-local/cassandra/init/*.cql` in the same PR.
3. Wire `atrest.Cipher` into message-worker behind `ATREST_ENABLED`.
   Add `enc_payload`/`enc_meta` to its store layer.
4. Wire decrypt into history-service read path. Add re-encrypt to edit
   and delete paths.
5. Provision Vault in each environment: enable the transit engine,
   create the `chat-kek` named key, configure a Kubernetes auth role
   bound to each service's ServiceAccount with `decrypt` and `encrypt`
   capabilities on `transit/encrypt/chat-kek` and
   `transit/decrypt/chat-kek`. Confirm new messages are stored
   encrypted in staging Cassandra.
6. (Future, separate spec.) Backfill of legacy plaintext rows.

## KEK rotation

Vault's transit engine handles rotation natively — no application code
or DEK row changes required:

1. `vault write -f transit/keys/chat-kek/rotate` produces a new key
   version. Subsequent `transit/encrypt/chat-kek` calls return
   ciphertext tagged with the new version (`vault:vN+1:...`).
2. Older ciphertext blobs continue to decrypt because Vault retains
   prior versions until `min_decryption_version` advances.
3. To remove an old version from service, advance
   `min_decryption_version` (`vault write transit/keys/chat-kek/config
   min_decryption_version=N+1`). Any DEK row still wrapped under an
   older version becomes undecryptable; in practice operators run a
   one-shot re-wrap job before advancing — implementing that job is
   straightforward (read each `room_data_keys` row, call
   `transit/rewrap/chat-kek`, `Replace` the row) but out of scope for
   this spec.

## Observability

- **Tracing:** `Cipher.Encrypt` and `Cipher.Decrypt` are spans with
  attributes `roomID`, `dekCacheHit`, and payload bytes in/out. Plaintext
  is never recorded on a span.
- **Metrics (Prometheus):**
  - `atrest_encrypt_total{result}` and `atrest_decrypt_total{result}`
  - `atrest_dek_cache_hits_total`, `atrest_dek_cache_misses_total`
  - `atrest_dek_creations_total`
  - `atrest_kek_wrap_total{result}` (Vault Wrap calls)
  - `atrest_kek_unwrap_total{result}` (Vault Unwrap calls)
- **Logs:** structured `log/slog`. Plaintext content and DEK bytes are
  never logged. Decrypt failures log `roomID`, `messageID`, and the
  error class.

## Testing

### `pkg/atrest` unit tests

- **Cipher** (with a deterministic in-memory `staticKeyWrapper` and the
  in-memory fake DEKStore): round-trip encrypt/decrypt, missing-DEK
  lazy creation, tampered ciphertext rejected (`ErrAuthFailed`), cache
  hit reuses cached AEAD without re-fetching from store, Vault errors
  propagate via wrapped errors.

### `pkg/atrest` integration tests (`//go:build integration`)

- testcontainers MongoDB + testcontainers HashiCorp Vault (dev mode,
  transit engine pre-configured). End-to-end round-trip with real
  Vault wrap/unwrap.
- Concurrent first-write race: N goroutines call `Encrypt` for the same
  roomID; assert exactly one DEK row exists and all goroutines decrypt
  each other's output.
- Vault key rotation: write a row, call
  `transit/keys/chat-kek/rotate`, verify the existing payload still
  decrypts under a fresh Cipher (cleared cache).

### Service-level tests

- message-worker handler tests inject a fake `Cipher` so they do not need
  real KEK material or Mongo.
- history-service tests likewise.
- A new history-service integration test covers the hybrid read path:
  insert one legacy plaintext row and one encrypted row, read both,
  assert both come back fully populated.

### Coverage

- ≥ 90% for `pkg/atrest` (security-critical core).
- ≥ 80% elsewhere, per project rules.
- Coverage must include error paths: GCM auth failure, Vault unwrap
  failure, DEK store errors, malformed payload after decrypt.

## Risks

- **Lost Vault KEK = unrecoverable data.** The Vault transit key is the
  root of the encryption hierarchy; losing all of its versions makes
  every DEK — and therefore every encrypted message — permanently
  unreadable. Mitigation: Vault's storage backend (Raft / Consul /
  cloud KMS) owns the backup story per the cluster's runbook, and the
  transit engine retains all key versions until an operator explicitly
  deletes them. This spec calls the invariant out as the single most
  important operational property.
- **Vault unavailability = service unavailability.** First-encrypt /
  cache-miss paths require a live Vault. Mitigation: the in-process DEK
  cache amortizes most calls; Vault HA + autopilot are operational
  responsibilities.
- **Default-on means a misconfigured deploy fails closed.** A pod with
  `ATREST_ENABLED=true` and unreachable Vault exits at startup.
  This is intentional: failing closed is preferable to silently
  storing plaintext.
- **DEK cache thrash under high room cardinality.** With more active
  rooms than the cache size, every encrypt/decrypt round-trips to Mongo
  and unwraps. Mitigation: capacity is configurable; metrics on hit/miss
  rates expose the regime so it can be tuned.
- **Search index plaintext exposure.** Until the future search-index
  encryption spec lands, the search backend retains a plaintext copy of
  message content. The deployment must treat the search index as
  equivalently sensitive to Cassandra during this period.
