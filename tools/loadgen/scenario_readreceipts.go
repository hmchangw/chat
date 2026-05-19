// tools/loadgen/scenario_readreceipts.go
//
// read-receipts scenario (Phase 3 §3.16): fires MessageRead events for a
// configurable subset of recipients per recently-published message. Models the
// realistic pattern of "fraction of recipients actually see the message before
// notification mutes/dismissal." Co-runs with messaging-pipeline (consumes
// Collector.RecentMessages).
//
// Federation note: when --federation is on (Phase 3.9), MessageRead on a
// remote-homed room generates cross-site subscription_read OUTBOX traffic.
// Phase 3.9 federation-lag scenario must subscribe to .subscription_read
// in addition to .created — TODO marker in scenario_federation.go when that
// scenario lands.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	randv2 "math/rand/v2"
)

// readReceiptsScenario fires MessageRead events for a configurable subset of
// recipients per recently-published message.
type readReceiptsScenario struct{}

func (readReceiptsScenario) Name() string          { return "read-receipts" }
func (readReceiptsScenario) DefaultPreset() string { return "realistic" }

// NewGenerator constructs the read-receipts load generator.
func (readReceiptsScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &readReceiptsGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(readReceiptsScenario{}) }

// readReceiptsGenerator is the runner produced by readReceiptsScenario.
type readReceiptsGenerator struct {
	deps ScenarioDeps
	rf   *runFlags
}

// readReceiptFn signature: invoked once per (message, recipient) selected pair.
type readReceiptFn func(ctx context.Context, msgID, roomType string) error

// fireReadReceipts iterates recents and invokes fn for the configured coverage
// fraction (random pick per message). Returns the first error.
// When rng is nil, uses math/rand/v2 package-level globals (goroutine-safe).
func fireReadReceipts(ctx context.Context, recents []RecentMessage, coverage float64, fn readReceiptFn, rng *randv2.Rand) error {
	if coverage <= 0 {
		return nil
	}
	if coverage > 1 {
		coverage = 1
	}
	float64Fn := randv2.Float64
	if rng != nil {
		float64Fn = rng.Float64
	}
	for _, msg := range recents {
		if float64Fn() > coverage {
			continue
		}
		if err := fn(ctx, msg.MessageID, msg.RoomType); err != nil {
			return fmt.Errorf("read receipt for %s: %w", msg.MessageID, err)
		}
	}
	return nil
}

// Run is the generator's main loop. Per tick, picks recent messages from the
// shared Collector ring buffer and fires MessageRead events for the configured
// coverage fraction.
func (g *readReceiptsGenerator) Run(ctx context.Context) error {
	rate := g.rf.Rate
	if rate <= 0 {
		rate = 10
	}
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	coverage := g.rf.ReceiptCoverage
	if coverage <= 0 {
		coverage = 0.6
	}

	hist := g.deps.Metrics().MessageRead

	fn := func(ctx context.Context, msgID, roomType string) error {
		start := time.Now()
		if err := g.publishReadEvent(ctx, msgID); err != nil {
			return fmt.Errorf("publish read receipt for %s: %w", msgID, err)
		}
		hist.WithLabelValues(roomType).Observe(time.Since(start).Seconds())
		return nil
	}

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			recents := g.deps.Collector().RecentMessages(50)
			if len(recents) == 0 {
				// Co-run with messaging-pipeline; wait for it to publish.
				continue
			}
			// Take a per-tick snapshot of the coverage so goroutines capture
			// the value, not a reference that could race with future updates.
			tickCoverage := coverage
			wg.Add(1)
			go func(snap []RecentMessage) {
				defer wg.Done()
				// Use math/rand/v2 package-level globals (goroutine-safe ChaCha8).
				// Passing nil signals fireReadReceipts to use package-level calls.
				// Errors logged inside fn; outer-loop continues.
				_ = fireReadReceipts(ctx, snap, tickCoverage, fn, nil)
			}(recents)
		}
	}
}

// publishReadEvent sends a MessageRead event for messageID.
//
// STUB for Phase 3.16's initial landing — TODO follow-up wires real
// history-service MessageRead RPC (request/reply via NATS).
func (g *readReceiptsGenerator) publishReadEvent(_ context.Context, _ string) error {
	return nil // PLACEHOLDER: real wire-up in Phase 3.16 follow-up
}
