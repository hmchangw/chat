package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// scenarioNeedsReadiness reports whether the given scenario benefits
// from a readiness probe. messaging-pipeline doesn't (its target is a
// JetStream consumer, not an RPC service); the read scenarios do.
func scenarioNeedsReadiness(scenario string) bool {
	switch scenario {
	case "history-read", "search-read", "room-rpc":
		return true
	default:
		return false
	}
}

// buildReadinessProbe returns a probe function that sends one RPC of the
// shape the chosen scenario will send during the run. A nil-return from
// the probe means the target service is reachable and replying.
func buildReadinessProbe(scenario string, sub *model.Subscription, siteID string, r Requester) func(context.Context) error {
	switch scenario {
	case "history-read":
		req, _ := json.Marshal(loadHistoryRequestWire{Limit: 1})
		subj := subject.MsgHistory(sub.User.Account, sub.RoomID, siteID)
		return func(ctx context.Context) error {
			_, err := r.Request(ctx, subj, req, 2*time.Second)
			return err
		}
	case "search-read":
		req, _ := json.Marshal(model.SearchMessagesRequest{SearchText: "probe", Size: 1})
		subj := subject.SearchMessages(sub.User.Account)
		return func(ctx context.Context) error {
			_, err := r.Request(ctx, subj, req, 2*time.Second)
			return err
		}
	case "room-rpc":
		subj := subject.RoomsList(sub.User.Account)
		return func(ctx context.Context) error {
			_, err := r.Request(ctx, subj, []byte(`{}`), 2*time.Second)
			return err
		}
	default:
		return func(_ context.Context) error { return nil }
	}
}

// ErrReadinessTimeout is returned when waitForReady gives up before the
// probe succeeds. Distinct from context.DeadlineExceeded so callers can
// differentiate "we hit our internal cap" from "ctx was cancelled
// upstream".
var ErrReadinessTimeout = errors.New("readiness probe deadline exceeded")

// readinessConfig is the parameter bundle for waitForReady. Probe is
// the per-attempt function; the orchestration of "retry with exponential
// backoff up to a deadline" lives in waitForReady.
type readinessConfig struct {
	Probe      func(ctx context.Context) error
	MinBackoff time.Duration // initial backoff between failed attempts
	MaxBackoff time.Duration // ceiling for exponential growth
}

// waitForReady calls cfg.Probe in a loop, with exponential backoff
// (cfg.MinBackoff doubling, capped at cfg.MaxBackoff), until the probe
// returns nil or ctx is cancelled. Returns nil on success,
// ErrReadinessTimeout if ctx expired with the cause set to deadline,
// or the wrapped ctx error if the upstream cancelled.
//
// Phase 3 §3.3: this is the readiness probe used to wait for the
// services-under-test to be reachable before generator traffic starts,
// avoiding first-second error spikes after `compose up`.
func waitForReady(ctx context.Context, cfg *readinessConfig) error {
	if cfg.Probe == nil {
		return fmt.Errorf("readinessConfig.Probe must be set")
	}
	backoff := cfg.MinBackoff
	if backoff <= 0 {
		backoff = 200 * time.Millisecond
	}
	maxB := cfg.MaxBackoff
	if maxB <= 0 {
		maxB = 2 * time.Second
	}

	for {
		err := cfg.Probe(ctx)
		if err == nil {
			return nil
		}
		// Wait before retrying.
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ErrReadinessTimeout
			}
			return fmt.Errorf("readiness aborted: %w", ctx.Err())
		case <-t.C:
		}
		backoff *= 2
		if backoff > maxB {
			backoff = maxB
		}
	}
}
