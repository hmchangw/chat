package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/model"
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
//
// Probes are built via the same request_builder*.go entry points the
// per-tick body uses, so a future change to a scenario's wire shape
// (e.g. an added required field on CreateRoomRequest) propagates to
// the probe automatically — no second source of truth (D3 fix).
func buildReadinessProbe(scenario string, sub *model.Subscription, siteID string, r Requester) func(context.Context) error {
	user := model.User{Account: sub.User.Account, ID: sub.User.ID, SiteID: siteID}
	room := model.Room{ID: sub.RoomID, SiteID: siteID}
	switch scenario {
	case "history-read":
		// Smallest reasonable load — Limit=1 keeps the probe cheap.
		args := historyRequestArgs{User: user, Room: room, Limit: 1}
		subj, body, err := buildHistoryRequest(HistoryLoadHistory, &args)
		if err != nil {
			return func(_ context.Context) error { return err }
		}
		return func(ctx context.Context) error {
			_, perr := r.Request(ctx, subj, body, 2*time.Second)
			return perr
		}
	case "search-read":
		args := searchRequestArgs{User: user, Query: "probe", Size: 1}
		subj, body, err := buildSearchRequest(SearchMessagesKind, &args)
		if err != nil {
			return func(_ context.Context) error { return err }
		}
		return func(ctx context.Context) error {
			_, perr := r.Request(ctx, subj, body, 2*time.Second)
			return perr
		}
	case "room-rpc":
		args := roomRequestArgs{User: user, Room: room, SiteID: siteID}
		subj, body, err := buildRoomRequest(RoomsListKind, &args)
		if err != nil {
			return func(_ context.Context) error { return err }
		}
		return func(ctx context.Context) error {
			_, perr := r.Request(ctx, subj, body, 2*time.Second)
			return perr
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
