# Message At-Rest Encryption Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add server-side at-rest envelope encryption of Cassandra-stored message content using a versioned KEK (from a Kubernetes Secret) wrapping per-room DEKs (in MongoDB), with the encrypt/decrypt logic isolated in a new `pkg/atrest` library and consumed by message-worker (write) and history-service (read/edit/delete).

**Architecture:** New `pkg/atrest` library exposes a `Cipher` that hides KEK loading, DEK storage in MongoDB, an in-process LRU cache of unwrapped DEKs, and AES-256-GCM JSON-payload encryption. The Cassandra schema gains a bundled `enc_payload blob` column plus an `enc_meta` UDT (nonce-only); legacy plaintext columns remain for backward compatibility. message-worker calls `Encrypt` before insert; history-service branches on `enc_payload IS NOT NULL` to decide whether to decrypt.

**Tech Stack:** Go 1.25, `crypto/aes` + `crypto/cipher` (AES-256-GCM), `crypto/rand`, `encoding/json`, `go.mongodb.org/mongo-driver/v2`, `github.com/gocql/gocql`, `caarlos0/env`, `go.uber.org/mock`, `stretchr/testify`, `testcontainers-go`, `container/list` (stdlib LRU).

**Spec:** `docs/superpowers/specs/2026-05-05-message-at-rest-encryption-design.md`

**Branch:** `claude/add-message-encryption-XaYfk`

---

## Conventions used in this plan

- Every file path is absolute from the repo root.
- Test files run via `make test SERVICE=<name>` for service tests, or `go test ./pkg/atrest/...` for package tests, but the canonical project runner is `make test`. Integration tests use `make test-integration SERVICE=<name>` or `go test -tags=integration ./pkg/atrest/...`.
- After every implementation step, run `make lint` once before committing if the step changed Go code.
- Commits are small and frequent. Each task ends with a commit; some tasks have intermediate commits between Red and Green if the test file is large enough that the failing-test commit is independently reviewable.

---

## File Structure

**New files:**

```
pkg/atrest/
    atrest.go                  # Public types: EncryptedFields, QuotedParentEncrypted, EncMeta, RoomDataKey; sentinel errors; Config
    kek_loader.go              # KEKLoader interface + file-backed impl with poll-based reload
    kek_loader_test.go         # Unit tests
    dek_store.go               # DEKStore interface + Mongo impl + //go:generate mockgen directive
    dek_store_test.go          # Unit tests (with fake collection)
    cache.go                   # Unexported LRU keyed by roomID -> []byte (unwrapped DEK)
    cache_test.go              # Unit tests
    cipher.go                  # Cipher interface + impl composing KEKLoader + DEKStore + cache
    cipher_test.go             # Unit tests with fakes/mocks
    mock_dek_store_test.go     # Generated mock (do not edit manually)
    integration_test.go        # //go:build integration; testcontainers Mongo + filesystem KEK
    testdata/
        valid_keks.json
        bad_keks_short_key.json
        bad_keks_missing_current.json
```

**Modified files:**

```
go.mod / go.sum                                       # No new deps; verify
docs/cassandra_message_model.md                       # New enc_meta UDT + enc_payload/enc_meta columns
docker-local/cassandra/init/*.cql                     # CREATE TYPE + ALTER TABLE statements
pkg/model/cassandra/message.go                        # Add EncMeta UDT struct, EncPayload + EncMeta fields on Message
pkg/model/cassandra/message_test.go                   # Round-trip test for new fields

message-worker/main.go                                # Wire AtrestConfig + KEKLoader + DEKStore + Cipher
message-worker/handler.go                             # Split metadata/encrypted fields; call Cipher.Encrypt
message-worker/handler_test.go                        # Inject fake Cipher; assert encrypted fields not written plaintext
message-worker/store.go                               # Add InsertEncryptedMessage to MessageStore
message-worker/store_cassandra.go                     # Implement InsertEncryptedMessage
message-worker/store_cassandra_test.go                # Update integration test
message-worker/mock_store_test.go                     # Regenerated via `make generate SERVICE=message-worker`

history-service/cmd/<entrypoint>.go                   # Wire AtrestConfig + Cipher
history-service/internal/cassrepo/<read>.go           # Add EncPayload/EncMeta to scan struct + decrypt branch
history-service/internal/cassrepo/<read>_test.go      # Hybrid path test
history-service/internal/service/messages.go          # Edit + delete paths re-encrypt
history-service/internal/service/messages_test.go     # Tests
history-service/integration_test.go                   # Hybrid plaintext + encrypted rows
```

The exact file names inside `history-service/cmd/` and `history-service/internal/cassrepo/` are determined by the existing layout and discovered at the start of Tasks 16-17 below before editing.

---

## Phase 1 — `pkg/atrest` library

### Task 1: Public types and sentinel errors

**Files:**
- Create: `pkg/atrest/atrest.go`

- [ ] **Step 1: Create the public types file**

```go
// Package atrest provides envelope encryption of message payloads at rest.
//
// Each room owns a single 256-bit Data Encryption Key (DEK) used with
// AES-256-GCM to encrypt a JSON-serialised payload. Each DEK is itself
// wrapped (encrypted) with a versioned Key Encryption Key (KEK) loaded from
// a Kubernetes Secret mounted as a JSON file. The wrapped DEK is stored in
// MongoDB; only the unwrapped form is held in process memory.
package atrest

import (
	"errors"
	"time"

	"github.com/hmchangw/chat/pkg/model/cassandra"
)

// EncryptedFields is the bundle of user-authored content that gets
// serialised, encrypted and stored in the Cassandra `enc_payload` column.
// Field names mirror the plaintext columns so that callers can construct
// this struct directly from a cassandra.Message.
type EncryptedFields struct {
	Msg                 string                 `json:"msg,omitempty"`
	Attachments         [][]byte               `json:"attachments,omitempty"`
	Card                *cassandra.Card        `json:"card,omitempty"`
	CardAction          *cassandra.CardAction  `json:"cardAction,omitempty"`
	SysMsgData          []byte                 `json:"sysMsgData,omitempty"`
	QuotedParentContent *QuotedParentEncrypted `json:"quotedParentContent,omitempty"`
}

// QuotedParentEncrypted holds the user-authored fields of a quoted parent
// message. Mentions, sender, timestamps and IDs stay plaintext on the
// quoted_parent_message UDT.
type QuotedParentEncrypted struct {
	Msg         string   `json:"msg,omitempty"`
	Attachments [][]byte `json:"attachments,omitempty"`
}

// EncMeta is the per-row metadata stored alongside the ciphertext.
// kek_version is intentionally absent: the authoritative KEK version is on
// the room's DEK row in MongoDB.
type EncMeta struct {
	Nonce []byte `cql:"nonce" json:"nonce"`
}

// RoomDataKey is the wrapped DEK record stored in MongoDB.
type RoomDataKey struct {
	ID         string    `bson:"_id"`
	WrappedDEK []byte    `bson:"wrappedDEK"`
	WrapNonce  []byte    `bson:"wrapNonce"`
	KEKVersion int       `bson:"kekVersion"`
	CreatedAt  time.Time `bson:"createdAt"`
}

// Config is parsed via caarlos0/env in each consuming service.
type Config struct {
	Enabled      bool          `env:"ATREST_ENABLED"        envDefault:"true"`
	KEKFile      string        `env:"ATREST_KEK_FILE"       envDefault:"/etc/chat/keks.json"`
	DEKCacheSize int           `env:"ATREST_DEK_CACHE_SIZE" envDefault:"10000"`
	DEKCacheTTL  time.Duration `env:"ATREST_DEK_CACHE_TTL"  envDefault:"1h"`
	ReloadEvery  time.Duration `env:"ATREST_KEK_RELOAD_EVERY" envDefault:"30s"`
}

// Sentinel errors. Callers use errors.Is to identify a class.
var (
	// ErrKEKVersionUnknown means a wrapped DEK references a KEK version
	// that is not present in the loaded key set.
	ErrKEKVersionUnknown = errors.New("atrest: KEK version unknown")
	// ErrAuthFailed means the GCM authentication tag did not validate
	// during decrypt — the ciphertext was tampered with or the wrong key
	// was used.
	ErrAuthFailed = errors.New("atrest: authentication failed")
	// ErrPayloadMalformed means JSON unmarshal of the decrypted payload
	// failed.
	ErrPayloadMalformed = errors.New("atrest: payload malformed")
	// ErrKEKFileInvalid means the KEK secret file failed schema or content
	// validation at load time.
	ErrKEKFileInvalid = errors.New("atrest: KEK file invalid")
)
```

> **Note:** Replace `github.com/hmchangw/chat` in the import path with the actual module path read from `go.mod` before pasting.

- [ ] **Step 2: Verify the package compiles**

Run: `go build ./pkg/atrest/...`
Expected: succeeds with no output.

- [ ] **Step 3: Run lint**

Run: `make lint`
Expected: passes.

- [ ] **Step 4: Commit**

```bash
git add pkg/atrest/atrest.go
git commit -m "feat(atrest): public types, config and sentinel errors"
```

---

### Task 2: KEKLoader — load and validate JSON key file

**Files:**
- Create: `pkg/atrest/kek_loader.go`
- Create: `pkg/atrest/kek_loader_test.go`
- Create: `pkg/atrest/testdata/valid_keks.json`
- Create: `pkg/atrest/testdata/bad_keks_short_key.json`
- Create: `pkg/atrest/testdata/bad_keks_missing_current.json`

- [ ] **Step 1: Create the test fixtures**

`pkg/atrest/testdata/valid_keks.json`:
```json
{
  "current": 2,
  "keys": {
    "1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
    "2": "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="
  }
}
```
(Both values base64-decode to exactly 32 ASCII bytes.)

`pkg/atrest/testdata/bad_keks_short_key.json`:
```json
{
  "current": 1,
  "keys": {
    "1": "c2hvcnQ="
  }
}
```
(Decodes to 5 bytes, not 32.)

`pkg/atrest/testdata/bad_keks_missing_current.json`:
```json
{
  "current": 9,
  "keys": {
    "1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
  }
}
```

- [ ] **Step 2: Write the failing tests**

`pkg/atrest/kek_loader_test.go`:
```go
package atrest

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFileKEKLoader_Valid(t *testing.T) {
	l, err := NewFileKEKLoader(filepath.Join("testdata", "valid_keks.json"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	ver, key := l.Current()
	assert.Equal(t, 2, ver)
	assert.Len(t, key, 32)

	k1, ok := l.ByVersion(1)
	assert.True(t, ok)
	assert.Len(t, k1, 32)

	_, ok = l.ByVersion(99)
	assert.False(t, ok)
}

func TestNewFileKEKLoader_ShortKey(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "bad_keks_short_key.json"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKFileInvalid))
}

func TestNewFileKEKLoader_MissingCurrent(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "bad_keks_missing_current.json"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKFileInvalid))
}

func TestNewFileKEKLoader_FileMissing(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "does_not_exist.json"))
	require.Error(t, err)
}
```

- [ ] **Step 3: Run tests; verify they fail with "NewFileKEKLoader undefined"**

