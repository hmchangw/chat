package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/model"
)

func TestPresenceCollector_LatencySample(t *testing.T) {
	c := newPresenceCollector()
	t0 := time.Unix(0, 0)
	c.Expect("u-1", model.StatusOnline, t0)
	c.Observe("u-1", model.StatusOnline, t0.Add(40*time.Millisecond))

	assert.Equal(t, int64(1), c.Attempted())
	assert.Equal(t, int64(0), c.Failed())
	lat := c.LatenciesMs()
	assert.Len(t, lat, 1)
	assert.InDelta(t, 40.0, lat[0], 0.001)
}

func TestPresenceCollector_WrongStatusIgnored(t *testing.T) {
	c := newPresenceCollector()
	t0 := time.Unix(0, 0)
	c.Expect("u-1", model.StatusOnline, t0)
	c.Observe("u-1", model.StatusAway, t0.Add(10*time.Millisecond)) // not what we awaited
	assert.Len(t, c.LatenciesMs(), 0)
	c.ReapMissing()
	assert.Equal(t, int64(1), c.Failed(), "unresolved expectation reaps as missing")
}

func TestPresenceCollector_OrphanObserveIgnored(t *testing.T) {
	c := newPresenceCollector()
	// Sweeper-driven offline for an account we never awaited: orphan, ignored.
	c.Observe("u-99", model.StatusOffline, time.Now())
	assert.Equal(t, int64(0), c.Attempted())
	assert.Len(t, c.LatenciesMs(), 0)
}

func TestPresenceCollector_EmitFailure(t *testing.T) {
	c := newPresenceCollector()
	c.RecordEmit()
	c.RecordEmitFailure()
	assert.Equal(t, int64(1), c.Attempted())
	assert.Equal(t, int64(1), c.Failed())
}

func TestPresenceCollector_Reset(t *testing.T) {
	c := newPresenceCollector()
	t0 := time.Unix(0, 0)
	c.Expect("u-1", model.StatusOnline, t0)
	c.Observe("u-1", model.StatusOnline, t0.Add(time.Millisecond))
	c.Reset()
	assert.Equal(t, int64(0), c.Attempted())
	assert.Equal(t, int64(0), c.Failed())
	assert.Len(t, c.LatenciesMs(), 0)
	c.ReapMissing()
	assert.Equal(t, int64(0), c.Failed(), "reset must drop stale expectations")
}

func TestPresenceCollector_Recovery(t *testing.T) {
	c := newPresenceCollector()
	start := time.Unix(100, 0)
	c.BeginRecovery([]string{"u-1", "u-2"}, start)
	assert.False(t, c.RecoveryComplete())

	c.Observe("u-1", model.StatusOnline, start.Add(20*time.Millisecond))
	assert.False(t, c.RecoveryComplete())
	c.Observe("u-2", model.StatusOnline, start.Add(70*time.Millisecond))
	assert.True(t, c.RecoveryComplete())
	assert.InDelta(t, 70.0, float64(c.RecoveryElapsed().Milliseconds()), 0.001)
}
