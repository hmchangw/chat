package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// countTicksInWindow counts how many ticks fall within [from, to).
func countTicksInWindow(ticks []time.Time, from, to time.Time) int {
	n := 0
	for _, t := range ticks {
		if (t.After(from) || t.Equal(from)) && t.Before(to) {
			n++
		}
	}
	return n
}

// TestTickloop_RespectsBurstEnvelope confirms that the burst envelope fires
// significantly more ticks during the burst window than the baseline window.
//
// Config: 5 rps baseline, 40× burst (= 200 rps) for 1s every 3s.
// Over a 6s run:
//   - Burst window  [0s, 1s) — 200 rps × 1s ≈ 200 ticks
//   - Baseline window [2s, 3s) — 5 rps × 1s ≈ 5 ticks
//
// We assert burst > baseline×20 (allowing 50% margin on the 40× factor).
func TestTickloop_RespectsBurstEnvelope(t *testing.T) {
	cfg := &tickLoopConfig{
		Rate:          5,
		BaselineRate:  5,
		BurstRatio:    40,
		BurstPeriod:   3 * time.Second,
		BurstDuration: 1 * time.Second,
		MaxInFlight:   500,
		Metrics:       NewMetrics(),
		Preset:        "test",
		Scenario:      "burst-test",
	}

	var (
		fired []time.Time
		mu    sync.Mutex
	)
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	tickLoop(ctx, cfg, func(_ context.Context) {
		now := time.Now()
		mu.Lock()
		fired = append(fired, now)
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()

	// Burst window: the first burst starts at t=0 and lasts for BurstDuration=1s.
	bursty := countTicksInWindow(fired, start, start.Add(time.Second))
	// Baseline window: the quiet period between the first and second burst.
	baseline := countTicksInWindow(fired, start.Add(2*time.Second), start.Add(3*time.Second))

	t.Logf("burst window ticks: %d, baseline window ticks: %d", bursty, baseline)

	// Burst should fire ≥20× more ticks than baseline (40× theoretical; 50% margin).
	assert.Greater(t, bursty, baseline*20,
		"burst window had %d ticks, baseline had %d; expected burst > baseline×20", bursty, baseline)
}

// TestTickloop_BurstDisabledWhenZero confirms that a zero BurstPeriod leaves
// the tick rate at the steady Rate with no changes.
func TestTickloop_BurstDisabledWhenZero(t *testing.T) {
	cfg := &tickLoopConfig{
		Rate:         10,
		BaselineRate: 10,
		// BurstPeriod, BurstRatio, BurstDuration all zero — burst disabled.
		MaxInFlight: 0,
		Metrics:     NewMetrics(),
		Preset:      "test",
		Scenario:    "burst-disabled",
	}

	var count int
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	tickLoop(ctx, cfg, func(_ context.Context) {
		count++
	})

	// 10 rps × 200ms ≈ 2 ticks; allow wide scheduling tolerance.
	assert.GreaterOrEqual(t, count, 1, "expected at least 1 tick at baseline rate")
	assert.LessOrEqual(t, count, 20, "expected no runaway burst; got %d", count)
}
