package integrationsuite

import "strings"

// Status is the review state of a scenario.
type Status string

const (
	// StatusDraft is the default for scenarios without @status:approved.
	// Their pass/fail does not count toward the authoritative score.
	StatusDraft Status = "draft"

	// StatusApproved marks scenarios the team has accepted as system
	// contracts. They count toward the authoritative score and gate CI.
	StatusApproved Status = "approved"
)

const (
	tagStatusApproved = "@status:approved"
	tagBlindspotPfx   = "@blindspot:"
)

// StatusFromTags returns the scenario status from its Gherkin tags.
// Default is StatusDraft.
func StatusFromTags(tags []string) Status {
	for _, t := range tags {
		if t == tagStatusApproved {
			return StatusApproved
		}
	}
	return StatusDraft
}

// BlindspotsFromTags returns the list of blindspot slugs declared on
// the scenario. Empty if none.
func BlindspotsFromTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if strings.HasPrefix(t, tagBlindspotPfx) {
			out = append(out, strings.TrimPrefix(t, tagBlindspotPfx))
		}
	}
	return out
}
