package main

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// natsRequester adapts nc.RequestWithContext to the Requester interface
// used by the read-only scenarios. The timeout is enforced via a per-call
// derived context so callers don't need to thread one in themselves.
// When the pool's Size > 1, the request is routed to the data connection
// hashed from the subject's user-account segment.
type natsRequester struct {
	pool  *ConnPool
	runID string // C3: stamped as an X-Loadgen-Run-ID header on every request
}

func (r *natsRequester) Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	conn := r.pool.For(UserFromSubject(subject))
	msg := &nats.Msg{Subject: subject, Data: data}
	if r.runID != "" {
		msg.Header = nats.Header{HeaderRunID: []string{r.runID}}
	}
	reply, err := conn.RequestMsgWithContext(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("nats request: %w", err)
	}
	return reply.Data, nil
}
