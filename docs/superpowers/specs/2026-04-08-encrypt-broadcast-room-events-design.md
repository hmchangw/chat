# Encrypt Broadcast Room Events (Group Rooms)

**Status:** Draft
**Date:** 2026-04-08
**Branch:** `claude/encrypt-room-events-4sxE8`

## Summary

Encrypt the `Message` field (`*ClientMessage`) within `model.RoomEvent` for group-room broadcasts. All other `RoomEvent` metadata (type, roomId, roomName, mentions, timestamps, etc.) stays in plaintext so clients can read routing/display info without decryption. The worker fetches the room's current encryption key from Valkey via `pkg/roomkeystore`, encrypts the JSON-marshaled `ClientMessage` via `pkg/roomcrypto.Encode`, sets the result in a new `EncryptedMessage json.RawMessage` field on the `RoomEvent`, nils out the plaintext `Message` field, and publishes the modified `RoomEvent`.

DM (direct message) broadcasts are explicitly **not** encrypted: per-user subjects (`chat.user.{account}.…`) are already isolated to a single recipient by NATS auth, so encryption adds no marginal protection there.

The key version used for encryption is carried inside the `EncryptedMessage` envelope (within the `roomcrypto.EncryptedMessage.Version` field), not in a NATS header.

## Goals

- The message content (`ClientMessage`) within group-room broadcast events is unreadable on the wire to any subscriber that does not possess the room's current private key.
- Event metadata (room ID, type, mentions, timestamps) remains in plaintext so clients can render notifications, badges, and sort order without decrypting.
- The receiver can identify the key version from a single JSON parse of the `encryptedMessage` field and reject/refetch if the cached version doesn't match.
- DM broadcasts and all other services are unaffected.
- Failure modes (missing key, Valkey down, encryption error) fail loudly via NAK + redelivery — no silent degradation.

## Non-Goals

- Encrypting DM broadcast events.
- Encrypting `Message.Content` at rest in Cassandra (`message-worker` continues to write plaintext).
- Encrypting other event types in other services (`message-gatekeeper`, `notification-worker`, `inbox-worker`, etc.).
- Removing the redundant `X-Room-Key-Version` NATS header from `pkg/roomkeysender/integration_test.go` (separate cleanup; would cascade into the TypeScript test client).
- Adding a process-local cache of room keys in `broadcast-worker`. Sub-millisecond Valkey GETs do not justify the freshness/rotation complexity.
- A `Decode` helper in `pkg/roomcrypto`. Tests reach into stdlib crypto directly if they need to verify decryption.
- Encryption metrics, dashboards, or alerts.

## Threat Model

This change protects the message content (`ClientMessage`) within group-room broadcast events from being read by any NATS subscriber on `chat.room.{roomID}.event` who does not possess the room's current private key. Event metadata (room ID, type, mentions, timestamps, user count) remains in plaintext — this is intentional; these fields contain no message body text and are needed by clients for UI rendering without decryption.

Distribution of the room's private key to legitimate members is governed by the existing `pkg/roomkeysender` flow (out of scope here). This design assumes legitimate room members have the key cached locally and can decrypt with the version identifier embedded in the `encryptedMessage` field.

DM events on per-user subjects are not in scope: NATS auth restricts those subjects to a single recipient, and encryption would add no marginal confidentiality.

## Architecture

### Where the encryption happens

`broadcast-worker/handler.go` is the only file that publishes `model.RoomEvent`. It has two publish paths:

- `publishGroupEvent` — single message to `subject.RoomEvent(roomID)`
- `publishDMEvents` — one message per subscriber to `subject.UserRoomEvent(account)`

Only `publishGroupEvent` is changed. `publishDMEvents` is untouched and continues to publish plaintext `RoomEvent` JSON.

### New flow for `publishGroupEvent`

1. Build the `RoomEvent` (existing logic: enrichment, mentions, etc.).
2. JSON-marshal `evt.Message` (the `*ClientMessage`) to bytes.
3. Fetch the room's current key via `keyStore.Get(ctx, room.ID)`.
4. If `Get` returns an error or `(nil, nil)`, return a wrapped error → message is NAK'd → JetStream redelivers.
5. Call `roomcrypto.Encode(string(msgJSON), key.KeyPair.PublicKey, key.Version)`.
6. JSON-marshal the resulting `*roomcrypto.EncryptedMessage` to bytes.
7. Set `evt.EncryptedMessage = json.RawMessage(encJSON)`.
8. Set `evt.Message = nil`.
9. JSON-marshal the full `evt` and publish to `subject.RoomEvent(room.ID)`.

