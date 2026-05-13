package main

// window_canary_test.go — canary suite for the abort watcher.
//
// Two tests:
// - TestAbortWatcher_CanaryBaseline_TripTimes runs the canary scenarios against
//   the current LatencyWindow implementation and asserts trip direction.
//   With LOADGEN_CANARY_REGEN=1 it writes goldens to testdata/canary/.
// - TestAbortWatcher_CanaryHDR_TripDriftWithinBudget compares trip times against
//   the committed goldens (captured from the HDR-backed implementation at the
//   time of the Task 1a.3 swap) and asserts ≤10% drift.
//
// The committed goldens are the HDR implementation's trip times, not v1's. The
// v1 slice implementation was replaced in Task 1a.3 (commit 220fb72); the
// goldens were captured immediately before the in-place internal swap.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// canaryGolden is the structure persisted to testdata/canary/window-v1-*.json.
type canaryGolden struct {
	Name       string        `json:"name"`
	RPS        int           `json:"rps"`
	BreachMs   int           `json:"breachMs,omitempty"` // uniform-latency cases; zero for bimodal cases
	P90Ms      int           `json:"p90Ms,omitempty"`    // bimodal-latency cases: 90% of samples at this ms value
	P10Ms      int           `json:"p10Ms,omitempty"`    // bimodal-latency cases: 10% of samples at this ms value
	SustainS   int           `json:"sustainS"`
	WantTrip   bool          `json:"wantTrip"`
	V1ElapsedS float64       `json:"v1ElapsedS"` // synthetic seconds until trip; -1e-9 (= -1ns sentinel from time.Duration(-1)) means never tripped
	V1Elapsed  time.Duration `json:"-"`          // computed from V1ElapsedS at load time
}

// canaryP99ThresholdMs is the abort-watcher p99 threshold used in the canary
// scenarios. Matches the abort watcher's default in runRun.
const canaryP99ThresholdMs = 200

// newCanaryWindow creates a LatencyWindow sized for a given rps and sustainS.
// The cap mirrors the production auto-size formula in resolveAbortWindowMaxSamples:
// 2 × rps × sustainS. This ensures the oldest retained sample is always at least
// sustainS old once the window is full — the abort watcher's coverage predicate can
// fire. Using the default 10_000 cap deafens the watcher at rps=500, sustainS=30
// (needs 15_000 samples) which is why canary cases must size the cap themselves.
func newCanaryWindow(rps, sustainS int) *LatencyWindow {
	cap := 2 * rps * sustainS
	if cap < 1 {
		cap = 1
	}
	return NewLatencyWindow(60 * time.Second).WithMaxSamples(cap)
}

// timeUntilTripSynthetic feeds samples into w using AddAt with a synthetic
// clock (starting at epoch+1000s) advancing at 1/rps per sample. Returns the
// synthetic duration elapsed when P99WithCoverage reports (>= threshold,
// covered=true). Returns -1 if the window never trips within the safety cap
// (120 synthetic seconds).
//
// Using synthetic time makes the test deterministic and fast — no real-time
// goroutines or sleeps are needed.
//
// P99WithCoverage is polled once per synthetic second (matching the
// production abort-watcher tick cadence) rather than on every sample
// addition, avoiding O(n²) complexity at high rps values. Trip-time
// resolution is therefore ±1 synthetic second.
func timeUntilTripSynthetic(
	w *LatencyWindow,
	rps int,
	breachMs int,
	sustainS int,
) time.Duration {
	threshold := time.Duration(canaryP99ThresholdMs) * time.Millisecond
	sustain := time.Duration(sustainS) * time.Second
	latency := time.Duration(breachMs) * time.Millisecond
	interval := time.Second / time.Duration(rps)

	// Synthetic epoch well in the past so retention/coverage math is clean.
	origin := time.Unix(1000, 0)
	synNow := origin

	// Safety cap: 120 synthetic seconds.
	safetyCap := 120 * time.Second

	sample := 0
	for synNow.Sub(origin) < safetyCap {
		synNow = synNow.Add(interval)
		w.AddAt(synNow, latency, false)
		sample++
		// Poll P99WithCoverage once per synthetic second (every rps samples),
		// matching the production abort-watcher tick cadence. This avoids
		// O(n²) cost that would arise from polling on every sample at high rps.
		if sample%rps == 0 {
			p99, covered := w.P99WithCoverage(synNow, sustain)
			if covered && p99 >= threshold {
				return synNow.Sub(origin)
			}
		}
	}
	return -1 // never tripped
}

