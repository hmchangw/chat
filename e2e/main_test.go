//go:build e2e

package e2e

import (
	"os"
	"testing"
)

// TestMain owns the e2e stack lifecycle. Chapter 7 fills it in with
// harness.StartStack(...) + stack.Stop(...). Until then it just runs the
// (empty) test set so `go test -tags e2e ./e2e/...` compiles and exits 0.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
