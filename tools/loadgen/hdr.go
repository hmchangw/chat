package main

import (
	"fmt"
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
)

// CellHistogram backs per-(scenario, kind, phase) latency cells.
// Floor 100µs, ceiling 60s, 3 sig digits. ~30 KB per histogram.
type CellHistogram struct{ h *hdr.Histogram }

func NewCellHistogram() *CellHistogram {
	return &CellHistogram{h: hdr.New(int64(100*time.Microsecond), int64(60*time.Second), 3)}
}

func (c *CellHistogram) Record(d time.Duration) {
	_ = c.h.RecordValue(int64(d))
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

// Export returns a snapshot of the histogram for inclusion in the artifact
// bundle's histograms file (Phase 1b §1.8). The caller is responsible for
// serializing the *hdr.Snapshot (e.g., via encoding/json).
//
// TODO(Phase 1b §1.8): wire this into the artifact bundle writer once the
// HDR log format is determined.
func (c *CellHistogram) Export() *hdr.Snapshot {
	return c.h.Export()
}

// WindowHistogram backs the abort-watcher rolling window.
// Tighter floor 10µs since the watcher is safety-critical.
type WindowHistogram struct{ h *hdr.Histogram }

func NewWindowHistogram() *WindowHistogram {
	return &WindowHistogram{h: hdr.New(int64(10*time.Microsecond), int64(60*time.Second), 3)}
}

func (w *WindowHistogram) Record(d time.Duration) { _ = w.h.RecordValue(int64(d)) }

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
