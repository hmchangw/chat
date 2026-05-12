package integrationsuite

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"
)

const natsRequestTimeout = 5 * time.Second

// natsRequest sends a NATS request as `account` (using their cached
// connection) on `subject` with body `payload`. The reply (or error) is
// captured into world.LastResponse with Transport="nats".
//
// Sets a fresh traceparent in the message header on every call.
func natsRequest(ctx context.Context, w *World, cfg *Config, account, site, subject string, payload []byte) error {
	creds := w.Credentials(account)
	nc, err := w.NATSPool().Conn(account, site, cfg.NATSURL(site), creds)
	if err != nil {
		w.SetLastResponse(&LastResponse{Transport: "nats", Err: err})
		return err
	}

	tp := NewTraceparent()
	traceID, _ := TraceIDFromTraceparent(tp)

	msg := nats.NewMsg(subject)
	msg.Data = payload
	msg.Header = nats.Header{}
	msg.Header.Set(TraceparentHeader, tp)

	reqCtx, cancel := context.WithTimeout(ctx, natsRequestTimeout)
	defer cancel()

	reply, err := nc.RequestMsgWithContext(reqCtx, msg)
	if err != nil {
		w.SetLastResponse(&LastResponse{
			Transport: "nats",
			Err:       err,
			TraceID:   traceID,
		})
		return nil // caller treats as a captured outcome, not a hard step error
	}

	w.SetLastResponse(&LastResponse{
		Transport: "nats",
		Body:      reply.Data,
		ErrorText: ParseNATSReplyError(reply.Data),
		TraceID:   traceID,
	})
	return nil
}