Run: `go test ./pkg/atrest/... -run TestNewFileKEKLoader -v`
Expected: build error, `undefined: NewFileKEKLoader`.

- [ ] **Step 4: Implement the loader (no reload yet)**

`pkg/atrest/kek_loader.go`:
```go
package atrest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
)

// KEKLoader exposes the in-memory key set parsed from the secret file.
type KEKLoader interface {
	// Current returns the active KEK version and its 32-byte key.
	Current() (version int, key []byte)
	// ByVersion looks up a KEK by version. ok is false if absent.
	ByVersion(v int) (key []byte, ok bool)
	// Close releases any resources (e.g. the reload goroutine).
	Close() error
}

// fileKEKLoader is the file-backed implementation. Reload is added in a
// later task; this version reads once at construction.
type fileKEKLoader struct {
	path string

	mu      sync.RWMutex
	keys    map[int][]byte
	current int

	closeOnce sync.Once
	stop      chan struct{}
}

// NewFileKEKLoader reads and validates path, returning a loader holding
// the parsed key set in memory. Returns ErrKEKFileInvalid on schema or
// content failure.
func NewFileKEKLoader(path string) (KEKLoader, error) {
	keys, current, err := loadKEKFile(path)
	if err != nil {
		return nil, err
	}
	return &fileKEKLoader{
		path:    path,
		keys:    keys,
		current: current,
		stop:    make(chan struct{}),
	}, nil
}

func (l *fileKEKLoader) Current() (int, []byte) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.current, l.keys[l.current]
}

func (l *fileKEKLoader) ByVersion(v int) ([]byte, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	k, ok := l.keys[v]
	return k, ok
}

func (l *fileKEKLoader) Close() error {
	l.closeOnce.Do(func() { close(l.stop) })
	return nil
}

// fileFormat mirrors the on-disk JSON schema.
type fileFormat struct {
	Current int               `json:"current"`
	Keys    map[string]string `json:"keys"`
}

func loadKEKFile(path string) (map[int][]byte, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("reading KEK file: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, 0, fmt.Errorf("%w: parse JSON: %v", ErrKEKFileInvalid, err)
	}
	if len(f.Keys) == 0 {
		return nil, 0, fmt.Errorf("%w: keys is empty", ErrKEKFileInvalid)
	}
	out := make(map[int][]byte, len(f.Keys))
	versions := make([]int, 0, len(f.Keys))
	for vstr, b64 := range f.Keys {
		v, err := strconv.Atoi(vstr)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: version %q is not an integer", ErrKEKFileInvalid, vstr)
		}
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: version %d: base64 decode: %v", ErrKEKFileInvalid, v, err)
		}
		if len(key) != 32 {
			return nil, 0, fmt.Errorf("%w: version %d: key is %d bytes, want 32", ErrKEKFileInvalid, v, len(key))
		}
		out[v] = key
		versions = append(versions, v)
	}
	if _, ok := out[f.Current]; !ok {
		sort.Ints(versions)
		return nil, 0, fmt.Errorf("%w: current=%d not present in keys (have %v)", ErrKEKFileInvalid, f.Current, versions)
	}
	return out, f.Current, nil
}
```

- [ ] **Step 5: Run tests; verify they pass**

Run: `go test ./pkg/atrest/... -run TestNewFileKEKLoader -v`
Expected: 4 tests PASS.

- [ ] **Step 6: Lint**

Run: `make lint`

- [ ] **Step 7: Commit**

```bash
git add pkg/atrest/kek_loader.go pkg/atrest/kek_loader_test.go pkg/atrest/testdata/
git commit -m "feat(atrest): KEKLoader file parsing and validation"
```

---

### Task 3: KEKLoader — poll-based hot reload

**Files:**
- Modify: `pkg/atrest/kek_loader.go`
- Modify: `pkg/atrest/kek_loader_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/atrest/kek_loader_test.go`:
```go
func TestFileKEKLoader_HotReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keks.json")

	// Initial file: current=1.
	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 1,
		"keys": {"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}
	}`), 0o600))

	l, err := newFileKEKLoaderWithInterval(path, 20*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	ver, _ := l.Current()
	assert.Equal(t, 1, ver)

	// Rewrite with current=2.
	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 2,
		"keys": {
			"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
			"2": "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="
		}
	}`), 0o600))

	require.Eventually(t, func() bool {
		v, _ := l.Current()
		return v == 2
	}, 2*time.Second, 30*time.Millisecond)
}

func TestFileKEKLoader_BadReloadKeepsPrior(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 1,
		"keys": {"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}
	}`), 0o600))

	l, err := newFileKEKLoaderWithInterval(path, 20*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	// Rewrite with malformed JSON.
	require.NoError(t, os.WriteFile(path, []byte(`{ not json`), 0o600))

	// Wait long enough for at least one reload tick.
	time.Sleep(100 * time.Millisecond)

	ver, key := l.Current()
	assert.Equal(t, 1, ver)
	assert.Len(t, key, 32)
}
```

Add the missing import at the top of the test file: `"os"`, `"time"`.

- [ ] **Step 2: Run tests; verify they fail**

Run: `go test ./pkg/atrest/... -run TestFileKEKLoader -v`
Expected: build error, `undefined: newFileKEKLoaderWithInterval`.

- [ ] **Step 3: Add reload support**

Replace `NewFileKEKLoader` in `pkg/atrest/kek_loader.go` with:
```go
// NewFileKEKLoader reads and validates path, then starts a background
// goroutine that re-reads the file every 30 seconds. Reload failures
// retain the previous in-memory key set.
func NewFileKEKLoader(path string) (KEKLoader, error) {
	return newFileKEKLoaderWithInterval(path, 30*time.Second)
}

func newFileKEKLoaderWithInterval(path string, interval time.Duration) (KEKLoader, error) {
	keys, current, err := loadKEKFile(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat KEK file: %w", err)
	}
	l := &fileKEKLoader{
		path:    path,
		keys:    keys,
		current: current,
		modTime: info.ModTime(),
		stop:    make(chan struct{}),
	}
	go l.reloadLoop(interval)
	return l, nil
}
```

Add a `modTime time.Time` field to `fileKEKLoader` and the import `"time"`.

Add the reload loop method:
```go
func (l *fileKEKLoader) reloadLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.maybeReload()
		}
	}
}

func (l *fileKEKLoader) maybeReload() {
	info, err := os.Stat(l.path)
	if err != nil {
		// File temporarily unreadable; retain prior state and try again next tick.
		return
	}
	l.mu.RLock()
	prev := l.modTime
	l.mu.RUnlock()
	if !info.ModTime().After(prev) {
		return
	}
	keys, current, err := loadKEKFile(l.path)
	if err != nil {
		// Validation failed; retain prior state. A metric/log hook is added in a
		// later task when the observability wiring lands.
		return
	}
	l.mu.Lock()
	l.keys = keys
	l.current = current
	l.modTime = info.ModTime()
	l.mu.Unlock()
}
```

- [ ] **Step 4: Run tests; verify they pass**

Run: `go test ./pkg/atrest/... -run TestFileKEKLoader -v`
Expected: 6 tests PASS (including the original four).

- [ ] **Step 5: Lint**

Run: `make lint`

- [ ] **Step 6: Commit**

```bash
git add pkg/atrest/kek_loader.go pkg/atrest/kek_loader_test.go
git commit -m "feat(atrest): poll-based KEK hot reload with mod-time gating"
```

---

### Task 4: DEKStore — interface, mockgen directive, Mongo implementation

**Files:**
- Create: `pkg/atrest/dek_store.go`
- Create: `pkg/atrest/dek_store_test.go`

> **Note on the existing repo pattern:** Other services keep their store interface in `store.go` and mongo impl in `store_mongo.go`. `pkg/atrest` keeps both in `dek_store.go` because the package owns multiple distinct collaborators (KEK loader, cache, cipher) and prefers per-collaborator files. Generated mock lives in `mock_dek_store_test.go` as a test-only file.

- [ ] **Step 1: Write the failing unit tests against an in-memory fake**

`pkg/atrest/dek_store_test.go`:
```go
package atrest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDEKStore is an in-memory DEKStore used to drive Cipher unit tests
// and to verify the contract assertions below before the Mongo impl exists.
type fakeDEKStore struct {
	mu   sync.Mutex
	data map[string]RoomDataKey
}

func newFakeDEKStore() *fakeDEKStore {
	return &fakeDEKStore{data: map[string]RoomDataKey{}}
}

func (f *fakeDEKStore) Get(_ context.Context, roomID string) (*RoomDataKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.data[roomID]
	if !ok {
		return nil, nil
	}
	cp := k
	return &cp, nil
}

func (f *fakeDEKStore) Upsert(_ context.Context, key RoomDataKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.data[key.ID]; ok {
		return nil // $setOnInsert: existing row wins
	}
	f.data[key.ID] = key
	return nil
}

func (f *fakeDEKStore) Replace(_ context.Context, key RoomDataKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key.ID] = key
	return nil
}

func TestFakeDEKStore_RoundTrip(t *testing.T) {
	s := newFakeDEKStore()
	ctx := context.Background()

	got, err := s.Get(ctx, "room1")
	require.NoError(t, err)
	assert.Nil(t, got)

	row := RoomDataKey{ID: "room1", WrappedDEK: []byte("wrapped"), WrapNonce: []byte("nonce"), KEKVersion: 1, CreatedAt: time.Now()}
	require.NoError(t, s.Upsert(ctx, row))

	got, err = s.Get(ctx, "room1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, row.WrappedDEK, got.WrappedDEK)

	// Upsert with same _id is a no-op.
	require.NoError(t, s.Upsert(ctx, RoomDataKey{ID: "room1", WrappedDEK: []byte("other"), KEKVersion: 9}))
	got, _ = s.Get(ctx, "room1")
	assert.Equal(t, []byte("wrapped"), got.WrappedDEK)

	// Replace overwrites.
	require.NoError(t, s.Replace(ctx, RoomDataKey{ID: "room1", WrappedDEK: []byte("re"), KEKVersion: 2}))
	got, _ = s.Get(ctx, "room1")
	assert.Equal(t, []byte("re"), got.WrappedDEK)
	assert.Equal(t, 2, got.KEKVersion)
}
```

- [ ] **Step 2: Run; expect compile error on `RoomDataKey` reference if not yet linked**

Run: `go test ./pkg/atrest/... -run TestFakeDEKStore -v`
Expected: PASS (RoomDataKey is already defined in atrest.go from Task 1).

- [ ] **Step 3: Add the DEKStore interface and Mongo implementation**

`pkg/atrest/dek_store.go`:
```go
package atrest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

//go:generate mockgen -destination=mock_dek_store_test.go -package=atrest -source=dek_store.go DEKStore

// DEKStore persists wrapped DEK records, one per room.
type DEKStore interface {
	// Get returns nil, nil if the row does not exist.
	Get(ctx context.Context, roomID string) (*RoomDataKey, error)
	// Upsert inserts the row only if absent (first-writer-wins).
	Upsert(ctx context.Context, key RoomDataKey) error
	// Replace fully overwrites the row. Used by KEK rotation.
	Replace(ctx context.Context, key RoomDataKey) error
}

// CollectionName is the canonical Mongo collection name.
const CollectionName = "room_data_keys"

type mongoDEKStore struct {
	coll *mongo.Collection
}

// NewMongoDEKStore returns a DEKStore backed by the given collection.
// Callers typically pass db.Collection(CollectionName).
func NewMongoDEKStore(coll *mongo.Collection) DEKStore {
	return &mongoDEKStore{coll: coll}
}

func (s *mongoDEKStore) Get(ctx context.Context, roomID string) (*RoomDataKey, error) {
	var out RoomDataKey
	err := s.coll.FindOne(ctx, bson.M{"_id": roomID}).Decode(&out)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, fmt.Errorf("dek store get: %w", err)
	}
	return &out, nil
}

