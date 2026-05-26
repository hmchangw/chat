package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
)

func TestRender_HappyRunZeroFails(t *testing.T) {
	r := RunReport{
		RunID:    "abcd",
		Duration: "1s",
		Cases: []CaseReport{
			{ScenarioName: "verified_user_creates_channel_room", Subset: "happy", Verdict: Verdict{Outcome: "pass"}},
		},
	}
	out := Render(&r)
	assert.Contains(t, out, "abcd")
	// Confusion matrix uses a 2-column form, not "pass: 1" — assert on
	// row+col presence instead.
	assert.Contains(t, out, "Confusion matrix")
	assert.Contains(t, out, "DRAFT")
}

func TestRender_ConfusionMatrixKindByOutcome(t *testing.T) {
	r := RunReport{
		Cases: []CaseReport{
			{ScenarioName: "p1", Kind: "positive", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "p2", Kind: "positive", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "p3", Kind: "positive", Verdict: Verdict{Outcome: "fail"}},
			{ScenarioName: "n1", Kind: "negative", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "n2", Kind: "negative", Verdict: Verdict{Outcome: "fail"}},
			// Empty Kind defaults to positive at runner level; the
			// reporter expects Kind already populated.
		},
	}
	out := Render(&r)
	assert.Contains(t, out, "Confusion matrix")
	assert.Contains(t, out, "true (pass)")
	assert.Contains(t, out, "false (fail)")
	assert.Contains(t, out, "+ve (through)")
	assert.Contains(t, out, "-ve (error/warning)")
	// 2 positive pass, 1 positive fail
	assert.True(t, strings.Contains(out, "+ve (through)         2"), "+ve true count should be 2; got:\n%s", out)
	// 1 negative pass, 1 negative fail
	assert.True(t, strings.Contains(out, "-ve (error/warning)   1"), "-ve true count should be 1; got:\n%s", out)
}

func TestRender_IncludesHeadingAndMerge(t *testing.T) {
	r := RunReport{
		RunID: "z",
		Git:   GitInfo{HEAD: "abc1234", LatestMerge: "def5678"},
	}
	out := Render(&r)
	assert.Contains(t, out, "Integration tests")
	assert.Contains(t, out, "latest merge: def5678")
}

func TestRenderApproved_FiltersToApprovedOnly(t *testing.T) {
	r := RunReport{
		RunID: "z",
		Cases: []CaseReport{
			{ScenarioName: "a_draft", Subset: "happy", Status: "draft", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "b_approved", Subset: "happy", Status: "approved", Verdict: Verdict{Outcome: "pass"}},
		},
	}
	out := RenderApproved(&r)
	assert.Contains(t, out, "b_approved")
	assert.NotContains(t, out, "a_draft")
	assert.Contains(t, out, "approved")
}

// TestRender_ScenarioHeadingCounts confirms each scenario heading
// carries a "passed/total" suffix so the reader can see how that
// scenario fared without scanning every test line.
func TestRender_ScenarioHeadingCounts(t *testing.T) {
	r := RunReport{
		Cases: []CaseReport{
			{ScenarioName: "a", Subset: "happy", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "a", Subset: "mishap-1", Verdict: Verdict{Outcome: "fail"}},
			{ScenarioName: "b", Subset: "happy", Verdict: Verdict{Outcome: "pass"}},
		},
	}
	out := Render(&r)
	assert.Contains(t, out, "## scenario: a: 1/2")
	assert.Contains(t, out, "## scenario: b: 1/1")
}

func TestRender_PerStepOutcome(t *testing.T) {
	r := RunReport{
		Cases: []CaseReport{
			{
				ScenarioName: "pipeline",
				Subset:       "happy",
				Verdict: Verdict{
					Outcome: "pass",
					Steps: []StepResult{
						{Service: "room-service", Outcome: "pass"},
						{Service: "room-worker", Outcome: "pass"},
					},
				},
			},
		},
	}
	out := Render(&r)
	assert.Contains(t, out, "seq[1] room-service: PASS")
	assert.Contains(t, out, "seq[2] room-worker: PASS")
}

