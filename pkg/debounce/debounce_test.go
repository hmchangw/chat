package debounce

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAdapter simulates the Adapter interface in memory with mutex for
// concurrency safety and injectable per-method errors.
type fakeAdapter struct {
	mu         sync.Mutex
	store      map[string]float64
	triggerErr error
	claimErr   error
	removeErr  error
	requeueErr error

	removeCalls  []string
	requeueCalls []string
}

func newFakeAdapter() *fakeAdapter {
	return &fakeAdapter{store: make(map[string]float64)}
}

func (f *fakeAdapter) Trigger(_ context.Context, key string, deadline time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.triggerErr != nil {
		return f.triggerErr
	}
	f.store[key] = float64(deadline.UnixMilli())
	return nil
}

func (f *fakeAdapter) Claim(_ context.Context, now time.Time, processingTimeout time.Duration, batchSize int) ([]ClaimedEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return nil, f.claimErr
	}

	nowMs := float64(now.UnixMilli())
	deadlineMs := float64(now.Add(processingTimeout).UnixMilli())

	var expired []string
	for member, score := range f.store {
		if score <= nowMs {
			expired = append(expired, member)
		}
	}
	sort.Strings(expired)
	if len(expired) > batchSize {
		expired = expired[:batchSize]
	}

	for _, member := range expired {
		f.store[member] = deadlineMs
	}

	entries := make([]ClaimedEntry, len(expired))
	for i, m := range expired {
		entries[i] = ClaimedEntry{Key: m, ClaimedScore: deadlineMs}
	}
	return entries, nil
}

func (f *fakeAdapter) Remove(_ context.Context, key string, claimedScore float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removeCalls = append(f.removeCalls, key)
	score, ok := f.store[key]
	if ok && score == claimedScore {
		delete(f.store, key)
	}
	return nil
}

func (f *fakeAdapter) Requeue(_ context.Context, key string, deadline time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.requeueErr != nil {
		return f.requeueErr
	}
	f.requeueCalls = append(f.requeueCalls, key)
	f.store[key] = float64(deadline.UnixMilli())
	return nil
}

func (f *fakeAdapter) Close() error { return nil }

func (f *fakeAdapter) getStore() map[string]float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make(map[string]float64, len(f.store))
	for k, v := range f.store {
		cp[k] = v
	}
	return cp
}

func (f *fakeAdapter) getRemoveCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]string, len(f.removeCalls))
	copy(cp, f.removeCalls)
	return cp
}

func (f *fakeAdapter) getRequeueCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]string, len(f.requeueCalls))
	copy(cp, f.requeueCalls)
	return cp
}

func testConfig() Config {
	return Config{
		Timeout:           10 * time.Second,
		PollInterval:      1 * time.Second,
		ProcessingTimeout: 30 * time.Second,
		MaxRetries:        3,
		InitialBackoff:    1 * time.Millisecond, // fast for tests
	}
}

func fixedNow() time.Time {
	return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
}

// mustNew is a test helper that calls New and fails the test on error.
func mustNew(t *testing.T, adapter Adapter, callback Callback, cfg Config) *Debouncer {
	t.Helper()
	d, err := New(adapter, callback, cfg)
	require.NoError(t, err)
	return d
}

func TestDebouncer_Trigger(t *testing.T) {
	t.Run("happy path — stores key with correct deadline", func(t *testing.T) {
		fa := newFakeAdapter()
		cfg := testConfig()
		d := mustNew(t, fa, nil, cfg)
		d.now = fixedNow

		err := d.Trigger(context.Background(), "room:123")
		require.NoError(t, err)

		store := fa.getStore()
		expectedDeadline := float64(fixedNow().Add(cfg.Timeout).UnixMilli())
		score, ok := store["room:123"]
		require.True(t, ok, "key should exist in adapter store")
		assert.Equal(t, expectedDeadline, score)
	})

	t.Run("adapter error — propagates error", func(t *testing.T) {
		fa := newFakeAdapter()
		fa.triggerErr = errors.New("connection refused")
		cfg := testConfig()
		d := mustNew(t, fa, nil, cfg)
		d.now = fixedNow

		err := d.Trigger(context.Background(), "room:123")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection refused")
	})
}

func TestDebouncer_processEntry_success(t *testing.T) {
	fa := newFakeAdapter()
	fa.store["room:42"] = 999999

	var callbackKey string
	callback := func(_ context.Context, key string) error {
		callbackKey = key
		return nil
	}

	cfg := testConfig()
	d := mustNew(t, fa, callback, cfg)
	d.now = fixedNow

	entry := ClaimedEntry{Key: "room:42", ClaimedScore: 999999}
	d.processEntry(context.Background(), entry)

	assert.Equal(t, "room:42", callbackKey)
	removeCalls := fa.getRemoveCalls()
	require.Len(t, removeCalls, 1)
	assert.Equal(t, "room:42", removeCalls[0])

	// Entry should be removed from store.
	store := fa.getStore()
	_, exists := store["room:42"]
	assert.False(t, exists, "entry should be removed after successful callback")
}