func (s *mongoDEKStore) Upsert(ctx context.Context, key RoomDataKey) error {
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now().UTC()
	}
	_, err := s.coll.UpdateOne(ctx,
		bson.M{"_id": key.ID},
		bson.M{"$setOnInsert": bson.M{
			"wrappedDEK": key.WrappedDEK,
			"wrapNonce":  key.WrapNonce,
			"kekVersion": key.KEKVersion,
			"createdAt":  key.CreatedAt,
		}},
		options.UpdateOne().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("dek store upsert: %w", err)
	}
	return nil
}

func (s *mongoDEKStore) Replace(ctx context.Context, key RoomDataKey) error {
	_, err := s.coll.ReplaceOne(ctx, bson.M{"_id": key.ID}, key, options.Replace().SetUpsert(false))
	if err != nil {
		return fmt.Errorf("dek store replace: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Generate the mock**

Run:
```
make generate
```
This runs `go generate ./...`; the new `//go:generate` line creates `pkg/atrest/mock_dek_store_test.go`.

Expected: a file named `pkg/atrest/mock_dek_store_test.go` is created containing `MockDEKStore`.

- [ ] **Step 5: Run all tests**

Run: `go test ./pkg/atrest/... -v`
Expected: all unit tests PASS.

- [ ] **Step 6: Lint**

Run: `make lint`

- [ ] **Step 7: Commit**

```bash
git add pkg/atrest/dek_store.go pkg/atrest/dek_store_test.go pkg/atrest/mock_dek_store_test.go
git commit -m "feat(atrest): DEKStore interface, mongo impl, mockgen, in-memory fake for tests"
```

---

### Task 5: DEK cache — bounded LRU with TTL

**Files:**
- Create: `pkg/atrest/cache.go`
- Create: `pkg/atrest/cache_test.go`

- [ ] **Step 1: Write the failing tests**

`pkg/atrest/cache_test.go`:
```go
package atrest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDEKCache_GetSet(t *testing.T) {
	c := newDEKCache(3, time.Hour)
	v, ok := c.get("a")
	assert.False(t, ok)
	assert.Nil(t, v)

	c.set("a", []byte("DEK-A"))
	v, ok = c.get("a")
	assert.True(t, ok)
	assert.Equal(t, []byte("DEK-A"), v)
}

func TestDEKCache_LRUEviction(t *testing.T) {
	c := newDEKCache(2, time.Hour)
	c.set("a", []byte("A"))
	c.set("b", []byte("B"))
	_, _ = c.get("a") // promote a
	c.set("c", []byte("C")) // evicts b (least-recent)

	_, okA := c.get("a")
	_, okB := c.get("b")
	_, okC := c.get("c")
	assert.True(t, okA)
	assert.False(t, okB)
	assert.True(t, okC)
}

func TestDEKCache_TTLExpiry(t *testing.T) {
	c := newDEKCache(10, 20*time.Millisecond)
	c.set("a", []byte("A"))
	time.Sleep(40 * time.Millisecond)
	_, ok := c.get("a")
	assert.False(t, ok)
}

func TestDEKCache_Invalidate(t *testing.T) {
	c := newDEKCache(10, time.Hour)
	c.set("a", []byte("A"))
	c.invalidate("a")
	_, ok := c.get("a")
	assert.False(t, ok)
}

func TestDEKCache_Concurrent(t *testing.T) {
	c := newDEKCache(100, time.Hour)
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(i int) {
			for j := 0; j < 1000; j++ {
				c.set("k", []byte{byte(i)})
				_, _ = c.get("k")
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	// No assertion beyond "no race": run with -race.
}
```

- [ ] **Step 2: Run; verify failure**

Run: `go test ./pkg/atrest/... -run TestDEKCache -v`
Expected: build error, `undefined: newDEKCache`.

- [ ] **Step 3: Implement the cache**

`pkg/atrest/cache.go`:
```go
package atrest

import (
	"container/list"
	"sync"
	"time"
)

// dekCache is an LRU keyed by roomID holding unwrapped DEK bytes.
// TTL is a soft retention bound; LRU eviction is the primary cap.
type dekCache struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	ll       *list.List
	index    map[string]*list.Element
	now      func() time.Time // injectable for tests
}

type dekEntry struct {
	roomID    string
	dek       []byte
	expiresAt time.Time
}

func newDEKCache(capacity int, ttl time.Duration) *dekCache {
	return &dekCache{
		capacity: capacity,
		ttl:      ttl,
		ll:       list.New(),
		index:    make(map[string]*list.Element, capacity),
		now:      time.Now,
	}
}

func (c *dekCache) get(roomID string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[roomID]
	if !ok {
		return nil, false
	}
	e := el.Value.(*dekEntry)
	if c.now().After(e.expiresAt) {
		c.ll.Remove(el)
		delete(c.index, roomID)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return e.dek, true
}

func (c *dekCache) set(roomID string, dek []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp := c.now().Add(c.ttl)
	if el, ok := c.index[roomID]; ok {
		e := el.Value.(*dekEntry)
		e.dek = dek
		e.expiresAt = exp
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&dekEntry{roomID: roomID, dek: dek, expiresAt: exp})
	c.index[roomID] = el
	if c.ll.Len() > c.capacity {
		oldest := c.ll.Back()
		if oldest != nil {
			c.ll.Remove(oldest)
			delete(c.index, oldest.Value.(*dekEntry).roomID)
		}
	}
}

func (c *dekCache) invalidate(roomID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[roomID]; ok {
		c.ll.Remove(el)
		delete(c.index, roomID)
	}
}
```

- [ ] **Step 4: Run; verify pass with race detector**

Run: `go test -race ./pkg/atrest/... -run TestDEKCache -v`
Expected: 5 tests PASS, no race warnings.

- [ ] **Step 5: Lint**

Run: `make lint`

- [ ] **Step 6: Commit**

```bash
git add pkg/atrest/cache.go pkg/atrest/cache_test.go
git commit -m "feat(atrest): bounded LRU DEK cache with TTL"
```

---

### Task 6: Cipher — encrypt and decrypt with lazy DEK creation

**Files:**
- Create: `pkg/atrest/cipher.go`
- Create: `pkg/atrest/cipher_test.go`

- [ ] **Step 1: Write the failing tests**

`pkg/atrest/cipher_test.go`:
```go
package atrest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staticKEKLoader is a non-reloading KEKLoader for unit tests.
type staticKEKLoader struct {
	keys    map[int][]byte
	current int
}

func (s *staticKEKLoader) Current() (int, []byte)       { return s.current, s.keys[s.current] }
func (s *staticKEKLoader) ByVersion(v int) ([]byte, bool) { k, ok := s.keys[v]; return k, ok }
func (s *staticKEKLoader) Close() error                  { return nil }

func newTestCipher(t *testing.T, store DEKStore) *cipher {
	t.Helper()
	loader := &staticKEKLoader{
		keys:    map[int][]byte{1: bytes32('a'), 2: bytes32('b')},
		current: 2,
	}
	return newCipher(loader, store, newDEKCache(100, time.Hour)).(*cipher)
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestCipher_RoundTrip(t *testing.T) {
	store := newFakeDEKStore()
	c := newTestCipher(t, store)
	ctx := context.Background()

	in := EncryptedFields{Msg: "hello world", SysMsgData: []byte{1, 2, 3}}
	payload, meta, err := c.Encrypt(ctx, "room1", in)
	require.NoError(t, err)
	assert.NotEmpty(t, payload)
	assert.Len(t, meta.Nonce, 12)

	out, err := c.Decrypt(ctx, "room1", payload, meta)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestCipher_LazyDEKCreation(t *testing.T) {
	store := newFakeDEKStore()
	c := newTestCipher(t, store)
	ctx := context.Background()

	row, err := store.Get(ctx, "room1")
	require.NoError(t, err)
	assert.Nil(t, row)

	_, _, err = c.Encrypt(ctx, "room1", EncryptedFields{Msg: "first"})
	require.NoError(t, err)

	row, err = store.Get(ctx, "room1")
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 2, row.KEKVersion) // current
	assert.Len(t, row.WrapNonce, 12)
}

func TestCipher_TamperedCiphertextRejected(t *testing.T) {
	store := newFakeDEKStore()
	c := newTestCipher(t, store)
	ctx := context.Background()

	payload, meta, err := c.Encrypt(ctx, "room1", EncryptedFields{Msg: "hello"})
	require.NoError(t, err)

	payload[0] ^= 0xFF
	_, err = c.Decrypt(ctx, "room1", payload, meta)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAuthFailed))
}

func TestCipher_KEKVersionUnknown(t *testing.T) {
	store := newFakeDEKStore()
	c := newTestCipher(t, store)
	ctx := context.Background()

	// Hand-craft a DEK row wrapped under a non-existent KEK version.
	require.NoError(t, store.Upsert(ctx, RoomDataKey{
		ID:         "room1",
		WrappedDEK: []byte("garbage"),
		WrapNonce:  make([]byte, 12),
		KEKVersion: 999,
	}))

	_, _, err := c.Encrypt(ctx, "room1", EncryptedFields{Msg: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKVersionUnknown))
}

func TestCipher_CacheHitAvoidsStore(t *testing.T) {
	store := newFakeDEKStore()
	c := newTestCipher(t, store)
	ctx := context.Background()

	_, _, err := c.Encrypt(ctx, "room1", EncryptedFields{Msg: "first"})
	require.NoError(t, err)

	// Replace store with a sentinel that fails any access.
	c.store = failingStore{}

	_, _, err = c.Encrypt(ctx, "room1", EncryptedFields{Msg: "second"})
	assert.NoError(t, err) // hit cache, no store call
}

type failingStore struct{}

func (failingStore) Get(context.Context, string) (*RoomDataKey, error) {
	return nil, errors.New("store should not be called")
}
func (failingStore) Upsert(context.Context, RoomDataKey) error  { return errors.New("nope") }
func (failingStore) Replace(context.Context, RoomDataKey) error { return errors.New("nope") }
```

- [ ] **Step 2: Run; verify build failure**

Run: `go test ./pkg/atrest/... -run TestCipher -v`
Expected: build error, `undefined: newCipher`.

- [ ] **Step 3: Implement the cipher**

`pkg/atrest/cipher.go`:
```go
package atrest

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Cipher is the public API used by services to encrypt/decrypt message
// payloads. Its concrete implementation composes a KEKLoader, a DEKStore
// and an LRU cache of unwrapped DEKs.
type Cipher interface {
	Encrypt(ctx context.Context, roomID string, fields EncryptedFields) ([]byte, EncMeta, error)
	Decrypt(ctx context.Context, roomID string, encPayload []byte, meta EncMeta) (EncryptedFields, error)
}

// NewCipher composes a Cipher from its dependencies.
func NewCipher(loader KEKLoader, store DEKStore, cfg Config) Cipher {
	return newCipher(loader, store, newDEKCache(cfg.DEKCacheSize, cfg.DEKCacheTTL))
}

func newCipher(loader KEKLoader, store DEKStore, cache *dekCache) Cipher {
	return &cipher{loader: loader, store: store, cache: cache, randReader: rand.Reader}
}

type cipher struct {
	loader     KEKLoader
	store      DEKStore
	cache      *dekCache
	randReader io.Reader
}

func (c *cipher) Encrypt(ctx context.Context, roomID string, fields EncryptedFields) ([]byte, EncMeta, error) {
	dek, err := c.dekFor(ctx, roomID)
	if err != nil {
		return nil, EncMeta{}, err
	}
	plaintext, err := json.Marshal(fields)
	if err != nil {
		return nil, EncMeta{}, fmt.Errorf("marshal payload: %w", err)
	}
	ciphertext, nonce, err := encryptGCM(dek, plaintext, c.randReader)
	if err != nil {
		return nil, EncMeta{}, fmt.Errorf("encrypt payload: %w", err)
	}
	return ciphertext, EncMeta{Nonce: nonce}, nil
}

func (c *cipher) Decrypt(ctx context.Context, roomID string, payload []byte, meta EncMeta) (EncryptedFields, error) {
	dek, err := c.dekFor(ctx, roomID)
	if err != nil {
		return EncryptedFields{}, err
	}
	plain, err := decryptGCM(dek, payload, meta.Nonce)
	if err != nil {
		return EncryptedFields{}, err
	}
	var out EncryptedFields
	if err := json.Unmarshal(plain, &out); err != nil {
		return EncryptedFields{}, fmt.Errorf("%w: %v", ErrPayloadMalformed, err)
	}
	return out, nil
}

// dekFor returns the unwrapped DEK for roomID, creating one lazily if
// no row exists yet.
func (c *cipher) dekFor(ctx context.Context, roomID string) ([]byte, error) {
	if dek, ok := c.cache.get(roomID); ok {
		return dek, nil
	}
	row, err := c.store.Get(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		dek, row2, err := c.createDEK(ctx, roomID)
		if err != nil {
			return nil, err
		}
		_ = row2
		c.cache.set(roomID, dek)
		return dek, nil
	}
	kek, ok := c.loader.ByVersion(row.KEKVersion)
	if !ok {
		return nil, fmt.Errorf("%w: version %d", ErrKEKVersionUnknown, row.KEKVersion)
	}
	dek, err := decryptGCM(kek, row.WrappedDEK, row.WrapNonce)
	if err != nil {
		return nil, fmt.Errorf("unwrap dek: %w", err)
	}
	c.cache.set(roomID, dek)
	return dek, nil
}

// createDEK generates and stores a fresh DEK. On a concurrent insert race,
// it re-Gets and uses the winner's row.
func (c *cipher) createDEK(ctx context.Context, roomID string) ([]byte, *RoomDataKey, error) {
	dek := make([]byte, 32)
	if _, err := io.ReadFull(c.randReader, dek); err != nil {
		return nil, nil, fmt.Errorf("generate DEK: %w", err)
	}
	kekVersion, kek := c.loader.Current()
	wrapped, nonce, err := encryptGCM(kek, dek, c.randReader)
	if err != nil {
		return nil, nil, fmt.Errorf("wrap DEK: %w", err)
	}
	row := RoomDataKey{
		ID:         roomID,
		WrappedDEK: wrapped,
		WrapNonce:  nonce,
		KEKVersion: kekVersion,
		CreatedAt:  time.Now().UTC(),
	}
	if err := c.store.Upsert(ctx, row); err != nil {
		return nil, nil, err
	}
	// Re-read to detect a concurrent insert that won the race.
	stored, err := c.store.Get(ctx, roomID)
	if err != nil {
		return nil, nil, err
	}
	if stored == nil {
		return nil, nil, errors.New("dek row missing after upsert")
	}
	if !bytesEqual(stored.WrappedDEK, wrapped) {
		// Lost the race: another goroutine inserted first. Unwrap theirs.
		kek2, ok := c.loader.ByVersion(stored.KEKVersion)
		if !ok {
			return nil, nil, fmt.Errorf("%w: version %d", ErrKEKVersionUnknown, stored.KEKVersion)
		}
		dek2, err := decryptGCM(kek2, stored.WrappedDEK, stored.WrapNonce)
		if err != nil {
			return nil, nil, fmt.Errorf("unwrap winner DEK: %w", err)
		}
		return dek2, stored, nil
	}
	return dek, stored, nil
}

// encryptGCM seals plaintext with a fresh 12-byte random nonce. Returns
// (ciphertext, nonce). The auth tag is appended to the ciphertext by GCM.
func encryptGCM(key, plaintext []byte, r io.Reader) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("new cipher: %w", err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("new gcm: %w", err)
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(r, nonce); err != nil {
		return nil, nil, fmt.Errorf("nonce: %w", err)
	}
	ct := g.Seal(nil, nonce, plaintext, nil)
	return ct, nonce, nil
}

func decryptGCM(key, ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	plain, err := g.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthFailed, err)
	}
	return plain, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run cipher tests**

Run: `go test -race ./pkg/atrest/... -run TestCipher -v`
Expected: 5 tests PASS.

- [ ] **Step 5: Run all unit tests**

Run: `go test -race ./pkg/atrest/... -v`
Expected: all PASS.

- [ ] **Step 6: Lint**

Run: `make lint`

- [ ] **Step 7: Commit**

```bash
git add pkg/atrest/cipher.go pkg/atrest/cipher_test.go
git commit -m "feat(atrest): Cipher with lazy DEK creation, AES-256-GCM, race-safe upsert"
```

---

### Task 7: pkg/atrest integration test (testcontainers Mongo + filesystem KEK)

**Files:**
- Create: `pkg/atrest/integration_test.go`

- [ ] **Step 1: Write the integration test**

`pkg/atrest/integration_test.go`:
```go
//go:build integration

package atrest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/testutil/testimages"

	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
)

func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	c, err := tcmongo.Run(ctx, testimages.Mongo())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	uri, err := c.ConnectionString(ctx)
	require.NoError(t, err)
	cli, err := mongoutil.Connect(ctx, uri, "", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Disconnect(ctx) })
	return cli.Database("atrest_test")
}

func writeKEKFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "keks.json")
	body, err := json.Marshal(map[string]any{
		"current": 1,
		"keys": map[string]string{
			"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, body, 0o600))
	return path
}

func TestIntegration_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := NewMongoDEKStore(db.Collection(CollectionName))

	loader, err := NewFileKEKLoader(writeKEKFile(t, t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = loader.Close() })

	c := NewCipher(loader, store, Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})

	in := EncryptedFields{Msg: "secret", Attachments: [][]byte{[]byte("file1")}}
	payload, meta, err := c.Encrypt(ctx, "room-A", in)
	require.NoError(t, err)

	out, err := c.Decrypt(ctx, "room-A", payload, meta)
	require.NoError(t, err)
	assert.Equal(t, in, out)

	// Confirm the row landed in Mongo.
	var row RoomDataKey
	require.NoError(t, db.Collection(CollectionName).FindOne(ctx, bson.M{"_id": "room-A"}).Decode(&row))
	assert.Equal(t, 1, row.KEKVersion)
}

func TestIntegration_ConcurrentFirstWriteRace(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := NewMongoDEKStore(db.Collection(CollectionName))
	loader, _ := NewFileKEKLoader(writeKEKFile(t, t.TempDir()))
	t.Cleanup(func() { _ = loader.Close() })

	// One Cipher per goroutine to defeat the in-process cache.
	const N = 16
	results := make([][]byte, N)
	metas := make([]EncMeta, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := NewCipher(loader, store, Config{DEKCacheSize: 1, DEKCacheTTL: time.Hour})
			p, m, err := c.Encrypt(ctx, "room-race", EncryptedFields{Msg: "x"})
			require.NoError(t, err)
			results[i] = p
			metas[i] = m
		}(i)
	}
	wg.Wait()

	// Exactly one DEK row exists.
	cur, err := db.Collection(CollectionName).Find(ctx, bson.M{"_id": "room-race"})
	require.NoError(t, err)
	var rows []RoomDataKey
	require.NoError(t, cur.All(ctx, &rows))
	require.Len(t, rows, 1)

	// All ciphertexts decrypt successfully under any cipher.
	c := NewCipher(loader, store, Config{DEKCacheSize: 1, DEKCacheTTL: time.Hour})
	for i := 0; i < N; i++ {
		out, err := c.Decrypt(ctx, "room-race", results[i], metas[i])
		require.NoError(t, err)
		assert.Equal(t, "x", out.Msg)
	}
}

func TestIntegration_KEKRotationReWrap(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := NewMongoDEKStore(db.Collection(CollectionName))

	dir := t.TempDir()
	path := filepath.Join(dir, "keks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 1,
		"keys": {"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}
	}`), 0o600))
	loader, err := newFileKEKLoaderWithInterval(path, 20*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = loader.Close() })

	c := NewCipher(loader, store, Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})
	payload, meta, err := c.Encrypt(ctx, "room-rot", EncryptedFields{Msg: "before"})
	require.NoError(t, err)

	// Rewrite the file with current=2.
	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 2,
		"keys": {
			"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
			"2": "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="
		}
	}`), 0o600))
	require.Eventually(t, func() bool {
		v, _ := loader.Current()
		return v == 2
	}, 2*time.Second, 30*time.Millisecond)

	// Manual re-wrap: read row, unwrap with v1, re-wrap with v2, Replace.
	row, err := store.Get(ctx, "room-rot")
	require.NoError(t, err)
	require.NotNil(t, row)
	kek1, _ := loader.ByVersion(1)
	dek, err := decryptGCM(kek1, row.WrappedDEK, row.WrapNonce)
	require.NoError(t, err)
	_, kek2 := loader.Current()
	wrapped, nonce, err := encryptGCM(kek2, dek, randReaderForTest())
	require.NoError(t, err)
	require.NoError(t, store.Replace(ctx, RoomDataKey{
		ID: row.ID, WrappedDEK: wrapped, WrapNonce: nonce, KEKVersion: 2, CreatedAt: row.CreatedAt,
	}))

	// New Cipher (no cache) decrypts the old payload.
	c2 := NewCipher(loader, store, Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})
	out, err := c2.Decrypt(ctx, "room-rot", payload, meta)
	require.NoError(t, err)
	assert.Equal(t, "before", out.Msg)
}

