package main

import (
	"context"
	"sync"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
)

// asyncDispatcher wraps NATS message handlers so they run in a bounded worker
// pool instead of inline on the subscription's delivery goroutine.
//
// NATS-go delivers messages to one subscription serially on a single
// goroutine. Any handler that does I/O (Mongo round-trips, JetStream
// publishes, downstream NATS requests) caps that subject's throughput at
// 1 / handler-latency. Under load the request-side timeout fires while
// messages pile up in the client's pending queue — observed for `msg.read`
// in daily-heavy load tests as P50 latencies of 20-30 s.
//
// dispatch(fn) returns a wrapper that returns to the delivery goroutine as
// soon as a worker slot is acquired, freeing the subscription to take the
// next message while fn runs concurrently. The semaphore bounds in-flight
// work so a slow downstream (Mongo, JetStream) can't spawn unbounded
// goroutines — instead the delivery goroutine blocks on the semaphore and
// backpressure shows up as NATS pending growth, which is observable and
// configurable.
//
// Ordering is NOT preserved across messages on the same subject. Every
// room-service handler is either idempotent (mute toggle, room create on
// retry) or last-write-wins (mark-read updates lastSeenAt = time.Now()), so
// out-of-order arrival is safe.
type asyncDispatcher struct {
	sem chan struct{}
	wg  sync.WaitGroup
}

// newAsyncDispatcher constructs a dispatcher with the given concurrency
// cap. maxWorkers <= 0 is clamped to 1, so a misconfigured env var falls
// back to the legacy serial behavior rather than crashing or deadlocking
// on a zero-cap channel.
func newAsyncDispatcher(maxWorkers int) *asyncDispatcher {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	return &asyncDispatcher{sem: make(chan struct{}, maxWorkers)}
}

// dispatch wraps a handler so each invocation runs in a worker goroutine.
// The caller (NATS delivery goroutine) blocks on the semaphore when the
// pool is full; this is intentional backpressure — see the type comment.
func (d *asyncDispatcher) dispatch(fn func(otelnats.Msg)) func(otelnats.Msg) {
	return func(m otelnats.Msg) {
		d.sem <- struct{}{}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer func() { <-d.sem }()
			fn(m)
		}()
	}
}

// Wait blocks until every dispatched handler returns or ctx is cancelled.
// Returns ctx.Err() on timeout. Call AFTER nc.Drain() during shutdown:
// drain waits for delivery goroutines (which just enqueue work into this
// dispatcher) but not for the workers the dispatcher spawned.
func (d *asyncDispatcher) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
