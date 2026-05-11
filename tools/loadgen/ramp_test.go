package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRamp_LinearRateAt(t *testing.T) {
	r := Ramp{From: 10, To: 100, Duration: 10 * time.Second, Shape: RampLinear}
	assert.Equal(t, 10, r.RateAt(0))
	assert.Equal(t, 55, r.RateAt(5*time.Second))
	assert.Equal(t, 100, r.RateAt(10*time.Second))
}

func TestRamp_ExponentialRateAt(t *testing.T) {
	r := Ramp{From: 10, To: 1000, Duration: 10 * time.Second, Shape: RampExponential}
	// At t=0 → From
	assert.Equal(t, 10, r.RateAt(0))
	// At t=Duration → To
	assert.Equal(t, 1000, r.RateAt(10*time.Second))
	// Monotonic non-decreasing.
	prev := 0
	for s := 0; s <= 10; s++ {
		got := r.RateAt(time.Duration(s) * time.Second)
		assert.GreaterOrEqual(t, got, prev, "RateAt(%ds) regressed: %d < %d", s, got, prev)
		prev = got
	}
}

func TestRamp_RateAtClampsBeyondDuration(t *testing.T) {
	r := Ramp{From: 10, To: 100, Duration: 5 * time.Second, Shape: RampLinear}
	assert.Equal(t, 100, r.RateAt(10*time.Second), "past duration should clamp to To")
	assert.Equal(t, 100, r.RateAt(60*time.Second))
}

func TestRamp_BeforeStartClampsToFrom(t *testing.T) {
	r := Ramp{From: 10, To: 100, Duration: 5 * time.Second, Shape: RampLinear}
	// Negative durations are not real; but RateAt(0) is From.
	assert.Equal(t, 10, r.RateAt(0))
}

func TestRamp_RejectsBothFixedAndRamp(t *testing.T) {
	// Validator: Generator's config-validate rejects both --rate (>0) and a
	// configured Ramp. Tested at the validation layer rather than the
	// Generator constructor since main.go owns the policy.
	err := validateRampVsRate(500, &Ramp{From: 10, To: 100, Duration: 5 * time.Second, Shape: RampLinear})
	assert.Error(t, err, "rate=500 + configured Ramp should be rejected")

	err = validateRampVsRate(500, nil)
	assert.NoError(t, err, "fixed-rate-only should validate")

	err = validateRampVsRate(0, &Ramp{From: 10, To: 100, Duration: 5 * time.Second, Shape: RampLinear})
	assert.NoError(t, err, "ramp-only (rate=0) should validate")
}

func TestRamp_LinearRampDown(t *testing.T) {
	// From > To: rate must monotonically decrease.
	r := Ramp{From: 1000, To: 10, Duration: 10 * time.Second, Shape: RampLinear}
	assert.Equal(t, 1000, r.RateAt(0))
	assert.Equal(t, 10, r.RateAt(10*time.Second))
	prev := 1001
	for s := 0; s <= 10; s++ {
		got := r.RateAt(time.Duration(s) * time.Second)
		assert.LessOrEqual(t, got, prev, "linear ramp-down regressed at %ds: %d > %d", s, got, prev)
		prev = got
	}
}

func TestTickInterval_EdgeCases(t *testing.T) {
	// rate <= 0 → 1s fallback so the ticker doesn't spin.
	assert.Equal(t, time.Second, tickInterval(0))
	assert.Equal(t, time.Second, tickInterval(-1))
	// rate = 1 → exactly 1s.
	assert.Equal(t, time.Second, tickInterval(1))
	// rate at extreme: clamped to minTickInterval (1µs).
	assert.Equal(t, minTickInterval, tickInterval(2_000_000_000))
	// Mid-range: 1000 rps = 1ms.
	assert.Equal(t, time.Millisecond, tickInterval(1000))
}
