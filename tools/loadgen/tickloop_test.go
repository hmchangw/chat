package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTickLoop_InvokesTickAtRate confirms the shared loop ticks at the
// requested rate and stops on context cancellation.
func TestTickLoop_InvokesTickAtRate(t *testing.T) {
	var ticks atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	tickLoop(ctx, tickLoopConfig{
		Rate:        500,
		MaxInFlight: 0,
		Metrics:     NewMetrics(),
		Preset:      "test",
		Scenario:    "test",
	}, func(ctx context.Context) {
		ticks.Add(1)
	})

	// 500 rps for 100ms ≈ 50 ticks. Wide tolerance for scheduling.
	got := ticks.Load()
	assert.GreaterOrEqual(t, got, int64(15), "got %d", got)
	assert.LessOrEqual(t, got, int64(120), "got %d", got)
}

func TestTickLoop_BoundedPoolSaturation(t *testing.T) {
	// Force pool saturation by blocking each tick longer than the interval.
	m := NewMetrics()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	tickLoop(ctx, tickLoopConfig{
		Rate:        2000,
		MaxInFlight: 1,
		Metrics:     m,
		Preset:      "test",
		Scenario:    "search",
	}, func(ctx context.Context) {
		time.Sleep(20 * time.Millisecond)
	})

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)

	var saturated float64
	for _, mf := range mfs {
		if mf.GetName() != "loadgen_request_errors_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range metric.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["reason"] != "saturated" {
				continue
			}
			// Cleanup C: kind label must be "*", not "saturated".
			assert.Equal(t, "*", labels["kind"],
				"saturation must use kind='*' (queue-level event); got %q", labels["kind"])
			assert.Equal(t, "search", labels["scenario"])
			saturated += metric.GetCounter().GetValue()
		}
	}
	assert.Greater(t, saturated, float64(0), "expected saturation counter to increment")
}

func TestTickLoop_RampOverridesFixedRate(t *testing.T) {
	var ticks atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	// Ramp 100 -> 1000 rps over 200ms. With a fixed rate of 50, the ramped
	// rate should dominate immediately (RateAt(0) = 100, well above 50).
	tickLoop(ctx, tickLoopConfig{
		Rate:        50,
		MaxInFlight: 0,
		Metrics:     NewMetrics(),
		Preset:      "test",
		Scenario:    "test",
		Ramp: &Ramp{
			From: 100, To: 1000,
			Duration: 200 * time.Millisecond,
			Shape:    RampLinear,
		},
	}, func(ctx context.Context) {
		ticks.Add(1)
	})

	got := ticks.Load()
	// At fixed 50rps over 250ms we'd expect ~12 ticks.
	// With ramp 100->1000 over 200ms (avg ~550 over the ramp window)
	// + ~50ms tail at 1000, expect well over 50 ticks.
	assert.GreaterOrEqual(t, got, int64(50),
		"ramp should drive rate above fixed; got %d ticks", got)
}
