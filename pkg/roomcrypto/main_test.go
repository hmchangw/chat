//go:build integration

package roomcrypto

// Import testutil for the Ryuk-disable init() side effect. This package
// starts its Node container per-test (t.Cleanup handles teardown);
// TerminateAll is a no-op when no shared testutil containers were started.

import (
	"os"
	"testing"

	"github.com/hmchangw/chat/pkg/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.TerminateAll()
	os.Exit(code)
}
