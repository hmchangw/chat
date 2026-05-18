package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFederationScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("federation-lag")
	require.True(t, ok)
	assert.Equal(t, "federation-lag", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestFederationStageTracker_ComputesPerStageLags(t *testing.T) {
	tracker := newFederationStageTracker()
	publishedAt := time.Now()
	tracker.RecordPublish("msg-1", publishedAt)
	tracker.RecordStage("msg-1", "outbox", publishedAt.Add(5*time.Millisecond))
	tracker.RecordStage("msg-1", "inbox", publishedAt.Add(50*time.Millisecond))
	tracker.RecordStage("msg-1", "persist", publishedAt.Add(70*time.Millisecond))
	tracker.RecordStage("msg-1", "visible", publishedAt.Add(100*time.Millisecond))

	lags, ok := tracker.Lags("msg-1")
	require.True(t, ok)
	assert.InDelta(t, 5*time.Millisecond, lags["outbox"], float64(2*time.Millisecond))
	assert.InDelta(t, 50*time.Millisecond, lags["inbox"], float64(2*time.Millisecond))
	assert.InDelta(t, 70*time.Millisecond, lags["persist"], float64(2*time.Millisecond))
	assert.InDelta(t, 100*time.Millisecond, lags["visible"], float64(2*time.Millisecond))
}

func TestFederationStageTracker_BoundedSize(t *testing.T) {
	tracker := newFederationStageTracker()
	// Fill beyond the cap (4096) to verify FIFO eviction.
	for i := 0; i < 5000; i++ {
		tracker.RecordPublish(padInt(i), time.Now())
	}
	assert.LessOrEqual(t, tracker.Size(), 4096, "tracker must FIFO-evict to stay bounded")
}

func TestFederationFlagsParse(t *testing.T) {
	args := []string{"--federation-flap", "--flap-period=30s", "--flap-down=5s", "--federation-cross-read"}
	rf, err := ParseRunFlags(args)
	require.NoError(t, err)
	assert.True(t, rf.FederationFlap)
	assert.True(t, rf.FederationCrossRead)
	assert.Equal(t, 30*time.Second, rf.FlapPeriod)
	assert.Equal(t, 5*time.Second, rf.FlapDown)
}

// TestFederationScenario_RequiresTwoSites verifies that NewGenerator returns
// an error when fewer than 2 sites are available.
func TestFederationScenario_RequiresTwoSites(t *testing.T) {
	sc, ok := LookupScenario("federation-lag")
	require.True(t, ok)
	factory, ok := sc.(GeneratorFactory)
	require.True(t, ok)

	// Use a fake ScenarioDeps that returns only 1 site.
	deps := &fakeFederationDeps{sites: []SiteDeps{{Name: "site-a"}}}
	_, err := factory.NewGenerator(deps, &runFlags{})
	require.Error(t, err, "NewGenerator must fail with only 1 site")
	assert.Contains(t, err.Error(), "2 sites")
}

// TestFederationScenario_AcceptsTwoSites verifies that NewGenerator succeeds
// when 2 sites are provided.
func TestFederationScenario_AcceptsTwoSites(t *testing.T) {
	sc, ok := LookupScenario("federation-lag")
	require.True(t, ok)
	factory, ok := sc.(GeneratorFactory)
	require.True(t, ok)

	deps := &fakeFederationDeps{
		sites: []SiteDeps{
			{Name: "site-a"},
			{Name: "site-b"},
		},
	}
	gen, err := factory.NewGenerator(deps, &runFlags{})
	require.NoError(t, err)
	require.NotNil(t, gen)

	// Verify Run blocks until context cancels.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gen.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

// fakeFederationDeps is a minimal ScenarioDeps implementation for federation tests.
type fakeFederationDeps struct {
	sites []SiteDeps
}

func (f *fakeFederationDeps) Publisher() Publisher       { return nil }
func (f *fakeFederationDeps) Requester() Requester       { return nil }
func (f *fakeFederationDeps) Collector() *Collector      { return NewCollector(NewMetrics(), "") }
func (f *fakeFederationDeps) Metrics() *Metrics          { return NewMetrics() }
func (f *fakeFederationDeps) Fixtures() *Fixtures        { return &Fixtures{} }
func (f *fakeFederationDeps) Preset() *Preset            { return &Preset{} }
func (f *fakeFederationDeps) SiteID() string             { return "site-a" }
func (f *fakeFederationDeps) MaxInFlight() int           { return 1 }
func (f *fakeFederationDeps) Omission() *OmissionTracker { return nil }
func (f *fakeFederationDeps) InjectMode() InjectMode     { return InjectFrontdoor }
func (f *fakeFederationDeps) WarmupDeadline() time.Time  { return time.Time{} }
func (f *fakeFederationDeps) MessageIDs() []string       { return nil }
func (f *fakeFederationDeps) Sites() []SiteDeps          { return f.sites }
func (f *fakeFederationDeps) Subscribers() *Subscribers  { return nil }
