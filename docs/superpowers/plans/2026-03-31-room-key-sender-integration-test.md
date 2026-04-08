# Room Key Sender Integration Test Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create an integration test that validates end-to-end room key distribution and message decryption — Go server publishes a key and encrypted message via NATS, TypeScript client receives the key over WebSocket and decrypts the message.

**Architecture:** Two testcontainers (NATS server + Node runtime) on a shared Docker network. Go publishes over TCP, TypeScript subscribes over WebSocket. The TypeScript client receives a `RoomKeyEvent`, stores the key, then receives an encrypted message with `X-Room-Key-Version` header, decrypts it using Web Crypto, and prints the plaintext to stdout. The Go test asserts stdout matches the original content.

**Tech Stack:** Go 1.24, NATS (`nats.go`), testcontainers-go (+ network package), TypeScript (`tsx`), `nats` npm package (NATS WS client), Web Crypto API

**Spec:** `docs/superpowers/specs/2026-03-31-room-key-sender-integration-test-design.md`

---

## File Structure

| Action | File | Responsibility |
|--------|------|----------------|
| Create | `pkg/roomkeysender/testdata/client.ts` | TypeScript NATS WS client — receives key, decrypts message |
| Create | `pkg/roomkeysender/integration_test.go` | Go integration test — NATS + Node containers, orchestration |

---

### Task 1: Create TypeScript client script

**Files:**
- Create: `pkg/roomkeysender/testdata/client.ts`

This script is a standalone TypeScript program that connects to NATS over WebSocket, subscribes to the room key and message subjects, receives a room key event, then uses it to decrypt an encrypted message and prints the plaintext to stdout.

- [ ] **Step 1: Create the `testdata` directory**

```bash
mkdir -p pkg/roomkeysender/testdata
```

- [ ] **Step 2: Write the TypeScript client script**

Create `pkg/roomkeysender/testdata/client.ts`:

