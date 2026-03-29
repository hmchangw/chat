package natsrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// Register subscribes a typed handler to a subject pattern.
//
// The pattern uses {name} placeholders for named params. At registration time,
// the pattern is converted to a NATS wildcard subject for QueueSubscribe, and
// a param extraction map is built. At request time, params are extracted from
// the subject, the request body is unmarshalled into Req, the handler is called,
// and the response is marshalled back as JSON.
//
// Register panics if the subscription fails. This is intentional — registration
// happens at startup, and a failed subscription means the service cannot function.
// This follows the same pattern as http.HandleFunc and template.Must.
//
// Error handling:
//   - Handler returns *RouteError → sent to client as-is (user-facing error)
//   - Handler returns any other error → logged, client receives "internal error"
//   - JSON unmarshal fails → client receives "invalid request payload"
//
// Example:
//
//	natsrouter.Register(router, "chat.user.{userID}.room.{roomID}.msg.history", svc.LoadHistory)
func Register[Req, Resp any](
	r *Router,
	pattern string,
	fn func(ctx context.Context, params Params, req Req) (*Resp, error),
) {
	rt := parsePattern(pattern)

	handler := r.applyMiddleware(func(msg *nats.Msg) {
		ctx := context.Background()
		params := rt.extractParams(msg.Subject)

		var req Req
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			natsutil.ReplyError(msg, "invalid request payload")
			return
		}

		resp, err := fn(ctx, params, req)
		if err != nil {
			replyErr(msg, err)
			return
		}

		natsutil.ReplyJSON(msg, resp)
	})

	subscribe(r, rt.natsSubject, handler)
}

// RegisterNoBody subscribes a handler that takes no request body — only params.
// Use for endpoints where the subject contains all the information needed
// (e.g. GET-style lookups by ID).
//
// Panics if the subscription fails (same rationale as Register).
//
// Example:
//
//	natsrouter.RegisterNoBody(router, "chat.user.{userID}.request.rooms.get.{roomID}", svc.GetRoom)
func RegisterNoBody[Resp any](
	r *Router,
	pattern string,
	fn func(ctx context.Context, params Params) (*Resp, error),
) {
	rt := parsePattern(pattern)

	handler := r.applyMiddleware(func(msg *nats.Msg) {
		ctx := context.Background()
		params := rt.extractParams(msg.Subject)

		resp, err := fn(ctx, params)
		if err != nil {
			replyErr(msg, err)
			return
		}

		natsutil.ReplyJSON(msg, resp)
	})

	subscribe(r, rt.natsSubject, handler)
}

// RegisterVoid subscribes a handler that processes a request without replying.
// Use for fire-and-forget event handlers where the sender does not expect a response.
//
// Panics if the subscription fails (same rationale as Register).
//
// Example:
//
//	natsrouter.RegisterVoid(router, "chat.user.{userID}.event.typing", svc.HandleTyping)
func RegisterVoid[Req any](
	r *Router,
	pattern string,
	fn func(ctx context.Context, params Params, req Req) error,
) {
	rt := parsePattern(pattern)

	handler := r.applyMiddleware(func(msg *nats.Msg) {
		ctx := context.Background()
		params := rt.extractParams(msg.Subject)

		var req Req
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			slog.Error("invalid payload in void handler", "error", err, "subject", msg.Subject)
			return
		}

		if err := fn(ctx, params, req); err != nil {
			slog.Error("void handler error", "error", err, "subject", msg.Subject)
		}
	})

	subscribe(r, rt.natsSubject, handler)
}

// replyErr handles error replies. If the error is a *RouteError, it is sent
// to the client as-is (user-facing). Any other error is logged and the client
// receives a generic "internal error".
func replyErr(msg *nats.Msg, err error) {
	var routeErr *RouteError
	if errors.As(err, &routeErr) {
		natsutil.ReplyJSON(msg, routeErr)
		return
	}
	slog.Error("handler error", "error", err, "subject", msg.Subject)
	natsutil.ReplyError(msg, "internal error")
}

// subscribe wraps QueueSubscribe with a panic on failure.
func subscribe(r *Router, subject string, handler nats.MsgHandler) {
	if _, err := r.nc.QueueSubscribe(subject, r.queue, handler); err != nil {
		panic(fmt.Sprintf("natsrouter: subscribing to %s: %v", subject, err))
	}
}
