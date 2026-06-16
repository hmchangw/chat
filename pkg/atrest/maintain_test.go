package atrest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitSignal blocks until ch delivers or the test deadline elapses, failing
// the test with msg on timeout. Keeps the table tests free of repeated
// select/time.After boilerplate.
func waitSignal[T any](t *testing.T, ch <-chan T, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
}

// counterValue reads the current value of a Prometheus counter without
// pulling in the client_golang testutil helper (and its extra dep).
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}

// TestMaintainToken_ReauthsAfterTokenExpires proves the core recovery
// behavior: when the current token's lifetime watcher ends (expiry, max_ttl,
// or a non-renewable token nearing TTL), the loop stops the dead lease and
// authenticates again for a fresh token.
func TestMaintainToken_ReauthsAfterTokenExpires(t *testing.T) {
	initialDone := make(chan error, 1)
	initialStopped := make(chan struct{}, 1)
	initial := tokenLease{done: initialDone, stop: func() { initialStopped <- struct{}{} }}

	relogin := make(chan struct{}, 4)
	lease := func(_ context.Context) (tokenLease, error) {
		relogin <- struct{}{}
		return tokenLease{done: make(chan error, 1), stop: func() {}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		maintainToken(ctx, initial, lease, func(int) time.Duration { return 0 })
	}()

	// Simulate the token's watcher terminating (renewal stopped / expiry).
	close(initialDone)

	waitSignal(t, initialStopped, "expired lease was not stopped")
	waitSignal(t, relogin, "did not re-authenticate after token expiry")

	cancel()
	waitSignal(t, loopDone, "loop did not exit after cancel")
}

// TestMaintainToken_RetriesWithBackoffOnLoginFailure proves a failed re-login
// is retried with backoff (not given up on) and that each failure increments
// the renewal-failure metric so operators get a signal while auth is down.
func TestMaintainToken_RetriesWithBackoffOnLoginFailure(t *testing.T) {
	before := counterValue(t, kekRenewalFailures)

	initialDone := make(chan error, 1)
	initial := tokenLease{done: initialDone, stop: func() {}}

	var calls int32
	success := make(chan struct{}, 1)
	lease := func(_ context.Context) (tokenLease, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return tokenLease{}, errors.New("vault unreachable")
		}
		success <- struct{}{}
		return tokenLease{done: make(chan error, 1), stop: func() {}}, nil
	}

	backoffCalled := make(chan int, 1)
	backoff := func(attempt int) time.Duration { backoffCalled <- attempt; return 0 }

	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); maintainToken(ctx, initial, lease, backoff) }()

	close(initialDone) // trigger re-login; first attempt fails

	select {
	case a := <-backoffCalled:
		assert.Equal(t, 1, a, "backoff should be called with the failed attempt number")
	case <-time.After(2 * time.Second):
		t.Fatal("backoff not invoked after login failure")
	}
	waitSignal(t, success, "did not retry login through to success")

	cancel()
	waitSignal(t, loopDone, "loop did not exit after cancel")

	assert.Equal(t, before+1, counterValue(t, kekRenewalFailures),
		"a failed re-login should increment the renewal-failure metric exactly once")
}

// TestMaintainToken_StopsLeaseOnContextCancel proves graceful shutdown: when
// the loop's context is cancelled, it stops the live lease (so the watcher
// goroutine doesn't leak) and exits without attempting a re-login.
func TestMaintainToken_StopsLeaseOnContextCancel(t *testing.T) {
	stopped := make(chan struct{}, 1)
	initial := tokenLease{done: make(chan error), stop: func() { stopped <- struct{}{} }}

	lease := func(_ context.Context) (tokenLease, error) {
		t.Error("lease must not be called when ctx is cancelled before expiry")
		return tokenLease{}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		maintainToken(ctx, initial, lease, func(int) time.Duration { return 0 })
	}()

	cancel()

	waitSignal(t, stopped, "live lease was not stopped on cancel")
	waitSignal(t, loopDone, "loop did not exit after cancel")
}

// TestMaintainToken_CancelDuringBackoffStops proves the retry loop honors
// cancellation mid-backoff instead of spinning forever while Vault is down.
func TestMaintainToken_CancelDuringBackoffStops(t *testing.T) {
	initialDone := make(chan error, 1)
	initial := tokenLease{done: initialDone, stop: func() {}}

	lease := func(_ context.Context) (tokenLease, error) {
		return tokenLease{}, errors.New("vault still down")
	}

	ctx, cancel := context.WithCancel(context.Background())
	enteredBackoff := make(chan struct{}, 1)
	backoff := func(int) time.Duration {
		select {
		case enteredBackoff <- struct{}{}:
		default:
		}
		return time.Hour // long: the loop must exit via cancel, not expiry
	}

	loopDone := make(chan struct{})
	go func() { defer close(loopDone); maintainToken(ctx, initial, lease, backoff) }()

	close(initialDone)
	waitSignal(t, enteredBackoff, "loop did not reach backoff after a failed login")

	cancel()
	waitSignal(t, loopDone, "loop did not exit when cancelled during backoff")
}

func TestDefaultBackoff(t *testing.T) {
	assert.Equal(t, 1*time.Second, defaultBackoff(1), "first attempt is base")
	assert.Equal(t, 2*time.Second, defaultBackoff(2))
	assert.Equal(t, 4*time.Second, defaultBackoff(3))
	assert.Equal(t, 30*time.Second, defaultBackoff(100), "capped at the ceiling")
	assert.Equal(t, 1*time.Second, defaultBackoff(0), "non-positive attempt clamps to base")
}
