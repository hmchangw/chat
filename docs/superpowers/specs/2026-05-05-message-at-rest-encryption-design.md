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
- Every DEK is itself encrypted ("wrapped") with a versioned **Key
  Encryption Key (KEK)** before being stored in MongoDB.
- The KEK material lives in a Kubernetes Secret mounted as a JSON file.
  Multiple KEK versions are supported simultaneously to enable online KEK
  rotation.

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
| 6 | KEK delivery | JSON file mounted from k8s Secret |
| 7 | Legacy plaintext rows | Hybrid read path; no backfill in this spec |
| 8 | Cassandra column layout | Single bundled `enc_payload` blob plus `enc_meta` UDT |
| 9 | Default rollout posture | `ATREST_ENABLED=true` by default; explicit opt-out only |

## Architecture overview

```
                     ┌──────────────────────────┐
                     │   k8s Secret (mounted)   │
                     │   /etc/chat/keks.json    │
                     │   { current: 3, keys:    │
                     │     {1:..,2:..,3:..} }   │
                     └────────────┬─────────────┘
                                  │ load + watch
                                  ▼
                     ┌──────────────────────────┐
                     │  pkg/atrest (new)        │
                     │  - KEKLoader (file)      │
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

### KEK secret file

Mounted at the path configured by `ATREST_KEK_FILE` (default
`/etc/chat/keks.json`):

```json
{
  "current": 3,
  "keys": {
    "1": "<base64 32-byte AES key>",
    "2": "<base64 32-byte AES key>",
    "3": "<base64 32-byte AES key>"
  }
}
```

`KEKLoader` validates on every load:

- File parses as JSON with the schema above.
- At least one entry in `keys`.
- Every value in `keys` decodes to exactly 32 bytes.
- `current` is present in `keys`.

A fail on any of these aborts the load. The loader keeps the previous
in-memory map on a reload failure and emits a metric/log; on initial load
failure the service exits.

### MongoDB collection `room_data_keys`

```go
type RoomDataKey struct {
    ID         string    `bson:"_id"`         // roomID
    WrappedDEK []byte    `bson:"wrappedDEK"`  // AES-GCM ciphertext + 16-byte tag
    WrapNonce  []byte    `bson:"wrapNonce"`   // 12 bytes, GCM nonce for the wrap
    KEKVersion int       `bson:"kekVersion"`  // KEK version that wrapped this DEK
    CreatedAt  time.Time `bson:"createdAt"`
}
```

One row per room. `_id = roomID`, so DEK creation is naturally idempotent
via `$setOnInsert`. Rows are never deleted in normal operation; KEK
rotation rewrites `WrappedDEK`, `WrapNonce`, and `KEKVersion` in place via
`Replace`, leaving `_id` and `CreatedAt` unchanged.

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

Two new columns on each of the three message tables (`messages_by_room`,
`messages_by_id`, `thread_messages_by_room`):

```cql
ALTER TABLE messages_by_room ADD enc_payload blob;
ALTER TABLE messages_by_room ADD enc_meta    frozen<enc_meta>;
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
    atrest.go            // public types: EncryptedFields, EncMeta, RoomDataKey, errors
    kek_loader.go        // KEKLoader implementation with fsnotify
    kek_loader_test.go
    dek_store.go         // DEKStore interface + Mongo implementation
    dek_store_test.go
    cipher.go            // Cipher implementation with cache
    cipher_test.go
    cache.go             // unexported LRU
    integration_test.go  // testcontainers Mongo + real fsnotify
    testdata/
