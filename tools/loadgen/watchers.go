package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// startAbortWatcher launches the saturation auto-detect goroutine in the
// background. It polls the latency window every second and calls cancelRun
// when the configured P99 or error-rate threshold is sustained long enough.
// The caller must add 1 to wg before calling and pass the same WaitGroup;
// the goroutine calls wg.Done on exit.
//
// If neither threshold is configured (rf.Abort.P99Ms == 0 && rf.Abort.ErrorPct
// == 0), no goroutine is started and wg is not touched.
//
// Extracted from executeRun to improve cohesion; no behaviour is changed.
func startAbortWatcher(
	ctx context.Context,
	rf *runFlags,
	latencyWindow *LatencyWindow,
	cancelRun context.CancelFunc,
	tripped *atomic.Bool,
	reason *atomic.Value,
	wg *sync.WaitGroup,
) {
	if rf.Abort.P99Ms == 0 && rf.Abort.ErrorPct == 0 {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		abortCfg := &abortConfig{
			Window:       latencyWindow,
			P99Limit:     time.Duration(rf.Abort.P99Ms) * time.Millisecond,
			P99Sustain:   rf.Abort.P99Sustain,
			ErrorPct:     rf.Abort.ErrorPct,
			ErrorSustain: rf.Abort.ErrorSustain,
		}
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				if fired, r := abortShouldFire(abortCfg, t); fired {
					slog.Warn("abort fired", "reason", r)
					tripped.Store(true)
					reason.Store(r)
					cancelRun()
					return
				}
			}
		}
	}()
}

// startLivenessWatcher launches the mid-run liveness watcher goroutine in the
// background. It probes the SUT periodically and calls cancelRun when
// rf.Liveness.Failures consecutive failures are observed. The caller must add
// 1 to wg before calling; the goroutine calls wg.Done on exit.
//
// If rf.Liveness.Interval <= 0 or there are no subscriptions in fixtures, no
// goroutine is started and wg is not touched.
//
// Probes use the OBSERVER connection regardless of --connections /
// --nats-creds-dir so per-user creds rotation cannot cause permission-denied
// false-positives (F3 fix).
//
// Extracted from executeRun to improve cohesion; no behaviour is changed.
func startLivenessWatcher(
	ctx context.Context,
	rf *runFlags,
	sc Scenario,
	deps *runDeps,
	pool *ConnPool,
	metrics *Metrics,
	p *Preset,
	runID string,
	failed *atomic.Bool,
	reason *atomic.Value,
	wg *sync.WaitGroup,
	cancelRun context.CancelFunc,
) {
	if rf.Liveness.Interval <= 0 || len(deps.fixtures.Subscriptions) == 0 {
		return
	}
	// Observer-routed requester so the probe uses the global creds
	// (cfg.NatsCredsFile) regardless of pool fan-out and rotation.
	observerPool := &ConnPool{observer: pool.Observer()}
	observerReq := &natsRequester{pool: observerPool, runID: runID}
	// Build a ScenarioDeps view restricted to the observer connection
	// so per-user creds rotation can't cause permission-denied false-positives (F3 fix).
	observerDeps := deps.withRequester(observerReq)
	liveProbe := buildLivenessProbeFromScenario(sc, observerDeps, pool.Observer())
	wg.Add(1)
	go func() {
		defer wg.Done()
		runLiveness(ctx, &livenessConfig{
			Probe:            liveProbe,
			Interval:         rf.Liveness.Interval,
			ConsecutiveFails: rf.Liveness.Failures,
			Timeout:          rf.Liveness.Timeout,
			Counter: func(result string) {
				metrics.LivenessProbes.WithLabelValues(p.Name, result).Inc()
			},
		}, failed, func(r string) {
			slog.Warn("liveness watcher fired", "reason", r)
			reason.Store(r)
			cancelRun()
		})
	}()
}
