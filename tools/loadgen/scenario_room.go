// tools/loadgen/scenario_room.go
//
// Registration for the "room-rpc" scenario. Phase 2 migration.
package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

// roomRPCScenario exercises room-service request/reply paths (room info,
// member list, subscription updates) without publishing messages.
type roomRPCScenario struct{}

func (roomRPCScenario) Name() string          { return "room-rpc" }
func (roomRPCScenario) DefaultPreset() string { return "room-rpc" }

// NewGenerator constructs the room-rpc load generator from the ScenarioDeps
// and run flags. Extracted from the former "room-rpc" case in executeRun.
func (roomRPCScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return NewRoomRPCGenerator(&RoomRPCConfig{
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

// BuildReadinessProbe returns a probe that sends one room-list RPC to verify
// the room service is reachable before the run starts.
// Migrated from buildReadinessProbe in readiness.go.
func (roomRPCScenario) BuildReadinessProbe(deps ScenarioDeps) func(context.Context) error {
	f := deps.Fixtures()
	if len(f.Subscriptions) == 0 {
		return func(_ context.Context) error { return nil }
	}
	sub := f.Subscriptions[0]
	user := model.User{Account: sub.User.Account, ID: sub.User.ID, SiteID: deps.SiteID()}
	room := model.Room{ID: sub.RoomID, SiteID: deps.SiteID()}
	args := roomRequestArgs{User: user, Room: room, SiteID: deps.SiteID()}
	subj, body, err := buildRoomRequest(RoomsListKind, &args)
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
func (r roomRPCScenario) BuildLivenessProbe(deps ScenarioDeps) func(context.Context) error {
	return r.BuildReadinessProbe(deps)
}

// room-rpc does NOT implement AutoWarmer — it has no message-ID pool dependency.

func init() { RegisterScenario(roomRPCScenario{}) }
