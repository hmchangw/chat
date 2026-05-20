//go:build integration

package testutil

import "os"

// init disables testcontainers-go's Ryuk reaper across every integration
// test in this repo. Ryuk fails to start on our CI runner (can't pull /
// run the sidecar image), and Ryuk-on-by-default would block every job.
// Cleanup is handled explicitly via TerminateAll, which each service's
// TestMain calls.
//
// LookupEnv guard so a developer debugging container leaks can flip Ryuk
// back on with `TESTCONTAINERS_RYUK_DISABLED=false go test ...` without
// editing code.
func init() {
	if _, set := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !set {
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
}
