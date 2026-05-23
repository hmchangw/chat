package main

import (
	"context"
	"errors"
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
	// injectMode lets a test override InjectMode(); zero-value preserves the
	// historical InjectFrontdoor behavior for tests that don't set it.
	injectMode InjectMode
	// runID and runsDir are populated only by tests that exercise the
	// per-run artifact path (e.g., search-sync-lag bootstrap_error
	// marker). Zero-values disable artifact writing.
	runID   string
	runsDir string
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
func (f *fakeScenarioDeps) Fixtures() *Fixtures { return f.fixtures }
func (f *fakeScenarioDeps) Preset() *Preset     { return f.preset }
func (f *fakeScenarioDeps) SiteID() string      { return f.siteID }
func (f *fakeScenarioDeps) MaxInFlight() int    { return 10 }
func (f *fakeScenarioDeps) Omission() *OmissionTracker {
	if f.omission != nil {
		return f.omission
	}
	return NewOmissionTracker(NewMetrics())
}
func (f *fakeScenarioDeps) InjectMode() InjectMode {
	if f.injectMode == "" {
		return InjectFrontdoor
	}
	return f.injectMode
}
func (f *fakeScenarioDeps) WarmupDeadline() time.Time { return f.warmupDeadline }
func (f *fakeScenarioDeps) MessageIDs() []string      { return f.msgIDs }
func (f *fakeScenarioDeps) Sites() []SiteDeps         { return nil }
func (f *fakeScenarioDeps) Subscribers() *Subscribers { return nil }
func (f *fakeScenarioDeps) RunID() string             { return f.runID }
func (f *fakeScenarioDeps) RunsDir() string           { return f.runsDir }

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

// TestBuildLivenessProbeFromScenario_DispatchBranches verifies that
// buildLivenessProbeFromScenario correctly dispatches to scenario-provided
// probes when available, and falls back to NATS RTT checks otherwise.
func TestBuildLivenessProbeFromScenario_DispatchBranches(t *testing.T) {
	// Branch 1: scenario implements LivenessProber → use its probe.
	historyScenario, ok := LookupScenario("history-read")
	require.True(t, ok, "history-read scenario must be registered")
	deps := minimalDeps("history-read")
	probe := buildLivenessProbeFromScenario(historyScenario, deps, nil)
	require.NotNil(t, probe, "probe must not be nil for LivenessProber scenario")
	// Calling the probe should work (it'll try to contact the fake requester).
	err := probe(context.Background())
	// err is expected due to fake requester; we're just verifying the probe exists and runs.
	_ = err

	// Branch 2: scenario without LivenessProber → fallback to NATS RTT.
	messagingScenario, ok := LookupScenario("messaging-pipeline")
	require.True(t, ok, "messaging-pipeline scenario must be registered")
	fakeConn := &fakeRTTConn{rtt: 5 * time.Millisecond, err: nil}
	fallbackProbe := buildLivenessProbeFromScenario(messagingScenario, nil, fakeConn)
	require.NotNil(t, fallbackProbe, "fallback probe must not be nil")
	err = fallbackProbe(context.Background())
	require.NoError(t, err, "RTT check should succeed with healthy fake conn")

	// Branch 2b: RTT conn reports error → probe wraps and propagates it.
	failConn := &fakeRTTConn{rtt: 0, err: context.Canceled}
	failProbe := buildLivenessProbeFromScenario(messagingScenario, nil, failConn)
	err = failProbe(context.Background())
	assert.True(t, errors.Is(err, context.Canceled), "probe must propagate RTT error (got: %v)", err)

	// Branch 2c: nil conn in fallback → probe succeeds (no-op).
	nilConnProbe := buildLivenessProbeFromScenario(messagingScenario, nil, nil)
	err = nilConnProbe(context.Background())
	require.NoError(t, err, "probe with nil conn must succeed (no-op)")
}

// fakeRTTConn implements natsConnLike for testing.
type fakeRTTConn struct {
	rtt time.Duration
	err error
}

func (f *fakeRTTConn) RTT() (time.Duration, error) {
	return f.rtt, f.err
}