The fetch happens inside the group-only branch (not at the top of `HandleMessage`) so the DM path stays zero-cost and DM rooms never need a key in Valkey.

### Wire format (group room)

The published JSON retains all metadata in plaintext. Only the `message` field is removed and replaced by `encryptedMessage`:

```json
{
  "type": "new_message",
  "roomId": "room-1",
  "timestamp": 1712700000000,
  "roomName": "general",
  "roomType": "group",
  "siteId": "site-a",
  "userCount": 5,
  "lastMsgAt": "2026-04-08T10:00:00Z",
  "lastMsgId": "msg-1",
  "mentionAll": false,
  "mentions": [{"account": "alice", "chineseName": "愛麗絲", "engName": "Alice Wang"}],
  "encryptedMessage": {
    "version": 3,
    "ephemeralPublicKey": "base64...",
    "nonce": "base64...",
    "ciphertext": "base64..."
  }
}
```

Client decryption flow:
1. Parse the `RoomEvent` JSON — read metadata fields directly in plaintext.
2. Parse `encryptedMessage` as `{version, ephemeralPublicKey, nonce, ciphertext}`.
3. Compare `version` with the locally cached room private-key version; if mismatched, fetch the correct key version from the server.
4. Decrypt `ciphertext` using ECDH + HKDF + AES-GCM to recover the `ClientMessage` JSON.

### Versioning scheme

`roomcrypto.EncryptedMessage` gains a `Version int` field. The version is assigned by `roomkeystore` (`VersionedKeyPair.Version`) and stamped into the envelope by `roomcrypto.Encode`. The receiver reads `version` from the `encryptedMessage` object within the `RoomEvent` body, compares against its locally cached private-key version, and either decrypts or refetches the key for that version.

Rationale for body-field over NATS header:
- The receiver always needs the version on every event and already parses the body.
- Atomic envelope: version and ciphertext are inseparable within the `encryptedMessage` object.
- No `Publisher` interface change in `broadcast-worker`.
- Real-world crypto envelopes (JWE `kid`, COSE `kid`) embed key identifiers in the envelope; the boundary is conventional.

## Type & API Changes

### `pkg/model/event.go` — new field on `RoomEvent`

```go
type RoomEvent struct {
    // ... existing fields unchanged ...

    Message          *ClientMessage  `json:"message,omitempty"`
    EncryptedMessage json.RawMessage `json:"encryptedMessage,omitempty"` // new
}
```

`EncryptedMessage` holds the JSON-marshaled `roomcrypto.EncryptedMessage` (containing `version`, `ephemeralPublicKey`, `nonce`, `ciphertext`). Using `json.RawMessage` avoids a `model` → `roomcrypto` package dependency — the raw bytes are opaque to the model package. The handler marshals `*roomcrypto.EncryptedMessage` into this field; the client parses it as needed.

When encrypted: `Message` is `nil`, `EncryptedMessage` is populated.
When plaintext (DM events): `Message` is populated, `EncryptedMessage` is `nil`.

`"encoding/json"` must be added to the import block of `pkg/model/event.go`.

### `pkg/roomcrypto/roomcrypto.go`

```go
type EncryptedMessage struct {
    Version            int    `json:"version"`            // key version used to encrypt
    EphemeralPublicKey []byte `json:"ephemeralPublicKey"`
    Nonce              []byte `json:"nonce"`
    Ciphertext         []byte `json:"ciphertext"`
}

func Encode(content string, roomPublicKey []byte, version int) (*EncryptedMessage, error)
```

The internal `encode(content, roomPublicKey, randReader)` helper gains a `version int` parameter and stamps it on the returned struct. All other steps (ECDH → HKDF → AES-GCM) are unchanged.

`version` is stamped as-is — no bounds check. The caller is responsible for passing the version it just fetched from `roomkeystore`. This matches `roomkeystore.VersionedKeyPair.Version`, also a plain `int`.

### One existing caller updated

