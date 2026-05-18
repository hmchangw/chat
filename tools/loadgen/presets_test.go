package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunPresets_ListsAllBuiltin(t *testing.T) {
	var buf bytes.Buffer
	code := runPresetsTo(&buf, nil)
	require.Equal(t, 0, code)
	out := buf.String()
	assert.Contains(t, out, "realistic")
	assert.Contains(t, out, "channel-heavy")
	assert.Contains(t, out, "dm-heavy")
}

func TestRunPresets_HasHeader(t *testing.T) {
	var buf bytes.Buffer
	code := runPresetsTo(&buf, nil)
	require.Equal(t, 0, code)
	out := buf.String()
	assert.Contains(t, out, "PRESET")
	assert.Contains(t, out, "USERS")
	assert.Contains(t, out, "ROOMS")
}
