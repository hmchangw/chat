package main

import (
	"log/slog"
	"sync"
	"time"
)

// LatencyWindow is a goroutine-safe rolling time window of recent
// (latency, errored, at) observations. Phase 3 §3.5 calls for this
// because Prometheus Histograms expose only cumulative bucket counts —
// we cannot extract a sliding-window p99 from a Gather() snapshot
// alone. The watcher reads from this in-process buffer; Prometheus
// scrape continues to read from the parallel histogram.
//
// Internals: the ring buffer stores (at, latency, errored) entries.
// Percentile queries build a temporary WindowHistogram (HDR) over the
// requested sub-window — O(n) per query where n is the window size.
// Coverage (whether the oldest tracked sample is older than `sustain`)
// is answered directly from the ring's head pointer in O(1).
//
// The HDR histogram gives O(1) Record and O(1) Quantile, eliminating
// the O(n log n) sort that the v1 slice implementation required. The
// O(n) rebuild per query matches the v1 O(n) slice copy cost, with the
// sort cost eliminated.
//
// maxSamples bounds the ring buffer length so retention stays bounded
// at high publish rates. When 0, no cap is applied. WARNING: when
// peak_rps × max_sustain > cap, the ring compresses below the sustain
// interval and the abort watcher cannot fire; it emits a slog.Warn
// "abort watcher deafened by sample cap". Size cap >=
// peak_rps × max_sustain to keep the watcher functional.
type LatencyWindow struct {
	mu         sync.Mutex
	retain     time.Duration
	maxSamples int

	// ring is the entry store. Entries are appended in monotonic time
	// order. For bounded rings (maxSamples > 0), head and tail index
	// into the fixed-size slice using modular arithmetic. For unbounded
	// rings (maxSamples == 0), head=0 and the slice grows via append.
	ring    []ringEntry
	head    int // index of oldest live entry (bounded only)
	tail    int // index of next write slot (bounded only)
	ringLen int // number of live entries
}

// ringEntry records one observation.
type ringEntry struct {
	at      time.Time
	latency time.Duration
	errored bool
}

// NewLatencyWindow returns a buffer that retains samples for the
// specified retention window. Samples older than `retain` are pruned
// lazily on each AddAt.
func NewLatencyWindow(retain time.Duration) *LatencyWindow {
	return &LatencyWindow{retain: retain}
}

// WithMaxSamples caps the ring buffer at n entries (drop-oldest when
// full) and returns the receiver for fluent construction:
//
//	w := NewLatencyWindow(retain).WithMaxSamples(10_000)
//
// 0 disables the cap (legacy unbounded behavior). Concurrent updates
// are safe (locked) but the new cap only takes effect on the next
// Add — there's no retroactive truncation.
func (w *LatencyWindow) WithMaxSamples(n int) *LatencyWindow {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.maxSamples = n
	if n > 0 {
		// Pre-allocate bounded ring; existing ring is discarded (no
		// retroactive truncation per the documented contract).
		w.ring = make([]ringEntry, n)
		w.head = 0
		w.tail = 0
		w.ringLen = 0
	}
	return w
}

// Saturated reports whether the ring is currently at or above its cap.
// Fires one sample early (at len == cap) — better to fire the
// cap-masking diagnostic one sample too soon than to miss it.
// Returns false when no cap is set.
func (w *LatencyWindow) Saturated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maxSamples > 0 && w.ringLen >= w.maxSamples
}

// Add records one observation at time.Now(). Use AddAt in tests for
// deterministic timing.
func (w *LatencyWindow) Add(latency time.Duration, errored bool) {
	w.AddAt(time.Now(), latency, errored)
}

// AddAt records one observation at the given clock time. Prunes samples
// older than (at - retain) before appending.
func (w *LatencyWindow) AddAt(at time.Time, latency time.Duration, errored bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneByTimeLocked(at)
	w.appendLocked(ringEntry{at: at, latency: latency, errored: errored})
	// S3: cap the ring (drop-oldest) so retention stays bounded.
	if w.maxSamples > 0 && w.ringLen > w.maxSamples {
		w.advanceHeadLocked()
	}
}

// P99 returns the 99th-percentile latency over the last `over` window
// ending at `now`. Returns 0 when the window is empty.
func (w *LatencyWindow) P99(now time.Time, over time.Duration) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.quantileInWindowLocked(now, over, 0.99)
}

// P50 returns the median latency over the window.
func (w *LatencyWindow) P50(now time.Time, over time.Duration) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.quantileInWindowLocked(now, over, 0.50)
}

// percentileAt returns an arbitrary quantile over the window, used by
// the progress reporter to surface p95 alongside p50/p99.
func (w *LatencyWindow) percentileAt(now time.Time, over time.Duration, q float64) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.quantileInWindowLocked(now, over, q)
}

// P99WithCoverage returns the p99 AND whether the window covers the
// full sustain interval, in a single locked operation. Avoids the
// TOCTOU race where a separate P99() and a separate "is the oldest
// sample old enough" check could interleave with an Add.
func (w *LatencyWindow) P99WithCoverage(now time.Time, over time.Duration) (time.Duration, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ringLen == 0 {
		return 0, false
	}
	covered := !w.oldestEntryLocked().at.After(now.Add(-over))
	return w.quantileInWindowLocked(now, over, 0.99), covered
}

