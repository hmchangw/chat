# Distributed Debounce Library Design

## Problem

When multiple users are removed from a room in a short period, rotating the room encryption key for every individual removal is wasteful. Instead, key rotation should be debounced — a timer starts on the first removal and resets on each subsequent removal. The callback fires only once after the quiet period expires.

This must work across multiple pods: if removal events for the same room land on different pods, the timer must still reset correctly.

## Solution

A generic distributed debounce library (`pkg/debounce`) backed by a Valkey sorted set. The library is not specific to room key rotation — it debounces any action by a string key.

### Core Mechanism

A Valkey sorted set stores keys with scores representing their deadline timestamps. When `Trigger(key)` is called, `ZADD` sets the score to `now + debounceTimeout`. Because `ZADD` overwrites existing scores, a second trigger from any pod resets the deadline.

A background poll loop checks for expired entries every `PollInterval`. When an entry's deadline has passed, a Lua script atomically claims it by pushing the score to `now + processingTimeout` (a lease). The callback executes, and on success the entry is conditionally removed — only if the score hasn't changed (meaning no new trigger arrived during processing).

## Public API

```go
package debounce

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

// Debouncer is the main type.
type Debouncer struct { ... }

// New creates a Debouncer.
func New(adapter Adapter, callback Callback, cfg Config) *Debouncer

// Trigger resets the debounce timer for the given key.
// Safe to call concurrently and from any pod.
func (d *Debouncer) Trigger(ctx context.Context, key string) error

// Start begins the background poll loop. Blocks until ctx is cancelled.
func (d *Debouncer) Start(ctx context.Context) error

// Close stops the poll loop and waits for in-flight callbacks to finish.
func (d *Debouncer) Close()
```

## Adapter Interface

```go
// Adapter abstracts the sorted-set operations the Debouncer needs.
type Adapter interface {
    // Trigger sets or resets the deadline for a key.
    Trigger(ctx context.Context, key string, deadline time.Time) error

    // Claim finds up to batchSize entries whose deadline has passed, pushes
    // their deadline to now+processingTimeout (lease), and returns the
    // claimed keys along with their new scores for conditional removal.
    Claim(ctx context.Context, now time.Time, processingTimeout time.Duration, batchSize int) ([]ClaimedEntry, error)

    // Remove deletes a key only if its score still matches claimedScore
    // (i.e., no new Trigger arrived during processing).
    Remove(ctx context.Context, key string, claimedScore float64) error

    // Requeue puts a key back with a new deadline (after callback failure).
    Requeue(ctx context.Context, key string, deadline time.Time) error
}

// ClaimedEntry represents a key claimed by the poll loop.
type ClaimedEntry struct {
    Key          string
    ClaimedScore float64
}
```

### Valkey Adapter

The Valkey adapter operates on a single sorted set key (e.g., `debounce:{name}`), configurable by the caller.

**Lua scripts:**

1. **Claim** — Atomically finds up to `batchSize` expired entries (`ZRANGEBYSCORE key -inf now LIMIT 0 batchSize`), updates each score to `now + processingTimeout`, and returns the entries with their new scores. Atomicity ensures only one pod claims each entry. Batch limiting prevents overwhelming a pod after a long outage.

2. **Remove** — `ZSCORE` followed by conditional `ZREM` only if the score matches `claimedScore`. Prevents removing a key that was re-triggered during processing.

**Non-scripted operations:**

- **Trigger** — Single `ZADD key score member` command.
- **Requeue** — Same as Trigger (`ZADD` with a new deadline).

## Lifecycle

### Poll Loop (`Start`)

```
Start(ctx) launched as goroutine by caller in main.go
  └─ every PollInterval:
       1. Claim(now) → []ClaimedEntry
       2. For each claimed entry, launch goroutine:
            a. Call callback(ctx, key)
            b. Success → Remove(key, claimedScore)
            c. Failure → retry with exponential backoff (up to MaxRetries)
            d. All retries exhausted → Requeue(key, now+Timeout)
       3. Track goroutines with sync.WaitGroup
```

### Graceful Shutdown (`Close`)

1. Cancel the poll loop context (stops polling).
2. `wg.Wait()` with a deadline — wait for in-flight callbacks to finish.
3. Callbacks still running after the deadline are abandoned. Their claimed entries expire via `ProcessingTimeout` and get picked up by another pod on the next poll.

## Key Scenarios

| Scenario | Behavior |
|----------|----------|
| Two pods trigger same key within debounce window | Both call `ZADD`. Last write wins — deadline extends. |
| Pod crashes mid-callback | Claimed entry's `processingTimeout` score expires. Next poll on any pod reclaims it. |
| New trigger arrives during callback processing | `ZADD` updates score. `Remove` sees score mismatch, skips removal. Key debounces again. |
| Callback fails transiently | Retries with exponential backoff. If exhausted, `Requeue` re-enters the debounce cycle. |
| Graceful shutdown | `Close()` waits for in-flight callbacks. Uncompleted ones expire naturally via `ProcessingTimeout`. |
| No triggers for a long time | Sorted set is empty. Poll loop runs but finds nothing. |

## Testing Strategy

### Unit Tests (`debounce_test.go`)

Fake `Adapter` implementation (in-memory map simulating a sorted set). Tests for `Debouncer` logic in isolation:

- Single trigger fires callback after timeout
- Repeated triggers for same key — callback fires only once
- Multiple keys debounce independently
- Callback failure triggers retry with backoff
- All retries exhausted — key is requeued
- New trigger during processing — key is not removed
- `Close()` waits for in-flight callbacks

### Adapter Unit Tests (`adapter_test.go`)

Fake Valkey client (following `roomkeystore`'s `fakeHashClient` pattern). Tests for Lua script correctness:

- Trigger sets/resets score
- Claim returns only expired entries and updates their scores
- Claim is idempotent — second call returns nothing
- Remove is conditional on score match
- Remove skips when score has changed

### Integration Tests (`integration_test.go`, `//go:build integration`)

Real Valkey container via `testcontainers-go`:

- End-to-end: trigger, poll, callback fires
- Multi-claim safety: two goroutines polling concurrently, callback fires exactly once
- Processing timeout: simulate crash by claiming without removing, verify re-claim after timeout
- Debounce reset: trigger, wait half the timeout, trigger again, verify callback fires only once after full window from second trigger

## File Layout

```
pkg/debounce/
├── debounce.go           # Debouncer type, New(), Trigger(), Start(), Close()
├── adapter.go            # Adapter interface, ClaimedEntry, Valkey adapter, Lua scripts
├── debounce_test.go      # Unit tests for Debouncer (fake adapter)
├── adapter_test.go       # Unit tests for Valkey adapter (fake client)
└── integration_test.go   # Integration tests with real Valkey (testcontainers)
```
