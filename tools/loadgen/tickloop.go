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
	// WarmupDeadline, when non-zero, is used to label saturated-drop
	// errors with the correct phase ("warmup" or "measured"). Zero value
	// is safe: time.Now().Before(zero) is always false, so every drop is
	// labelled "measured" — correct for callers that don't have a warmup.
	WarmupDeadline time.Time

	// Burst envelope: periodically multiply the rate by BurstRatio for
	// BurstDuration every BurstPeriod. Models incident/spike traffic shapes.
	// Zero values disable the envelope (steady rate).
	//
	// BaselineRate is the quiet-period rate used when burst is active; if
	// zero, Rate is used instead. BurstRatio is the integer multiplier applied
	// during the burst window (e.g. 40 = 40× baseline). The burst window
	// starts at elapsed=0 relative to the loop start time.
	BaselineRate  int
	BurstPeriod   time.Duration
	BurstRatio    int
	BurstDuration time.Duration
}

// burstEnabled reports whether cfg has a fully-specified burst envelope.
func (cfg *tickLoopConfig) burstEnabled() bool {
	return cfg.BurstPeriod > 0 && cfg.BurstRatio > 1 && cfg.BurstDuration > 0
}

// currentRate returns the effective ticks-per-second at time now given the
// loop's start time. When burst is disabled or Ramp is active, the Ramp or
// Rate field governs (unchanged from original behaviour).
//
// When burst is active, the rate alternates between BaselineRate (or Rate if
// BaselineRate is zero) during the quiet phase and BaselineRate×BurstRatio
// during the burst phase. Burst windows begin at elapsed multiples of
// BurstPeriod and last for BurstDuration.
func (cfg *tickLoopConfig) currentRate(now, start time.Time) int {
	if !cfg.burstEnabled() {
		// Burst disabled — callers that also have a Ramp will not call this
		// for the ramp path; this is the steady-rate fallback.
		base := cfg.BaselineRate
		if base <= 0 {
			base = cfg.Rate
		}
		return base
	}
	base := cfg.BaselineRate
	if base <= 0 {
		base = cfg.Rate
	}
	elapsed := now.Sub(start)
	if elapsed < 0 {
		elapsed = 0
	}
	cyclePos := elapsed % cfg.BurstPeriod
	if cyclePos < cfg.BurstDuration {
		return base * cfg.BurstRatio
	}
	return base
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
// When cfg.BurstPeriod > 0, the tick rate alternates between
// cfg.BaselineRate (or cfg.Rate) and cfg.BaselineRate×cfg.BurstRatio on
// a BurstPeriod cycle. The burst envelope rebuild cadence is
// BurstDuration/10 (capped at rateRebuildInterval) so that short burst
// windows are detected promptly.
//
// On ctx cancellation, drains in-flight ticks for up to drainGracePeriod
// before returning.
func tickLoop(ctx context.Context, cfg *tickLoopConfig, tick func(context.Context)) {
	start := time.Now()

	rate := cfg.Rate
	if cfg.Ramp != nil {
		rate = cfg.Ramp.RateAt(0)
	} else if cfg.burstEnabled() {
		rate = cfg.currentRate(start, start)
	}
	interval := tickInterval(rate)

	t := time.NewTicker(interval)
	defer t.Stop()

	var rebuild <-chan time.Time
	if cfg.Ramp != nil || cfg.burstEnabled() {
		// Choose the rebuild cadence: for ramp, at most rateRebuildInterval
		// but at least Duration/10; for burst, at most rateRebuildInterval
		// but at least BurstDuration/10 so we don't miss a burst edge.
		ri := rateRebuildInterval
		if cfg.Ramp != nil && cfg.Ramp.Duration > 0 && cfg.Ramp.Duration/10 < ri {
			ri = cfg.Ramp.Duration / 10
		}
		if cfg.burstEnabled() && cfg.BurstDuration/10 < ri {
			ri = cfg.BurstDuration / 10
		}
		if ri <= 0 {
			ri = rateRebuildInterval
		}
		rebuildTicker := time.NewTicker(ri)
		defer rebuildTicker.Stop()
		rebuild = rebuildTicker.C
	}

	maybeRebuild := func() {
		var newRate int
		if cfg.Ramp != nil {
			newRate = cfg.Ramp.RateAt(time.Since(start))
		} else if cfg.burstEnabled() {
			newRate = cfg.currentRate(time.Now(), start)
		}
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
				saturatedPhase := "measured"
				if intendedAt.Before(cfg.WarmupDeadline) {
					saturatedPhase = "warmup"
				}
				cfg.Metrics.Requests.WithLabelValues(
					cfg.Preset, cfg.Scenario, "*", "saturated",
				).Inc()
				cfg.Metrics.RequestErrors.WithLabelValues(
					cfg.Preset, cfg.Scenario, "*", saturatedPhase, "saturated",
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
