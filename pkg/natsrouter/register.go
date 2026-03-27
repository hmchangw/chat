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
// Handler errors are logged with the subject and sanitized — callers receive
// "internal error", never raw Go error strings.
//
// Example:
//
//	natsrouter.Register[LoadHistoryRequest, LoadHistoryResponse](
//	    router,
//	    "chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history",
//	    func(ctx context.Context, p natsrouter.Params, req LoadHistoryRequest) (*LoadHistoryResponse, error) {
//	        userID := p.Get("userID")
//	        return svc.LoadHistory(ctx, userID, req)
//	    },
//	)
func Register[Req, Resp any](
	r *Router,
	pattern string,
	fn func(ctx context.Context, params Params, req Req) (*Resp, error),
) error {
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
		return fmt.Errorf("subscribing to %s: %w", rt.natsSubject, err)
	}
	return nil
}

// RegisterNoBody subscribes a handler that takes no request body — only params.
// Use for endpoints where the subject contains all the information needed
// (e.g. GET-style lookups by ID).
//
// Example:
//
//	natsrouter.RegisterNoBody[Room](
//	    router,
//	    "chat.user.{userID}.request.rooms.get.{roomID}",
//	    func(ctx context.Context, p natsrouter.Params) (*Room, error) {
//	        return svc.GetRoom(ctx, p.Get("roomID"))
//	    },
//	)
func RegisterNoBody[Resp any](
	r *Router,
	pattern string,
	fn func(ctx context.Context, params Params) (*Resp, error),
) error {
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
		return fmt.Errorf("subscribing to %s: %w", rt.natsSubject, err)
	}
	return nil
}
