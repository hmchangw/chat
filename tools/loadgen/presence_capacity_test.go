package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCapacityConfig_Defaults(t *testing.T) {
	cfg, err := parseCapacityConfig(nil)
	require.NoError(t, err)
	assert.Equal(t, []int{10000, 20000, 50000, 100000, 200000}, cfg.Steps)
	assert.Equal(t, 30*time.Second, cfg.Warmup)
	assert.Equal(t, 120*time.Second, cfg.Hold)
	assert.Equal(t, 30*time.Second, cfg.Heartbeat)
	assert.InDelta(t, 0.001, cfg.FalseOfflineRate, 1e-9)
	assert.InDelta(t, 0.10, cfg.PingTolerance, 1e-9)
	assert.True(t, cfg.StopOnTrip)
}

func TestParseCapacityConfig_StepsShorthandAndOverrides(t *testing.T) {
	cfg, err := parseCapacityConfig([]string{"--steps=1k,2k", "--hold=10s", "--false-offline-rate=0.05"})
	require.NoError(t, err)
	assert.Equal(t, []int{1000, 2000}, cfg.Steps)
	assert.Equal(t, 10*time.Second, cfg.Hold)
	assert.InDelta(t, 0.05, cfg.FalseOfflineRate, 1e-9)
}

func TestParseCapacityConfig_BadSteps(t *testing.T) {
	_, err := parseCapacityConfig([]string{"--steps=abc"})
	require.Error(t, err)
}
