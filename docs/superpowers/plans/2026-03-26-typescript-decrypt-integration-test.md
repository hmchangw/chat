# TypeScript Decrypt Integration Test Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an integration test that verifies Go's `pkg/roomcrypto.Encode` produces ciphertext that a TypeScript client (Node.js 20 + Web Crypto API) can correctly decrypt.

**Architecture:** A Go integration test (`//go:build integration`) spins up a `node:20-alpine` testcontainer, installs `tsx`, and copies a TypeScript decrypt script into the container. For each test case the Go test encrypts content with `Encode`, writes the key material and `EncryptedMessage` to a JSON payload file, copies it into the container, and runs `tsx /decrypt.ts /payload.json`. The test asserts the plaintext on stdout matches the original content.

**Tech Stack:** Go 1.24, `testcontainers-go v0.34.0` (already in `go.mod`), `node:20-alpine` Docker image, `tsx` (TypeScript runner), Web Crypto API (`crypto.subtle` built into Node.js 18+).

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `pkg/roomcrypto/integration_test.go` | **Create** | Go integration test: container setup, table-driven encrypt→decrypt assertions |
| `pkg/roomcrypto/testdata/decrypt.ts` | **Create** | TypeScript script: read payload file, decrypt via Web Crypto, print plaintext to stdout |
| `go.mod` / `go.sum` | **Modify** | Promote `testcontainers-go` from indirect to direct (via `go mod tidy`) |

---

## Task 1: Write the Go Integration Test (Red)

**Files:**
- Create: `pkg/roomcrypto/integration_test.go`

The test file references `testdata/decrypt.ts` which does not exist yet. The test will compile but fail at runtime when `CopyFileToContainer` cannot find the script — that is the expected Red state.

- [ ] **Step 1.1: Create the integration test file**

Create `pkg/roomcrypto/integration_test.go`:

```go
//go:build integration

package roomcrypto

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// decryptPayload is the JSON structure passed to the TypeScript decrypt script.
type decryptPayload struct {
	PrivateKey string            `json:"privateKey"` // base64(privKey.Bytes()) — 32-byte P-256 scalar
	PublicKey  string            `json:"publicKey"`  // base64(pubKey.Bytes())  — 65-byte uncompressed point
	Message    *EncryptedMessage `json:"message"`
}

// setupNodeContainer starts a node:20-alpine container, copies decrypt.ts into it,
// and installs tsx. The container is terminated via t.Cleanup.
func setupNodeContainer(t *testing.T) testcontainers.Container {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:      "node:20-alpine",
			Cmd:        []string{"sh", "-c", "sleep 600"},
			WaitingFor: wait.ForExec([]string{"node", "--version"}).WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err, "start node:20-alpine container")
	t.Cleanup(func() {
		_ = container.Terminate(ctx) // best-effort; ignore cleanup errors
	})

	// Copy the TypeScript decrypt script into the container.
	scriptPath := filepath.Join("testdata", "decrypt.ts")
	err = container.CopyFileToContainer(ctx, scriptPath, "/decrypt.ts", 0o644)
	require.NoError(t, err, "copy testdata/decrypt.ts into container")

	// Install tsx (TypeScript runner). Combined stdout+stderr is captured for diagnostics.
	exitCode, reader, err := container.Exec(ctx, []string{"sh", "-c", "npm install -g tsx --quiet 2>&1"})
	require.NoError(t, err, "exec npm install tsx")
	out := readOutput(reader)
	require.Equal(t, 0, exitCode, "npm install tsx failed:\n%s", out)

	return container
}

// readOutput reads the Docker multiplexed output stream into a single string.
func readOutput(r io.Reader) string {
	if r == nil {
		return ""
	}
	var stdout, stderr bytes.Buffer
	_, _ = stdcopy.StdCopy(&stdout, &stderr, r)
	return stdout.String() + stderr.String()
}

func TestEncode_TypeScriptDecrypt(t *testing.T) {
	ctx := context.Background()
	container := setupNodeContainer(t)

	cases := []struct {
		name    string
		content string
	}{
		{name: "non-empty message", content: "hello from Go"},
		{name: "empty string", content: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate a fresh P-256 key pair for this subtest.
			privKey, err := ecdh.P256().GenerateKey(rand.Reader)
			require.NoError(t, err)

			// Encrypt with Go.
			msg, err := Encode(tc.content, privKey.PublicKey().Bytes())
			require.NoError(t, err)

			// Build the JSON payload the TypeScript script expects.
			payload := decryptPayload{
				PrivateKey: base64.StdEncoding.EncodeToString(privKey.Bytes()),
				PublicKey:  base64.StdEncoding.EncodeToString(privKey.PublicKey().Bytes()),
				Message:    msg,
			}
			payloadJSON, err := json.Marshal(payload)
			require.NoError(t, err)

			// Write payload to a per-subtest temp file and copy into the container.
			payloadFile := filepath.Join(t.TempDir(), "payload.json")
			err = os.WriteFile(payloadFile, payloadJSON, 0o600)
			require.NoError(t, err)
			err = container.CopyFileToContainer(ctx, payloadFile, "/payload.json", 0o644)
			require.NoError(t, err, "copy payload.json into container")

			// Run the TypeScript decrypt script.
			exitCode, reader, err := container.Exec(ctx, []string{"tsx", "/decrypt.ts", "/payload.json"})
			require.NoError(t, err, "exec tsx decrypt")
			output := readOutput(reader)
			require.Equal(t, 0, exitCode, "decrypt script exited non-zero:\n%s", output)

			// stdout should equal the original plaintext; trim trailing newline only.
			assert.Equal(t, tc.content, strings.TrimRight(output, "\n"))
		})
	}
}
```

