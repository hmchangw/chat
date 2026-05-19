// tools/loadgen/scenario_notif.go
//
// notification-fanout scenario (Phase 3 §3.4): publishes messages with a
// configurable mention rate and subscribes to per-recipient notification
// subjects to measure publish→notification fanout latency.
//
// §3.4a (in-app): subscribes to subject.Notification(account) for every
// fixture user, records receipt lag into loadgen_notification_lag_seconds
// {channel="inapp"}.
//
// §3.4b (push/email): see scenario_notif_routing.go (build-tagged skeleton
// because pkg/subject does not yet expose per-channel routing builders).
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// notificationFanoutScenario publishes messages with a configurable mention
// rate and subscribes to per-recipient notification subjects to measure
// publish→notification fanout latency.
//
// §3.4a (in-app): subscribes to subject.Notification(account) for every
// fixture user, records receipt lag into loadgen_notification_lag_seconds
// {channel="inapp"}.
//
// §3.4b (push/email): see scenario_notif_routing.go (build-tagged skeleton
// if pkg/subject doesn't yet expose per-channel routing).
type notificationFanoutScenario struct{}

func (notificationFanoutScenario) Name() string          { return "notification-fanout" }
func (notificationFanoutScenario) DefaultPreset() string { return "realistic" }

func (notificationFanoutScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &notificationFanoutGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(notificationFanoutScenario{}) }

type notificationFanoutGenerator struct {
	deps    ScenarioDeps
	rf      *runFlags
	tracker *notifLagTracker
}

// notifLagTracker records publish times per message-ID so subscribers can
// compute lag = receivedAt - publishedAt on receipt. Bounded by a soft
// LRU-style eviction to avoid memory growth on long runs (cap 4096 entries).
type notifLagTracker struct {
	mu      sync.Mutex
	publish map[string]time.Time
	cap     int
	order   []string // insertion-order for FIFO eviction (simpler than true LRU)
}

func newNotifLagTracker() *notifLagTracker {
	return &notifLagTracker{
		publish: map[string]time.Time{},
		cap:     4096,
	}
}

// RecordPublished stores publishedAt for the given messageID, subject to the
// FIFO cap (4096 entries). Duplicate registrations for the same ID are no-ops.
func (t *notifLagTracker) RecordPublished(messageID string, publishedAt time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.publish[messageID]; exists {
		return
	}
	if len(t.publish) >= t.cap {
		// Evict oldest by FIFO.
		evict := t.order[0]
		t.order = t.order[1:]
		delete(t.publish, evict)
	}
	t.publish[messageID] = publishedAt
	t.order = append(t.order, messageID)
}

// LagFor returns the lag from publish to receivedAt for the given message,
// or (0, false) if the message wasn't tracked.
func (t *notifLagTracker) LagFor(messageID string, receivedAt time.Time) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	publishedAt, ok := t.publish[messageID]
	if !ok {
		return 0, false
	}
	return receivedAt.Sub(publishedAt), true
}

// Run is the generator's main loop.
//
// Phase 3.4a SKELETON. Real wire-up:
//  1. For every fixture user, subscribe to subject.Notification(user.Account)
//     via g.deps.Subscribers().Subscribe(subj, handler).
//  2. handler: extract messageID from payload, call tracker.LagFor,
//     observe into hist if found.
//  3. Tick loop publishes messages with MentionRate-driven mention payload;
//     after publish, call tracker.RecordPublished(msgID, publishedAt).
//
// TODO Phase 3.4 follow-up: wire real subject.Notification subscribe path
// and the publish-side mention injection (depends on the messaging-pipeline
// generator's publishOne which we can either co-run or duplicate-and-trim).
func (g *notificationFanoutGenerator) Run(ctx context.Context) error {
	g.tracker = newNotifLagTracker()

	inapp, err := g.deps.Metrics().NotificationLag.CurryWith(prometheus.Labels{"channel": "inapp"})
	if err != nil {
		return fmt.Errorf("curry notification lag histogram: %w", err)
	}
	_ = inapp // wired when subscriber goroutine is implemented

	rate := g.rf.Rate
	if rate <= 0 {
		rate = 10
	}
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// STUB tick: once wire-up lands, this becomes a real publish +
			// tracker.RecordPublished call. No-op for now.
		}
	}
}
