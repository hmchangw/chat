# Distributed Debounce Library Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a generic distributed debounce library (`pkg/debounce`) backed by a Valkey sorted set that coalesces rapid triggers for the same key into a single callback invocation, even across multiple pods.

**Architecture:** Valkey sorted set stores keys with deadline scores. `Trigger(key)` sets/resets the deadline via `ZADD`. A background poll loop claims expired entries atomically with a Lua script (lease pattern), fires the callback, and conditionally removes the entry. Retry with exponential backoff on failure; requeue after exhaustion.

**Tech Stack:** Go 1.25, `github.com/redis/go-redis/v9`, Lua scripts for atomicity, `testcontainers-go` for integration tests.

**Spec:** `docs/superpowers/specs/2026-04-08-distributed-debounce-library-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `pkg/debounce/adapter.go` | `Adapter` interface, `ClaimedEntry`, internal `sortedSetCommander` interface, `valkeyAdapter` (implements `Adapter`), `redisAdapter` (implements `sortedSetCommander`), Lua scripts, `NewValkeyAdapter` constructor |
| `pkg/debounce/debounce.go` | `Callback` type, `Config` struct, `Debouncer` struct, `New()`, `Trigger()`, `Start()`, `Close()`, internal `poll()` and `processEntry()` |
| `pkg/debounce/adapter_test.go` | `fakeSortedSetCommander`, unit tests for `valkeyAdapter` |
| `pkg/debounce/debounce_test.go` | `fakeAdapter`, unit tests for `Debouncer` |
| `pkg/debounce/integration_test.go` | Integration tests with real Valkey via testcontainers |

---

### Task 1: Adapter Interface, Types, and Stubs

**Files:**
- Create: `pkg/debounce/adapter.go`

- [ ] **Step 1: Create `adapter.go` with all type definitions and stub methods**

```go
package debounce

import (
	"context"
	"time"
)

// ClaimedEntry represents a key claimed by the poll loop.
type ClaimedEntry struct {
	Key          string
	ClaimedScore float64
}

// Adapter abstracts the sorted-set operations the Debouncer needs.
type Adapter interface {
	// Trigger sets or resets the deadline for a key.
	Trigger(ctx context.Context, key string, deadline time.Time) error

	// Claim finds up to batchSize entries whose deadline has passed, pushes
	// their deadline to now+processingTimeout (lease), and returns the
	// claimed keys along with their new scores for conditional removal.
	Claim(ctx context.Context, now time.Time, processingTimeout time.Duration, batchSize int) ([]ClaimedEntry, error)

	// Remove deletes a key only if its score still matches claimedScore.
	Remove(ctx context.Context, key string, claimedScore float64) error

	// Requeue puts a key back with a new deadline (after callback failure).
	Requeue(ctx context.Context, key string, deadline time.Time) error
}

// sortedSetCommander is a minimal internal interface over the Valkey sorted-set
// commands used by valkeyAdapter. Unexported so unit tests can inject a fake
// without a live Valkey connection.
type sortedSetCommander interface {
	zadd(ctx context.Context, member string, score float64) error
	claimExpired(ctx context.Context, now float64, processingDeadline float64, batchSize int) ([]string, error)
	removeIfScore(ctx context.Context, member string, expectedScore float64) error
}

// valkeyAdapter implements Adapter using a sortedSetCommander.
type valkeyAdapter struct {
	client sortedSetCommander
}

