//go:build integration

package testutil

import "os"

// init disables testcontainers-go's Ryuk reaper across every integration
// test in this repo. Cleanup is handled explicitly via TerminateAll, which
// each service's TestMain calls.
//
// LookupEnv guard so a developer debugging container leaks can flip Ryuk
// back on with `TESTCONTAINERS_RYUK_DISABLED=false go test ...` without
// editing code.
func init() {
	if _, set := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !set {
		// Best-effort — process-level env mutation can't realistically
		// fail. If it ever did, testcontainers-go would just default to
		// Ryuk-on and CI would surface the original failure mode.
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
}