// TestRender_PerStepDurationShown surfaces per-step duration so the
// reader can spot which service in a sequence is the bottleneck.
func TestRender_PerStepDurationShown(t *testing.T) {
	r := RunReport{
		Cases: []CaseReport{
			{
				ScenarioName: "pipeline",
				Subset:       "happy",
				Verdict: Verdict{
					Outcome: "pass",
					Steps: []StepResult{
						{Service: "room-service", Outcome: "pass", Duration: 50 * 1_000_000}, // 50ms
						{Service: "room-worker", Outcome: "pass", Duration: 250 * 1_000_000}, // 250ms
					},
				},
			},
		},
	}
	out := Render(&r)
	assert.Contains(t, out, "seq[1] room-service: PASS")
	assert.Contains(t, out, "50ms")
	assert.Contains(t, out, "seq[2] room-worker: PASS")
	assert.Contains(t, out, "250ms")
}

func TestRender_PerformanceHistoryShown(t *testing.T) {
	r := RunReport{
		Cases: []CaseReport{
			{ScenarioName: "x", Subset: "happy", Verdict: Verdict{Outcome: "pass"}},
		},
		Perf: &PerformanceStore{
			Version: 1,
			Scenarios: map[string]ScenarioPerf{
				"x": {Tests: map[string]TestPerf{
					"happy": {
						LatestPass: &PassRecord{Commit: "latest1", DurationMs: 100, Timestamp: "t1"},
						BestPass:   &PassRecord{Commit: "best1", DurationMs: 50},
						WorstPass:  &PassRecord{Commit: "worst1", DurationMs: 200},
					},
				}},
			},
		},
	}
	out := Render(&r)
	assert.Contains(t, out, "Latest pass:")
	assert.Contains(t, out, "@latest1")
	assert.Contains(t, out, "Best pass:")
	assert.Contains(t, out, "@best1")
	assert.Contains(t, out, "Worst pass:")
	assert.Contains(t, out, "@worst1")
}

// TestRender_PerStepPerformanceHistory confirms each seq line carries
// its own Latest/Best/Worst when the performance store has step-level
// history, so a reader can spot which service is trending slower.
func TestRender_PerStepPerformanceHistory(t *testing.T) {
	r := RunReport{
		Cases: []CaseReport{
			{ScenarioName: "p", Subset: "happy", Verdict: Verdict{
				Outcome: "pass",
				Steps: []StepResult{
					{Service: "room-service", Outcome: "pass", Duration: 50 * time.Millisecond},
					{Service: "room-worker", Outcome: "pass", Duration: 250 * time.Millisecond},
				},
			}},
		},
		Perf: &PerformanceStore{
			Version: 1,
			Scenarios: map[string]ScenarioPerf{
				"p": {Tests: map[string]TestPerf{
					"happy": {
						Steps: []StepPerf{
							{Service: "room-service",
								LatestPass: &PassRecord{Commit: "rsLatest", DurationMs: 50},
								BestPass:   &PassRecord{Commit: "rsBest", DurationMs: 40, Timestamp: "tB"},
								WorstPass:  &PassRecord{Commit: "rsWorst", DurationMs: 65},
							},
							{Service: "room-worker",
								LatestPass: &PassRecord{Commit: "rwLatest", DurationMs: 250},
								BestPass:   &PassRecord{Commit: "rwBest", DurationMs: 180, Timestamp: "tB"},
								WorstPass:  &PassRecord{Commit: "rwWorst", DurationMs: 410},
							},
						},
					},
				}},
			},
		},
	}
	out := Render(&r)
	assert.Contains(t, out, "seq[1] room-service: PASS")
	assert.Contains(t, out, "@rsLatest")
	assert.Contains(t, out, "@rsBest")
	assert.Contains(t, out, "@rsWorst")
	assert.Contains(t, out, "seq[2] room-worker: PASS")
	assert.Contains(t, out, "@rwLatest")
	assert.Contains(t, out, "@rwBest")
	assert.Contains(t, out, "@rwWorst")
}

func TestRender_PerformanceNoPassYet(t *testing.T) {
	r := RunReport{
		Cases: []CaseReport{
			{ScenarioName: "x", Subset: "happy", Verdict: Verdict{Outcome: "fail"}},
		},
		Perf: &PerformanceStore{Version: 1, Scenarios: map[string]ScenarioPerf{}},
	}
	out := Render(&r)
	assert.Contains(t, out, "no pass yet")
}

