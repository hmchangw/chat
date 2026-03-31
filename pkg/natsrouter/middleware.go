package natsrouter

import (
	"log/slog"
	"time"
)

// Middleware is a handler that participates in the middleware chain.
// Call c.Next() to continue, or return/call c.Abort() to short-circuit.
type Middleware = HandlerFunc

// Recovery returns middleware that catches panics, logs them, and replies with "internal error".
func Recovery() HandlerFunc {
	return func(c *Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic recovered", "panic", r, "subject", c.Msg.Subject)
				c.ReplyError("internal error")
				c.Abort()
			}
		}()
		c.Next()
	}
}

// Logging returns middleware that logs each request with subject and duration.
func Logging() HandlerFunc {
	return func(c *Context) {
		start := time.Now()
		c.Next()
		slog.Info("nats request", "subject", c.Msg.Subject, "duration", time.Since(start))
	}
}
