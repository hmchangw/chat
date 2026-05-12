package integrationsuite

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusFromTags_DefaultIsDraft(t *testing.T) {
	assert.Equal(t, StatusDraft, StatusFromTags([]string{"@smoke"}))
	assert.Equal(t, StatusDraft, StatusFromTags([]string{}))
}

func TestStatusFromTags_ApprovedTagWins(t *testing.T) {
	assert.Equal(t, StatusApproved, StatusFromTags([]string{"@smoke", "@status:approved"}))
}

func TestStatusFromTags_UnknownStatusTagIgnored(t *testing.T) {
	// Defensive: unknown @status:xxx falls back to draft, no panic.
	assert.Equal(t, StatusDraft, StatusFromTags([]string{"@status:weirdvalue"}))
}

func TestBlindspotsFromTags(t *testing.T) {
	tags := []string{"@smoke", "@blindspot:partial-history", "@blindspot:another-thing"}
	got := BlindspotsFromTags(tags)
	assert.Equal(t, []string{"partial-history", "another-thing"}, got)
}

func TestBlindspotsFromTags_NoBlindspot(t *testing.T) {
	got := BlindspotsFromTags([]string{"@smoke"})
	assert.Empty(t, got)
}
