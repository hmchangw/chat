package harness

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Cucumber JSON minimal schema. Only the fields we need.
type cukeFeature struct {
	URI      string         `json:"uri"`
	Elements []cukeScenario `json:"elements"`
}

type cukeScenario struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Line  int        `json:"line"`
	Type  string     `json:"type"`
	Tags  []cukeTag  `json:"tags"`
	Steps []cukeStep `json:"steps"`
}

type cukeTag struct {
	Name string `json:"name"`
}

type cukeStep struct {
	Name   string     `json:"name"`
	Result cukeResult `json:"result"`
}

type cukeResult struct {
	Status       string `json:"status"`
	ErrorMessage string `json:"error_message"`
}

// ParseCucumber reads a cucumber JSON document and returns the
// aggregated RunSummary (Approved + Draft) plus the flat list of
// failure/blindspot rows for the human report.
//
// The returned RunSummary does NOT set RunID / StartISO / Duration —
// the caller (main_test.go post-run) fills those in.
func ParseCucumber(doc []byte) (*RunSummary, []FailureRow, error) {
	var features []cukeFeature
	if err := json.Unmarshal(doc, &features); err != nil {
		return nil, nil, fmt.Errorf("cucumber: parse: %w", err)
	}

	summary := &RunSummary{
		Approved: ScopeSummary{FailureByClass: map[Class]int{}},
		Draft:    ScopeSummary{FailureByClass: map[Class]int{}},
	}
	var failures []FailureRow

	for _, f := range features {
		for _, sc := range f.Elements {
			if sc.Type != "scenario" {
				continue
			}
			tagNames := make([]string, 0, len(sc.Tags))
			for _, t := range sc.Tags {
				tagNames = append(tagNames, t.Name)
			}
			status := StatusFromTags(tagNames)
			blindspots := BlindspotsFromTags(tagNames)

			scope := scopeFor(summary, status)

			scenarioFailed, errMsg := scenarioOutcome(&sc)

			switch {
			case len(blindspots) > 0:
				scope.Blindspot++
				failures = append(failures, FailureRow{
					Status:      status,
					FeatureFile: relURI(f.URI),
					Line:        sc.Line,
					Name:        sc.Name,
					Reason:      strings.Join(blindspots, ", "),
				})
			case scenarioFailed:
				scope.Failed++
				cls := ClassUnclassified // v1: HTTP/NATS class detection happens upstream; default here.
				scope.FailureByClass[cls]++
				failures = append(failures, FailureRow{
					Status:      status,
					FeatureFile: relURI(f.URI),
					Line:        sc.Line,
					Name:        sc.Name,
					Class:       cls,
				})
				_ = errMsg
			default:
				scope.Passed++
			}
		}
	}

	return summary, failures, nil
}

func scopeFor(s *RunSummary, status Status) *ScopeSummary {
	if status == StatusApproved {
		return &s.Approved
	}
	return &s.Draft
}

// scenarioOutcome returns (failed, lastErrorMessage). A scenario fails
// if any step failed.
func scenarioOutcome(sc *cukeScenario) (bool, string) {
	for _, step := range sc.Steps {
		if step.Result.Status == "failed" {
			return true, step.Result.ErrorMessage
		}
	}
	return false, ""
}

// relURI strips a leading "./" from cucumber's URIs.
func relURI(u string) string { return strings.TrimPrefix(u, "./") }