func TestDebouncer_processEntry_retryThenSuccess(t *testing.T) {
	fa := newFakeAdapter()
	fa.store["room:99"] = 888888

	var attempts int32
	callback := func(_ context.Context, key string) error {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt <= 2 {
			return errors.New("transient error")
		}
		return nil
	}

	cfg := testConfig()
	cfg.MaxRetries = 3
	cfg.InitialBackoff = 1 * time.Millisecond
	d := mustNew(t, fa, callback, cfg)
	d.now = fixedNow

	entry := ClaimedEntry{Key: "room:99", ClaimedScore: 888888}
	d.processEntry(context.Background(), entry)

	assert.Equal(t, int32(3), atomic.LoadInt32(&attempts), "should have 3 attempts total (1 initial + 2 retries)")
	removeCalls := fa.getRemoveCalls()
	require.Len(t, removeCalls, 1)
	assert.Equal(t, "room:99", removeCalls[0])

	store := fa.getStore()
	_, exists := store["room:99"]
	assert.False(t, exists, "entry should be removed after eventual success")
}

func TestDebouncer_processEntry_exhaustedRetries(t *testing.T) {
	fa := newFakeAdapter()
	fa.store["room:fail"] = 777777

	callback := func(_ context.Context, _ string) error {
		return errors.New("permanent error")
	}

	cfg := testConfig()
	cfg.MaxRetries = 2
	cfg.InitialBackoff = 1 * time.Millisecond
	d := mustNew(t, fa, callback, cfg)
	d.now = fixedNow

	entry := ClaimedEntry{Key: "room:fail", ClaimedScore: 777777}
	d.processEntry(context.Background(), entry)

	// Should not have been removed.
	removeCalls := fa.getRemoveCalls()
	assert.Empty(t, removeCalls, "Remove should not be called when all retries exhausted")

	// Should have been requeued with new deadline.
	requeueCalls := fa.getRequeueCalls()
	require.Len(t, requeueCalls, 1)
	assert.Equal(t, "room:fail", requeueCalls[0])

	// Verify the requeued score is the new deadline.
	store := fa.getStore()
	expectedDeadline := float64(fixedNow().Add(cfg.Timeout).UnixMilli())
	score, ok := store["room:fail"]
	require.True(t, ok, "entry should still exist after requeue")
	assert.Equal(t, expectedDeadline, score)
}

func TestDebouncer_processEntry_contextCancelled(t *testing.T) {
	fa := newFakeAdapter()
	fa.store["room:cancel"] = 666666

	var attempts int32
	callback := func(_ context.Context, _ string) error {
		atomic.AddInt32(&attempts, 1)
		return errors.New("fail")
	}

	cfg := testConfig()
	cfg.MaxRetries = 5
	cfg.InitialBackoff = 1 * time.Millisecond
	d := mustNew(t, fa, callback, cfg)
	d.now = fixedNow

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so the retry backoff select picks up ctx.Done().
	cancel()

	entry := ClaimedEntry{Key: "room:cancel", ClaimedScore: 666666}
	d.processEntry(ctx, entry)

	// With context cancelled, processEntry should stop retrying after the first attempt.
	// The first attempt (attempt 0) runs without a backoff wait, so exactly 1 call.
	assert.Equal(t, int32(1), atomic.LoadInt32(&attempts),
		"should stop retrying after context is cancelled")
}

func TestDebouncer_poll_claimsAndProcesses(t *testing.T) {
	fa := newFakeAdapter()
	now := fixedNow()

	// Add expired entries.
	fa.store["key-a"] = float64(now.Add(-5 * time.Second).UnixMilli())
	fa.store["key-b"] = float64(now.Add(-3 * time.Second).UnixMilli())
	// Non-expired entry should not be processed.
	fa.store["key-c"] = float64(now.Add(10 * time.Second).UnixMilli())

	var mu sync.Mutex
	var calledKeys []string
	callback := func(_ context.Context, key string) error {
		mu.Lock()
		calledKeys = append(calledKeys, key)
		mu.Unlock()
		return nil
	}

	cfg := testConfig()
	d := mustNew(t, fa, callback, cfg)
	d.now = func() time.Time { return now }

	d.poll(context.Background())

	// Wait for goroutines spawned by poll.
	d.wg.Wait()

	mu.Lock()
	sort.Strings(calledKeys)
	mu.Unlock()

	assert.Equal(t, []string{"key-a", "key-b"}, calledKeys)
}

