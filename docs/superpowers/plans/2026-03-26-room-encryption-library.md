# Room Encryption Library Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `pkg/roomcrypto` with an `Encode` function that encrypts a room message using ECDH P-256 + HKDF-SHA256 + AES-256-GCM hybrid encryption.

**Architecture:** Each call generates a fresh ephemeral P-256 key pair, performs ECDH with the room's stored public key, derives a 32-byte AES key via HKDF-SHA256, and encrypts the message with AES-256-GCM. The output (`EncryptedMessage`) carries the ephemeral public key, nonce, and ciphertext — all needed by JS/Swift clients to decrypt.

**Tech Stack:** `crypto/ecdh`, `crypto/aes`, `crypto/cipher`, `crypto/rand` (stdlib); `golang.org/x/crypto/hkdf` (already in `go.mod` as indirect; promoted to direct via `go mod tidy`).

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `pkg/roomcrypto/roomcrypto.go` | **Create** | `EncryptedMessage` type + `Encode` function |
| `pkg/roomcrypto/roomcrypto_test.go` | **Create** | All unit tests (table-driven, round-trip, non-determinism) |
| `go.mod` / `go.sum` | **Modify** | Promote `golang.org/x/crypto` from indirect to direct |

---

## Task 1: Write Failing Tests (Red)

**Files:**
- Create: `pkg/roomcrypto/roomcrypto_test.go`

- [ ] **Step 1.1: Create the test file**

Create `pkg/roomcrypto/roomcrypto_test.go` with the full content below. The package (`roomcrypto`) does not exist yet — the tests will fail to compile, which is the expected Red state.

```go
package roomcrypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/hkdf"
)

func TestEncode(t *testing.T) {
	privKey, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	validPubKey := privKey.PublicKey().Bytes()

	tests := []struct {
		name        string
		content     string
		pubKey      []byte
		wantErr     bool
		errContains string
	}{
		{
			name:    "happy path",
			content: "hello, world",
			pubKey:  validPubKey,
		},
		{
			name:    "empty content",
			content: "",
			pubKey:  validPubKey,
		},
		{
			name:        "invalid key - wrong length",
			content:     "hello",
			pubKey:      make([]byte, 32),
			wantErr:     true,
			errContains: "parsing room public key",
		},
		{
			name:        "invalid key - invalid curve point",
			content:     "hello",
			pubKey:      make([]byte, 65), // 65 zero bytes — not a valid P-256 point
			wantErr:     true,
			errContains: "parsing room public key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Encode(tt.content, tt.pubKey)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Len(t, result.EphemeralPublicKey, 65)
			assert.Len(t, result.Nonce, 12)
			assert.NotEmpty(t, result.Ciphertext)
		})
	}
}

func TestEncode_RoundTrip(t *testing.T) {
	// Retain the private key — it is used to decrypt in the steps below.
	privKey, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubKeyBytes := privKey.PublicKey().Bytes()

	cases := []struct {
		name    string
		content string
	}{
		{name: "non-empty", content: "hello, world"},
		{name: "empty string", content: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := Encode(tc.content, pubKeyBytes)
			require.NoError(t, err)

			// Step 1: parse the ephemeral public key from the EncryptedMessage
			ephPubKey, err := ecdh.P256().NewPublicKey(msg.EphemeralPublicKey)
			require.NoError(t, err)

			// Step 2: ECDH with the room private key and the ephemeral public key
			sharedSecret, err := privKey.ECDH(ephPubKey)
			require.NoError(t, err)

			// Step 3: re-derive the AES key using the same HKDF parameters
			aesKey := make([]byte, 32)
			hkdfReader := hkdf.New(sha256.New, sharedSecret, nil, []byte("room-message-encryption"))
			_, err = io.ReadFull(hkdfReader, aesKey)
			require.NoError(t, err)

			// Step 4: decrypt with AES-256-GCM; AAD is nil on both sides
			block, err := aes.NewCipher(aesKey)
			require.NoError(t, err)
			gcm, err := cipher.NewGCM(block)
			require.NoError(t, err)

			// msg.Ciphertext includes the 16-byte GCM tag appended by Seal — pass it directly
			plaintext, err := gcm.Open(nil, msg.Nonce, msg.Ciphertext, nil)
			require.NoError(t, err)

			assert.Equal(t, tc.content, string(plaintext))
		})
	}
}

func TestEncode_NonDeterminism(t *testing.T) {
	privKey, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubKeyBytes := privKey.PublicKey().Bytes()

	r1, err := Encode("test message", pubKeyBytes)
	require.NoError(t, err)
	r2, err := Encode("test message", pubKeyBytes)
	require.NoError(t, err)

	assert.False(t, bytes.Equal(r1.EphemeralPublicKey, r2.EphemeralPublicKey),
		"ephemeral public keys must differ across calls")
	assert.False(t, bytes.Equal(r1.Nonce, r2.Nonce),
		"nonces must differ across calls")
	assert.False(t, bytes.Equal(r1.Ciphertext, r2.Ciphertext),
		"ciphertexts must differ across calls (symptom of nonce reuse if equal)")

	// Guard: nonce must not be a naive truncation of the ephemeral public key
	assert.False(t, bytes.Equal(r1.Nonce, r1.EphemeralPublicKey[:12]),
		"nonce must not equal first 12 bytes of ephemeral public key")
}
```

- [ ] **Step 1.2: Confirm the tests fail to compile (Red)**

```bash
make test SERVICE=pkg/roomcrypto
```

Expected output: build error — `undefined: Encode` (or similar). The package does not exist yet. This confirms the Red state.

