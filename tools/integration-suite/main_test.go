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
		os.Exit(status)
	}
}

// InitializeTestSuite loads config and run ID once before any scenario.
func InitializeTestSuite(ctx *godog.TestSuiteContext) {
	ctx.BeforeSuite(func() {
		cfg, err := LoadConfig()
		if err != nil {
			slog.Error("integration-suite: config load failed", "err", err)
			os.Exit(2)
		}
		suiteConfig = cfg
		suiteRunID = GenerateRunID()
		suiteWorld = NewWorld(suiteRunID)
		slog.Info("integration-suite: starting", "runID", suiteRunID, "sites", cfg.Sites)
	})
}

// InitializeScenario installs per-scenario state on the World.
func InitializeScenario(ctx *godog.ScenarioContext) {
	ctx.Before(func(c context.Context, sc *godog.Scenario) (context.Context, error) {
		suiteWorld.BeginScenario(sc.Name)
		return c, nil
	})
}
