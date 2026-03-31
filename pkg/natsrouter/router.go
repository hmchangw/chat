package natsrouter

import (
	"fmt"

	"github.com/nats-io/nats.go"
)

// Registrar is the interface for registering route handlers.
// Both Router and Group implement it.
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

// Group creates a sub-router with a subject prefix and optional middleware.
func (r *Router) Group(prefix string, mw ...HandlerFunc) *Group {
	return &Group{parent: r, prefix: prefix, middleware: mw}
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

// Group is a sub-router with a subject prefix and scoped middleware.
type Group struct {
	parent     Registrar
	prefix     string
	middleware []HandlerFunc
}

// Use appends middleware scoped to this group.
func (g *Group) Use(mw ...HandlerFunc) {
	g.middleware = append(g.middleware, mw...)
}

// Group creates a nested sub-group.
func (g *Group) Group(prefix string, mw ...HandlerFunc) *Group {
	combined := make([]HandlerFunc, len(g.middleware), len(g.middleware)+len(mw))
	copy(combined, g.middleware)
	combined = append(combined, mw...)
	return &Group{parent: g.parent, prefix: g.prefix + "." + prefix, middleware: combined}
}

func (g *Group) addRoute(pattern string, handlers []HandlerFunc) {
	fullPattern := g.prefix + "." + pattern
	all := make([]HandlerFunc, 0, len(g.middleware)+len(handlers))
	all = append(all, g.middleware...)
	all = append(all, handlers...)
	g.parent.addRoute(fullPattern, all)
}
