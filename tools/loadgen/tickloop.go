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
}

// tickLoop runs `tick` at `cfg.Rate` ticks per second until ctx is
// cancelled. With MaxInFlight > 0, ticks dispatch into a bounded
// goroutine pool and pool saturation is recorded as
// loadgen_request_errors_total{scenario, kind="*", reason="saturated"}
// (Cleanup C: queue-level event, not per-RPC, so kind is the wildcard
// "*" rather than a duplicated "saturated").
//
// On ctx cancellation, drains in-flight ticks for up to drainGracePeriod
// before returning.
func tickLoop(ctx context.Context, cfg tickLoopConfig, tick func(context.Context)) {
	interval := time.Second / time.Duration(cfg.Rate)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	if cfg.MaxInFlight <= 0 {
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				tick(ctx)
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
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer func() {
						<-sem
						wg.Done()
					}()
					tick(ctx)
				}()
			default:
				cfg.Metrics.RequestErrors.WithLabelValues(
					cfg.Preset, cfg.Scenario, "*", "saturated",
				).Inc()
			}
		}
	}
}
