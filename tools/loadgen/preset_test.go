package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltinPresets_ContainsAllFour(t *testing.T) {
	names := []string{"small", "medium", "large", "realistic"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			p, ok := BuiltinPreset(name)
			require.True(t, ok, "preset %q must exist", name)
			assert.Equal(t, name, p.Name)
			assert.Greater(t, p.Users, 0)
			assert.Greater(t, p.Rooms, 0)
		})
	}
}

func TestBuiltinPresets_UnknownReturnsFalse(t *testing.T) {
	_, ok := BuiltinPreset("nonexistent")
	assert.False(t, ok)
}

func TestBuiltinPresets_UniformShape(t *testing.T) {
	for _, name := range []string{"small", "medium", "large"} {
		t.Run(name, func(t *testing.T) {
			p, _ := BuiltinPreset(name)
			assert.Equal(t, DistUniform, p.RoomSizeDist)
			assert.Equal(t, DistUniform, p.SenderDist)
			assert.InDelta(t, 0.0, p.MentionRate, 1e-9)
			assert.InDelta(t, 0.0, p.ThreadRate, 1e-9)
		})
	}
}

func TestBuiltinPresets_RealisticShape(t *testing.T) {
	p, _ := BuiltinPreset("realistic")
	assert.Equal(t, DistMixed, p.RoomSizeDist)
	assert.Equal(t, DistZipf, p.SenderDist)
	assert.Greater(t, p.MentionRate, 0.0)
	assert.Greater(t, p.ThreadRate, 0.0)
	assert.Greater(t, p.ContentBytes.Max, p.ContentBytes.Min)
}
