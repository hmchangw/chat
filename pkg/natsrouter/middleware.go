package natsrouter

import (
	"log/slog"
	"time"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// Middleware is a handler that participates in the middleware chain.
// Call c.Next() to continue, or return/call c.Abort() to short-circuit.
type Middleware = HandlerFunc

// requestIDKey is the context key used to store the request ID.
const requestIDKey = "requestID"

// RequestID returns middleware that extracts X-Request-ID (or mints via idgen) and stores it on both the natsrouter keys map and the underlying ctx.
func RequestID() HandlerFunc {
	return func(c *Context) {
		reqID := ""
		if c.Msg != nil && c.Msg.Header != nil {
			reqID = c.Msg.Header.Get(natsutil.RequestIDHeader)
		}
		if reqID == "" {
			reqID = idgen.GenerateID()
		}
		c.Set(requestIDKey, reqID)
		c.SetContext(natsutil.WithRequestID(c.ctx, reqID))
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
				attrs := append(requestAttrs(c), "panic", r)
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
