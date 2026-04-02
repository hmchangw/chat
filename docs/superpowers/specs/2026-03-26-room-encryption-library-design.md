# Room Encryption Library — Design Spec

**Date:** 2026-03-26
**Status:** Approved

---

## Overview

A shared Go library at `pkg/roomcrypto/` that encrypts room messages on the server side. The server encodes; clients (JavaScript, Swift) decode. Encryption is asymmetric: each room has a P-256 key pair stored in Valkey. This spec covers only the `Encode` function — the initial scope.

---

## Out of Scope / Future Work

- **Server-side `Decode`** is not implemented. `EncryptedMessage` is consumed by clients only. If a Go service ever requires decryption, a follow-up spec must be filed before implementing it in this package.
- Room key pair generation and storage are out of scope for this library.

---

## Package Structure

```
pkg/roomcrypto/
  roomcrypto.go       — EncryptedMessage type + Encode function
  roomcrypto_test.go  — unit tests
```

---

## API Surface

```go
package roomcrypto

// EncryptedMessage holds the output of Encode.
// []byte fields marshal to base64 in JSON automatically.
//
// Note: this struct uses only json tags (no bson tags) because it is a
// serialisation-only type sent to clients over JSON, not persisted to a database.
type EncryptedMessage struct {
    EphemeralPublicKey []byte `json:"ephemeralPublicKey"` // 65 bytes, uncompressed P-256 point
    Nonce              []byte `json:"nonce"`              // 12 bytes, AES-GCM nonce
    Ciphertext         []byte `json:"ciphertext"`         // encrypted content + 16-byte AES-GCM tag
}

// Encode encrypts content using the room's P-256 public key.
// roomPublicKey is the uncompressed point (65 bytes).
func Encode(content string, roomPublicKey []byte) (*EncryptedMessage, error)
```

`[]byte` fields on `EncryptedMessage` marshal to base64 strings in JSON automatically, which JavaScript and Swift can consume without transformation. `EncryptedMessage` is a crypto result type — it is serialised and sent to clients, not persisted to a database, so `bson` tags are not required.

---

## Algorithm and Data Flow

Each call to `Encode` executes the following steps:

1. **Parse** the room public key — `roomPubKey, err := ecdh.P256().NewPublicKey(roomPublicKey)` — fail immediately if invalid. `roomPubKey` is the parsed `*ecdh.PublicKey`; the raw `roomPublicKey []byte` parameter is not used again.
2. **Generate** an ephemeral P-256 key pair — `ephemeralPrivKey, err := ecdh.P256().GenerateKey(rand.Reader)`.
3. **ECDH** — `ephemeralPrivKey.ECDH(roomPubKey)` → 32-byte shared secret. Note: the parsed `*ecdh.PublicKey` (`roomPubKey`) is passed, not the raw bytes.
4. **HKDF** — derive a 32-byte AES key:
   ```
   reader := hkdf.New(sha256.New, sharedSecret, nil, []byte("room-message-encryption"))
   io.ReadFull(reader, aesKey[:])
   ```
   The `info` string provides domain separation so the same shared secret cannot be reused for a different purpose. The error from `io.ReadFull` must be checked and wrapped per project convention; in practice it is unreachable for SHA-256, but must not be silently ignored.
5. **AES-256-GCM setup** — `cipher.NewGCM(aes.NewCipher(aesKey))`.
6. **Nonce** — 12 random bytes from `crypto/rand`.
7. **Seal** — seal with nonce. Additional data (AAD) is intentionally `nil` — see note below. Go's `Seal` appends the 16-byte GCM authentication tag to the ciphertext.
8. **Return** `EncryptedMessage{ephemeralPrivKey.PublicKey().Bytes(), nonce, ciphertext}`.

**Note on AAD = nil:** The GCM tag authenticates the ciphertext but not the surrounding message metadata (room ID, sender, message ID). This is a conscious trade-off: clients do not have access to stable, canonical per-message metadata at decrypt time (the encrypted payload is self-contained and metadata may vary by transport). The `nil` AAD must be used consistently on both sides. Future work may revisit this if a stable binding context becomes available.

