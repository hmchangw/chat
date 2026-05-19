// tools/loadgen/scenario_room_test.go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoomRPCScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("room-rpc")
	require.True(t, ok)
	assert.Equal(t, "room-rpc", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestRoomRPCScenario_DefaultPresetIsValid(t *testing.T) {
	sc, ok := LookupScenario("room-rpc")
	require.True(t, ok)
	_, presetExists := BuiltinPreset(sc.DefaultPreset())
	assert.True(t, presetExists, "DefaultPreset %q must exist", sc.DefaultPreset())
}
