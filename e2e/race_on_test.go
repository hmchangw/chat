//go:build e2e && race

package e2e

// raceDetector is true when `go test -race` is on. The race runtime adds
// ~5-10x to wall-clock latency on Keycloak-bound goroutines, so tests that
// assert latency budgets multiply their thresholds by a factor when this
// is set.
const raceDetector = true
