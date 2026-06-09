package main

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// ReadReceiptRequester is the narrow request/reply transport the read-receipt
// generator depends on. The production implementation wraps
// nats.Conn.RequestWithContext; tests inject a fake.
type ReadReceiptRequester interface {
	Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error)
}

// natsReadReceiptRequester is the production ReadReceiptRequester. Each call
// performs nats.Conn.RequestWithContext under a per-call timeout context.
type natsReadReceiptRequester struct {
	nc *nats.Conn
}

func newNATSReadReceiptRequester(nc *nats.Conn) *natsReadReceiptRequester {
	return &natsReadReceiptRequester{nc: nc}
}

func (r *natsReadReceiptRequester) Request(ctx context.Context, subj string, data []byte, timeout time.Duration) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	msg, err := r.nc.RequestWithContext(reqCtx, subj, data)
	if err != nil {
		return nil, fmt.Errorf("nats request: %w", err)
	}
	return msg.Data, nil
}
