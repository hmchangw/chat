// tools/loadgen/scenario_notif.go
//
// notification-fanout scenario (Phase 3 §3.4): publishes messages and
// subscribes to per-recipient notification subjects to measure
// publish→notification fanout latency.
//
// §3.4a (in-app): subscribes to subject.Notification(account) for every
// fixture user account, records receipt lag into
// loadgen_notification_lag_seconds{channel="inapp"}.
//
// §3.4b (push/email): see scenario_notif_routing.go (build-tagged skeleton
// because pkg/subject does not yet expose per-channel routing builders).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// notificationFanoutScenario publishes messages and subscribes to
// per-recipient notification subjects to measure publish→notification
// fanout latency.
//
// §3.4a (in-app): subscribes to subject.Notification(account) for every
// fixture user account, records receipt lag into
// loadgen_notification_lag_seconds{channel="inapp"}.
//
// §3.4b (push/email): see scenario_notif_routing.go (build-tagged skeleton).
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
// FIFO eviction to avoid memory growth on long runs (cap 4096 entries).
//
// IMPORTANT: LagFor is read-only and does NOT consume entries, so the same
// (messageID, publishedAt) record serves all N recipients' subscribers
// (one observation per actual receipt — N × tick-count cardinality).
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
// or (0, false) if the message wasn't tracked. Read-only — does NOT consume
// the tracker entry, so the same message can be observed once per recipient.
func (t *notifLagTracker) LagFor(messageID string, receivedAt time.Time) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	publishedAt, ok := t.publish[messageID]
	if !ok {
		return 0, false
	}
	return receivedAt.Sub(publishedAt), true
}

// newNotificationHandler returns a nats.MsgHandler that decodes a
// NotificationEvent payload, looks up its publish time in the tracker,
// and calls observe with the resulting lag when found. Malformed payloads
// and unknown message IDs are dropped silently — receivedAt is taken once
// at handler entry so it is not skewed by tracker contention.
func newNotificationHandler(tracker *notifLagTracker, observe func(time.Duration)) nats.MsgHandler {
	return func(msg *nats.Msg) {
		receivedAt := time.Now().UTC()
		var evt model.NotificationEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			// Drop malformed events; the SUT should never produce them, but
			// loadgen is a measurement tool and must not panic on garbage.
			return
		}
		if evt.Message.ID == "" {
			return
		}
		lag, ok := tracker.LagFor(evt.Message.ID, receivedAt)
		if !ok {
			return
		}
		observe(lag)
	}
}

// subscribeFunc is the minimum surface notificationFanoutGenerator needs
// from Subscribers — declared here for testability so the tick loop can be
// exercised without a real *nats.Conn.
type subscribeFunc func(subject string, handler func(*nats.Msg)) error

// subscribeAllAccounts subscribes the supplied handler to
// subject.Notification(account) for every UNIQUE account in fixtures.Users.
// Returns the count of subscriptions established.
//
// Dedup is by account string (a single user may appear in many fixture
// subscriptions across rooms, but the notification subject is per-account
// so only one sub is needed).
func subscribeAllAccounts(fixtures *Fixtures, sub subscribeFunc, handler func(*nats.Msg)) (int, error) {
	seen := make(map[string]struct{}, len(fixtures.Users))
	count := 0
	for i := range fixtures.Users {
		account := fixtures.Users[i].Account
		if account == "" {
			continue
		}
		if _, dup := seen[account]; dup {
			continue
		}
		seen[account] = struct{}{}
		if err := sub(subject.Notification(account), handler); err != nil {
			return count, fmt.Errorf("subscribe notification for %s: %w", account, err)
		}
		count++
	}
	return count, nil
}