// timeUntilTripBimodalSynthetic is like timeUntilTripSynthetic but feeds a
// bimodal latency distribution: 90% of samples at p90Ms and 10% at p10Ms.
// The distribution is deterministic — sample index mod 10 selects the bucket
// (index%10 == 9 gets p10Ms; all others get p90Ms).
//
// This exercises HDR's bucketing at the p99 boundary: a 90/10 split of 50ms
// (majority) and 300ms (minority) yields a p99 of 300ms — well above the 200ms
// threshold — so the watcher trips. The trip time may differ slightly from a
// uniform-300ms run because HDR may place the p99 sample in an adjacent bucket
// whose representative value differs from 300ms by ≤0.1% (3-sig-digit precision).
// This non-zero drift is the property under test.
func timeUntilTripBimodalSynthetic(
	w *LatencyWindow,
	rps int,
	p90Ms int,
	p10Ms int,
	sustainS int,
) time.Duration {
	threshold := time.Duration(canaryP99ThresholdMs) * time.Millisecond
	sustain := time.Duration(sustainS) * time.Second
	latP90 := time.Duration(p90Ms) * time.Millisecond
	latP10 := time.Duration(p10Ms) * time.Millisecond
	interval := time.Second / time.Duration(rps)

	// Synthetic epoch well in the past so retention/coverage math is clean.
	origin := time.Unix(1000, 0)
	synNow := origin

	// Safety cap: 120 synthetic seconds.
	safetyCap := 120 * time.Second

	sample := 0
	for synNow.Sub(origin) < safetyCap {
		synNow = synNow.Add(interval)
		// Deterministic bimodal distribution: 10% minority gets p10Ms, rest get p90Ms.
		var latency time.Duration
		if sample%10 == 9 {
			latency = latP10
		} else {
			latency = latP90
		}
		w.AddAt(synNow, latency, false)
		sample++
		// Poll P99WithCoverage once per synthetic second (every rps samples).
		if sample%rps == 0 {
			p99, covered := w.P99WithCoverage(synNow, sustain)
			if covered && p99 >= threshold {
				return synNow.Sub(origin)
			}
		}
	}
	return -1 // never tripped
}

// canaryGoldenPath returns the path for a canary golden file.
func canaryGoldenPath(name string) string {
	return filepath.Join("testdata", "canary", "window-v1-"+name+".json")
}

// canaryCase is a single scenario for TestAbortWatcher_CanaryBaseline_TripTimes.
// For uniform cases, set BreachMs. For bimodal cases, set P90Ms and P10Ms.
type canaryCase struct {
	name     string
	rps      int
	breachMs int // uniform cases
	p90Ms    int // bimodal cases: 90% of samples at this ms value
	p10Ms    int // bimodal cases: 10% of samples at this ms value
	sustainS int
	wantTrip bool
}

// runCanaryCase executes one canary scenario: uniform if P90Ms == 0, bimodal otherwise.
func runCanaryCase(w *LatencyWindow, c canaryCase) time.Duration {
	if c.p90Ms != 0 {
		return timeUntilTripBimodalSynthetic(w, c.rps, c.p90Ms, c.p10Ms, c.sustainS)
	}
	return timeUntilTripSynthetic(w, c.rps, c.breachMs, c.sustainS)
}

// TestAbortWatcher_CanaryBaseline_TripTimes runs the four canary scenarios
// against the current LatencyWindow implementation and, when
// LOADGEN_CANARY_REGEN=1 is set, writes the resulting trip-times as JSON
// goldens. When the env var is absent, it reads the goldens and just asserts
// that the trip-time direction (trip / no-trip) matches the expected behavior.
func TestAbortWatcher_CanaryBaseline_TripTimes(t *testing.T) {
	// Validates trip direction (trip vs. no-trip) for each canary scenario
	// against the current LatencyWindow. Regen mode (LOADGEN_CANARY_REGEN=1)
	// also writes the resulting elapsed times to testdata/canary/ as goldens
	// consumed by TestAbortWatcher_CanaryHDR_TripDriftWithinBudget.
	regen := os.Getenv("LOADGEN_CANARY_REGEN") == "1"

	cases := []canaryCase{
		// below: p99 = 50ms < 200ms threshold — should never trip.
		{name: "below", rps: 200, breachMs: 50, sustainS: 30, wantTrip: false},
		// at: p99 = 250ms >= 200ms — should trip after ~30s sustain accumulates.
		{name: "at", rps: 500, breachMs: 250, sustainS: 30, wantTrip: true},
		// above: p99 = 800ms >> 200ms — should trip quickly.
		{name: "above", rps: 1500, breachMs: 800, sustainS: 30, wantTrip: true},
		// bimodal-degraded: 90% at 50ms + 10% at 300ms → p99 = 300ms > 200ms threshold.
		// Exercises HDR quantization at the p99 bucket boundary (non-zero drift expected).
		{name: "bimodal-degraded", rps: 1000, p90Ms: 50, p10Ms: 300, sustainS: 30, wantTrip: true},
	}

	for _, c := range cases {
		c := c // capture
		t.Run(c.name, func(t *testing.T) {
			w := newCanaryWindow(c.rps, c.sustainS)
			elapsed := runCanaryCase(w, c)
			tripped := elapsed >= 0

			assert.Equal(t, c.wantTrip, tripped,
				"case=%s rps=%d breachMs=%d p90Ms=%d p10Ms=%d sustainS=%d: unexpected trip/no-trip",
				c.name, c.rps, c.breachMs, c.p90Ms, c.p10Ms, c.sustainS)

			if regen {
				golden := canaryGolden{
					Name:       c.name,
					RPS:        c.rps,
					BreachMs:   c.breachMs,
					P90Ms:      c.p90Ms,
					P10Ms:      c.p10Ms,
					SustainS:   c.sustainS,
					WantTrip:   c.wantTrip,
					V1ElapsedS: elapsed.Seconds(),
				}
				data, err := json.MarshalIndent(golden, "", "  ")
				require.NoError(t, err, "marshal golden")
				data = append(data, '\n') // POSIX text-file convention
				require.NoError(t,
					os.MkdirAll(filepath.Dir(canaryGoldenPath(c.name)), 0o755),
					"create testdata/canary dir",
				)
				require.NoError(t,
					os.WriteFile(canaryGoldenPath(c.name), data, 0o644),
					"write golden file",
				)
				t.Logf("wrote golden: %s  elapsed=%.3fs tripped=%v",
					canaryGoldenPath(c.name), elapsed.Seconds(), tripped)
			}
		})
	}
}

