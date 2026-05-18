package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLeg is a stub for one room-open leg that sleeps for `delay` then returns.
type fakeLeg struct {
	delay time.Duration
	err   error
	calls atomic.Int64
}

func (f *fakeLeg) Do(_ context.Context) error {
	f.calls.Add(1)
	time.Sleep(f.delay)
	return f.err
}

func TestRoomOpen_E2EIsMaxOfLegs(t *testing.T) {
	legs := []legFn{
		{Name: "history", Fn: (&fakeLeg{delay: 10 * time.Millisecond}).Do},
		{Name: "rooms_get", Fn: (&fakeLeg{delay: 30 * time.Millisecond}).Do},
		{Name: "read", Fn: (&fakeLeg{delay: 50 * time.Millisecond}).Do},
		{Name: "restricted", Fn: (&fakeLeg{delay: 20 * time.Millisecond}).Do},
	}
	start := time.Now()
	outcome, err := openRoom(context.Background(), legs)
	elapsed := time.Since(start)
	require.NoError(t, err)

	// E2E should be ~50ms (the slowest leg).
	assert.InDelta(t, 50*time.Millisecond, outcome.E2E, float64(15*time.Millisecond),
		"E2E (%v) should be ~max(legs) = 50ms", outcome.E2E)
	// Wall-clock elapsed should also be ~50ms (parallel execution).
	assert.InDelta(t, 50*time.Millisecond, elapsed, float64(15*time.Millisecond),
		"wall-clock (%v) should be ~50ms (parallel execution, not sum=110ms)", elapsed)
	// Per-leg latencies recorded.
	assert.Len(t, outcome.LegLatencies, 4)
	assert.InDelta(t, 10*time.Millisecond, outcome.LegLatencies["history"], float64(5*time.Millisecond))
	assert.InDelta(t, 50*time.Millisecond, outcome.LegLatencies["read"], float64(5*time.Millisecond))
}

func TestRoomOpen_ErrorFromOneLegFailsAll(t *testing.T) {
	legs := []legFn{
		{Name: "history", Fn: (&fakeLeg{delay: 5 * time.Millisecond}).Do},
		{Name: "rooms_get", Fn: (&fakeLeg{delay: 5 * time.Millisecond, err: errors.New("boom")}).Do},
	}
	_, err := openRoom(context.Background(), legs)
	assert.Error(t, err)
}

func TestRoomOpenScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("room-open")
	require.True(t, ok)
	assert.Equal(t, "room-open", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}