func (a *valkeyAdapter) Trigger(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (a *valkeyAdapter) Claim(_ context.Context, _ time.Time, _ time.Duration, _ int) ([]ClaimedEntry, error) {
	return nil, nil
}

func (a *valkeyAdapter) Remove(_ context.Context, _ string, _ float64) error {
	return nil
}

func (a *valkeyAdapter) Requeue(_ context.Context, _ string, _ time.Time) error {
	return nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./pkg/debounce/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/debounce/adapter.go
git commit -m "pkg/debounce: add adapter interface, types, and stubs"
```

---

### Task 2: Adapter Unit Tests (RED)

**Files:**
- Create: `pkg/debounce/adapter_test.go`

- [ ] **Step 1: Write `adapter_test.go` with fakeSortedSetCommander and all tests**

The fake simulates sorted-set behavior in memory, following the `roomkeystore` pattern of `fakeHashClient` (see `pkg/roomkeystore/roomkeystore_test.go`).

```go
package debounce

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSortedSetCommander simulates Valkey sorted-set operations in memory.
type fakeSortedSetCommander struct {
	store     map[string]float64 // member -> score
	zaddErr   error
	claimErr  error
	removeErr error
}

func (f *fakeSortedSetCommander) zadd(_ context.Context, member string, score float64) error {
	if f.zaddErr != nil {
		return f.zaddErr
	}
	if f.store == nil {
		f.store = make(map[string]float64)
	}
	f.store[member] = score
	return nil
}

func (f *fakeSortedSetCommander) claimExpired(_ context.Context, now float64, processingDeadline float64, batchSize int) ([]string, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	var expired []string
	for member, score := range f.store {
		if score <= now {
			expired = append(expired, member)
		}
	}
	sort.Strings(expired)
	if len(expired) > batchSize {
		expired = expired[:batchSize]
	}
	for _, member := range expired {
		f.store[member] = processingDeadline
	}
	return expired, nil
}

func (f *fakeSortedSetCommander) removeIfScore(_ context.Context, member string, expectedScore float64) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	if score, ok := f.store[member]; ok && score == expectedScore {
		delete(f.store, member)
	}
	return nil
}

func newTestAdapter(fake *fakeSortedSetCommander) *valkeyAdapter {
	return &valkeyAdapter{client: fake}
}

func TestValkeyAdapter_Trigger(t *testing.T) {
	tests := []struct {
		name        string
		fake        *fakeSortedSetCommander
		key         string
		deadline    time.Time
		wantErr     bool
		errContains string
	}{
		{
			name:     "happy path — stores member with deadline as score",
			fake:     &fakeSortedSetCommander{store: map[string]float64{}},
			key:      "room-1",
			deadline: time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC),
		},
		{
			name:        "zadd error — returns wrapped error",
			fake:        &fakeSortedSetCommander{zaddErr: errors.New("connection refused")},
			key:         "room-1",
			deadline:    time.Now(),
			wantErr:     true,
			errContains: "set debounce deadline",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAdapter(tt.fake)
			err := a.Trigger(context.Background(), tt.key, tt.deadline)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			score, ok := tt.fake.store[tt.key]
			require.True(t, ok, "member should exist in store")
			assert.Equal(t, float64(tt.deadline.UnixMilli()), score)
		})
	}
}

func TestValkeyAdapter_Claim(t *testing.T) {
	now := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	processingTimeout := 30 * time.Second
	expectedDeadline := float64(now.Add(processingTimeout).UnixMilli())

	tests := []struct {
		name        string
		fake        *fakeSortedSetCommander
		batchSize   int
		wantKeys    []string
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path — claims expired entries",
			fake: &fakeSortedSetCommander{store: map[string]float64{
				"room-1": float64(now.Add(-5 * time.Second).UnixMilli()),
				"room-2": float64(now.Add(-1 * time.Second).UnixMilli()),
				"room-3": float64(now.Add(10 * time.Second).UnixMilli()), // not expired
			}},
			batchSize: 10,
			wantKeys:  []string{"room-1", "room-2"},
		},
		{
			name:      "no expired entries — returns empty",
			fake:      &fakeSortedSetCommander{store: map[string]float64{}},
			batchSize: 10,
			wantKeys:  nil,
		},
		{
			name: "batch limit — returns at most batchSize",
			fake: &fakeSortedSetCommander{store: map[string]float64{
				"room-1": float64(now.Add(-5 * time.Second).UnixMilli()),
				"room-2": float64(now.Add(-1 * time.Second).UnixMilli()),
			}},
			batchSize: 1,
			wantKeys:  []string{"room-1"},
		},
		{
			name:        "claim error — returns wrapped error",
			fake:        &fakeSortedSetCommander{claimErr: errors.New("script error")},
			batchSize:   10,
			wantErr:     true,
			errContains: "claim expired entries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAdapter(tt.fake)
			entries, err := a.Claim(context.Background(), now, processingTimeout, tt.batchSize)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			var keys []string
			for _, e := range entries {
				keys = append(keys, e.Key)
				assert.Equal(t, expectedDeadline, e.ClaimedScore, "claimed score should be processingDeadline")
			}
			assert.Equal(t, tt.wantKeys, keys)

			// Verify claimed entries had their scores updated in the store.
			for _, key := range tt.wantKeys {
				assert.Equal(t, expectedDeadline, tt.fake.store[key], "store score should be updated to processingDeadline")
			}
		})
	}
}