// randReaderForTest provides a deterministic source for nonce generation
// during the rotation test. crypto/rand is fine in production; here we
// reuse package-level rand.
func randReaderForTest() io.Reader {
	return rand.Reader
}
```

Add the imports `"crypto/rand"` and `"io"` at the top of the file.

> **Note:** Replace `github.com/hmchangw/chat` with the actual module path. Verify `pkg/testutil/testimages.Mongo()` exists and is the canonical helper used by other integration tests in this repo (it is, per recent commit history); if the helper signature differs, match the form used in `message-worker/integration_test.go`.

- [ ] **Step 2: Run integration tests**

Run: `make test-integration SERVICE=atrest` if the make target accepts package paths; otherwise:
`go test -tags=integration -race ./pkg/atrest/... -v`
Expected: 3 tests PASS.

- [ ] **Step 3: Lint**

Run: `make lint`

- [ ] **Step 4: Commit**

```bash
git add pkg/atrest/integration_test.go
git commit -m "test(atrest): integration tests — round-trip, race, KEK rotation re-wrap"
```

---

### Task 8: Coverage check and `pkg/atrest` PR

- [ ] **Step 1: Verify coverage ≥ 90%**

Run:
```
go test -race -coverprofile=/tmp/atrest.cov ./pkg/atrest/...
go tool cover -func=/tmp/atrest.cov | tail -1
```
Expected: total coverage ≥ 90%. If below, add tests for the missing branches before continuing.

- [ ] **Step 2: Push the branch**

Run: `git push -u origin claude/add-message-encryption-XaYfk`

This concludes Phase 1. The next phase modifies the Cassandra schema; nothing in Phase 2 depends on Phase 1 being merged but it does depend on Phase 1 being mergeable.

---

## Phase 2 — Cassandra schema and Go model

### Task 9: Add `EncMeta` UDT and `enc_payload`/`enc_meta` columns to docker-local CQL

**Files:**
- Create: `docker-local/cassandra/init/07-udt-enc_meta.cql`
- Modify: `docker-local/cassandra/init/10-table-messages_by_room.cql`
- Modify: `docker-local/cassandra/init/11-table-thread_messages_by_room.cql`
- Modify: `docker-local/cassandra/init/12-table-pinned_messages_by_room.cql`
- Modify: `docker-local/cassandra/init/13-table-messages_by_id.cql`

> **Discover step:** Before editing, open
> `docker-local/cassandra/init/12-table-pinned_messages_by_room.cql` and
> `13-table-messages_by_id.cql` and confirm they include the same content
> columns (`msg`, `attachments`, `card`, `card_action`, `sys_msg_data`).
> If `pinned_messages_by_room` only stores pointers (no body), skip the
> ALTER on that table and note the omission in the spec self-review.

- [ ] **Step 1: Create the new UDT file**

`docker-local/cassandra/init/07-udt-enc_meta.cql`:
```cql
CREATE TYPE IF NOT EXISTS "EncMeta"(
  nonce BLOB
);
```

- [ ] **Step 2: Add columns to each message table**

Append to `docker-local/cassandra/init/10-table-messages_by_room.cql`:
```cql
ALTER TABLE messages_by_room ADD enc_payload BLOB;
ALTER TABLE messages_by_room ADD enc_meta FROZEN<"EncMeta">;
```

Repeat the equivalent two `ALTER TABLE` statements at the end of:
- `11-table-thread_messages_by_room.cql`
- `13-table-messages_by_id.cql`
- `12-table-pinned_messages_by_room.cql` **only if** the discover step
  showed it stores body content; otherwise omit and document.

> **Note:** `ALTER TABLE … ADD` is an additive change and is safe to apply
> repeatedly during local re-init because the init scripts run on a fresh
> volume in dev. Production rollouts run these statements once via
> ops/IaC, not via these init files (the init files are dev-only).

- [ ] **Step 3: Recreate the local Cassandra container and verify**

Run:
```
cd docker-local && docker compose down -v && docker compose up -d cassandra
sleep 30
docker compose exec cassandra cqlsh -e "DESCRIBE TYPE chat.\"EncMeta\";"
docker compose exec cassandra cqlsh -e "DESCRIBE TABLE chat.messages_by_room;" | grep -E "enc_payload|enc_meta"
```
Expected: UDT and both columns are present.

- [ ] **Step 4: Commit**

```bash
git add docker-local/cassandra/init/
git commit -m "schema: enc_meta UDT and enc_payload/enc_meta columns on message tables"
```

---

### Task 10: Update `docs/cassandra_message_model.md`

**Files:**
- Modify: `docs/cassandra_message_model.md`

- [ ] **Step 1: Add the new UDT under the UDT section**

Insert after the `QuotedParentMessage` UDT block:
```markdown
#### EncMeta
```cql
CREATE TYPE IF NOT EXISTS "EncMeta"(
  nonce BLOB  // 12 bytes, AES-256-GCM nonce for enc_payload
);
```
Per-row metadata for at-rest encryption. The KEK version that wrapped the
room's DEK is intentionally **not** stored here — it lives on the
`room_data_keys` MongoDB document and is authoritative there. See
`docs/superpowers/specs/2026-05-05-message-at-rest-encryption-design.md`.
```

- [ ] **Step 2: Add the new columns under each affected table**

In the column list of `messages_by_room`, `thread_messages_by_room`,
`messages_by_id` (and `pinned_messages_by_room` if applicable per the
discover step in Task 9), add:
```cql
  enc_payload BLOB,                 // bundled JSON ciphertext of user-authored content; non-null for rows
                                    //   written after the at-rest encryption rollout
  enc_meta FROZEN<"EncMeta">,       // nonce; null for legacy plaintext rows
```

- [ ] **Step 3: Add a short "Encryption" subsection at the bottom**

```markdown
## Encryption (at rest)
Rows written after the at-rest encryption rollout encrypt user-authored
content into a single `enc_payload` blob and leave the legacy plaintext
columns (`msg`, `attachments`, `card`, `card_action`, `sys_msg_data`, and
the body fields of `quoted_parent_message`) null. Rows written before the
rollout retain their plaintext columns and have `enc_payload IS NULL`. The
read path branches on `enc_payload IS NOT NULL`.
```

- [ ] **Step 4: Commit**

```bash
git add docs/cassandra_message_model.md
git commit -m "docs(cassandra): EncMeta UDT, enc_payload/enc_meta columns, encryption section"
```

---

### Task 11: Update `pkg/model/cassandra/message.go` and its test

**Files:**
- Modify: `pkg/model/cassandra/message.go`
- Modify: `pkg/model/cassandra/message_test.go`

- [ ] **Step 1: Write the failing round-trip test**

Append to `pkg/model/cassandra/message_test.go`:
```go
func TestMessage_RoundTripEncrypted(t *testing.T) {
	in := Message{
		RoomID:     "r1",
		MessageID:  "m1",
		EncPayload: []byte{0xDE, 0xAD, 0xBE, 0xEF},
		EncMeta:    &EncMeta{Nonce: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}},
	}
	roundTrip(t, in)
}

func TestEncMeta_JSON(t *testing.T) {
	in := EncMeta{Nonce: []byte{1, 2, 3}}
	roundTrip(t, in)
}
```

If `roundTrip` is not defined locally, follow the pattern used by other
tests in the file (typically marshal+unmarshal via JSON, asserting the
result equals the input).

- [ ] **Step 2: Run; verify failure**

Run: `go test ./pkg/model/cassandra/... -run TestMessage_RoundTripEncrypted -v`
Expected: build error, undefined `EncPayload` and `EncMeta`.

- [ ] **Step 3: Add the type and fields**

In `pkg/model/cassandra/message.go`, define `EncMeta` near the top of the
file (after the `Card`, `File`, etc. UDT structs):
```go
// EncMeta maps to the Cassandra "EncMeta" UDT. nonce is the 12-byte
// AES-256-GCM nonce used to encrypt the row's enc_payload column.
//
// This type is intentionally a sibling of atrest.EncMeta rather than a
// shared definition: this struct carries the cql:"" tags needed for gocql
// binding, while atrest.EncMeta is the crypto-API form. The two types
// have identical content; Tasks 14 and 18 convert between them with a
// one-liner at each boundary (one byte-slice copy by reference).
type EncMeta struct {
	Nonce []byte `json:"nonce" cql:"nonce"`
}
```

Add the two new fields to the `Message` struct, immediately after the
`UpdatedAt` field:
```go
	EncPayload []byte   `json:"encPayload,omitempty" cql:"enc_payload"`
	EncMeta    *EncMeta `json:"encMeta,omitempty"    cql:"enc_meta"`
```

- [ ] **Step 4: Run; verify pass**

Run: `go test ./pkg/model/cassandra/... -v`
Expected: all PASS.

- [ ] **Step 5: Build the whole module to catch downstream breakage**

Run: `go build ./...`
Expected: succeeds.

- [ ] **Step 6: Lint**

Run: `make lint`

- [ ] **Step 7: Commit**

```bash
git add pkg/model/cassandra/message.go pkg/model/cassandra/message_test.go
git commit -m "model(cassandra): add EncMeta UDT struct and EncPayload/EncMeta fields on Message"
```

---

## Phase 3 — message-worker write path

The existing `Store.SaveMessage` and `Store.SaveThreadMessage` build
explicit `INSERT` statements with the plaintext content columns (`msg`,
`sys_msg_data`, `quoted_parent_message`, etc.). Phase 3 swaps those
plaintext columns for the new `enc_payload`/`enc_meta` columns when
encryption is enabled, while leaving the metadata columns unchanged.

The cipher is injected into the Cassandra store implementation, not the
handler. The `Store` interface in `message-worker/store.go` does not
change. Handler-level tests stay valid.

### Task 12: Helper — split a `model.Message` into metadata + `EncryptedFields`

**Files:**
- Create: `pkg/atrest/split.go`
- Create: `pkg/atrest/split_test.go`

A small helper inside `pkg/atrest` so both message-worker (write) and
history-service (read/edit) reuse the same field-splitting rules.

- [ ] **Step 1: Write the failing tests**

`pkg/atrest/split_test.go`:
```go
package atrest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/model/cassandra"
)

func TestSplitMessage_AllFields(t *testing.T) {
	in := &cassandra.Message{
		RoomID:     "r1",
		MessageID:  "m1",
		Msg:        "hello",
		Attachments: [][]byte{[]byte("a")},
		Card:        &cassandra.Card{Template: "t", Data: []byte("c")},
		SysMsgData:  []byte("sys"),
		QuotedParentMessage: &cassandra.QuotedParentMessage{
			MessageID:   "p",
			Msg:         "parent body",
			Attachments: [][]byte{[]byte("pa")},
		},
	}
	enc := SplitForEncryption(in)
	assert.Equal(t, "hello", enc.Msg)
	assert.Equal(t, [][]byte{[]byte("a")}, enc.Attachments)
	assert.Equal(t, "t", enc.Card.Template)
	assert.Equal(t, []byte("sys"), enc.SysMsgData)
	assert.NotNil(t, enc.QuotedParentContent)
	assert.Equal(t, "parent body", enc.QuotedParentContent.Msg)

	// SplitForEncryption MUST NOT mutate the input.
	assert.Equal(t, "hello", in.Msg)
	assert.NotNil(t, in.QuotedParentMessage)
	assert.Equal(t, "parent body", in.QuotedParentMessage.Msg)
}