The ephemeral key is regenerated on every call, so two encryptions of identical content produce different ciphertexts. The client recovers the plaintext by performing ECDH between their room private key and the ephemeral public key, re-deriving the AES key via HKDF with the same `info` string, and calling `gcm.Open(nil, nonce, ciphertext, nil)`.

### Dependencies

`pkg/roomcrypto` imports `golang.org/x/crypto/hkdf` directly. This promotes it from an indirect to a direct dependency in `go.mod`. Running `go mod tidy` as part of the implementation step is required. This spec serves as the explicit approval for that direct dependency addition (per the project's "ask before adding dependencies" rule).

| Primitive | Package |
|-----------|---------|
| P-256 ECDH | `crypto/ecdh` (stdlib) |
| AES-GCM | `crypto/aes`, `crypto/cipher` (stdlib) |
| HKDF-SHA256 | `golang.org/x/crypto/hkdf` (promote from indirect) |
| Randomness | `crypto/rand` (stdlib) |

---

## Error Handling

`Encode` returns a non-nil error in six cases, each wrapped with context:

| Step | Failure point | Wrapped message |
|---|---|---|
| 1 | Invalid public key bytes | `"parsing room public key: %w"` |
| 2 | Ephemeral key generation failure | `"generating ephemeral key: %w"` |
| 4 | HKDF key derivation failure (`io.ReadFull`) | `"deriving AES key: %w"` — unreachable for SHA-256, must be checked per project convention |
| 5 | AES cipher creation (`aes.NewCipher`) | `"creating AES cipher: %w"` — unreachable for 32-byte key, must be checked per project convention |
| 5 | GCM wrapper creation (`cipher.NewGCM`) | `"creating GCM wrapper: %w"` — same rationale |
| 6 | Nonce generation failure | `"generating nonce: %w"` |

No raw crypto internals are surfaced to callers.

---

## Testing

All tests are in `pkg/roomcrypto/roomcrypto_test.go`, `package roomcrypto`. No external services are required.

### Table-driven tests for `Encode`

| Scenario | Input | Expected |
|---|---|---|
| Happy path | valid 65-byte P-256 public key, non-empty content | no error; `EphemeralPublicKey` is 65 bytes, `Nonce` is 12 bytes, `Ciphertext` is non-empty |
| Empty content | valid key, `""` | no error; valid ciphertext (16-byte GCM tag, content length zero) |
| Invalid key — wrong length | 32 random bytes | error wrapping parse failure |
| Invalid key — invalid curve point | 65 zero bytes | error wrapping parse failure |

### Round-trip property test

Generate a real P-256 key pair inside the test using `ecdh.P256().GenerateKey(rand.Reader)`. Retain the `*ecdh.PrivateKey` for the decrypt steps; extract the public key bytes via `privKey.PublicKey().Bytes()` and pass them to `Encode`. Run this for both a non-empty string and `""`. The decrypt steps are:

1. Parse `EncryptedMessage.EphemeralPublicKey` via `ecdh.P256().NewPublicKey(...)` → `ephPubKey`
2. `roomPrivateKey.ECDH(ephPubKey)` → shared secret
3. `HKDF-SHA256(secret, salt=nil, info=[]byte("room-message-encryption"))` via `io.ReadFull` → 32-byte AES key
4. `gcm.Open(nil, EncryptedMessage.Nonce, EncryptedMessage.Ciphertext, nil)` → plaintext

`EncryptedMessage.Ciphertext` already includes the 16-byte GCM authentication tag appended by `Seal`; pass it directly to `Open`. AAD is `nil` on both encrypt and decrypt sides.

### Non-determinism test

Call `Encode` twice with identical inputs and assert:

1. `result1.EphemeralPublicKey` differs from `result2.EphemeralPublicKey`
2. `result1.Nonce` differs from `result2.Nonce`
3. `result1.Ciphertext` differs from `result2.Ciphertext` (direct symptom of nonce reuse)
4. Within a single result, `Nonce` does not equal the first 12 bytes of `EphemeralPublicKey`: `!bytes.Equal(result.Nonce, result.EphemeralPublicKey[:12])` — specifically guards against nonce being naively derived as a truncation of the ephemeral public key (this check does not cover all derivation bugs, but catches the most obvious one)

Nonce reuse is a critical AES-GCM failure mode; all four assertions are required.

### Coverage target

90%+ in line with the project's core library standard.