---

## Task 2: Implement `Encode` (Green)

**Files:**
- Create: `pkg/roomcrypto/roomcrypto.go`
- Modify: `go.mod`, `go.sum` (via `go mod tidy`)

- [ ] **Step 2.1: Create the implementation file**

Create `pkg/roomcrypto/roomcrypto.go`:

```go
package roomcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// EncryptedMessage holds the output of Encode.
// []byte fields marshal to base64 in JSON automatically.
//
// Note: this struct intentionally deviates from the project convention of including bson tags.
// It is a serialisation-only type sent to clients over JSON; it is never written to MongoDB.
type EncryptedMessage struct {
	EphemeralPublicKey []byte `json:"ephemeralPublicKey"` // 65 bytes, uncompressed P-256 point
	Nonce              []byte `json:"nonce"`              // 12 bytes, AES-GCM nonce
	Ciphertext         []byte `json:"ciphertext"`         // encrypted content + 16-byte AES-GCM tag
}

// Encode encrypts content using the room's P-256 public key.
// roomPublicKey is the uncompressed point (65 bytes) as stored in MongoDB.
func Encode(content string, roomPublicKey []byte) (*EncryptedMessage, error) {
	// Step 1: parse and validate the room public key
	roomPubKey, err := ecdh.P256().NewPublicKey(roomPublicKey)
	if err != nil {
		return nil, fmt.Errorf("parsing room public key: %w", err)
	}

	// Step 2: generate a fresh ephemeral P-256 key pair for this message
	ephemeralPrivKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating ephemeral key: %w", err)
	}

	// Step 3: ECDH — derive shared secret from ephemeral private key + room public key
	sharedSecret, err := ephemeralPrivKey.ECDH(roomPubKey)
	if err != nil {
		// Unreachable: roomPubKey was validated by NewPublicKey above.
		return nil, fmt.Errorf("computing ECDH shared secret: %w", err)
	}

	// Step 4: HKDF-SHA256 — derive 32-byte AES key from shared secret
	// info="room-message-encryption" provides domain separation
	aesKey := make([]byte, 32)
	hkdfReader := hkdf.New(sha256.New, sharedSecret, nil, []byte("room-message-encryption"))
	if _, err := io.ReadFull(hkdfReader, aesKey); err != nil {
		// Unreachable for SHA-256, but must be checked per project convention.
		return nil, fmt.Errorf("deriving AES key: %w", err)
	}

	// Step 5: AES-256-GCM cipher setup
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		// Unreachable: aesKey is always 32 bytes from HKDF above.
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		// Unreachable: AES always produces a 128-bit block cipher.
		return nil, fmt.Errorf("creating GCM wrapper: %w", err)
	}

	// Step 6: generate a random 12-byte nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	// Step 7: encrypt and authenticate; AAD is nil (see spec for trade-off rationale)
	// Seal appends the 16-byte GCM authentication tag to the ciphertext.
	ciphertext := gcm.Seal(nil, nonce, []byte(content), nil)

	return &EncryptedMessage{
		EphemeralPublicKey: ephemeralPrivKey.PublicKey().Bytes(),
		Nonce:              nonce,
		Ciphertext:         ciphertext,
	}, nil
}
```

- [ ] **Step 2.2: Promote `golang.org/x/crypto` to a direct dependency**

```bash
go mod tidy
```

Expected: `go.mod` now lists `golang.org/x/crypto` without `// indirect`. `go.sum` is updated. No other changes.

- [ ] **Step 2.3: Run the tests and confirm they all pass (Green)**

```bash
make test SERVICE=pkg/roomcrypto
```

Expected output: all tests pass, no race conditions detected. Example:

```
ok  	github.com/hmchangw/chat/pkg/roomcrypto	0.XXXs
```

If any test fails, diagnose before continuing — do not move to the next step with a failing test.

---

## Task 3: Verify Coverage, Lint, and Commit

**Files:**
- No new files

- [ ] **Step 3.1: Check test coverage**

```bash
go test -race -coverprofile=coverage.out ./pkg/roomcrypto/...
go tool cover -func=coverage.out | grep -E '(total|roomcrypto)'
```

Expected: total coverage ≥ 80% (84% is the achievable ceiling). Four genuinely unreachable error branches account for the gap: ECDH shared secret, HKDF `io.ReadFull`, `aes.NewCipher`, and `cipher.NewGCM` — all protected by runtime invariants that cannot be violated after the preceding validation steps. The project minimum is 80%; 84% satisfies that threshold.

- [ ] **Step 3.2: Run the linter**

```bash
make lint
```

Expected: no lint errors. Common issues to watch for:
- `errcheck`: all error returns must be handled
- `goimports`: imports must be grouped (stdlib, then external)
- `staticcheck`: no unused code

Fix any lint errors before continuing.

- [ ] **Step 3.3: Commit**

```bash
git add pkg/roomcrypto/roomcrypto.go pkg/roomcrypto/roomcrypto_test.go go.mod go.sum
git commit -m "feat(roomcrypto): add Encode function with ECDH P-256 + HKDF + AES-256-GCM

Implements pkg/roomcrypto.Encode — encrypts a room message using a
per-call ephemeral ECDH key pair, HKDF-SHA256 key derivation, and
AES-256-GCM authenticated encryption. Output is JSON-serialisable
for JS and Swift clients to decrypt.

Promotes golang.org/x/crypto from indirect to direct dependency."
```

- [ ] **Step 3.4: Push the branch**

```bash
git push -u origin claude/room-encryption-library-PPHyO
```

Expected: push succeeds, remote branch is updated.
