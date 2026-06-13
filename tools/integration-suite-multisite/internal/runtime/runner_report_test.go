package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
)

func newReportFixture() (*PerformanceStore, *RunReport) {
	return NewPerformanceStore(), &RunReport{StartISO: "2026-06-13T00:00:00Z"}
}

func TestRecordScenario_Legacy_Unchanged(t *testing.T) {
	// Without FlowVerdicts, recordScenario behaves exactly as today —
	// one CaseReport, one PerformanceStore row keyed by "<name>/stub".
	perf, report := newReportFixture()
	s := &scenario.Scenario{Name: "legacy-x", Tag: "positive"}
	recordScenario(perf, report,
		s, scenarioVerdict{Outcome: "pass"}, 100*time.Millisecond)
	require.Len(t, report.Cases, 1)
	assert.Equal(t, "legacy-x", report.Cases[0].ScenarioName)
}

func TestRecordScenario_FlowShape_PerIDRows(t *testing.T) {
	// A scenarioVerdict carrying FlowVerdicts records one PerformanceStore
	// row per step in addition to the top-level scenario row.
	perf, report := newReportFixture()
	s := &scenario.Scenario{Name: "flow-x", Tag: "positive"}
	v := scenarioVerdict{
		Outcome: "pass",
		FlowVerdicts: []StepVerdict{
			{ID: "create", Kind: "fire", Pass: true, Duration: 10 * time.Millisecond},
			{ID: "create_accepted", Kind: "observe", Pass: true, Duration: 5 * time.Millisecond},
		},
	}
	recordScenario(perf, report, s, v, 100*time.Millisecond)
	// Per-step rows present in the store.
	assert.True(t, perf.HasCase("flow-x/create"))
	assert.True(t, perf.HasCase("flow-x/create_accepted"))
}

func TestRecordScenario_FlowShape_HaltedUpstream(t *testing.T) {
	// Halted-upstream steps record with Verdict reflecting the halt.
	perf, report := newReportFixture()
	s := &scenario.Scenario{Name: "flow-y", Tag: "positive"}
	v := scenarioVerdict{
		Outcome: "fail",
		Reason:  "step \"gate\" (observe): timed out",
		FlowVerdicts: []StepVerdict{
			{ID: "create", Kind: "fire", Pass: true, Duration: 10 * time.Millisecond},
			{ID: "gate", Kind: "observe", Pass: false, Duration: 10 * time.Second,
				Reason: "no event matched"},
			{ID: "rename", Kind: "fire", HaltedUpstream: true},
		},
	}
	recordScenario(perf, report, s, v, 10*time.Second+50*time.Millisecond)
	// Halted-upstream step still has a row, but its verdict marks the halt.
	require.True(t, perf.HasCase("flow-y/rename"))
}