func TestApplyDecryptedFields_Restores(t *testing.T) {
	target := &cassandra.Message{
		RoomID:    "r1",
		MessageID: "m1",
		QuotedParentMessage: &cassandra.QuotedParentMessage{
			MessageID: "p", // metadata preserved by reader
		},
	}
	enc := EncryptedFields{
		Msg:         "hello",
		Attachments: [][]byte{[]byte("a")},
		Card:        &cassandra.Card{Template: "t", Data: []byte("c")},
		SysMsgData:  []byte("sys"),
		QuotedParentContent: &QuotedParentEncrypted{
			Msg:         "parent body",
			Attachments: [][]byte{[]byte("pa")},
		},
	}
	ApplyDecryptedFields(target, enc)
	assert.Equal(t, "hello", target.Msg)
	assert.Equal(t, "t", target.Card.Template)
	assert.Equal(t, []byte("sys"), target.SysMsgData)
	assert.Equal(t, "parent body", target.QuotedParentMessage.Msg)
	assert.Equal(t, [][]byte{[]byte("pa")}, target.QuotedParentMessage.Attachments)
	// metadata preserved
	assert.Equal(t, "p", target.QuotedParentMessage.MessageID)
}

func TestStripEncryptedFields_NullsContent(t *testing.T) {
	in := &cassandra.Message{
		Msg:         "hello",
		Attachments: [][]byte{[]byte("a")},
		Card:        &cassandra.Card{Template: "t"},
		SysMsgData:  []byte("sys"),
		QuotedParentMessage: &cassandra.QuotedParentMessage{
			MessageID:   "p",
			Msg:         "parent body",
			Attachments: [][]byte{[]byte("pa")},
		},
	}
	StripEncryptedFields(in)
	assert.Empty(t, in.Msg)
	assert.Nil(t, in.Attachments)
	assert.Nil(t, in.Card)
	assert.Nil(t, in.SysMsgData)
	// quoted_parent_message kept, body fields nulled
	assert.Equal(t, "p", in.QuotedParentMessage.MessageID)
	assert.Empty(t, in.QuotedParentMessage.Msg)
	assert.Nil(t, in.QuotedParentMessage.Attachments)
}
```

- [ ] **Step 2: Run; verify failure**

Run: `go test ./pkg/atrest/... -run TestSplit -v -run TestApply -run TestStrip`
Expected: build error, helpers undefined.

- [ ] **Step 3: Implement the helpers**

`pkg/atrest/split.go`:
```go
package atrest

import "github.com/hmchangw/chat/pkg/model/cassandra"

// SplitForEncryption returns a copy of msg's user-authored fields suitable
// for passing to Cipher.Encrypt. The input is not mutated.
func SplitForEncryption(msg *cassandra.Message) EncryptedFields {
	out := EncryptedFields{
		Msg:         msg.Msg,
		Attachments: msg.Attachments,
		Card:        msg.Card,
		CardAction:  msg.CardAction,
		SysMsgData:  msg.SysMsgData,
	}
	if msg.QuotedParentMessage != nil {
		q := msg.QuotedParentMessage
		if q.Msg != "" || len(q.Attachments) > 0 {
			out.QuotedParentContent = &QuotedParentEncrypted{
				Msg:         q.Msg,
				Attachments: q.Attachments,
			}
		}
	}
	return out
}

// StripEncryptedFields nulls out user-authored fields on msg in place.
// Call this after SplitForEncryption to produce the metadata-only struct
// that gets written to Cassandra alongside enc_payload.
//
// quoted_parent_message metadata (sender, IDs, timestamps) is preserved;
// only its body fields (msg, attachments) are nulled.
func StripEncryptedFields(msg *cassandra.Message) {
	msg.Msg = ""
	msg.Attachments = nil
	msg.Card = nil
	msg.CardAction = nil
	msg.SysMsgData = nil
	if msg.QuotedParentMessage != nil {
		msg.QuotedParentMessage.Msg = ""
		msg.QuotedParentMessage.Attachments = nil
	}
}

// ApplyDecryptedFields copies fields from enc back onto msg. Used by
// history-service after Cipher.Decrypt. Metadata fields on
// quoted_parent_message are preserved; only body fields are filled.
func ApplyDecryptedFields(msg *cassandra.Message, enc EncryptedFields) {
	msg.Msg = enc.Msg
	msg.Attachments = enc.Attachments
	msg.Card = enc.Card
	msg.CardAction = enc.CardAction
	msg.SysMsgData = enc.SysMsgData
	if enc.QuotedParentContent != nil {
		if msg.QuotedParentMessage == nil {
			msg.QuotedParentMessage = &cassandra.QuotedParentMessage{}
		}
		msg.QuotedParentMessage.Msg = enc.QuotedParentContent.Msg
		msg.QuotedParentMessage.Attachments = enc.QuotedParentContent.Attachments
	}
}
```

- [ ] **Step 4: Run; verify pass**

Run: `go test ./pkg/atrest/... -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/atrest/split.go pkg/atrest/split_test.go
git commit -m "feat(atrest): SplitForEncryption / StripEncryptedFields / ApplyDecryptedFields helpers"
```

---

### Task 13: Inject `atrest.Cipher` into message-worker `CassandraStore`

**Files:**
- Modify: `message-worker/store_cassandra.go`
- Modify: `message-worker/main.go`

- [ ] **Step 1: Add cipher field and constructor parameter**

In `message-worker/store_cassandra.go`, locate the `CassandraStore`
struct definition and add a `cipher` field:
```go
type CassandraStore struct {
	session *gocql.Session
	cipher  atrest.Cipher
}
```
Add the import: `"github.com/hmchangw/chat/pkg/atrest"`.

Update the constructor (whatever it is currently named, e.g.
`NewCassandraStore`) to accept `cipher atrest.Cipher` and set it.

- [ ] **Step 2: Build to surface caller breakage**

Run: `go build ./message-worker/...`
Expected: compile errors at the call site in `main.go`. That is the next
step's anchor.

- [ ] **Step 3: Wire the cipher in `main.go`**

In `message-worker/main.go`:

1. Add the `Atrest atrest.Config \`envPrefix:""\`` field to the service
   `Config` struct. (No prefix — the env vars are already prefixed
   `ATREST_*`.) Import `"github.com/hmchangw/chat/pkg/atrest"`.

2. After `mongoutil.Connect` succeeds and before constructing the
   Cassandra store, add:
```go
var cipher atrest.Cipher
if cfg.Atrest.Enabled {
	loader, err := atrest.NewFileKEKLoader(cfg.Atrest.KEKFile)
	if err != nil {
		slog.Error("failed to load KEK file", "err", err)
		os.Exit(1)
	}
	defer loader.Close()
	dekColl := mongoClient.Database(cfg.MongoDB).Collection(atrest.CollectionName)
	cipher = atrest.NewCipher(loader, atrest.NewMongoDEKStore(dekColl), cfg.Atrest)
} else {
	cipher = atrest.NoopCipher{}
}
```

3. Pass `cipher` into the new constructor.

- [ ] **Step 4: Add the no-op cipher to `pkg/atrest`**

Append to `pkg/atrest/cipher.go`:
```go
// NoopCipher is a pass-through Cipher used when ATREST_ENABLED=false.
// Encrypt returns the JSON-encoded payload as-is (no encryption);
// Decrypt JSON-decodes it. NoopCipher exists only to keep service code
// simple — it is never selected when Enabled=true.
type NoopCipher struct{}

func (NoopCipher) Encrypt(_ context.Context, _ string, fields EncryptedFields) ([]byte, EncMeta, error) {
	b, err := json.Marshal(fields)
	if err != nil {
		return nil, EncMeta{}, fmt.Errorf("noop marshal: %w", err)
	}
	return b, EncMeta{}, nil
}

func (NoopCipher) Decrypt(_ context.Context, _ string, payload []byte, _ EncMeta) (EncryptedFields, error) {
	var out EncryptedFields
	if err := json.Unmarshal(payload, &out); err != nil {
		return EncryptedFields{}, fmt.Errorf("%w: %v", ErrPayloadMalformed, err)
	}
	return out, nil
}
```

> **Wait** — services that disable encryption via `ATREST_ENABLED=false`
> should keep writing plaintext columns, not pass-through-JSON to
> `enc_payload`. The cleaner design is for the store impl to branch on
> `cipher == nil` (or on a `Config.Enabled` flag passed in alongside) and
> use legacy SQL when disabled. **Replace the previous step with:** drop
> the `NoopCipher` idea; instead pass `enabled bool` to the store
> constructor and let it pick the SQL form. Update `main.go` to:
> ```go
> var cipher atrest.Cipher
> if cfg.Atrest.Enabled {
> 	loader, err := atrest.NewFileKEKLoader(cfg.Atrest.KEKFile)
> 	// ...
> 	cipher = atrest.NewCipher(loader, atrest.NewMongoDEKStore(dekColl), cfg.Atrest)
> }
> store := NewCassandraStore(session, cipher)  // cipher may be nil
> ```
> The store then checks `s.cipher != nil` to decide which SQL to issue.
> No `NoopCipher` type is added.

- [ ] **Step 5: Lint and build**

Run: `go build ./message-worker/... && make lint`

- [ ] **Step 6: Commit**

```bash
git add message-worker/main.go message-worker/store_cassandra.go
git commit -m "wire(message-worker): inject atrest.Cipher into Cassandra store"
```

---

### Task 14: Encrypted SaveMessage and SaveThreadMessage

**Files:**
- Modify: `message-worker/store_cassandra.go`
- Modify: `message-worker/store_cassandra_test.go` (integration test)

- [ ] **Step 1: Update `SaveMessage` to write encrypted columns**

Locate `SaveMessage` in `message-worker/store_cassandra.go`. It currently
issues two INSERTs (one to `messages_by_room`, one to `messages_by_id`).
Refactor as follows:

1. Convert the incoming `*model.Message` into a `cassandra.Message` with
   the existing field-mapping logic (it lives inline in the current
   implementation).
2. If `s.cipher != nil`:
   - `enc := atrest.SplitForEncryption(&cm)` where `cm` is the cassandra
     row about to be persisted.
   - `payload, meta, err := s.cipher.Encrypt(ctx, cm.RoomID, enc)`
   - On success:
     ```go
     atrest.StripEncryptedFields(&cm)
     cm.EncPayload = payload
     cm.EncMeta = &cassandra.EncMeta{Nonce: meta.Nonce}
     ```
     The `&cassandra.EncMeta{Nonce: meta.Nonce}` is the boundary
     conversion called out in Task 11 — `atrest.EncMeta` and
     `cassandra.EncMeta` are sibling types of identical shape.
3. Issue INSERTs that bind ALL columns the rows currently take, but bind
   `enc_payload` and `enc_meta` instead of `msg`, `attachments`, `card`,
   `card_action`, `sys_msg_data`. The `quoted_parent_message` UDT is
   bound from the modified `cm.QuotedParentMessage` which now has its body
   fields nulled (per `StripEncryptedFields`).
4. If `s.cipher == nil`: issue the legacy INSERTs unchanged (preserves
   the `ATREST_ENABLED=false` path).

