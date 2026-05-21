//go:build integration

package testutil

import (
	"os"
	"testing"
)

// RunTests runs m.Run, terminates shared containers, and exits.
// Usage: func TestMain(m *testing.M) { testutil.RunTests(m) }
func RunTests(m *testing.M) {
	code := m.Run()
	TerminateAll()
	os.Exit(code)
}
