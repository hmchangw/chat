package natsrouter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
)

// Registrar is the interface for registering route handlers.
type Registrar interface {
	addRoute(pattern string, handlers []HandlerFunc)
}

// Router manages NATS subscriptions with pattern-based routing and middleware.
type Router struct {
	nc         *otelnats.Conn
	queue      string
	middleware []HandlerFunc

	mu   sync.Mutex
	subs []*nats.Subscription
	wg   sync.WaitGroup
}

// New creates a Router with the given NATS connection and queue group.
func New(nc *otelnats.Conn, queue string) *Router {
	return &Router{nc: nc, queue: queue}
}

// Use appends middleware to the router's chain.
func (r *Router) Use(mw ...HandlerFunc) {
	r.middleware = append(r.middleware, mw...)
}

func (r *Router) addRoute(pattern string, handlers []HandlerFunc) {
	rt := parsePattern(pattern)
	all := make([]HandlerFunc, 0, len(r.middleware)+len(handlers))
	all = append(all, r.middleware...)
	all = append(all, handlers...)

	natsHandler := func(m otelnats.Msg) {
		r.wg.Add(1)
		defer r.wg.Done()
		c := acquireContext(m.Context(), m.Msg, rt.extractParams(m.Msg.Subject), all)
		c.Next()
		releaseContext(c)
	}

	sub, err := r.nc.QueueSubscribe(rt.natsSubject, r.queue, natsHandler)
	if err != nil {
		panic(fmt.Sprintf("natsrouter: subscribing to %s: %v", rt.natsSubject, err))
	}

	r.mu.Lock()
	r.subs = append(r.subs, sub)
	r.mu.Unlock()
}

// Shutdown drains every route registered through r and waits for in-flight
// handlers to finish or ctx to expire.
//
// After Shutdown returns, the router will not dispatch new requests. Calling
// Shutdown a second time is a no-op. This is independent of nc.Drain() — use
// Shutdown when you need to stop the router while keeping the NATS connection
// open for other work (e.g., publishing shutdown events).
//
// Returns ctx.Err() if handlers were still running when the deadline expired,
// combined with any error reported by Subscription.Drain().
func (r *Router) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	subs := r.subs
	r.subs = nil
	r.mu.Unlock()

	var errs []error
	for _, s := range subs {
		if err := s.Drain(); err != nil {
			errs = append(errs, fmt.Errorf("draining %q: %w", s.Subject, err))
		}
	}

	// Wait for each subscription's dispatch loop to fully exit BEFORE
	// touching r.wg. sub.Drain() returns immediately after sending UNSUB;
	// callbacks already dispatched still run afterwards. Calling wg.Wait()
	// while those callbacks may still execute wg.Add(1) is "Add at zero
	// after Wait" misuse and trips the race detector. IsValid() flips to
	// false only when the dispatch goroutine has exited.
	if err := waitSubsDrained(ctx, subs); err != nil {
		errs = append(errs, err)
		return errors.Join(errs...)
	}

	// Now no new wg.Add can happen — safe to Wait.
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		errs = append(errs, ctx.Err())
	}
	return errors.Join(errs...)
}

// waitSubsDrained polls each subscription until its dispatch loop has exited
// (IsValid returns false) or ctx is cancelled. NATS exposes no per-subscription
// "drain complete" signal, so polling is the documented pattern.
func waitSubsDrained(ctx context.Context, subs []*nats.Subscription) error {
	const pollInterval = 5 * time.Millisecond
	for _, s := range subs {
		for s.IsValid() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollInterval):
			}
		}
	}
	return nil
}
