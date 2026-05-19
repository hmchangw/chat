package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProbe struct {
	failuresBeforeOK int
	calls            int
}

func (f *fakeProbe) probe(ctx context.Context, msgID string) error {
	f.calls++
	if f.calls <= f.failuresBeforeOK {
		return errors.New("not yet")
	}
	return nil
}

func TestSettle_AllProbesEventuallySucceed(t *testing.T) {
	f := &fakeProbe{failuresBeforeOK: 2}
	out, err := Settle(context.Background(), SettleFlags{
		Timeout:  2 * time.Second,
		Interval: 10 * time.Millisecond,
		Probes:   5,
	}, []string{"m1", "m2", "m3", "m4", "m5"}, f.probe)
	require.NoError(t, err)
	assert.True(t, out.AllSucceeded)
	assert.Equal(t, 5, out.Succeeded)
	assert.Equal(t, 0, out.Failed)
}

func TestSettle_TimeoutWithSomeProbesFailing(t *testing.T) {
	f := &fakeProbe{failuresBeforeOK: 999} // never succeeds
	out, err := Settle(context.Background(), SettleFlags{
		Timeout:  100 * time.Millisecond,
		Interval: 20 * time.Millisecond,
		Probes:   3,
	}, []string{"a", "b", "c"}, f.probe)
	require.Error(t, err)
	assert.False(t, out.AllSucceeded)
	assert.Equal(t, 0, out.Succeeded)
}

// TestSettle_CtxAbortPreservesCounts verifies Fix 5: when ctx is cancelled
// mid-poll after some probes have already succeeded, Succeeded and Failed
// reflect the actual probe results rather than both being 0.
func TestSettle_CtxAbortPreservesCounts(t *testing.T) {
	// Probe that succeeds immediately for the first two IDs ("m1", "m2")
	// and blocks (until ctx cancels) for the third ("m3").
	unblock := make(chan struct{})
	successFor := map[string]bool{"m1": true, "m2": true}
	probe := func(ctx context.Context, id string) error {
		if successFor[id] {
			return nil
		}
		// Block until ctx is cancelled.
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	defer close(unblock)

	out, err := Settle(ctx, SettleFlags{
		Timeout:  2 * time.Second, // longer than ctx — ctx fires first
		Interval: 5 * time.Millisecond,
		Probes:   3,
	}, []string{"m1", "m2", "m3"}, probe)

	require.Error(t, err)
	assert.False(t, out.AllSucceeded)
	// Before Fix 5 these were both 0; after the fix they reflect actual progress.
	assert.Equal(t, 2, out.Succeeded, "two probes succeeded before ctx abort")
	assert.Equal(t, 1, out.Failed, "one probe was still pending at abort")
}
