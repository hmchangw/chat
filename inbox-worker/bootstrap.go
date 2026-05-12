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

// streamManager is the minimal JetStream surface bootstrapStreams depends on.
// Kept service-local so we don't pollute pkg/ with a multi-method type and so
// tests can inject a fake without mockgen.
type streamManager interface {
	CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (oteljetstream.Stream, error)
	Stream(ctx context.Context, name string) (oteljetstream.Stream, error)
}

// bootstrapStreams handles the JetStream INBOX stream this service uses. When
// enabled (dev/integration), it creates the stream via CreateOrUpdateStream.
// When disabled (production), it verifies the stream exists via Stream() and
// returns an error if it doesn't — fail-fast so a misprovisioned deploy
// surfaces at startup rather than at first publish.
//
// Ownership rule: this helper sets only the stream schema (Name + Subjects)
// from pkg/stream.Inbox. Federation config (Sources + SubjectTransforms for
// cross-site OUTBOX→INBOX sourcing) belongs to ops/IaC and is layered on in
// production. App code never sets it -- but it must not ERASE it either.
//
// When the stream already exists, this helper reads its current Sources +
// SubjectTransforms and merges them into the schema-only update so that
// CreateOrUpdateStream preserves federation config across worker restarts.
// Without this preservation, every worker restart would briefly clear
// INBOX_<siteID>.Sources, breaking cross-site sourcing for any in-flight
// gateway deliveries until the next ops/IaC reconciliation. (The e2e
// harness's BootstrapFederation hits this race directly when
// inbox-worker-b is stop/started under E2E_REUSE_STACK; production
// rarely restarts inbox-worker, but the same window exists at deploy.)
func bootstrapStreams(ctx context.Context, js streamManager, siteID string, enabled bool) error {
	inboxCfg := stream.Inbox(siteID)
	if enabled {
		newCfg := jetstream.StreamConfig{
			Name:     inboxCfg.Name,
			Subjects: inboxCfg.Subjects,
		}
		// Preserve federation config that was layered on by ops/IaC (or by
		// the e2e harness's BootstrapFederation) before this worker restarted.
		// A missing stream is fine -- first-time bootstrap creates it without
		// federation, and the federation layer's first reconciliation sets
		// Sources after.
		if existing, err := js.Stream(ctx, inboxCfg.Name); err == nil && existing != nil {
			if info := existing.CachedInfo(); info != nil && len(info.Config.Sources) > 0 {
				newCfg.Sources = info.Config.Sources
			}
		}
		if _, err := js.CreateOrUpdateStream(ctx, newCfg); err != nil {
			return fmt.Errorf("create INBOX stream: %w", err)
		}
		return nil
	}
	// Production path: verify the stream exists. Fail fast if it doesn't —
	// ops/IaC owns provisioning, and a missing stream means the deploy is
	// broken before the first publish or consume.
	if _, err := js.Stream(ctx, inboxCfg.Name); err != nil {
		return fmt.Errorf("verify INBOX stream: %w", err)
	}
	return nil
}