// publishNotifTick publishes one frontdoor message and registers its
// message-ID with the tracker so subscribers can compute lag on receipt.
// Mentions are injected per-message at the configured Preset.MentionRate
// — the notification-worker SUT fans out to every room member regardless
// of mention status, but the mention payload exercises the worker's
// mention-extraction codepath which can drive realistic CPU profile.
//
// No-op when there are no fixture subscriptions.
func (g *notificationFanoutGenerator) publishNotifTick(ctx context.Context) error {
	fixtures := g.deps.Fixtures()
	if fixtures == nil || len(fixtures.Subscriptions) == 0 {
		return nil
	}
	// #nosec G404 -- load-test subscription picker; reproducibility via --seed requires deterministic math/rand, no security context
	subIdx := rand.IntN(len(fixtures.Subscriptions))
	sub := fixtures.Subscriptions[subIdx]

	content := "loadgen notification-fanout probe"
	preset := g.deps.Preset()
	// #nosec G404 -- load-test mention-rate dice; reproducibility via --seed requires deterministic math/rand, no security context
	if preset != nil && preset.MentionRate > 0 && rand.Float64() < preset.MentionRate {
		// Mention a different fixture user — fall back to self if there
		// is only one user in the pool.
		other := sub.User.Account
		if len(fixtures.Users) > 1 {
			// #nosec G404 -- load-test mention-target picker; reproducibility via --seed requires deterministic math/rand, no security context
			otherIdx := rand.IntN(len(fixtures.Users))
			if fixtures.Users[otherIdx].Account == sub.User.Account && otherIdx+1 < len(fixtures.Users) {
				otherIdx++
			}
			other = fixtures.Users[otherIdx].Account
		}
		content = "hey @" + other + " " + content
	}

	msgID := idgen.GenerateMessageID()
	reqID := idgen.GenerateRequestID()
	req := model.SendMessageRequest{ID: msgID, Content: content, RequestID: reqID}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal send-message request: %w", err)
	}

	publishedAt := time.Now().UTC()
	subj := subject.MsgSend(sub.User.Account, sub.RoomID, g.deps.SiteID())

	// Record BEFORE publish so a fast SUT can't deliver the notification
	// before LagFor can find the entry.
	g.tracker.RecordPublished(msgID, publishedAt)

	if perr := g.deps.Publisher().Publish(ctx, subj, data); perr != nil {
		return fmt.Errorf("publish notification probe: %w", perr)
	}
	return nil
}

// Run subscribes to per-account notification subjects (one per unique
// fixture account), then publishes user messages at the configured rate.
// Each received notification observes lag into
// loadgen_notification_lag_seconds{channel="inapp"}.
//
// A single publish fans out to N recipients (everyone in the room except
// the sender) — each recipient's notification yields a separate lag
// observation. Histogram cardinality is N × tick-count.
func (g *notificationFanoutGenerator) Run(ctx context.Context) error {
	g.tracker = newNotifLagTracker()

	inapp, err := g.deps.Metrics().NotificationLag.CurryWith(prometheus.Labels{"channel": "inapp"})
	if err != nil {
		return fmt.Errorf("curry notification lag histogram: %w", err)
	}
	observe := func(lag time.Duration) {
		inapp.WithLabelValues().Observe(lag.Seconds())
	}
	handler := newNotificationHandler(g.tracker, observe)

	subs := g.deps.Subscribers()
	if subs != nil {
		subFn := func(subj string, h func(*nats.Msg)) error {
			_, serr := subs.Subscribe(subj, h)
			return serr
		}
		count, serr := subscribeAllAccounts(g.deps.Fixtures(), subFn, handler)
		if serr != nil {
			return fmt.Errorf("subscribe per-account notification subjects: %w", serr)
		}
		slog.Info("notification-fanout subscribed",
			"accounts", count,
			"channel", "inapp")
	} else {
		// Tests may pass a nil Subscribers; surface a warn so it's clear
		// no in-app observations will be made in that path.
		slog.Warn("notification-fanout has no Subscribers; tick loop will record publishes but not observe lag")
	}

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
			if perr := g.publishNotifTick(ctx); perr != nil {
				slog.Error("notification-fanout publish tick failed", "error", perr)
			}
		}
	}
}
