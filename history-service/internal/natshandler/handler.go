package natshandler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// Handler holds the NATS connection and queue group for registering endpoints.
type Handler struct {
	nc    *nats.Conn
	queue string
}

// New creates a new NATS Handler with the given connection and queue group.
func New(nc *nats.Conn, queue string) *Handler {
	return &Handler{nc: nc, queue: queue}
}

// Register subscribes a handler function to a NATS subject with queue group.
// It extracts userID from the subject via ParseUserRoomSubject, unmarshals
// the request payload into Req, calls fn, and replies with the result.
// Errors are sanitized at the transport boundary — internal errors are logged
// and "internal error" is returned to the caller.
func Register[Req, Resp any](
	h *Handler,
	subj string,
	fn func(ctx context.Context, userID string, req Req) (*Resp, error),
) error {
	_, err := h.nc.QueueSubscribe(subj, h.queue, func(msg *nats.Msg) {
		userID, _, ok := subject.ParseUserRoomSubject(msg.Subject)
		if !ok {
			natsutil.ReplyError(msg, "invalid subject")
			return
		}

		ctx := context.Background()

		var req Req
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			natsutil.ReplyError(msg, "invalid request payload")
			return
		}

		resp, err := fn(ctx, userID, req)
		if err != nil {
			slog.Error("handler error", "error", err, "subject", msg.Subject)
			natsutil.ReplyError(msg, "internal error")
			return
		}

		natsutil.ReplyJSON(msg, resp)
	})
	if err != nil {
		return fmt.Errorf("subscribing to %s: %w", subj, err)
	}
	return nil
}
