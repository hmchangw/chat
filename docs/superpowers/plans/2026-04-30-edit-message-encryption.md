# Edit Message Encryption — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Encrypt the `NewMsg` field in `MessageEditedEvent` using the room's Valkey-backed key pair before publishing to NATS, matching the encryption pattern broadcast-worker uses for new messages.

**Architecture:** `history-service` gains a `RoomKeyProvider` interface (narrow `Get`-only contract, mirroring broadcast-worker) and a Valkey connection. The `EditMessage` handler calls a private `encryptEditMsg` helper after the Cassandra write: if a key exists the helper calls `roomcrypto.Encode`, serialises the result into `EncryptedNewMsg`, and clears `NewMsg`; if no key is found (nil) or any step errors, it falls back to plaintext `NewMsg` with a `slog.Warn` — the Cassandra write already succeeded and the publish is best-effort, so graceful fallback is preferable to silently dropping the event. Cassandra continues to store plaintext; encryption is publish-time only, consistent with broadcast-worker.

**Tech Stack:** Go 1.25, `pkg/roomkeystore` (Valkey-backed), `pkg/roomcrypto` (ECDH+AES-GCM), `go.uber.org/mock` for tests, `crypto/ecdh` + `crypto/rand` in test helpers.

---

## File Map

| File | Change |
|------|--------|
| `history-service/internal/config/config.go` | Add `ValkeyConfig` struct; add `Valkey ValkeyConfig` to top-level `Config` |
| `history-service/internal/service/service.go` | Add `RoomKeyProvider` interface; add `keyProvider` field to `HistoryService`; update `New()` signature; update `//go:generate` |
| `history-service/internal/service/mocks/mock_repository.go` | Regenerated — adds `MockRoomKeyProvider` |
| `history-service/internal/models/event.go` | Add `EncryptedNewMsg json.RawMessage` to `MessageEditedEvent`; add `omitempty` to `NewMsg` |
| `history-service/internal/service/messages.go` | Add `encryptEditMsg` private helper; update `EditMessage` to call it; add imports |
| `history-service/internal/service/messages_test.go` | Update `newService()` return signature; update all call sites; add `keys.Get` expectations to existing EditMessage tests; add two new encryption tests |
| `history-service/cmd/main.go` | Add Valkey import, `NewValkeyStore` construction, pass to `service.New`, add to shutdown |
| `history-service/deploy/docker-compose.yml` | Add `valkey` service; add `VALKEY_ADDR` env var to `history-service` |

---

## Task 1: Add `ValkeyConfig` to history-service config

**Files:**
- Modify: `history-service/internal/config/config.go`

- [ ] **Step 1: Read the current file**

  Open `history-service/internal/config/config.go`. You will add a `ValkeyConfig` struct and a `Valkey` field to `Config`.

