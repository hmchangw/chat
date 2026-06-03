package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMsg constructs an otelnats.Msg suitable for handing to a wrapped
// handler. The dispatcher never reads the message body itself, so a bare
// nats.Msg suffices.
func fakeMsg() otelnats.Msg {
	return otelnats.Msg{Msg: &nats.Msg{}, Ctx: context.Background()}
}

func TestAsyncDispatcher_HandlerRunsAfterCallerReturns(t *testing.T) {
	// The wrapper must return to its caller (the NATS delivery goroutine)
	// before the handler completes — otherwise we're back to serializing
	// every message on the subscription's delivery goroutine. Proven here
	// by holding the handler open and observing the wrapper has already
	// returned.
	d := newAsyncDispatcher(4)
	hold := make(chan struct{})
	finished := make(chan struct{})
	wrapped := d.dispatch(func(_ otelnats.Msg) {
		<-hold
		close(finished)
	})
	wrapped(fakeMsg()) // must not block on `hold`
	select {
	case <-finished:
		t.Fatal("handler completed before being released — wrapper ran synchronously")
	default:
	}
	close(hold)
	require.NoError(t, d.Wait(ctxWithTimeout(t, time.Second)))
	<-finished
}

func TestAsyncDispatcher_BoundsConcurrencyToMaxWorkers(t *testing.T) {
	// With maxWorkers=3, no more than 3 handlers may run concurrently.
	// We hold each handler on a barrier and assert the inflight peak == 3.
	const max = 3
	d := newAsyncDispatcher(max)
	var inflight, peak int64
	release := make(chan struct{})
	handler := d.dispatch(func(_ otelnats.Msg) {
		now := atomic.AddInt64(&inflight, 1)
		for {
			p := atomic.LoadInt64(&peak)
			if now <= p || atomic.CompareAndSwapInt64(&peak, p, now) {
				break
			}
		}
		<-release
		atomic.AddInt64(&inflight, -1)
	})

	// Fire 10 messages. The caller goroutine blocks on the dispatcher's
	// semaphore once 3 are in flight, so we drive sends from goroutines
	// to avoid deadlocking the test.
	const n = 10
	var sendWG sync.WaitGroup
	sendWG.Add(n)
	for i := 0; i < n; i++ {
		go func() { defer sendWG.Done(); handler(fakeMsg()) }()
	}

	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&peak) == int64(max)
	}, time.Second, 10*time.Millisecond, "peak inflight never reached %d", max)

	close(release)
	sendWG.Wait()
	require.NoError(t, d.Wait(ctxWithTimeout(t, time.Second)))
	assert.Equal(t, int64(max), atomic.LoadInt64(&peak), "peak should equal worker cap exactly")
}

func TestAsyncDispatcher_WaitFlushesInflight(t *testing.T) {
	// Wait must block until every dispatched handler returns — otherwise
	// shutdown can disconnect Mongo while handlers still hold sessions.
	d := newAsyncDispatcher(4)
	var done int64
	handler := d.dispatch(func(_ otelnats.Msg) {
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt64(&done, 1)
	})
	for i := 0; i < 5; i++ {
		handler(fakeMsg())
	}
	require.NoError(t, d.Wait(ctxWithTimeout(t, time.Second)))
	assert.Equal(t, int64(5), atomic.LoadInt64(&done))
}

func TestAsyncDispatcher_WaitReturnsCtxErrOnTimeout(t *testing.T) {
	// Wait must surface ctx.Err() when shutdown timeout expires before all
	// handlers drain — so main can log the leak and exit anyway.
	d := newAsyncDispatcher(1)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) }) // unblock the goroutine after the test
	handler := d.dispatch(func(_ otelnats.Msg) { <-block })
	handler(fakeMsg())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := d.Wait(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestNewAsyncDispatcher_ClampsToOne(t *testing.T) {
	// Misconfigured zero/negative maxWorkers must still produce a usable
	// dispatcher — falling back to serial execution is safer than crashing
	// or building a zero-cap chan that blocks forever.
	for _, n := range []int{0, -1, -100} {
		d := newAsyncDispatcher(n)
		assert.Equal(t, 1, cap(d.sem), "maxWorkers=%d", n)
	}
}

func ctxWithTimeout(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
