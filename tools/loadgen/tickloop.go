package main

import (
	"context"
	"sync"
	"time"
)

// tickLoopConfig is the shared configuration for the read-scenario tick
// loop. Each generator (HistoryReadGenerator, SearchReadGenerator,
// RoomRPCGenerator) provides its own per-tick body via the `tick`
// argument to tickLoop.
type tickLoopConfig struct {
	Rate        int
	MaxInFlight int
	Metrics     *Metrics
	Preset      string // preset name; the metrics label
	Scenario    string // scenario name; the metrics label ("history" / "search" / "room")
	// Ramp, when non-nil, overrides Rate: the ticker's interval is
	// recomputed once per second from Ramp.RateAt(elapsed). When nil,
	// the loop ticks at a fixed Rate for the whole run.
	Ramp *Ramp
	// Omission, when non-nil, records coordinated-omission deficits for
	// each tick: the gap between intended dispatch time and actual start
	// (serviced) or drop time (dropped/saturated).
	Omission *OmissionTracker
}

// rateRebuildInterval is how often tickLoop polls the Ramp curve to
// rebuild its ticker. A coarse 1s cadence keeps overhead negligible
// while still giving fine-grained latency knee detection in Grafana.
const rateRebuildInterval = 1 * time.Second

// tickLoop runs `tick` at `cfg.Rate` ticks per second until ctx is
// cancelled. With MaxInFlight > 0, ticks dispatch into a bounded
// goroutine pool and pool saturation is recorded as
// loadgen_request_errors_total{scenario, kind="*", reason="saturated"}
// (Cleanup C: queue-level event, not per-RPC, so kind is the wildcard
// "*" rather than a duplicated "saturated").
//
// When cfg.Ramp is non-nil, the rate follows the ramp curve and the
// ticker is rebuilt every rateRebuildInterval (~1s).
//
// On ctx cancellation, drains in-flight ticks for up to drainGracePeriod
// before returning.
func tickLoop(ctx context.Context, cfg tickLoopConfig, tick func(context.Context)) {
	rate := cfg.Rate
	if cfg.Ramp != nil {
		rate = cfg.Ramp.RateAt(0)
	}
	interval := tickInterval(rate)

	t := time.NewTicker(interval)
	defer t.Stop()

	var rebuild <-chan time.Time
	start := time.Now()
	if cfg.Ramp != nil {
		// Sample the ramp curve at most every rateRebuildInterval, but
		// never less often than 10 sub-intervals across the ramp window
		// — keeps short test ramps observable while bounding overhead on
		// production-length runs.
		ri := rateRebuildInterval
		if cfg.Ramp.Duration > 0 && cfg.Ramp.Duration/10 < ri {
			ri = cfg.Ramp.Duration / 10
		}
		if ri <= 0 {
			ri = rateRebuildInterval
		}
		rebuildTicker := time.NewTicker(ri)
		defer rebuildTicker.Stop()
		rebuild = rebuildTicker.C
	}

	maybeRebuild := func() {
		if cfg.Ramp == nil {
			return
		}
		newRate := cfg.Ramp.RateAt(time.Since(start))
		if newRate <= 0 {
			return
		}
		// Go 1.23+ Reset doesn't require a drained channel; at most one
		// stale tick from the old interval may fire immediately after
		// the rate change, briefly doubling the rate by a single tick.
		// We accept that — the rate-bucket label captures the regime
		// shift and a single extra tick is below any meaningful signal.
		t.Reset(tickInterval(newRate))
	}

	if cfg.MaxInFlight <= 0 {
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				tick(ctx)
			case <-rebuild:
				maybeRebuild()
			}
		}
	}

	sem := make(chan struct{}, cfg.MaxInFlight)
	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(drainGracePeriod):
			}
			return
		case <-t.C:
			intendedAt := time.Now()
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func() {
					actualStart := time.Now()
					if cfg.Omission != nil {
						cfg.Omission.RecordServiced(intendedAt, actualStart)
					}
					defer func() {
						<-sem
						wg.Done()
					}()
					tick(ctx)
				}()
			default:
				// B2: account saturated drops as attempts in the
				// Requests counter too, so rate(loadgen_requests_total)
				// matches rate(loadgen_request_errors_total{reason="saturated"})
				// + the in-flight ticks. Phase label is "saturated" to
				// keep the warmup/measured split clean for the success
				// path.
				if cfg.Omission != nil {
					cfg.Omission.RecordDropped(intendedAt, time.Now())
				}
				cfg.Metrics.Requests.WithLabelValues(
					cfg.Preset, cfg.Scenario, "*", "saturated",
				).Inc()
				cfg.Metrics.RequestErrors.WithLabelValues(
					cfg.Preset, cfg.Scenario, "*", "saturated",
				).Inc()
			}
		case <-rebuild:
			maybeRebuild()
		}
	}
}

// minTickInterval clamps the lower bound of a ticker interval so that
// extreme rates (e.g. configured at the int64 limit, or a ramp curve
// crossing into 1ns territory) don't spin the ticker fast enough to
// peg a CPU. 1µs corresponds to ~1M ticks/s — well above any realistic
// load test target — and gives the goroutine pool time to dispatch.
const minTickInterval = 1 * time.Microsecond

func tickInterval(rate int) time.Duration {
	if rate <= 0 {
		return time.Second
	}
	interval := time.Second / time.Duration(rate)
	if interval < minTickInterval {
		return minTickInterval
	}
	return interval
}
