// tools/loadgen/scenario_history_test.go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHistoryReadScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("history-read")
	require.True(t, ok)
	assert.Equal(t, "history-read", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestHistoryReadScenario_DefaultPresetIsValid(t *testing.T) {
	sc, ok := LookupScenario("history-read")
	require.True(t, ok)
	_, presetExists := BuiltinPreset(sc.DefaultPreset())
	assert.True(t, presetExists, "DefaultPreset %q must exist", sc.DefaultPreset())
}
