package main

import (
	"context"
	"fmt"
	"time"
)

// SettleFlags configures the optional settle phase that runs after warmup but
// before the measured window. The settle phase probes N recent message-IDs to
// ensure they are visible in the downstream system before measurements begin
// (read-after-write fence).
type SettleFlags struct {
	Timeout  time.Duration
	Interval time.Duration
	Probes   int // N message-IDs to sample; 0 disables the settle phase
}

// SettleOutcome reports the result of a Settle call.
type SettleOutcome struct {
	AllSucceeded bool
	Succeeded    int
	Failed       int
	Probes       []string
}

// ProbeFn is called once per message-ID per poll round. It returns nil when
// the ID is visible in the downstream system, or a non-nil error when it is
// not yet visible.
type ProbeFn func(ctx context.Context, id string) error

// Settle polls sampleIDs using probe until all IDs respond successfully or the
// deadline expires. It returns a non-nil error when any ID fails to become
// visible before sf.Timeout elapses.
//
// When sf.Probes == 0 or sampleIDs is empty the function returns immediately
// with AllSucceeded == true (settle phase disabled).
func Settle(ctx context.Context, sf SettleFlags, sampleIDs []string, probe ProbeFn) (SettleOutcome, error) {
	out := SettleOutcome{Probes: sampleIDs}
	if sf.Probes == 0 || len(sampleIDs) == 0 {
		out.AllSucceeded = true
		return out, nil
	}
	deadline := time.Now().Add(sf.Timeout)
	ok := make(map[string]bool)
	for time.Now().Before(deadline) && len(ok) < len(sampleIDs) {
		for _, id := range sampleIDs {
			if ok[id] {
				continue
			}
			if err := probe(ctx, id); err == nil {
				ok[id] = true
			}
		}
		if len(ok) >= len(sampleIDs) {
			break
		}
		select {
		case <-ctx.Done():
			return out, fmt.Errorf("settle aborted: %w", ctx.Err())
		case <-time.After(sf.Interval):
		}
	}
	out.Succeeded = len(ok)
	out.Failed = len(sampleIDs) - len(ok)
	out.AllSucceeded = (out.Failed == 0)
	if !out.AllSucceeded {
		return out, fmt.Errorf("settle timed out: %d/%d probes succeeded", out.Succeeded, len(sampleIDs))
	}
	return out, nil
}
