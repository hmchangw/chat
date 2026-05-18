package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeReader returns ErrRAWNotVisible for the first failuresBefore polls, then nil (visible).
type fakeReader struct {
	failuresBefore int
	calls          int
}

func (f *fakeReader) Lookup(_ context.Context, _ string) error {
	f.calls++
	if f.calls <= f.failuresBefore {
		return ErrRAWNotVisible
	}
	return nil
}

func TestRAW_PollUntilVisible_BoundsLagAndWindow(t *testing.T) {
	reader := &fakeReader{failuresBefore: 3} // hit on the 4th call
	interval := 10 * time.Millisecond
	timeout := 1 * time.Second
	publishedAt := time.Now()

	outcome, err := pollUntilVisible(
		context.Background(),
		reader.Lookup,
		"msg-1",
		publishedAt,
		interval,
		timeout,
	)
	require.NoError(t, err)

	// Lag is bounded by (N+1) × interval where N is failuresBefore.
	expectedMaxLag := time.Duration(4) * interval
	assert.LessOrEqual(t, outcome.Lag, expectedMaxLag+5*time.Millisecond,
		"lag %v should be <= ~40ms (4 polls × 10ms + jitter)", outcome.Lag)

	// Visibility window is at most 1 × interval (time between last-NotFound and first-OK).
	assert.LessOrEqual(t, outcome.VisibilityWindow, interval+5*time.Millisecond,
		"visibility window %v should be <= 1 × interval (~10ms) + jitter", outcome.VisibilityWindow)
}

func TestRAW_PollUntilVisible_TimeoutReturnsError(t *testing.T) {
	reader := &fakeReader{failuresBefore: 999} // never visible within timeout
	interval := 10 * time.Millisecond
	timeout := 50 * time.Millisecond

	_, err := pollUntilVisible(
		context.Background(),
		reader.Lookup,
		"msg-1",
		time.Now(),
		interval,
		timeout,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRAWTimeout)
}

func TestRAWScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("raw-consistency")
	require.True(t, ok)
	assert.Equal(t, "raw-consistency", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}
