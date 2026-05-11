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

// TestAbortWatcher_TripsOnLongTailEvenWhenMedianOK is the regression
// test for B1: the abort watcher must compare p99 against P99Limit, not
// p50. A workload where the median is fine but the long tail is well
// over the threshold should trip — that's the whole point of a p99
// knob (long-tail saturation detection).
func TestAbortWatcher_TripsOnLongTailEvenWhenMedianOK(t *testing.T) {
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	// 100 samples across 10s. 95% are 1ms (median = 1ms, well below threshold).
	// 5% are 5s (p99 lands well above threshold).
	for i := 0; i < 100; i++ {
		var lat time.Duration
		if i%20 == 0 { // every 20th sample → 5%
			lat = 5 * time.Second
		} else {
			lat = 1 * time.Millisecond
		}
		w.AddAt(t0.Add(time.Duration(i)*100*time.Millisecond), lat, false)
	}
	cfg := &abortConfig{
		Window:     w,
		P99Limit:   1 * time.Second,
		P99Sustain: 10 * time.Second,
	}
	now := t0.Add(11 * time.Second)
	tripped, reason := abortShouldFire(cfg, now)
	assert.True(t, tripped, "5%% tail at 5s should trip a 1s p99 limit even though median is 1ms")
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

func TestAbortWatcher_DoesNotTriggerOnRecoveredSpike(t *testing.T) {
	// A spike that happened OUTSIDE the current sustain window should not
	// trip; the window has moved past it. Sustain=10s, "now" is 30s after
	// the spike, so all spike samples are pruned/excluded from the p99.
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	// First 5s: spike at 5s latency.
	for i := 0; i < 5; i++ {
		w.AddAt(t0.Add(time.Duration(i)*time.Second), 5*time.Second, false)
	}
	// Next 30s: healthy at 1ms.
	for i := 5; i < 35; i++ {
		w.AddAt(t0.Add(time.Duration(i)*time.Second), 1*time.Millisecond, false)
	}
	cfg := &abortConfig{
		Window:     w,
		P99Limit:   1 * time.Second,
		P99Sustain: 10 * time.Second, // last 10s only — spike is 25-30s old
	}
	tripped, _ := abortShouldFire(cfg, t0.Add(35*time.Second))
	require.False(t, tripped, "spike older than sustain window must not trip")
}

func TestAbortWatcher_RequiresFullSustainCoverage(t *testing.T) {
	// On a fresh run, a single high-latency sample shouldn't trip even
	// though it's 100% over the threshold for the moment — the window
	// hasn't accumulated enough history to assert "sustained".
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	w.AddAt(t0, 5*time.Second, false)
	cfg := &abortConfig{
		Window:     w,
		P99Limit:   1 * time.Second,
		P99Sustain: 30 * time.Second,
	}
	tripped, _ := abortShouldFire(cfg, t0.Add(1*time.Second))
	require.False(t, tripped, "single sample 1s in cannot satisfy a 30s sustain")
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

// TestPickWeighted_ZeroTotalReturnsZeroValue is the F2 regression: a
// weights map where every weight is zero used to panic via r.Intn(0).
func TestPickWeighted_ZeroTotalReturnsZeroValue(t *testing.T) {
	weights := map[historyRequestKind]int{
		HistoryLoadHistory:    0,
		HistoryGetMessageByID: 0,
	}
	got := pickWeighted(weights)
	assert.Equal(t, historyRequestKind(0), got, "all-zero weights must return zero value, not panic")
}

// TestRunRun_ExitCodeOnAbort exercises the abort exit-code mapping in
// isolation. The full runRun is bound to a NATS connection so we can't
// invoke it as a unit test; instead we verify the exit-code policy that
// runRun applies when abortTripped is set. The plan's Task 20 Done gate
// was "exit with code 2"; this test locks it down at the shape level.
func TestRunRun_ExitCodeOnAbort(t *testing.T) {
	// Direct exit-code derivation: abort > clean fail > clean pass.
	cases := []struct {
		name     string
		tripped  bool
		sent     int
		errs     int
		wantCode int
	}{
		{"clean pass", false, 1000, 0, 0},
		{"clean tolerable errors", false, 1000, 1, 0},
		{"errors over tolerance", false, 1000, 100, 1},
		{"abort wins over clean", true, 1000, 0, 2},
		{"abort wins over errors", true, 1000, 100, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := exitCodeFor(tc.tripped, tc.sent, tc.errs)
			assert.Equal(t, tc.wantCode, got)
		})
	}
}

// TestLatencyWindow_Add_UsesWallClock confirms the production `Add`
// entry point (wall-clock variant of AddAt) is wired correctly.
func TestLatencyWindow_Add_UsesWallClock(t *testing.T) {
	w := NewLatencyWindow(1 * time.Second)
	w.Add(5*time.Millisecond, false)
	w.Add(7*time.Millisecond, true)
	// Both samples are near time.Now(); P99 over 1s should pick the higher.
	p99 := w.P99(time.Now(), 1*time.Second)
	assert.GreaterOrEqual(t, p99, 5*time.Millisecond)
	rate := w.ErrorRate(time.Now(), 1*time.Second)
	assert.InDelta(t, 0.5, rate, 0.001, "one error of two = 50%%")
}

func TestLatencyWindow_P50_ExactValue(t *testing.T) {
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	// 100 samples 1ms..100ms.
	for i := 1; i <= 100; i++ {
		w.AddAt(t0.Add(time.Duration(i)*100*time.Millisecond), time.Duration(i)*time.Millisecond, false)
	}
	end := t0.Add(101 * 100 * time.Millisecond)
	p50 := w.P50(end, 60*time.Second)
	// 100 samples, idx = floor(99 * 0.50) = 49 → samples[49] = 50ms.
	assert.Equal(t, 50*time.Millisecond, p50)
}

func TestLatencyWindow_PercentileAt_P95(t *testing.T) {
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	for i := 1; i <= 100; i++ {
		w.AddAt(t0.Add(time.Duration(i)*100*time.Millisecond), time.Duration(i)*time.Millisecond, false)
	}
	end := t0.Add(101 * 100 * time.Millisecond)
	p95 := w.percentileAt(end, 60*time.Second, 0.95)
	// idx = floor(99 * 0.95) = 94 → samples[94] = 95ms.
	assert.Equal(t, 95*time.Millisecond, p95)
}

func TestLatencyWindow_P99_ExactValue(t *testing.T) {
	// Pin the off-by-one: with 100 samples 1..100ms, p99 idx = floor(99*0.99) = 98 → 99ms.
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	for i := 1; i <= 100; i++ {
		w.AddAt(t0.Add(time.Duration(i)*100*time.Millisecond), time.Duration(i)*time.Millisecond, false)
	}
	end := t0.Add(101 * 100 * time.Millisecond)
	p99 := w.P99(end, 60*time.Second)
	assert.Equal(t, 99*time.Millisecond, p99, "p99 of 100 samples must be the 99th value, not the 100th")
}

// S3: Percentiles is the batched form — sort once, return many
// quantiles. The progress reporter calls P50 / P95 / P99 once per
// tick today (three separate sorts of the same slice); switching it
// to Percentiles drops the per-tick cost to a single sort.
func TestLatencyWindow_Percentiles_MatchesSeparateCalls(t *testing.T) {
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	for i := 1; i <= 100; i++ {
		w.AddAt(t0.Add(time.Duration(i)*100*time.Millisecond), time.Duration(i)*time.Millisecond, false)
	}
	end := t0.Add(101 * 100 * time.Millisecond)
	got := w.Percentiles(end, 60*time.Second, 0.50, 0.95, 0.99)
	require.Len(t, got, 3)
	// Same indexing rule as percentileLocked: idx = floor((N-1)*q).
	assert.Equal(t, 50*time.Millisecond, got[0], "p50")
	assert.Equal(t, 95*time.Millisecond, got[1], "p95")
	assert.Equal(t, 99*time.Millisecond, got[2], "p99")
}

func TestLatencyWindow_Percentiles_EmptyWindowReturnsZeros(t *testing.T) {
	w := NewLatencyWindow(10 * time.Second)
	got := w.Percentiles(time.Now(), 1*time.Second, 0.50, 0.99)
	require.Len(t, got, 2)
	assert.Zero(t, got[0])
	assert.Zero(t, got[1])
}

func TestLatencyWindow_Percentiles_NoQuantilesReturnsEmpty(t *testing.T) {
	// Defensive: if a caller passes no quantiles, return an empty slice
	// rather than panic on an unbounded slice index.
	w := NewLatencyWindow(10 * time.Second)
	w.AddAt(time.Unix(100, 0), 5*time.Millisecond, false)
	got := w.Percentiles(time.Unix(101, 0), 10*time.Second)
	assert.Empty(t, got)
}

// S3: at very high publish rates the unbounded sample slice would
// retain 300k+ entries (5k rps × 60s). Cap it so the periodic
// percentile sort doesn't drift into the milliseconds. Cap policy:
// drop-oldest when over cap, preserving sliding-window semantics
// (i.e. the cap doesn't change WHICH samples we keep, just bounds
// HOW MANY). Adds remain monotonic-time so the binary-search prune
// in AddAt continues to work.
func TestLatencyWindow_MaxSamples_DropsOldestOverCap(t *testing.T) {
	w := NewLatencyWindow(60 * time.Second)
	w.SetMaxSamples(5)
	t0 := time.Unix(100, 0)
	// 10 samples 1..10ms — only the last 5 (6..10ms) should survive.
	for i := 1; i <= 10; i++ {
		w.AddAt(t0.Add(time.Duration(i)*time.Millisecond), time.Duration(i)*time.Millisecond, false)
	}
	end := t0.Add(11 * time.Millisecond)
	got := w.Percentiles(end, 60*time.Second, 0.50, 0.99)
	require.Len(t, got, 2)
	// With samples [6,7,8,9,10]ms surviving, p50 idx=floor(4*0.5)=2 → 8ms; p99 idx=floor(4*0.99)=3 → 9ms.
	assert.Equal(t, 8*time.Millisecond, got[0], "p50 of the 5 surviving samples")
	assert.Equal(t, 9*time.Millisecond, got[1], "p99 of the 5 surviving samples")
}

func TestLatencyWindow_MaxSamples_ZeroMeansUnbounded(t *testing.T) {
	// Default (0) preserves legacy behavior — no cap, all samples kept.
	w := NewLatencyWindow(60 * time.Second)
	t0 := time.Unix(100, 0)
	for i := 1; i <= 50; i++ {
		w.AddAt(t0.Add(time.Duration(i)*time.Millisecond), time.Duration(i)*time.Millisecond, false)
	}
	// p99 over the full window — idx=floor(49*0.99)=48 → samples[48]=49ms.
	end := t0.Add(60 * time.Millisecond)
	assert.Equal(t, 49*time.Millisecond, w.P99(end, 60*time.Second))
}