- [ ] **Step 2: Add ValkeyConfig**

  Replace the `Config` struct (keep the existing structs; only add what's shown):

  ```go
  // ValkeyConfig holds Valkey (Redis-compatible) connection settings.
  // Env vars: VALKEY_ADDR, VALKEY_PASSWORD
  type ValkeyConfig struct {
  	Addr     string `env:"ADDR" required:"true"`
  	Password string `env:"PASSWORD" envDefault:""`
  }

  // Config is the top-level configuration for history-service.
  type Config struct {
  	SiteID    string          `env:"SITE_ID" envDefault:"site-local"`
  	Cassandra CassandraConfig `envPrefix:"CASSANDRA_"`
  	Mongo     MongoConfig     `envPrefix:"MONGO_"`
  	NATS      NATSConfig      `envPrefix:"NATS_"`
  	Valkey    ValkeyConfig    `envPrefix:"VALKEY_"`
  }
  ```

- [ ] **Step 3: Verify compilation**

  ```bash
  make build SERVICE=history-service
  ```

  Expected: build succeeds. `VALKEY_ADDR` is now required at startup.

- [ ] **Step 4: Commit**

  ```bash
  git add history-service/internal/config/config.go
  git commit -m "feat(config): add Valkey config to history-service"
  ```

---

## Task 2: Add `RoomKeyProvider` interface, update `HistoryService`, regenerate mocks

**Files:**
- Modify: `history-service/internal/service/service.go`
- Regenerate: `history-service/internal/service/mocks/mock_repository.go`

- [ ] **Step 1: Add `RoomKeyProvider` interface and update `HistoryService` in `service.go`**

  In `history-service/internal/service/service.go`:

  1. Add import `"github.com/hmchangw/chat/pkg/roomkeystore"` to the import block.

  2. Update the `//go:generate` directive (add `RoomKeyProvider` to the comma-separated list):

     ```go
     //go:generate mockgen -destination=mocks/mock_repository.go -package=mocks . MessageReader,MessageWriter,SubscriptionRepository,EventPublisher,ThreadRoomRepository,RoomKeyProvider
     ```

  3. Add the interface after `ThreadRoomRepository`:

     ```go
     // RoomKeyProvider fetches the current encryption key for a room.
     // Defined here (not imported from pkg/roomkeystore directly) to keep the
     // dependency contract narrow — only Get is used by history-service.
     type RoomKeyProvider interface {
     	Get(ctx context.Context, roomID string) (*roomkeystore.VersionedKeyPair, error)
     }
     ```

  4. Add `keyProvider RoomKeyProvider` to the `HistoryService` struct:

     ```go
     type HistoryService struct {
     	msgReader     MessageReader
     	msgWriter     MessageWriter
     	subscriptions SubscriptionRepository
     	publisher     EventPublisher
     	threadRooms   ThreadRoomRepository
     	keyProvider   RoomKeyProvider
     }
     ```

  5. Update `New()` to accept and store `keyProvider`:

     ```go
     func New(msgs MessageRepository, subs SubscriptionRepository, pub EventPublisher, threadRooms ThreadRoomRepository, keyProvider RoomKeyProvider) *HistoryService {
     	return &HistoryService{
     		msgReader:     msgs,
     		msgWriter:     msgs,
     		subscriptions: subs,
     		publisher:     pub,
     		threadRooms:   threadRooms,
     		keyProvider:   keyProvider,
     	}
     }
     ```

- [ ] **Step 2: Regenerate mocks**

  ```bash
  make generate SERVICE=history-service
  ```

  Expected: `history-service/internal/service/mocks/mock_repository.go` is updated with `MockRoomKeyProvider`.

- [ ] **Step 3: Verify compilation**

  ```bash
  make build SERVICE=history-service
  ```

  Expected: **build fails** — `service.New(...)` call in `cmd/main.go` no longer matches the signature (missing 5th argument). That is expected at this point; `main.go` will be fixed in Task 7.

  Verify the error is only about `main.go`, not about any logic file:

  ```bash
  make build SERVICE=history-service 2>&1 | grep -v "^#"
  ```

  Expected output is one line like: `history-service/cmd/main.go:73:23: too few arguments in call to service.New`

- [ ] **Step 4: Commit (broken build is intentional — Task 7 will fix it)**

  ```bash
  git add history-service/internal/service/service.go \
          history-service/internal/service/mocks/mock_repository.go
  git commit -m "feat(service): add RoomKeyProvider interface to history-service"
  ```

---

## Task 3: Add `EncryptedNewMsg` to `MessageEditedEvent`

**Files:**
- Modify: `history-service/internal/models/event.go`

- [ ] **Step 1: Update `MessageEditedEvent`**

  Replace the struct in `history-service/internal/models/event.go`:

  ```go
  // MessageEditedEvent is the live event published to chat.room.{roomID}.event
  // after a successful edit. For channel rooms the edit content is encrypted via
  // roomcrypto.Encode and placed in EncryptedNewMsg (NewMsg is empty). For rooms
  // without a key (DM or key not yet provisioned), NewMsg carries the plaintext.
  // Per CLAUDE.md, every NATS event carries a Timestamp (event publish time).
  type MessageEditedEvent struct {
  	Type            string          `json:"type"`                        // always "message_edited"
  	Timestamp       int64           `json:"timestamp"`                   // UTC millis, event publish time
  	RoomID          string          `json:"roomId"`
  	MessageID       string          `json:"messageId"`
  	NewMsg          string          `json:"newMsg,omitempty"`            // plaintext; empty when EncryptedNewMsg is set
  	EncryptedNewMsg json.RawMessage `json:"encryptedNewMsg,omitempty"`   // roomcrypto.EncryptedMessage JSON; set for encrypted rooms
  	EditedBy        string          `json:"editedBy"`                    // actor account
  	EditedAt        int64           `json:"editedAt"`                    // UTC millis, domain time
  }
  ```

  Add `"encoding/json"` to the import block of `event.go` if not already present. Since the file currently has no imports (models package), add:

  ```go
  import "encoding/json"
  ```

- [ ] **Step 2: Verify compilation**

  ```bash
  make build SERVICE=history-service
  ```

  Expected: same error as after Task 2 (only `main.go` argument count). No new errors.

- [ ] **Step 3: Commit**

  ```bash
  git add history-service/internal/models/event.go
  git commit -m "feat(models): add EncryptedNewMsg field to MessageEditedEvent"
  ```

---

## Task 4: Update test helper and all call sites in `messages_test.go`

This task is purely mechanical — no behaviour changes; all existing tests must still pass after it.

**Files:**
- Modify: `history-service/internal/service/messages_test.go`

- [ ] **Step 1: Add `roomkeystore` import**

  In the import block of `messages_test.go`, add:

  ```go
  "crypto/ecdh"
  "crypto/rand"

  "github.com/hmchangw/chat/history-service/internal/service/mocks"
  "github.com/hmchangw/chat/pkg/roomkeystore"
  ```

  (`mocks` is already imported; `roomkeystore` and the crypto packages are new.)

- [ ] **Step 2: Add `generateTestKeyPair` helper**

  After the existing `ptrTime` helper (around line 33), add:

  ```go
  // generateTestKeyPair creates a real P-256 key pair for encryption tests.
  func generateTestKeyPair(t *testing.T) *roomkeystore.VersionedKeyPair {
  	t.Helper()
  	privKey, err := ecdh.P256().GenerateKey(rand.Reader)
  	require.NoError(t, err)
  	return &roomkeystore.VersionedKeyPair{
  		Version: 1,
  		KeyPair: roomkeystore.RoomKeyPair{
  			PublicKey:  privKey.PublicKey().Bytes(), // 65-byte uncompressed P-256 point
  			PrivateKey: privKey.Bytes(),              // 32-byte scalar
  		},
  	}
  }
  ```

- [ ] **Step 3: Update `newService()` to return 6 values**

  Replace the existing `newService` function:

  ```go
  func newService(t *testing.T) (*service.HistoryService, *mocks.MockMessageRepository, *mocks.MockSubscriptionRepository, *mocks.MockEventPublisher, *mocks.MockThreadRoomRepository, *mocks.MockRoomKeyProvider) {
  	ctrl := gomock.NewController(t)
  	msgs := mocks.NewMockMessageRepository(ctrl)
  	subs := mocks.NewMockSubscriptionRepository(ctrl)
  	pub := mocks.NewMockEventPublisher(ctrl)
  	threadRooms := mocks.NewMockThreadRoomRepository(ctrl)
  	keys := mocks.NewMockRoomKeyProvider(ctrl)
  	return service.New(msgs, subs, pub, threadRooms, keys), msgs, subs, pub, threadRooms, keys
  }
  ```

- [ ] **Step 4: Update all call sites — non-EditMessage tests**

  Every call site currently reads one of these patterns:
  ```go
  svc, msgs, subs, _, _ := newService(t)
  svc, msgs, subs, pub, _ := newService(t)
  svc, _, subs, _, _ := newService(t)
  ```

  Add a sixth blank identifier to each. Example transformation:
  ```go
  // Before:
  svc, msgs, subs, _, _ := newService(t)
  // After:
  svc, msgs, subs, _, _, _ := newService(t)
  ```

  Apply this to every test function that **does not** call `EditMessage`. That covers: all `LoadHistory`, `LoadNextMessages`, `GetMessageByID`, `LoadSurroundingMessages`, access-control, `QuoteRedact`, `TShow`, and `DeleteMessage` tests.

  Tip — run a quick grep to find every call site:
  ```bash
  grep -n "newService(t)" history-service/internal/service/messages_test.go
  ```

- [ ] **Step 5: Update the two existing EditMessage tests that reach the publish step**

  These are `TestHistoryService_EditMessage_Success` and `TestHistoryService_EditMessage_PublishFails`.

  For both, change the opening destructure to capture `keys`:
  ```go
  svc, msgs, subs, pub, _, keys := newService(t)
  ```

  And add a `keys.EXPECT()` line immediately before the `pub.EXPECT()` block (nil key → plaintext, no behaviour change):
  ```go
  keys.EXPECT().Get(gomock.Any(), "r1").Return(nil, nil)
  ```

  The rest of each test (including the `assert.Equal(t, "new content", evt.NewMsg)` assertion in `TestHistoryService_EditMessage_Success`) stays unchanged — nil key means the event carries `NewMsg` as plaintext.

  For all other existing EditMessage tests (NotSubscribed, NotSender, NotFound, WrongRoom, AlreadyDeleted, EmptyNewMsg, TooLarge, UpdateFails): only add the sixth blank identifier — they return before the key-fetch step, so no `keys.EXPECT()` is needed.

- [ ] **Step 6: Run the test suite (all existing tests must pass)**

  ```bash
  make test SERVICE=history-service
  ```

  Expected: all existing tests pass. The `service.New` signature change has not been fixed in `main.go` yet, but unit tests don't compile `main.go`.

---

## Task 5: Write failing tests for encryption paths (Red)

**Files:**
- Modify: `history-service/internal/service/messages_test.go`

- [ ] **Step 1: Add `TestHistoryService_EditMessage_Success_EncryptedEvent`**

  Append after `TestHistoryService_EditMessage_PublishFails`:

  ```go
  // TestHistoryService_EditMessage_Success_EncryptedEvent verifies that when a
  // room key exists the handler places the encrypted payload in EncryptedNewMsg
  // and leaves NewMsg empty.
  func TestHistoryService_EditMessage_Success_EncryptedEvent(t *testing.T) {
  	svc, msgs, subs, pub, _, keys := newService(t)
  	c := testContext()

  	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
  	hydrated := &models.Message{
  		MessageID: "m-abc",
  		RoomID:    "r1",
  		Sender:    models.Participant{Account: "u1"},
  	}
  	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)
  	msgs.EXPECT().UpdateMessageContent(gomock.Any(), hydrated, "secret edit", gomock.Any()).Return(nil)

  	kp := generateTestKeyPair(t)
  	keys.EXPECT().Get(gomock.Any(), "r1").Return(kp, nil)

  	pub.EXPECT().
  		Publish(gomock.Any(), "chat.room.r1.event", gomock.Any()).
  		DoAndReturn(func(_ context.Context, _ string, data []byte) error {
  			var evt models.MessageEditedEvent
  			require.NoError(t, json.Unmarshal(data, &evt))
  			assert.Equal(t, "message_edited", evt.Type)
  			assert.Empty(t, evt.NewMsg, "NewMsg must be empty when EncryptedNewMsg is set")
  			assert.NotEmpty(t, evt.EncryptedNewMsg, "EncryptedNewMsg must be populated when a room key exists")
  			// Verify the encrypted payload is valid JSON (roomcrypto.EncryptedMessage).
  			var enc map[string]interface{}
  			assert.NoError(t, json.Unmarshal(evt.EncryptedNewMsg, &enc), "EncryptedNewMsg must be valid JSON")
  			return nil
  		})

  	resp, err := svc.EditMessage(c, models.EditMessageRequest{MessageID: "m-abc", NewMsg: "secret edit"})
  	require.NoError(t, err)
  	assert.Equal(t, "m-abc", resp.MessageID)
  	assert.NotZero(t, resp.EditedAt)
  }
  ```

- [ ] **Step 2: Add `TestHistoryService_EditMessage_Success_KeyFetchError`**

  ```go
  // TestHistoryService_EditMessage_Success_KeyFetchError verifies that when the
  // key store returns an error the handler falls back to plaintext NewMsg and
  // still publishes the event (best-effort; the Cassandra write already succeeded).
  func TestHistoryService_EditMessage_Success_KeyFetchError(t *testing.T) {
  	svc, msgs, subs, pub, _, keys := newService(t)
  	c := testContext()

  	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
  	hydrated := &models.Message{
  		MessageID: "m-abc",
  		RoomID:    "r1",
  		Sender:    models.Participant{Account: "u1"},
  	}
  	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)
  	msgs.EXPECT().UpdateMessageContent(gomock.Any(), hydrated, "some edit", gomock.Any()).Return(nil)

  	keys.EXPECT().Get(gomock.Any(), "r1").Return(nil, fmt.Errorf("valkey connection refused"))

  	// Handler must still publish despite the key error — edit is already persisted.
  	pub.EXPECT().
  		Publish(gomock.Any(), "chat.room.r1.event", gomock.Any()).
  		DoAndReturn(func(_ context.Context, _ string, data []byte) error {
  			var evt models.MessageEditedEvent
  			require.NoError(t, json.Unmarshal(data, &evt))
  			assert.Equal(t, "some edit", evt.NewMsg, "must fall back to plaintext on key error")
  			assert.Empty(t, evt.EncryptedNewMsg)
  			return nil
  		})

  	resp, err := svc.EditMessage(c, models.EditMessageRequest{MessageID: "m-abc", NewMsg: "some edit"})
  	require.NoError(t, err)
  	assert.Equal(t, "m-abc", resp.MessageID)
  }
  ```

- [ ] **Step 3: Run the new tests to confirm they FAIL (Red)**

  ```bash
  cd history-service && go test ./internal/service/... -run "TestHistoryService_EditMessage_Success_EncryptedEvent|TestHistoryService_EditMessage_Success_KeyFetchError" -v 2>&1 | tail -20
  ```

  Expected: both tests FAIL — `encryptEditMsg` does not exist yet, so the current `EditMessage` always sets `NewMsg`, violating the `assert.Empty(t, evt.NewMsg)` assertion in the first test.

---

## Task 6: Implement `encryptEditMsg` and update `EditMessage` (Green)

**Files:**
- Modify: `history-service/internal/service/messages.go`

- [ ] **Step 1: Add imports to `messages.go`**

  Add these two lines to the import block:

  ```go
  "github.com/hmchangw/chat/pkg/roomcrypto"
  "github.com/hmchangw/chat/pkg/roomkeystore"
  ```

  The full import block becomes:

  ```go
  import (
  	"encoding/json"
  	"log/slog"
  	"strings"
  	"time"

  	"github.com/hmchangw/chat/history-service/internal/cassrepo"
  	"github.com/hmchangw/chat/history-service/internal/models"
  	"github.com/hmchangw/chat/pkg/natsrouter"
  	"github.com/hmchangw/chat/pkg/roomcrypto"
  	"github.com/hmchangw/chat/pkg/roomkeystore"
  	"github.com/hmchangw/chat/pkg/subject"
  )
  ```

  (The `roomkeystore` import is needed because `encryptEditMsg` accesses `key.KeyPair.PublicKey` and `key.Version` — fields of `*roomkeystore.VersionedKeyPair`.)

- [ ] **Step 2: Add `encryptEditMsg` private helper**

  Add this function before `EditMessage` in `messages.go`:

  ```go
  // encryptEditMsg attempts to encrypt plaintext using the room's current key.
  // Returns ("", encryptedJSON) on success. Returns (plaintext, nil) on any
  // failure (no key, key fetch error, encode error) so the caller can fall back
  // to a plaintext event — the Cassandra write already succeeded.
  func (s *HistoryService) encryptEditMsg(c *natsrouter.Context, roomID, plaintext string) (string, json.RawMessage) {
  	key, err := s.keyProvider.Get(c, roomID)
  	if err != nil {
  		slog.Warn("edit: get room key failed, event will be plaintext", "error", err, "roomID", roomID)
  		return plaintext, nil
  	}
  	if key == nil {
  		return plaintext, nil
  	}
  	encrypted, err := roomcrypto.Encode(plaintext, key.KeyPair.PublicKey, key.Version)
  	if err != nil {
  		slog.Warn("edit: encrypt failed, event will be plaintext", "error", err, "roomID", roomID)
  		return plaintext, nil
  	}
  	encJSON, err := json.Marshal(encrypted)
  	if err != nil {
  		slog.Warn("edit: marshal encrypted message failed, event will be plaintext", "error", err, "roomID", roomID)
  		return plaintext, nil
  	}
  	return "", json.RawMessage(encJSON)
  }
  ```

- [ ] **Step 3: Update the event-building block in `EditMessage`**

  Replace the existing event construction + publish block in `EditMessage` (lines ~252–269):

  ```go
  	// Publish live event (best-effort — publish failure is logged, not returned).
  	editedAtMs := editedAt.UnixMilli()
  	plainMsg, encMsg := s.encryptEditMsg(c, roomID, req.NewMsg)
  	evt := models.MessageEditedEvent{
  		Type:            "message_edited",
  		Timestamp:       editedAtMs,
  		RoomID:          roomID,
  		MessageID:       req.MessageID,
  		NewMsg:          plainMsg,
  		EncryptedNewMsg: encMsg,
  		EditedBy:        account,
  		EditedAt:        editedAtMs,
  	}
  	if payload, err := json.Marshal(evt); err == nil {
  		if pubErr := s.publisher.Publish(c, subject.RoomEvent(roomID), payload); pubErr != nil {
  			slog.Warn("edit: publish event failed", "error", pubErr, "messageID", req.MessageID)
  		}
  	} else {
  		slog.Warn("edit: marshal event failed", "error", err, "messageID", req.MessageID)
  	}
  ```

- [ ] **Step 4: Run the failing tests to confirm they now PASS (Green)**

  ```bash
  cd history-service && go test ./internal/service/... -run "TestHistoryService_EditMessage_Success_EncryptedEvent|TestHistoryService_EditMessage_Success_KeyFetchError" -v 2>&1 | tail -10
  ```

  Expected: both tests PASS.

- [ ] **Step 5: Run the full service test suite**

  ```bash
  make test SERVICE=history-service
  ```

  Expected: all tests pass.

- [ ] **Step 6: Commit**

  ```bash
  git add history-service/internal/service/messages.go \
          history-service/internal/service/messages_test.go
  git commit -m "feat(service): encrypt NewMsg in MessageEditedEvent for history-service"
  ```

---

## Task 7: Wire Valkey in `main.go`

**Files:**
- Modify: `history-service/cmd/main.go`

- [ ] **Step 1: Add import**

  Add to the import block:

  ```go
  "github.com/hmchangw/chat/pkg/roomkeystore"
  ```

- [ ] **Step 2: Add Valkey construction after the Cassandra connection block**

  After `cassutil.Connect(...)` and before `cassRepo := cassrepo.NewRepository(cassSession)`, add:

  ```go
  keyStore, err := roomkeystore.NewValkeyStore(roomkeystore.Config{
  	Addr:        cfg.Valkey.Addr,
  	Password:    cfg.Valkey.Password,
  	GracePeriod: 0, // history-service never rotates keys; grace period is irrelevant
  })
  if err != nil {
  	slog.Error("valkey connect failed", "error", err)
  	os.Exit(1)
  }
  ```

- [ ] **Step 3: Pass `keyStore` to `service.New`**

  Change the `service.New` call:

  ```go
  svc := service.New(cassRepo, subRepo, pub, threadRoomRepo, keyStore)
  ```

- [ ] **Step 4: Add Valkey close to the shutdown chain**

  In `shutdown.Wait(...)`, add the keyStore closer as the last entry before the database disconnects:

  ```go
  shutdown.Wait(ctx, 25*time.Second,
  	func(ctx context.Context) error { return router.Shutdown(ctx) },
  	func(ctx context.Context) error { return nc.Drain() },
  	func(ctx context.Context) error { return tracerShutdown(ctx) },
  	func(ctx context.Context) error { return keyStore.Close() },
  	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
  	func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
  )
  ```

- [ ] **Step 5: Verify compilation**

  ```bash
  make build SERVICE=history-service
  ```

  Expected: build succeeds with no errors.

- [ ] **Step 6: Commit**

  ```bash
  git add history-service/cmd/main.go
  git commit -m "feat(main): wire Valkey keyStore in history-service"
  ```

---

## Task 8: Update `docker-compose.yml` for local development

**Files:**
- Modify: `history-service/deploy/docker-compose.yml`

- [ ] **Step 1: Add Valkey service and env var**

  Replace the file content with:

  ```yaml
  name: history-service

  services:
    valkey:
      image: valkey/valkey:8-alpine
      networks:
        - chat-local

    history-service:
      build:
        context: ../..
        dockerfile: history-service/deploy/Dockerfile
      environment:
        - NATS_URL=nats://nats:4222
        - NATS_CREDS_FILE=/etc/nats/backend.creds
        - SITE_ID=site-local
        - MONGO_URI=mongodb://mongodb:27017
        - MONGO_DB=chat
        - CASSANDRA_HOSTS=cassandra
        - CASSANDRA_KEYSPACE=chat
        - VALKEY_ADDR=valkey:6379
      volumes:
        - ../../docker-local/backend.creds:/etc/nats/backend.creds:ro
      networks:
        - chat-local

  networks:
    chat-local:
      external: true
  ```

- [ ] **Step 2: Run the full test suite one final time**

  ```bash
  make test SERVICE=history-service
  ```

  Expected: all tests pass.

- [ ] **Step 3: Run lint**

  ```bash
  make lint
  ```

  Expected: no errors.

- [ ] **Step 4: Commit**

  ```bash
  git add history-service/deploy/docker-compose.yml
  git commit -m "chore(deploy): add Valkey to history-service docker-compose"
  ```

- [ ] **Step 5: Push**

  ```bash
  git push -u origin claude/review-spec-plan-2b31C
  ```

---

## Self-Review

**Spec coverage:**

| Requirement | Task |
|---|---|
| Encrypt `NewMsg` in `MessageEditedEvent` for channel rooms | Task 6 |
| Encrypt for DM rooms too (key-exists check, not room-type check) | Task 6 — nil key = no encryption, key present = encrypt regardless of room type |
| Valkey required at startup (fail fast like broadcast-worker) | Task 7 |
| Plaintext fallback if key fetch fails (best-effort publish) | Task 6 `encryptEditMsg` |
| `EncryptedNewMsg` field on event + `NewMsg` omitempty | Task 3 |
| Cassandra content stays plaintext | No Cassandra write changes — confirmed by absence of cassrepo edits |
| Unit tests for encrypted path | Task 5 |
| Unit tests for key-error fallback path | Task 5 |
| docker-compose updated | Task 8 |

**Placeholder scan:** None found.

**Type consistency:**
- `RoomKeyProvider.Get` returns `*roomkeystore.VersionedKeyPair` — matches `encryptEditMsg` usage of `key.KeyPair.PublicKey` and `key.Version`
- `MockRoomKeyProvider` generated from the interface — matches test `keys.EXPECT().Get(...)` calls
- `models.MessageEditedEvent.EncryptedNewMsg` is `json.RawMessage` — matches `json.Marshal(encrypted)` result assigned to it
