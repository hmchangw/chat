package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func presenceTestThresholds() presenceThresholds {
	return presenceThresholds{P95Ms: 200, P99Ms: 500, ErrorRate: 0.01, GCPauseInconclusive: 50}
}

func TestEvaluatePresenceStep_Pass(t *testing.T) {
	in := presenceStepInputs{
		N: 1000, EffectiveN: 1000,
		LatencySamples: []float64{10, 20, 30, 40, 50},
		Attempted:      500, Failed: 0,
	}
	r := evaluatePresenceStep(in, presenceTestThresholds())
	assert.Equal(t, verdictPass, r.Kind)
}

func TestEvaluatePresenceStep_TripLatency(t *testing.T) {
	samples := make([]float64, 100)
	for i := range samples {
		samples[i] = 600 // all over p99 cap
	}
	in := presenceStepInputs{N: 1000, EffectiveN: 1000, LatencySamples: samples, Attempted: 100}
	r := evaluatePresenceStep(in, presenceTestThresholds())
	assert.Equal(t, verdictTrip, r.Kind)
	assert.NotEmpty(t, r.Reasons)
}

func TestEvaluatePresenceStep_TripErrorRate(t *testing.T) {
	in := presenceStepInputs{
		N: 1000, EffectiveN: 1000,
		LatencySamples: []float64{10}, Attempted: 100, Failed: 5, // 5% > 1%
	}
	r := evaluatePresenceStep(in, presenceTestThresholds())
	assert.Equal(t, verdictTrip, r.Kind)
}

func TestEvaluatePresenceStep_InconclusiveGC(t *testing.T) {
	in := presenceStepInputs{
		N: 1000, EffectiveN: 1000, LatencySamples: []float64{10}, Attempted: 100,
		Self: SelfMetrics{GCPauseP99Ms: 80},
	}
	r := evaluatePresenceStep(in, presenceTestThresholds())
	assert.Equal(t, verdictInconclusive, r.Kind)
}

func TestEvaluatePresenceStep_InconclusiveZeroAttempts(t *testing.T) {
	in := presenceStepInputs{N: 1000, EffectiveN: 1000, Attempted: 0}
	r := evaluatePresenceStep(in, presenceTestThresholds())
	assert.Equal(t, verdictInconclusive, r.Kind)
}

func TestEvaluatePresenceStep_InconclusiveActivationShortfall(t *testing.T) {
	in := presenceStepInputs{
		N: 1000, EffectiveN: 800, // 80% < 95%
		LatencySamples: []float64{10}, Attempted: 100,
	}
	r := evaluatePresenceStep(in, presenceTestThresholds())
	assert.Equal(t, verdictInconclusive, r.Kind)
}
