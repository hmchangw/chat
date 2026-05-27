//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/testutil"
)

func TestRunDaily_Integration_TinyPresetPasses(t *testing.T) {
	natsURL := testutil.NATS(t)

	cfg := dailyConfig{
		Preset:             "daily-heavy",
		Steps:              []int{10},
		Warmup:             1 * time.Second,
		Hold:               5 * time.Second,
		Cooldown:           500 * time.Millisecond,
		StopOnTrip:         true,
		MaxDirectUsers:     10,
		MultiplexPoolSize:  0,
		MaxConnsPerProcess: 25,
	}

	baseCfg := &config{
		NatsURL:     natsURL,
		MongoURI:    "mongodb://unused",
		MongoDB:     "unused",
		ValkeyAddrs: []string{"unused"},
		SiteID:      "site-test",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := runDailyForTest(ctx, cfg, &prodEnvFactory{baseCfg: baseCfg})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Tripped, "reasons: %v", results[0].TrippedReasons)
}
