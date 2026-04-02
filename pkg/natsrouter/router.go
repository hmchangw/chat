package natsrouter

import (
	"fmt"

	"github.com/nats-io/nats.go"
)

// Registrar is the interface for registering route handlers.
type Registrar interface {
	addRoute(pattern string, handlers []HandlerFunc)
}

// Router manages NATS subscriptions with pattern-based routing and middleware.
type Router struct {
	nc         *nats.Conn
	queue      string
	middleware []HandlerFunc
}

// New creates a Router with the given NATS connection and queue group.
func New(nc *nats.Conn, queue string) *Router {
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

	natsHandler := func(msg *nats.Msg) {
		c := acquireContext(msg, rt.extractParams(msg.Subject), all)
		c.Next()
		releaseContext(c)
	}

	if _, err := r.nc.QueueSubscribe(rt.natsSubject, r.queue, natsHandler); err != nil {
		panic(fmt.Sprintf("natsrouter: subscribing to %s: %v", rt.natsSubject, err))
	}
}
