package natsrouter

import (
	"fmt"

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
		c := acquireContext(m.Context(), m.Msg, rt.extractParams(m.Msg.Subject), all)
		c.Next()
		releaseContext(c)
	}

	if _, err := r.nc.QueueSubscribe(rt.natsSubject, r.queue, natsHandler); err != nil {
		panic(fmt.Sprintf("natsrouter: subscribing to %s: %v", rt.natsSubject, err))
	}
}
