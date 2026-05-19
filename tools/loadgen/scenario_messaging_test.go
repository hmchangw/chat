// tools/loadgen/scenario_messaging_test.go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessagingPipelineScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("messaging-pipeline")
	require.True(t, ok)
	assert.Equal(t, "messaging-pipeline", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestMessagingPipelineScenario_DefaultPresetIsValid(t *testing.T) {
	sc, ok := LookupScenario("messaging-pipeline")
	require.True(t, ok)
	_, presetExists := BuiltinPreset(sc.DefaultPreset())
	assert.True(t, presetExists, "DefaultPreset %q must exist", sc.DefaultPreset())
}
