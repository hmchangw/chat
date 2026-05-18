// tools/loadgen/scenario_raw.go
//
// raw-consistency scenario (Phase 3 §3.1): publishes a message and immediately
// polls multiple downstream read paths until visibility is observed, recording
// per-path lag and visibility-window into Prometheus histograms.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/idgen"
)

// rawConsistencyScenario publishes a message via frontdoor and immediately spawns
// poll goroutines that retry the read until visibility is observed. Records
// per-path lag (first-visible − published) and visibility-window
// (first-visible − last-not-visible) into Prometheus histograms.
type rawConsistencyScenario struct{}

func (rawConsistencyScenario) Name() string          { return "raw-consistency" }
func (rawConsistencyScenario) DefaultPreset() string { return "small" }

// NewGenerator constructs the raw-consistency load generator.
func (rawConsistencyScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &rawConsistencyGenerator{
		deps: deps,
		rf:   rf,
	}, nil
}

func init() { RegisterScenario(rawConsistencyScenario{}) }

// ------------------------------------------------------------------ generator

type rawConsistencyGenerator struct {
	deps ScenarioDeps
	rf   *runFlags
}

// LookupFn polls a single read path for the given message ID.
// Returns ErrRAWNotVisible when not yet visible, nil when visible, or another
// error on transport failure.
type LookupFn func(ctx context.Context, messageID string) error

var (
	// ErrRAWNotVisible is returned by a LookupFn when the message has not yet
	// propagated to the read path being polled.
	ErrRAWNotVisible = errors.New("message not yet visible to this read path")

	// ErrRAWTimeout is returned by pollUntilVisible when the poll deadline
	// passes without observing visibility.
	ErrRAWTimeout = errors.New("RAW poll timed out without observing visibility")
)

// RAWOutcome captures the timing signals from a single poll-until-visible run.
type RAWOutcome struct {
	// Lag is the duration from publishedAt to the first successful lookup.
	Lag time.Duration
	// VisibilityWindow is the duration from the last failed lookup to the
	// first successful lookup. Zero when visible on the very first poll.
	VisibilityWindow time.Duration
	// PollsBeforeHit is the total number of lookups performed (including the
	// successful one).
	PollsBeforeHit int
}

// pollUntilVisible polls lookup every interval until it returns nil (visible)
// or publishedAt+timeout is exceeded. On success it returns the lag and
// visibility window. On timeout it returns ErrRAWTimeout.
func pollUntilVisible(
	ctx context.Context,
	lookup LookupFn,
	msgID string,
	publishedAt time.Time,
	interval time.Duration,
	timeout time.Duration,
) (RAWOutcome, error) {
	deadline := publishedAt.Add(timeout)
	var lastFailure time.Time
	pollCount := 0

	for {
		if err := lookup(ctx, msgID); err == nil {
			firstVisible := time.Now()
			window := time.Duration(0)
			if !lastFailure.IsZero() {
				window = firstVisible.Sub(lastFailure)
			}
			return RAWOutcome{
				Lag:              firstVisible.Sub(publishedAt),
				VisibilityWindow: window,
				PollsBeforeHit:   pollCount + 1,
			}, nil
		}
		lastFailure = time.Now()
		pollCount++

		if time.Now().After(deadline) {
			return RAWOutcome{}, ErrRAWTimeout
		}

		select {
		case <-ctx.Done():
			return RAWOutcome{}, fmt.Errorf("RAW poll cancelled: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

// Run is the generator's main loop. Per tick, publishes a message and spawns
// a poll goroutine per registered read path that records lag + visibility-window.
func (g *rawConsistencyGenerator) Run(ctx context.Context) error {
	pollInterval := g.rf.RAW.PollInterval
	if pollInterval == 0 {
		pollInterval = 10 * time.Millisecond
	}
	pollTimeout := g.rf.RAW.Timeout
	if pollTimeout == 0 {
		pollTimeout = 5 * time.Second
	}

	rate := g.rf.Rate
	if rate <= 0 {
		rate = 10
	}
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			messageID := idgen.GenerateMessageID()
			publishedAt := time.Now()

			if err := g.publishForRAW(ctx, messageID, publishedAt); err != nil {
				// Skip polls for failed publishes; the publish error is
				// already counted by the underlying Publisher metrics.
				continue
			}

			wg.Add(1)
			go func(msgID string, pubAt time.Time) {
				defer wg.Done()
				outcome, err := pollUntilVisible(ctx, g.lookupHistory, msgID, pubAt, pollInterval, pollTimeout)
				if err != nil {
					return
				}
				m := g.deps.Metrics()
				m.RAWLag.WithLabelValues("history").Observe(outcome.Lag.Seconds())
				m.RAWVisibilityWindow.WithLabelValues("history").Observe(outcome.VisibilityWindow.Seconds())
			}(messageID, publishedAt)
		}
	}
}

// lookupHistory polls history-service for the message ID via NATS request/reply.
// Returns ErrRAWNotVisible when not yet present, nil when visible, or another
// error on transport failure.
//
// TODO Phase 3.1 follow-up: wire the real history-service get-by-id endpoint
// (or a LoadHistory scan with a small limit) using the Requester pattern from
// read_generator.go. The current stub always returns ErrRAWNotVisible so the
// poll loop runs until timeout in live operation — the scaffolding is complete
// and compile-clean.
func (g *rawConsistencyGenerator) lookupHistory(_ context.Context, _ string) error {
	return ErrRAWNotVisible // TODO Phase 3.1 follow-up: wire real history-service lookup
}

// publishForRAW publishes a message for the given ID via the configured
// injection path (frontdoor) so the downstream pipeline can propagate it to
// the read paths being polled.
//
// TODO Phase 3.1 follow-up: build a SendMessageRequest using deps.Publisher()
// and the fixture subscriptions, mirroring the messaging-pipeline generator's
// publish pattern. The current stub returns nil so the poll goroutine is
// spawned but always observes ErrRAWNotVisible (safe no-op in dev).
func (g *rawConsistencyGenerator) publishForRAW(_ context.Context, _ string, _ time.Time) error {
	return nil // TODO Phase 3.1 follow-up: wire real frontdoor publish
}
