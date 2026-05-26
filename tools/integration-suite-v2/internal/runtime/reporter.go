package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CaseReport is one test case's verdict and metadata.
type CaseReport struct {
	ScenarioName string
	Subset       string
	Status       string
	Kind         string // "positive" / "negative"; from scenario.Kind; defaults to "positive" when omitted
	Duration     time.Duration
	Verdict      Verdict
}

// RunReport is the aggregate of one suite invocation.
type RunReport struct {
	RunID    string
	StartISO string
	Duration string
	Cases    []CaseReport
	Git      GitInfo // captured at the start of the run
	Perf     *PerformanceStore
}

// previewLimit caps how much of any single JSON-serialised value we
// surface in the report.
const previewLimit = 240

// Render produces the markdown body of the per-run report. Renders
// all cases regardless of status; the approved-only filter is applied
// by the caller via RenderApproved.
func Render(r *RunReport) string {
	return render(r, false)
}

// RenderApproved is like Render but only includes cases with
// Status == "approved". The summary counts are also computed only
// over approved cases.
func RenderApproved(r *RunReport) string {
	filtered := *r
	filtered.Cases = filtered.Cases[:0:0]
	for i := range r.Cases {
		if r.Cases[i].Status == "approved" {
			filtered.Cases = append(filtered.Cases, r.Cases[i])
		}
	}
	return render(&filtered, true)
}

func render(r *RunReport, approvedOnly bool) string {
	var b strings.Builder

	// Heading
	mergeHash := r.Git.LatestMerge
	if mergeHash == "" {
		mergeHash = "—"
	}
	scope := "total"
	if approvedOnly {
		scope = "approved"
	}
	fmt.Fprintf(&b, "# Integration tests — %s   (latest merge: %s)\n\n", scope, mergeHash)

	// Run summary
	fmt.Fprintf(&b, "Run:        %s   (runID %s)\nDuration:   %s\nTotal:      %d test cases\n\n",
		r.StartISO, r.RunID, r.Duration, len(r.Cases))

	// Confusion matrix: scenario kind (positive / negative) × outcome
	// (true=pass / false=fail). Positive scenarios assert the system
	// does the thing; negative scenarios assert it correctly rejects.
	// Pass means the scenario's expected reads were satisfied — i.e.
	// the system behaved as the scenario predicted.
	posTrue, posFalse := 0, 0
	negTrue, negFalse := 0, 0
	approvedPass, approvedFail := 0, 0
	draftPass, draftFail := 0, 0
	for i := range r.Cases {
		c := &r.Cases[i]
		pass := c.Verdict.Outcome == "pass"
		negative := c.Kind == "negative"
		switch {
		case negative && pass:
			negTrue++
		case negative && !pass:
			negFalse++
		case !negative && pass:
			posTrue++
		default:
			posFalse++
		}
		switch {
		case c.Status == "approved" && pass:
			approvedPass++
		case c.Status == "approved" && !pass:
			approvedFail++
		case pass:
			draftPass++
		default:
			draftFail++
		}
	}
	b.WriteString("## Confusion matrix\n\n")
	b.WriteString("                        true (pass)   false (fail)\n")
	fmt.Fprintf(&b, "  +ve (through)         %-13d %-13d\n", posTrue, posFalse)
	fmt.Fprintf(&b, "  -ve (error/warning)   %-13d %-13d\n\n", negTrue, negFalse)

	b.WriteString("## Status breakdown\n\n")
	b.WriteString("              pass    fail\n")
	fmt.Fprintf(&b, "APPROVED      %-7d %-7d\n", approvedPass, approvedFail)
	fmt.Fprintf(&b, "DRAFT         %-7d %-7d\n\n", draftPass, draftFail)

	// Scenarios (sorted for stable output)
	scenIDs := make([]string, 0, len(r.Cases))
	byScen := map[string][]*CaseReport{}
	for i := range r.Cases {
		c := &r.Cases[i]
		if _, ok := byScen[c.ScenarioName]; !ok {
			scenIDs = append(scenIDs, c.ScenarioName)
		}
		byScen[c.ScenarioName] = append(byScen[c.ScenarioName], c)
	}
	sort.Strings(scenIDs)

	for _, scenID := range scenIDs {
		tests := byScen[scenID]
		passed := 0
		for _, c := range tests {
			if c.Verdict.Outcome == "pass" {
				passed++
			}
		}
		fmt.Fprintf(&b, "## scenario: %s: %d/%d\n\n", scenID, passed, len(tests))
		for _, c := range tests {
			renderTest(&b, scenID, c, r.Perf)
		}
		b.WriteString("\n")
	}

	return b.String()
}

