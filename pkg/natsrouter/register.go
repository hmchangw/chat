package natsrouter

import (
	"encoding/json"
	"log/slog"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errnats"
)

// Register subscribes a typed handler to a subject pattern; empty payload yields zero-value Req.
// Panics on subscription failure (startup-only).
func Register[Req, Resp any](
	r *Router,
	pattern string,
	fn func(c *Context, req Req) (*Resp, error),
) {
	handler := HandlerFunc(func(c *Context) {
		var req Req
		// Empty payload is valid — handler receives zero-value Req (optional-body endpoints).
		if len(c.Msg.Data) > 0 {
			if err := json.Unmarshal(c.Msg.Data, &req); err != nil {
				// WithCause preserves the parse error for server logs without echoing it to the client.
				replyErr(c, errcode.BadRequest("invalid request payload", errcode.WithCause(err)))
				return
			}
		}

		resp, err := fn(c, req)
		if err != nil {
			replyErr(c, err)
			return
		}

		c.ReplyJSON(resp)
	})

	r.addRoute(pattern, []HandlerFunc{handler})
}

// RegisterNoBody subscribes a handler that takes no request body.
func RegisterNoBody[Resp any](
	r *Router,
	pattern string,
	fn func(c *Context) (*Resp, error),
) {
	handler := HandlerFunc(func(c *Context) {
		resp, err := fn(c)
		if err != nil {
			replyErr(c, err)
			return
		}

		c.ReplyJSON(resp)
	})

	r.addRoute(pattern, []HandlerFunc{handler})
}

// RegisterVoid subscribes a handler that processes a request without replying.
func RegisterVoid[Req any](
	r *Router,
	pattern string,
	fn func(c *Context, req Req) error,
) {
	handler := HandlerFunc(func(c *Context) {
		var req Req
		if err := json.Unmarshal(c.Msg.Data, &req); err != nil {
			slog.Error("invalid payload in void handler", "error", err, "subject", c.Msg.Subject)
			return
		}

		if err := fn(c, req); err != nil {
			slog.Error("void handler error", "error", err, "subject", c.Msg.Subject)
		}
	})

	r.addRoute(pattern, []HandlerFunc{handler})
}

// replyErr classifies err and sends the errcode envelope on the reply subject.
func replyErr(c *Context, err error) {
	errnats.Reply(c, c.Msg, err)
}
