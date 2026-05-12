package integrationsuite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewIDPrefixer_FormatIsItRunScenarioEntity(t *testing.T) {
	p := NewIDPrefixer("7a2c", "room-create-01")

	got := p.ID("alice")
	assert.Equal(t, "it-7a2c-room-create-01-alice", got)
}

func TestNewIDPrefixer_DifferentScenariosDoNotCollide(t *testing.T) {
	p1 := NewIDPrefixer("7a2c", "scenario-a")
	p2 := NewIDPrefixer("7a2c", "scenario-b")

	assert.NotEqual(t, p1.ID("alice"), p2.ID("alice"))
}

func TestGenerateRunID_Is4HexChars(t *testing.T) {
	id := GenerateRunID()
	assert.Len(t, id, 4)
	for _, r := range id {
		assert.True(t, (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'),
			"non-hex char in run ID: %q", string(r))
	}
}

func TestGenerateRunID_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := GenerateRunID()
		if seen[id] {
			continue
		}
		seen[id] = true
	}
	assert.Greater(t, len(seen), 900, "expected near-unique run IDs over 1000 generations")
}

func TestScenarioIDFromName_KebabCasedFromGherkinName(t *testing.T) {
	cases := map[string]string{
		"Adding a member persists subscription": "adding-a-member-persists-subscription",
		"DM room is deterministic":              "dm-room-is-deterministic",
		"Outline: row 3":                        "outline-row-3",
	}
	for input, want := range cases {
		got := ScenarioIDFromName(input)
		assert.Equal(t, want, got, "input: %q", input)
		assert.False(t, strings.Contains(got, " "), "must not contain spaces")
	}
}
