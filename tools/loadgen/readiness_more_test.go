package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeScenarioDeps is a minimal ScenarioDeps implementation for unit tests.
type fakeScenarioDeps struct {
	fixtures       *Fixtures
	preset         *Preset
	siteID         string
	req            Requester
	pub            Publisher
	collector      *Collector
	metrics        *Metrics
	omission       *OmissionTracker
	warmupDeadline time.Time
	msgIDs         []string
}

func (f *fakeScenarioDeps) Publisher() Publisher {
	return f.pub
}
func (f *fakeScenarioDeps) Requester() Requester {
	return f.req
}
func (f *fakeScenarioDeps) Collector() *Collector {
	if f.collector != nil {
		return f.collector
	}
	return NewCollector(NewMetrics(), "test")
}
func (f *fakeScenarioDeps) Metrics() *Metrics {
	if f.metrics != nil {
		return f.metrics
	}
	return NewMetrics()
}
func (f *fakeScenarioDeps) Fixtures() *Fixtures        { return f.fixtures }
func (f *fakeScenarioDeps) Preset() *Preset            { return f.preset }
func (f *fakeScenarioDeps) SiteID() string             { return f.siteID }
func (f *fakeScenarioDeps) MaxInFlight() int           { return 10 }
func (f *fakeScenarioDeps) WarmupPublisher() Publisher { return f.pub }
func (f *fakeScenarioDeps) Omission() *OmissionTracker {
	if f.omission != nil {
		return f.omission
	}
	return NewOmissionTracker(NewMetrics())
}
func (f *fakeScenarioDeps) InjectMode() InjectMode    { return InjectFrontdoor }
func (f *fakeScenarioDeps) WarmupDeadline() time.Time { return f.warmupDeadline }
func (f *fakeScenarioDeps) MessageIDs() []string      { return f.msgIDs }

// TestScenarioReadiness_RegistryBased verifies that the three read scenarios
// implement ReadinessProber and messaging-pipeline does not, using the registry.
func TestScenarioReadiness_RegistryBased(t *testing.T) {
	cases := []struct {
		scenario   string
		wantProber bool
	}{
		{"history-read", true},
		{"search-read", true},
		{"room-rpc", true},
		{"messaging-pipeline", false},
	}
	for _, tc := range cases {
		t.Run(tc.scenario, func(t *testing.T) {
			sc, ok := LookupScenario(tc.scenario)
			require.True(t, ok, "scenario %q must be registered", tc.scenario)
			_, isProber := sc.(ReadinessProber)
			assert.Equal(t, tc.wantProber, isProber,
				"ReadinessProber implementation mismatch for %s", tc.scenario)
		})
	}
}

// TestBuildReadinessProbe_AllScenariosReturnNonNilFn verifies that
// BuildReadinessProbe returns a non-nil function for the three read scenarios.
func TestBuildReadinessProbe_AllScenariosReturnNonNilFn(t *testing.T) {
	for _, name := range []string{"history-read", "search-read", "room-rpc"} {
		t.Run(name, func(t *testing.T) {
			sc, ok := LookupScenario(name)
			require.True(t, ok)
			pr, ok := sc.(ReadinessProber)
			require.True(t, ok, "%s must implement ReadinessProber", name)
			deps := minimalDeps(name)
			fn := pr.BuildReadinessProbe(deps)
			require.NotNil(t, fn, "probe fn must be non-nil for %s", name)
		})
	}
}

// TestBuildLivenessProbe_MessagingPipelineUsesRTT verifies that messaging-pipeline
// falls back to the NATS RTT check via buildLivenessProbeFromScenario (not
// through the LivenessProber interface, since it doesn't implement it).
func TestBuildLivenessProbe_MessagingPipelineUsesRTT(t *testing.T) {
	sc, ok := LookupScenario("messaging-pipeline")
	require.True(t, ok)
	_, isLiveProber := sc.(LivenessProber)
	assert.False(t, isLiveProber,
		"messaging-pipeline must not implement LivenessProber (falls back to NATS RTT)")
}

// minimalDeps returns a fakeScenarioDeps with one subscription, used for probe tests.
func minimalDeps(scenario string) ScenarioDeps {
	preset, _ := BuiltinPreset(scenario)
	fixtures := BuildFixtures(&preset, 42, "site-local")
	return &fakeScenarioDeps{
		fixtures: &fixtures,
		preset:   &preset,
		siteID:   "site-local",
		req:      &recordingRequester{},
	}
}
