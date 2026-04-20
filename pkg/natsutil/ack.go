package natsutil

import "log/slog"

// Acker is the minimal JetStream message interface the Ack helper needs.
// Both `jetstream.Msg` (nats.go) and otel-wrapped variants
// (e.g. `oteljetstream.Msg`) satisfy it.
type Acker interface {
	Ack() error
}

// Naker is the minimal JetStream message interface the Nak helper needs.
// Same compatibility story as Acker.
type Naker interface {
	Nak() error
}

// Ack acks `msg` and logs any failure under a consistent structured-log
// shape (`reason` + `error`). `reason` is a short label describing WHY
// the message is being acked — e.g. "handler succeeded", "filtered",
// "malformed payload" — so operators can query logs by cause without
// parsing free-text phrases.
//
// Use this from every JetStream consumer in the repo rather than hand-rolling
// an `if err := msg.Ack(); err != nil { slog.Error(...) }` block. Consolidating
// the pattern gives us one place to add tracing spans, metrics counters, or
// delivery-context fields later, and keeps log keys consistent across services.
func Ack(msg Acker, reason string) {
	if err := msg.Ack(); err != nil {
		slog.Error("ack failed", "reason", reason, "error", err)
	}
}

// Nak naks `msg` for redelivery and logs any failure under the same
// structured-log shape as Ack. `reason` describes WHY the message is being
// redelivered — e.g. "handler error", "bulk index failure", "transient
// downstream error".
func Nak(msg Naker, reason string) {
	if err := msg.Nak(); err != nil {
		slog.Error("nak failed", "reason", reason, "error", err)
	}
}
