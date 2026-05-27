package main

import (
	"context"
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

// testEnvFactory returns a stepEnv with stubs so runDaily can run without real NATS.
type testEnvFactory struct{}

//nolint:gocritic // cfg passed by value to satisfy envFactory interface
func (testEnvFactory) Build(cfg dailyConfig, users []*userState) *stepEnv {
	return &stepEnv{
		collector:      NewCollector(NewMetrics(), "test"),
		users:          users,
		thresholds:     defaultThresholds(),
		pollPending:    func(_ context.Context) (map[string]int64, error) { return nil, nil },
		scrapeServices: func(_ context.Context) (map[string]int64, error) { return nil, nil },
		maxDirect:      cfg.MaxDirectUsers,
		warmup:         cfg.Warmup,
		hold:           cfg.Hold,
		cooldown:       cfg.Cooldown,
	}
}

func TestRunDaily_SmokeOnTinyConfig(t *testing.T) {
	cfg := dailyConfig{
		Preset:             "daily-heavy",
		Steps:              []int{10},
		Warmup:             20 * time.Millisecond,
		Hold:               50 * time.Millisecond,
		Cooldown:           10 * time.Millisecond,
		StopOnTrip:         true,
		MaxDirectUsers:     10,
		MultiplexPoolSize:  0,
		MaxConnsPerProcess: 10,
	}
	results, err := runDailyForTest(context.Background(), cfg, testEnvFactory{})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Tripped)
}

func TestRunStep_StubReturnsPassWhenEverythingIsGreen(t *testing.T) {
	env := &stepEnv{
		collector:  NewCollector(NewMetrics(), "test"),
		thresholds: defaultThresholds(),
		pollPending: func(ctx context.Context) (map[string]int64, error) {
			return map[string]int64{}, nil
		},
		scrapeServices: func(ctx context.Context) (map[string]int64, error) {
			return map[string]int64{}, nil
		},
		maxDirect: 100,
		warmup:    50 * time.Millisecond,
		hold:      100 * time.Millisecond,
		cooldown:  20 * time.Millisecond,
	}
	r := runStep(context.Background(), env, 100, 0)
	require.False(t, r.Tripped)
	require.False(t, r.Inconclusive)
	require.Equal(t, 100, r.N)
}
