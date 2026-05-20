//go:build integration

package roomkeysender_test

// Import testutil for the Ryuk-disable init() side effect even though
// this package starts its containers per-test (t.Cleanup handles teardown).

import (
	"testing"

	"github.com/hmchangw/chat/pkg/testutil"
)

func TestMain(m *testing.M) { testutil.RunTests(m) }
