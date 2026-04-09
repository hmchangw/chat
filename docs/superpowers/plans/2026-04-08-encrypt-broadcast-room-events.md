# Encrypt Broadcast Room Events Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Encrypt group-room broadcast events in `broadcast-worker` using `roomcrypto.Encode` with the room key fetched from Valkey via `roomkeystore`, carrying the key version inside the `EncryptedMessage` envelope.

**Architecture:** `broadcast-worker` fetches the current room key from Valkey in `publishGroupEvent`, JSON-marshals the `RoomEvent`, encrypts via `roomcrypto.Encode` (which now stamps a `Version` field), and publishes the `EncryptedMessage` JSON to `subject.RoomEvent(roomID)`. DM events stay plaintext — per-user subjects already isolate recipients. Missing key or Valkey errors fail-loudly via NAK + JetStream redelivery; no plaintext fallback.

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB, Valkey (via `go-redis` client in `pkg/roomkeystore`), `pkg/roomcrypto` (ECDH P-256 + HKDF-SHA256 + AES-256-GCM), `go.uber.org/mock`, `testify`, `testcontainers-go`.

**Spec:** `docs/superpowers/specs/2026-04-08-encrypt-broadcast-room-events-design.md`

**Branch:** `claude/encrypt-room-events-4sxE8`

---

## File Structure

**Modified:**
- `pkg/roomcrypto/roomcrypto.go` — add `Version int` field to `EncryptedMessage`; add `version int` parameter to `Encode` and internal `encode`
- `pkg/roomcrypto/roomcrypto_test.go` — update 4 existing `Encode` callers; add new tests asserting `Version` round-trips
- `pkg/roomkeystore/roomkeystore.go` — add `Close() error` to `RoomKeyStore` interface; add `closeClient() error` to `hashCommander` interface; implement `valkeyStore.Close`
- `pkg/roomkeystore/adapter.go` — implement `redisAdapter.closeClient` calling `a.c.Close()`
- `pkg/roomkeystore/roomkeystore_test.go` — add `closeClient` to `fakeHashClient`; add `TestValkeyStore_Close`
- `pkg/roomkeysender/integration_test.go` — update one call site (line ~284) to pass `version` to `roomcrypto.Encode`
- `broadcast-worker/handler.go` — add `RoomKeyProvider` interface; add `keyStore` field to `Handler`; update `NewHandler` constructor; rewrite `publishGroupEvent` to encrypt
- `broadcast-worker/store.go` — add `//go:generate mockgen` directive for `RoomKeyProvider`
- `broadcast-worker/handler_test.go` — add `testRoomKey`, `decryptRoomEvent`, `decryptForTest` helpers; update 11 `NewHandler` call sites; add encryption-specific tests
- `broadcast-worker/integration_test.go` — fix pre-existing `recordingPublisher.Publish` signature bug; add `fakeRoomKeyProvider`; update 4 `NewHandler` call sites; update 3 group-room tests to decrypt
- `broadcast-worker/main.go` — add Valkey config fields; call `roomkeystore.NewValkeyStore`; pass `keyStore` to `NewHandler`; add `keyStore.Close()` to graceful shutdown
- `broadcast-worker/deploy/docker-compose.yml` — add `valkey` service; add Valkey env vars to `broadcast-worker` service

**Created:**
- `broadcast-worker/mock_keystore_test.go` — generated mock for `RoomKeyProvider`
- `broadcast-worker/testhelpers_test.go` — shared `decryptForTest` helper visible to both unit and integration tests

---

## Task 1: `pkg/roomcrypto` — Add `Version` field and update `Encode` signature

**Files:**
- Modify: `pkg/roomcrypto/roomcrypto.go`
- Modify: `pkg/roomcrypto/roomcrypto_test.go`
- Modify: `pkg/roomkeysender/integration_test.go` (line ~284, the sole non-test caller)

- [ ] **Step 1: Write the failing test for the `Version` field**

Append to `pkg/roomcrypto/roomcrypto_test.go` at the end of the file (before `type failReader struct{}`):

```go
func TestEncode_Version(t *testing.T) {
	privKey, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubKeyBytes := privKey.PublicKey().Bytes()

	msg, err := Encode("hello", pubKeyBytes, 42)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, 42, msg.Version, "Version must be stamped from the parameter")
}

func TestEncryptedMessage_JSONRoundTrip(t *testing.T) {
	orig := &EncryptedMessage{
		Version:            7,
		EphemeralPublicKey: []byte{0x01, 0x02, 0x03},
		Nonce:              []byte{0x04, 0x05, 0x06},
		Ciphertext:         []byte{0x07, 0x08, 0x09},
	}
	data, err := json.Marshal(orig)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"version":7`, "JSON tag must be lowercase 'version'")

	var decoded EncryptedMessage
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, orig.Version, decoded.Version)
	assert.Equal(t, orig.EphemeralPublicKey, decoded.EphemeralPublicKey)
	assert.Equal(t, orig.Nonce, decoded.Nonce)
	assert.Equal(t, orig.Ciphertext, decoded.Ciphertext)
}
```

Add `"encoding/json"` to the import block of `roomcrypto_test.go`.

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test -race -run 'TestEncode_Version|TestEncryptedMessage_JSONRoundTrip' ./pkg/roomcrypto/...`
Expected: Compile error — `too many arguments in call to Encode` (current signature takes 2 args), and/or `msg.Version undefined`.

- [ ] **Step 3: Update `EncryptedMessage` struct in `pkg/roomcrypto/roomcrypto.go`**

Replace lines 20–24 of `pkg/roomcrypto/roomcrypto.go` with:

```go
type EncryptedMessage struct {
	Version            int    `json:"version"`            // key version used to encrypt; matches roomkeystore VersionedKeyPair.Version
	EphemeralPublicKey []byte `json:"ephemeralPublicKey"` // 65 bytes, uncompressed P-256 point
	Nonce              []byte `json:"nonce"`              // 12 bytes, AES-GCM nonce
	Ciphertext         []byte `json:"ciphertext"`         // encrypted content + 16-byte AES-GCM tag
}
```

- [ ] **Step 4: Update `Encode` and internal `encode` signatures**

Replace lines 26–34 of `pkg/roomcrypto/roomcrypto.go` with:

```go
// Encode encrypts content using the room's P-256 public key.
// roomPublicKey is the uncompressed point (65 bytes) as stored in MongoDB.
// version is the key version identifier, stamped into the returned EncryptedMessage
// so receivers can pick the right private key for decryption.
func Encode(content string, roomPublicKey []byte, version int) (*EncryptedMessage, error) {
	return encode(content, roomPublicKey, version, rand.Reader)
}

// encode is the internal implementation that accepts an io.Reader for randomness,
// enabling error path testing without changing the public API.
func encode(content string, roomPublicKey []byte, version int, randReader io.Reader) (*EncryptedMessage, error) {
```

