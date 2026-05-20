//go:build integration

package roomkeysender_test

// Import testutil for the Ryuk-disable init() side effect. TerminateAll
// is called even though this package starts its containers per-test
// (their t.Cleanups already handle teardown); TerminateAll is a no-op
// when no shared testutil containers were started.

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
