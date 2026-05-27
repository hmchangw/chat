package main

import (
	"fmt"
	"sort"
	"time"
)

// ConsumerPendingDelta captures a single durable's pending-message count
// at the start and end of a hold window.
type ConsumerPendingDelta struct {
	Start int64
	End   int64
	Delta int64
}

// SelfMetrics describes the loadgen process's own resource state during
// the hold window. High values mean the load box is the bottleneck and
// the step is INCONCLUSIVE rather than PASS/TRIP.
type SelfMetrics struct {
	GCPauseP99Ms float64
	CPUPercent   float64
	Goroutines   int
}

// Thresholds are the per-signal cutoffs that decide PASS / TRIP / INCONCLUSIVE.
type Thresholds struct {
	P95LatencyMs        float64
	P99LatencyMs        float64
	ErrorRate           float64 // fraction (0.001 = 0.1%)
	PendingGrowth       int64
	GCPauseInconclusive float64
	CPUInconclusive     float64
}

func defaultThresholds() Thresholds {
	return Thresholds{
		P95LatencyMs: 500, P99LatencyMs: 1000,
		ErrorRate: 0.001, PendingGrowth: 1000,
		GCPauseInconclusive: 50, CPUInconclusive: 80,
	}
}

// stepInputs is everything evaluateStep needs to produce a verdict.
type stepInputs struct {
	N               int
	StartedAt       time.Time
	HoldDuration    time.Duration
	LatencySamples  []float64 // milliseconds
	AttemptedOps    int64
	FailedOps       int64
	ConsumerPending map[string]ConsumerPendingDelta
	ServiceErrors   map[string]int64
	Self            SelfMetrics
}

// StepResult is the verdict for a single ramp step.
type StepResult struct {
	N                     int
	StartedAt             time.Time
	HoldDuration          time.Duration
	P50LatencyMs          float64
	P95LatencyMs          float64
	P99LatencyMs          float64
	ErrorRate             float64
	AttemptedOps          int64
	FailedOps             int64
	ConsumerPending       map[string]ConsumerPendingDelta
	ServiceErrorIncreases map[string]int64
	LoadgenSelfMetrics    SelfMetrics
	Tripped               bool
	Inconclusive          bool
	TrippedReasons        []string
}

func percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	cp := make([]float64, len(samples))
	copy(cp, samples)
	sort.Float64s(cp)
	idx := int(p * float64(len(cp)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

//nolint:gocritic // hugeParam: pure-function signature is intentional; the per-step copy cost is negligible.
func evaluateStep(in stepInputs, th Thresholds) StepResult {
	r := StepResult{
		N: in.N, StartedAt: in.StartedAt, HoldDuration: in.HoldDuration,
		AttemptedOps: in.AttemptedOps, FailedOps: in.FailedOps,
		ConsumerPending:       in.ConsumerPending,
		ServiceErrorIncreases: in.ServiceErrors,
		LoadgenSelfMetrics:    in.Self,
		P50LatencyMs:          percentile(in.LatencySamples, 0.50),
		P95LatencyMs:          percentile(in.LatencySamples, 0.95),
		P99LatencyMs:          percentile(in.LatencySamples, 0.99),
	}
	if in.AttemptedOps > 0 {
		r.ErrorRate = float64(in.FailedOps) / float64(in.AttemptedOps)
	}

	// Inconclusive overrides trip.
	if in.Self.GCPauseP99Ms > th.GCPauseInconclusive || in.Self.CPUPercent > th.CPUInconclusive {
		r.Inconclusive = true
		r.TrippedReasons = append(r.TrippedReasons,
			fmt.Sprintf("inconclusive: gc=%.1fms cpu=%.1f%%", in.Self.GCPauseP99Ms, in.Self.CPUPercent))
		return r
	}

	if r.P95LatencyMs > th.P95LatencyMs {
		r.Tripped = true
		r.TrippedReasons = append(r.TrippedReasons,
			fmt.Sprintf("p95=%.0fms > %.0f", r.P95LatencyMs, th.P95LatencyMs))
	}
	if r.P99LatencyMs > th.P99LatencyMs {
		r.Tripped = true
		r.TrippedReasons = append(r.TrippedReasons,
			fmt.Sprintf("p99=%.0fms > %.0f", r.P99LatencyMs, th.P99LatencyMs))
	}
	if r.ErrorRate > th.ErrorRate {
		r.Tripped = true
		r.TrippedReasons = append(r.TrippedReasons,
			fmt.Sprintf("error_rate=%.4f > %.4f", r.ErrorRate, th.ErrorRate))
	}
	for durable, d := range in.ConsumerPending {
		if d.Delta > th.PendingGrowth {
			r.Tripped = true
			r.TrippedReasons = append(r.TrippedReasons,
				fmt.Sprintf("%s pending +%d > +%d", durable, d.Delta, th.PendingGrowth))
		}
	}
	for svc, n := range in.ServiceErrors {
		if n > 0 {
			r.Tripped = true
			r.TrippedReasons = append(r.TrippedReasons,
				fmt.Sprintf("%s errors +%d", svc, n))
		}
	}
	return r
}
