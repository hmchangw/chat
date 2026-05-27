package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseDailyConfig_Defaults(t *testing.T) {
	c, err := parseDailyConfig([]string{"--preset=daily-heavy"})
	require.NoError(t, err)
	require.Equal(t, "daily-heavy", c.Preset)
	require.Equal(t, []int{1000, 2000, 5000, 10000, 20000, 50000, 100000}, c.Steps)
	require.Equal(t, 60*time.Second, c.Warmup)
	require.Equal(t, 180*time.Second, c.Hold)
	require.Equal(t, 30*time.Second, c.Cooldown)
	require.Equal(t, 20000, c.MaxDirectUsers)
	require.Equal(t, 200, c.MultiplexPoolSize)
	require.Equal(t, 25000, c.MaxConnsPerProcess)
	require.True(t, c.StopOnTrip)
}

func TestParseDailyConfig_Overrides(t *testing.T) {
	c, err := parseDailyConfig([]string{
		"--preset=daily-light",
		"--steps=1000,5000",
		"--warmup=10s",
		"--hold=30s",
		"--cooldown=5s",
		"--max-direct-users=5000",
		"--multiplex-pool-size=50",
		"--max-conns-per-process=10000",
		"--stop-on-trip=false",
	})
	require.NoError(t, err)
	require.Equal(t, []int{1000, 5000}, c.Steps)
	require.Equal(t, 10*time.Second, c.Warmup)
	require.False(t, c.StopOnTrip)
}

func TestParseDailyConfig_Rejects_UnknownPreset(t *testing.T) {
	_, err := parseDailyConfig([]string{"--preset=nope"})
	require.Error(t, err)
}

func TestParseDailyConfig_RejectsTooManyConns(t *testing.T) {
	_, err := parseDailyConfig([]string{
		"--preset=daily-heavy",
		"--max-direct-users=30000",
		"--max-conns-per-process=10000",
	})
	require.Error(t, err) // 30000 direct + 200 mux > 10000 cap
}