func TestRender_FailureCounts(t *testing.T) {
	r := RunReport{
		RunID:    "wxyz",
		Duration: "2s",
		Cases: []CaseReport{
			{ScenarioName: "a", Subset: "happy", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "b", Subset: "happy", Verdict: Verdict{
				Outcome: "fail",
				Steps: []StepResult{{Service: "room-service", Outcome: "fail", MissingPositives: []ReadFailure{
					{Service: "room-service", Location: "mongo.rooms", Reason: "no match"},
				}}},
				MissingPositives: []ReadFailure{{Service: "room-service", Location: "mongo.rooms", Reason: "no match"}},
			}},
		},
	}
	out := Render(&r)
	// Confusion matrix shows DRAFT pass=1, fail=1
	assert.Contains(t, out, "DRAFT")
	assert.True(t, strings.Contains(out, "1       1"), "DRAFT row should show 1 pass, 1 fail in the matrix")
	assert.Contains(t, out, "missing-positive")
	assert.Contains(t, out, "mongo.rooms")
}

// TestRender_DiagnosticDetail covers the per-test detail surface:
// expected values on missing-positive, scenario headers, per-step
// outcome lines, and overall PASS/FAIL marks.
func TestRender_DiagnosticDetail(t *testing.T) {
	r := RunReport{
		RunID:    "diag",
		Duration: "1s",
		Cases: []CaseReport{
			{ScenarioName: "a_passes", Subset: "happy", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "b_fails", Subset: "happy", Verdict: Verdict{
				Outcome: "fail",
				Steps: []StepResult{{
					Service: "room-worker",
					Outcome: "fail",
					MissingPositives: []ReadFailure{{
						Service:  "room-worker",
						Location: "mongo.rooms",
						Expected: map[string]any{"_id": "r-pure-test", "type": "channel"},
					}},
				}},
				MissingPositives: []ReadFailure{{
					Service:  "room-worker",
					Location: "mongo.rooms",
					Expected: map[string]any{"_id": "r-pure-test", "type": "channel"},
				}},
			}},
		},
	}
	out := Render(&r)
	// Scenario headers
	assert.Contains(t, out, "scenario: a_passes")
	assert.Contains(t, out, "scenario: b_fails")
	// Per-test outcome
	assert.Contains(t, out, "PASS")
	assert.Contains(t, out, "FAIL")
	// Step + missing-positive detail
	assert.Contains(t, out, "seq[1] room-worker: FAIL")
	assert.Contains(t, out, "missing-positive at mongo.rooms")
	assert.Contains(t, out, "expected:")
	assert.Contains(t, out, "r-pure-test")
}

// TestRender_PayloadPreviewCascade confirms the unexpected-cascade
// detail block surfaces the event's payload (JSON preview) so the
// author can see what fired without flipping to docker logs.
func TestRender_PayloadPreviewCascade(t *testing.T) {
	r := RunReport{
		RunID:    "cas",
		Duration: "1s",
		Cases: []CaseReport{
			{ScenarioName: "c", Subset: "happy", Verdict: Verdict{
				Outcome: "fail",
				UnexpectedCascades: []readers.Event{
					{
						Location: "logs.room-worker",
						OwnerSvc: "room-worker",
						Payload:  map[string]any{"msg": "process message failed", "error": "room key absent for r-x"},
					},
				},
			}},
		},
	}
	out := Render(&r)
	assert.Contains(t, out, "unexpected-cascade at logs.room-worker (owner=room-worker)")
	assert.Contains(t, out, "payload:")
	assert.Contains(t, out, "room key absent")
}

// TestPreview_TruncatesLongValues confirms the preview helper caps
// long payloads so the report stays readable.
func TestPreview_TruncatesLongValues(t *testing.T) {
	long := map[string]any{"field": strings.Repeat("x", 500)}
	out := preview(long)
	// Should be capped near previewLimit + truncation marker
	assert.True(t, len(out) <= previewLimit+5, "preview should cap output, got %d chars", len(out))
	assert.Contains(t, out, "…")
}
