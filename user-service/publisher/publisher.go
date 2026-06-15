// Package publisher publishes cross-site outbox events over core NATS (no JetStream).
package publisher

import (
	"context"
	"fmt"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// Publisher implements service.EventPublisher using core NATS (no JetStream).
// Status events are last-write-wins, so no publish dedup or JetStream is needed.
type Publisher struct{ nc *otelnats.Conn }

// New returns a Publisher backed by the given NATS connection.
func New(nc *otelnats.Conn) *Publisher { return &Publisher{nc: nc} }

// Publish sends data to subject using the underlying NATS connection. It uses
// PublishMsg with a natsutil.NewMsg so the outgoing event carries X-Request-ID
// from ctx (cross-site log correlation); otelnats still injects its trace
// headers on top, and ctx is forwarded for trace propagation.
func (p *Publisher) Publish(ctx context.Context, subject string, data []byte) error {
	if err := p.nc.PublishMsg(ctx, natsutil.NewMsg(ctx, subject, data)); err != nil {
		return fmt.Errorf("publish outbox event: %w", err)
	}
	return nil
}
