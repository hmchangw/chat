// tools/loadgen/scenario_history.go
//
// Registration for the "history-read" scenario. Phase 2 migration.
package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

type historyReadScenario struct{}

func (historyReadScenario) Name() string          { return "history-read" }
func (historyReadScenario) DefaultPreset() string { return "history-read" }

// NewGenerator constructs the history-read load generator from the ScenarioDeps
// and run flags. Extracted from the former "history-read" case in executeRun.
func (historyReadScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return NewHistoryReadGenerator(&HistoryReadConfig{
		Preset:         deps.Preset(),
		Fixtures:       *deps.Fixtures(),
		SiteID:         deps.SiteID(),
		Rate:           rf.Rate,
		Requester:      deps.Requester(),
		Metrics:        deps.Metrics(),
		Collector:      deps.Collector(),
		WarmupDeadline: deps.WarmupDeadline(),
		MaxInFlight:    deps.MaxInFlight(),
		Ramp:           rf.BuiltRamp,
		Timeout:        rf.RequestTimeout,
		MessageIDs:     deps.MessageIDs(),
		Omission:       deps.Omission(),
	}, rf.Seed), nil
}

// BuildReadinessProbe returns a probe that sends one history RPC (Limit=1)
// to verify the history service is reachable before the run starts.
// Migrated from buildReadinessProbe in readiness.go.
func (historyReadScenario) BuildReadinessProbe(deps ScenarioDeps) func(context.Context) error {
	f := deps.Fixtures()
	if len(f.Subscriptions) == 0 {
		return func(_ context.Context) error { return nil }
	}
	sub := f.Subscriptions[0]
	user := model.User{Account: sub.User.Account, ID: sub.User.ID, SiteID: deps.SiteID()}
	room := model.Room{ID: sub.RoomID, SiteID: deps.SiteID()}
	args := historyRequestArgs{User: user, Room: room, Limit: 1}
	subj, body, err := buildHistoryRequest(HistoryLoadHistory, &args)
	if err != nil {
		return func(_ context.Context) error { return err }
	}
	r := deps.Requester()
	return func(ctx context.Context) error {
		_, perr := r.Request(ctx, subj, body, 2*time.Second)
		return perr
	}
}

// BuildLivenessProbe reuses the readiness probe shape for mid-run liveness
// checks. Delegates to BuildReadinessProbe so there is a single source of
// truth for the wire format.
func (h historyReadScenario) BuildLivenessProbe(deps ScenarioDeps) func(context.Context) error {
	return h.BuildReadinessProbe(deps)
}

// NeedsAutoWarmup reports whether the configured preset requires a brief
// messaging-pipeline phase to populate the message-ID pool. Only true when
// HistoryMix includes at least one kind that needs a message ID
// (GetMessageByID / LoadSurroundingMessages / GetThreadMessages).
func (historyReadScenario) NeedsAutoWarmup(p *Preset) bool {
	for kind := range p.HistoryMix {
		if needsMessageID(kind) {
			return true
		}
	}
	return false
}

func init() { RegisterScenario(historyReadScenario{}) }
