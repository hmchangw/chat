package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPhase2Demo_ScenarioIsRegistered(t *testing.T) {
	sc, ok := LookupScenario("demo-phase2-exit")
	require.True(t, ok, "demo scenario must be registered by its init()")
	assert.Equal(t, "demo-phase2-exit", sc.Name())

	all := AllScenarios()
	assert.Contains(t, all, "demo-phase2-exit", "demo scenario must appear in AllScenarios()")
}
