package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunLiveness_StaysSilentOnHealthyProbes(t *testing.T) {
	var failed atomic.Bool
	var onFailCalled atomic.Bool
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	runLiveness(ctx, &livenessConfig{
		Probe:            func(_ context.Context) error { return nil },
		Interval:         10 * time.Millisecond,
		ConsecutiveFails: 3,
		Timeout:          5 * time.Millisecond,
	}, &failed, func(_ string) { onFailCalled.Store(true) })

	assert.False(t, failed.Load(), "healthy probes must not trip the watcher")
	assert.False(t, onFailCalled.Load(), "onFail must not be invoked when probes succeed")
}

func TestRunLiveness_TripsAfterConsecutiveFails(t *testing.T) {
	var failed atomic.Bool
	var reason atomic.Value
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	runLiveness(ctx, &livenessConfig{
		Probe:            func(_ context.Context) error { return errors.New("unreachable") },
		Interval:         5 * time.Millisecond,
		ConsecutiveFails: 3,
		Timeout:          2 * time.Millisecond,
	}, &failed, func(r string) { reason.Store(r) })

	assert.True(t, failed.Load(), "3 consecutive failures must trip the watcher")
	r, ok := reason.Load().(string)
	require.True(t, ok)
	assert.Contains(t, r, "liveness probe failed")
	assert.Contains(t, r, "3")
}

func TestRunLiveness_ResetsConsecutiveOnRecovery(t *testing.T) {
	var failed atomic.Bool
	var probeCount atomic.Int64
	// Fail twice, then succeed forever. Should NOT trip even after 100ms
	// of probing because the streak never reaches ConsecutiveFails=3.
	probe := func(_ context.Context) error {
		n := probeCount.Add(1)
		if n <= 2 {
			return errors.New("transient")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	runLiveness(ctx, &livenessConfig{
		Probe:            probe,
		Interval:         5 * time.Millisecond,
		ConsecutiveFails: 3,
		Timeout:          2 * time.Millisecond,
	}, &failed, nil)

	assert.False(t, failed.Load(), "transient failures followed by recovery must not trip")
}

func TestRunLiveness_HonorsContextCancel(t *testing.T) {
	var failed atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	runLiveness(ctx, &livenessConfig{
		Probe:            func(_ context.Context) error { return errors.New("would fail") },
		Interval:         1 * time.Millisecond,
		ConsecutiveFails: 1,
	}, &failed, nil)
	assert.False(t, failed.Load(), "cancelled ctx must return without probing")
}

func TestRunLiveness_CounterReportsOkAndFail(t *testing.T) {
	var failed atomic.Bool
	var ok, fail atomic.Int64
	probeCount := 0
	probe := func(_ context.Context) error {
		probeCount++
		if probeCount%2 == 0 {
			return errors.New("alternating")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	runLiveness(ctx, &livenessConfig{
		Probe:            probe,
		Interval:         5 * time.Millisecond,
		ConsecutiveFails: 100, // never trip
		Counter: func(result string) {
			if result == "ok" {
				ok.Add(1)
			} else {
				fail.Add(1)
			}
		},
	}, &failed, nil)
	assert.Greater(t, ok.Load(), int64(0))
	assert.Greater(t, fail.Load(), int64(0))
}

func TestExitCodeForFull_LivenessWinsOverEverything(t *testing.T) {
	cases := []struct {
		name                 string
		saturation, liveness bool
		sent, errs           int
		wantCode             int
	}{
		{"clean", false, false, 1000, 0, 0},
		{"errors", false, false, 1000, 100, 1},
		{"saturation", true, false, 1000, 0, 2},
		{"liveness", false, true, 1000, 0, 3},
		{"liveness beats saturation", true, true, 1000, 0, 3},
		{"liveness beats errors", false, true, 1000, 100, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := exitCodeForFull(tc.saturation, tc.liveness, tc.sent, tc.errs)
			assert.Equal(t, tc.wantCode, got)
		})
	}
}
