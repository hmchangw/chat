package debounce

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMiniredis starts an in-process miniredis server and returns a connected Adapter.
// No Docker required. Server is stopped via t.Cleanup.
func setupMiniredis(t *testing.T, key string) Adapter {
	t.Helper()
	mr := miniredis.RunT(t)
	adapter, err := NewValkeyAdapter(AdapterConfig{
		Addr: mr.Addr(),
		Key:  key,
	})
	require.NoError(t, err, "create adapter")
	return adapter
}

func TestDebouncer_Miniredis_EndToEnd(t *testing.T) {
	adapter := setupMiniredis(t, "debounce:e2e")

	var callCount atomic.Int32
	var receivedKey atomic.Value

	cb := func(_ context.Context, key string) error {
		callCount.Add(1)
		receivedKey.Store(key)
		return nil
	}

	d := New(adapter, cb, Config{
		Timeout:           500 * time.Millisecond,
		PollInterval:      100 * time.Millisecond,
		ProcessingTimeout: 5 * time.Second,
		MaxRetries:        0,
		InitialBackoff:    1 * time.Millisecond,
	})

	ctx := context.Background()
	go func() { _ = d.Start(ctx) }()
	t.Cleanup(func() { d.Close() })

	err := d.Trigger(ctx, "room-1")
	require.NoError(t, err)

	// Wait for timeout (500ms) + a few poll intervals to fire.
	time.Sleep(900 * time.Millisecond)

	assert.Equal(t, int32(1), callCount.Load(), "callback should fire exactly once")
	assert.Equal(t, "room-1", receivedKey.Load(), "callback should receive correct key")

	// Wait a bit more to confirm no duplicate fires.
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, int32(1), callCount.Load(), "callback should still be exactly once after extra wait")
}

func TestDebouncer_Miniredis_DebounceReset(t *testing.T) {
	adapter := setupMiniredis(t, "debounce:reset")

	var callCount atomic.Int32

	cb := func(_ context.Context, key string) error {
		callCount.Add(1)
		return nil
	}

	d := New(adapter, cb, Config{
		Timeout:           500 * time.Millisecond,
		PollInterval:      100 * time.Millisecond,
		ProcessingTimeout: 5 * time.Second,
		MaxRetries:        0,
		InitialBackoff:    1 * time.Millisecond,
	})

	ctx := context.Background()
	go func() { _ = d.Start(ctx) }()
	t.Cleanup(func() { d.Close() })

	// First trigger.
	err := d.Trigger(ctx, "room-1")
	require.NoError(t, err)

	// Wait 300ms (< 500ms timeout), then re-trigger to reset the deadline.
	time.Sleep(300 * time.Millisecond)
	err = d.Trigger(ctx, "room-1")
	require.NoError(t, err)

	// 300ms after re-trigger: 600ms total, but only 300ms since last trigger.
	// The deadline was reset, so the callback should NOT have fired yet.
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int32(0), callCount.Load(), "callback should NOT have fired before reset timeout expires")

	// Wait another 400ms (700ms since re-trigger, well past the 500ms timeout).
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, int32(1), callCount.Load(), "callback should fire exactly once after reset timeout expires")
}

func TestDebouncer_Miniredis_MultipleKeys(t *testing.T) {
	adapter := setupMiniredis(t, "debounce:multi")

	var mu sync.Mutex
	fired := make(map[string]int)

	cb := func(_ context.Context, key string) error {
		mu.Lock()
		defer mu.Unlock()
		fired[key]++
		return nil
	}

	d := New(adapter, cb, Config{
		Timeout:           300 * time.Millisecond,
		PollInterval:      50 * time.Millisecond,
		ProcessingTimeout: 5 * time.Second,
		MaxRetries:        0,
		InitialBackoff:    1 * time.Millisecond,
	})

	ctx := context.Background()
	go func() { _ = d.Start(ctx) }()
	t.Cleanup(func() { d.Close() })

	for _, key := range []string{"room-1", "room-2", "room-3"} {
		err := d.Trigger(ctx, key)
		require.NoError(t, err)
	}

	// Wait for timeout + polls.
	time.Sleep(700 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, fired["room-1"], "room-1 callback should fire exactly once")
	assert.Equal(t, 1, fired["room-2"], "room-2 callback should fire exactly once")
	assert.Equal(t, 1, fired["room-3"], "room-3 callback should fire exactly once")
	assert.Len(t, fired, 3, "exactly 3 keys should have fired")
}

func TestDebouncer_Miniredis_MultiClaimSafety(t *testing.T) {
	adapter := setupMiniredis(t, "debounce:multiclaim")

	var totalCalls atomic.Int32

	cb := func(_ context.Context, key string) error {
		totalCalls.Add(1)
		return nil
	}

	cfg := Config{
		Timeout:           300 * time.Millisecond,
		PollInterval:      50 * time.Millisecond,
		ProcessingTimeout: 5 * time.Second,
		MaxRetries:        0,
		InitialBackoff:    1 * time.Millisecond,
	}

	// Two debouncers sharing the same adapter (simulating two pods).
	d1 := New(adapter, cb, cfg)
	d2 := New(adapter, cb, cfg)

	ctx := context.Background()
	go func() { _ = d1.Start(ctx) }()
	go func() { _ = d2.Start(ctx) }()
	t.Cleanup(func() {
		d1.Close()
		d2.Close()
	})

	// Trigger via d1.
	err := d1.Trigger(ctx, "room-1")
	require.NoError(t, err)

	// Wait for timeout + polls.
	time.Sleep(700 * time.Millisecond)

	assert.Equal(t, int32(1), totalCalls.Load(), "callback should fire exactly once total across both debouncers")
}

func TestDebouncer_Miniredis_ProcessingTimeout(t *testing.T) {
	adapter := setupMiniredis(t, "debounce:proctimeout")

	var callCount atomic.Int32

	cb := func(_ context.Context, key string) error {
		callCount.Add(1)
		return nil
	}

	cfg := Config{
		Timeout:           200 * time.Millisecond,
		PollInterval:      50 * time.Millisecond,
		ProcessingTimeout: 1 * time.Second,
		MaxRetries:        0,
		InitialBackoff:    1 * time.Millisecond,
	}

	ctx := context.Background()

	// Trigger the key.
	err := adapter.Trigger(ctx, "room-1", time.Now().Add(cfg.Timeout))
	require.NoError(t, err)

	// Wait for the debounce timeout to expire so the entry becomes claimable.
	time.Sleep(300 * time.Millisecond)

	// Manually claim the entry to simulate a crashed worker that claimed but
	// never processed. The score is bumped to now + processingTimeout.
	entries, err := adapter.Claim(ctx, time.Now(), cfg.ProcessingTimeout, 100)
	require.NoError(t, err)
	require.Len(t, entries, 1, "should claim exactly one entry")
	// Do NOT call Remove — simulating a crash after claiming.

	// Now start the debouncer. The entry's score is set to
	// now + processingTimeout (1s), so after ~1s it becomes re-claimable.
	d := New(adapter, cb, cfg)
	go func() { _ = d.Start(ctx) }()
	t.Cleanup(func() { d.Close() })

	// Wait for the processing timeout to expire plus poll intervals.
	time.Sleep(1200 * time.Millisecond)

	assert.Equal(t, int32(1), callCount.Load(), "callback should fire once after processing timeout expires (re-claimed)")
}
