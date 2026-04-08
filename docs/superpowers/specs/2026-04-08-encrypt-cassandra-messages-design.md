# Encrypt Cassandra Message Content

## Overview

Add transparent application-level encryption at rest for message content stored in Cassandra. Only the `msg`/`content` column is encrypted; all metadata (room_id, created_at, sender, etc.) remains plaintext. The application servers hold the keys and encrypt/decrypt transparently — Cassandra never sees plaintext message content.

## Threat Model

Protect against unauthorized access to raw Cassandra data (disk theft, unauthorized DB admin, backup exposure). Application servers are trusted — they hold the encryption keys and perform encrypt/decrypt on every write/read.

## Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Scope | `msg`/`content` column only | Metadata is not sensitive enough to warrant encryption overhead |
| Key management | Per-room symmetric keys in Valkey | Isolation — compromised key exposes one room only |
| Key separation | Separate from `roomkeystore` E2E keys | Different threat models, independent rotation schedules |
| Algorithm | AES-256-GCM (symmetric) | Right primitive for server-side encrypt/decrypt; hardware-accelerated |
| Key rotation strategy | Encrypt-on-write only | Old messages stay encrypted with their original key version; old versions retained |
| Storage format | Base64-encoded text with prefix | No Cassandra schema change; backward-compatible with plaintext |
| Approach | Shared `pkg/` library (Approach A) | Centralized logic, independently testable, matches project conventions |

## Architecture

### New Packages

#### `pkg/msgkeystore` — DB Encryption Key Store

Mirrors `pkg/roomkeystore` pattern with a separate Valkey key namespace.

- **Key type:** Single 32-byte AES-256 symmetric key per room (not a key pair)
- **Valkey key pattern:** `room:<roomID>:dbkey` (current), `room:<roomID>:dbkey:prev` (previous, with grace period TTL)
- **Versioning:** Version 0 on initial `Set`, incremented on `Rotate`, old version retained with configurable grace period
- **Same Valkey instance** as `roomkeystore`, different namespace

```go
type KeyStore interface {
    Set(ctx context.Context, roomID string, key []byte) (int, error)
    Get(ctx context.Context, roomID string) (*VersionedKey, error)
    GetByVersion(ctx context.Context, roomID string, version int) ([]byte, error)
    Rotate(ctx context.Context, roomID string, newKey []byte) (int, error)
    Delete(ctx context.Context, roomID string) error
}

type VersionedKey struct {
    Version int
    Key     []byte // 32 bytes, AES-256
}
```

Internal implementation uses the same `hashCommander` pattern as `roomkeystore` for testability.

#### `pkg/msgcrypto` — Encrypt/Decrypt Library

Symmetric AES-256-GCM encryption for message content, using keys from `msgkeystore`.

```go
type Encryptor struct {
    keys msgkeystore.KeyStore
}

func NewEncryptor(keys msgkeystore.KeyStore) *Encryptor

func (e *Encryptor) Encrypt(ctx context.Context, roomID, content string) (string, error)
func (e *Encryptor) Decrypt(ctx context.Context, roomID, ciphertext string) (string, error)
```

**Encrypt flow:**
1. Fetch current key + version from `msgkeystore` for the room
2. Generate random 12-byte nonce
3. AES-256-GCM encrypt the content
4. Return: `enc:v<version>:<base64(nonce + ciphertext + tag)>`

**Decrypt flow:**
1. Parse the prefix — if no `enc:` prefix, return content as-is (plaintext passthrough for pre-encryption messages)
2. Extract version number from `v<N>`
3. Fetch key by version from `msgkeystore`
4. Base64-decode, split nonce (first 12 bytes) from ciphertext+tag
5. AES-256-GCM decrypt and return plaintext

### Service Integration

#### message-worker (write path)

- Wire `msgkeystore` and `msgcrypto.Encryptor` in `main.go`
- In `handler.go` `processMessage`: call `encryptor.Encrypt(ctx, msg.RoomID, msg.Content)` before passing to `store.SaveMessage`
- Store layer unchanged — receives an already-encrypted string, writes to `content` column
- Encryption failure → message NACKed and retried

#### history-service (read path)

- Wire `msgkeystore` and `msgcrypto.Encryptor` in `cmd/main.go`
- Inject `Encryptor` into `HistoryService`
- In `service/messages.go`, after each query returns messages, call `encryptor.Decrypt(ctx, msg.RoomID, msg.Msg)` on each message's `Msg` field
- Applies to all 4 handlers: `LoadHistory`, `LoadNextMessages`, `LoadSurroundingMessages`, `GetMessageByID`
- Pre-encryption plaintext messages pass through `Decrypt` unmodified

#### broadcast-worker (no changes)

Gets message content from the NATS event, never reads Cassandra — unaffected.

#### Key provisioning (out of scope)

Room DB encryption keys must exist before messages can be encrypted. Key provisioning (calling `msgkeystore.Set` when a room is created) is a separate task — this spec covers only the encrypt/decrypt path assuming keys already exist. For development and testing, keys are created as part of test setup.

## Error Handling

| Scenario | Behavior |
|----------|----------|
| Missing key on encrypt | Error returned, message NACKed and retried. Log `slog.Error`. Fatal config issue — room should have a key from creation. |
| Missing key version on decrypt | Error returned to client. Log `slog.Error`. Rare — DB key rotation is infrequent and grace periods are long. |
| Plaintext message (no `enc:` prefix) | `Decrypt` returns content as-is. No error, no log. Expected for pre-encryption messages. |
| Malformed ciphertext | `Decrypt` returns error (bad base64, wrong length, GCM auth failure). Error returned to client. |

## Performance

- **AES-256-GCM** is hardware-accelerated on modern CPUs (AES-NI). Negligible latency for chat-sized messages.
- **Valkey round-trips:** One per encrypt call. For history-service batch reads (up to 100 messages per page), all messages in a query share the same room — fetch the key once per request, reuse for all messages in the batch.
- **Storage overhead:** ~33% from base64 encoding on the ciphertext. Negligible for typical chat message sizes.

## Backward Compatibility

- Pre-encryption plaintext messages remain readable — `Decrypt` passes through content without the `enc:` prefix
- No Cassandra schema migration required — encrypted content is a text string in the existing column
- Gradual rollout: once deployed, new messages are encrypted; old messages continue to work

## Testing Strategy

All unit tests, no new integration tests needed.

### `pkg/msgkeystore`

- Fake `hashCommander` (same pattern as `roomkeystore`)
- Test `Set`, `Get`, `GetByVersion`, `Rotate`, `Delete`
- Test versioning logic, grace period, missing key returns

### `pkg/msgcrypto`

- Mock `KeyStore` interface
- Encrypt → Decrypt round-trip produces original content
- Plaintext passthrough (no `enc:` prefix)
- Wrong key version returns error
- Malformed ciphertext (bad base64, truncated nonce, tampered ciphertext) returns error
- Prefix format correctness (`enc:v<N>:<base64>`)
- Deterministic `io.Reader` for nonce generation to enable exact ciphertext assertions

### `message-worker`

- Mock `Store` and encrypt function
- `processMessage` calls encrypt before `SaveMessage`
- Encryption failure causes NACK
- Store failure after successful encryption causes NACK

### `history-service`

- Mock `MessageRepository`, `SubscriptionRepository`, and decrypt function
- Each handler decrypts `Msg` field on returned messages
- Plaintext messages pass through unchanged
- Decryption failure returns error to client