- [ ] **Step 1.2: Promote `testcontainers-go` to a direct dependency**

```bash
cd /home/user/chat && go mod tidy
```

Expected: `go.mod` now lists `github.com/testcontainers/testcontainers-go` as a direct dependency (no `// indirect`). Also adds `github.com/docker/docker` if it wasn't already direct (it is in `go.mod` as indirect — it will remain indirect since only the `stdcopy` sub-package is used, which is fine).

- [ ] **Step 1.3: Confirm the test fails (Red) — `testdata/decrypt.ts` does not exist yet**

```bash
cd /home/user/chat && make test-integration SERVICE=pkg/roomcrypto
```

Expected: FAIL. The test compiles successfully but `setupNodeContainer` fails at `CopyFileToContainer` with an error like:
```
no such file or directory: testdata/decrypt.ts
```
or the container exits non-zero. This confirms the Red state.

- [ ] **Step 1.4: Commit the test file and updated go.mod**

```bash
cd /home/user/chat
git add pkg/roomcrypto/integration_test.go go.mod go.sum
git commit -m "test(roomcrypto): add TypeScript decrypt integration test (Red)

Verifies that Go's Encode output can be decrypted by a TypeScript client
using Web Crypto API in a node:20-alpine testcontainer. Fails until
testdata/decrypt.ts is created."
```

---

## Task 2: Write the TypeScript Decrypt Script (Green)

**Files:**
- Create: `pkg/roomcrypto/testdata/decrypt.ts`

- [ ] **Step 2.1: Create the testdata directory and TypeScript script**

```bash
mkdir -p /home/user/chat/pkg/roomcrypto/testdata
```

Create `pkg/roomcrypto/testdata/decrypt.ts`:

