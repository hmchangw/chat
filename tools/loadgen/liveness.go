package main

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// LivenessProbe is the per-attempt callable used by runLiveness. Returns
// nil if the SUT is reachable, an error otherwise. Same shape as
// readinessConfig.Probe so callers can reuse buildReadinessProbe.
type LivenessProbe func(ctx context.Context) error

// livenessConfig is the parameter bundle for runLiveness.
type livenessConfig struct {
	Probe LivenessProbe
	// Interval between probes. Defaults to 30s if <= 0.
	Interval time.Duration
	// ConsecutiveFails is the number of back-to-back probe failures
	// required to trip the liveness watcher. Defaults to 3 if <= 0.
	// Single transient failures are normal; sustained failure indicates
	// the SUT has actually become unreachable.
	ConsecutiveFails int
	// Timeout per probe attempt. Defaults to 5s if <= 0.
	Timeout time.Duration
	// Counter is the optional metric counter the watcher increments with
	// labels (result="ok" | "fail"). Lets dashboards plot probe health.
	// Nil-safe.
	Counter func(result string)
}

// ErrLivenessDegraded is returned via the cancel cause when the
// watcher trips: ConsecutiveFails consecutive probe failures.
var ErrLivenessDegraded = errors.New("liveness probe failed for the configured streak")

// runLiveness probes the SUT periodically while a load run is in
// progress. On `ConsecutiveFails` back-to-back failures, sets the
// `failed` atomic to true (caller-observable) and calls `onFail` (which
// is typically the run context's cancelRun). Returns when ctx is done.
//
// Phase 3 §3.3 + SRE blocker #4: one-shot readiness probes cover
// "SUT was up when we started"; the mid-run watcher covers "SUT is
// STILL up." Without it, a 2-hour run that loses NATS at minute 30
// produces 90 minutes of garbage data marked "pass."
func runLiveness(ctx context.Context, cfg *livenessConfig, failed *atomic.Bool, onFail func(reason string)) {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	fails := cfg.ConsecutiveFails
	if fails <= 0 {
		fails = 3
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	consecutive := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, timeout)
			err := cfg.Probe(probeCtx)
			cancel()
			if err != nil {
				consecutive++
				if cfg.Counter != nil {
					cfg.Counter("fail")
				}
				if consecutive >= fails {
					failed.Store(true)
					if onFail != nil {
						onFail("liveness probe failed " +
							strconvI(consecutive) + " consecutive times")
					}
					return
				}
				continue
			}
			consecutive = 0
			if cfg.Counter != nil {
				cfg.Counter("ok")
			}
		}
	}
}

// strconvI keeps liveness.go free of strconv import — used once for the
// trip reason string.
func strconvI(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = '0' + byte(i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
