// tools/loadgen/scenario_search.go
//
// Registration for the "search-read" scenario. Phase 2 migration.
package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

// searchReadScenario issues search queries against the search-service index.
// Requires search-sync-worker to have indexed published messages.
type searchReadScenario struct{}

func (searchReadScenario) Name() string          { return "search-read" }
func (searchReadScenario) DefaultPreset() string { return "search-read" }

// NewGenerator constructs the search-read load generator from the ScenarioDeps
// and run flags. Extracted from the former "search-read" case in executeRun.
func (searchReadScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return NewSearchReadGenerator(&SearchReadConfig{
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
		Omission:       deps.Omission(),
	}, rf.Seed), nil
}

// BuildReadinessProbe returns a probe that sends one search RPC to verify
// the search service is reachable before the run starts.
// Migrated from buildReadinessProbe in readiness.go.
func (searchReadScenario) BuildReadinessProbe(deps ScenarioDeps) func(context.Context) error {
	f := deps.Fixtures()
	if len(f.Subscriptions) == 0 {
		return func(_ context.Context) error { return nil }
	}
	sub := f.Subscriptions[0]
	user := model.User{Account: sub.User.Account, ID: sub.User.ID, SiteID: deps.SiteID()}
	args := searchRequestArgs{User: user, Query: "probe", Size: 1}
	subj, body, err := buildSearchRequest(SearchMessagesKind, &args)
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
// checks.
func (s searchReadScenario) BuildLivenessProbe(deps ScenarioDeps) func(context.Context) error {
	return s.BuildReadinessProbe(deps)
}

// search-read does NOT implement AutoWarmer — it has no message-ID pool dependency.

func init() { RegisterScenario(searchReadScenario{}) }
