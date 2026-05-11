//go:build e2e

package harness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/stream"
)

// BootstrapFederation layers cross-site Sources onto each site's INBOX_*
// stream so events published to one site's OUTBOX flow into the peer's
// INBOX with the canonical chat.inbox.{site}.aggregate.> destination
// subject. Mirrors the prod-reference config from
// docs/superpowers/specs/2026-04-27-inbox-stream-ownership-design.md.
//
// Per amendment R1 9.A: no service in the repo bootstraps OUTBOX_{siteID}
// despite room-worker being the primary publisher. The harness owns its
// existence in e2e -- ensureOutbox runs before updateInbox so the cross-
// cluster Source has a target to resolve.
//
// Idempotent: CreateOrUpdateStream / UpdateStream rewrite the same config
// every call. INBOX_{siteID} must already exist (created by inbox-worker
// via BOOTSTRAP_STREAMS=true at startup); waitForStream gates on it.
func BootstrapFederation(ctx context.Context, stack *Stack) error {
	if err := ensureOutbox(ctx, stack.SiteA); err != nil {
		return fmt.Errorf("ensure OUTBOX siteA: %w", err)
	}
	if err := ensureOutbox(ctx, stack.SiteB); err != nil {
		return fmt.Errorf("ensure OUTBOX siteB: %w", err)
	}
	if err := updateInbox(ctx, stack.SiteA, stack.SiteB.SiteID); err != nil {
		return fmt.Errorf("federation siteA<-siteB: %w", err)
	}
	if err := updateInbox(ctx, stack.SiteB, stack.SiteA.SiteID); err != nil {
		return fmt.Errorf("federation siteB<-siteA: %w", err)
	}
	return nil
}

// ensureOutbox creates OUTBOX_{siteID} on the given site if it doesn't exist.
// Schema (Name + Subjects) only -- federation Sources are layered later by
// updateInbox.
func ensureOutbox(ctx context.Context, site SiteEndpoints) error {
	js, cleanup, err := jsForSite(site)
	if err != nil {
		return err
	}
	defer cleanup()

	cfg := stream.Outbox(site.SiteID)
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     cfg.Name,
		Subjects: cfg.Subjects,
	}); err != nil {
		return fmt.Errorf("create/update %s: %w", cfg.Name, err)
	}
	return nil
}

// updateInbox attaches a cross-cluster Source to the local INBOX that pulls
// from OUTBOX_<peerSiteID> via the supercluster gateway. SubjectTransform
// rewrites the cross-site subject namespace into the local chat.inbox.>
// hierarchy inbox-worker subscribes to.
func updateInbox(ctx context.Context, site SiteEndpoints, peerSiteID string) error {
	js, cleanup, err := jsForSite(site)
	if err != nil {
		return err
	}
	defer cleanup()

	cfg := stream.Inbox(site.SiteID)
	src := fmt.Sprintf("outbox.%s.to.%s.>", peerSiteID, site.SiteID)
	dst := fmt.Sprintf("chat.inbox.%s.aggregate.>", site.SiteID)

	// inbox-worker bootstraps INBOX at startup; wait for it before layering
	// federation on top. Workers don't have docker healthchecks (per R4.C)
	// so we can't gate on compose dependencies here.
	if err := waitForStream(ctx, js, cfg.Name); err != nil {
		return err
	}

	if _, err := js.UpdateStream(ctx, jetstream.StreamConfig{
		Name:     cfg.Name,
		Subjects: cfg.Subjects,
		Sources: []*jetstream.StreamSource{{
			Name: fmt.Sprintf("OUTBOX_%s", peerSiteID),
			SubjectTransforms: []jetstream.SubjectTransformConfig{{
				Source:      src,
				Destination: dst,
			}},
		}},
	}); err != nil {
		return fmt.Errorf("update stream %s: %w", cfg.Name, err)
	}
	return nil
}

// jsForSite opens a system-creds NATS connection + JetStream context for
// the given site, returning a cleanup closure the caller must defer.
func jsForSite(site SiteEndpoints) (jetstream.JetStream, func(), error) {
	conn, err := nats.Connect(site.NATSURL, nats.UserCredentials(site.NATSCredsFile))
	if err != nil {
		return nil, nil, fmt.Errorf("connect %s: %w", site.NATSURL, err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("jetstream context %s: %w", site.SiteID, err)
	}
	return js, func() { conn.Close() }, nil
}

// waitForStream polls for stream existence, with a generous timeout to allow
// inbox-worker startup. 30s covers worst-case container startup; longer
// waits indicate a real provisioning bug worth surfacing.
func waitForStream(ctx context.Context, js jetstream.JetStream, name string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := js.Stream(ctx, name); err == nil {
			return nil
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("stream %s did not appear within 30s: %w", name, errStreamTimeout)
}

var errStreamTimeout = errors.New("waitForStream timeout")
