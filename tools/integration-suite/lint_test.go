package integrationsuite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractBlindspotSlugs_FromFeatureText(t *testing.T) {
	feat := `Feature: X
  @status:approved @blindspot:foo @smoke
  Scenario: A
    Given y

  @blindspot:bar
  Scenario: B
    Given z
`
	got := ExtractBlindspotSlugs(feat)
	assert.ElementsMatch(t, []string{"foo", "bar"}, got)
}

func TestExtractRegisteredSlugs_FromRegisterMarkdown(t *testing.T) {
	reg := `# Blindspots

## foo

text…

## bar

more text
`
	got := ExtractRegisteredSlugs(reg)
	assert.ElementsMatch(t, []string{"foo", "bar"}, got)
}

func TestDiffSlugs_Symmetric(t *testing.T) {
	inFeatures := []string{"foo", "bar"}
	inRegister := []string{"foo", "baz"}

	missingInRegister, missingInFeatures := DiffSlugs(inFeatures, inRegister)
	require.Len(t, missingInRegister, 1)
	assert.Contains(t, missingInRegister, "bar")
	require.Len(t, missingInFeatures, 1)
	assert.Contains(t, missingInFeatures, "baz")
}
