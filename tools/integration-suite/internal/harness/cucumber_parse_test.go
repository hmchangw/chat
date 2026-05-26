package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCucumber_CountsPassedAndFailedPerStatus(t *testing.T) {
	doc := []byte(`[
		{
			"uri": "features/service/room.feature",
			"elements": [
				{
					"id": "feat;scen-1",
					"name": "Approved scenario",
					"line": 5,
					"type": "scenario",
					"tags": [{"name": "@status:approved"}],
					"steps": [
						{"name": "step 1", "result": {"status": "passed"}}
					]
				},
				{
					"id": "feat;scen-2",
					"name": "Draft failing",
					"line": 12,
					"type": "scenario",
					"tags": [],
					"steps": [
						{"name": "step a", "result": {"status": "failed", "error_message": "boom"}}
					]
				}
			]
		}
	]`)

	summary, failures, err := ParseCucumber(doc)
	require.NoError(t, err)

	assert.Equal(t, 1, summary.Approved.Passed)
	assert.Equal(t, 0, summary.Approved.Failed)
	assert.Equal(t, 0, summary.Draft.Passed)
	assert.Equal(t, 1, summary.Draft.Failed)

	require.Len(t, failures, 1)
	assert.Equal(t, StatusDraft, failures[0].Status)
	assert.Equal(t, "Draft failing", failures[0].Name)
	assert.Equal(t, "features/service/room.feature", failures[0].FeatureFile)
	assert.Equal(t, 12, failures[0].Line)
}

func TestParseCucumber_BlindspotCountsAsFailureWithReason(t *testing.T) {
	doc := []byte(`[
		{
			"uri": "features/regional-resilience/x.feature",
			"elements": [
				{
					"id": "feat;scen-1",
					"name": "Has blindspot",
					"line": 8,
					"type": "scenario",
					"tags": [{"name": "@status:approved"}, {"name": "@blindspot:foo"}],
					"steps": [
						{"name": "step", "result": {"status": "failed", "error_message": "undocumented behavior: foo"}}
					]
				}
			]
		}
	]`)

	summary, failures, err := ParseCucumber(doc)
	require.NoError(t, err)

	assert.Equal(t, 0, summary.Approved.Failed)
	assert.Equal(t, 1, summary.Approved.Blindspot)

	require.Len(t, failures, 1)
	assert.Equal(t, "foo", failures[0].Reason)
}
