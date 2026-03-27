package natsrouter

import (
	"context"
	"encoding/json"
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
// Handler errors are logged with the subject and sanitized — callers receive
// "internal error", never raw Go error strings.
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
			slog.Error("handler error", "error", err, "subject", msg.Subject)
			natsutil.ReplyError(msg, "internal error")
			return
		}

		natsutil.ReplyJSON(msg, resp)
	})

	if _, err := r.nc.QueueSubscribe(rt.natsSubject, r.queue, handler); err != nil {
		panic(fmt.Sprintf("natsrouter: subscribing to %s: %v", rt.natsSubject, err))
	}
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
			slog.Error("handler error", "error", err, "subject", msg.Subject)
			natsutil.ReplyError(msg, "internal error")
			return
		}

		natsutil.ReplyJSON(msg, resp)
	})

	if _, err := r.nc.QueueSubscribe(rt.natsSubject, r.queue, handler); err != nil {
		panic(fmt.Sprintf("natsrouter: subscribing to %s: %v", rt.natsSubject, err))
	}
}