`pkg/roomkeysender/integration_test.go:284` is the only non-test caller of `roomcrypto.Encode` in the repo. Update to pass the version it already has in scope:

```go
encrypted, err := roomcrypto.Encode(plaintext, pubKeyBytes, version)
```

The redundant `X-Room-Key-Version` NATS header in that test stays in place to keep the diff focused; removing it would cascade into TypeScript-side changes that are out of scope.

### `pkg/roomkeystore` — add `Close`

`roomkeystore.RoomKeyStore` currently has no way to release the underlying `redis.Client`. The broadcast-worker's graceful shutdown sequence needs it. Add:

```go
type RoomKeyStore interface {
    Set(ctx context.Context, roomID string, pair RoomKeyPair) (int, error)
    Get(ctx context.Context, roomID string) (*VersionedKeyPair, error)
    GetByVersion(ctx context.Context, roomID string, version int) (*RoomKeyPair, error)
    Rotate(ctx context.Context, roomID string, newPair RoomKeyPair) (int, error)
    Delete(ctx context.Context, roomID string) error
    Close() error  // new
}
```

Wire-through:
- `hashCommander` interface gains `closeClient() error`
- `redisAdapter.closeClient()` calls `a.c.Close()`
- `valkeyStore.Close()` delegates to `s.client.closeClient()`
- The fake `hashCommander` in `pkg/roomkeystore/roomkeystore_test.go` implements `closeClient() error { return nil }`

## `broadcast-worker` Changes

### `handler.go`

New consumer-defined interface, scoped to the one method the handler needs:

```go
// RoomKeyProvider fetches the current encryption key for a room.
type RoomKeyProvider interface {
    Get(ctx context.Context, roomID string) (*roomkeystore.VersionedKeyPair, error)
}
```

`Handler` gains a `keyStore RoomKeyProvider` field. `NewHandler` takes a third parameter:

```go
func NewHandler(store Store, pub Publisher, keyStore RoomKeyProvider) *Handler
```

`publishGroupEvent` rewritten — encrypts only the `Message` field:

```go
func (h *Handler) publishGroupEvent(ctx context.Context, room *model.Room, clientMsg *model.ClientMessage, mentionAll bool, mentions []model.Participant) error {
    evt := buildRoomEvent(room, clientMsg)
    evt.MentionAll = mentionAll
    if len(mentions) > 0 {
        evt.Mentions = mentions
    }

    // Encrypt the ClientMessage, not the entire RoomEvent.
    msgJSON, err := json.Marshal(evt.Message)
    if err != nil {
        return fmt.Errorf("marshal client message: %w", err)
    }

    key, err := h.keyStore.Get(ctx, room.ID)
    if err != nil {
        return fmt.Errorf("get room key for room %s: %w", room.ID, err)
    }
    if key == nil {
        return fmt.Errorf("get room key for room %s: no current key", room.ID)
    }

    encrypted, err := roomcrypto.Encode(string(msgJSON), key.KeyPair.PublicKey, key.Version)
    if err != nil {
        return fmt.Errorf("encrypt message for room %s: %w", room.ID, err)
    }

    encJSON, err := json.Marshal(encrypted)
    if err != nil {
        return fmt.Errorf("marshal encrypted message: %w", err)
    }

    evt.EncryptedMessage = json.RawMessage(encJSON)
    evt.Message = nil

    payload, err := json.Marshal(evt)
    if err != nil {
        return fmt.Errorf("marshal group room event: %w", err)
    }

    return h.pub.Publish(ctx, subject.RoomEvent(room.ID), payload)
}
```

`publishDMEvents` is unchanged — continues to set `Message` and leave `EncryptedMessage` nil.

New imports in `handler.go`:
```go
"encoding/json" // already present
"github.com/hmchangw/chat/pkg/roomcrypto"
"github.com/hmchangw/chat/pkg/roomkeystore"
```

### `main.go`

Config struct extended:

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

Startup wiring (between Mongo connect and the existing publisher/handler block):

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

`NewHandler` call updated:
```go
handler := NewHandler(store, publisher, keyStore)
```

Graceful shutdown gets one extra step, placed after `nc.Drain` (so any in-flight publish completes first) and before `mongoutil.Disconnect`:
```go
func(ctx context.Context) error { return keyStore.Close() },
```

### `deploy/docker-compose.yml`