func TestValkeyAdapter_Remove(t *testing.T) {
	tests := []struct {
		name        string
		fake        *fakeSortedSetCommander
		key         string
		score       float64
		wantRemoved bool
		wantErr     bool
		errContains string
	}{
		{
			name:        "happy path — removes when score matches",
			fake:        &fakeSortedSetCommander{store: map[string]float64{"room-1": 100}},
			key:         "room-1",
			score:       100,
			wantRemoved: true,
		},
		{
			name:        "score mismatch — does not remove",
			fake:        &fakeSortedSetCommander{store: map[string]float64{"room-1": 200}},
			key:         "room-1",
			score:       100,
			wantRemoved: false,
		},
		{
			name:        "remove error — returns wrapped error",
			fake:        &fakeSortedSetCommander{removeErr: errors.New("io error")},
			key:         "room-1",
			score:       100,
			wantErr:     true,
			errContains: "conditional remove",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAdapter(tt.fake)
			err := a.Remove(context.Background(), tt.key, tt.score)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			_, exists := tt.fake.store[tt.key]
			if tt.wantRemoved {
				assert.False(t, exists, "member should be removed")
			} else {
				assert.True(t, exists, "member should still exist")
			}
		})
	}
}

func TestValkeyAdapter_Requeue(t *testing.T) {
	tests := []struct {
		name        string
		fake        *fakeSortedSetCommander
		key         string
		deadline    time.Time
		wantErr     bool
		errContains string
	}{
		{
			name:     "happy path — sets new deadline",
			fake:     &fakeSortedSetCommander{store: map[string]float64{}},
			key:      "room-1",
			deadline: time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC),
		},
		{
			name:        "zadd error — returns wrapped error",
			fake:        &fakeSortedSetCommander{zaddErr: errors.New("connection refused")},
			key:         "room-1",
			deadline:    time.Now(),
			wantErr:     true,
			errContains: "requeue debounce entry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAdapter(tt.fake)
			err := a.Requeue(context.Background(), tt.key, tt.deadline)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			score, ok := tt.fake.store[tt.key]
			require.True(t, ok)
			assert.Equal(t, float64(tt.deadline.UnixMilli()), score)
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/debounce`
Expected: FAIL — stub methods return zero values, so assertions like `score == deadline.UnixMilli()` fail.

---

### Task 3: Adapter Implementation (GREEN)

**Files:**
- Modify: `pkg/debounce/adapter.go` (replace stub methods)

- [ ] **Step 1: Implement valkeyAdapter methods**

Replace the four stub methods in `adapter.go` with:

```go
func (a *valkeyAdapter) Trigger(ctx context.Context, key string, deadline time.Time) error {
	score := float64(deadline.UnixMilli())
	if err := a.client.zadd(ctx, key, score); err != nil {
		return fmt.Errorf("set debounce deadline: %w", err)
	}
	return nil
}

func (a *valkeyAdapter) Claim(ctx context.Context, now time.Time, processingTimeout time.Duration, batchSize int) ([]ClaimedEntry, error) {
	nowMs := float64(now.UnixMilli())
	deadlineMs := float64(now.Add(processingTimeout).UnixMilli())
	members, err := a.client.claimExpired(ctx, nowMs, deadlineMs, batchSize)
	if err != nil {
		return nil, fmt.Errorf("claim expired entries: %w", err)
	}
	entries := make([]ClaimedEntry, len(members))
	for i, m := range members {
		entries[i] = ClaimedEntry{Key: m, ClaimedScore: deadlineMs}
	}
	return entries, nil
}

func (a *valkeyAdapter) Remove(ctx context.Context, key string, claimedScore float64) error {
	if err := a.client.removeIfScore(ctx, key, claimedScore); err != nil {
		return fmt.Errorf("conditional remove: %w", err)
	}
	return nil
}

func (a *valkeyAdapter) Requeue(ctx context.Context, key string, deadline time.Time) error {
	score := float64(deadline.UnixMilli())
	if err := a.client.zadd(ctx, key, score); err != nil {
		return fmt.Errorf("requeue debounce entry: %w", err)
	}
	return nil
}
```

Add `"fmt"` to the imports.

- [ ] **Step 2: Run tests to verify they pass**

Run: `make test SERVICE=pkg/debounce`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add pkg/debounce/adapter.go pkg/debounce/adapter_test.go
git commit -m "pkg/debounce: implement valkeyAdapter with unit tests"
```

---

### Task 4: Redis Adapter, Lua Scripts, and Constructor

**Files:**
- Modify: `pkg/debounce/adapter.go`

- [ ] **Step 1: Add redisAdapter, Lua scripts, and NewValkeyAdapter**

Append to `adapter.go` (after the `valkeyAdapter` methods). Follow the pattern in `pkg/roomkeystore/adapter.go`.

```go
// --- Redis/Valkey implementation of sortedSetCommander ---

// redisAdapter wraps *redis.Client to satisfy sortedSetCommander.
type redisAdapter struct {
	c   *redis.Client
	key string // sorted set key name
}

func (a *redisAdapter) zadd(ctx context.Context, member string, score float64) error {
	return a.c.ZAdd(ctx, a.key, redis.Z{Score: score, Member: member}).Err()
}

// claimScript atomically finds expired entries, updates their scores to a
// processing deadline, and returns them as a flat list of member names.
// Runs as a single Lua script so no other client can interleave.
var claimScript = redis.NewScript(`
local key = KEYS[1]
local batchSize = tonumber(ARGV[3])

local entries = redis.call('ZRANGEBYSCORE', key, '-inf', ARGV[1], 'LIMIT', 0, batchSize)
if #entries == 0 then
    return {}
end
for _, member in ipairs(entries) do
    redis.call('ZADD', key, ARGV[2], member)
end
return entries
`)

func (a *redisAdapter) claimExpired(ctx context.Context, now float64, processingDeadline float64, batchSize int) ([]string, error) {
	raw, err := claimScript.Run(ctx, a.c, []string{a.key}, now, processingDeadline, batchSize).StringSlice()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	return raw, nil
}

// removeScript conditionally removes a member only if its current score
// matches the expected score. Prevents removing a key that was re-triggered
// during processing.
var removeScript = redis.NewScript(`
local key = KEYS[1]
local member = ARGV[1]
local expectedScore = ARGV[2]

local score = redis.call('ZSCORE', key, member)
if score and tonumber(score) == tonumber(expectedScore) then
    redis.call('ZREM', key, member)
end
return 0
`)

func (a *redisAdapter) removeIfScore(ctx context.Context, member string, expectedScore float64) error {
	return removeScript.Run(ctx, a.c, []string{a.key}, member, expectedScore).Err()
}

// AdapterConfig holds Valkey connection configuration for the debounce adapter.
type AdapterConfig struct {
	Addr     string
	Password string
	Key      string // Valkey sorted set key name, e.g., "debounce:room-key-rotation"
}

// NewValkeyAdapter creates an Adapter backed by Valkey, pings to verify connectivity.
func NewValkeyAdapter(cfg AdapterConfig) (Adapter, error) {
	c := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("valkey connect: %w", err)
	}
	return &valkeyAdapter{client: &redisAdapter{c: c, key: cfg.Key}}, nil
}
```

Add `"github.com/redis/go-redis/v9"` to the imports.

- [ ] **Step 2: Verify compilation**

Run: `go build ./pkg/debounce/`
Expected: no errors

- [ ] **Step 3: Run existing unit tests still pass**

Run: `make test SERVICE=pkg/debounce`
Expected: PASS (new code is additive, doesn't change existing logic)

- [ ] **Step 4: Commit**

```bash
git add pkg/debounce/adapter.go
git commit -m "pkg/debounce: add redisAdapter, Lua scripts, and NewValkeyAdapter"
```

---

### Task 5: Debouncer Types and Stubs

**Files:**
- Create: `pkg/debounce/debounce.go`

- [ ] **Step 1: Create `debounce.go` with types and stub methods**

```go
package debounce

import (
	"context"
	"sync"
	"time"
)

const defaultBatchSize = 100

// Callback is invoked when the debounce timer expires for a key.
type Callback func(ctx context.Context, key string) error

// Config holds debounce tuning parameters.
type Config struct {
	Timeout           time.Duration // Debounce quiet period (e.g., 10s)
	PollInterval      time.Duration // How often to check for expired keys (e.g., 1s)
	ProcessingTimeout time.Duration // Claim lease duration (e.g., 30s)
	MaxRetries        int           // Retry attempts on callback failure (e.g., 3)
	InitialBackoff    time.Duration // First retry delay, doubles each attempt (e.g., 2s)
}

// Debouncer manages distributed debounce timers via a Valkey sorted set.
type Debouncer struct {
	adapter  Adapter
	callback Callback
	cfg      Config
	now      func() time.Time // for testing; defaults to time.Now
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// New creates a Debouncer.
func New(adapter Adapter, callback Callback, cfg Config) *Debouncer {
	return &Debouncer{
		adapter:  adapter,
		callback: callback,
		cfg:      cfg,
		now:      time.Now,
	}
}

// Trigger resets the debounce timer for the given key.
func (d *Debouncer) Trigger(_ context.Context, _ string) error {
	return nil
}

// Start begins the background poll loop. Blocks until ctx is cancelled.
func (d *Debouncer) Start(_ context.Context) error {
	return nil
}

// Close stops the poll loop and waits for in-flight callbacks to finish.
func (d *Debouncer) Close() {
}

func (d *Debouncer) poll(_ context.Context) {
}

func (d *Debouncer) processEntry(_ context.Context, _ ClaimedEntry) {
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./pkg/debounce/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/debounce/debounce.go
git commit -m "pkg/debounce: add Debouncer types, Config, and stubs"
```

---

### Task 6: Debouncer Unit Tests (RED)

**Files:**
- Create: `pkg/debounce/debounce_test.go`

- [ ] **Step 1: Write fakeAdapter and debouncer tests**

The `fakeAdapter` simulates full sorted-set behavior so the Debouncer's poll/process logic can be tested without Valkey. Tests call unexported methods (`poll`, `processEntry`) directly for fast, deterministic assertions.

```go
package debounce

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAdapter simulates sorted-set behavior in memory for Debouncer unit tests.
type fakeAdapter struct {
	mu         sync.Mutex
	entries    map[string]float64
	triggerErr error
	claimErr   error
	removeErr  error
	requeueErr error
}

func (f *fakeAdapter) Trigger(_ context.Context, key string, deadline time.Time) error {
	if f.triggerErr != nil {
		return f.triggerErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		f.entries = make(map[string]float64)
	}
	f.entries[key] = float64(deadline.UnixMilli())
	return nil
}

func (f *fakeAdapter) Claim(_ context.Context, now time.Time, processingTimeout time.Duration, batchSize int) ([]ClaimedEntry, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	nowMs := float64(now.UnixMilli())
	deadlineMs := float64(now.Add(processingTimeout).UnixMilli())
	var claimed []ClaimedEntry
	for key, score := range f.entries {
		if score <= nowMs && len(claimed) < batchSize {
			f.entries[key] = deadlineMs
			claimed = append(claimed, ClaimedEntry{Key: key, ClaimedScore: deadlineMs})
		}
	}
	return claimed, nil
}

func (f *fakeAdapter) Remove(_ context.Context, key string, claimedScore float64) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if score, ok := f.entries[key]; ok && score == claimedScore {
		delete(f.entries, key)
	}
	return nil
}

func (f *fakeAdapter) Requeue(_ context.Context, key string, deadline time.Time) error {
	if f.requeueErr != nil {
		return f.requeueErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		f.entries = make(map[string]float64)
	}
	f.entries[key] = float64(deadline.UnixMilli())
	return nil
}

func TestDebouncer_Trigger(t *testing.T) {
	now := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	timeout := 10 * time.Second

	t.Run("happy path — stores key with correct deadline", func(t *testing.T) {
		fa := &fakeAdapter{entries: map[string]float64{}}
		d := New(fa, nil, Config{Timeout: timeout})
		d.now = func() time.Time { return now }

		err := d.Trigger(context.Background(), "room-1")
		require.NoError(t, err)

		fa.mu.Lock()
		score, ok := fa.entries["room-1"]
		fa.mu.Unlock()
		require.True(t, ok)
		assert.Equal(t, float64(now.Add(timeout).UnixMilli()), score)
	})

	t.Run("adapter error — propagates", func(t *testing.T) {
		fa := &fakeAdapter{triggerErr: errors.New("connection refused")}
		d := New(fa, nil, Config{Timeout: timeout})

		err := d.Trigger(context.Background(), "room-1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection refused")
	})
}

func TestDebouncer_processEntry_success(t *testing.T) {
	fa := &fakeAdapter{entries: map[string]float64{"room-1": 100}}
	callbackCalled := false
	d := New(fa, func(_ context.Context, key string) error {
		callbackCalled = true
		assert.Equal(t, "room-1", key)
		return nil
	}, Config{MaxRetries: 3, InitialBackoff: time.Millisecond, Timeout: time.Second})

	d.processEntry(context.Background(), ClaimedEntry{Key: "room-1", ClaimedScore: 100})

	assert.True(t, callbackCalled)
	fa.mu.Lock()
	_, exists := fa.entries["room-1"]
	fa.mu.Unlock()
	assert.False(t, exists, "entry should be removed after successful callback")
}

func TestDebouncer_processEntry_retryThenSuccess(t *testing.T) {
	fa := &fakeAdapter{entries: map[string]float64{"room-1": 100}}
	attempts := 0
	d := New(fa, func(_ context.Context, _ string) error {
		attempts++
		if attempts < 3 {
			return errors.New("transient error")
		}
		return nil
	}, Config{MaxRetries: 3, InitialBackoff: time.Millisecond, Timeout: time.Second})

	d.processEntry(context.Background(), ClaimedEntry{Key: "room-1", ClaimedScore: 100})

	assert.Equal(t, 3, attempts)
	fa.mu.Lock()
	_, exists := fa.entries["room-1"]
	fa.mu.Unlock()
	assert.False(t, exists, "entry should be removed after eventual success")
}

func TestDebouncer_processEntry_exhaustedRetries(t *testing.T) {
	fa := &fakeAdapter{entries: map[string]float64{}}
	d := New(fa, func(_ context.Context, _ string) error {
		return errors.New("permanent error")
	}, Config{MaxRetries: 2, InitialBackoff: time.Millisecond, Timeout: 10 * time.Millisecond})
	d.now = time.Now

	d.processEntry(context.Background(), ClaimedEntry{Key: "room-1", ClaimedScore: 100})

	fa.mu.Lock()
	score, exists := fa.entries["room-1"]
	fa.mu.Unlock()
	assert.True(t, exists, "entry should be requeued after exhausted retries")
	assert.Greater(t, score, float64(0), "requeue deadline should be set")
}

func TestDebouncer_processEntry_contextCancelled(t *testing.T) {
	fa := &fakeAdapter{entries: map[string]float64{}}
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	d := New(fa, func(_ context.Context, _ string) error {
		attempts++
		cancel() // cancel after first failure
		return errors.New("fail")
	}, Config{MaxRetries: 5, InitialBackoff: time.Millisecond, Timeout: time.Second})

	d.processEntry(ctx, ClaimedEntry{Key: "room-1", ClaimedScore: 100})

	assert.Equal(t, 1, attempts, "should stop retrying after context cancellation")
}

func TestDebouncer_poll_claimsAndProcesses(t *testing.T) {
	now := time.Now()
	expiredScore := float64(now.Add(-time.Second).UnixMilli())
	fa := &fakeAdapter{entries: map[string]float64{
		"room-1": expiredScore,
		"room-2": expiredScore,
	}}
	var mu sync.Mutex
	calledKeys := map[string]bool{}
	d := New(fa, func(_ context.Context, key string) error {
		mu.Lock()
		calledKeys[key] = true
		mu.Unlock()
		return nil
	}, Config{
		ProcessingTimeout: time.Minute,
		MaxRetries:        0,
		InitialBackoff:    time.Millisecond,
		Timeout:           time.Second,
	})
	d.now = func() time.Time { return now }

	d.poll(context.Background())
	d.wg.Wait()

	mu.Lock()
	assert.True(t, calledKeys["room-1"])
	assert.True(t, calledKeys["room-2"])
	mu.Unlock()
}

func TestDebouncer_poll_claimError(t *testing.T) {
	fa := &fakeAdapter{claimErr: errors.New("valkey down")}
	d := New(fa, func(_ context.Context, _ string) error {
		t.Fatal("callback should not be called when claim fails")
		return nil
	}, Config{ProcessingTimeout: time.Minute})
	d.now = time.Now

	// Should not panic or crash.
	d.poll(context.Background())
	d.wg.Wait()
}

func TestDebouncer_Close_waitsForInFlight(t *testing.T) {
	fa := &fakeAdapter{entries: map[string]float64{}}
	callbackDone := make(chan struct{})
	completed := false
	d := New(fa, func(_ context.Context, _ string) error {
		<-callbackDone
		completed = true
		return nil
	}, Config{
		ProcessingTimeout: 5 * time.Second,
		MaxRetries:        0,
		InitialBackoff:    time.Millisecond,
		Timeout:           time.Second,
	})

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.processEntry(context.Background(), ClaimedEntry{Key: "room-1", ClaimedScore: 100})
	}()

	go func() {
		time.Sleep(50 * time.Millisecond)
		close(callbackDone)
	}()

	d.Close()
	assert.True(t, completed, "Close should wait for in-flight callback to finish")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/debounce`
Expected: FAIL — stub methods in `debounce.go` don't do anything, so callbacks are never called, entries are never stored.

---

### Task 7: Debouncer Implementation (GREEN)

**Files:**
- Modify: `pkg/debounce/debounce.go` (replace stub methods)

- [ ] **Step 1: Implement Trigger**

Replace the stub:

```go
// Trigger resets the debounce timer for the given key.
func (d *Debouncer) Trigger(ctx context.Context, key string) error {
	deadline := d.now().Add(d.cfg.Timeout)
	return d.adapter.Trigger(ctx, key, deadline)
}
```

- [ ] **Step 2: Implement processEntry**

Replace the stub:

```go
func (d *Debouncer) processEntry(ctx context.Context, entry ClaimedEntry) {
	backoff := d.cfg.InitialBackoff
	for attempt := 0; attempt <= d.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff *= 2
			}
		}
		if err := d.callback(ctx, entry.Key); err != nil {
			slog.Warn("debounce callback failed",
				"key", entry.Key, "attempt", attempt+1, "error", err)
			continue
		}
		if err := d.adapter.Remove(ctx, entry.Key, entry.ClaimedScore); err != nil {
			slog.Error("debounce remove failed", "key", entry.Key, "error", err)
		}
		return
	}
	deadline := d.now().Add(d.cfg.Timeout)
	if err := d.adapter.Requeue(ctx, entry.Key, deadline); err != nil {
		slog.Error("debounce requeue failed", "key", entry.Key, "error", err)
	}
}
```

Add `"log/slog"` to imports.

- [ ] **Step 3: Implement poll**

Replace the stub:

```go
func (d *Debouncer) poll(ctx context.Context) {
	entries, err := d.adapter.Claim(ctx, d.now(), d.cfg.ProcessingTimeout, defaultBatchSize)
	if err != nil {
		slog.Error("debounce claim failed", "error", err)
		return
	}
	for _, entry := range entries {
		d.wg.Add(1)
		go func(e ClaimedEntry) {
			defer d.wg.Done()
			d.processEntry(ctx, e)
		}(entry)
	}
}
```

- [ ] **Step 4: Implement Start and Close**

Replace the stubs:

```go
// Start begins the background poll loop. Blocks until ctx is cancelled.
func (d *Debouncer) Start(ctx context.Context) error {
	ctx, d.cancel = context.WithCancel(ctx)
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			d.poll(ctx)
		}
	}
}

// Close stops the poll loop and waits for in-flight callbacks to finish.
func (d *Debouncer) Close() {
	if d.cancel != nil {
		d.cancel()
	}
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d.cfg.ProcessingTimeout):
		slog.Warn("debounce close timed out waiting for in-flight callbacks")
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `make test SERVICE=pkg/debounce`
Expected: PASS

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add pkg/debounce/debounce.go pkg/debounce/debounce_test.go
git commit -m "pkg/debounce: implement Debouncer with unit tests"
```

---

### Task 8: Integration Tests

**Files:**
- Create: `pkg/debounce/integration_test.go`

- [ ] **Step 1: Write integration tests with testcontainers**

Follow the pattern in `pkg/roomkeystore/integration_test.go`.

```go
//go:build integration

package debounce

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// setupValkey starts a valkey/valkey:8 container and returns a connected Adapter.
// The container is terminated via t.Cleanup.
func setupValkey(t *testing.T, key string) Adapter {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "valkey/valkey:8",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForLog("Ready to accept connections"),
		},
		Started: true,
	})
	require.NoError(t, err, "start valkey container")
	t.Cleanup(func() {
		_ = container.Terminate(ctx)
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379")
	require.NoError(t, err)

	adapter, err := NewValkeyAdapter(AdapterConfig{
		Addr: fmt.Sprintf("%s:%s", host, port.Port()),
		Key:  key,
	})
	require.NoError(t, err, "create adapter")
	return adapter
}

func TestDebouncer_Integration_EndToEnd(t *testing.T) {
	adapter := setupValkey(t, "debounce:e2e")
	var callCount atomic.Int32
	var calledKey atomic.Value

	d := New(adapter, func(_ context.Context, key string) error {
		callCount.Add(1)
		calledKey.Store(key)
		return nil
	}, Config{
		Timeout:           500 * time.Millisecond,
		PollInterval:      100 * time.Millisecond,
		ProcessingTimeout: 5 * time.Second,
		MaxRetries:        0,
		InitialBackoff:    time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Start(ctx)

	err := d.Trigger(context.Background(), "room-1")
	require.NoError(t, err)

	// Wait for debounce timeout + a few poll intervals.
	time.Sleep(900 * time.Millisecond)

	assert.Equal(t, int32(1), callCount.Load(), "callback should fire exactly once")
	assert.Equal(t, "room-1", calledKey.Load())

	d.Close()
}

func TestDebouncer_Integration_DebounceReset(t *testing.T) {
	adapter := setupValkey(t, "debounce:reset")
	var callCount atomic.Int32

	d := New(adapter, func(_ context.Context, _ string) error {
		callCount.Add(1)
		return nil
	}, Config{
		Timeout:           500 * time.Millisecond,
		PollInterval:      100 * time.Millisecond,
		ProcessingTimeout: 5 * time.Second,
		MaxRetries:        0,
		InitialBackoff:    time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Start(ctx)

	// Trigger, wait 300ms (< 500ms timeout), trigger again.
	err := d.Trigger(context.Background(), "room-1")
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int32(0), callCount.Load(), "callback should not fire before timeout")

	err = d.Trigger(context.Background(), "room-1")
	require.NoError(t, err)

	// Wait 300ms more — 600ms from first trigger but only 300ms from second.
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int32(0), callCount.Load(), "callback should not fire — debounce was reset")

	// Wait remaining ~300ms for the second trigger's timeout to expire.
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, int32(1), callCount.Load(), "callback should fire exactly once after debounce window")

	d.Close()
}

func TestDebouncer_Integration_MultipleKeys(t *testing.T) {
	adapter := setupValkey(t, "debounce:multi")
	var mu sync.Mutex
	calledKeys := map[string]int{}

	d := New(adapter, func(_ context.Context, key string) error {
		mu.Lock()
		calledKeys[key]++
		mu.Unlock()
		return nil
	}, Config{
		Timeout:           300 * time.Millisecond,
		PollInterval:      100 * time.Millisecond,
		ProcessingTimeout: 5 * time.Second,
		MaxRetries:        0,
		InitialBackoff:    time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Start(ctx)

	require.NoError(t, d.Trigger(context.Background(), "room-1"))
	require.NoError(t, d.Trigger(context.Background(), "room-2"))
	require.NoError(t, d.Trigger(context.Background(), "room-3"))

	time.Sleep(700 * time.Millisecond)

	mu.Lock()
	assert.Equal(t, 1, calledKeys["room-1"])
	assert.Equal(t, 1, calledKeys["room-2"])
	assert.Equal(t, 1, calledKeys["room-3"])
	mu.Unlock()

	d.Close()
}

func TestDebouncer_Integration_MultiClaimSafety(t *testing.T) {
	adapter := setupValkey(t, "debounce:multiclaim")
	var callCount atomic.Int32

	cfg := Config{
		Timeout:           300 * time.Millisecond,
		PollInterval:      50 * time.Millisecond,
		ProcessingTimeout: 5 * time.Second,
		MaxRetries:        0,
		InitialBackoff:    time.Millisecond,
	}

	callback := func(_ context.Context, _ string) error {
		callCount.Add(1)
		return nil
	}

	// Two debouncers sharing the same Valkey key — simulates two pods.
	d1 := New(adapter, callback, cfg)
	d2 := New(adapter, callback, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d1.Start(ctx)
	go d2.Start(ctx)

	require.NoError(t, d1.Trigger(context.Background(), "room-1"))

	time.Sleep(700 * time.Millisecond)

	assert.Equal(t, int32(1), callCount.Load(), "callback should fire exactly once across both debouncers")

	d1.Close()
	d2.Close()
}

func TestDebouncer_Integration_ProcessingTimeout(t *testing.T) {
	adapter := setupValkey(t, "debounce:proctimeout")
	var callCount atomic.Int32

	d := New(adapter, func(_ context.Context, _ string) error {
		callCount.Add(1)
		return nil
	}, Config{
		Timeout:           200 * time.Millisecond,
		PollInterval:      100 * time.Millisecond,
		ProcessingTimeout: 1 * time.Second,
		MaxRetries:        0,
		InitialBackoff:    time.Millisecond,
	})

	ctx := context.Background()

	// Trigger and wait for expiry.
	require.NoError(t, d.Trigger(ctx, "room-1"))
	time.Sleep(300 * time.Millisecond)

	// Manually claim without processing (simulate crash).
	_, err := d.adapter.Claim(ctx, time.Now(), 1*time.Second, 10)
	require.NoError(t, err)

	// Start the debouncer — it should re-claim after processingTimeout.
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go d.Start(dctx)

	// Wait for processingTimeout to expire + poll.
	time.Sleep(1500 * time.Millisecond)

	assert.Equal(t, int32(1), callCount.Load(), "callback should fire after processing timeout expires")

	d.Close()
}
```

- [ ] **Step 2: Run integration tests**

Run: `make test-integration SERVICE=pkg/debounce`
Expected: PASS (requires Docker)

- [ ] **Step 3: Commit**

```bash
git add pkg/debounce/integration_test.go
git commit -m "pkg/debounce: add integration tests with testcontainers"
```

---

### Task 9: Final Verification

- [ ] **Step 1: Run all unit tests**

Run: `make test SERVICE=pkg/debounce`
Expected: PASS

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: no errors

- [ ] **Step 3: Run integration tests**

Run: `make test-integration SERVICE=pkg/debounce`
Expected: PASS

- [ ] **Step 4: Check test coverage**

Run: `go test -race -coverprofile=coverage.out ./pkg/debounce/ && go tool cover -func=coverage.out`
Expected: >= 80% coverage

- [ ] **Step 5: Push**

```bash
git push -u origin claude/batch-key-rotation-3vpc4
```
