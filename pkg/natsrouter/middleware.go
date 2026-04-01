package natsrouter

import (
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
)

// Middleware is a handler that participates in the middleware chain.
// Call c.Next() to continue, or return/call c.Abort() to short-circuit.
type Middleware = HandlerFunc

// requestIDKey is the context key used to store the request ID.
const requestIDKey = "requestID"

// RequestID returns middleware that generates or extracts a correlation ID
// for each request and stores it in the context via c.Set("requestID", id).
// If the incoming NATS message has an "X-Request-ID" header, that value is used;
// otherwise a new UUID is generated.
func RequestID() HandlerFunc {
	return func(c *Context) {
		reqID := ""
		if c.Msg != nil && c.Msg.Header != nil {
			reqID = c.Msg.Header.Get("X-Request-ID")
		}
		if reqID == "" {
			reqID = uuid.New().String()
		}
		c.Set(requestIDKey, reqID)
		c.Next()
	}
}

// requestAttrs returns common log attributes including the request ID if present.
func requestAttrs(c *Context) []any {
	var attrs []any
	if c.Msg != nil {
		attrs = append(attrs, "subject", c.Msg.Subject)
	}
	if id, ok := c.Get(requestIDKey); ok {
		attrs = append(attrs, "requestID", id)
	}
	return attrs
}

// Recovery returns middleware that catches panics, logs them, and replies with "internal error".
func Recovery() HandlerFunc {
	return func(c *Context) {
		defer func() {
			if r := recover(); r != nil {
				attrs := append(requestAttrs(c), "panic", r, "stack", string(debug.Stack()))
				slog.Error("panic recovered", attrs...)
				c.ReplyError("internal error")
				c.Abort()
			}
		}()
		c.Next()
	}
}

// Logging returns middleware that logs each request with subject, duration, and request ID.
func Logging() HandlerFunc {
	return func(c *Context) {
		start := time.Now()
		c.Next()
		attrs := append(requestAttrs(c), "duration", time.Since(start))
		slog.Info("nats request", attrs...)
	}
}
