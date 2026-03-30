package natsrouter

import (
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// Middleware wraps a NATS message handler. It receives the next handler in the
// chain and returns a new handler that can execute logic before/after calling
// next, or short-circuit by not calling next at all.
//
// Middleware sees the raw *nats.Msg — it can inspect/modify the message,
// reject requests before unmarshal, or wrap the reply.
//
// Example:
//
//	func AuthMiddleware(next nats.MsgHandler) nats.MsgHandler {
//	    return func(msg *nats.Msg) {
//	        if !isAuthorized(msg) {
//	            natsutil.ReplyError(msg, "unauthorized")
//	            return // short-circuit
//	        }
//	        next(msg) // continue chain
//	    }
//	}
type Middleware func(next nats.MsgHandler) nats.MsgHandler

// Recovery returns middleware that catches panics in the handler chain,
// logs the panic with the subject, and replies with a sanitized error.
// Place this first in the middleware chain to catch panics from all handlers.
//
// Example:
//
//	router.Use(natsrouter.Recovery())
func Recovery() Middleware {
	return func(next nats.MsgHandler) nats.MsgHandler {
		return func(msg *nats.Msg) {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic recovered", "panic", r, "subject", msg.Subject)
					natsutil.ReplyError(msg, "internal error")
				}
			}()
			next(msg)
		}
	}
}

// Logging returns middleware that logs each request with subject and duration.
//
// Example:
//
//	router.Use(natsrouter.Logging())
func Logging() Middleware {
	return func(next nats.MsgHandler) nats.MsgHandler {
		return func(msg *nats.Msg) {
			start := time.Now()
			next(msg)
			slog.Info("nats request", "subject", msg.Subject, "duration", time.Since(start))
		}
	}
}