```typescript
import { connect, type NatsConnection, type Msg } from "nats.ws";

// ---- Types matching Go models ----

interface RoomKeyEvent {
  roomId: string;
  versionId: string;
  publicKey: string;  // base64-encoded 65-byte uncompressed P-256 point
  privateKey: string; // base64-encoded 32-byte scalar
}

interface EncryptedMessage {
  ephemeralPublicKey: string; // base64-encoded 65-byte uncompressed P-256 point
  nonce: string;              // base64-encoded 12-byte AES-GCM nonce
  ciphertext: string;         // base64-encoded ciphertext + 16-byte GCM tag
}

// ---- Helpers ----

function toBase64Url(buf: Buffer): string {
  return buf.toString("base64").replace(/\+/g, "-").replace(/\//g, "_").replace(/=/g, "");
}

// ---- Decryption (matches pkg/roomcrypto algorithm) ----

async function decryptMessage(
  encrypted: EncryptedMessage,
  roomPrivateKeyB64: string,
  roomPublicKeyB64: string,
): Promise<string> {
  const privKeyBytes = Buffer.from(roomPrivateKeyB64, "base64");
  const pubKeyBytes = Buffer.from(roomPublicKeyB64, "base64");

  // Import room private key as JWK for ECDH.
  const jwkPrivate: JsonWebKey = {
    kty: "EC",
    crv: "P-256",
    d: toBase64Url(privKeyBytes),
    x: toBase64Url(pubKeyBytes.slice(1, 33)),
    y: toBase64Url(pubKeyBytes.slice(33, 65)),
  };

  const roomPrivKey = await crypto.subtle.importKey(
    "jwk",
    jwkPrivate,
    { name: "ECDH", namedCurve: "P-256" },
    false,
    ["deriveBits"],
  );

  // Import ephemeral public key from encrypted message.
  const ephKeyBytes = Buffer.from(encrypted.ephemeralPublicKey, "base64");
  const jwkEph: JsonWebKey = {
    kty: "EC",
    crv: "P-256",
    x: toBase64Url(ephKeyBytes.slice(1, 33)),
    y: toBase64Url(ephKeyBytes.slice(33, 65)),
  };

  const ephPubKey = await crypto.subtle.importKey(
    "jwk",
    jwkEph,
    { name: "ECDH", namedCurve: "P-256" },
    false,
    [],
  );

  // ECDH: derive shared secret.
  const sharedSecretBits = await crypto.subtle.deriveBits(
    { name: "ECDH", public: ephPubKey },
    roomPrivKey,
    256,
  );

  // HKDF-SHA256: derive AES-256-GCM key.
  const hkdfKey = await crypto.subtle.importKey("raw", sharedSecretBits, "HKDF", false, [
    "deriveKey",
  ]);

  const aesKey = await crypto.subtle.deriveKey(
    {
      name: "HKDF",
      hash: "SHA-256",
      salt: new Uint8Array(0),
      info: new TextEncoder().encode("room-message-encryption"),
    },
    hkdfKey,
    { name: "AES-GCM", length: 256 },
    false,
    ["decrypt"],
  );

  // AES-256-GCM decrypt.
  const nonce = Buffer.from(encrypted.nonce, "base64");
  const ciphertext = Buffer.from(encrypted.ciphertext, "base64");

  const plaintext = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: nonce },
    aesKey,
    ciphertext,
  );

  return new TextDecoder().decode(plaintext);
}

// ---- Main ----

async function main(): Promise<void> {
  const [natsURL, username, roomID] = process.argv.slice(2);
  if (!natsURL || !username || !roomID) {
    process.stderr.write("usage: tsx client.ts <nats-ws-url> <username> <roomID>\n");
    process.exit(1);
  }

  const keySubject = `chat.user.${username}.event.room.key`;
  const msgSubject = `test.room.${roomID}.msg`;

  // Store received keys indexed by versionId.
  const keys = new Map<string, { publicKey: string; privateKey: string }>();

  const nc: NatsConnection = await connect({ servers: natsURL });

  // Subscribe to key events.
  const keySub = nc.subscribe(keySubject);
  // Subscribe to encrypted messages.
  const msgSub = nc.subscribe(msgSubject);

  // Process key events in background.
  (async () => {
    for await (const msg of keySub) {
      const evt: RoomKeyEvent = JSON.parse(new TextDecoder().decode(msg.data));
      keys.set(evt.versionId, { publicKey: evt.publicKey, privateKey: evt.privateKey });
    }
  })();

  // Process encrypted messages — decrypt first one and exit.
  for await (const msg of msgSub) {
    const versionId = msg.headers?.get("X-Room-Key-Version");
    if (!versionId) {
      process.stderr.write("missing X-Room-Key-Version header\n");
      process.exit(1);
    }

    const keyPair = keys.get(versionId);
    if (!keyPair) {
      process.stderr.write(`no key found for version ${versionId}\n`);
      process.exit(1);
    }

    const encrypted: EncryptedMessage = JSON.parse(new TextDecoder().decode(msg.data));
    const plaintext = await decryptMessage(encrypted, keyPair.privateKey, keyPair.publicKey);

    process.stdout.write(plaintext);
    break;
  }

  await nc.drain();
}

main().catch((err: unknown) => {
  process.stderr.write(`error: ${err instanceof Error ? err.message : String(err)}\n`);
  process.exit(1);
});
```

- [ ] **Step 3: Commit**

```bash
git add pkg/roomkeysender/testdata/client.ts
git commit -m "feat(roomkeysender): add TypeScript NATS WS client for integration test"
```

---

### Task 2: Create Go integration test

**Files:**
- Create: `pkg/roomkeysender/integration_test.go`

**Dependencies you need to know about:**

- `roomkeysender.NewSender(pub)` creates a sender; `Send(username, *model.RoomKeyEvent)` publishes
- `roomcrypto.Encode(content, publicKey)` returns `*roomcrypto.EncryptedMessage`
- `subject.RoomKeyUpdate(username)` returns `"chat.user.{username}.event.room.key"`
- `nats.Conn.PublishMsg(*nats.Msg)` sends a message with headers
- NATS headers: `nats.Header` is `http.Header` (a `map[string][]string`)
- The `testcontainers-go/network` package creates shared Docker networks so the Node container can reach the NATS container by hostname
- The existing `pkg/roomcrypto/integration_test.go` shows the pattern for Node containers (setup, copy files, exec, read stdout)
- The `docker/docker/pkg/stdcopy` package demuxes Docker exec output streams

- [ ] **Step 1: Add the `testcontainers-go/network` dependency**

```bash
go get github.com/testcontainers/testcontainers-go/network@v0.34.0
```

