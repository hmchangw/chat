// tools/loadgen/scenario_notif_test.go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotificationScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("notification-fanout")
	require.True(t, ok)
	assert.Equal(t, "notification-fanout", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestNotificationLag_RecordedOnReceipt(t *testing.T) {
	tracker := newNotifLagTracker()
	publishedAt := time.Now()
	tracker.RecordPublished("msg-1", publishedAt)

	receivedAt := publishedAt.Add(15 * time.Millisecond)
	lag, ok := tracker.LagFor("msg-1", receivedAt)
	require.True(t, ok)
	assert.InDelta(t, 15*time.Millisecond, lag, float64(2*time.Millisecond))
}

func TestNotificationLag_UnknownMessageReturnsFalse(t *testing.T) {
	tracker := newNotifLagTracker()
	_, ok := tracker.LagFor("msg-unknown", time.Now())
	assert.False(t, ok, "unknown messages must return ok=false")
}