func TestDebouncer_poll_claimError(t *testing.T) {
	fa := newFakeAdapter()
	fa.claimErr = errors.New("valkey unavailable")

	var callbackCalled bool
	callback := func(_ context.Context, _ string) error {
		callbackCalled = true
		return nil
	}

	cfg := testConfig()
	d := mustNew(t, fa, callback, cfg)
	d.now = fixedNow

	// Should not panic or crash.
	d.poll(context.Background())
	d.wg.Wait()

	assert.False(t, callbackCalled, "callback should not be called when claim fails")
}

func TestDebouncer_Close_waitsForInFlight(t *testing.T) {
	fa := newFakeAdapter()
	now := fixedNow()

	// Add an expired entry.
	fa.store["key-slow"] = float64(now.Add(-5 * time.Second).UnixMilli())

	callbackStarted := make(chan struct{})
	callbackDone := make(chan struct{})

	callback := func(_ context.Context, _ string) error {
		close(callbackStarted)
		<-callbackDone
		return nil
	}

	cfg := testConfig()
	cfg.PollInterval = 1 * time.Millisecond
	d := mustNew(t, fa, callback, cfg)
	d.now = func() time.Time { return now }

	ctx := context.Background()

	// Start the debouncer in background.
	go func() {
		_ = d.Start(ctx)
	}()

	// Wait for callback to start.
	select {
	case <-callbackStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for callback to start")
	}

	// Close should block until the in-flight callback completes.
	closeDone := make(chan struct{})
	go func() {
		d.Close()
		close(closeDone)
	}()

	// Give Close a moment to start waiting, verify it hasn't returned yet.
	time.Sleep(10 * time.Millisecond)
	select {
	case <-closeDone:
		t.Fatal("Close returned before in-flight callback completed")
	default:
		// Expected: Close is still blocking.
	}

	// Let the callback finish.
	close(callbackDone)

	// Now Close should return.
	select {
	case <-closeDone:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Close to return")
	}
}

func TestDebouncer_processEntry_removeError(t *testing.T) {
	fa := newFakeAdapter()
	fa.store["room-1"] = 100
	fa.removeErr = errors.New("remove failed")
	d := mustNew(t, fa, func(_ context.Context, _ string) error {
		return nil
	}, testConfig())

	d.processEntry(context.Background(), ClaimedEntry{Key: "room-1", ClaimedScore: 100})

	// Callback succeeded, Remove was attempted but failed — entry still in store.
	store := fa.getStore()
	assert.Contains(t, store, "room-1", "entry should remain when Remove fails")
}

func TestDebouncer_processEntry_requeueError(t *testing.T) {
	fa := newFakeAdapter()
	fa.requeueErr = errors.New("requeue failed")
	d := mustNew(t, fa, func(_ context.Context, _ string) error {
		return errors.New("permanent error")
	}, testConfig())

	d.processEntry(context.Background(), ClaimedEntry{Key: "room-1", ClaimedScore: 100})

	// All retries exhausted, Requeue attempted but failed — entry not in store.
	store := fa.getStore()
	assert.NotContains(t, store, "room-1", "entry should not be in store when Requeue fails")
}

func TestDebouncer_Close_timesOut(t *testing.T) {
	fa := newFakeAdapter()
	d := mustNew(t, fa, func(_ context.Context, _ string) error {
		time.Sleep(10 * time.Second) // very long callback
		return nil
	}, Config{
		Timeout:           time.Second,
		PollInterval:      time.Second,
		ProcessingTimeout: 50 * time.Millisecond, // short timeout for test
		MaxRetries:        0,
		InitialBackoff:    time.Millisecond,
	})

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.processEntry(context.Background(), ClaimedEntry{Key: "room-1", ClaimedScore: 100})
	}()

	// Give the goroutine a moment to start.
	time.Sleep(10 * time.Millisecond)

	start := time.Now()
	d.Close()
	elapsed := time.Since(start)
	assert.Less(t, elapsed, time.Second, "Close should return after ProcessingTimeout, not wait for callback")
}

func TestNew_invalidConfig(t *testing.T) {
	fa := newFakeAdapter()
	cb := func(_ context.Context, _ string) error { return nil }

	tests := []struct {
		name        string
		cfg         Config
		errContains string
	}{
		{
			name:        "zero PollInterval",
			cfg:         Config{Timeout: time.Second, PollInterval: 0, ProcessingTimeout: time.Second},
			errContains: "PollInterval",
		},
		{
			name:        "zero Timeout",
			cfg:         Config{Timeout: 0, PollInterval: time.Second, ProcessingTimeout: time.Second},
			errContains: "Timeout",
		},
		{
			name:        "zero ProcessingTimeout",
			cfg:         Config{Timeout: time.Second, PollInterval: time.Second, ProcessingTimeout: 0},
			errContains: "ProcessingTimeout",
		},
		{
			name:        "zero InitialBackoff with retries",
			cfg:         Config{Timeout: time.Second, PollInterval: time.Second, ProcessingTimeout: time.Second, MaxRetries: 3, InitialBackoff: 0},
			errContains: "InitialBackoff",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(fa, cb, tt.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}
