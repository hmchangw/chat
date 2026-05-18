// tools/loadgen/scenario_presence_test.go
//
//go:build presence_ready

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPresenceTypingScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("presence-typing")
	require.True(t, ok)
	assert.Equal(t, "presence-typing", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}
