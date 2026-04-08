package debouncetimer

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimer_FiresAfterDuration(t *testing.T) {
	fired := make(chan struct{})
	dt := New(50*time.Millisecond, func() {
		close(fired)
	})
	defer dt.Stop()

	dt.Reset()

	select {
	case <-fired:
		// success
	case <-time.After(time.Second):
		t.Fatal("callback did not fire within timeout")
	}
}

func TestTimer_ResetPostponesCallback(t *testing.T) {
	var count atomic.Int32
	fired := make(chan struct{}, 1)

	dt := New(80*time.Millisecond, func() {
		count.Add(1)
		fired <- struct{}{}
	})
	defer dt.Stop()

	dt.Reset()

	// Reset again at 40ms — well before the 80ms deadline.
	time.Sleep(40 * time.Millisecond)
	dt.Reset()

	// At 80ms from start (40ms after second Reset), callback should NOT have
	// fired yet because the second Reset pushed the deadline to 120ms.
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, int32(0), count.Load(), "callback fired too early after reset")

	// Wait for the callback to fire after the postponed deadline.
	select {
	case <-fired:
		assert.Equal(t, int32(1), count.Load())
	case <-time.After(time.Second):
		t.Fatal("callback did not fire after reset")
	}
}

func TestTimer_MultipleResetsFiresOnce(t *testing.T) {
	var count atomic.Int32
	done := make(chan struct{})

	dt := New(50*time.Millisecond, func() {
		count.Add(1)
		close(done)
	})
	defer dt.Stop()

	// Rapid-fire resets.
	for i := 0; i < 10; i++ {
		dt.Reset()
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-done:
		// success
	case <-time.After(time.Second):
		t.Fatal("callback did not fire")
	}

	// Give extra time to ensure no duplicate fires.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), count.Load(), "callback should fire exactly once")
}

func TestTimer_StopPreventsCallback(t *testing.T) {
	var count atomic.Int32

	dt := New(50*time.Millisecond, func() {
		count.Add(1)
	})

	dt.Reset()
	time.Sleep(20 * time.Millisecond)
	dt.Stop()

	// Wait past the original deadline.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), count.Load(), "callback should not fire after Stop")
}

func TestTimer_StopIsIdempotent(t *testing.T) {
	dt := New(50*time.Millisecond, func() {})

	dt.Reset()
	dt.Stop()
	dt.Stop() // must not panic
	dt.Stop()
}

func TestTimer_ResetAfterStopIsNoop(t *testing.T) {
	var count atomic.Int32

	dt := New(50*time.Millisecond, func() {
		count.Add(1)
	})

	dt.Reset()
	dt.Stop()
	dt.Reset() // should be ignored

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), count.Load(), "callback should not fire after Stop+Reset")
}

func TestTimer_StopAfterCallbackFired(t *testing.T) {
	fired := make(chan struct{})
	dt := New(20*time.Millisecond, func() {
		close(fired)
	})

	dt.Reset()

	select {
	case <-fired:
		// success
	case <-time.After(time.Second):
		t.Fatal("callback did not fire")
	}

	dt.Stop() // must not panic after callback has already fired
}

func TestTimer_ConcurrentResets(t *testing.T) {
	var count atomic.Int32
	done := make(chan struct{})

	dt := New(50*time.Millisecond, func() {
		count.Add(1)
		close(done)
	})
	defer dt.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dt.Reset()
		}()
	}
	wg.Wait()

	select {
	case <-done:
		assert.Equal(t, int32(1), count.Load())
	case <-time.After(time.Second):
		t.Fatal("callback did not fire")
	}
}

func TestTimer_ConcurrentResetAndStop(t *testing.T) {
	dt := New(50*time.Millisecond, func() {})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			dt.Reset()
		}()
		go func() {
			defer wg.Done()
			dt.Stop()
		}()
	}
	wg.Wait()
	// Must not panic or race.
}

func TestTimer_NewRequiresPositiveDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
	}{
		{name: "zero duration", duration: 0},
		{name: "negative duration", duration: -time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Panics(t, func() {
				New(tt.duration, func() {})
			})
		})
	}
}

func TestTimer_NewRequiresCallback(t *testing.T) {
	require.Panics(t, func() {
		New(time.Second, nil)
	})
}