- [ ] **Step 5: Stamp the version on the returned struct**

Find the `return &EncryptedMessage{...}` block near the bottom of `encode` (around line 85) and replace it with:

```go
	return &EncryptedMessage{
		Version:            version,
		EphemeralPublicKey: ephemeralPrivKey.PublicKey().Bytes(),
		Nonce:              nonce,
		Ciphertext:         ciphertext,
	}, nil
```

- [ ] **Step 6: Update existing callers of `Encode` in `roomcrypto_test.go`**

There are 4 existing `Encode` calls in `pkg/roomcrypto/roomcrypto_test.go` and 2 existing `encode` calls. Update each to pass a `version` argument:

- Line ~63 (`TestEncode`): `result, err := Encode(tt.content, tt.pubKey, 0)`
- Line ~96 (`TestEncode_RoundTrip`): `msg, err := Encode(tc.content, pubKeyBytes, 0)`
- Lines ~136, ~138 (`TestEncode_NonDeterminism`): `r1, err := Encode("test message", pubKeyBytes, 0)` and `r2, err := Encode("test message", pubKeyBytes, 0)`
- Line ~159 (`TestEncode_RandReaderErrors` / "ephemeral key generation fails"): `result, err := encode("hello", pubKeyBytes, 0, &failReader{})`
- Line ~174 (`TestEncode_RandReaderErrors` / "nonce generation fails"): `_, encErr = encode("hello", pubKeyBytes, 0, r)`

Use `0` as the version for all existing tests — the new test `TestEncode_Version` is the one that asserts version is correctly carried through.

- [ ] **Step 7: Update the one non-test caller in `pkg/roomkeysender/integration_test.go`**

At line 284 of `pkg/roomkeysender/integration_test.go`, replace:

```go
encrypted, err := roomcrypto.Encode(plaintext, pubKeyBytes)
```

with:

```go
encrypted, err := roomcrypto.Encode(plaintext, pubKeyBytes, version)
```

`version` is already an `int` in scope at that line (declared at line 241 of that file).

- [ ] **Step 8: Run the full `pkg/roomcrypto` test suite**

Run: `make test SERVICE=pkg/roomcrypto`
Expected: PASS — all tests green including the new `TestEncode_Version` and `TestEncryptedMessage_JSONRoundTrip`.

- [ ] **Step 9: Verify the whole repo still compiles**

Run: `go build ./...`
Expected: exit code 0, no output.

Also run: `go vet -tags=integration ./pkg/roomkeysender/...`
Expected: exit code 0. Confirms the integration test compiles with the updated call.

- [ ] **Step 10: Commit**

```bash
git add pkg/roomcrypto/roomcrypto.go pkg/roomcrypto/roomcrypto_test.go pkg/roomkeysender/integration_test.go
git commit -m "$(cat <<'EOF'
feat(roomcrypto): add Version field to EncryptedMessage envelope

Encode now takes a version int that is stamped into the returned
EncryptedMessage. Receivers read the version from a single JSON parse
of the body to pick the right private key for decryption. Updates the
one existing non-test caller in pkg/roomkeysender/integration_test.go
to pass its in-scope version variable.
EOF
)"
```

---

## Task 2: `pkg/roomkeystore` — Add `Close()` method

**Files:**
- Modify: `pkg/roomkeystore/roomkeystore.go`
- Modify: `pkg/roomkeystore/adapter.go`
- Modify: `pkg/roomkeystore/roomkeystore_test.go`

- [ ] **Step 1: Write the failing test for `Close`**

Append to `pkg/roomkeystore/roomkeystore_test.go` at the end of the file:

```go
func TestValkeyStore_Close(t *testing.T) {
	t.Run("happy path — delegates to client", func(t *testing.T) {
		fake := &fakeHashClient{}
		store := newTestStore(fake)
		err := store.Close()
		require.NoError(t, err)
		assert.True(t, fake.closed, "Close should have been called on the commander")
	})

	t.Run("propagates client error", func(t *testing.T) {
		fake := &fakeHashClient{closeErr: errors.New("connection already gone")}
		store := newTestStore(fake)
		err := store.Close()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "close valkey client")
	})
}
```

- [ ] **Step 2: Extend `fakeHashClient` with `closed` flag, `closeErr` field, and `closeClient` method**

In `pkg/roomkeystore/roomkeystore_test.go`, update the `fakeHashClient` struct definition (lines 17–25) to add two new fields:

```go
type fakeHashClient struct {
	store             map[string]map[string]string
	hsetErr           error
	hgetallErr        error
	hgetallCallCount  int // tracks number of hgetall calls made
	hgetallErrOnCall  int // if >0, hgetallErr fires only on this call number (1-based)
	rotatePipelineErr error
	deletePipelineErr error
	closeErr          error
	closed            bool
}
```

Add the new method after `deletePipeline` (around line 82):

```go
func (f *fakeHashClient) closeClient() error {
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closed = true
	return nil
}
```

- [ ] **Step 3: Run the new test to verify it fails**

Run: `go test -race -run TestValkeyStore_Close ./pkg/roomkeystore/...`
Expected: Compile error — `*fakeHashClient does not implement hashCommander (missing method closeClient)` OR `store.Close undefined`.

- [ ] **Step 4: Extend `hashCommander` interface and add `Close` to `RoomKeyStore`**

In `pkg/roomkeystore/roomkeystore.go`, update the `RoomKeyStore` interface (lines 28–34) to add `Close`:

```go
// RoomKeyStore defines storage operations for room encryption key pairs.
type RoomKeyStore interface {
	Set(ctx context.Context, roomID string, pair RoomKeyPair) (int, error)
	Get(ctx context.Context, roomID string) (*VersionedKeyPair, error)
	GetByVersion(ctx context.Context, roomID string, version int) (*RoomKeyPair, error)
	Rotate(ctx context.Context, roomID string, newPair RoomKeyPair) (int, error)
	Delete(ctx context.Context, roomID string) error
	Close() error
}
```

Update the `hashCommander` interface (lines 45–50) to add `closeClient`:

```go
// hashCommander is a minimal internal interface over the Valkey hash commands used by valkeyStore.
// Unexported and command-specific so unit tests can inject a fake without a live Valkey connection.
type hashCommander interface {
	hset(ctx context.Context, key string, pub, priv string) error
	hgetall(ctx context.Context, key string) (map[string]string, error)
	rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv string, gracePeriod time.Duration) (int, error)
	deletePipeline(ctx context.Context, currentKey, prevKey string) error
	closeClient() error
}
```

- [ ] **Step 5: Implement `valkeyStore.Close`**

Append to `pkg/roomkeystore/roomkeystore.go` at the end of the file (after `Delete`):

```go
// Close releases the underlying Valkey client connection. Safe to call at shutdown.
func (s *valkeyStore) Close() error {
	if err := s.client.closeClient(); err != nil {
		return fmt.Errorf("close valkey client: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Implement `redisAdapter.closeClient` in `adapter.go`**

Append to `pkg/roomkeystore/adapter.go` after `deletePipeline` (around line 66):

```go
func (a *redisAdapter) closeClient() error {
	return a.c.Close()
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test -race -run TestValkeyStore_Close ./pkg/roomkeystore/...`
Expected: PASS.

Run: `make test SERVICE=pkg/roomkeystore`
Expected: PASS — full package green.

- [ ] **Step 8: Verify the whole repo still compiles**

Run: `go build ./...`
Expected: exit code 0, no output.

- [ ] **Step 9: Commit**

```bash
git add pkg/roomkeystore/roomkeystore.go pkg/roomkeystore/adapter.go pkg/roomkeystore/roomkeystore_test.go
git commit -m "$(cat <<'EOF'
feat(roomkeystore): add Close method to RoomKeyStore interface

Consumers need a way to release the underlying Valkey client at
graceful shutdown. Adds Close() to the RoomKeyStore interface,
closeClient() to the internal hashCommander interface, wires them
through valkeyStore and redisAdapter, and extends the test fake.
EOF
)"
```

---

## Task 3: `broadcast-worker/integration_test.go` — Fix pre-existing `recordingPublisher` signature bug

**Context:** `recordingPublisher.Publish` currently has a stale 2-arg signature (`subj string, data []byte`) that does not satisfy the current `Publisher` interface (`Publish(ctx context.Context, subject string, data []byte) error`). `go vet -tags=integration ./broadcast-worker/...` currently fails with:

```
cannot use pub (*recordingPublisher) as Publisher value in argument to NewHandler: *recordingPublisher does not implement Publisher (wrong type for method Publish)
    have Publish(string, []byte) error
    want Publish(context.Context, string, []byte) error
