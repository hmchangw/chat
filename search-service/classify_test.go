package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyRoomIDs_NilRequestedReturnsNilNil(t *testing.T) {
	unrestricted, restrictedWithFloor := classifyRoomIDs(nil, map[string]int64{
		"rx": 1_700_000_000_000,
	})
	assert.Nil(t, unrestricted, "nil requested → signal 'use all my rooms' via nil unrestricted")
	assert.Nil(t, restrictedWithFloor)
}

func TestClassifyRoomIDs_EmptyRequestedReturnsNilNil(t *testing.T) {
	unrestricted, restrictedWithFloor := classifyRoomIDs([]string{}, map[string]int64{})
	assert.Nil(t, unrestricted)
	assert.Nil(t, restrictedWithFloor)
}

func TestClassifyRoomIDs_AllUnrestricted(t *testing.T) {
	restricted := map[string]int64{"rx": 1_700_000_000_000}
	unrestricted, restrictedWithFloor := classifyRoomIDs([]string{"r1", "r2"}, restricted)

	require.Len(t, unrestricted, 2)
	assert.Contains(t, unrestricted, "r1")
	assert.Contains(t, unrestricted, "r2")
	assert.Empty(t, restrictedWithFloor)
}

func TestClassifyRoomIDs_AllRestricted(t *testing.T) {
	restricted := map[string]int64{
		"rx": 1_700_000_000_000,
		"ry": 1_600_000_000_000,
	}
	unrestricted, restrictedWithFloor := classifyRoomIDs([]string{"rx", "ry"}, restricted)

	assert.Empty(t, unrestricted)
	require.Len(t, restrictedWithFloor, 2)
	assert.Equal(t, int64(1_700_000_000_000), restrictedWithFloor["rx"])
	assert.Equal(t, int64(1_600_000_000_000), restrictedWithFloor["ry"])
}

func TestClassifyRoomIDs_Mixed(t *testing.T) {
	restricted := map[string]int64{"rx": 1_700_000_000_000}
	unrestricted, restrictedWithFloor := classifyRoomIDs([]string{"r1", "rx"}, restricted)

	require.Len(t, unrestricted, 1)
	assert.Equal(t, "r1", unrestricted[0])
	require.Len(t, restrictedWithFloor, 1)
	assert.Equal(t, int64(1_700_000_000_000), restrictedWithFloor["rx"])
}

func TestClassifyRoomIDs_UnknownIDsDropped(t *testing.T) {
	// Security rule: classifyRoomIDs places unrecognised room IDs into the
	// unrestricted bucket. The ES query (scopedAccessClauses) then ANDs the
	// unrestricted bucket against the user-room terms-lookup, which silently
	// drops rooms the caller doesn't belong to. The security guarantee is
	// enforced at the ES layer, not in classifyRoomIDs itself.
	restricted := map[string]int64{"rx": 1_700_000_000_000}
	unrestricted, restrictedWithFloor := classifyRoomIDs(
		[]string{"r1", "unknown-room", "rx"},
		restricted,
	)

	// "r1" and "unknown-room" → both land in unrestricted bucket (ES is the final guard)
	// "rx" → restrictedWithFloor
	require.Len(t, unrestricted, 2, "r1 and unknown-room both land in unrestricted bucket pre-ES")
	require.Len(t, restrictedWithFloor, 1)
	assert.Equal(t, int64(1_700_000_000_000), restrictedWithFloor["rx"])
}

func TestClassifyRoomIDs_EmptyRestrictedMap(t *testing.T) {
	unrestricted, restrictedWithFloor := classifyRoomIDs(
		[]string{"r1", "r2"},
		map[string]int64{},
	)
	require.Len(t, unrestricted, 2)
	assert.Empty(t, restrictedWithFloor)
}

func TestClassifyRoomIDs_NilRestrictedMap(t *testing.T) {
	unrestricted, restrictedWithFloor := classifyRoomIDs(
		[]string{"r1"},
		nil,
	)
	require.Len(t, unrestricted, 1)
	assert.Empty(t, restrictedWithFloor)
}
