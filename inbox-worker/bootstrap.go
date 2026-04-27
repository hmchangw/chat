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
//
// Note: Subjects is intentionally omitted from the StreamConfig. In production
// the INBOX stream's Sources and SubjectTransforms (cross-site OUTBOX→INBOX
// sourcing) are configured by ops/IaC. This helper only creates the stream
// with its Name so the consumer can be attached; the sourcing config is
// managed externally and must not be overwritten here.
func bootstrapStreams(ctx context.Context, js streamCreator, siteID string, enabled bool) error {
	if !enabled {
		return nil
	}
	inboxCfg := stream.Inbox(siteID)
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: inboxCfg.Name,
	}); err != nil {
		return fmt.Errorf("create INBOX stream: %w", err)
	}
	return nil
}