```

### KEKLoader

```go
type KEKLoader interface {
    Current() (version int, key []byte)
    ByVersion(v int) ([]byte, bool)
    Close() error
}
```

- Construction reads the file once and validates. Failure is fatal at
  startup.
- `Watch` (started internally) uses `fsnotify` to re-read on file
  modification. On a successful reload the in-memory map is swapped
  atomically. On a failed reload the previous map is retained and a metric
  is incremented.
- Returned key bytes must not be mutated by callers.

### DEKStore

```go
type DEKStore interface {
    Get(ctx context.Context, roomID string) (*RoomDataKey, error)  // nil if absent
    Upsert(ctx context.Context, key RoomDataKey) error              // $setOnInsert semantics
    Replace(ctx context.Context, key RoomDataKey) error             // for KEK rotation
}
```

The Mongo implementation uses:

- `Get`: `FindOne({_id: roomID})`, returns `(nil, nil)` on
  `mongo.ErrNoDocuments`.
- `Upsert`: `UpdateOne({_id: roomID}, {$setOnInsert: {...}}, upsert=true)`.
  Race-safe: concurrent first writers all converge on the row inserted by
  the winner.
- `Replace`: `ReplaceOne({_id: roomID}, full doc)`. Used by the rotation
  worker.

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

1. Look up unwrapped DEK in cache by `roomID`.
2. On cache miss: `DEKStore.Get`. If absent, generate a new 32-byte DEK,
   wrap it with the current KEK, `DEKStore.Upsert`. If `Upsert` indicates a
   concurrent insert won the race, re-`Get` and use that wrapped DEK.
   Unwrap with `KEKLoader.ByVersion(kekVersion)`. Cache the unwrapped DEK.
3. Marshal `fields` to JSON.
4. Encrypt with AES-256-GCM under the unwrapped DEK using a fresh 12-byte
   random nonce.
5. Return `(ciphertext, EncMeta{Nonce: nonce})`.

Decrypt logic:

1. Look up unwrapped DEK in cache (same as encrypt).
2. Decrypt `encPayload` with AES-256-GCM under the unwrapped DEK using
   `encMeta.Nonce`. GCM auth tag failure returns a typed error.
3. Unmarshal JSON into `EncryptedFields`. Return.

### DEK cache

- Unexported, owned by the Cipher implementation.
- LRU keyed by `roomID` holding the unwrapped DEK bytes.
- Capacity from `ATREST_DEK_CACHE_SIZE` (default 10_000).
- TTL from `ATREST_DEK_CACHE_TTL` (default 1h). Since the DEK bytes never
  change for the lifetime of a room, TTL is not required for correctness;
  it exists only as a defensive bound on memory retention for cold rooms
  in long-lived processes. LRU eviction is the primary cap.
- KEK rotation does not invalidate cache entries — it changes only the
  wrap stored in MongoDB; the unwrapped DEK is unchanged.

### Errors

Typed sentinel errors exported from the package, including:

- `ErrKEKVersionUnknown` — wrap references a KEK version not in the
  loaded set.
- `ErrAuthFailed` — GCM tag mismatch on decrypt (tampering or wrong key).
- `ErrPayloadMalformed` — JSON unmarshal failure after decrypt.

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

Both message-worker and history-service get an identical config block
parsed via `caarlos0/env`:

```go
type AtrestConfig struct {
    Enabled       bool          `env:"ATREST_ENABLED"        envDefault:"true"`
    KEKFile       string        `env:"ATREST_KEK_FILE"       envDefault:"/etc/chat/keks.json"`
    DEKCacheSize  int           `env:"ATREST_DEK_CACHE_SIZE" envDefault:"10000"`
    DEKCacheTTL   time.Duration `env:"ATREST_DEK_CACHE_TTL"  envDefault:"1h"`
}
```

When `Enabled=false`, the cipher is wired as a no-op pass-through that
takes the legacy write/read path. Tests that do not exercise crypto can
either inject a no-op cipher or set `ATREST_ENABLED=false` in the test
environment.

A misconfigured deploy with `Enabled=true` and a missing or invalid
`keks.json` fails closed: the service exits at startup. This is the
intended behavior — better than silently writing plaintext.

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
5. Provision the k8s Secret in each environment (one entry, version `1`)
   and the volume mount. Confirm new messages are stored encrypted in
   staging Cassandra.
6. (Future, separate spec.) Backfill of legacy plaintext rows.

## KEK rotation

Operational procedure (no code in this spec besides the optional rotation
worker):

1. Add a new key version to the k8s Secret, leaving `current` at the old
   version. Wait for file watchers to pick it up across all pods.
2. Flip `current` to the new version. Subsequent DEK creations and
   re-wraps use the new version.
3. Run the rotation worker (one-shot) to re-wrap existing DEK rows: scan
   `room_data_keys` for `kekVersion != current`, unwrap with the prior
   KEK, re-wrap with the current KEK, call `DEKStore.Replace`. Idempotent
   and resumable.
4. Once all rows are re-wrapped, the old KEK version may be removed from
   the Secret.

The rotation worker is a small `cmd/atrest-rotator` binary at the repo
root, deployable as a Kubernetes Job. It is operational rather than core
to the at-rest goal and may be landed as a follow-up PR after step 4 of
the rollout.

## Observability

- **Tracing:** `Cipher.Encrypt` and `Cipher.Decrypt` are spans with
  attributes `roomID`, `dekCacheHit`, and payload bytes in/out. Plaintext
  is never recorded on a span.
- **Metrics (Prometheus):**
  - `atrest_encrypt_total{result}` and `atrest_decrypt_total{result}`
  - `atrest_dek_cache_hits_total`, `atrest_dek_cache_misses_total`
  - `atrest_dek_creations_total`
  - `atrest_kek_reload_total{result}`
  - `atrest_kek_current_version` (gauge)
- **Logs:** structured `log/slog`. Plaintext content, full DEK bytes, and
  full KEK bytes are never logged. Decrypt failures log `roomID`,
  `messageID`, `kekVersion`, and the error class.

## Testing

### `pkg/atrest` unit tests

- **KEKLoader:** valid file, malformed JSON, missing `current`, wrong-
  length keys, hot reload via `fsnotify` simulated by writing to a temp
  file, reload with invalid contents leaves prior map intact.
- **Cipher** (with fakes for KEKLoader and DEKStore): round-trip
  encrypt/decrypt, missing-DEK lazy creation, KEK-version-not-found,
  tampered ciphertext rejected (`ErrAuthFailed`), cache hit reuses
  unwrapped DEK without re-fetching from store.

### `pkg/atrest` integration tests (`//go:build integration`)

