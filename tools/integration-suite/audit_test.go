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
