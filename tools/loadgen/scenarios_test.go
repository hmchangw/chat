package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunScenarios_ListsAllRegistered(t *testing.T) {
	var buf bytes.Buffer
	code := runScenariosTo(&buf, nil)
	require.Equal(t, 0, code)
	out := buf.String()
	// At least the 4 Phase 2 scenarios should be listed.
	assert.Contains(t, out, "messaging-pipeline")
	assert.Contains(t, out, "history-read")
	assert.Contains(t, out, "search-read")
	assert.Contains(t, out, "room-rpc")
}

func TestRunScenarios_HasHeader(t *testing.T) {
	var buf bytes.Buffer
	code := runScenariosTo(&buf, nil)
	require.Equal(t, 0, code)
	out := buf.String()
	assert.Contains(t, out, "SCENARIO")
	assert.Contains(t, out, "DEFAULT PRESET")
}
