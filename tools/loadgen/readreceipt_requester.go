package main

import (
	"context"
	"time"
)

// ReadReceiptRequester is the narrow request/reply transport the read-receipt
// generator depends on. The production implementation wraps
// nats.Conn.RequestWithContext; tests inject a fake.
// The concrete natsReadReceiptRequester lives in readreceipt_main.go where it
// is wired to a real NATS connection.
type ReadReceiptRequester interface {
	Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error)
}