func renderTest(b *strings.Builder, scenID string, c *CaseReport, perf *PerformanceStore) {
	outcome := strings.ToUpper(c.Verdict.Outcome)
	fmt.Fprintf(b, "  test %s (%s): %s   duration=%s\n",
		c.Subset, c.Subset, outcome, c.Duration.Round(time.Millisecond))

	tp := lookupPerf(perf, scenID, c.Subset)

	// Per-step. Duration is delta-from-previous-step (or from scenario
	// fire for step 1); zero when the step had no matched events.
	// Latest/best/worst alongside (when history exists) so the reader
	// can spot a service trending slower over time.
	for i, st := range c.Verdict.Steps {
		stepOutcome := strings.ToUpper(st.Outcome)
		if st.Duration > 0 {
			fmt.Fprintf(b, "    seq[%d] %s: %s   (%s)\n", i+1, st.Service, stepOutcome, st.Duration.Round(time.Millisecond))
		} else {
			fmt.Fprintf(b, "    seq[%d] %s: %s\n", i+1, st.Service, stepOutcome)
		}
		for _, mp := range st.MissingPositives {
			fmt.Fprintf(b, "        - missing-positive at %s\n", mp.Location)
			if mp.Expected != nil {
				fmt.Fprintf(b, "            expected: %s\n", preview(mp.Expected))
			}
		}
		if sp := lookupStepPerf(tp, i, st.Service); sp != nil {
			if sp.LatestPass != nil {
				fmt.Fprintf(b, "        Latest: %s @%s\n",
					durationFromMs(sp.LatestPass.DurationMs), sp.LatestPass.Commit)
			}
			if sp.BestPass != nil {
				fmt.Fprintf(b, "        Best:   %s @%s   (%s)\n",
					durationFromMs(sp.BestPass.DurationMs), sp.BestPass.Commit, sp.BestPass.Timestamp)
			}
			if sp.WorstPass != nil {
				fmt.Fprintf(b, "        Worst:  %s @%s\n",
					durationFromMs(sp.WorstPass.DurationMs), sp.WorstPass.Commit)
			}
		}
	}

	// Scenario-level cascades and anomalies (not tied to a single step)
	for j := range c.Verdict.UnexpectedCascades {
		ev := &c.Verdict.UnexpectedCascades[j]
		fmt.Fprintf(b, "    - unexpected-cascade at %s (owner=%s)\n", ev.Location, ev.OwnerSvc)
		if ev.Payload != nil {
			fmt.Fprintf(b, "        payload: %s\n", preview(ev.Payload))
		}
	}
	for j := range c.Verdict.Anomalies {
		ev := &c.Verdict.Anomalies[j]
		fmt.Fprintf(b, "    - anomaly at %s (owner=%s)\n", ev.Location, ev.OwnerSvc)
		if ev.Payload != nil {
			fmt.Fprintf(b, "        payload: %s\n", preview(ev.Payload))
		}
	}
	if c.Verdict.Reason != "" {
		fmt.Fprintf(b, "    reason: %s\n", c.Verdict.Reason)
	}

	// Performance history (latest / best / worst pass) — only for tests
	// that have ever passed; new tests show "no pass yet".
	if tp == nil {
		b.WriteString("    perf: no pass yet\n")
	} else {
		if tp.LatestPass != nil {
			fmt.Fprintf(b, "    Latest pass: %s @%s\n",
				durationFromMs(tp.LatestPass.DurationMs), tp.LatestPass.Commit)
		}
		if tp.BestPass != nil {
			fmt.Fprintf(b, "    Best pass:   %s @%s   (%s)\n",
				durationFromMs(tp.BestPass.DurationMs), tp.BestPass.Commit, tp.BestPass.Timestamp)
		}
		if tp.WorstPass != nil {
			fmt.Fprintf(b, "    Worst pass:  %s @%s\n",
				durationFromMs(tp.WorstPass.DurationMs), tp.WorstPass.Commit)
		}
	}
	b.WriteString("\n")
}

// lookupStepPerf returns the stored history for one step, aligned by
// index and service name. Returns nil if the test has no stored steps
// or the index/service doesn't line up (sequence changed).
func lookupStepPerf(tp *TestPerf, stepIdx int, service string) *StepPerf {
	if tp == nil || stepIdx < 0 || stepIdx >= len(tp.Steps) {
		return nil
	}
	if tp.Steps[stepIdx].Service != service {
		return nil
	}
	return &tp.Steps[stepIdx]
}

func lookupPerf(p *PerformanceStore, scenID, testID string) *TestPerf {
	if p == nil {
		return nil
	}
	sp, ok := p.Scenarios[scenID]
	if !ok {
		return nil
	}
	tp, ok := sp.Tests[testID]
	if !ok {
		return nil
	}
	return &tp
}

func durationFromMs(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// preview renders a value as a compact JSON string capped at
// previewLimit characters.
func preview(v any) string {
	bytes, err := json.Marshal(v)
	if err != nil {
		s := fmt.Sprintf("%v", v)
		if len(s) > previewLimit {
			return s[:previewLimit] + "…"
		}
		return s
	}
	s := string(bytes)
	if len(s) > previewLimit {
		return s[:previewLimit] + "…"
	}
	return s
}

// Write persists Render to a file path, creating parent dirs.
func Write(path string, r *RunReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(Render(r)), 0o644)
}

// WriteApproved persists RenderApproved to a file path.
func WriteApproved(path string, r *RunReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(RenderApproved(r)), 0o644)
}
