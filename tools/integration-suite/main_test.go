package integrationsuite

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

var (
	suiteConfig *Config
	suiteRunID  string
	suiteWorld  *World
)

func TestFeatures(t *testing.T) {
	if os.Getenv("SITES") == "" {
		t.Skip("integration suite skipped: SITES env not set")
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("integration-suite: config load failed: %v", err)
	}
	suiteConfig = cfg
	suiteRunID = GenerateRunID()
	suiteWorld = NewWorld(suiteRunID)
	slog.Info("integration-suite: starting", "runID", suiteRunID, "sites", cfg.Sites)

	if err := os.MkdirAll("reports", 0o755); err != nil {
		t.Fatalf("reports dir: %v", err)
	}

	suite := godog.TestSuite{
		Name:                 "integration-suite",
		ScenarioInitializer:  InitializeScenario,
		TestSuiteInitializer: InitializeTestSuite,
		Options: &godog.Options{
			Format:   "pretty,cucumber:reports/cucumber.json,junit:reports/junit.xml",
			Paths:    []string{"features"},
			TestingT: t,
		},
	}

	startedAt := time.Now().UTC()
	exit := suite.Run()
	duration := time.Since(startedAt).Round(time.Second)

	// Post-run: build human summary regardless of exit code.
	if err := writeHumanSummary(startedAt, duration); err != nil {
		t.Logf("integration-suite: writing human summary: %v", err)
	}

	if exit != 0 {
		t.Fatalf("integration-suite: exit %d", exit)
	}
}

func writeHumanSummary(startedAt time.Time, duration time.Duration) error {
	doc, err := os.ReadFile("reports/cucumber.json")
	if err != nil {
		return fmt.Errorf("read cucumber.json: %w", err)
	}
	summary, failures, err := ParseCucumber(doc)
	if err != nil {
		return err
	}
	summary.RunID = suiteRunID
	summary.StartISO = startedAt.Format(time.RFC3339)
	summary.Duration = duration.String()
	summary.Failures = failures

	out := RenderSummary(summary)

	// Tests run with CWD=tools/integration-suite — walk up to repo root.
	targetDir := filepath.Join("..", "..", "docs", "integration-suite")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", targetDir, err)
	}
	target := filepath.Join(targetDir, "last-run.md")
	if err := os.WriteFile(target, []byte(out), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
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
	registerRoomSteps(ctx)
	registerErrorSteps(ctx)

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
