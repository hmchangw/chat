package natsrouter

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
)

// defaultMaxConcurrency is the default per-pod handler concurrency cap. Sized
// to match the project-wide MAX_WORKERS convention used by JetStream worker
// services. Override with WithMaxConcurrency.
const defaultMaxConcurrency = 100

// Registrar is the interface that Register/RegisterNoBody/RegisterVoid use
// to attach handlers. Implemented by *Router today.
//
// The contract is intentionally minimal so future wrappers (for example a
// route-group type that prepends a shared subject prefix and shared
// middleware) can compose by implementing the same interface and
// delegating to a parent Registrar. addRoute receives the
// fully-resolved subject pattern and the complete middleware-chain-plus-
// handler slice; the implementation owns the NATS subscription lifecycle.
type Registrar interface {
	addRoute(pattern string, handlers []HandlerFunc)
}

// Router manages NATS subscriptions with pattern-based routing and middleware.
type Router struct {
	nc         *otelnats.Conn
	queue      string
	middleware []HandlerFunc

	// sem gates handler concurrency: every handler invocation acquires a
	// slot before running and releases it on return. cap(sem) is the
	// per-pod concurrency ceiling. Configured by WithMaxConcurrency.
	sem chan struct{}

	mu   sync.Mutex
	subs []*nats.Subscription
}

// Option configures a Router on construction.
type Option func(*Router)

// WithMaxConcurrency sets the maximum number of in-flight handler
// invocations across all routes registered on this router. Defaults to
// the constructor default. Non-positive values are ignored; the
// constructor default applies in that case. If multiple
// WithMaxConcurrency options are supplied, the last one takes effect.
// Saturation triggers a 503-style ErrUnavailable reply.
func WithMaxConcurrency(n int) Option {
	return func(r *Router) {
		if n > 0 {
			r.sem = make(chan struct{}, n)
		}
	}
}

// New creates a Router with the given NATS connection and queue group.
func New(nc *otelnats.Conn, queue string, opts ...Option) *Router {
	r := &Router{
		nc:    nc,
		queue: queue,
		sem:   make(chan struct{}, defaultMaxConcurrency),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
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

	// Register close listeners BEFORE calling Drain so we don't miss the
	// event. nats.go fires SubscriptionClosed only after the per-sub
	// dispatch loop has fully exited — every callback that was ever going
	// to run has already returned by that point, so there is no "Add at
	// zero after Wait" window to guard against.
	closed := make([]<-chan nats.SubStatus, len(subs))
	for i, s := range subs {
		closed[i] = s.StatusChanged(nats.SubscriptionClosed)
	}

	var errs []error
	for _, s := range subs {
		if err := s.Drain(); err != nil {
			errs = append(errs, fmt.Errorf("draining %q: %w", s.Subject, err))
		}
	}

	for i, ch := range closed {
		select {
		case <-ch:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("waiting for %q close: %w", subs[i].Subject, ctx.Err()))
			return errors.Join(errs...)
		}
	}
	return errors.Join(errs...)
}
