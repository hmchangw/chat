package main

import (
	"log/slog"
	"slices"
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
//
// S3: maxSamples bounds the slice length so the periodic percentile
// sort cost stays bounded at high publish rates (e.g. 5k rps × 60s
// retention would otherwise hold 300k samples). When 0, no cap is
// applied (legacy behavior). The cap policy is drop-oldest, which
// preserves sliding-window semantics — we lose the early end of the
// retention window once the rate × retention exceeds the cap, but
// the abort watcher only consults the tail (the sustain interval),
// which is much shorter than the retention.
type LatencyWindow struct {
	mu         sync.Mutex
	retain     time.Duration
	samples    []windowSample
	maxSamples int
}

// NewLatencyWindow returns a buffer that retains samples for the
// specified retention window. Samples older than `retain` are pruned
// lazily on each Add.
func NewLatencyWindow(retain time.Duration) *LatencyWindow {
	return &LatencyWindow{retain: retain}
}

// WithMaxSamples caps the slice length at n (drop-oldest when full)
// and returns the receiver for fluent construction:
//
//	w := NewLatencyWindow(retain).WithMaxSamples(10_000)
//
// 0 disables the cap (legacy unbounded behavior). Concurrent updates
// are safe (locked) but the new cap only takes effect on the next
// Add — there's no retroactive truncation. Designed for one-shot
// construction-time setup; runtime mutation is supported but not the
// expected use case.
func (w *LatencyWindow) WithMaxSamples(n int) *LatencyWindow {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.maxSamples = n
	return w
}

// Saturated reports whether the slice is currently at its cap (i.e.
// the next Add will drop the oldest sample). Used by the abort
// watcher to emit a structured diagnostic when the cap appears to
// be masking a sustained breach: if Saturated() is true AND
// P99WithCoverage returns covered=false, the watcher can't fire
// because retention has been compressed below the sustain interval.
// Returns false when no cap is set (uncapped windows are never
// "saturated"; they grow without bound).
func (w *LatencyWindow) Saturated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maxSamples > 0 && len(w.samples) >= w.maxSamples
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
	// S3: cap the slice (drop-oldest) so the periodic percentile sort
	// stays bounded under sustained high publish rates.
	if w.maxSamples > 0 && len(w.samples) > w.maxSamples {
		w.samples = w.samples[len(w.samples)-w.maxSamples:]
	}
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

// percentileAt returns an arbitrary quantile over the window, used by
// the progress reporter to surface p95 alongside p50/p99. The Phase 3
// §3.2 spec lists these three percentile fields on every progress line.
func (w *LatencyWindow) percentileAt(now time.Time, over time.Duration, q float64) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.percentileLocked(now, over, q)
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
	latencies := w.windowedLatenciesLocked(now, over)
	sortInPlace(latencies)
	return percentileFromSorted(latencies, q)
}

// Percentiles returns multiple quantiles over the same window in a
// single locked operation, sorting the in-window latencies exactly
// once. Progress reporting and other multi-percentile readouts MUST
// prefer this over N separate P50/P95/P99 calls — each separate call
// would re-take the lock, re-copy the slice, and re-sort it.
// Returns a slice the same length as `qs`. When `qs` is empty the
// return is nil (callers should treat empty/nil equivalently).
func (w *LatencyWindow) Percentiles(now time.Time, over time.Duration, qs ...float64) []time.Duration {
	if len(qs) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	latencies := w.windowedLatenciesLocked(now, over)
	sortInPlace(latencies)
	out := make([]time.Duration, len(qs))
	for i, q := range qs {
		out[i] = percentileFromSorted(latencies, q)
	}
	return out
}

// windowedLatenciesLocked returns the in-window latencies (unsorted).
// Caller must hold w.mu.
func (w *LatencyWindow) windowedLatenciesLocked(now time.Time, over time.Duration) []time.Duration {
	cutoff := now.Add(-over)
	latencies := make([]time.Duration, 0, len(w.samples))
	for i := range w.samples {
		if w.samples[i].at.Before(cutoff) || w.samples[i].at.After(now) {
			continue
		}
		latencies = append(latencies, w.samples[i].latency)
	}
	return latencies
}

// sortInPlace ascending-sorts the slice (no copy). The caller owns
// the slice and the post-call ordering. Uses slices.Sort (no
// reflection) which is ~25-30% faster than the older sort.Slice on
// typed slices like []time.Duration.
func sortInPlace(latencies []time.Duration) {
	slices.Sort(latencies)
}

func percentileFromSorted(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * q)
	return sorted[idx]
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
//
// When the LatencyWindow is at its sample cap AND a breach is observed
// without sustain coverage, this function emits a structured slog.Warn
// ("abort watcher deafened by sample cap") so operators don't get a
// silent no-fire surprise — the cap has compressed retention below
// the sustain interval, so the watcher CAN'T see whether the breach
// is sustained. Fix: raise --abort-window-max-samples for the target
// publish rate, or disable the cap with 0.
func abortShouldFire(cfg *abortConfig, now time.Time) (bool, string) {
	tripped, reason, _ := abortShouldFireDiag(cfg, now)
	return tripped, reason
}

// abortShouldFireDiag is the test-visible variant that also returns
// whether the cap-masking-sustain diagnostic condition was detected.
// Production callers use abortShouldFire which discards the flag (it
// emits a slog.Warn instead). Returning the bool from the diag form
// keeps tests deterministic without rummaging through captured log
// output.
func abortShouldFireDiag(cfg *abortConfig, now time.Time) (bool, string, bool) {
	capMasked := false
	if cfg.P99Limit > 0 {
		// Atomic percentile + coverage check (W3 fix): without the
		// combined call, an Add between two separate locks could prune
		// the head and break the "covers full sustain interval"
		// invariant inconsistently across the two reads.
		p99, covered := cfg.Window.P99WithCoverage(now, cfg.P99Sustain)
		if p99 > cfg.P99Limit && covered {
			return true, "p99 over " + cfg.P99Limit.String() + " sustained for " + cfg.P99Sustain.String(), false
		}
		if p99 > cfg.P99Limit && !covered && cfg.Window.Saturated() {
			capMasked = true
			slog.Warn("abort watcher deafened by sample cap",
				"check", "p99",
				"p99", p99.String(),
				"limit", cfg.P99Limit.String(),
				"sustain", cfg.P99Sustain.String(),
				"hint", "raise --abort-window-max-samples or set to 0 to disable the cap",
			)
		}
	}
	if cfg.ErrorPct > 0 {
		rate, covered := cfg.Window.ErrorRateWithCoverage(now, cfg.ErrorSustain)
		if rate > cfg.ErrorPct && covered {
			return true, "error_rate over threshold sustained for " + cfg.ErrorSustain.String(), capMasked
		}
		if rate > cfg.ErrorPct && !covered && cfg.Window.Saturated() {
			capMasked = true
			slog.Warn("abort watcher deafened by sample cap",
				"check", "error_rate",
				"rate", rate,
				"limit", cfg.ErrorPct,
				"sustain", cfg.ErrorSustain.String(),
				"hint", "raise --abort-window-max-samples or set to 0 to disable the cap",
			)
		}
	}
	return false, "", capMasked
}
