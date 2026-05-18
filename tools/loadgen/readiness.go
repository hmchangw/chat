package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// natsConnLike is the subset of *nats.Conn used by buildLivenessProbeFromScenario
// in run.go. Keeping it tight lets tests inject a fake without dragging the
// full NATS connection lifecycle.
type natsConnLike interface {
	RTT() (time.Duration, error)
}

// ErrReadinessTimeout is returned when waitForReady gives up before the
// probe succeeds. Distinct from context.DeadlineExceeded so callers can
// differentiate "we hit our internal cap" from "ctx was cancelled
// upstream".
var ErrReadinessTimeout = errors.New("readiness probe deadline exceeded")

// readinessConfig is the parameter bundle for waitForReady. Probe is
// the per-attempt function; the orchestration of "retry with exponential
// backoff up to a deadline" lives in waitForReady.
type readinessConfig struct {
	Probe      func(ctx context.Context) error
	MinBackoff time.Duration // initial backoff between failed attempts
	MaxBackoff time.Duration // ceiling for exponential growth
}

// waitForReady calls cfg.Probe in a loop, with exponential backoff
// (cfg.MinBackoff doubling, capped at cfg.MaxBackoff), until the probe
// returns nil or ctx is cancelled. Returns nil on success,
// ErrReadinessTimeout if ctx expired with the cause set to deadline,
// or the wrapped ctx error if the upstream cancelled.
//
// Phase 3 §3.3: this is the readiness probe used to wait for the
// services-under-test to be reachable before generator traffic starts,
// avoiding first-second error spikes after `compose up`.
func waitForReady(ctx context.Context, cfg *readinessConfig) error {
	if cfg.Probe == nil {
		return fmt.Errorf("readinessConfig.Probe must be set")
	}
	backoff := cfg.MinBackoff
	if backoff <= 0 {
		backoff = 200 * time.Millisecond
	}
	maxB := cfg.MaxBackoff
	if maxB <= 0 {
		maxB = 2 * time.Second
	}

	for {
		err := cfg.Probe(ctx)
		if err == nil {
			return nil
		}
		// Wait before retrying.
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ErrReadinessTimeout
			}
			return fmt.Errorf("readiness aborted: %w", ctx.Err())
		case <-t.C:
		}
		backoff *= 2
		if backoff > maxB {
			backoff = maxB
		}
	}
}
