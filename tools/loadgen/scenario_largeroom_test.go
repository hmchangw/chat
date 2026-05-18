package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLargeRoomScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("large-room-broadcast")
	require.True(t, ok)
	assert.Equal(t, "large-room-broadcast", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestCompletionTracker_RatioAtP99(t *testing.T) {
	// 100 members. A message has receive timestamps at:
	//   80 members @ t+10ms, 19 @ t+50ms, 1 @ t+5000ms (laggard)
	// p99 should be ~50ms (the 99th sample sorted). Completion ratio at p99 = 99/100 = 0.99.
	tracker := newCompletionTracker(100)
	pubAt := time.Now()
	for i := 0; i < 80; i++ {
		tracker.Observe("msg-1", i, pubAt.Add(10*time.Millisecond))
	}
	for i := 80; i < 99; i++ {
		tracker.Observe("msg-1", i, pubAt.Add(50*time.Millisecond))
	}
	tracker.Observe("msg-1", 99, pubAt.Add(5*time.Second))

	p99, ratio := tracker.Snapshot("msg-1", pubAt)
	assert.InDelta(t, float64(50*time.Millisecond), float64(p99), float64(2*time.Millisecond),
		"p99 latency should be ~50ms; got %v", p99)
	assert.InDelta(t, 0.99, ratio, 0.02,
		"completion ratio at p99 should be ~0.99; got %.2f", ratio)
}

func TestLargeRoomPresets_RegisteredAndValid(t *testing.T) {
	for _, name := range []string{"announce-room", "firehose-room", "bot-room"} {
		p, ok := BuiltinPreset(name)
		require.True(t, ok, "preset %s must be registered", name)
		assert.Greater(t, p.Users, 0, "preset %s: Users > 0", name)
		assert.Greater(t, p.Rooms, 0, "preset %s: Rooms > 0", name)
	}
}
