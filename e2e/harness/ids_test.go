//go:build e2e

package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewTestIDs_ProducesNonEmptyMessageID(t *testing.T) {
	ids := NewTestIDs(t)
	assert.NotEmpty(t, ids.MessageID)
	assert.Equal(t, t.Name(), ids.TestName)
}

// Confirms amendment R1 10.E: RoomID is NOT pre-minted by the harness;
// tests read it from CreateRoomReply.RoomID returned by room-service.
// If a future refactor restores a pre-mint, this test fails loudly.
func TestNewTestIDs_DoesNotPreMintRoomID(t *testing.T) {
	type harnessIDs struct {
		TestName  string
		MessageID string
	}
	// Compile-time structural check: TestIDs must have exactly these two
	// fields. Adding RoomID back would break this assignment.
	_ = harnessIDs(NewTestIDs(t))
}

func TestNewTestIDs_DistinctAcrossCalls(t *testing.T) {
	a := NewTestIDs(t).MessageID
	b := NewTestIDs(t).MessageID
	assert.NotEqual(t, a, b)
}

func TestSafeSiteID_ReplacesUnsafeRunes(t *testing.T) {
	got := SafeSiteID(t, "syn")
	assert.NotContains(t, got, "/")
	assert.NotContains(t, got, " ")
	assert.NotContains(t, got, ".")
}
