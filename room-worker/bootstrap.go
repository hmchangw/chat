package main

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"

	"github.com/hmchangw/chat/pkg/stream"
)

// bootstrapConfig groups every field that is ONLY meaningful when the
// service is being stood up in dev or integration tests against a NATS
// instance where the streams it consumes do not yet exist. In production
// streams are pre-provisioned by ops/IaC and Bootstrap.Enabled must remain
// false; the service only creates its own durable consumer.
type bootstrapConfig struct {
	// Enabled (BOOTSTRAP_STREAMS) toggles whether the service calls
	// CreateOrUpdateStream at startup for the streams it consumes.
	// Leave false in production.
	Enabled bool `env:"STREAMS" envDefault:"false"`
}

// streamCreator is the minimal JetStream surface bootstrapStreams depends on.
// Kept service-local so we don't pollute pkg/ with a one-method type and so
// tests can inject a fake without mockgen.
type streamCreator interface {
	CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (oteljetstream.Stream, error)
}

// bootstrapStreams creates the JetStream streams this service consumes from.
// No-op when enabled is false (the production path) — streams are owned by
// ops/IaC. In dev/integration the local docker-compose sets
// BOOTSTRAP_STREAMS=true so a developer can stand the service up in isolation
// against a fresh NATS instance.
func bootstrapStreams(ctx context.Context, js streamCreator, siteID string, enabled bool) error {
	if !enabled {
		return nil
	}
	roomsCfg := stream.Rooms(siteID)
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     roomsCfg.Name,
		Subjects: roomsCfg.Subjects,
	}); err != nil {
		return fmt.Errorf("create ROOMS stream: %w", err)
	}
	return nil
}