A `valkey` service is added alongside the existing `mongo` and `nats` services, exposing port 6379. The `broadcast-worker` service gains:
- `VALKEY_ADDR=valkey:6379`
- `VALKEY_KEY_GRACE_PERIOD=24h`

The Valkey image uses `valkey/valkey:8-alpine` (current stable). No existing service in the repo runs Valkey yet, so this is the first pin.

## Test Plan

### `pkg/roomcrypto/roomcrypto_test.go`

- Existing tests updated to pass a non-zero `version` (e.g., `7`) so the assertion is meaningful — zero would pass even if the field were never set.
- New assertion: after `Encode`, `EncryptedMessage.Version` equals the passed value.
- New JSON round-trip case: marshal an `EncryptedMessage{Version: 42, ...}`, unmarshal back, assert `Version == 42` and that `version` is the JSON field name.
- Existing error-path tests continue to work; just thread a version through.

### `pkg/roomkeystore/roomkeystore_test.go`

- The fake `hashCommander` implements `closeClient() error { return nil }`.
- New `Test_valkeyStore_Close` verifies `Close()` delegates to the underlying commander and returns nil for the fake.

### `pkg/roomkeysender/integration_test.go`

- One-line update at line 284 to pass `version` to `roomcrypto.Encode`. Behavior unchanged — the TypeScript client still decrypts using the same key.

### `broadcast-worker/handler_test.go`

**Mock generation:** add a `//go:generate mockgen` directive for `RoomKeyProvider`. The output file will follow whatever pattern other services in the repo use (single mock file vs. per-interface), confirmed at implementation time.

**Test fixture:**

```go
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
```

**Decryption helper for tests:**

```go
// decryptClientMessage parses data as a RoomEvent, decrypts the EncryptedMessage
// field to recover the ClientMessage, and returns both. Tests can assert metadata
// fields on the RoomEvent directly (plaintext) and message content via the decrypted ClientMessage.
func decryptClientMessage(t *testing.T, data []byte, key *roomkeystore.VersionedKeyPair) (model.RoomEvent, *model.ClientMessage) {
    t.Helper()
    var evt model.RoomEvent
    require.NoError(t, json.Unmarshal(data, &evt))
    require.Nil(t, evt.Message, "Message must be nil when EncryptedMessage is set")
    require.NotEmpty(t, evt.EncryptedMessage, "EncryptedMessage must be populated")

    var env roomcrypto.EncryptedMessage
    require.NoError(t, json.Unmarshal(evt.EncryptedMessage, &env))
    require.Equal(t, key.Version, env.Version)

    plaintext, err := decryptForTest(&env, key.KeyPair.PrivateKey)
    require.NoError(t, err)

    var msg model.ClientMessage
    require.NoError(t, json.Unmarshal([]byte(plaintext), &msg))
    return evt, &msg
}
```

`decryptForTest` is a ~30-line stdlib-only inverse of `roomcrypto.encode` (ECDH + HKDF + AES-GCM Open), kept inside the test file so `pkg/roomcrypto` does not gain a public `Decode` API.

**Existing group-room tests** (`TestHandler_HandleMessage_GroupRoom`):
- Each subtest registers a `MockRoomKeyProvider` expecting one `Get(ctx, "room-1")` call returning `testRoomKey(t)`.
- Replace `decodeRoomEvent(t, data)` with `decryptClientMessage(t, data, key)`. Metadata assertions (`evt.Type`, `evt.RoomID`, `evt.RoomName`, `evt.Mentions`, etc.) use the returned `RoomEvent` directly (plaintext). Message content assertions (`msg.ID`, `msg.Sender`, etc.) use the returned `*ClientMessage`.

**New encryption-specific cases** (added to `TestHandler_HandleMessage_Errors` or a new `TestHandler_HandleMessage_Encryption`):

| Case | Setup | Expect |
|---|---|---|
| keystore returns nil | `Get` returns `(nil, nil)` | error wrapping `"no current key"`, no publish |
| keystore returns error | `Get` returns `(nil, errors.New("valkey down"))` | error wrapping `"get room key"`, no publish |
| DM path skips key fetch | DM room handler call | `MockRoomKeyProvider` has zero expectations |
| published event has encrypted message | happy path | `RoomEvent.Message` is nil, `RoomEvent.EncryptedMessage` is populated, metadata fields are plaintext |
| decrypted message matches original | happy path | decrypted `ClientMessage` has correct ID, Content, Sender |
| version round-trips | happy path | `env.Version == key.Version` |

