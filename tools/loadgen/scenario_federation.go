// tools/loadgen/scenario_federation.go
//
// federation-lag scenario (Phase 3 §3.9): measures cross-site OUTBOX→INBOX
// replication lag across 4 stages on the destination site.
//
// Four lag stages:
//
//	stage=outbox  — publishedAt → observer sees event on siteA OUTBOX
//	stage=inbox   — siteA OUTBOX → event sourced into siteB INBOX
//	stage=persist — siteB INBOX → siteB canonical message persisted
//	stage=visible — siteB persist → readable via siteB.LoadHistory
//
// Sub-modes:
//
//	--federation-flap: every --flap-period kill site-b containers for
//	  --flap-down, restart, measure INBOX backlog drain.
//	--federation-cross-read: a user on site-a reads history of a room homed
//	  on site-b. Reports loadgen_federation_cross_read_seconds.
//
// CLAUDE.md §6 reserves Sources+SubjectTransforms for ops/IaC. The federation
// testcontainer test loads cross-site sourcing config from
// tools/loadgen/testdata/federation/streams.json via a setup helper — loadgen
// production code never touches federation policy.
//
// Phase 3.9 §3.9 (loadgen v2 plan).
package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// federationLagScenario measures cross-site OUTBOX→INBOX replication lag
// across 4 stages on the destination site.
type federationLagScenario struct{}

func (federationLagScenario) Name() string          { return "federation-lag" }
func (federationLagScenario) DefaultPreset() string { return "small" }

// NewGenerator constructs the federation-lag load generator.
// Returns an error if fewer than 2 sites are available — federation requires
// both a source site (site-a) and a destination site (site-b).
// Pass --federation-secondary-nats-url or run against the federation Compose
// overlay (docker-compose.federation.yml) to supply the second site.
func (federationLagScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	sites := deps.Sites()
	if len(sites) < 2 {
		return nil, errors.New("federation-lag scenario requires 2 sites; pass --federation-secondary-nats-url or run against the federation Compose overlay")
	}
	return &federationLagGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(federationLagScenario{}) }

// federationLagGenerator is the runner produced by federationLagScenario.
type federationLagGenerator struct {
	deps    ScenarioDeps
	rf      *runFlags
	tracker *federationStageTracker
}

// msgStageEntry holds the publish time and all observed stage timestamps for
// one in-flight message.
type msgStageEntry struct {
	publishedAt time.Time
	stages      map[string]time.Time
}

// federationStageTracker records per-(messageID, stage) timestamps to compute
// 4-stage replication lags. FIFO-eviction-bounded at cap entries (default 4096)
// to prevent unbounded memory growth under sustained publish rates.
//
// Concurrency: all methods are safe for concurrent use.
type federationStageTracker struct {
	mu      sync.Mutex
	entries map[string]*msgStageEntry
	order   []string // FIFO insertion order for eviction
	cap     int
}

// newFederationStageTracker creates a new tracker bounded at 4096 entries.
func newFederationStageTracker() *federationStageTracker {
	return &federationStageTracker{
		entries: make(map[string]*msgStageEntry),
		cap:     4096,
	}
}

// RecordPublish records the publish time for a message. If the tracker is at
// capacity, the oldest entry is evicted (FIFO) before inserting the new one.
// Duplicate calls for the same messageID are no-ops.
func (t *federationStageTracker) RecordPublish(messageID string, publishedAt time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.entries[messageID]; exists {
		return
	}
	if len(t.entries) >= t.cap {
		evict := t.order[0]
		t.order = t.order[1:]
		delete(t.entries, evict)
	}
	t.entries[messageID] = &msgStageEntry{
		publishedAt: publishedAt,
		stages:      make(map[string]time.Time),
	}
	t.order = append(t.order, messageID)
}

// RecordStage records when stage was observed for messageID.
// If messageID is not known (not yet published or already evicted), the call
// is a no-op.
func (t *federationStageTracker) RecordStage(messageID, stage string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if entry, ok := t.entries[messageID]; ok {
		entry.stages[stage] = at
	}
}

// Lags returns per-stage durations (seenAt - publishedAt) for messageID.
// Returns (nil, false) if the message is unknown or already evicted.
func (t *federationStageTracker) Lags(messageID string) (map[string]time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[messageID]
	if !ok {
		return nil, false
	}
	out := make(map[string]time.Duration, len(entry.stages))
	for stage, seenAt := range entry.stages {
		out[stage] = seenAt.Sub(entry.publishedAt)
	}
	return out, true
}

// Size returns the number of tracked message IDs currently held.
func (t *federationStageTracker) Size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// Run is a SKELETON. Real wire-up (Phase 3.9 follow-up):
//
//  1. Read deps.Sites()[0] (siteA) and Sites()[1] (siteB).
//  2. On siteA: subscribe to `outbox.{siteA}.>.created` → call
//     tracker.RecordStage(msgID, "outbox", now).
//  3. On siteB: subscribe to the INBOX subject → "inbox" stage.
//     Subscribe to siteB canonical-msg subject → "persist" stage.
//     Periodically poll siteB.Requester().LoadHistory → "visible" stage.
//  4. Publish via siteA.Publisher() at the run's configured rate.
//  5. On --federation-flap: every FlapPeriod, stop site-b services for FlapDown,
//     restart, measure backlog drain via the time from restart to first "inbox" hit.
//     Emit loadgen_federation_flap_drain_seconds.
//  6. On --federation-cross-read: separate path — user on siteA polls siteB
//     history → loadgen_federation_cross_read_seconds.
//
// For the Phase 3.9 initial landing, Run blocks until ctx is cancelled.
func (g *federationLagGenerator) Run(ctx context.Context) error {
	g.tracker = newFederationStageTracker()

	hist := g.deps.Metrics().FederationLag
	_ = hist
	_ = prometheus.Labels{}

	<-ctx.Done()
	return nil
}
