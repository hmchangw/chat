# TypeScript Decrypt Integration Test — Design Spec

**Date:** 2026-03-26
**Status:** Approved

---

## Overview

An integration test that verifies Go's `pkg/roomcrypto.Encode` produces ciphertext that a TypeScript client can correctly decrypt. The test runs under `//go:build integration`, uses `testcontainers-go` to spin up a `node:20-alpine` container, and executes a TypeScript decrypt script inside it using `tsx`.

---

## Files

```
pkg/roomcrypto/
  integration_test.go        — Go integration test (//go:build integration)
  testdata/
    decrypt.ts               — TypeScript decrypt script (Web Crypto API, no npm deps)
```

---

## Test Flow

1. Start a `node:20-alpine` testcontainer. Register `t.Cleanup` to terminate it.
2. Install `tsx` in the container: `npm install -g tsx --quiet`.
3. Copy `testdata/decrypt.ts` into the container once at `/decrypt.ts`.
4. For each test case (table-driven, two cases — see Testing section):
   a. Generate a fresh P-256 key pair inside the subtest.
   b. Call `Encode(content, pubKeyBytes)` → `EncryptedMessage`.
   c. Serialise a JSON payload (see Payload Format).
   d. Write the payload to a temp file via `t.TempDir()`.
   e. Copy the temp file into the container at `/payload.json`.
   f. Execute `tsx /decrypt.ts /payload.json` in the container.
   g. Assert exit code is 0.
   h. Assert stdout (trimmed) equals the original `content`.

The container is started once per test function and reused across subtests to avoid repeating the `npm install` cost.

---

## Payload Format

The Go test serialises this struct to JSON and writes it to a temp file:

```go
type decryptPayload struct {
    PrivateKey string            `json:"privateKey"` // base64(privKey.Bytes()) — 32-byte P-256 scalar
    PublicKey  string            `json:"publicKey"`  // base64(pubKey.Bytes())  — 65-byte uncompressed point
    Message    *EncryptedMessage `json:"message"`    // EncryptedMessage JSON (base64 fields)
}
```

`EncryptedMessage` already marshals its `[]byte` fields to base64 automatically.

---

## TypeScript Decrypt Script

The script receives the payload file path as `process.argv[2]`, reads and parses it, and decrypts using the Web Crypto API (`crypto.subtle`) built into Node.js 18+. No npm packages are required beyond `tsx`.

### Key Import: Raw Bytes → JWK

Go's `privKey.Bytes()` returns a raw 32-byte P-256 scalar. Web Crypto cannot import raw private key scalars directly — JWK format is required. The script constructs the JWK from the raw bytes:

```
d = base64url(privateKey[0:32])    ← raw scalar
x = base64url(publicKey[1:33])     ← X coordinate (uncompressed point, 0x04 prefix stripped)
y = base64url(publicKey[33:65])    ← Y coordinate
```

### Decrypt Steps

1. **Import room private key** — `importKey('jwk', { kty:'EC', crv:'P-256', d, x, y }, { name:'ECDH', namedCurve:'P-256' }, false, ['deriveBits'])`
2. **Import ephemeral public key** — same as above but without `d`, from `message.ephemeralPublicKey` (base64-decoded, then split into x/y)
3. **ECDH shared secret** — `deriveBits({ name:'ECDH', public: ephPubKey }, roomPrivKey, 256)` → 32-byte `ArrayBuffer`
4. **Import shared secret as HKDF key** — `importKey('raw', sharedSecret, 'HKDF', false, ['deriveKey'])`
5. **HKDF-SHA256** — `deriveKey({ name:'HKDF', hash:'SHA-256', salt: new Uint8Array(0), info: new TextEncoder().encode('room-message-encryption') }, hkdfKey, { name:'AES-GCM', length:256 }, false, ['decrypt'])`
6. **AES-256-GCM decrypt** — `decrypt({ name:'AES-GCM', iv: nonce }, aesKey, ciphertext)` where `ciphertext` includes the 16-byte GCM tag appended by Go's `Seal`; AAD is omitted (matches Go's `nil` AAD)
7. **Output** — `process.stdout.write(new TextDecoder().decode(plaintext))`

Parameters match Go exactly: `salt=new Uint8Array(0)` (nil), `info="room-message-encryption"`, no AAD.

### Error Handling

A top-level `try/catch` wraps all async logic. On error: print to `process.stderr` and `process.exit(1)`. The Go test will see a non-zero exit code and the combined stdout/stderr is included in the test failure message for diagnosis.

---

## Go Integration Test

**File:** `pkg/roomcrypto/integration_test.go`
**Build tag:** `//go:build integration`
**Package:** `package roomcrypto`

### Container Setup

```go
func setupNodeContainer(t *testing.T) testcontainers.Container {
    t.Helper()
    ctx := context.Background()
    container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: testcontainers.ContainerRequest{
            Image: "node:20-alpine",
            Cmd:   []string{"sh", "-c", "sleep 600"},
        },
        Started: true,
    })
    // require.NoError, t.Cleanup(Terminate), copy script, install tsx
    ...
    return container
}
```

### Test Cases

| Case | `content` | Expected stdout |
|---|---|---|
| non-empty message | `"hello from Go"` | `"hello from Go"` |
| empty string | `""` | `""` |

### Assertions

- `require.NoError` on all container API calls
- `require.Equal(t, 0, exitCode)` — non-zero exit includes output in failure message
- `assert.Equal(t, tc.content, strings.TrimRight(stdout, "\n"))` — trim trailing newline only (not all whitespace, to preserve the empty-string case)

---

## Error Handling

| Failure point | How caught |
|---|---|
| Container startup failure | `require.NoError` — test stops immediately |
| `CopyFileToContainer` failure | `require.NoError` |
| `tsx` install failure (non-zero exit from npm) | Checked via exit code of the setup exec |
| `Exec` API error | `require.NoError` |
| Non-zero exit from decrypt script | `require.Equal(t, 0, exitCode)` with output in failure message |
| Plaintext mismatch | `assert.Equal` |

---

## Testing

Run with:

```bash
make test-integration SERVICE=pkg/roomcrypto
```

No additional infrastructure or config needed. Follows the same `testcontainers-go` pattern as the existing integration tests in the repo (`message-worker`, `room-worker`, etc.).
