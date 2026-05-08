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

func TestReadiness_SucceedsOnFirstReply(t *testing.T) {
	var called atomic.Int64
	probe := func(ctx context.Context) error {
		called.Add(1)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	err := waitForReady(ctx, &readinessConfig{
		Probe:      probe,
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), called.Load(), "probe should have been called exactly once")
}

func TestReadiness_RetriesUntilReady(t *testing.T) {
	var called atomic.Int64
	probe := func(ctx context.Context) error {
		n := called.Add(1)
		if n < 3 {
			return errors.New("not ready yet")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	start := time.Now()
	err := waitForReady(ctx, &readinessConfig{
		Probe:      probe,
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond,
	})
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Equal(t, int64(3), called.Load())
	// Backoff sequence: 0 (immediate), 10ms, 20ms = at least 30ms total.
	assert.GreaterOrEqual(t, elapsed, 25*time.Millisecond)
}

func TestReadiness_ReturnsErrorAfterDeadline(t *testing.T) {
	probe := func(ctx context.Context) error {
		return errors.New("never ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	err := waitForReady(ctx, &readinessConfig{
		Probe:      probe,
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 30 * time.Millisecond,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrReadinessTimeout),
		"expected DeadlineExceeded or ErrReadinessTimeout; got %v", err)
}

func TestReadiness_BackoffCapsAtMax(t *testing.T) {
	// With probe always failing and a tight deadline, ensure backoff doesn't
	// grow unbounded — the loop should fire many attempts within the deadline,
	// not get stuck waiting for an exponential giant.
	var called atomic.Int64
	probe := func(ctx context.Context) error {
		called.Add(1)
		return errors.New("always fail")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = waitForReady(ctx, &readinessConfig{
		Probe:      probe,
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 30 * time.Millisecond,
	})
	// Without a cap, exp doubling would let only ~5 calls in 200ms.
	// With cap at 30ms we expect 8+ calls.
	assert.GreaterOrEqual(t, called.Load(), int64(6),
		"backoff cap should let multiple probes fire; got %d", called.Load())
}