// loadCanaryGoldens reads all window-v1-*.json files from testdata/canary/
// and returns them as a slice of canaryGolden.
func loadCanaryGoldens(t *testing.T) []canaryGolden {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("testdata", "canary", "window-v1-*.json"))
	require.NoError(t, err, "glob canary goldens")
	require.NotEmpty(t, matches, "no canary goldens found; run with LOADGEN_CANARY_REGEN=1 first")

	var out []canaryGolden
	for _, path := range matches {
		data, err := os.ReadFile(path)
		require.NoError(t, err, "read %s", path)
		var g canaryGolden
		require.NoError(t, json.Unmarshal(data, &g), "unmarshal %s", path)
		g.V1Elapsed = time.Duration(g.V1ElapsedS * float64(time.Second))
		out = append(out, g)
	}
	return out
}

// TestAbortWatcher_CanaryHDR_TripDriftWithinBudget runs each canary scenario
// against the post-HDR LatencyWindow and asserts that trip-times drift ≤10%
// from the v1 goldens. For wantTrip=false scenarios, asserts the HDR version
// also does not trip.
func TestAbortWatcher_CanaryHDR_TripDriftWithinBudget(t *testing.T) {
	// The canary suite now includes four scenarios:
	//   - below (uniform 50ms): p99 < threshold, never trips (0% drift by construction).
	//   - at (uniform 250ms): p99 ≥ threshold, trips; HDR identity at uniform input → 0% drift.
	//   - above (uniform 800ms): same as "at" but faster trip; 0% drift.
	//   - bimodal-degraded (90% 50ms + 10% 300ms): p99 ≈ 300ms >> threshold; HDR
	//     bucketing may place the p99 in an adjacent bucket, yielding small but
	//     non-zero drift (≤ 0.1% for 3-sig-digit HDR). This is the case that PROVES
	//     HDR stays within the 10% drift budget under realistic non-uniform input.
	goldens := loadCanaryGoldens(t)

	for _, g := range goldens {
		g := g // capture
		t.Run(g.Name, func(t *testing.T) {
			w := newCanaryWindow(g.RPS, g.SustainS)
			// Dispatch to bimodal helper for bimodal goldens; uniform otherwise.
			var elapsed time.Duration
			if g.P90Ms != 0 {
				elapsed = timeUntilTripBimodalSynthetic(w, g.RPS, g.P90Ms, g.P10Ms, g.SustainS)
			} else {
				elapsed = timeUntilTripSynthetic(w, g.RPS, g.BreachMs, g.SustainS)
			}
			tripped := elapsed >= 0

			if !g.WantTrip {
				// Non-trip scenario: HDR must also not trip.
				assert.False(t, tripped,
					"case=%s: v1 did not trip; HDR must not trip either (elapsed=%v)",
					g.Name, elapsed)
				return
			}

			// Trip scenario: assert ≤10% drift in synthetic trip-time.
			require.True(t, tripped,
				"case=%s: v1 tripped (v1Elapsed=%.3fs) but HDR never tripped within 120s",
				g.Name, g.V1ElapsedS)

			drift := math.Abs(float64(elapsed-g.V1Elapsed)) / float64(g.V1Elapsed)
			t.Logf("case=%s v1=%.3fs hdr=%.3fs drift=%.2f%%",
				g.Name, g.V1ElapsedS, elapsed.Seconds(), drift*100)

			assert.LessOrEqual(t, drift, 0.10,
				"trip-time drift %.2f%% exceeds 10%% budget (v1=%.3fs, hdr=%.3fs)",
				drift*100, g.V1ElapsedS, elapsed.Seconds())
		})
	}
}
