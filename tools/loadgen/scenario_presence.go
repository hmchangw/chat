// tools/loadgen/scenario_presence.go
//
//go:build presence_ready

// Package main: presence-typing scenario.
//
// PHASE 3.3 GATING NOTE:
// This file compiles only with build tag `presence_ready` because pkg/subject
// does not yet define presence/typing subject builders. When the SUT adds:
//   - subject.PresenceUpdate(account, roomID)
//   - subject.TypingActive(account, roomID)
//   - subject.PresenceSubscribe(roomID)
//
// flip the build tag (i.e., remove it or change to `//go:build !presence_ready`)
// and replace the stub `presenceSubjectsAvailable()` in scenario_roomopen.go
// (Task 3.14) to return true.
//
// See docs/scenarios/presence-typing.md for the design + subject contract.
//
// Phase 3 §3.3 (loadgen v2 plan).
package main

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// presenceTypingScenario emits per-user typing events at a token-bucket-limited
// rate, plus periodic presence subscribes/unsubscribes. Records fanout latency
// (emit → observer-receipt) into loadgen_presence_fanout_seconds.
type presenceTypingScenario struct{}

func (presenceTypingScenario) Name() string          { return "presence-typing" }
func (presenceTypingScenario) DefaultPreset() string { return "realistic" }

func (presenceTypingScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &presenceTypingGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(presenceTypingScenario{}) }

type presenceTypingGenerator struct {
	deps ScenarioDeps
	rf   *runFlags
}

// Run is a SKELETON. Once presence subjects land in pkg/subject:
//  1. Build a token-bucket-per-user rate limiter sized by --typing-bucket-period
//     (default: 1 token / 3s).
//  2. For the configured --typing-active-users subset, emit TypingActive
//     events at the bucket rate.
//  3. For every fixture user, subscribe to subject.PresenceSubscribe(roomID)
//     via runtime.Subscribers and record (msgID, recipient, receivedAt) into
//     a fanout tracker (similar to large-room's completionTracker).
//  4. Periodically emit PresenceUpdate; expect observer-side latencies in
//     loadgen_presence_fanout_seconds{preset} histogram.
//  5. Compute presence_delivered_ratio = (observed / expected) and expose
//     as loadgen_presence_delivered_ratio gauge.
func (g *presenceTypingGenerator) Run(ctx context.Context) error {
	// Placeholder — real wiring is gated on pkg/subject having presence builders.
	// See file-header doc for the contract this skeleton expects.
	_ = sync.WaitGroup{}
	_ = rand.IntN
	_ = time.Now
	_ = prometheus.Labels{}
	<-ctx.Done()
	return nil
}
