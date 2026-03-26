# Room Encryption Library — Design Spec

**Date:** 2026-03-26
**Status:** Approved

---

## Overview

A shared Go library at `pkg/roomcrypto/` that encrypts room messages on the server side. The server encodes; clients (JavaScript, Swift) decode. Encryption is asymmetric: each room has a P-256 key pair stored in MongoDB. This spec covers only the `Encode` function — the initial scope.

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
type EncryptedMessage struct {
    EphemeralPublicKey []byte `json:"ephemeralPublicKey"` // 65 bytes, uncompressed P-256 point
    Nonce              []byte `json:"nonce"`              // 12 bytes, AES-GCM nonce
    Ciphertext         []byte `json:"ciphertext"`         // encrypted content + AES-GCM tag
}

// Encode encrypts content using the room's P-256 public key.
// roomPublicKey is the uncompressed point (65 bytes) as stored in MongoDB.
func Encode(content string, roomPublicKey []byte) (*EncryptedMessage, error)
```

`[]byte` fields on `EncryptedMessage` marshal to base64 strings in JSON automatically, which JavaScript and Swift can consume without transformation.

---

## Algorithm and Data Flow

Each call to `Encode` executes the following steps:

1. **Parse** the room public key — `ecdh.P256().NewPublicKey(roomPublicKey)` — fail immediately if invalid.
2. **Generate** an ephemeral P-256 key pair — `ecdh.P256().GenerateKey(rand.Reader)`.
3. **ECDH** — `ephemeralPrivKey.ECDH(roomPublicKey)` → 32-byte shared secret.
4. **HKDF** — derive a 32-byte AES key:
   ```
   HKDF-SHA256(secret=sharedSecret, salt=nil, info=[]byte("room-message-encryption"))
   ```
   The `info` string provides domain separation so the same shared secret cannot be reused for a different purpose.
5. **Nonce** — 12 random bytes from `crypto/rand`.
6. **AES-256-GCM** — `cipher.NewGCM(aes.NewCipher(aesKey))`, seal with nonce, no additional data.
7. **Return** `EncryptedMessage{ephemeralPubKey.Bytes(), nonce, ciphertext}`.

The ephemeral key is regenerated on every call, so two encryptions of identical content produce different ciphertexts. The client recovers the plaintext by performing ECDH between their room private key and the ephemeral public key, re-deriving the AES key via HKDF, and decrypting with AES-256-GCM.

### Dependencies

All crypto primitives come from the Go standard library and `golang.org/x/crypto`, which is already present in `go.mod` as an indirect dependency:

| Primitive | Package |
|-----------|---------|
| P-256 ECDH | `crypto/ecdh` (stdlib) |
| AES-GCM | `crypto/aes`, `crypto/cipher` (stdlib) |
| HKDF-SHA256 | `golang.org/x/crypto/hkdf` |
| Randomness | `crypto/rand` (stdlib) |

---

## Error Handling

`Encode` returns a non-nil error in three cases, each wrapped with context:

| Failure point | Wrapped message |
|---|---|
| Invalid public key bytes | `"parsing room public key: %w"` |
| Ephemeral key generation failure | `"generating ephemeral key: %w"` |
| Nonce generation failure | `"generating nonce: %w"` |

AES cipher/GCM setup errors are prevented by ensuring HKDF always outputs exactly 32 bytes. No raw crypto internals are surfaced to callers.

---

## Testing

All tests are in `pkg/roomcrypto/roomcrypto_test.go`, `package roomcrypto`. No external services are required.

### Table-driven tests for `Encode`

| Scenario | Input | Expected |
|---|---|---|
| Happy path | valid 65-byte P-256 public key, non-empty content | no error; `EphemeralPublicKey` is 65 bytes, `Nonce` is 12 bytes, `Ciphertext` is non-empty |
| Empty content | valid key, `""` | no error; valid ciphertext (AES-GCM tag alone) |
| Invalid key — wrong length | 32 random bytes | error wrapping parse failure |
| Invalid key — invalid curve point | 65 zero bytes | error wrapping parse failure |

### Round-trip property test

Generate a real P-256 key pair inside the test, call `Encode`, then manually perform the ECDH + HKDF + AES-256-GCM decrypt steps and assert the recovered plaintext equals the original. This verifies correctness end-to-end without requiring a `Decode` function.

### Ephemeral key randomness test

Call `Encode` twice with identical inputs and assert that `EphemeralPublicKey` differs between the two results.

### Coverage target

90%+ in line with the project's core library standard.
