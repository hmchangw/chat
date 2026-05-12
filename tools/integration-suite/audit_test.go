package integrationsuite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderAuditChecklist_OnlyApprovedScenariosSampled(t *testing.T) {
	approved := []AuditRow{
		{FeatureFile: "service/room.feature", Line: 5, Name: "A passed", Outcome: "Passed"},
		{FeatureFile: "service/room.feature", Line: 18, Name: "B passed", Outcome: "Passed"},
	}

	out := RenderAuditChecklist("7a2c", approved)

	assert.Contains(t, out, "7a2c")
	assert.Contains(t, out, "service/room.feature:5")
	assert.Contains(t, out, "service/room.feature:18")
}

func TestSampleApproved_NeverReturnsMoreThanN(t *testing.T) {
	all := []AuditRow{
		{FeatureFile: "a.feature", Line: 1, Name: "x"},
		{FeatureFile: "b.feature", Line: 2, Name: "y"},
		{FeatureFile: "c.feature", Line: 3, Name: "z"},
	}
	got := SampleApproved(all, 2, 1234)
	require.Len(t, got, 2)
}

func TestSampleApproved_NisLargerThanPopulation(t *testing.T) {
	all := []AuditRow{{FeatureFile: "a.feature", Line: 1, Name: "x"}}
	got := SampleApproved(all, 10, 1234)
	require.Len(t, got, 1)
}

func TestSampleApproved_DeterministicForSeed(t *testing.T) {
	all := []AuditRow{
		{FeatureFile: "a.feature", Line: 1, Name: "x"},
		{FeatureFile: "b.feature", Line: 2, Name: "y"},
		{FeatureFile: "c.feature", Line: 3, Name: "z"},
	}
	a := SampleApproved(all, 2, 99)
	b := SampleApproved(all, 2, 99)
	assert.Equal(t, a, b)
}

func TestRenderAuditChecklist_NoApprovedScenarios(t *testing.T) {
	out := RenderAuditChecklist("7a2c", nil)
	assert.True(t, strings.Contains(out, "no approved scenarios"))
}

func TestTally_HappyPath(t *testing.T) {
	rows := []AuditClass{ClassTP, ClassTP, ClassFP, ClassFN, ClassTN, ClassTP}
	m := Tally(rows)

	assert.Equal(t, 3, m.TP)
	assert.Equal(t, 1, m.TN)
	assert.Equal(t, 1, m.FP)
	assert.Equal(t, 1, m.FN)
	assert.InDelta(t, 66.67, m.AccuracyPct(), 0.01)
}

func TestRenderTally_IncludesAllNumbers(t *testing.T) {
	m := ConfusionMatrix{TP: 22, TN: 5, FP: 2, FN: 1}
	out := RenderTally("7a2c", m)
	assert.Contains(t, out, "7a2c")
	assert.Contains(t, out, "TP=22")
	assert.Contains(t, out, "FP=2")
	assert.Contains(t, out, "FN=1")
	assert.Contains(t, out, "TN=5")
}

func TestParseChecklistClasses_ReadsClassColumn(t *testing.T) {
	md := "" +
		"# Suite Audit — run 7a2c\n\n" +
		"| Scenario | Outcome | Reviewer says | Class |\n" +
		"|---|---|---|---|\n" +
		"| f.feature:1 a | Passed | ok | TP |\n" +
		"| f.feature:2 b | Failed | bug | TN |\n" +
		"| f.feature:3 c | Passed | wrong | FP |\n"

	got, err := ParseChecklistClasses([]byte(md))
	require.NoError(t, err)
	assert.Equal(t, []AuditClass{ClassTP, ClassTN, ClassFP}, got)
}

func TestParseChecklistClasses_SkipsEmptyClassColumn(t *testing.T) {
	md := "" +
		"| Scenario | Outcome | Reviewer says | Class |\n" +
		"|---|---|---|---|\n" +
		"| f.feature:1 a | Passed | ok |  |\n" +
		"| f.feature:2 b | Failed | bug | TN |\n"

	got, err := ParseChecklistClasses([]byte(md))
	require.NoError(t, err)
	assert.Equal(t, []AuditClass{ClassTN}, got)
}
