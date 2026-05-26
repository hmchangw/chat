// federation-init is a one-shot binary that wires cross-site JetStream
// stream sources between the two local-dev NATS instances (site-a and
// site-b). It assumes the leafnode bridge configured in
// docker-local/site-{a,b}/nats.conf has already linked the chatapp account
// across the two NATSes, and it assumes each site's inbox-worker has
// already created its INBOX_<site> stream schema (Name + Subjects) on
// startup.
//
// What this binary adds: a StreamSource on each INBOX pointing at the
// peer's OUTBOX_<peer> stream (via JetStream Domain), filtered to the
// outbox subjects targeting THIS site, and rewritten via SubjectTransform
// onto chat.inbox.<site>.aggregate.> — matching the schema documented in
// pkg/stream/stream.go's Inbox().
//
// Idempotent: re-running with the same source config is a no-op.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type siteConfig struct {
	local, remote string
	url, creds    string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	sites := []siteConfig{
		{
			local:  envOr("SITE_A_ID", "site-a"),
			remote: envOr("SITE_B_ID", "site-b"),
			url:    envOr("SITE_A_NATS_URL", "nats://nats-a:4222"),
			creds:  envOr("SITE_A_CREDS", "/creds/site-a/backend.creds"),
		},
		{
			local:  envOr("SITE_B_ID", "site-b"),
			remote: envOr("SITE_A_ID", "site-a"),
			url:    envOr("SITE_B_NATS_URL", "nats://nats-b:4222"),
			creds:  envOr("SITE_B_CREDS", "/creds/site-b/backend.creds"),
		},
	}

	for _, s := range sites {
		if err := wireInboxSource(logger, s); err != nil {
			logger.Error("federation-init failed", "site", s.local, "err", err.Error())
			os.Exit(1)
		}
	}
	logger.Info("federation-init complete")
}

func wireInboxSource(logger *slog.Logger, s siteConfig) error {
	nc, err := nats.Connect(s.url,
		nats.UserCredentials(s.creds),
		nats.MaxReconnects(10),
		nats.ReconnectWait(2*time.Second),
		nats.Timeout(10*time.Second),
	)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", s.url, err)
	}
	defer nc.Drain() //nolint:errcheck // best-effort on shutdown

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("init jetstream: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	inboxName := fmt.Sprintf("INBOX_%s", s.local)
	outboxName := fmt.Sprintf("OUTBOX_%s", s.remote)
	filter := fmt.Sprintf("outbox.%s.to.%s.>", s.remote, s.local)
	dest := fmt.Sprintf("chat.inbox.%s.aggregate.>", s.local)

	// Wait for inbox-worker to create INBOX_<site> on startup. The worker
	// owns Name + Subjects per the bootstrap convention in CLAUDE.md;
	// we only layer the federation source on top.
	existing, err := waitForStream(ctx, js, inboxName)
	if err != nil {
		return fmt.Errorf("wait for %s: %w", inboxName, err)
	}

	info, err := existing.Info(ctx)
	if err != nil {
		return fmt.Errorf("get %s info: %w", inboxName, err)
	}

	cfg := info.Config
	cfg.Sources = []*jetstream.StreamSource{{
		Name:          outboxName,
		Domain:        s.remote,
		FilterSubject: filter,
		SubjectTransforms: []jetstream.SubjectTransformConfig{{
			Source:      filter,
			Destination: dest,
		}},
	}}

	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		return fmt.Errorf("update %s with source from %s: %w", inboxName, outboxName, err)
	}
	logger.Info("federated",
		"local_stream", inboxName,
		"remote_stream", outboxName,
		"remote_domain", s.remote,
		"filter", filter,
		"destination", dest,
	)
	return nil
}

func waitForStream(ctx context.Context, js jetstream.JetStream, name string) (jetstream.Stream, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		s, err := js.Stream(ctx, name)
		if err == nil {
			return s, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout: %w", err)
		case <-ticker.C:
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
