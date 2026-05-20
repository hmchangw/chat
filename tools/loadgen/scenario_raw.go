// tools/loadgen/scenario_raw.go
//
// raw-consistency scenario (Phase 3 §3.1): publishes a message and immediately
// polls multiple downstream read paths until visibility is observed, recording
// per-path lag and visibility-window into Prometheus histograms.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
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
// visibility window. On timeout it returns ErrRAWTimeout. Any lookup error
// other than ErrRAWNotVisible is surfaced immediately (wrapped) so callers
// can distinguish a transport failure from "not yet indexed" without waiting
// for the timeout.
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

	// Reuse a single timer across iterations rather than allocating one per
	// poll via time.After. At 10ms poll × 100rps × 60s that's 6M timer objects
	// in the GC; reuse keeps the hot path allocation-free.
	timer := time.NewTimer(interval)
	defer timer.Stop()
	if !timer.Stop() {
		// Drain the initial timer so the first Reset below starts fresh.
		<-timer.C
	}

	for {
		err := lookup(ctx, msgID)
		if err == nil {
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
		if !errors.Is(err, ErrRAWNotVisible) {
			return RAWOutcome{}, fmt.Errorf("RAW lookup: %w", err)
		}
		lastFailure = time.Now()
		pollCount++

		if time.Now().After(deadline) {
			return RAWOutcome{}, ErrRAWTimeout
		}

		timer.Reset(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return RAWOutcome{}, fmt.Errorf("RAW poll cancelled: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// Run is the generator's main loop. Per tick, picks a fixture subscription,
// publishes a SendMessageRequest via the frontdoor MsgSend subject, then
// spawns a poll goroutine that calls history-service GetMessageByID until
// the message is visible. Records per-path lag + visibility-window into
// Prometheus histograms labelled path="history".
func (g *rawConsistencyGenerator) Run(ctx context.Context) error {
	pollInterval := g.rf.RAW.PollInterval
	if pollInterval == 0 {
		pollInterval = 10 * time.Millisecond
	}
	pollTimeout := g.rf.RAW.Timeout
	if pollTimeout == 0 {
		pollTimeout = 5 * time.Second
	}
	requestTimeout := g.rf.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 2 * time.Second
	}

	rate := g.rf.Rate
	if rate <= 0 {
		rate = 10
	}
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	subs := g.deps.Fixtures().Subscriptions
	if len(subs) == 0 {
		// No fixtures — Run would spin without ever publishing. Block on ctx
		// so the scenario still respects shutdown semantics without panicking.
		<-ctx.Done()
		return nil
	}

	var wg sync.WaitGroup
	defer wg.Wait()

	var pickIdx int
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			sub := subs[pickIdx%len(subs)]
			pickIdx++

			messageID := idgen.GenerateMessageID()
			publishedAt := time.Now()

			if err := g.publishForRAW(ctx, &sub, messageID, publishedAt); err != nil {
				// Skip polls for failed publishes; publish errors are
				// already counted by the underlying Publisher metrics.
				continue
			}

			lookup := newHistoryLookup(g.deps.Requester(), &sub, g.deps.SiteID(), requestTimeout)
			wg.Add(1)
			go func(msgID string, pubAt time.Time) {
				defer wg.Done()
				outcome, err := pollUntilVisible(ctx, lookup, msgID, pubAt, pollInterval, pollTimeout)
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

// newHistoryLookup returns a LookupFn that polls history-service for the given
// messageID via GetMessageByID. We use GetMessageByID rather than a LoadHistory
// scan because:
//
//   - GetMessageByID is O(1) per poll on Cassandra (messages_by_id partition
//     key = message ID), so a 10ms poll interval can fire hundreds of times
//     per publish without overwhelming the SUT.
//   - LoadHistory would scan the room's recent window on every poll, which
//     amplifies the SUT load by the room's traffic rate and adds noise to the
//     RAW lag signal (history-service's read latency varies with page size).
//   - GetMessageByID is the same path a client uses for permalinks and "jump
//     to message" — it's the user-visible read path RAW is meant to measure.
//
// The lookup returns:
//
//   - nil                      — message is visible (success reply)
//   - ErrRAWNotVisible         — history-service returned a not_found app error
//   - <transport error wrap>   — NATS request failed (timeout, disconnect, etc.)
//   - <app error wrap>         — non-not_found app error (forbidden, internal);
//     surfaced so pollUntilVisible exits fast instead of burning the timeout
//     on a misconfiguration.
//
// Subject and body are precomputed once and reused across every poll iteration
// — closing over them avoids per-poll json.Marshal + fmt.Sprintf allocations
// on a hot path that can fire hundreds of times per published message.
func newHistoryLookup(
	requester Requester,
	sub *model.Subscription,
	siteID string,
	timeout time.Duration,
) LookupFn {
	subj := subject.MsgGet(sub.User.Account, sub.RoomID, siteID)
	return func(ctx context.Context, msgID string) error {
		body, err := json.Marshal(getMessageByIDRequestWire{MessageID: msgID})
		if err != nil {
			return fmt.Errorf("marshal GetMessageByID: %w", err)
		}
		reply, err := requester.Request(ctx, subj, body, timeout)
		if err != nil {
			return fmt.Errorf("history-service GetMessageByID: %w", err)
		}
		// natsrouter encodes handler errors as model.ErrorResponse. A
		// CodeNotFound reply means "message hasn't propagated yet" → treat
		// as not-visible. Any other coded error (forbidden, internal, …)
		// is a real problem; surface it so the poll loop fails fast.
		if errResp, ok := natsutil.TryParseError(reply); ok {
			if errResp.Code == natsrouter.CodeNotFound {
				return ErrRAWNotVisible
			}
			return fmt.Errorf("history-service GetMessageByID app error: %s", errResp.Error)
		}
		return nil
	}
}

// publishForRAW publishes a SendMessageRequest for the given messageID via the
// configured frontdoor MsgSend subject. RAW measures the user-visible read
// path, so we MUST publish through the frontdoor (not MESSAGES_CANONICAL): the
// scenario's lag signal is "time from publish to history-service visible",
// which includes gatekeeper validation + canonical persist. Publishing
// directly to canonical would understate the lag operators see in production.
func (g *rawConsistencyGenerator) publishForRAW(
	ctx context.Context,
	sub *model.Subscription,
	messageID string,
	_ time.Time,
) error {
	req := model.SendMessageRequest{
		ID:        messageID,
		Content:   "raw-consistency probe",
		RequestID: idgen.GenerateRequestID(),
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal SendMessageRequest: %w", err)
	}
	subj := subject.MsgSend(sub.User.Account, sub.RoomID, g.deps.SiteID())
	if err := g.deps.Publisher().Publish(ctx, subj, data); err != nil {
		return fmt.Errorf("publish frontdoor MsgSend: %w", err)
	}
	return nil
}
