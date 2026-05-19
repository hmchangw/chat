// tools/loadgen/scenario_notif_routing_test.go
//
//go:build notif_routing_ready

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotifRoutingScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("notif-routing")
	require.True(t, ok)
	assert.Equal(t, "notif-routing", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}
