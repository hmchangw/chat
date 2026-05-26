// Command audit generates an audit checklist from the most recent
// cucumber JSON report. Output is written to
// docs/integration-suite-v1/audit-<runID>.md.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	is "github.com/hmchangw/chat/tools/integration-suite/internal/harness"
)

func main() {
	sample := flag.Int("sample", 30, "number of approved scenarios to sample")
	cucumberPath := flag.String("cucumber", "tools/integration-suite/reports/cucumber.json", "path to cucumber.json")
	outDir := flag.String("out-dir", "docs/integration-suite-v1", "output directory")
	seed := flag.Int64("seed", time.Now().UnixNano(), "RNG seed for sampling")
	flag.Parse()

	doc, err := os.ReadFile(*cucumberPath)
	if err != nil {
		slog.Error("audit: read cucumber.json", "err", err, "path", *cucumberPath)
		os.Exit(2)
	}
	_, _, err = is.ParseCucumber(doc)
	if err != nil {
		slog.Error("audit: parse cucumber", "err", err)
		os.Exit(2)
	}

	approved := buildApprovedRows(doc)
	sampled := is.SampleApproved(approved, *sample, *seed)

	runID := fmt.Sprintf("%d", *seed) // good enough — the runID would normally be passed via env
	out := is.RenderAuditChecklist(runID, sampled)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		slog.Error("audit: mkdir", "err", err)
		os.Exit(2)
	}
	target := filepath.Join(*outDir, fmt.Sprintf("audit-%s.md", runID))
	if err := os.WriteFile(target, []byte(out), 0o644); err != nil {
		slog.Error("audit: write", "err", err)
		os.Exit(2)
	}
	fmt.Println(target)
}

// buildApprovedRows scans the cucumber JSON to find all approved
// scenarios (passing AND failing) and turns them into AuditRows.
func buildApprovedRows(doc []byte) []is.AuditRow {
	var features []struct {
		URI      string `json:"uri"`
		Elements []struct {
			Name string `json:"name"`
			Line int    `json:"line"`
			Type string `json:"type"`
			Tags []struct {
				Name string `json:"name"`
			} `json:"tags"`
			Steps []struct {
				Result struct {
					Status string `json:"status"`
				} `json:"result"`
			} `json:"steps"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(doc, &features); err != nil {
		return nil
	}
	var out []is.AuditRow
	for _, f := range features {
		for _, sc := range f.Elements {
			if sc.Type != "scenario" {
				continue
			}
			tags := make([]string, 0, len(sc.Tags))
			for _, t := range sc.Tags {
				tags = append(tags, t.Name)
			}
			if is.StatusFromTags(tags) != is.StatusApproved {
				continue
			}
			outcome := "Passed"
			for _, step := range sc.Steps {
				if step.Result.Status == "failed" {
					outcome = "Failed"
					break
				}
			}
			out = append(out, is.AuditRow{
				FeatureFile: f.URI,
				Line:        sc.Line,
				Name:        sc.Name,
				Outcome:     outcome,
			})
		}
	}
	return out
}
