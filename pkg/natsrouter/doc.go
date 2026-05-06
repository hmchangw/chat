// Package natsrouter provides Gin-style pattern-based routing for NATS
// request/reply services with typed handlers, middleware, and automatic
// JSON marshal/unmarshal.
//
// # Concurrency model
//
// The router spawns one goroutine per incoming message. By default
// there is NO admission control — the router behaves like gin.Default()
// over net/http: every accepted message gets its own handler goroutine,
// and backpressure flows from downstream timeouts (HandlerTimeout
// middleware, ctx-aware database drivers).
//
// For services that need a hard cap on in-flight handlers (memory-
// constrained pods, downstream pools that don't queue cleanly, etc.),
// opt into admission control with WithMaxConcurrency(N) at construction.
// When admission is on, a non-blocking acquire on a semaphore inside
// the per-subscription dispatcher gates each spawn:
//
//   - On acquire success, the router spawns the handler goroutine.
//   - On acquire failure (cap reached), the router publishes an
//     ErrUnavailable reply ({"error":"service busy","code":"unavailable"})
//     and returns. Callers should retry with backoff.
//
// Per-route concurrency overrides are not supported today. The Registrar
// interface is intentionally minimal so a future wrapper (e.g. a route
// group with its own admission semaphore) can be added without breaking
// the existing API.
//
// # Fire-and-forget routes
//
// When admission control is enabled (WithMaxConcurrency), RegisterVoid
// handlers have no NATS reply subject by definition. When a
// fire-and-forget message arrives while the semaphore is saturated, the
// router has no reply channel on which to publish ErrUnavailable, so the
// message is SILENTLY DROPPED. Callers that publish to a void route via
// nc.Publish (rather than nc.Request) get no signal that the message
// was dropped. Size MaxConcurrency conservatively for services that
// expose RegisterVoid endpoints, or front them with JetStream so
// dropped messages can be redelivered.
//
// # Queue-group fairness under saturation
//
// When admission control is enabled (WithMaxConcurrency), NATS
// queue-group routing distributes messages among subscribers without
// knowing whether any individual subscriber's process-level admission
// control is full. A saturated pod will continue to receive (and
// busy-reply) its share of messages even while other pods in the
// queue group sit idle. Operators should monitor the per-pod
// busy-reply rate (or set up server-side auto-scaling on it) rather
// than assume queue-group routing alone provides load balancing.
//
// # Ordering
//
// Per-subject FIFO ordering is NOT preserved. Two messages that arrive
// on the same subscription are spawned into independent goroutines and
// race; whichever wins the goroutine schedule runs first. Handlers must
// be idempotent or use external coordination (e.g. Cassandra LWTs,
// Mongo conditional updates) to ensure correctness under concurrent
// invocation.
//
// # Panic safety
//
// The router installs a process-safety backstop in every spawned
// handler goroutine: an unrecovered panic is caught at the spawn
// site, logged with stack trace, and (if the message has a Reply
// subject) replied to with "internal error". This guarantees the
// process cannot be crashed by a single bad handler regardless of
// middleware configuration. Recovery middleware (registered via
// r.Use(Recovery())) is still the recommended path because it
// produces structured ErrInternal replies enriched with request-ID
// and other middleware-set fields; the spawn-site backstop is
// strictly a defense-in-depth catch.
//
// # Shutdown
//
// Router.Shutdown drains every subscription, waits for the dispatcher
// goroutines to exit (SubscriptionClosed), and then waits on a
// WaitGroup that tracks every spawned handler goroutine. The full
// shutdown returns only after all in-flight handlers have completed or
// the context expires (whichever comes first).
//
// See README.md in this directory for full documentation and examples.
package natsrouter
