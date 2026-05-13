package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRunQuality_TrustedHappyPath(t *testing.T) {
	rq := &RunQualityInputs{
		DrainTimedOut:        false,
		MeasuredDuration:     60 * time.Second,
		AbortP99Sustain:      30 * time.Second,
		WarmupErrorRate:      0.02,
		SettleOutcome:        SettleOutcome{AllSucceeded: true},
		OmissionP99Serviced:  5 * time.Millisecond,
		MeasuredP99:          80 * time.Millisecond,
		AbortWatcherDeafened: false,
		LivenessFailures:     0,
	}
	v := EvaluateRunQuality(rq)
	assert.Equal(t, "TRUSTED", v.Verdict)
	assert.Empty(t, v.Issues)
}

func TestRunQuality_DegradedHighOmission(t *testing.T) {
	rq := baseGoodInputs()
	rq.OmissionP99Serviced = 30 * time.Millisecond // 37.5% of 80ms p99
	v := EvaluateRunQuality(rq)
	assert.Equal(t, "DEGRADED", v.Verdict)
	assert.Contains(t, strings.Join(v.Issues, " "), "omission p99")
}

func TestRunQuality_UntrustedDrainTimeout(t *testing.T) {
	rq := baseGoodInputs()
	rq.DrainTimedOut = true
	v := EvaluateRunQuality(rq)
	assert.Equal(t, "UNTRUSTED", v.Verdict)
}

func TestRunQuality_UntrustedShortMeasured(t *testing.T) {
	rq := baseGoodInputs()
	rq.MeasuredDuration = 20 * time.Second
	rq.AbortP99Sustain = 30 * time.Second
	v := EvaluateRunQuality(rq)
	assert.Equal(t, "UNTRUSTED", v.Verdict)
}

func TestRunQuality_UntrustedWarmupErrorHigh(t *testing.T) {
	rq := baseGoodInputs()
	rq.WarmupErrorRate = 0.30
	v := EvaluateRunQuality(rq)
	assert.Equal(t, "UNTRUSTED", v.Verdict)
}

func TestRunQuality_DegradedAbortDeafenedPartial(t *testing.T) {
	rq := baseGoodInputs()
	rq.AbortWatcherDeafened = true
	rq.PeakRPSTimesSustain = 12000
	rq.AbortWindowCap = 10000 // ratio 1.2 <= 2.0
	v := EvaluateRunQuality(rq)
	assert.Equal(t, "DEGRADED", v.Verdict)
}

func TestRunQuality_UntrustedAbortDeafenedSevere(t *testing.T) {
	rq := baseGoodInputs()
	rq.AbortWatcherDeafened = true
	rq.PeakRPSTimesSustain = 30000
	rq.AbortWindowCap = 10000 // ratio 3.0 > 2.0
	v := EvaluateRunQuality(rq)
	assert.Equal(t, "UNTRUSTED", v.Verdict)
}

func TestRunQuality_DegradedLivenessFailures(t *testing.T) {
	rq := baseGoodInputs()
	rq.LivenessFailures = 3
	v := EvaluateRunQuality(rq)
	assert.Equal(t, "DEGRADED", v.Verdict)
	assert.Len(t, v.Issues, 1)
	assert.Contains(t, v.Issues[0], "liveness probe failed 3 times")
}

func TestRunQuality_UntrustedWithConcurrentDegraded_IssuesContainBoth(t *testing.T) {
	rq := baseGoodInputs()
	rq.DrainTimedOut = true // UNTRUSTED
	rq.LivenessFailures = 2 // DEGRADED
	v := EvaluateRunQuality(rq)
	assert.Equal(t, "UNTRUSTED", v.Verdict)
	joined := strings.Join(v.Issues, " ")
	assert.Contains(t, joined, "async drain timed out")
	assert.Contains(t, joined, "liveness probe failed 2 times")
}

func baseGoodInputs() *RunQualityInputs {
	return &RunQualityInputs{
		MeasuredDuration:    60 * time.Second,
		AbortP99Sustain:     30 * time.Second,
		WarmupErrorRate:     0.02,
		SettleOutcome:       SettleOutcome{AllSucceeded: true},
		OmissionP99Serviced: 5 * time.Millisecond,
		MeasuredP99:         80 * time.Millisecond,
	}
}