Write helper functions at the top of the file:
```go
func (s *CassandraStore) bindMessageInsert(ctx context.Context, table string, cm *cassandra.Message) error {
	if s.cipher != nil {
		enc := atrest.SplitForEncryption(cm)
		payload, meta, err := s.cipher.Encrypt(ctx, cm.RoomID, enc)
		if err != nil {
			return fmt.Errorf("encrypt message %s in room %s: %w", cm.MessageID, cm.RoomID, err)
		}
		atrest.StripEncryptedFields(cm)
		cm.EncPayload = payload
		cm.EncMeta = &cassandra.EncMeta{Nonce: meta.Nonce}
	}
	// CQL with enc_payload + enc_meta replacing msg, attachments, card,
	// card_action, sys_msg_data when cipher is set; otherwise the legacy
	// columns are bound. To keep this readable, branch on s.cipher and
	// build two completely separate INSERT statements.
	// (Implementation detail left to the engineer; both paths must bind
	// every metadata column the legacy path bound.)
	// ...
}
```

> **Important:** the legacy INSERT and the encrypted INSERT must share
> the exact same WHERE/PRIMARY KEY semantics. Test both paths.

- [ ] **Step 2: Repeat for `SaveThreadMessage`**

Same logic, applied to the `messages_by_id` and `thread_messages_by_room`
INSERTs.

- [ ] **Step 3: Add an integration test for the encrypted path**

In `message-worker/store_cassandra_test.go` (the existing
`//go:build integration` file), add:
```go
func TestSaveMessage_EncryptsBody(t *testing.T) {
	ctx := context.Background()
	session := setupCassandra(t)
	mongoDB := setupMongo(t)

	loader, err := atrest.NewFileKEKLoader(writeTestKEKFile(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = loader.Close() })

	c := atrest.NewCipher(loader, atrest.NewMongoDEKStore(mongoDB.Collection(atrest.CollectionName)),
		atrest.Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})
	store := NewCassandraStore(session, c)

	msg := &model.Message{
		MessageID: "m1",
		RoomID:    "r1",
		Msg:       "secret body",
		// ... fill required metadata ...
	}
	require.NoError(t, store.SaveMessage(ctx, msg, &cassParticipant{ID: "u1"}, "siteA"))

	// Direct CQL: msg column is null/empty, enc_payload is non-nil.
	var (
		msgCol     string
		encPayload []byte
	)
	require.NoError(t, session.Query(`SELECT msg, enc_payload FROM messages_by_room WHERE room_id=? AND message_id=? LIMIT 1`, "r1", "m1").
		Scan(&msgCol, &encPayload))
	assert.Empty(t, msgCol)
	assert.NotEmpty(t, encPayload)
}
```

`writeTestKEKFile` is the same helper used in `pkg/atrest/integration_test.go`; either copy it (small enough to be acceptable) or factor a shared helper into `pkg/testutil/atresttest/` (a new test-only package, only imported by `_test.go` files — matches the `pkg/testutil/` convention).

- [ ] **Step 4: Regenerate mocks if `Store` interface changed**

The interface did NOT change in this task, so mocks are unaffected. Skip
unless lint or tests indicate otherwise.

- [ ] **Step 5: Run unit + integration tests**

Run:
```
make test SERVICE=message-worker
make test-integration SERVICE=message-worker
```
Expected: PASS.

- [ ] **Step 6: Lint**

Run: `make lint`

- [ ] **Step 7: Commit**

```bash
git add message-worker/store_cassandra.go message-worker/store_cassandra_test.go
git commit -m "feat(message-worker): encrypt message body before Cassandra insert"
```

---

### Task 15: Update message-worker `deploy/docker-compose.yml`

**Files:**
- Modify: `message-worker/deploy/docker-compose.yml`

- [ ] **Step 1: Add KEK file mount and env**

Add a volume mount and env var on the message-worker service block:
```yaml
    environment:
      - ATREST_ENABLED=true
      - ATREST_KEK_FILE=/etc/chat/keks.json
    volumes:
      - ./atrest-keks.json:/etc/chat/keks.json:ro
```

Create `message-worker/deploy/atrest-keks.json` with a single dev key:
```json
{
  "current": 1,
  "keys": {
    "1": "ZGV2ZWxvcG1lbnQta2V5LWZvci1sb2NhbC1ydW5zMDEyMw=="
  }
}
```
(That is 32 ASCII bytes base64-encoded — `development-key-for-local-runs0123`.)

> **Security note:** `atrest-keks.json` in the repo is for local dev ONLY.
> Production KEKs are provisioned via Kubernetes Secrets and mounted at
> the same path. Document this in the same PR's commit message.

- [ ] **Step 2: Verify local docker-compose still boots**

Run:
```
cd message-worker/deploy && docker compose up -d
docker compose logs message-worker | tail
```
Expected: service starts; logs do not show "failed to load KEK file".

- [ ] **Step 3: Commit**

```bash
git add message-worker/deploy/docker-compose.yml message-worker/deploy/atrest-keks.json
git commit -m "deploy(message-worker): mount dev KEK file for local docker-compose"
```

---

## Phase 4 — history-service read, edit and delete paths

history-service has three Cassandra interaction surfaces:

- `internal/cassrepo/` — CQL access. Reads use `structScan` into
  `cassandra.Message`; writes go through `write.go` for edit and delete.
- `internal/service/messages.go` — the service-layer logic that the
  HTTP/NATS handlers call into.

Phase 4 wires `atrest.Cipher` into the cassrepo layer (so decryption is
invisible above it) and updates the write paths in `write.go` to encrypt.

### Task 16: `baseColumns` includes `enc_payload` and `enc_meta`

**Files:**
- Modify: `history-service/internal/cassrepo/messages_by_room.go` (or wherever `baseColumns` is defined; search to confirm)
- Modify: any other file selecting from message tables

- [ ] **Step 1: Locate the column list**

Run: `grep -rn "baseColumns" history-service/internal/cassrepo/`
Confirm there is a single canonical `const baseColumns =` declaration.

- [ ] **Step 2: Append the new columns**

Add `, enc_payload, enc_meta` to the end of `baseColumns`. Because
`structScan` is by-name and the struct already has matching `cql:` tags
from Task 11, no further scan-side changes are required.

- [ ] **Step 3: Build to confirm no caller breaks**

Run: `go build ./history-service/...`

- [ ] **Step 4: Commit**

```bash
git add history-service/internal/cassrepo/
git commit -m "history(cassrepo): include enc_payload and enc_meta in baseColumns"
```

---

### Task 17: Inject `atrest.Cipher` into `cassrepo.Repository`

**Files:**
- Modify: `history-service/internal/cassrepo/repository.go`
- Modify: `history-service/cmd/main.go`

- [ ] **Step 1: Add cipher field to Repository**

Open `history-service/internal/cassrepo/repository.go`. Add a field:
```go
type Repository struct {
	// ... existing fields ...
	cipher atrest.Cipher  // may be nil when ATREST_ENABLED=false
}
```

Update `NewRepository` (or the existing constructor) to accept
`cipher atrest.Cipher` and set it. Add the import.

- [ ] **Step 2: Wire in `cmd/main.go`**

In `history-service/cmd/main.go`:

1. Add `Atrest atrest.Config \`envPrefix:""\`` to the service config struct.
2. After mongo and cassandra clients are connected:
```go
var cipher atrest.Cipher
if cfg.Atrest.Enabled {
	loader, err := atrest.NewFileKEKLoader(cfg.Atrest.KEKFile)
	if err != nil {
		slog.Error("failed to load KEK file", "err", err)
		os.Exit(1)
	}
	defer loader.Close()
	dekColl := mongoClient.Database(cfg.MongoDB).Collection(atrest.CollectionName)
	cipher = atrest.NewCipher(loader, atrest.NewMongoDEKStore(dekColl), cfg.Atrest)
}
```
3. Pass `cipher` into `NewRepository`.

- [ ] **Step 3: Build, lint, commit**

Run: `go build ./history-service/... && make lint`
```bash
git add history-service/cmd/main.go history-service/internal/cassrepo/repository.go
git commit -m "wire(history-service): inject atrest.Cipher into cassrepo.Repository"
```

---

### Task 18: Hybrid read path — decrypt rows where `enc_payload IS NOT NULL`

**Files:**
- Modify: `history-service/internal/cassrepo/messages_by_room.go`
- Modify: `history-service/internal/cassrepo/messages_by_id.go`
- Modify: `history-service/internal/cassrepo/thread_messages.go`
- Modify: `history-service/internal/cassrepo/messages_by_room_integration_test.go` (add hybrid test)

- [ ] **Step 1: Write the failing integration test**

In `history-service/internal/cassrepo/messages_by_room_integration_test.go`:

```go
func TestHybridRead_LegacyAndEncrypted(t *testing.T) {
	ctx := context.Background()
	session := setupCassandra(t)
	mongoDB := setupMongo(t)

	loader, err := atrest.NewFileKEKLoader(writeTestKEKFile(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = loader.Close() })

	cipher := atrest.NewCipher(loader, atrest.NewMongoDEKStore(mongoDB.Collection(atrest.CollectionName)),
		atrest.Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})
	repo := NewRepository(session, cipher)

	now := time.Now().UTC().Truncate(time.Millisecond)

	// Insert a legacy plaintext row directly with CQL.
	require.NoError(t, session.Query(`INSERT INTO messages_by_room (room_id, created_at, message_id, msg, site_id) VALUES (?, ?, ?, ?, ?)`,
		"r1", now, "legacy", "plaintext-body", "siteA").Exec())

	// Insert an encrypted row by going through the cipher.
	enc := atrest.EncryptedFields{Msg: "encrypted-body"}
	payload, meta, err := cipher.Encrypt(ctx, "r1", enc)
	require.NoError(t, err)
	require.NoError(t, session.Query(`INSERT INTO messages_by_room (room_id, created_at, message_id, enc_payload, enc_meta, site_id) VALUES (?, ?, ?, ?, ?, ?)`,
		"r1", now.Add(time.Second), "encrypted", payload, &cassandra.EncMeta{Nonce: meta.Nonce}, "siteA").Exec())

	// Read both back via the repo.
	msgs, err := repo.GetByRoom(ctx, "r1", now.Add(-time.Hour), now.Add(time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, msgs, 2)

	bodies := []string{msgs[0].Msg, msgs[1].Msg}
	assert.Contains(t, bodies, "plaintext-body")
	assert.Contains(t, bodies, "encrypted-body")
}
```

> Replace `repo.GetByRoom` with the actual reader function name. Find it
> via `grep -n "func.*Repository" history-service/internal/cassrepo/messages_by_room.go`.

- [ ] **Step 2: Run; verify failure**

Run: `make test-integration SERVICE=history-service`
Expected: the encrypted message comes back with `Msg=""` because nothing
decrypts it.

- [ ] **Step 3: Add a `decryptIfNeeded` helper and call it after every scan**