- [ ] **Step 2: Write the integration test**

Create `pkg/roomkeysender/integration_test.go`:

```go
//go:build integration

package roomkeysender_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomcrypto"
	"github.com/hmchangw/chat/pkg/roomkeysender"
)

const natsAlias = "nats-server"

// setupNetwork creates a shared Docker network for the NATS and Node containers.
func setupNetwork(t *testing.T) *testcontainers.DockerNetwork {
	t.Helper()
	ctx := context.Background()
	nw, err := network.New(ctx)
	require.NoError(t, err, "create docker network")
	t.Cleanup(func() {
		_ = nw.Remove(ctx)
	})
	return nw
}

// setupNATS starts a NATS container with TCP (4222) and WebSocket (8080) enabled.
// Returns a connected Go NATS client (TCP) and the WebSocket URL reachable from
// other containers on the shared network.
func setupNATS(t *testing.T, nw *testcontainers.DockerNetwork) (*nats.Conn, string) {
	t.Helper()
	ctx := context.Background()

	// NATS config enabling WebSocket without TLS.
	natsConf := `
listen: 0.0.0.0:4222
websocket {
  listen: "0.0.0.0:8080"
  no_tls: true
}
`

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "nats:2-alpine",
			ExposedPorts: []string{"4222/tcp", "8080/tcp"},
			Cmd:          []string{"--config", "/nats.conf"},
			Files: []testcontainers.ContainerFile{
				{
					Reader:            strings.NewReader(natsConf),
					ContainerFilePath: "/nats.conf",
					FileMode:          0o644,
				},
			},
			Networks:       []string{nw.Name},
			NetworkAliases: map[string][]string{nw.Name: {natsAlias}},
			WaitingFor:     wait.ForLog("Server is ready").WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err, "start NATS container")
	t.Cleanup(func() {
		_ = container.Terminate(ctx)
	})

	// Build TCP URL for Go client (mapped host port).
	host, err := container.Host(ctx)
	require.NoError(t, err)
	tcpPort, err := container.MappedPort(ctx, "4222")
	require.NoError(t, err)
	tcpURL := fmt.Sprintf("nats://%s:%s", host, tcpPort.Port())

	nc, err := nats.Connect(tcpURL)
	require.NoError(t, err, "connect to NATS")
	t.Cleanup(func() {
		nc.Close()
	})

	// Build WS URL for TypeScript client (container-to-container via network alias).
	wsURL := fmt.Sprintf("ws://%s:8080", natsAlias)

	return nc, wsURL
}

// setupNode starts a Node container on the shared network, installs tsx + nats npm
// packages, and copies the client.ts script. Returns the container for exec calls.
func setupNode(t *testing.T, nw *testcontainers.DockerNetwork) testcontainers.Container {
	t.Helper()
	ctx := context.Background()

	scriptPath := filepath.Join("testdata", "client.ts")

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:      "node:20-alpine",
			Cmd:        []string{"sh", "-c", "sleep 600"},
			Networks:   []string{nw.Name},
			WaitingFor: wait.ForExec([]string{"node", "--version"}).WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err, "start Node container")
	t.Cleanup(func() {
		_ = container.Terminate(ctx)
	})

	// Copy client script into the container.
	err = container.CopyFileToContainer(ctx, scriptPath, "/client.ts", 0o644)
	require.NoError(t, err, "copy client.ts into container")

	// Install tsx and nats (WebSocket client).
	exitCode, reader, err := container.Exec(ctx, []string{
		"sh", "-c", "npm install -g tsx --quiet 2>&1 && npm install -g nats.ws --quiet 2>&1",
	})
	require.NoError(t, err, "exec npm install")
	out := readCombined(reader)
	require.Equal(t, 0, exitCode, "npm install failed:\n%s", out)

	return container
}

// readCombined reads a Docker multiplexed output stream and concatenates stdout+stderr.
func readCombined(r io.Reader) string {
	if r == nil {
		return ""
	}
	var stdout, stderr bytes.Buffer
	_, _ = stdcopy.StdCopy(&stdout, &stderr, r)
	return stdout.String() + stderr.String()
}

// splitOutput reads a Docker multiplexed stream and returns stdout and combined output separately.
func splitOutput(r io.Reader) (stdout, combined string) {
	if r == nil {
		return "", ""
	}
	var outBuf, errBuf bytes.Buffer
	_, _ = stdcopy.StdCopy(&outBuf, &errBuf, r)
	return outBuf.String(), outBuf.String() + errBuf.String()
}

func TestRoomKeySender_TypeScriptClient(t *testing.T) {
	ctx := context.Background()

	// 1. Start infrastructure.
	nw := setupNetwork(t)
	nc, wsURL := setupNATS(t, nw)
	nodeContainer := setupNode(t, nw)

	// 2. Generate a fresh P-256 key pair.
	privKey, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubKeyBytes := privKey.PublicKey().Bytes()
	privKeyBytes := privKey.Bytes()

	// 3. Test parameters.
	username := "alice"
	roomID := "room-1"
	versionID := "v-test-001"
	plaintext := "hello from Go integration test"

	// 4. Start the TypeScript client (blocks until it prints output or times out).
	// Run in background via exec; we read output after publishing.
	clientDone := make(chan struct {
		exitCode int
		stdout   string
		combined string
		err      error
	}, 1)

	go func() {
		exitCode, reader, err := nodeContainer.Exec(ctx, []string{
			"tsx", "/client.ts", wsURL, username, roomID,
		})
		stdout, combined := splitOutput(reader)
		clientDone <- struct {
			exitCode int
			stdout   string
			combined string
			err      error
		}{exitCode, stdout, combined, err}
	}()

	// 5. Brief delay for TypeScript subscriptions to establish.
	time.Sleep(3 * time.Second)

	// 6. Publish room key via roomkeysender.
	sender := roomkeysender.NewSender(nc)
	evt := &model.RoomKeyEvent{
		RoomID:     roomID,
		VersionID:  versionID,
		PublicKey:  pubKeyBytes,
		PrivateKey: privKeyBytes,
	}
	err = sender.Send(username, evt)
	require.NoError(t, err, "send room key event")

	// 7. Small delay to ensure key is received before the encrypted message.
	time.Sleep(500 * time.Millisecond)

	// 8. Encrypt a message with the room public key.
	encrypted, err := roomcrypto.Encode(plaintext, pubKeyBytes)
	require.NoError(t, err, "encrypt message")
	encryptedJSON, err := json.Marshal(encrypted)
	require.NoError(t, err, "marshal encrypted message")

	// 9. Publish encrypted message with X-Room-Key-Version header.
	msgSubject := fmt.Sprintf("test.room.%s.msg", roomID)
	natsMsg := &nats.Msg{
		Subject: msgSubject,
		Data:    encryptedJSON,
		Header:  nats.Header{"X-Room-Key-Version": []string{versionID}},
	}
	err = nc.PublishMsg(natsMsg)
	require.NoError(t, err, "publish encrypted message")

	// 10. Wait for TypeScript client to finish.
	select {
	case result := <-clientDone:
		require.NoError(t, result.err, "exec client.ts")
		require.Equal(t, 0, result.exitCode, "client.ts exited non-zero:\n%s", result.combined)
		assert.Equal(t, plaintext, strings.TrimRight(result.stdout, "\n"))
	case <-time.After(30 * time.Second):
		t.Fatal("TypeScript client timed out after 30s")
	}
}
```

- [ ] **Step 3: Run `go mod tidy` to resolve dependencies**

```bash
go mod tidy
```

- [ ] **Step 4: Verify the test compiles**

```bash
go build -tags integration ./pkg/roomkeysender/...
```

Expected: no compilation errors.

- [ ] **Step 5: Run the integration test**

```bash
make test-integration SERVICE=pkg/roomkeysender
```

Expected: `TestRoomKeySender_TypeScriptClient` PASSES — TypeScript client outputs the original plaintext.

- [ ] **Step 6: Commit**

```bash
git add pkg/roomkeysender/integration_test.go go.mod go.sum
git commit -m "feat(roomkeysender): add integration test with TypeScript NATS WS client"
```

---

### Task 3: Final verification and push

- [ ] **Step 1: Run all unit tests to check for regressions**

```bash
make test
```

Expected: All tests PASS across all packages.

- [ ] **Step 2: Run lint**

```bash
make lint
```

Expected: No lint errors.

- [ ] **Step 3: Push**

```bash
git push -u origin claude/room-key-library-S5JBX
```
