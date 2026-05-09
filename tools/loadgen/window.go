package main

import (
	"sort"
	"sync"
	"time"
)

// windowSample is one observation in the LatencyWindow ring buffer.
type windowSample struct {
	at      time.Time
	latency time.Duration
	errored bool
}

// LatencyWindow is a goroutine-safe ring buffer of recent
// (latency, errored, at) observations. Phase 3 §3.5 calls for this
// because Prometheus Histograms expose only cumulative bucket counts —
// we cannot extract a sliding-window p99 from a Gather() snapshot
// alone. The watcher reads from this in-process buffer; Prometheus
// scrape continues to read from the parallel histogram.
type LatencyWindow struct {
	mu      sync.Mutex
	retain  time.Duration
	samples []windowSample
}

// NewLatencyWindow returns a buffer that retains samples for the
// specified retention window. Samples older than `retain` are pruned
// lazily on each Add.
func NewLatencyWindow(retain time.Duration) *LatencyWindow {
	return &LatencyWindow{retain: retain}
}

// Add records one observation at time.Now(). Use AddAt in tests for
// deterministic timing.
func (w *LatencyWindow) Add(latency time.Duration, errored bool) {
	w.AddAt(time.Now(), latency, errored)
}

// AddAt records one observation at the given clock time. Prunes
// samples older than (at - retain) before appending.
func (w *LatencyWindow) AddAt(at time.Time, latency time.Duration, errored bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := at.Add(-w.retain)
	// Drop everything older than cutoff. Samples are appended in
	// monotonic time, so the first index >= cutoff is a binary search.
	idx := sort.Search(len(w.samples), func(i int) bool {
		return !w.samples[i].at.Before(cutoff)
	})
	if idx > 0 {
		w.samples = w.samples[idx:]
	}
	w.samples = append(w.samples, windowSample{at: at, latency: latency, errored: errored})
}

// P99 returns the 99th-percentile latency over the last `over` window
// ending at `now`. Returns 0 when the window is empty.
func (w *LatencyWindow) P99(now time.Time, over time.Duration) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.percentileLocked(now, over, 0.99)
}

// P50 returns the median latency over the window. Used by the abort
// watcher's sustain check: if more than half the samples breach the
// limit, the median is over it — the breach is dominant rather than
// a transient spike.
func (w *LatencyWindow) P50(now time.Time, over time.Duration) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.percentileLocked(now, over, 0.50)
}

// P99WithCoverage returns the p99 AND whether the window covers the
// full sustain interval, in a single locked operation. Avoids the
// TOCTOU race where a separate P99() and a separate "is the oldest
// sample old enough" check could interleave with an Add that prunes
// the head between them.
func (w *LatencyWindow) P99WithCoverage(now time.Time, over time.Duration) (time.Duration, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.samples) == 0 {
		return 0, false
	}
	covered := !w.samples[0].at.After(now.Add(-over))
	return w.percentileLocked(now, over, 0.99), covered
}

// ErrorRateWithCoverage is the error-rate companion to P99WithCoverage:
// returns the error fraction over the last `over` window AND whether
// the window has accumulated full coverage, atomically.
func (w *LatencyWindow) ErrorRateWithCoverage(now time.Time, over time.Duration) (float64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.samples) == 0 {
		return 0, false
	}
	covered := !w.samples[0].at.After(now.Add(-over))
	cutoff := now.Add(-over)
	total, errs := 0, 0
	for i := range w.samples {
		if w.samples[i].at.Before(cutoff) || w.samples[i].at.After(now) {
			continue
		}
		total++
		if w.samples[i].errored {
			errs++
		}
	}
	if total == 0 {
		return 0, covered
	}
	return float64(errs) / float64(total), covered
}

// percentileLocked must be called with w.mu held.
func (w *LatencyWindow) percentileLocked(now time.Time, over time.Duration, q float64) time.Duration {
	cutoff := now.Add(-over)
	latencies := make([]time.Duration, 0, len(w.samples))
	for i := range w.samples {
		if w.samples[i].at.Before(cutoff) || w.samples[i].at.After(now) {
			continue
		}
		latencies = append(latencies, w.samples[i].latency)
	}
	if len(latencies) == 0 {
		return 0
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	idx := int(float64(len(latencies)-1) * q)
	return latencies[idx]
}

// ErrorRate returns the fraction of errored samples over the last
// `over` window ending at `now`. Returns 0 when the window is empty.
func (w *LatencyWindow) ErrorRate(now time.Time, over time.Duration) float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := now.Add(-over)
	total, errs := 0, 0
	for i := range w.samples {
		if w.samples[i].at.Before(cutoff) || w.samples[i].at.After(now) {
			continue
		}
		total++
		if w.samples[i].errored {
			errs++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(errs) / float64(total)
}

// abortConfig is the saturation auto-detect configuration. P99Limit==0
// disables the latency check; ErrorPct==0 disables the error-rate check.
type abortConfig struct {
	Window       *LatencyWindow
	P99Limit     time.Duration // p99 threshold; 0 disables
	P99Sustain   time.Duration // sustained-breach window required to trip
	ErrorPct     float64       // [0..1]; 0 disables
	ErrorSustain time.Duration
}

// abortShouldFire reports whether the abort watcher should trigger at
// time `now` and, if so, returns the reason string for the report.
// The "sustained" requirement is enforced by querying the underlying
// window with the configured sustain duration: if every sample in
// that sub-window is in breach, the threshold has been continuously
// over.
func abortShouldFire(cfg *abortConfig, now time.Time) (bool, string) {
	if cfg.P99Limit > 0 {
		// Atomic percentile + coverage check (W3 fix): without the
		// combined call, an Add between two separate locks could prune
		// the head and break the "covers full sustain interval"
		// invariant inconsistently across the two reads.
		p99, covered := cfg.Window.P99WithCoverage(now, cfg.P99Sustain)
		if p99 > cfg.P99Limit && covered {
			return true, "p99 over " + cfg.P99Limit.String() + " sustained for " + cfg.P99Sustain.String()
		}
	}
	if cfg.ErrorPct > 0 {
		rate, covered := cfg.Window.ErrorRateWithCoverage(now, cfg.ErrorSustain)
		if rate > cfg.ErrorPct && covered {
			return true, "error_rate over threshold sustained for " + cfg.ErrorSustain.String()
		}
	}
	return false, ""
}
