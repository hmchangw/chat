package integrationsuite

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/cucumber/godog"
)

var (
	suiteConfig *Config
	suiteRunID  string
	suiteWorld  *World
)

func TestFeatures(t *testing.T) {
	// Skip when env vars are unset so `make test` (whole-repo) can pass
	// without a live integration stack. The suite is invoked explicitly
	// via `make integration-suite`.
	if os.Getenv("SITES") == "" {
		t.Skip("integration-suite: SITES env var unset; run via `make integration-suite`")
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("integration-suite: config load failed: %v", err)
	}
	suiteConfig = cfg
	suiteRunID = GenerateRunID()
	suiteWorld = NewWorld(suiteRunID)
	slog.Info("integration-suite: starting", "runID", suiteRunID, "sites", cfg.Sites)

	suite := godog.TestSuite{
		Name:                 "integration-suite",
		ScenarioInitializer:  InitializeScenario,
		TestSuiteInitializer: InitializeTestSuite,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			TestingT: t,
		},
	}
	if status := suite.Run(); status != 0 {
		t.Fatalf("integration-suite: godog exited with status %d", status)
	}
}

// InitializeTestSuite is a placeholder; config is loaded in TestFeatures
// so the test can Skip cleanly when env vars are absent.
func InitializeTestSuite(_ *godog.TestSuiteContext) {}

// InitializeScenario installs per-scenario state on the World.
func InitializeScenario(ctx *godog.ScenarioContext) {
	ctx.Before(func(c context.Context, sc *godog.Scenario) (context.Context, error) {
		suiteWorld.BeginScenario(sc.Name)
		return c, nil
	})

	registerAuthSteps(ctx)

	ctx.After(func(c context.Context, sc *godog.Scenario, stepErr error) (context.Context, error) {
		tagNames := make([]string, 0, len(sc.Tags))
		for _, t := range sc.Tags {
			tagNames = append(tagNames, t.Name)
		}
		if err := BlindspotFailure(BlindspotsFromTags(tagNames)); err != nil {
			// Force the scenario to count as failed by returning the error,
			// regardless of stepErr.
			return c, err
		}
		return c, stepErr
	})
}
