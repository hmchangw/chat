package integrationsuite

import (
	"os"
	"testing"

	"github.com/cucumber/godog"
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

// InitializeTestSuite runs once before any scenario.
func InitializeTestSuite(ctx *godog.TestSuiteContext) {}

// InitializeScenario runs once per scenario.
func InitializeScenario(ctx *godog.ScenarioContext) {}