```typescript
import { readFileSync } from 'fs';

interface EncryptedMessage {
  ephemeralPublicKey: string; // base64-encoded 65-byte uncompressed P-256 point
  nonce: string;              // base64-encoded 12-byte AES-GCM nonce
  ciphertext: string;         // base64-encoded ciphertext + 16-byte GCM tag
}

interface DecryptPayload {
  privateKey: string;   // base64 of 32-byte P-256 scalar (from Go privKey.Bytes())
  publicKey: string;    // base64 of 65-byte uncompressed point (from Go pubKey.Bytes())
  message: EncryptedMessage;
}

// Convert a Buffer to base64url encoding (required by the JWK spec).
function toBase64Url(buf: Buffer): string {
  return buf.toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
}

async function decryptMessage(payload: DecryptPayload): Promise<string> {
  const privKeyBytes = Buffer.from(payload.privateKey, 'base64');
  const pubKeyBytes  = Buffer.from(payload.publicKey, 'base64');

  // Go's privKey.Bytes() is the raw 32-byte P-256 scalar.
  // Go's pubKey.Bytes() is the 65-byte uncompressed point: 0x04 || x (32) || y (32).
  // Web Crypto requires JWK format for private key import.
  const jwkPrivate: JsonWebKey = {
    kty: 'EC',
    crv: 'P-256',
    d: toBase64Url(privKeyBytes),               // private scalar
    x: toBase64Url(pubKeyBytes.slice(1, 33)),   // X coordinate
    y: toBase64Url(pubKeyBytes.slice(33, 65)),  // Y coordinate
  };

  const roomPrivKey = await crypto.subtle.importKey(
    'jwk',
    jwkPrivate,
    { name: 'ECDH', namedCurve: 'P-256' },
    false,
    ['deriveBits'],
  );

  // Import the ephemeral public key from the EncryptedMessage.
  // Public keys must use keyUsages: [] — passing any usage throws DataError.
  const ephKeyBytes = Buffer.from(payload.message.ephemeralPublicKey, 'base64');
  const jwkEph: JsonWebKey = {
    kty: 'EC',
    crv: 'P-256',
    x: toBase64Url(ephKeyBytes.slice(1, 33)),
    y: toBase64Url(ephKeyBytes.slice(33, 65)),
  };

  const ephPubKey = await crypto.subtle.importKey(
    'jwk',
    jwkEph,
    { name: 'ECDH', namedCurve: 'P-256' },
    false,
    [],
  );

  // ECDH: derive 32-byte shared secret.
  const sharedSecretBits = await crypto.subtle.deriveBits(
    { name: 'ECDH', public: ephPubKey },
    roomPrivKey,
    256,
  );

  // Import the shared secret as an HKDF key.
  const hkdfKey = await crypto.subtle.importKey(
    'raw',
    sharedSecretBits,
    'HKDF',
    false,
    ['deriveKey'],
  );

  // HKDF-SHA256: derive AES-256-GCM key.
  // salt=new Uint8Array(0) matches Go's nil salt — RFC 5869 §2.2: null salt ≡ zero-length salt.
  const aesKey = await crypto.subtle.deriveKey(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: new Uint8Array(0),
      info: new TextEncoder().encode('room-message-encryption'),
    },
    hkdfKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['decrypt'],
  );

  // AES-256-GCM decrypt.
  // ciphertext already includes the 16-byte GCM tag appended by Go's gcm.Seal.
  // AAD is omitted — matches Go's nil AAD.
  const nonce      = Buffer.from(payload.message.nonce,      'base64');
  const ciphertext = Buffer.from(payload.message.ciphertext, 'base64');

  const plaintext = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv: nonce },
    aesKey,
    ciphertext,
  );

  return new TextDecoder().decode(plaintext);
}

async function main(): Promise<void> {
  const payloadPath = process.argv[2];
  if (!payloadPath) {
    process.stderr.write('usage: tsx decrypt.ts <payload-file>\n');
    process.exit(1);
  }

  const payload: DecryptPayload = JSON.parse(readFileSync(payloadPath, 'utf8'));
  const plaintext = await decryptMessage(payload);
  // Use process.stdout.write (not console.log) to avoid adding an extra newline.
  process.stdout.write(plaintext);
}

main().catch((err: unknown) => {
  process.stderr.write(`error: ${err instanceof Error ? err.message : String(err)}\n`);
  process.exit(1);
});
```

- [ ] **Step 2.2: Run the integration tests (Green)**

```bash
cd /home/user/chat && make test-integration SERVICE=pkg/roomcrypto
```

Expected output (the container startup and npm install take ~30s on first run):

```
ok  	github.com/hmchangw/chat/pkg/roomcrypto	30.XXXs
```

Both subtests (`non-empty message` and `empty string`) must pass. If either fails, check:
- Exit code is 0 from the tsx command (non-zero means a crypto error — examine the captured output)
- The `TrimRight` assertion strips at most one trailing newline

Do NOT proceed to the next step with a failing test.

- [ ] **Step 2.3: Run lint**

```bash
cd /home/user/chat && make lint
```

Expected: 0 issues. If `goimports` flags the integration test imports, run `make fmt` first.

- [ ] **Step 2.4: Commit the TypeScript script and push**

```bash
cd /home/user/chat
git add pkg/roomcrypto/testdata/decrypt.ts
git commit -m "feat(roomcrypto): add TypeScript decrypt script for integration test

Uses Web Crypto API (crypto.subtle) built into Node.js 18+. No npm
packages required beyond tsx. Mirrors the Go Encode algorithm exactly:
ECDH P-256 + HKDF-SHA256 + AES-256-GCM with nil AAD."

git push -u origin claude/room-encryption-library-PPHyO
```
