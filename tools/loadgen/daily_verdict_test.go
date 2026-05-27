package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvaluateStep_AllGreen(t *testing.T) {
	s := stepInputs{
		N: 1000, HoldDuration: 180 * time.Second,
		LatencySamples: []float64{10, 20, 50, 100, 200},
		AttemptedOps:   10000, FailedOps: 0,
		ConsumerPending: map[string]ConsumerPendingDelta{
			"message-worker":   {Start: 100, End: 110, Delta: 10},
			"broadcast-worker": {Start: 50, End: 55, Delta: 5},
		},
		ServiceErrors: map[string]int64{},
		Self:          SelfMetrics{GCPauseP99Ms: 5, CPUPercent: 40, Goroutines: 50000},
	}
	r := evaluateStep(s, defaultThresholds())
	require.False(t, r.Tripped)
	require.False(t, r.Inconclusive)
	require.Empty(t, r.TrippedReasons)
}

func TestEvaluateStep_TripsOnPendingGrowth(t *testing.T) {
	s := stepInputs{
		N: 5000, HoldDuration: 180 * time.Second,
		LatencySamples: []float64{10, 20},
		AttemptedOps:   1000,
		ConsumerPending: map[string]ConsumerPendingDelta{
			"broadcast-worker": {Start: 100, End: 2000, Delta: 1900},
		},
	}
	r := evaluateStep(s, defaultThresholds())
	require.True(t, r.Tripped)
	require.Contains(t, r.TrippedReasons[0], "broadcast-worker")
}

func TestEvaluateStep_TripsOnP95Latency(t *testing.T) {
	// Half the samples are elevated above the 500ms threshold so the p95
	// index (94 of 100 sorted ascending) lands in the elevated region.
	samples := make([]float64, 100)
	for i := 0; i < 50; i++ {
		samples[i] = 200
	}
	for i := 50; i < 100; i++ {
		samples[i] = 600
	}
	s := stepInputs{
		N: 5000, HoldDuration: 180 * time.Second,
		LatencySamples: samples, AttemptedOps: 1000,
	}
	r := evaluateStep(s, defaultThresholds())
	require.True(t, r.Tripped)
	require.Contains(t, r.TrippedReasons[0], "p95")
}

func TestEvaluateStep_InconclusiveOnHighGC(t *testing.T) {
	s := stepInputs{
		N: 20000, HoldDuration: 180 * time.Second,
		LatencySamples: []float64{10},
		AttemptedOps:   1000,
		Self:           SelfMetrics{GCPauseP99Ms: 80, CPUPercent: 90, Goroutines: 100000},
	}
	r := evaluateStep(s, defaultThresholds())
	require.True(t, r.Inconclusive)
	require.False(t, r.Tripped) // inconclusive overrides trip
}

func TestEvaluateStep_TripsOnErrorRate(t *testing.T) {
	s := stepInputs{
		N: 5000, HoldDuration: 180 * time.Second,
		LatencySamples: []float64{10},
		AttemptedOps:   10000, FailedOps: 50, // 0.5% > 0.1%
	}
	r := evaluateStep(s, defaultThresholds())
	require.True(t, r.Tripped)
	require.Contains(t, r.TrippedReasons[0], "error_rate")
}
