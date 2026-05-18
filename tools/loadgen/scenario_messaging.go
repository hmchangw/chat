// tools/loadgen/scenario_messaging.go
//
// Registration for the "messaging-pipeline" scenario. Phase 2 migration.
package main

import "strconv"

// messagingPipelineScenario publishes user messages through the gatekeeper
// → canonical → broadcast pipeline. It's the default load profile.
type messagingPipelineScenario struct{}

func (messagingPipelineScenario) Name() string          { return "messaging-pipeline" }
func (messagingPipelineScenario) DefaultPreset() string { return "small" }

// NewGenerator constructs the messaging-pipeline load generator from the
// ScenarioDeps and run flags. The implementation is a direct extraction
// from the former switch-case default branch in executeRun (run.go).
func (messagingPipelineScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	// The messaging-pipeline generator needs ConnIDFor, which maps userIDs to
	// connection indices for per-connection metric labelling. This extra is not
	// in ScenarioDeps (it's pipeline-specific), so we type-assert to *runDeps.
	// If unavailable (e.g. test fakes), fall back to always returning "0".
	var connIDFor func(userID string) string
	if rd, ok := deps.(*runDeps); ok {
		connIDFor = rd.ConnIDFor()
	}
	if connIDFor == nil {
		connIDFor = func(_ string) string { return strconv.Itoa(0) }
	}
	return NewGenerator(&GeneratorConfig{
		Preset:         deps.Preset(),
		Fixtures:       *deps.Fixtures(),
		SiteID:         deps.SiteID(),
		Rate:           rf.Rate,
		Inject:         deps.InjectMode(),
		Publisher:      deps.Publisher(),
		Metrics:        deps.Metrics(),
		Collector:      deps.Collector(),
		WarmupDeadline: deps.WarmupDeadline(),
		MaxInFlight:    deps.MaxInFlight(),
		Ramp:           rf.BuiltRamp,
		ConnIDFor:      connIDFor,
		Omission:       deps.Omission(),
	}, rf.Seed), nil
}

// messaging-pipeline does NOT implement ReadinessProber, LivenessProber, or
// AutoWarmer. The liveness probe falls back to a NATS RTT check in
// buildLivenessProbeFromScenario (run.go).

func init() { RegisterScenario(messagingPipelineScenario{}) }
