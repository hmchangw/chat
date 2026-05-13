package main

import (
	"fmt"
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
)

// CellHistogram backs per-(scenario, kind, phase) latency cells.
// Floor 100µs, ceiling 60s, 3 sig digits. ~30 KB per histogram.
type CellHistogram struct{ h *hdr.Histogram }

// NewCellHistogram returns a CellHistogram ready to record.
func NewCellHistogram() *CellHistogram {
	return &CellHistogram{h: hdr.New(int64(100*time.Microsecond), int64(60*time.Second), 3)}
}

func (c *CellHistogram) Record(d time.Duration) {
	_ = c.h.RecordValue(int64(d)) // out-of-range values (>60s ceiling) are silently dropped; clipping is acceptable for this metric
}

// Quantile returns the value at the given quantile q in [0.0, 1.0].
// Returns 0 on an empty histogram.
// The underlying library uses percentile notation [0.0, 100.0], so q is scaled.
func (c *CellHistogram) Quantile(q float64) time.Duration {
	if c.h.TotalCount() == 0 {
		return 0
	}
	return time.Duration(c.h.ValueAtQuantile(q * 100))
}

func (c *CellHistogram) Count() int64 { return c.h.TotalCount() }

func (c *CellHistogram) Merge(other *CellHistogram) error {
	if dropped := c.h.Merge(other.h); dropped > 0 {
		return fmt.Errorf("merging histograms: %d values dropped (out of range)", dropped)
	}
	return nil
}

// Reset clears the histogram (used when a run starts a new measured-window phase).
func (c *CellHistogram) Reset() { c.h.Reset() }

// Export returns a *hdr.Snapshot serializable to JSON (consumed by
// artifacts.go's WriteBundle into histograms.hlog). The caller is responsible for
// serializing the *hdr.Snapshot (e.g., via encoding/json).
// Returns a *hdr.Snapshot (library type); callers must import
// github.com/HdrHistogram/hdrhistogram-go directly to use the returned value.
func (c *CellHistogram) Export() *hdr.Snapshot {
	return c.h.Export()
}

// WindowHistogram backs the abort-watcher rolling window.
// Tighter floor 10µs since the watcher is safety-critical. No Merge: rolling
// windows are Reset between intervals, not merged.
type WindowHistogram struct{ h *hdr.Histogram }

// NewWindowHistogram returns a WindowHistogram with a tighter 10µs floor for the abort watcher.
func NewWindowHistogram() *WindowHistogram {
	return &WindowHistogram{h: hdr.New(int64(10*time.Microsecond), int64(60*time.Second), 3)}
}

func (w *WindowHistogram) Record(d time.Duration) {
	_ = w.h.RecordValue(int64(d)) // out-of-range values (>60s ceiling) are silently dropped; clipping is acceptable for this metric
}

// Quantile returns the value at the given quantile q in [0.0, 1.0].
// Returns 0 on an empty histogram.
// The underlying library uses percentile notation [0.0, 100.0], so q is scaled.
func (w *WindowHistogram) Quantile(q float64) time.Duration {
	if w.h.TotalCount() == 0 {
		return 0
	}
	return time.Duration(w.h.ValueAtQuantile(q * 100))
}

func (w *WindowHistogram) Count() int64 { return w.h.TotalCount() }
func (w *WindowHistogram) Reset()       { w.h.Reset() }
