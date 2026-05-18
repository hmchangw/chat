// tools/loadgen/scenario_roomopen.go
//
// room-open composite scenario (Phase 3 §3.14): simulates a client opening
// one room by firing 4 requests in parallel via errgroup and reporting
// per-leg + e2e (slowest-leg) latency.
//
// Legs:
//  1. history    — MsgHistory (history-service)
//  2. rooms_get  — RoomsGet / RoomsInfoBatch (room-service)
//  3. read       — MessageRead write (history-service)
//  4. restricted — Restricted-rooms cache lookup (search-service)
//
// Presence-subscribe (leg 5) is OMITTED — Phase 3.3 audit of pkg/subject/
// found no presence subject builders. A TODO below tracks the follow-up.
// TODO Phase 3.3: add "presence" leg once pkg/subject defines presence builders.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// roomOpenScenario simulates a client opening a room: fires 4 parallel
// requests (history, rooms-get, message-read, restricted lookup) and reports
// per-leg + e2e (slowest-leg) latency.
//
// The presence leg is omitted pending Phase 3.3 (presence subject builders
// absent from pkg/subject/ as of this commit). When builders land, set
// presenceSubjectsAvailable() to true and wire the real call.
type roomOpenScenario struct{}

func (roomOpenScenario) Name() string          { return "room-open" }
func (roomOpenScenario) DefaultPreset() string { return "small" }

// NewGenerator constructs the room-open load generator.
func (roomOpenScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &roomOpenGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(roomOpenScenario{}) }

// ---------------- generator ----------------

type roomOpenGenerator struct {
	deps ScenarioDeps
	rf   *runFlags
}

// legFn pairs a leg name with its request function.
type legFn struct {
	Name string
	Fn   func(ctx context.Context) error
}

// RoomOpenOutcome captures per-leg latencies + e2e (slowest leg).
type RoomOpenOutcome struct {
	E2E          time.Duration
	LegLatencies map[string]time.Duration
}

// openRoom fires every leg in parallel via errgroup, captures per-leg latency,
// and computes e2e = max(per-leg). Returns the first error if any leg fails.
func openRoom(ctx context.Context, legs []legFn) (RoomOpenOutcome, error) {
	out := RoomOpenOutcome{LegLatencies: make(map[string]time.Duration, len(legs))}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	for _, leg := range legs {
		leg := leg // shadow for closure
		g.Go(func() error {
			start := time.Now()
			err := leg.Fn(gctx)
			elapsed := time.Since(start)
			mu.Lock()
			out.LegLatencies[leg.Name] = elapsed
			mu.Unlock()
			if err != nil {
				return fmt.Errorf("leg %s: %w", leg.Name, err)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return out, err
	}
	// E2E = max of per-leg latencies.
	var e2e time.Duration
	for _, lat := range out.LegLatencies {
		if lat > e2e {
			e2e = lat
		}
	}
	out.E2E = e2e
	return out, nil
}

// Run is the generator's main loop. Per tick, fires all room-open legs in
// parallel and records per-leg + e2e latency into Prometheus histograms.
func (g *roomOpenGenerator) Run(ctx context.Context) error {
	rate := g.rf.Rate
	if rate <= 0 {
		rate = 10
	}
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	m := g.deps.Metrics()
	legHist := m.RoomOpen
	e2eHist := m.RoomOpenE2E

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			legs := g.buildLegs(ctx)

			wg.Add(1)
			go func() {
				defer wg.Done()
				outcome, err := openRoom(ctx, legs)
				if err != nil {
					// Record per-leg latencies even on failure — observability
					// matters under chaos scenarios where partial results are common.
					for name, lat := range outcome.LegLatencies {
						legHist.WithLabelValues(name).Observe(lat.Seconds())
					}
					return
				}
				for name, lat := range outcome.LegLatencies {
					legHist.WithLabelValues(name).Observe(lat.Seconds())
				}
				e2eHist.Observe(outcome.E2E.Seconds())
			}()
		}
	}
}

// buildLegs constructs the per-leg request functions for one room-open tick.
// All legs are STUBS for Phase 3.14's initial landing.
// TODO Phase 3.14 follow-up: wire real history-service, room-service, and
// search-service requests using the existing read_generator.go pattern.
func (g *roomOpenGenerator) buildLegs(_ context.Context) []legFn {
	legs := []legFn{
		{Name: "history", Fn: g.stubLeg("history", 5*time.Millisecond)},
		{Name: "rooms_get", Fn: g.stubLeg("rooms_get", 3*time.Millisecond)},
		{Name: "read", Fn: g.stubLeg("read", 2*time.Millisecond)},
		{Name: "restricted", Fn: g.stubLeg("restricted", 4*time.Millisecond)},
	}
	// Phase 3.3 presence audit result: pkg/subject/ has no presence builders
	// as of this commit → presence leg omitted. Set presenceSubjectsAvailable()
	// to true and add the leg here when Phase 3.3 lands.
	if presenceSubjectsAvailable() {
		legs = append(legs, legFn{Name: "presence", Fn: g.stubLeg("presence", 6*time.Millisecond)})
	}
	return legs
}

// stubLeg returns a fake request fn that sleeps for delay then returns nil.
// Replaced by real wire-up in a Phase 3.14 follow-up commit.
func (g *roomOpenGenerator) stubLeg(_ string, delay time.Duration) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		select {
		case <-time.After(delay):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// presenceSubjectsAvailable returns true if pkg/subject defines the presence
// builders needed for the presence-subscribe leg.
//
// Phase 3.3 audit result: grep of pkg/subject/ found no Presence builders —
// returning false. Update this when presence support lands in pkg/subject/.
// TODO Phase 3.3: return true once pkg/subject defines presence subject builders.
func presenceSubjectsAvailable() bool {
	return false
}
