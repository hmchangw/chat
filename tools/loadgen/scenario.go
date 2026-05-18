// tools/loadgen/scenario.go
//
// Scenario interface family + registry.
//
// Adding a new scenario in Phase 3:
//  1. Create scenario_<NAME>.go with a type implementing Scenario + GeneratorFactory.
//  2. Call RegisterScenario in init().
//
// That's it. No edits to main.go or run.go.
//
// Phase 2 §2.2 (loadgen v2 implementation plan).
package main

import (
	"context"
	"fmt"
)

// Scenario is the minimum a scenario must implement.
type Scenario interface {
	Name() string
	DefaultPreset() string
}

// GeneratorFactory is a scenario that knows how to construct its load generator.
type GeneratorFactory interface {
	NewGenerator(deps ScenarioDeps, rf runFlags) (Runner, error)
}

// ReadinessProber is optional — scenario provides a SUT-readiness probe.
type ReadinessProber interface {
	BuildReadinessProbe(deps ScenarioDeps) func(context.Context) error
}

// LivenessProber is optional — scenario provides a liveness probe.
type LivenessProber interface {
	BuildLivenessProbe(deps ScenarioDeps) func(context.Context) error
}

// AutoWarmer is optional — scenario declares whether auto-warmup is needed.
type AutoWarmer interface {
	NeedsAutoWarmup(p *Preset) bool
}

// ScenarioDeps is the runtime-side capability surface a Scenario may consume.
// Implemented by *Runtime in Task 2.2; tests may provide fakes.
//
// TODO(Task 2.4): add Sites() []SiteDeps and Subscribers() *Subscribers once
// concrete impls exist. Kept minimal here to avoid phantom dependencies.
type ScenarioDeps interface {
	Publisher() Publisher
	Requester() Requester
	Collector() *Collector
	Metrics() *Metrics
	Fixtures() *Fixtures
}

// Runner is a constructed load generator. Run blocks until ctx is cancelled or
// the scenario reaches natural completion.
type Runner interface {
	Run(ctx context.Context) error
}

// SiteDeps holds per-site dependencies (NATS conn, JS, Mongo handle).
// For federation (Phase 3 §3.9), Runtime returns multiple SiteDeps.
// Concrete implementation is Task 2.4's responsibility; defined here as a
// placeholder so GeneratorFactory signatures are complete.
//
// TODO(Task 2.4): replace any fields with concrete typed accessors.
type SiteDeps interface {
	Name() string
	NC() any // typed to *nats.Conn in concrete impls; kept loose here
	JS() any // typed to jetstream.JetStream in concrete impls
	Mongo() any
}

// Subscribers is a long-lived subscriptions registry used by large-room
// scenarios (Phase 3 §3.2). Concrete impl is Task 2.4's responsibility.
//
// TODO(Task 2.4): implement in runtime.go alongside ScenarioDeps.
type Subscribers interface {
	Subscribe(subject string, handler func([]byte)) error
	Close() error
}

// ---------------- registry ----------------

var scenarioRegistry = map[string]Scenario{}

// RegisterScenario adds a scenario to the global registry. Panics on duplicate
// registration to catch init() collisions early.
func RegisterScenario(s Scenario) {
	name := s.Name()
	if _, exists := scenarioRegistry[name]; exists {
		panic(fmt.Sprintf("scenario %q registered twice — check init() in scenario_*.go", name))
	}
	scenarioRegistry[name] = s
}

// LookupScenario returns the scenario for name, or (nil, false) if unknown.
func LookupScenario(name string) (Scenario, bool) {
	s, ok := scenarioRegistry[name]
	return s, ok
}

// AllScenarios returns a copy of the registry. Mutations on the returned map
// do not affect the registry.
func AllScenarios() map[string]Scenario {
	out := make(map[string]Scenario, len(scenarioRegistry))
	for k, v := range scenarioRegistry {
		out[k] = v
	}
	return out
}
