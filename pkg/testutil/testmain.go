//go:build integration

package testutil

import (
	"os"
	"testing"
)

// RunTests is the canonical TestMain body for any package that uses
// shared testcontainers from this package. It runs the test binary,
// terminates every container started via testutil, and exits with the
// right code.
//
//	func TestMain(m *testing.M) { testutil.RunTests(m) }
func RunTests(m *testing.M) {
	code := m.Run()
	TerminateAll()
	os.Exit(code)
}
