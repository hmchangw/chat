package main

import (
	"errors"
	"math"
	"strconv"
	"time"
)

// RampShape selects the rate curve a Ramp follows from From to To.
type RampShape int

const (
	// RampLinear interpolates rate linearly: rate(t) = From + (To-From) * (t/Duration).
	RampLinear RampShape = iota
	// RampExponential interpolates geometrically: rate(t) = From * (To/From)^(t/Duration).
	// Useful when the linear ramp blasts past the knee in too few samples.
	RampExponential
)

// Ramp specifies a rate curve from `From` rps to `To` rps over `Duration`.
// A zero-value Ramp (From == 0 && To == 0) is "no ramp"; callers should
// branch on cfg.Ramp != nil to decide between the fixed-rate path and
// the ramp path.
type Ramp struct {
	From, To int
	Duration time.Duration
	Shape    RampShape
}

// RateAt returns the rate (rps) at time `t` since the ramp's start.
// Clamped to From at t<=0 and To at t>=Duration so callers can poll the
// curve regardless of how long the run actually took.
func (r *Ramp) RateAt(t time.Duration) int {
	if t <= 0 {
		return r.From
	}
	if t >= r.Duration || r.Duration <= 0 {
		return r.To
	}
	frac := float64(t) / float64(r.Duration)
	switch r.Shape {
	case RampExponential:
		// rate = From * (To/From)^frac. Avoids 0/0 when From==0 by falling
		// back to linear in the degenerate case.
		if r.From <= 0 {
			return int(float64(r.To) * frac)
		}
		ratio := float64(r.To) / float64(r.From)
		return int(math.Round(float64(r.From) * math.Pow(ratio, frac)))
	default: // RampLinear
		return r.From + int(math.Round(float64(r.To-r.From)*frac))
	}
}

// ErrRampAndRateConfigured is returned by validateRampVsRate when the
// caller specifies both a fixed --rate and a Ramp; the rates would
// fight, so the policy layer rejects it.
var ErrRampAndRateConfigured = errors.New("--rate and --ramp-from/--ramp-to cannot both be set")

// validateRampVsRate enforces the "one or the other" rule. Returns nil
// when only one is set or when neither is set (caller may impose its
// own minimum-rate requirement separately).
func validateRampVsRate(rate int, ramp *Ramp) error {
	if rate > 0 && ramp != nil {
		return ErrRampAndRateConfigured
	}
	return nil
}

// rateBucketUpperBounds enumerates the fixed rate-bucket boundaries used
// by the loadgen_published_total{rate_bucket} label. Phase 3 §3.4: 9
// values, intentionally bounded so a ramped run produces a small set of
// label combinations and Grafana can render the rate trajectory.
var rateBucketUpperBounds = []int{10, 50, 100, 200, 500, 1000, 2000, 5000, 10000}

// rateBucketLabel returns the rate-bucket label for a given rps. Buckets
// are inclusive on the upper bound: rps=50 → "10-50", rps=51 → "50-100".
// rps over 10000 → "10000+". rps=0 → "0".
func rateBucketLabel(rps int) string {
	if rps <= 0 {
		return "0"
	}
	prev := 0
	for _, ub := range rateBucketUpperBounds {
		if rps <= ub {
			if prev == 0 {
				return "0-" + strconv.Itoa(ub)
			}
			return strconv.Itoa(prev) + "-" + strconv.Itoa(ub)
		}
		prev = ub
	}
	return "10000+"
}
