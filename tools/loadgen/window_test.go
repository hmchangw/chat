package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLatencyWindow_P99_OverInterval(t *testing.T) {
	w := NewLatencyWindow(60 * time.Second)
	now := time.Unix(100, 0)

	// 100 samples spaced 100ms apart; latencies 1ms..100ms.
	for i := 1; i <= 100; i++ {
		w.AddAt(now.Add(time.Duration(i)*100*time.Millisecond), time.Duration(i)*time.Millisecond, false)
	}

	// Over the full 10s window, p99 should be ~99ms.
	end := now.Add(110 * 100 * time.Millisecond)
	p99 := w.P99(end, 15*time.Second)
	assert.GreaterOrEqual(t, p99, 95*time.Millisecond, "p99 should be near max; got %s", p99)
}

func TestLatencyWindow_DropsOlderThanWindow(t *testing.T) {
	w := NewLatencyWindow(1 * time.Second)
	t0 := time.Unix(100, 0)
	w.AddAt(t0, 100*time.Millisecond, false)                  // very old
	w.AddAt(t0.Add(5*time.Second), 1*time.Millisecond, false) // recent

	// Query the last 1s at t = t0 + 5s. The 100ms sample is too old.
	p99 := w.P99(t0.Add(5*time.Second), 1*time.Second)
	assert.Equal(t, 1*time.Millisecond, p99,
		"window must drop samples older than the retention window")
}

func TestLatencyWindow_ErrorRate(t *testing.T) {
	w := NewLatencyWindow(200 * time.Second)
	t0 := time.Unix(100, 0)
	for i := 0; i < 100; i++ {
		errored := i%10 == 0 // 10 errors out of 100
		w.AddAt(t0.Add(time.Duration(i)*time.Second), 1*time.Millisecond, errored)
	}
	// Over the last 200s (everything), error rate should be 0.10.
	rate := w.ErrorRate(t0.Add(100*time.Second), 200*time.Second)
	assert.InDelta(t, 0.10, rate, 0.001)
}

func TestLatencyWindow_EmptyReturnsZero(t *testing.T) {
	w := NewLatencyWindow(10 * time.Second)
	assert.Zero(t, w.P99(time.Now(), 1*time.Second))
	assert.Zero(t, w.ErrorRate(time.Now(), 1*time.Second))
}

func TestAbortWatcher_TriggersOnSustainedP99(t *testing.T) {
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	// 30 seconds of samples all at 5s latency — well above any reasonable p99.
	for i := 0; i < 60; i++ {
		w.AddAt(t0.Add(time.Duration(i)*time.Second), 5*time.Second, false)
	}

	cfg := &abortConfig{
		Window:       w,
		P99Limit:     1 * time.Second,
		P99Sustain:   10 * time.Second,
		ErrorPct:     0,
		ErrorSustain: 10 * time.Second,
	}
	now := t0.Add(60 * time.Second)
	tripped, reason := abortShouldFire(cfg, now)
	assert.True(t, tripped)
	assert.Contains(t, reason, "p99")
}

func TestAbortWatcher_TriggersOnSustainedErrorRate(t *testing.T) {
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	// 60 samples, half errored — 50% error rate.
	for i := 0; i < 60; i++ {
		w.AddAt(t0.Add(time.Duration(i)*time.Second), 1*time.Millisecond, i%2 == 0)
	}
	cfg := &abortConfig{
		Window:       w,
		P99Limit:     0,
		ErrorPct:     0.10, // 10% threshold
		ErrorSustain: 30 * time.Second,
	}
	tripped, reason := abortShouldFire(cfg, t0.Add(60*time.Second))
	assert.True(t, tripped)
	assert.Contains(t, reason, "error_rate")
}

func TestAbortWatcher_DoesNotTriggerOnShortSpike(t *testing.T) {
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	// 5 seconds of high latency (not enough to sustain a 30s threshold).
	for i := 0; i < 30; i++ {
		var lat time.Duration
		if i >= 25 {
			lat = 5 * time.Second
		} else {
			lat = 1 * time.Millisecond
		}
		w.AddAt(t0.Add(time.Duration(i)*time.Second), lat, false)
	}
	cfg := &abortConfig{
		Window: w, P99Limit: 1 * time.Second, P99Sustain: 30 * time.Second,
	}
	tripped, _ := abortShouldFire(cfg, t0.Add(30*time.Second))
	require.False(t, tripped, "5s of breach should not trip a 30s sustain")
}

func TestAbortWatcher_DisabledWhenLimitsZero(t *testing.T) {
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	for i := 0; i < 30; i++ {
		w.AddAt(t0.Add(time.Duration(i)*time.Second), 5*time.Second, true)
	}
	cfg := &abortConfig{Window: w, P99Limit: 0, ErrorPct: 0}
	tripped, _ := abortShouldFire(cfg, t0.Add(30*time.Second))
	assert.False(t, tripped, "watcher must be silent when both limits are 0")
}
