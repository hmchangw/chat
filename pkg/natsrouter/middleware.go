package natsrouter

import (
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
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

// forwardedSiteHeader marks a request as already forwarded by SiteProxy.
const forwardedSiteHeader = "X-Forwarded-Site"

// siteProxyTimeout is the deadline for forwarded cross-site NATS requests.
const siteProxyTimeout = 10 * time.Second

// SiteProxy returns middleware that transparently forwards requests to remote sites.
// It reads the "siteID" parameter from the subject. Requests targeting the local site
// (or already forwarded from another site) proceed to the next handler. Requests
// targeting a remote site are forwarded via NATS request/reply and the response is
// relayed back to the caller.
//
// The middleware sets an X-Forwarded-Site header on forwarded messages to prevent
// infinite loops when both sites subscribe to the same wildcard pattern.
func SiteProxy(localSiteID string, nc *nats.Conn) HandlerFunc {
	return func(c *Context) {
		// Already forwarded from another site — handle locally.
		if c.Msg != nil && c.Msg.Header != nil && c.Msg.Header.Get(forwardedSiteHeader) != "" {
			c.Next()
			return
		}

		siteID := c.Param("siteID")
		if siteID == "" || siteID == localSiteID {
			c.Next()
			return
		}

		// Forward to the remote site's service via NATS request/reply.
		msg := nats.NewMsg(c.Msg.Subject)
		msg.Data = c.Msg.Data
		msg.Header = make(nats.Header)
		for k, v := range c.Msg.Header {
			msg.Header[k] = v
		}
		msg.Header.Set(forwardedSiteHeader, localSiteID)

		resp, err := nc.RequestMsg(msg, siteProxyTimeout)
		if err != nil {
			attrs := append(requestAttrs(c), "remoteSiteID", siteID, "error", err)
			slog.Error("remote site request failed", attrs...)
			c.ReplyError("remote site unavailable")
			c.Abort()
			return
		}

		if err := c.Msg.Respond(resp.Data); err != nil {
			slog.Error("relay response failed", append(requestAttrs(c), "error", err)...)
		}
		c.Abort()
	}
}
