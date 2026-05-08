package main

import (
	"context"
	"log/slog"
	"time"
)

// progressConfig is the parameter bundle for runProgress. The Ticks
// channel is injected so tests can drive the loop deterministically;
// production use sets it to time.NewTicker(interval).C.
type progressConfig struct {
	Metrics *Metrics
	Preset  string
	Logger  *slog.Logger
	Ticks   <-chan time.Time
}

// runProgress reads metric snapshots on each Ticks signal and emits a
// structured "progress" log line carrying the deltas since the previous
// snapshot. Returns when ctx is cancelled.
//
// Phase 3 §3.2: this is the live progress reporter. Inject the tick
// channel from time.NewTicker(--progress-interval) at the call site.
func runProgress(ctx context.Context, cfg *progressConfig) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	prev := snapshotProgress(cfg.Metrics, cfg.Preset)
	prevTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-cfg.Ticks:
			if !ok {
				return
			}
			cur := snapshotProgress(cfg.Metrics, cfg.Preset)
			elapsed := t.Sub(prevTime)
			rate := 0.0
			if elapsed > 0 {
				rate = float64(cur.published-prev.published) / elapsed.Seconds()
			}
			logger.LogAttrs(ctx, slog.LevelInfo, "progress",
				slog.Float64("rate_rps", rate),
				slog.Float64("delta_published", float64(cur.published-prev.published)),
				slog.Float64("delta_publish_errors", float64(cur.publishErrors-prev.publishErrors)),
				slog.Float64("delta_request_errors", float64(cur.requestErrors-prev.requestErrors)),
				slog.Float64("delta_requests", float64(cur.requests-prev.requests)),
				slog.Duration("elapsed_window", elapsed),
			)
			prev = cur
			prevTime = t
		}
	}
}

// progressSnapshot is the cumulative counter snapshot used for delta
// computation between ticks.
type progressSnapshot struct {
	published     float64
	publishErrors float64
	requests      float64
	requestErrors float64
}

func snapshotProgress(m *Metrics, _ string) progressSnapshot {
	mfs, err := m.Registry.Gather()
	if err != nil {
		return progressSnapshot{}
	}
	return progressSnapshot{
		published:     gatheredCounterValue(mfs, "loadgen_published_total", "", ""),
		publishErrors: gatheredCounterValue(mfs, "loadgen_publish_errors_total", "", ""),
		requests:      gatheredCounterValue(mfs, "loadgen_requests_total", "", ""),
		requestErrors: gatheredCounterValue(mfs, "loadgen_request_errors_total", "", ""),
	}
}
