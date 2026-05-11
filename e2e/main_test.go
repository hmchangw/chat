//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hmchangw/chat/e2e/harness"
)

// stack is the package-level singleton populated by TestMain. Test files in
// this package read it directly (e.g. `site := stack.SiteA`).
var stack *harness.Stack

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	s, err := harness.Start(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: harness.Start: %v\n", err)
		return 1
	}
	if err := harness.BootstrapFederation(ctx, s); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: BootstrapFederation: %v\n", err)
		_ = s.Stop(context.Background())
		return 1
	}
	stack = s

	code := m.Run()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopCancel()
	if err := s.Stop(stopCtx); err != nil {
		// Don't override a passing-test exit code with a teardown error.
		fmt.Fprintf(os.Stderr, "e2e: stack.Stop: %v\n", err)
	}
	return code
}
