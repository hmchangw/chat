// tools/loadgen/scenario_largeroom.go
//
// large-room-broadcast scenario (Phase 3 §3.2): maintains long-lived
// per-member subscriptions for ALL recipients of a target room, publishes via
// standard frontdoor, and tracks the fraction of members that received the
// message at p99 receive-time.
//
// Three preset variants model different traffic shapes:
//   - announce-room: 10k members, 1 write/s (spec: ~1/min; Rate=1 is the
//     closest granularity in the per-second rate field)
//   - firehose-room: 1k members, 50 writes/s (moderate, balanced)
//   - bot-room: 100 members, 200 writes/s (high write from few senders)
package main

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

// largeRoomScenario maintains long-lived per-member subscriptions for ALL
// recipients of a target room, publishes via frontdoor, and tracks the
// fraction of members that received the message at p99 receive-time.
//
// IMPORTANT: With 10k members, all subscriptions on one NATS connection
// overflows the slow-consumer threshold. Spread across --connections=N
// via the existing ConnPool. For Phase 3.2's skeleton, the long-lived
// subscription setup is a STUB — TODO follow-up wires real per-member
// member-inbox subscriptions using Runtime.Subscribers().Subscribe.
type largeRoomScenario struct{}

func (largeRoomScenario) Name() string          { return "large-room-broadcast" }
func (largeRoomScenario) DefaultPreset() string { return "firehose-room" }

// NewGenerator constructs the large-room-broadcast load generator.
func (largeRoomScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &largeRoomGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(largeRoomScenario{}) }

// largeRoomGenerator is the runner produced by largeRoomScenario.
type largeRoomGenerator struct {
	deps    ScenarioDeps
	rf      *runFlags
	tracker *completionTracker
}

// completionTracker records per-message member-receive timestamps. Computes
// (p99-latency, completion-ratio-at-p99) snapshots on demand.
type completionTracker struct {
	expectedMembers int
	mu              sync.Mutex
	// perMessage: message-ID -> [member-index]receivedAt (zero if not yet received)
	perMessage map[string][]time.Time
}

func newCompletionTracker(expectedMembers int) *completionTracker {
	return &completionTracker{
		expectedMembers: expectedMembers,
		perMessage:      map[string][]time.Time{},
	}
}

// Observe records that memberIdx received messageID at receivedAt.
// Out-of-range memberIdx is silently dropped (caller's bug, not a hot-path concern).
func (t *completionTracker) Observe(messageID string, memberIdx int, receivedAt time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	timestamps, ok := t.perMessage[messageID]
	if !ok {
		timestamps = make([]time.Time, t.expectedMembers)
		t.perMessage[messageID] = timestamps
	}
	if memberIdx < 0 || memberIdx >= len(timestamps) {
		return
	}
	timestamps[memberIdx] = receivedAt
}

// Snapshot returns (p99 latency from publishedAt, ratio of members at-or-below p99).
// For messages with no observations, returns (0, 0).
func (t *completionTracker) Snapshot(messageID string, publishedAt time.Time) (time.Duration, float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	timestamps, ok := t.perMessage[messageID]
	if !ok {
		return 0, 0
	}
	// Collect non-zero latencies.
	latencies := make([]time.Duration, 0, len(timestamps))
	for _, ts := range timestamps {
		if ts.IsZero() {
			continue
		}
		latencies = append(latencies, ts.Sub(publishedAt))
	}
	if len(latencies) == 0 {
		return 0, 0
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	// p99 = 99th percentile of received latencies (nearest-rank, 1-indexed).
	// Formula: ceil(n × 0.99) gives the 1-indexed rank; subtract 1 for 0-indexed.
	// Example: n=100 → ceil(99) − 1 = 98 (10th-from-last), not 99 (the outlier).
	n := len(latencies)
	p99Index := int(math.Ceil(float64(n)*0.99)) - 1
	if p99Index < 0 {
		p99Index = 0
	}
	if p99Index >= n {
		p99Index = n - 1
	}
	p99 := latencies[p99Index]
	// Completion ratio at p99 = fraction of expected members who received by p99 deadline.
	// (Equivalent to: count of latencies ≤ p99 / expectedMembers)
	receivedByP99 := 0
	for _, lat := range latencies {
		if lat <= p99 {
			receivedByP99++
		}
	}
	ratio := float64(receivedByP99) / float64(t.expectedMembers)
	return p99, ratio
}

// errSubsChurnConflict is a sentinel for the eventual subscription-churn-detection
// logic. Returned when large-room-broadcast detects a concurrent subscription-churn
// scenario is running (subscription freeze required). Not yet detected today —
// the detection wire-up is a Phase 3.2 follow-up.
var errSubsChurnConflict = errors.New("large-room-broadcast refuses to run alongside subscription-churn scenario; subscription freeze required")

// Run is the generator's main loop.
//
// Phase 3.2 SKELETON. Real wire-up (per-member long-lived NATS subs
// via runtime.Subscribers, completion-tracker integration with the
// publish path) is a follow-up. For the skeleton, this Run:
//  1. Initializes the completion tracker.
//  2. Does NOT actually subscribe (would overwhelm NATS without
//     ConnPool spreading — proper wire-up needs that integration).
//  3. Returns when ctx is cancelled.
//
// TODO Phase 3.2 follow-up:
//   - Detect subscription-churn co-run; return errSubsChurnConflict.
//   - Iterate Fixtures.Subscriptions for the target large room.
//   - For each (member, room) pair, subscribe via
//     g.deps.Subscribers().Subscribe(memberInboxSubject(member, room), handler)
//     where the handler calls g.tracker.Observe(messageID, memberIdx, time.Now()).
//   - At each publish, after publishing, capture publishedAt and start
//     a goroutine that, after some "settle window" (e.g., 5s), calls
//     g.tracker.Snapshot(messageID, publishedAt) and emits the metrics
//     via g.deps.Metrics().LargeRoomReceive and LargeRoomCompletion.
func (g *largeRoomGenerator) Run(ctx context.Context) error {
	// placeholder until member count is wired from Fixtures
	g.tracker = newCompletionTracker(0)

	// errSubsChurnConflict referenced here to suppress the "unused" linter
	// error while the detection logic is stubbed. The follow-up commit
	// replaces this with the real concurrent-scenario check.
	_ = errSubsChurnConflict

	<-ctx.Done()
	return nil
}
