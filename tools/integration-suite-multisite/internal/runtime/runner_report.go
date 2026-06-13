package runtime

import (
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
)

// Stubbed for multi-site refactor; Task 14 will reimplement.

// scenarioVerdict is the per-scenario outcome used by the stubbed runner.
type scenarioVerdict struct {
	Outcome string
	Reason  string
	// FlowVerdicts is populated for flow scenarios; nil for legacy.
	// When present, recordScenario emits one PerformanceStore row per
	// step (keyed "<scenarioName>/<stepID>") alongside the top-level
	// scenario row.
	FlowVerdicts []StepVerdict
}

// recordScenario adapts a scenarioVerdict into the CaseReport / CaseLatest
// shape so the reporter and PerformanceStore keep working. This is a
// stub — Task 14 will reimplement with proper per-case recording.
func recordScenario(perf *PerformanceStore, report *RunReport,
	s *scenario.Scenario, v scenarioVerdict, dur time.Duration) {
	caseID := s.Name + "/stub"
	latest := &CaseLatest{
		RanAt:      report.StartISO,
		Verdict:    v.Outcome,
		DurationMs: dur.Milliseconds(),
	}
	if v.Outcome == "fail" {
		latest.ErrorBlocks = []ErrorBlock{{Attempt: 1, Label: v.Reason}}
	}
	perf.RecordExecuted(caseID, latest)

	// Flow scenarios: emit per-step PerformanceStore rows so step-level
	// latency tracks over time (spec §7). Each step's caseID is
	// "<scenarioName>/<stepID>". Legacy scenarios (FlowVerdicts == nil)
	// skip this block entirely.
	for _, sv := range v.FlowVerdicts {
		stepCaseID := s.Name + "/" + sv.ID
		stepLatest := &CaseLatest{
			RanAt:      report.StartISO,
			Verdict:    stepVerdictString(sv),
			DurationMs: sv.Duration.Milliseconds(),
		}
		if !sv.Pass && !sv.HaltedUpstream && sv.Reason != "" {
			stepLatest.ErrorBlocks = []ErrorBlock{{Attempt: 1, Label: sv.Reason}}
		}
		perf.RecordExecuted(stepCaseID, stepLatest)
	}

	report.Cases = append(report.Cases, CaseReport{
		ScenarioName: s.Name,
		SourcePath:   s.SourcePath,
		Subset:       "scenario",
		Status:       v3Status(s),
		Kind:         s.Tag,
		Duration:     dur,
		Verdict:      Verdict{Outcome: v.Outcome, Reason: v.Reason},
	})
}

// stepVerdictString maps a flow StepVerdict to a verdict label for the
// PerformanceStore row.
func stepVerdictString(sv StepVerdict) string {
	switch {
	case sv.HaltedUpstream:
		return "halted-upstream"
	case sv.Pass:
		return "pass"
	default:
		return "fail"
	}
}

// v3Status mirrors v2's status(*Scenario) — returns "approved" for the
// gated CI-relevant scenarios and "draft" otherwise.
func v3Status(s *scenario.Scenario) string {
	if s.Status == "approved" {
		return "approved"
	}
	return "draft"
}
