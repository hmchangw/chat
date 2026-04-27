package idgen_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/idgen"
)

func isBase62(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		default:
			return false
		}
	}
	return true
}

func TestGenerateID_LengthAndAlphabet(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := idgen.GenerateID()
		assert.Len(t, id, 17)
		assert.True(t, isBase62(id), "id %q contains non-base62 characters", id)
	}
}

func TestGenerateID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := idgen.GenerateID()
		_, dup := seen[id]
		assert.False(t, dup, "duplicate ID %q at iteration %d", id, i)
		seen[id] = struct{}{}
	}
}

func TestDeriveID_StableAcrossCalls(t *testing.T) {
	seed := "addmembers:room-1:1735689600000"
	a := idgen.DeriveID(seed)
	b := idgen.DeriveID(seed)
	assert.Equal(t, a, b, "same seed must yield same ID")
	assert.Len(t, a, 17)
	assert.True(t, isBase62(a))
}

func TestDeriveID_DifferentSeedsDifferentIDs(t *testing.T) {
	a := idgen.DeriveID("addmembers:room-1:1")
	b := idgen.DeriveID("addmembers:room-1:2")
	assert.NotEqual(t, a, b)
}

func TestDeriveID_EmptySeed(t *testing.T) {
	id := idgen.DeriveID("")
	assert.Len(t, id, 17)
	assert.True(t, isBase62(id))
}