In a small new file `history-service/internal/cassrepo/decrypt.go`:
```go
package cassrepo

import (
	"context"
	"fmt"

	"github.com/hmchangw/chat/pkg/atrest"
	"github.com/hmchangw/chat/pkg/model/cassandra"
)

// decryptIfNeeded decrypts m's enc_payload in place when present. When
// enc_payload is nil the row is treated as legacy plaintext and m is
// returned unchanged. When the cipher is nil (ATREST_ENABLED=false) and
// enc_payload is non-nil, a typed error is returned because the service
// is not configured to read encrypted rows.
func (r *Repository) decryptIfNeeded(ctx context.Context, m *cassandra.Message) error {
	if len(m.EncPayload) == 0 {
		return nil
	}
	if r.cipher == nil {
		return fmt.Errorf("encrypted row encountered but cipher is disabled")
	}
	meta := atrest.EncMeta{}
	if m.EncMeta != nil {
		meta.Nonce = m.EncMeta.Nonce
	}
	fields, err := r.cipher.Decrypt(ctx, m.RoomID, m.EncPayload, meta)
	if err != nil {
		return fmt.Errorf("decrypt message %s in room %s: %w", m.MessageID, m.RoomID, err)
	}
	atrest.ApplyDecryptedFields(m, fields)
	// Clear the on-row encrypted fields so callers above the cassrepo
	// layer never see them.
	m.EncPayload = nil
	m.EncMeta = nil
	return nil
}
```

In each reader (e.g. `messages_by_room.go`, `messages_by_id.go`,
`thread_messages.go`), after the existing `structScan` loop produces a
populated `m`, call `r.decryptIfNeeded(ctx, &m)` and propagate the error.
For range queries, decrypt each row before appending to the result slice.

- [ ] **Step 4: Run integration tests; verify pass**

Run: `make test-integration SERVICE=history-service`
Expected: the new test PASSES.

- [ ] **Step 5: Lint and commit**

Run: `make lint`
```bash
git add history-service/internal/cassrepo/
git commit -m "feat(history-service): hybrid read path decrypts enc_payload rows"
```

---

### Task 19: Encrypt on edit; clear `enc_payload` on delete

**Files:**
- Modify: `history-service/internal/cassrepo/write.go`
- Modify: `history-service/internal/cassrepo/write_integration_test.go`

- [ ] **Step 1: Locate the edit path**

Run: `grep -n "func.*Edit\|UPDATE messages_by" history-service/internal/cassrepo/write.go`

The edit path currently UPDATEs message body columns. The new path:

1. Build a temporary `cassandra.Message` populated with the new edit
   fields.
2. `enc := atrest.SplitForEncryption(&tmp)`
3. `payload, meta, err := r.cipher.Encrypt(ctx, roomID, enc)`
4. Issue an UPDATE that sets `enc_payload`, `enc_meta`, `edited_at`,
   `updated_at` and clears `msg`, `attachments`, `card`, `card_action`,
   `sys_msg_data` to NULL (so the row remains internally consistent —
   reader never finds both forms populated).

When `r.cipher == nil`, retain the legacy UPDATE form.

- [ ] **Step 2: Write the failing edit-path integration test**

In `history-service/internal/cassrepo/write_integration_test.go`, add a
test that:
1. Inserts a message via the encrypted write path.
2. Calls the edit function with a new body.
3. Reads back via the cassrepo reader and asserts the body is the new
   text.
4. Direct-CQL queries the row and asserts `msg IS NULL` and
   `enc_payload IS NOT NULL`.

- [ ] **Step 3: Implement the edit changes; run; verify pass**

Run: `make test-integration SERVICE=history-service`

- [ ] **Step 4: Update the delete path**

Locate the delete tombstone code. The new path sets `deleted=true`,
clears the legacy body columns AND sets `enc_payload=NULL`,
`enc_meta=NULL`. No crypto operation runs on delete.

Add an integration test that deletes a previously-encrypted message and
asserts both `enc_payload IS NULL` and `deleted=true`.

- [ ] **Step 5: Run all history-service tests**

Run:
```
make test SERVICE=history-service
make test-integration SERVICE=history-service
```
Expected: PASS.

- [ ] **Step 6: Lint and commit**

```bash
git add history-service/internal/cassrepo/
git commit -m "feat(history-service): re-encrypt on edit; null enc_payload on delete"
```

---

### Task 20: Update history-service `deploy/docker-compose.yml`

**Files:**
- Modify: `history-service/deploy/docker-compose.yml`

- [ ] **Step 1: Mirror the message-worker change**

Add the same `ATREST_ENABLED`, `ATREST_KEK_FILE` env vars and the same
`atrest-keks.json` volume mount. Reuse the same dev key file path so
both services point at the same physical file:
```yaml
    environment:
      - ATREST_ENABLED=true
      - ATREST_KEK_FILE=/etc/chat/keks.json
    volumes:
      - ../../message-worker/deploy/atrest-keks.json:/etc/chat/keks.json:ro
```

(Or copy the file into `history-service/deploy/`. The shared file form
keeps the local-dev story clearer about what "same KEK across services"
means.)

- [ ] **Step 2: Verify and commit**

Run:
```
cd history-service/deploy && docker compose up -d
docker compose logs history-service | tail
```
Expected: service starts.

```bash
git add history-service/deploy/docker-compose.yml
git commit -m "deploy(history-service): mount dev KEK file"
```

---

### Task 21: Observability — metrics, tracing, structured logs

**Files:**
- Modify: `pkg/atrest/cipher.go`
- Modify: `pkg/atrest/kek_loader.go`
- Create: `pkg/atrest/metrics.go`

The spec calls for prometheus metrics, OpenTelemetry spans on Encrypt and
Decrypt, and structured `log/slog` logs that never log plaintext, full
DEKs, or full KEKs. These are wired inside `pkg/atrest` so consumers get
them for free.

- [ ] **Step 1: Define metrics**

`pkg/atrest/metrics.go`:
```go
package atrest

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	encryptCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "atrest_encrypt_total",
		Help: "Total number of payload encryptions, by result.",
	}, []string{"result"})

	decryptCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "atrest_decrypt_total",
		Help: "Total number of payload decryptions, by result.",
	}, []string{"result"})

	dekCacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "atrest_dek_cache_hits_total",
		Help: "DEK cache hits.",
	})
	dekCacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "atrest_dek_cache_misses_total",
		Help: "DEK cache misses (forced a store fetch or lazy creation).",
	})
	dekCreations = promauto.NewCounter(prometheus.CounterOpts{
		Name: "atrest_dek_creations_total",
		Help: "Lazy DEK creations.",
	})

	kekReloadCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "atrest_kek_reload_total",
		Help: "KEK file reload attempts, by result.",
	}, []string{"result"})

	kekCurrentVersion = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "atrest_kek_current_version",
		Help: "Current KEK version in memory.",
	})
)
```

> **Verify dependency:** confirm `github.com/prometheus/client_golang` is
> already in `go.mod` (other services already export Prometheus metrics).
> If absent, ask before adding.

- [ ] **Step 2: Increment counters in Cipher**

In `pkg/atrest/cipher.go`, instrument `Encrypt`, `Decrypt`, and `dekFor`:

```go
func (c *cipher) Encrypt(ctx context.Context, roomID string, fields EncryptedFields) (out []byte, meta EncMeta, err error) {
	defer func() {
		res := "ok"
		if err != nil {
			res = "error"
		}
		encryptCounter.WithLabelValues(res).Inc()
	}()
	// ... existing body ...
}
```

Mirror the pattern in `Decrypt` with `decryptCounter`.

In `dekFor`, after the cache lookup branches:
```go
if dek, ok := c.cache.get(roomID); ok {
	dekCacheHits.Inc()
	return dek, nil
}
dekCacheMisses.Inc()
```

In `createDEK`, increment `dekCreations.Inc()` once on the path where the
race-detection check confirms this goroutine inserted the row (i.e. the
branch that does **not** take the "lost the race" path).

- [ ] **Step 3: KEK reload metrics + slog**

In `kek_loader.go` `maybeReload`:
```go
if _, err := os.Stat(l.path); err != nil {
	kekReloadCounter.WithLabelValues("error").Inc()
	slog.Error("atrest: KEK stat failed", "path", l.path, "err", err)
	return
}
// ... mod-time check ...
keys, current, err := loadKEKFile(l.path)
if err != nil {
	kekReloadCounter.WithLabelValues("error").Inc()
	slog.Error("atrest: KEK reload failed", "path", l.path, "err", err)
	return
}
// after successful swap:
kekReloadCounter.WithLabelValues("ok").Inc()
kekCurrentVersion.Set(float64(current))
slog.Info("atrest: KEK reloaded", "path", l.path, "current", current, "versions", len(keys))
```

Set `kekCurrentVersion` once at construction too.

- [ ] **Step 4: Tracing spans**

Wrap `Encrypt` and `Decrypt` bodies in OpenTelemetry spans. Search for
`otel.Tracer(` in the repo and follow the convention used by other
`pkg/` packages. Span attributes: `roomID` (string), `dekCacheHit` (bool,
set after the cache branch), `payloadBytesIn` (int), `payloadBytesOut`
(int). Plaintext content MUST NOT be set as a span attribute.

- [ ] **Step 5: Lint and commit**

Run: `make lint`
```bash
git add pkg/atrest/metrics.go pkg/atrest/cipher.go pkg/atrest/kek_loader.go
git commit -m "obs(atrest): prometheus metrics, OTel spans, slog reload events"
```

---

### Task 22: Final verification pass

- [ ] **Step 1: Full repo lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 2: All unit tests with race detector**

Run: `make test`
Expected: PASS.

- [ ] **Step 3: All integration tests**

Run: `make test-integration`
Expected: PASS.

- [ ] **Step 4: Coverage spot-check**

```
go test -coverprofile=/tmp/cov.out ./pkg/atrest/... ./message-worker/... ./history-service/...
go tool cover -func=/tmp/cov.out | grep -E "atrest|message-worker|history" | tail -50
```
Confirm `pkg/atrest` is ≥ 90%; the two services are ≥ 80%.

- [ ] **Step 5: Smoke test the full local stack**

Run:
```
cd docker-local && docker compose up -d
cd ../message-worker/deploy && docker compose up -d
cd ../../history-service/deploy && docker compose up -d
```
Send a test message (use `tools/loadgen` or manual NATS publish).
Verify via cqlsh that the row has `enc_payload` non-null and `msg` null,
and that history-service returns the original plaintext on read.

- [ ] **Step 6: Push and open PR**

Run: `git push -u origin claude/add-message-encryption-XaYfk`

PR description references the spec at
`docs/superpowers/specs/2026-05-05-message-at-rest-encryption-design.md`
and lists the rollout posture (`ATREST_ENABLED=true` by default).

---

## Out of scope for this plan

The spec lists these as deliberate non-goals; this plan does not address
them and they remain for future specs:

- Backfill of pre-cutover plaintext rows in Cassandra.
- Encryption of the search index (search-sync-worker output).
- KEK rotation worker (`cmd/atrest-rotator`) — designed in the spec but
  intentionally deferred. The library exposes `DEKStore.Replace` so that
  the worker is a thin scan loop when its time comes.
- Encryption of MongoDB collections beyond `room_data_keys`.
- Per-room DEK rotation.
