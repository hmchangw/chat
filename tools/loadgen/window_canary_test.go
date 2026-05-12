package main

// window_canary_test.go — abort-watcher canary suite for the LatencyWindow
// HDR migration (Phase 1a §3).
//
// The canary captures v1 (slice-based) trip-times as golden JSON files, then
// after the HDR swap asserts that trip-times drift ≤10% from those goldens.
//
// Usage:
//   # Capture v1 goldens (run once against the slice implementation):
//   LOADGEN_CANARY_REGEN=1 go test -run TestAbortWatcher_CanaryBaseline_TripTimes -v
//
//   # Assert HDR drift ≤10% (run after the swap, without the env var):
//   go test -run TestAbortWatcher_CanaryHDR_TripDriftWithinBudget -v

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
	BreachMs   int           `json:"breachMs"`
	SustainS   int           `json:"sustainS"`
	WantTrip   bool          `json:"wantTrip"`
	V1ElapsedS float64       `json:"v1ElapsedS"` // synthetic seconds until trip; -1 = never tripped
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

// canaryGoldenPath returns the path for a canary golden file.
func canaryGoldenPath(name string) string {
	return filepath.Join("testdata", "canary", "window-v1-"+name+".json")
}

// TestAbortWatcher_CanaryBaseline_TripTimes runs the three canary scenarios
// against the current LatencyWindow implementation and, when
// LOADGEN_CANARY_REGEN=1 is set, writes the resulting trip-times as JSON
// goldens. When the env var is absent, it reads the goldens and just asserts
// that the trip-time direction (trip / no-trip) matches the expected behavior.
func TestAbortWatcher_CanaryBaseline_TripTimes(t *testing.T) {
	regen := os.Getenv("LOADGEN_CANARY_REGEN") == "1"

	cases := []struct {
		name     string
		rps      int
		breachMs int
		sustainS int
		wantTrip bool
	}{
		// below: p99 = 50ms < 200ms threshold — should never trip.
		{"below", 200, 50, 30, false},
		// at: p99 = 250ms >= 200ms — should trip after ~30s sustain accumulates.
		{"at", 500, 250, 30, true},
		// above: p99 = 800ms >> 200ms — should trip quickly.
		{"above", 1500, 800, 30, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newCanaryWindow(c.rps, c.sustainS)
			elapsed := timeUntilTripSynthetic(w, c.rps, c.breachMs, c.sustainS)
			tripped := elapsed >= 0

			assert.Equal(t, c.wantTrip, tripped,
				"case=%s rps=%d breachMs=%d sustainS=%d: unexpected trip/no-trip",
				c.name, c.rps, c.breachMs, c.sustainS)

			if regen {
				golden := canaryGolden{
					Name:       c.name,
					RPS:        c.rps,
					BreachMs:   c.breachMs,
					SustainS:   c.sustainS,
					WantTrip:   c.wantTrip,
					V1ElapsedS: elapsed.Seconds(),
				}
				data, err := json.MarshalIndent(golden, "", "  ")
				require.NoError(t, err, "marshal golden")
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
	goldens := loadCanaryGoldens(t)

	for _, g := range goldens {
		g := g // capture
		t.Run(g.Name, func(t *testing.T) {
			w := newCanaryWindow(g.RPS, g.SustainS)
			elapsed := timeUntilTripSynthetic(w, g.RPS, g.BreachMs, g.SustainS)
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
