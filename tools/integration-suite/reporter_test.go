package integrationsuite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderSummary_TwoScoreSplit(t *testing.T) {
	summary := &RunSummary{
		RunID:    "7a2c",
		StartISO: "2026-05-12T14:22:11Z",
		Duration: "4m12s",
		Approved: ScopeSummary{
			Passed:    108,
			Failed:    5,
			Blindspot: 3,
			FailureByClass: map[Class]int{
				ClassHandlerError: 2,
				ClassTimeout:      2,
				ClassPersistence:  1,
			},
		},
		Draft: ScopeSummary{
			Passed:    30,
			Failed:    7,
			Blindspot: 5,
			FailureByClass: map[Class]int{
				ClassHandlerError: 2,
				ClassTimeout:      3,
				ClassUnreachable:  2,
			},
		},
	}

	out := RenderSummary(summary)

	assert.Contains(t, out, "APPROVED")
	assert.Contains(t, out, "DRAFT")
	assert.Contains(t, out, "93.1%")   // 108 / (108+5+3) = 0.9310...
	assert.Contains(t, out, "71.4%")   // 30 / (30+7+5)   = 0.7142...
	assert.Contains(t, out, "7a2c")
	assert.Contains(t, out, "HandlerError")
}

func TestRenderSummary_EmptyApprovedReportsZero(t *testing.T) {
	summary := &RunSummary{
		RunID:    "0000",
		StartISO: "2026-05-12T00:00:00Z",
		Duration: "0s",
		Approved: ScopeSummary{},
		Draft:    ScopeSummary{},
	}

	out := RenderSummary(summary)
	require.NotEmpty(t, out)
	assert.Contains(t, out, "0 scenarios")
}

func TestRenderSummary_FailureRowsIncludeStatusAndTrace(t *testing.T) {
	summary := &RunSummary{
		RunID:    "7a2c",
		StartISO: "2026-05-12T14:22:11Z",
		Duration: "1s",
		Approved: ScopeSummary{Passed: 1},
		Failures: []FailureRow{
			{
				Status:      StatusApproved,
				FeatureFile: "service/room.feature",
				Line:        12,
				Name:        "Reading a missing room",
				Class:       ClassHandlerError,
				TraceID:     "4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7",
			},
		},
	}

	out := RenderSummary(summary)
	assert.True(t, strings.Contains(out, "[APPROVED]"))
	assert.Contains(t, out, "service/room.feature:12")
	assert.Contains(t, out, "HandlerError")
	assert.Contains(t, out, "4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7")
}
