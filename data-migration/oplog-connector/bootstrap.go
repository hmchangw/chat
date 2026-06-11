package main

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"

	"github.com/hmchangw/chat/pkg/stream"
)

// bootstrapConfig gates dev/integration stream creation. In production the
// MIGRATION_OPLOG stream is provisioned by ops/IaC and Enabled stays false.
type bootstrapConfig struct {
	Enabled bool `env:"STREAMS" envDefault:"false"`
}

// streamManager is the minimal JetStream surface bootstrapStreams needs, kept
// service-local so tests can fake it without mockgen.
type streamManager interface {
	CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (oteljetstream.Stream, error)
	Stream(ctx context.Context, name string) (oteljetstream.Stream, error)
}

// bootstrapStreams owns the MIGRATION_OPLOG_{siteID} stream. When enabled
// (dev/integration) it creates the stream from its schema (Name + Subjects);
// when disabled (production) it verifies the stream exists and fails fast if
// not. Federation config (Sources/SubjectTransforms) is ops/IaC-owned and is
// never set here.
func bootstrapStreams(ctx context.Context, js streamManager, siteID string, enabled bool) error {
	cfg := stream.MigrationOplog(siteID)
	if enabled {
		if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:     cfg.Name,
			Subjects: cfg.Subjects,
		}); err != nil {
			return fmt.Errorf("create MIGRATION_OPLOG stream: %w", err)
		}
		return nil
	}
	if _, err := js.Stream(ctx, cfg.Name); err != nil {
		return fmt.Errorf("verify MIGRATION_OPLOG stream: %w", err)
	}
	return nil
}
