package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetScenarioRegistryForTest swaps the registry with an empty one for the
// duration of the test, restoring on cleanup. Test-only helper — kept here
// (not in scenario.go) to avoid importing "testing" in production code.
func resetScenarioRegistryForTest(t *testing.T) {
	t.Helper()
	old := scenarioRegistry
	scenarioRegistry = map[string]Scenario{}
	t.Cleanup(func() { scenarioRegistry = old })
}

type fakeScenario struct {
	name   string
	preset string
}

func (f *fakeScenario) Name() string          { return f.name }
func (f *fakeScenario) DefaultPreset() string { return f.preset }

func TestScenarioRegistry_RegisterAndLookup(t *testing.T) {
	resetScenarioRegistryForTest(t)
	RegisterScenario(&fakeScenario{name: "fake", preset: "small"})
	got, ok := LookupScenario("fake")
	require.True(t, ok, "registered scenario must be looked up")
	assert.Equal(t, "small", got.DefaultPreset())
}

func TestScenarioRegistry_DuplicateRegistrationPanics(t *testing.T) {
	resetScenarioRegistryForTest(t)
	RegisterScenario(&fakeScenario{name: "dup", preset: "small"})
	assert.Panics(t, func() {
		RegisterScenario(&fakeScenario{name: "dup", preset: "other"})
	}, "duplicate registration must panic to catch init() collisions")
}

func TestScenarioRegistry_AllScenariosReturnsCopy(t *testing.T) {
	resetScenarioRegistryForTest(t)
	RegisterScenario(&fakeScenario{name: "a", preset: "p"})
	RegisterScenario(&fakeScenario{name: "b", preset: "q"})
	all := AllScenarios()
	assert.Len(t, all, 2)
	// Mutating the returned map must not affect the registry.
	delete(all, "a")
	_, ok := LookupScenario("a")
	assert.True(t, ok, "AllScenarios() must return a copy, not the live map")
}

type fakeScenarioWithProbes struct {
	fakeScenario
}

func (f *fakeScenarioWithProbes) BuildReadinessProbe(_ ScenarioDeps) func(context.Context) error {
	return func(context.Context) error { return nil }
}

func (f *fakeScenarioWithProbes) BuildLivenessProbe(_ ScenarioDeps) func(context.Context) error {
	return func(context.Context) error { return nil }
}

func TestScenarioRegistry_OptionalProbeInterfaces(t *testing.T) {
	resetScenarioRegistryForTest(t)
	RegisterScenario(&fakeScenarioWithProbes{fakeScenario: fakeScenario{name: "probed", preset: "small"}})
	s, _ := LookupScenario("probed")

	pr, isProber := s.(ReadinessProber)
	require.True(t, isProber, "should implement ReadinessProber when method is defined")
	assert.NotNil(t, pr.BuildReadinessProbe(nil))

	lp, isLProber := s.(LivenessProber)
	require.True(t, isLProber, "should implement LivenessProber when method is defined")
	assert.NotNil(t, lp.BuildLivenessProbe(nil))
}
