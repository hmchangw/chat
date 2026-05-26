// Command coverage reports how many documented behavior cases in
// docs/integration-suite-v1/coverage.md have a passing @covers scenario.
// Report-only: it never exits non-zero.
package main

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	is "github.com/hmchangw/chat/tools/integration-suite/internal/harness"
)

func main() {
	featuresRoot := "tools/integration-suite/features"
	registerPath := "docs/integration-suite-v1/coverage.md"
	cucumberPath := "tools/integration-suite/reports/cucumber.json"

	regBody, err := os.ReadFile(registerPath)
	if err != nil {
		fmt.Printf("integration-suite-coverage: no register at %s — nothing to score\n", registerPath)
		return
	}
	entries := is.ParseCoverageRegister(string(regBody))

	// Static orphan check: @covers tags vs register headings.
	var featureSlugs []string
	_ = filepath.WalkDir(featuresRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".feature") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		featureSlugs = append(featureSlugs, is.ExtractCoversSlugs(string(body))...)
		return nil
	})
	registerSlugs := is.RegisteredCoverageSlugs(string(regBody))
	orphanTags, neverReferenced := is.DiffCoverage(featureSlugs, registerSlugs)
	if len(orphanTags) > 0 {
		fmt.Printf("WARN: @covers tags with no coverage.md entry:\n%s\n\n", is.JoinLines(orphanTags))
	}
	if len(neverReferenced) > 0 {
		fmt.Printf("NOTE: registered cases never @covers'd by any scenario:\n%s\n\n", is.JoinLines(neverReferenced))
	}

	// Score against the last run if available; else show declared state.
	doc, derr := os.ReadFile(cucumberPath)
	if derr != nil {
		slog.Info("coverage: no cucumber.json — showing declared state only", "path", cucumberPath)
		doc = nil
	}
	rep, serr := is.ScoreCoverage(doc, entries)
	if serr != nil {
		slog.Error("coverage: score", "err", serr)
		return
	}
	fmt.Print(is.RenderCoverage(rep))
}