**`TestHandler_HandleMessage_DMRoom` updates:**
- `MockRoomKeyProvider` with zero expectations — gomock fails the test if `Get` is called.
- Continue decoding DM published events as plaintext `RoomEvent`; no encryption assertions.

**Existing failure-path tests in `TestHandler_HandleMessage_Errors`:**
- "room not found", "update room fails", "set subscription mentions fails", "unknown room type" — none reach `publishGroupEvent`, so they get a `MockRoomKeyProvider` with zero expectations and otherwise unchanged.

### `broadcast-worker/integration_test.go`

The current integration test uses a real Mongo container plus an in-memory `recordingPublisher`. Adding a real Valkey container just for one field would be disproportionate.

Add a small in-memory fake next to `recordingPublisher`:

```go
type fakeRoomKeyProvider struct {
    pair *roomkeystore.VersionedKeyPair
}

func (f *fakeRoomKeyProvider) Get(_ context.Context, _ string) (*roomkeystore.VersionedKeyPair, error) {
    return f.pair, nil
}
```

All three group-room tests (`TestBroadcastWorker_GroupRoom_Integration`, `TestBroadcastWorker_GroupRoom_MentionAll_Integration`, `TestBroadcastWorker_GroupRoom_IndividualMention_Integration`):
- Generate a P-256 key pair in setup
- Wrap it in `&fakeRoomKeyProvider{pair: ...}` and pass to `NewHandler`
- Parse published records as `RoomEvent` (metadata in plaintext); decrypt `EncryptedMessage` field via the shared `decryptClientMessage` helper to verify the `ClientMessage` content

`TestBroadcastWorker_DMRoom_Integration`:
- Constructs `&fakeRoomKeyProvider{pair: nil}` — verifies the DM path doesn't even need a real key
- Continues to decode published records as plaintext `RoomEvent`

### Coverage

- `pkg/roomcrypto`: one new field, one new param, one new test case → maintains existing coverage.
- `broadcast-worker`: every new branch in `publishGroupEvent` (success, keystore error, nil key, encrypt error) has an explicit test case per the table above.

## Failure Modes & Operational Considerations

| Failure | Behavior | Recovery |
|---|---|---|
| Valkey unreachable / network error | `keyStore.Get` returns error → wrapped error → NAK → `slog.Error` with `roomID`, `siteID` | JetStream redelivers; retries until Valkey is back |
| Room has no current key in Valkey | `Get` returns `(nil, nil)` → `"no current key"` error → NAK | Operator provisions the room key out-of-band; redelivery picks it up |
| `roomcrypto.Encode` fails (key parse, ECDH) | Wrapped error → NAK → `slog.Error` | Indicates corrupt key in Valkey; redelivery keeps failing until replaced |
| `json.Marshal(*EncryptedMessage)` fails | Should be unreachable; still wrapped and NAK'd | N/A |
| `Publisher.Publish` fails | Existing behavior — wrapped error → NAK | Existing behavior unchanged |

**Operational notes:**

- **Startup ordering:** broadcast-worker now requires Valkey at startup. If Valkey is down, `roomkeystore.NewValkeyStore` fails its ping and the process exits non-zero — consistent with how the worker handles Mongo and NATS today.
- **No silent degradation:** there is no plaintext fallback. If Valkey or the room key is unavailable, group-room broadcasts stall (NAK + redelivery) until the dependency is restored.
- **Latency cost per group message:** one extra Valkey roundtrip. Sub-millisecond on a healthy connection; not a meaningful contribution to the per-message latency budget.
- **DM path is zero-cost:** the keystore is not consulted for `RoomTypeDM`. Existing DM throughput is unaffected.
- **Graceful shutdown:** `keyStore.Close()` runs after `nc.Drain()` so any in-flight publish completes before the underlying Redis client is torn down.
- **Observability:** missing-key and Valkey-error events show up as `slog.Error` lines with `roomID` and `siteID` fields. No new metrics in this change; a `broadcast_worker_room_key_fetch_failures_total` counter would be a follow-up if needed.
