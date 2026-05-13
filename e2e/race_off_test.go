//go:build e2e && !race

package e2e

// raceDetector is false when -race is NOT set. Used by auth_load_test to pick
// the calibrated production-style latency thresholds.
const raceDetector = false