```

This prerequisite fix must land before Task 4 touches the file.

**Files:**
- Modify: `broadcast-worker/integration_test.go`

- [ ] **Step 1: Verify the failing `go vet`**

Run: `go vet -tags=integration ./broadcast-worker/...`
Expected: FAIL with the "wrong type for method Publish" error quoted above.

- [ ] **Step 2: Fix the `Publish` method signature**

In `broadcast-worker/integration_test.go`, replace line 49:

```go
func (p *recordingPublisher) Publish(subj string, data []byte) error {
```

with:

```go
func (p *recordingPublisher) Publish(_ context.Context, subj string, data []byte) error {
```

- [ ] **Step 3: Verify the `context` package is imported**

Check the import block at lines 5–21 of `broadcast-worker/integration_test.go`. `"context"` is already imported (line 6), so no changes to imports.

- [ ] **Step 4: Verify `go vet` passes**

Run: `go vet -tags=integration ./broadcast-worker/...`
Expected: exit code 0, no output.

- [ ] **Step 5: Commit**

```bash
git add broadcast-worker/integration_test.go
git commit -m "$(cat <<'EOF'
fix(broadcast-worker): update recordingPublisher signature to match Publisher interface

The integration test's recordingPublisher.Publish had a stale 2-arg
signature that no longer satisfied the Publisher interface, causing
the integration build tag to fail go vet. Add the ctx parameter
(ignored, as before) so the interface is satisfied.
EOF
)"
```

---

## Task 4: `broadcast-worker` — Add `RoomKeyProvider` interface, wire Valkey in main.go (no behavior change)

**Context:** This task introduces the dependency and threads it through every constructor and test without changing `publishGroupEvent`. At the end of this task the worker publishes the exact same plaintext `RoomEvent` it did before — encryption is added in Task 5. This split lets us commit a self-contained "plumbing only" change with no test-behavior changes.

**Files:**
- Modify: `broadcast-worker/handler.go`
- Modify: `broadcast-worker/store.go`
- Modify: `broadcast-worker/main.go`
- Modify: `broadcast-worker/handler_test.go`
- Modify: `broadcast-worker/integration_test.go`
- Create: `broadcast-worker/mock_keystore_test.go` (generated)

- [ ] **Step 1: Add `RoomKeyProvider` interface and `keyStore` field to `handler.go`**

In `broadcast-worker/handler.go`, update the import block (lines 3–13) to add `roomkeystore`:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeystore"
	"github.com/hmchangw/chat/pkg/subject"
)
```

Replace lines 15–28 (`Publisher` interface, `Handler` struct, `NewHandler`) with:

```go
// Publisher abstracts NATS publishing so the handler is testable.
type Publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

// RoomKeyProvider fetches the current encryption key for a room.
// Defined here (not imported from pkg/roomkeystore directly) to keep the
// handler's dependency contract narrow — only Get is used.
type RoomKeyProvider interface {
	Get(ctx context.Context, roomID string) (*roomkeystore.VersionedKeyPair, error)
}

// Handler processes MESSAGES_CANONICAL messages and broadcasts room events.
type Handler struct {
	store    Store
	pub      Publisher
	keyStore RoomKeyProvider
}

func NewHandler(store Store, pub Publisher, keyStore RoomKeyProvider) *Handler {
	return &Handler{store: store, pub: pub, keyStore: keyStore}
}
```

Note: Task 5 actually uses `keyStore` inside `publishGroupEvent`. In this task the field is present but unreferenced — Go will not complain because struct fields don't need to be used.

- [ ] **Step 2: Add `//go:generate` directive for `RoomKeyProvider`**

In `broadcast-worker/store.go`, after the existing `//go:generate` directive at line 10, add a second directive:

```go
//go:generate mockgen -destination=mock_store_test.go -package=main . Store
//go:generate mockgen -destination=mock_keystore_test.go -package=main . RoomKeyProvider
```

Note: `mockgen` needs to know which package the `RoomKeyProvider` interface lives in. Since it lives in `broadcast-worker/handler.go` (same `package main`), the directive above works as-is. Run this verification before step 3:

Run: `grep -n "RoomKeyProvider" broadcast-worker/handler.go`
Expected: shows the interface definition line.

- [ ] **Step 3: Regenerate mocks**

Run: `make generate SERVICE=broadcast-worker`
Expected: creates `broadcast-worker/mock_keystore_test.go` and leaves `broadcast-worker/mock_store_test.go` unchanged. No errors.

Verify the new file exists:

Run: `ls broadcast-worker/mock_keystore_test.go`
Expected: `broadcast-worker/mock_keystore_test.go`

Verify it declares `MockRoomKeyProvider`:

Run: `grep -n 'type MockRoomKeyProvider' broadcast-worker/mock_keystore_test.go`
Expected: one line matching.

- [ ] **Step 4: Update all 11 `NewHandler` call sites in `handler_test.go`**

Every existing test currently constructs `h := NewHandler(store, pub)`. Each needs to pass a `MockRoomKeyProvider` with zero expectations (gomock fails the test if unexpected `Get` calls are made, which gives us a contract assertion for free).

For each of these lines in `broadcast-worker/handler_test.go`, replace the `NewHandler` call as shown:

**Line 139** (`TestHandler_HandleMessage_GroupRoom` subtest body):

Replace:
```go
				h := NewHandler(store, pub)
```
with:
```go
				keyStore := NewMockRoomKeyProvider(ctrl)
				h := NewHandler(store, pub, keyStore)
```

**Line 242** (`TestHandler_HandleMessage_DMRoom` subtest body):

Replace:
```go
				h := NewHandler(store, pub)
```
with:
```go
				keyStore := NewMockRoomKeyProvider(ctrl)
				h := NewHandler(store, pub, keyStore)
```

**Lines 280, 294, 308, 323, 343, 359, 384, 404** (all subtests in `TestHandler_HandleMessage_Errors`):

In each subtest, locate the existing pattern:
```go
		h := NewHandler(store, pub)
```
and replace with:
```go
		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, pub, keyStore)
```

**Line 445** (`TestHandler_HandleMessage_DMRoom_PublishError`):

Replace:
```go
	h := NewHandler(store, pub)
```
with:
```go
	keyStore := NewMockRoomKeyProvider(ctrl)
	h := NewHandler(store, pub, keyStore)
```

Note: every subtest that declares `ctrl := gomock.NewController(t)` must continue to do so — we're using the same `ctrl` for both the store mock and the key-store mock. There are no subtests in these tests that lack `ctrl`.

- [ ] **Step 5: Add `fakeRoomKeyProvider` and update all 4 `NewHandler` call sites in `integration_test.go`**

At the top of `broadcast-worker/integration_test.go`, in the import block, add:

```go
	"github.com/hmchangw/chat/pkg/roomkeystore"
```

Below the `recordingPublisher` definition and before `TestBroadcastWorker_GroupRoom_Integration` (around line 63), add:

```go
// fakeRoomKeyProvider is an in-memory stand-in for roomkeystore.RoomKeyStore used by
// integration tests. The broadcast-worker integration tests are scoped to "Mongo + handler"
// and do not spin up a real Valkey container; a tiny fake keeps the test surface minimal.
type fakeRoomKeyProvider struct {
	pair *roomkeystore.VersionedKeyPair
}

func (f *fakeRoomKeyProvider) Get(_ context.Context, _ string) (*roomkeystore.VersionedKeyPair, error) {
	return f.pair, nil
}
```

For each of the 4 `NewHandler` call sites (lines 85, 131, 170, 222), replace:

```go
	handler := NewHandler(store, pub)
```

with:

```go
	keyStore := &fakeRoomKeyProvider{pair: nil}
	handler := NewHandler(store, pub, keyStore)
```

Note: passing `pair: nil` is intentional here — Task 4 does not yet call `keyStore.Get`, so the value is unused. Task 6 will update the 3 group-room tests to populate `pair` with a real key and the DM test will keep `nil`.

- [ ] **Step 6: Update the `NewHandler` call in `main.go` (prep only)**

This step adds the Valkey wiring so `main.go` compiles with the new signature. `main.go` cannot pass a mock — it must pass a real `roomkeystore.RoomKeyStore`, which means wiring Valkey. This is done here rather than in a later task so every commit leaves the repo in a buildable state.

In `broadcast-worker/main.go`, update the import block (lines 3–21) to add `roomkeystore`:

```go
import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"
	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/roomkeystore"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
)
```

Replace the `config` struct (lines 23–29) with:

```go
type config struct {
	NatsURL              string        `env:"NATS_URL"                  envDefault:"nats://localhost:4222"`
	SiteID               string        `env:"SITE_ID"                   envDefault:"default"`
	MongoURI             string        `env:"MONGO_URI"                 envDefault:"mongodb://localhost:27017"`
	MongoDB              string        `env:"MONGO_DB"                  envDefault:"chat"`
	MaxWorkers           int           `env:"MAX_WORKERS"               envDefault:"100"`
	ValkeyAddr           string        `env:"VALKEY_ADDR,required"`
	ValkeyPassword       string        `env:"VALKEY_PASSWORD"           envDefault:""`
	ValkeyKeyGracePeriod time.Duration `env:"VALKEY_KEY_GRACE_PERIOD,required"`
}
```

After the Mongo connect block (currently lines 48–54, ending with `store := NewMongoStore(...)`) and before the NATS connect block, insert the Valkey init:

```go
	keyStore, err := roomkeystore.NewValkeyStore(roomkeystore.Config{
		Addr:        cfg.ValkeyAddr,
		Password:    cfg.ValkeyPassword,
		GracePeriod: cfg.ValkeyKeyGracePeriod,
	})
	if err != nil {
		slog.Error("valkey connect failed", "error", err)
		os.Exit(1)
	}
```

Update the `NewHandler` call (currently line 87) from:

```go
	handler := NewHandler(store, publisher)
```

to:

```go
	handler := NewHandler(store, publisher, keyStore)
```

Update `shutdown.Wait` (currently lines 127–145) to add `keyStore.Close()` after `nc.Drain()` and before `mongoutil.Disconnect`:

```go
	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error {
			iter.Stop()
			return nil
		},
		func(ctx context.Context) error {
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("worker drain timed out: %w", ctx.Err())
			}
		},
		func(ctx context.Context) error { return tracerShutdown(ctx) },
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { return keyStore.Close() },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	)
```

- [ ] **Step 7: Verify the package compiles**

Run: `make build SERVICE=broadcast-worker`
Expected: `bin/broadcast-worker` produced, exit code 0.

- [ ] **Step 8: Verify unit tests still pass (no behavior change expected)**

Run: `make test SERVICE=broadcast-worker`
Expected: PASS. Existing tests behave identically — they never reach `keyStore.Get` because `publishGroupEvent` still publishes plaintext.

- [ ] **Step 9: Verify integration test compiles**

Run: `go vet -tags=integration ./broadcast-worker/...`
Expected: exit code 0, no output.

- [ ] **Step 10: Commit**

```bash
git add broadcast-worker/handler.go broadcast-worker/store.go broadcast-worker/main.go broadcast-worker/handler_test.go broadcast-worker/integration_test.go broadcast-worker/mock_keystore_test.go
git commit -m "$(cat <<'EOF'
feat(broadcast-worker): thread RoomKeyProvider through handler and main

Adds a narrow RoomKeyProvider interface next to Publisher, defined in
the consumer per project convention. Wires Valkey config and
roomkeystore.NewValkeyStore into main.go and adds Close() to graceful
shutdown after nc.Drain. All NewHandler call sites updated. No
behavior change yet — publishGroupEvent still emits plaintext events.
The encryption itself lands in the next commit.
EOF
)"
```

---

## Task 5: `broadcast-worker` — Implement encryption in `publishGroupEvent` (TDD)

**Files:**
- Create: `broadcast-worker/testhelpers_test.go` — shared test helpers (no build tag, visible to both unit and integration builds)
- Modify: `broadcast-worker/handler_test.go` — update group-room subtests; add new encryption-specific tests
- Modify: `broadcast-worker/handler.go` — implement encryption in `publishGroupEvent`

- [ ] **Step 1: Create `testhelpers_test.go` with shared crypto helpers**

Create `broadcast-worker/testhelpers_test.go` with:

```go
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/hkdf"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomcrypto"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// testRoomKey generates a fresh P-256 key pair and wraps it in a VersionedKeyPair
// with Version 3. Used by both unit and integration tests that need to encrypt
// and then decrypt a broadcast event to verify the pipeline.
func testRoomKey(t *testing.T) *roomkeystore.VersionedKeyPair {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	return &roomkeystore.VersionedKeyPair{
		Version: 3,
		KeyPair: roomkeystore.RoomKeyPair{
			PublicKey:  priv.PublicKey().Bytes(),
			PrivateKey: priv.Bytes(),
		},
	}
}

// decryptForTest is the inverse of roomcrypto.Encode, implemented inline in tests
// so pkg/roomcrypto does not need to expose a public Decode function.
// The parameters MUST match roomcrypto.encode exactly: P-256 ECDH, HKDF-SHA256
// with info="room-message-encryption", AES-256-GCM with nil AAD.
func decryptForTest(env *roomcrypto.EncryptedMessage, roomPrivateKey []byte) (string, error) {
	privKey, err := ecdh.P256().NewPrivateKey(roomPrivateKey)
	if err != nil {
		return "", fmt.Errorf("parse room private key: %w", err)
	}
	ephPubKey, err := ecdh.P256().NewPublicKey(env.EphemeralPublicKey)
	if err != nil {
		return "", fmt.Errorf("parse ephemeral public key: %w", err)
	}
	sharedSecret, err := privKey.ECDH(ephPubKey)
	if err != nil {
		return "", fmt.Errorf("ecdh: %w", err)
	}
	aesKey := make([]byte, 32)
	hkdfReader := hkdf.New(sha256.New, sharedSecret, nil, []byte("room-message-encryption"))
	if _, err := io.ReadFull(hkdfReader, aesKey); err != nil {
		return "", fmt.Errorf("hkdf: %w", err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}
	plaintext, err := gcm.Open(nil, env.Nonce, env.Ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("gcm open: %w", err)
	}
	return string(plaintext), nil
}

// decryptRoomEvent unmarshals the published body as an EncryptedMessage,
// asserts Version matches the expected key version, decrypts, and returns
// the parsed inner RoomEvent.
func decryptRoomEvent(t *testing.T, data []byte, key *roomkeystore.VersionedKeyPair) model.RoomEvent {
	t.Helper()
	var env roomcrypto.EncryptedMessage
	require.NoError(t, json.Unmarshal(data, &env))
	require.Equal(t, key.Version, env.Version, "EncryptedMessage.Version must match the key version")
	plaintext, err := decryptForTest(&env, key.KeyPair.PrivateKey)
	require.NoError(t, err)
	var evt model.RoomEvent
	require.NoError(t, json.Unmarshal([]byte(plaintext), &evt))
	return evt
}
```

- [ ] **Step 2: Update `TestHandler_HandleMessage_GroupRoom` to expect encryption**

In `broadcast-worker/handler_test.go`, inside the `for _, tc := range tests { t.Run(tc.name, func(t *testing.T) { ... }) }` block of `TestHandler_HandleMessage_GroupRoom` (starts around line 114):

Locate the existing setup:
```go
				ctrl := gomock.NewController(t)
				store := NewMockStore(ctrl)
				pub := &mockPublisher{}
```

Replace with:
```go
				ctrl := gomock.NewController(t)
				store := NewMockStore(ctrl)
				pub := &mockPublisher{}
				key := testRoomKey(t)
				keyStore := NewMockRoomKeyProvider(ctrl)
				keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)
```

Locate the existing handler construction (the line from Task 4):
```go
				keyStore := NewMockRoomKeyProvider(ctrl)
				h := NewHandler(store, pub, keyStore)
```

Replace with:
```go
				h := NewHandler(store, pub, keyStore)
```

(The `keyStore` is already declared above with an expectation; the line from Task 4 is now redundant.)

Locate the existing decode call:
```go
				evt := decodeRoomEvent(t, pub.records[0].data)
```

Replace with:
```go
				evt := decryptRoomEvent(t, pub.records[0].data, key)
```

- [ ] **Step 3: Update `TestHandler_HandleMessage_Errors` subtests that reach `publishGroupEvent`**

Two subtests in `TestHandler_HandleMessage_Errors` successfully reach `publishGroupEvent` and must be updated to expect encryption:

**"sender mentioned deduplicates lookup"** (around line 374):

Locate the setup block:
```go
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}
		...
		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, pub, keyStore)
```

Replace `keyStore := NewMockRoomKeyProvider(ctrl)` with:
```go
		key := testRoomKey(t)
		keyStore := NewMockRoomKeyProvider(ctrl)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)
```

Locate at the end of the subtest:
```go
		evt := decodeRoomEvent(t, pub.records[0].data)
```

Replace with:
```go
		evt := decryptRoomEvent(t, pub.records[0].data, key)
```

**"employee lookup fails fallback to account"** (around line 395):

Apply the exact same two replacements (add `key := testRoomKey(t)` and the `Get` expectation; change `decodeRoomEvent` to `decryptRoomEvent`).

- [ ] **Step 4: Add new encryption-specific test cases**

Append to `broadcast-worker/handler_test.go` at the end of the file (after `TestExtractMentionedAccounts`):

```go
func TestHandler_HandleMessage_GroupRoom_Encryption(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC)

	t.Run("keystore returns nil key", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		expectEmployeeLookup(store, []string{"sender"}, nil)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(nil, nil)

		h := NewHandler(store, pub, keyStore)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no current key")
		assert.Empty(t, pub.records)
	})

	t.Run("keystore returns error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		expectEmployeeLookup(store, []string{"sender"}, nil)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(nil, errors.New("valkey down"))

		h := NewHandler(store, pub, keyStore)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get room key")
		assert.Contains(t, err.Error(), "valkey down")
		assert.Empty(t, pub.records)
	})

	t.Run("published payload is not a plaintext RoomEvent", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}
		key := testRoomKey(t)
		keyStore := NewMockRoomKeyProvider(ctrl)

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		expectEmployeeLookup(store, []string{"sender"}, nil)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)

		h := NewHandler(store, pub, keyStore)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.NoError(t, err)
		require.Len(t, pub.records, 1)

		// Body parses cleanly as an EncryptedMessage with matching version.
		var env roomcrypto.EncryptedMessage
		require.NoError(t, json.Unmarshal(pub.records[0].data, &env))
		assert.Equal(t, key.Version, env.Version)
		assert.Len(t, env.EphemeralPublicKey, 65)
		assert.Len(t, env.Nonce, 12)
		assert.NotEmpty(t, env.Ciphertext)

		// Body does NOT parse as a RoomEvent with recognisable fields.
		// A strict assertion would be brittle (json.Unmarshal into a typed struct
		// ignores unknown keys), so we assert the decrypted content matches and
		// rely on TestHandler_HandleMessage_GroupRoom for field-level checks.
		decrypted := decryptRoomEvent(t, pub.records[0].data, key)
		assert.Equal(t, model.RoomEventNewMessage, decrypted.Type)
		assert.Equal(t, "room-1", decrypted.RoomID)
	})
}
```

Verify the `roomcrypto` import is present in `handler_test.go`'s import block. If not, add:

```go
	"github.com/hmchangw/chat/pkg/roomcrypto"
```

- [ ] **Step 5: Run the updated tests to verify they FAIL (Red)**

Run: `make test SERVICE=broadcast-worker`
Expected: multiple failures in `TestHandler_HandleMessage_GroupRoom` and `TestHandler_HandleMessage_GroupRoom_Encryption`. The failures should mention things like `json: cannot unmarshal object into Go value of type roomcrypto.EncryptedMessage` (because the current code publishes a plaintext `RoomEvent`), or `missing call(s) to mockRoomKeyProvider.Get` (gomock complaining that `publishGroupEvent` never called `Get`).

Write down the first two failure messages as evidence that the tests were actually red before the implementation.

- [ ] **Step 6: Implement encryption in `publishGroupEvent` (Green)**

In `broadcast-worker/handler.go`, add `"github.com/hmchangw/chat/pkg/roomcrypto"` to the import block.

Replace the entire `publishGroupEvent` function (currently lines 90–102) with:

```go
func (h *Handler) publishGroupEvent(ctx context.Context, room *model.Room, clientMsg *model.ClientMessage, mentionAll bool, mentions []model.Participant) error {
	evt := buildRoomEvent(room, clientMsg)
	evt.MentionAll = mentionAll
	if len(mentions) > 0 {
		evt.Mentions = mentions
	}

	plaintext, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal group room event: %w", err)
	}

	key, err := h.keyStore.Get(ctx, room.ID)
	if err != nil {
		return fmt.Errorf("get room key for room %s: %w", room.ID, err)
	}
	if key == nil {
		return fmt.Errorf("get room key for room %s: no current key", room.ID)
	}

	encrypted, err := roomcrypto.Encode(string(plaintext), key.KeyPair.PublicKey, key.Version)
	if err != nil {
		return fmt.Errorf("encrypt group room event for room %s: %w", room.ID, err)
	}

	payload, err := json.Marshal(encrypted)
	if err != nil {
		return fmt.Errorf("marshal encrypted group room event: %w", err)
	}

	return h.pub.Publish(ctx, subject.RoomEvent(room.ID), payload)
}
```

- [ ] **Step 7: Run the tests to verify they PASS (Green)**

Run: `make test SERVICE=broadcast-worker`
Expected: PASS — all tests green, including:
- `TestHandler_HandleMessage_GroupRoom` (all 4 subtests)
- `TestHandler_HandleMessage_DMRoom` (still plaintext)
- `TestHandler_HandleMessage_Errors` (all subtests)
- `TestHandler_HandleMessage_GroupRoom_Encryption` (all 3 new subtests)

- [ ] **Step 8: Verify no lint regressions**

Run: `make lint`
Expected: exit code 0. If `golangci-lint` reports issues, fix them in-place. Common issues from TDD cycles: unused imports, unused variables, goimports ordering.

- [ ] **Step 9: Verify integration test still compiles (it will fail at runtime; that's fine)**

Run: `go vet -tags=integration ./broadcast-worker/...`
Expected: exit code 0, no output. (Integration tests won't be run here — Task 6 updates them to decrypt.)

- [ ] **Step 10: Commit**

```bash
git add broadcast-worker/handler.go broadcast-worker/handler_test.go broadcast-worker/testhelpers_test.go
git commit -m "$(cat <<'EOF'
feat(broadcast-worker): encrypt group room broadcast events

publishGroupEvent now fetches the room's current encryption key from
Valkey via the injected RoomKeyProvider, encrypts the JSON-marshaled
RoomEvent via roomcrypto.Encode, and publishes the EncryptedMessage
(with Version stamped in the envelope) to subject.RoomEvent(roomID).

Missing key and keystore errors return a wrapped error so the NATS
message is NAK'd and redelivered — no plaintext fallback. DM events
remain plaintext (per-user subjects already isolate recipients).

Adds testhelpers_test.go with decryptForTest / decryptRoomEvent
helpers shared between unit and integration tests. The group-room
unit tests now round-trip through encryption to verify both the
envelope shape and the decrypted content.
EOF
)"
```

---

## Task 6: `broadcast-worker/integration_test.go` — Update integration tests for encrypted group events

**Context:** After Task 5, the 3 group-room integration tests fail because they unmarshal the published body as a plaintext `model.RoomEvent`. This task updates them to populate a real key in `fakeRoomKeyProvider` and decrypt the published body via the shared `decryptRoomEvent` helper from `testhelpers_test.go`. The DM integration test stays plaintext.

**Files:**
- Modify: `broadcast-worker/integration_test.go`

- [ ] **Step 1: Confirm the integration tests are currently broken (expected failure)**

Run: `make test-integration SERVICE=broadcast-worker`
Expected: the 3 group-room tests fail; the DM test passes. Failure message will be something like `json: cannot unmarshal object into Go value of type model.RoomEvent` or an assertion on `roomEvt.SiteID` returning an empty string (because the body is now an `EncryptedMessage`, not a `RoomEvent`).

If Docker is unavailable in the development environment, skip this step — the compile-time check from Task 5 already verified the file builds.

- [ ] **Step 2: Update `TestBroadcastWorker_GroupRoom_Integration`**

In `broadcast-worker/integration_test.go`, locate `TestBroadcastWorker_GroupRoom_Integration` (starts at line 64).

Replace the setup block:
```go
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
	pub := &recordingPublisher{}
	keyStore := &fakeRoomKeyProvider{pair: nil}
	handler := NewHandler(store, pub, keyStore)
```

with:
```go
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
	pub := &recordingPublisher{}
	key := testRoomKey(t)
	keyStore := &fakeRoomKeyProvider{pair: key}
	handler := NewHandler(store, pub, keyStore)
```

Replace the decode block at the end of the test:
```go
	var roomEvt model.RoomEvent
	require.NoError(t, json.Unmarshal(records[0].data, &roomEvt))
	assert.Equal(t, "site-a", roomEvt.SiteID)
	require.NotNil(t, roomEvt.Message)
	require.NotNil(t, roomEvt.Message.Sender)
	assert.Equal(t, "u1", roomEvt.Message.Sender.UserID)
```

with:
```go
	roomEvt := decryptRoomEvent(t, records[0].data, key)
	assert.Equal(t, "site-a", roomEvt.SiteID)
	require.NotNil(t, roomEvt.Message)
	require.NotNil(t, roomEvt.Message.Sender)
	assert.Equal(t, "u1", roomEvt.Message.Sender.UserID)
```

- [ ] **Step 3: Update `TestBroadcastWorker_GroupRoom_MentionAll_Integration`**

Locate `TestBroadcastWorker_GroupRoom_MentionAll_Integration` (starts at line 115).

Replace the setup block:
```go
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
	pub := &recordingPublisher{}
	keyStore := &fakeRoomKeyProvider{pair: nil}
	handler := NewHandler(store, pub, keyStore)
```

with:
```go
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
	pub := &recordingPublisher{}
	key := testRoomKey(t)
	keyStore := &fakeRoomKeyProvider{pair: key}
	handler := NewHandler(store, pub, keyStore)
```

This test does not currently decode the published body (it only asserts Mongo state), so no decoding change is needed. However, the test will still call `publishGroupEvent`, which now requires a valid key — populating `pair: key` is required for the publish to succeed.

- [ ] **Step 4: Update `TestBroadcastWorker_GroupRoom_IndividualMention_Integration`**

Locate `TestBroadcastWorker_GroupRoom_IndividualMention_Integration` (starts at line 149).

Replace the setup block:
```go
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
	pub := &recordingPublisher{}
	keyStore := &fakeRoomKeyProvider{pair: nil}
	handler := NewHandler(store, pub, keyStore)
```

with:
```go
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("employee"))
	pub := &recordingPublisher{}
	key := testRoomKey(t)
	keyStore := &fakeRoomKeyProvider{pair: key}
	handler := NewHandler(store, pub, keyStore)
```

Replace the decode block near the end of the test:
```go
	records := pub.getRecords()
	var roomEvt model.RoomEvent
	require.NoError(t, json.Unmarshal(records[0].data, &roomEvt))
	require.Len(t, roomEvt.Mentions, 1)
	assert.Equal(t, "bob", roomEvt.Mentions[0].Account)
	assert.Equal(t, "鮑勃", roomEvt.Mentions[0].ChineseName)
	assert.Equal(t, "Bob Chen", roomEvt.Mentions[0].EngName)
	assert.Empty(t, roomEvt.Mentions[0].UserID)
```

with:
```go
	records := pub.getRecords()
	roomEvt := decryptRoomEvent(t, records[0].data, key)
	require.Len(t, roomEvt.Mentions, 1)
	assert.Equal(t, "bob", roomEvt.Mentions[0].Account)
	assert.Equal(t, "鮑勃", roomEvt.Mentions[0].ChineseName)
	assert.Equal(t, "Bob Chen", roomEvt.Mentions[0].EngName)
	assert.Empty(t, roomEvt.Mentions[0].UserID)
```

- [ ] **Step 5: Leave `TestBroadcastWorker_DMRoom_Integration` unchanged**

Verify `TestBroadcastWorker_DMRoom_Integration` (starts at line 201) still sets `keyStore := &fakeRoomKeyProvider{pair: nil}` from Task 4, and still decodes published records as plaintext `model.RoomEvent`. No changes — this test is the explicit demonstration that the DM path never touches the keystore.

- [ ] **Step 6: Run integration tests**

Run: `make test-integration SERVICE=broadcast-worker`
Expected: PASS — all 4 integration tests green.

If Docker is unavailable, at minimum run:
```
go vet -tags=integration ./broadcast-worker/...
```
Expected: exit code 0.

- [ ] **Step 7: Commit**

```bash
git add broadcast-worker/integration_test.go
git commit -m "$(cat <<'EOF'
test(broadcast-worker): update integration tests for encrypted group events

Group-room integration tests now generate a real P-256 key pair,
populate the fakeRoomKeyProvider with it, and decrypt published
records via the shared decryptRoomEvent helper. The DM integration
test keeps pair: nil and continues to assert plaintext, verifying the
DM path never consults the keystore.
EOF
)"
```

---

## Task 7: `broadcast-worker/deploy/docker-compose.yml` — Add Valkey service and env vars

**Files:**
- Modify: `broadcast-worker/deploy/docker-compose.yml`

- [ ] **Step 1: Add `valkey` service and update `broadcast-worker` environment**

Replace the entire contents of `broadcast-worker/deploy/docker-compose.yml` with:

```yaml
services:
  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "8222:8222"
    command: ["--jetstream", "--http_port", "8222"]

  mongodb:
    image: mongo:8
    ports:
      - "27017:27017"

  valkey:
    image: valkey/valkey:8-alpine
    ports:
      - "6379:6379"

  broadcast-worker:
    build:
      context: ../..
      dockerfile: broadcast-worker/deploy/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
      - VALKEY_ADDR=valkey:6379
      - VALKEY_KEY_GRACE_PERIOD=24h
    depends_on:
      - nats
      - mongodb
      - valkey
```

- [ ] **Step 2: Validate the compose file**

Run: `docker compose -f broadcast-worker/deploy/docker-compose.yml config >/dev/null`
Expected: exit code 0, no output. This validates YAML syntax and service references without pulling images.

If `docker compose` is not available in the environment, at minimum run:
```
python3 -c "import yaml, sys; yaml.safe_load(open('broadcast-worker/deploy/docker-compose.yml'))"
```
Expected: exit code 0. Basic YAML sanity check.

- [ ] **Step 3: Commit**

```bash
git add broadcast-worker/deploy/docker-compose.yml
git commit -m "$(cat <<'EOF'
chore(broadcast-worker): add valkey service to docker-compose

Adds a valkey/valkey:8-alpine service on port 6379 and wires the
required VALKEY_ADDR and VALKEY_KEY_GRACE_PERIOD env vars into the
broadcast-worker service. Required now that broadcast-worker fetches
room encryption keys from Valkey on every group-room broadcast.
EOF
)"
```

---

## Task 8: Final verification — lint, format, full test suite

**Files:** none modified unless fixes are needed

- [ ] **Step 1: Format the whole repo**

Run: `make fmt`
Expected: exit code 0. If the command produces diffs, re-run `git status` and stage any formatted files for an amendment (see Step 5 below).

- [ ] **Step 2: Run the linter**

Run: `make lint`
Expected: exit code 0, no issues.

If `golangci-lint` reports issues:
- Fix them in place.
- If the fix is a formatting-only change, it will be rolled into Step 5.
- If the fix is a semantic change, it must be re-tested.

- [ ] **Step 3: Regenerate mocks to verify they're current**

Run: `make generate`
Expected: exit code 0. `git status` should show no modifications — if mocks are out of sync with their source interfaces, files will be updated.

- [ ] **Step 4: Run the full unit test suite with race detector**

Run: `make test`
Expected: PASS across the whole repo. No race detector warnings.

- [ ] **Step 5: Run the integration suite for the packages we touched**

Run: `make test-integration SERVICE=broadcast-worker`
Expected: PASS (requires Docker).

Run: `make test-integration SERVICE=pkg/roomkeystore`
Expected: PASS — verifies the new `Close()` method against a real Valkey container.

Run: `make test-integration SERVICE=pkg/roomkeysender`
Expected: PASS — verifies the updated `roomcrypto.Encode` signature is compatible with the existing TypeScript client round-trip.

If any integration suite fails due to missing Docker, document the failure and skip to Step 6 — unit tests are the primary acceptance gate.

- [ ] **Step 6: Commit any residual fixes from Steps 1–4 (only if needed)**

If Steps 1–4 produced changes to tracked files, create a cleanup commit:

```bash
git status
# Review the diff carefully
git add <specific files>
git commit -m "$(cat <<'EOF'
chore: apply lint and format fixes

Final sweep after the encryption feature: goimports ordering, any
residual golangci-lint findings, and mock regeneration. No behavior
changes.
EOF
)"
```

If Steps 1–4 produced no changes, no commit is needed for this task.

- [ ] **Step 7: Verify the branch is ready**

Run: `git log --oneline claude/encrypt-room-events-4sxE8 ^main | wc -l`
Expected: 8 commits (one per task, possibly +1 if Step 6 produced a cleanup commit). Review `git log --oneline -10` to confirm each commit message is concrete and descriptive.

Run: `git status`
Expected: `nothing to commit, working tree clean`.

---

## Spec Coverage Checklist

| Spec requirement | Task(s) covering it |
|---|---|
| `EncryptedMessage.Version int` field | Task 1 |
| `Encode(content, pubKey, version int)` signature | Task 1 |
| Update `pkg/roomkeysender/integration_test.go` caller | Task 1 |
| `RoomKeyStore.Close()` method | Task 2 |
| `hashCommander.closeClient()`, `redisAdapter.closeClient()`, `valkeyStore.Close()` | Task 2 |
| Fake `hashCommander` closeClient stub | Task 2 |
| `TestValkeyStore_Close` | Task 2 |
| Fix pre-existing `recordingPublisher` signature | Task 3 |
| `RoomKeyProvider` interface in `broadcast-worker/handler.go` | Task 4 |
| `Handler.keyStore` field + `NewHandler` constructor update | Task 4 |
| `mockgen` directive for `RoomKeyProvider` + generated mock | Task 4 |
| `main.go` Valkey config (`VALKEY_ADDR`, `VALKEY_PASSWORD`, `VALKEY_KEY_GRACE_PERIOD`) | Task 4 |
| `main.go` `roomkeystore.NewValkeyStore` startup + `keyStore.Close` shutdown | Task 4 |
| All 11 unit test `NewHandler` call sites updated | Task 4 |
| All 4 integration test `NewHandler` call sites + `fakeRoomKeyProvider` | Task 4 |
| `publishGroupEvent` encryption implementation | Task 5 |
| Happy path decrypts to the same `RoomEvent` | Task 5 |
| Missing key → wrapped error with `"no current key"` | Task 5 |
| Keystore error → wrapped error with `"get room key"` | Task 5 |
| DM path makes zero keystore calls | Task 5 (unit) + Task 6 (integration) |
| `testRoomKey`, `decryptForTest`, `decryptRoomEvent` shared helpers | Task 5 |
| Group-room integration tests decrypt published body | Task 6 |
| DM integration test stays plaintext | Task 6 |
| Valkey service in `broadcast-worker/deploy/docker-compose.yml` | Task 7 |
| `VALKEY_ADDR` + `VALKEY_KEY_GRACE_PERIOD` in broadcast-worker env block | Task 7 |
| Lint, format, full test pass | Task 8 |

## Explicitly out of scope (from spec)

- DM event encryption.
- Encrypting `Message.Content` at rest in Cassandra.
- Encryption in any service other than `broadcast-worker`.
- Removing the redundant `X-Room-Key-Version` NATS header from `pkg/roomkeysender/integration_test.go`.
- Process-local caching of room keys.
- A public `Decode` helper in `pkg/roomcrypto` (test files use inline stdlib crypto via `testhelpers_test.go`).
- Real Valkey container in `broadcast-worker/integration_test.go` (in-memory `fakeRoomKeyProvider` is used instead).
- Encryption metrics, dashboards, or alerts.
