package integrationsuite

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"

	. "github.com/hmchangw/chat/tools/integration-suite/internal/harness"
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

// natsRequestService sends an unauthenticated (service-level) NATS request on
// the given subject using an anonymous connection to the primary site's broker.
// This is used for server-to-server RPCs like RoomsInfoBatch that do not
// require user credentials. The reply is captured into world.LastResponse.
func natsRequestService(ctx context.Context, site, subj string, payload []byte) error {
	natsURL := suiteConfig.NATSURL(site)

	// Open a short-lived anonymous connection for this RPC.
	nc, err := nats.Connect(natsURL, nats.Name("integration-suite/service"))
	if err != nil {
		suiteWorld.SetLastResponse(&LastResponse{
			Transport: "nats",
			Err:       err,
		})
		return nil // captured as a transport-level failure, not a hard step error
	}
	defer nc.Close()

	tp := NewTraceparent()
	traceID, _ := TraceIDFromTraceparent(tp)

	msg := nats.NewMsg(subj)
	msg.Data = payload
	msg.Header = nats.Header{}
	msg.Header.Set(TraceparentHeader, tp)

	reqCtx, cancel := context.WithTimeout(ctx, natsRequestTimeout)
	defer cancel()

	reply, err := nc.RequestMsgWithContext(reqCtx, msg)
	if err != nil {
		suiteWorld.SetLastResponse(&LastResponse{
			Transport: "nats",
			Err:       err,
			TraceID:   traceID,
		})
		return nil
	}

	suiteWorld.SetLastResponse(&LastResponse{
		Transport: "nats",
		Body:      reply.Data,
		ErrorText: ParseNATSReplyError(reply.Data),
		TraceID:   traceID,
	})
	return nil
}
