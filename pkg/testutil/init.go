//go:build integration

package testutil

import "os"

// init disables testcontainers-go's Ryuk reaper repo-wide because it
// fails to start on our CI runner. Cleanup is handled by TerminateAll.
// LookupEnv guard lets local debugging flip Ryuk back on without an
// edit: `TESTCONTAINERS_RYUK_DISABLED=false go test ...`.
func init() {
	if _, set := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !set {
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
}
