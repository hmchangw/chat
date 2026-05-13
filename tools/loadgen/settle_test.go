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
