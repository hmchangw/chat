package main

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"

	"github.com/hmchangw/chat/pkg/stream"
)

// Dev/integration only; production streams are owned by ops/IaC.
type bootstrapConfig struct {
	Enabled bool `env:"STREAMS" envDefault:"false"`
}

// Service-local JetStream surface so tests inject a fake without mockgen.
type streamManager interface {
	CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (oteljetstream.Stream, error)
	Stream(ctx context.Context, name string) (oteljetstream.Stream, error)
}

// Authors schema (Name + Subjects); preserves ops-owned federation
// (Sources) verbatim and unions Subjects. Mirror is NOT preserved.
func bootstrapStreams(ctx context.Context, js streamManager, siteID string, enabled bool) error {
	inboxCfg := stream.Inbox(siteID)
	if enabled {
		newCfg := jetstream.StreamConfig{
			Name:     inboxCfg.Name,
			Subjects: inboxCfg.Subjects,
		}
		if existing, err := js.Stream(ctx, inboxCfg.Name); err == nil && existing != nil {
			if info := existing.CachedInfo(); info != nil {
				if len(info.Config.Sources) > 0 {
					newCfg.Sources = info.Config.Sources
				}
				newCfg.Subjects = unionSubjects(inboxCfg.Subjects, info.Config.Subjects)
			}
		}
		if _, err := js.CreateOrUpdateStream(ctx, newCfg); err != nil {
			return fmt.Errorf("create INBOX stream: %w", err)
		}
		return nil
	}
	if _, err := js.Stream(ctx, inboxCfg.Name); err != nil {
		return fmt.Errorf("verify INBOX stream: %w", err)
	}
	return nil
}

// unionSubjects returns declared ∪ existing with declared first. Exact-string
// dedup (NATS doesn't normalize, so "foo.*" and "foo.>" remain distinct).
func unionSubjects(declared, existing []string) []string {
	seen := make(map[string]struct{}, len(declared)+len(existing))
	out := make([]string, 0, len(declared)+len(existing))
	for _, s := range declared {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range existing {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
