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
	// Window, when non-nil, is consulted for live percentile snapshots
	// (p50/p95/p99 over the last `WindowOver`). Spec §3.2 promises these
	// fields on every progress line. Optional; nil-safe.
	Window     *LatencyWindow
	WindowOver time.Duration
	// Run boundary fields used to populate `elapsed`, `remaining`, and
	// `target` on each progress line. RunStart is the moment the
	// generator's measurement window began; RunDuration is the
	// configured --duration. Both zero-value-safe.
	RunStart    time.Time
	RunDuration time.Duration
	TargetRate  int
}

// runProgress reads metric snapshots on each Ticks signal and emits a
// structured "progress" log line carrying the deltas since the previous
// snapshot. Returns when ctx is cancelled.
//
// Phase 3 §3.2: this is the live progress reporter. Inject the tick
// channel from time.NewTicker(--progress-interval) at the call site.
// Each emitted line carries:
//
//	rate_rps             current instantaneous publish/request rate
//	target               configured target rate (rps)
//	delta_published      publishes since the previous tick
//	delta_publish_errors publish errors since the previous tick
//	delta_requests       requests since the previous tick
//	delta_request_errors request errors since the previous tick
//	saturated            cumulative loadgen-side drops since start
//	elapsed              wall-clock elapsed since RunStart
//	remaining            wall-clock remaining until RunStart+RunDuration
//	elapsed_window       wall-clock between this tick and the previous
//	p50_ms / p95_ms / p99_ms latency percentiles over the abort window
func runProgress(ctx context.Context, cfg *progressConfig) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	prev := snapshotProgress(cfg.Metrics)
	prevTime := time.Now()
	if cfg.RunStart.IsZero() {
		cfg.RunStart = time.Now()
	}
	if cfg.WindowOver <= 0 {
		cfg.WindowOver = 30 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-cfg.Ticks:
			if !ok {
				return
			}
			cur := snapshotProgress(cfg.Metrics)
			elapsedSinceTick := t.Sub(prevTime)
			rate := 0.0
			if elapsedSinceTick > 0 {
				rate = float64(cur.published-prev.published) / elapsedSinceTick.Seconds()
			}
			elapsed := t.Sub(cfg.RunStart)
			remaining := time.Duration(0)
			if cfg.RunDuration > 0 {
				remaining = cfg.RunDuration - elapsed
				if remaining < 0 {
					remaining = 0
				}
			}
			var p50, p95, p99 time.Duration
			if cfg.Window != nil {
				p50 = cfg.Window.P50(t, cfg.WindowOver)
				p95 = cfg.Window.percentileAt(t, cfg.WindowOver, 0.95)
				p99 = cfg.Window.P99(t, cfg.WindowOver)
			}
			logger.LogAttrs(ctx, slog.LevelInfo, "progress",
				slog.Float64("rate_rps", rate),
				slog.Int("target", cfg.TargetRate),
				slog.Float64("delta_published", float64(cur.published-prev.published)),
				slog.Float64("delta_publish_errors", float64(cur.publishErrors-prev.publishErrors)),
				slog.Float64("delta_request_errors", float64(cur.requestErrors-prev.requestErrors)),
				slog.Float64("delta_requests", float64(cur.requests-prev.requests)),
				slog.Float64("saturated", cur.saturated),
				slog.Duration("elapsed", elapsed),
				slog.Duration("remaining", remaining),
				slog.Duration("elapsed_window", elapsedSinceTick),
				slog.Float64("p50_ms", float64(p50.Microseconds())/1000.0),
				slog.Float64("p95_ms", float64(p95.Microseconds())/1000.0),
				slog.Float64("p99_ms", float64(p99.Microseconds())/1000.0),
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
	saturated     float64
}

func snapshotProgress(m *Metrics) progressSnapshot {
	mfs, err := m.Registry.Gather()
	if err != nil {
		return progressSnapshot{}
	}
	return progressSnapshot{
		published:     gatheredCounterValue(mfs, "loadgen_published_total", "", ""),
		publishErrors: gatheredCounterValue(mfs, "loadgen_publish_errors_total", "", ""),
		requests:      gatheredCounterValue(mfs, "loadgen_requests_total", "", ""),
		requestErrors: gatheredCounterValue(mfs, "loadgen_request_errors_total", "", ""),
		saturated: gatheredCounterValue(mfs, "loadgen_publish_errors_total", "reason", "saturated") +
			gatheredCounterValue(mfs, "loadgen_request_errors_total", "reason", "saturated"),
	}
}
