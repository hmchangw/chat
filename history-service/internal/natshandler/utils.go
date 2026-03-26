package natshandler

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// HandleRequest is a generic helper that eliminates unmarshal/reply boilerplate.
// It unmarshals the NATS message payload into Req, calls the handler func,
// and replies with the result via natsutil.ReplyJSON or natsutil.ReplyError.
func HandleRequest[Req, Resp any](msg *nats.Msg, handlerFn func(ctx context.Context, req Req) (*Resp, error)) {
	ctx := context.Background()

	var req Req
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		natsutil.ReplyError(msg, "invalid request payload")
		return
	}

	resp, err := handlerFn(ctx, req)
	if err != nil {
		slog.Error("handler error", "error", err)
		natsutil.ReplyError(msg, "internal error")
		return
	}

	natsutil.ReplyJSON(msg, resp)
}
