//go:build e2e

package harness

import (
	"strings"
	"testing"

	"github.com/hmchangw/chat/pkg/idgen"
)

// TestIDs holds per-test resource identifiers minted from idgen so each
// test's data is isolated within the shared stack. Test name is embedded
// for greppability of log lines back to the originating test.
//
// Per amendment R1 10.E: RoomID is NOT pre-minted. Channel rooms are
// created by room-service which assigns the ID server-side and returns it
// in CreateRoomReply.RoomID; tests read that value and thread it through
// subsequent calls. DM rooms use a deterministic idgen.BuildDMRoomID
// derived from the two participants' user IDs at the call site.
type TestIDs struct {
	TestName  string
	MessageID string
}

// NewTestIDs mints fresh per-test IDs. The MessageID is generated client-
// side because SendMessageRequest.ID is required and the test owns its
// content's identity.
func NewTestIDs(t *testing.T) TestIDs {
	t.Helper()
	return TestIDs{
		TestName:  t.Name(),
		MessageID: idgen.GenerateMessageID(),
	}
}

// SafeSiteID returns a docker-network-safe variant of the test name for
// rare cases where a test wants its own per-test "site identity" (e.g. a
// federation edge case spinning up a synthetic third site). Most tests use
// the existing stack siteIDs (siteA / siteB) directly.
func SafeSiteID(t *testing.T, prefix string) string {
	t.Helper()
	clean := strings.NewReplacer("/", "-", " ", "-", ".", "-").Replace(t.Name())
	return prefix + "-" + clean
}
