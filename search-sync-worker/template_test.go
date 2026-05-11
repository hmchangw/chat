package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIndexTopology_Prod(t *testing.T) {
	shards, replicas := indexTopology(4, 2, false)
	assert.Equal(t, 4, shards)
	assert.Equal(t, 2, replicas)
}

func TestIndexTopology_Dev(t *testing.T) {
	// Dev collapses every input to 1/0 regardless of prod values.
	shards, replicas := indexTopology(4, 2, true)
	assert.Equal(t, 1, shards)
	assert.Equal(t, 0, replicas)
}

func TestCustomAnalyzerSettings_Shape(t *testing.T) {
	got := customAnalyzerSettings()

	analyzer := got["analyzer"].(map[string]any)
	custom := analyzer["custom_analyzer"].(map[string]any)
	assert.Equal(t, "custom", custom["type"])
	assert.Equal(t, "custom_tokenizer", custom["tokenizer"])
	assert.Equal(t, []string{"lowercase"}, custom["filter"])

	tokenizer := got["tokenizer"].(map[string]any)
	tok := tokenizer["custom_tokenizer"].(map[string]any)
	assert.Equal(t, "whitespace", tok["type"])
	assert.Equal(t, []string{"letter", "digit", "punctuation", "symbol"}, tok["token_chars"])
}
