package natsrouter

import "github.com/nats-io/nats.go"

// Router manages NATS request/reply subscriptions with pattern-based
// routing and middleware. Create with New(), add middleware with Use(),
// then register handlers with Register or RegisterNoBody.
type Router struct {
	nc         *nats.Conn
	queue      string
	middleware []Middleware
}

// New creates a Router with the given NATS connection and queue group.
// The queue group ensures only one instance in a service cluster handles
// each request (competing consumers / load balancing).
//
// Example:
//
//	router := natsrouter.New(nc, "history-service")
func New(nc *nats.Conn, queue string) *Router {
	return &Router{nc: nc, queue: queue}
}

// Use appends middleware to the router's middleware chain.
// Middleware executes in the order added: Use(A, B, C) → A → B → C → handler.
//
// Example:
//
//	router.Use(natsrouter.Recovery())
//	router.Use(natsrouter.Logging())
func (r *Router) Use(mw ...Middleware) {
	r.middleware = append(r.middleware, mw...)
}

// applyMiddleware wraps a handler with all registered middleware.
// Middleware is applied from outside in: given [A, B, C], result is A(B(C(handler))).
func (r *Router) applyMiddleware(handler nats.MsgHandler) nats.MsgHandler {
	for i := len(r.middleware) - 1; i >= 0; i-- {
		handler = r.middleware[i](handler)
	}
	return handler
}