// ErrorRateWithCoverage returns the error fraction over the last `over`
// window AND whether the window has accumulated full coverage, atomically.
func (w *LatencyWindow) ErrorRateWithCoverage(now time.Time, over time.Duration) (float64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ringLen == 0 {
		return 0, false
	}
	covered := !w.oldestEntryLocked().at.After(now.Add(-over))
	cutoff := now.Add(-over)
	total, errs := 0, 0
	w.iterLocked(func(e ringEntry) {
		if e.at.Before(cutoff) || e.at.After(now) {
			return
		}
		total++
		if e.errored {
			errs++
		}
	})
	if total == 0 {
		return 0, covered
	}
	return float64(errs) / float64(total), covered
}

// Percentiles returns multiple quantiles over the same window in a
// single locked operation. Returns a slice the same length as `qs`.
// When `qs` is empty, returns nil.
func (w *LatencyWindow) Percentiles(now time.Time, over time.Duration, qs ...float64) []time.Duration {
	if len(qs) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// Build the in-window histogram once; query all quantiles from it.
	tmp := w.buildWindowHistLocked(now, over)
	out := make([]time.Duration, len(qs))
	for i, q := range qs {
		out[i] = tmp.Quantile(q)
	}
	return out
}

// ErrorRate returns the fraction of errored samples over the last `over`
// window ending at `now`. Returns 0 when the window is empty.
func (w *LatencyWindow) ErrorRate(now time.Time, over time.Duration) float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := now.Add(-over)
	total, errs := 0, 0
	w.iterLocked(func(e ringEntry) {
		if e.at.Before(cutoff) || e.at.After(now) {
			return
		}
		total++
		if e.errored {
			errs++
		}
	})
	if total == 0 {
		return 0
	}
	return float64(errs) / float64(total)
}

// ── internal helpers ─────────────────────────────────────────────────────────

// quantileInWindowLocked builds a temporary HDR histogram from ring
// entries within [now-over, now] and returns the requested quantile.
// O(n) where n is the number of in-window entries. The HDR eliminates
// the O(n log n) sort required by the v1 sorted-slice implementation.
// Caller must hold w.mu.
func (w *LatencyWindow) quantileInWindowLocked(now time.Time, over time.Duration, q float64) time.Duration {
	return w.buildWindowHistLocked(now, over).Quantile(q)
}

// buildWindowHistLocked returns a fresh WindowHistogram populated with
// the latencies of ring entries in [now-over, now]. Returns an empty
// histogram when the ring is empty or no entries fall in the window.
// Caller must hold w.mu.
func (w *LatencyWindow) buildWindowHistLocked(now time.Time, over time.Duration) *WindowHistogram {
	tmp := NewWindowHistogram() // NOTE: ~30KB alloc inside lock; acceptable at 1Hz watcher tick. If query rate increases, switch to a reusable scratch histogram + in-place Reset.
	cutoff := now.Add(-over)
	w.iterLocked(func(e ringEntry) {
		if e.at.Before(cutoff) || e.at.After(now) {
			return
		}
		tmp.Record(e.latency)
	})
	return tmp
}

// pruneByTimeLocked removes ring entries older than (at - retain).
// Must be called with w.mu held.
func (w *LatencyWindow) pruneByTimeLocked(at time.Time) {
	if w.ringLen == 0 {
		return
	}
	cutoff := at.Add(-w.retain)
	for w.ringLen > 0 && w.oldestEntryLocked().at.Before(cutoff) {
		w.advanceHeadLocked()
	}
}

// appendLocked adds a new entry to the ring. For bounded rings this
// writes at tail and wraps; for unbounded rings it appends to the slice.
func (w *LatencyWindow) appendLocked(e ringEntry) {
	if w.maxSamples == 0 {
		// Unbounded: simple append.
		w.ring = append(w.ring, e)
		w.ringLen = len(w.ring)
		w.tail = w.ringLen
		return
	}
	// Bounded ring buffer: write at tail, advance tail.
	w.ring[w.tail] = e
	w.tail = (w.tail + 1) % w.maxSamples
	w.ringLen++
}

// advanceHeadLocked drops the oldest entry by advancing the head pointer.
func (w *LatencyWindow) advanceHeadLocked() {
	if w.ringLen == 0 {
		return
	}
	if w.maxSamples == 0 {
		// Unbounded: drop first element.
		w.ring = w.ring[1:]
		w.ringLen = len(w.ring)
		w.tail = w.ringLen
		w.head = 0
		return
	}
	w.head = (w.head + 1) % w.maxSamples
	w.ringLen--
}

// oldestEntryLocked returns the oldest (head) ring entry.
// Caller must ensure ringLen > 0.
func (w *LatencyWindow) oldestEntryLocked() ringEntry {
	if w.maxSamples == 0 {
		return w.ring[0]
	}
	return w.ring[w.head]
}

// iterLocked calls fn for each live entry in insertion order (oldest first).
// Must be called with w.mu held.
func (w *LatencyWindow) iterLocked(fn func(ringEntry)) {
	if w.ringLen == 0 {
		return
	}
	if w.maxSamples == 0 {
		for i := 0; i < w.ringLen; i++ {
			fn(w.ring[i])
		}
		return
	}
	// Bounded ring: iterate from head to tail (wrapping).
	for i := 0; i < w.ringLen; i++ {
		fn(w.ring[(w.head+i)%w.maxSamples])
	}
}

// ── abort watcher ─────────────────────────────────────────────────────────

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