- testcontainers MongoDB. End-to-end round-trip with real Mongo.
- Concurrent first-write race: N goroutines call `Encrypt` for the same
  roomID; assert exactly one DEK row exists and all goroutines decrypt
  each other's output.
- KEK rotation: write rows under KEK v1, run the rotation worker, verify
  rows still decrypt and that `room_data_keys` now show `kekVersion=2`.

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
- Coverage must include error paths: GCM auth failure, KEK version
  unknown, DEK store errors, malformed payload after decrypt.

## Risks

- **Lost KEK = unrecoverable data.** The KEK file is the root of the
  encryption hierarchy; losing all copies of all KEK versions makes every
  DEK — and therefore every encrypted message — permanently unreadable.
  Mitigation: KEK material is stored as a Kubernetes Secret backed by the
  cluster's secret-management runbook, which owns its backup story. This
  spec calls the invariant out as the single most important operational
  property.
- **Default-on means a misconfigured deploy fails closed.** A pod with
  `ATREST_ENABLED=true` and a missing or invalid `keks.json` exits at
  startup. This is intentional: failing closed is preferable to silently
  storing plaintext.
- **DEK cache thrash under high room cardinality.** With more active
  rooms than the cache size, every encrypt/decrypt round-trips to Mongo
  and unwraps. Mitigation: capacity is configurable; metrics on hit/miss
  rates expose the regime so it can be tuned.
- **Search index plaintext exposure.** Until the future search-index
  encryption spec lands, the search backend retains a plaintext copy of
  message content. The deployment must treat the search index as
  equivalently sensitive to Cassandra during this period.
