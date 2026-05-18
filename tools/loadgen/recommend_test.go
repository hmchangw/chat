package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecommend_PicksPresetByTargetRPS(t *testing.T) {
	var buf bytes.Buffer
	// Low rps → small preset
	code := runRecommendTo(&buf, []string{"--target-rps=10", "--duration=1m"})
	require.Equal(t, 0, code)
	out := buf.String()
	assert.Contains(t, out, "preset:")
	assert.Contains(t, out, "abort-window-max-samples:")
}

func TestRecommend_HighRPSPicksLargePreset(t *testing.T) {
	var buf bytes.Buffer
	code := runRecommendTo(&buf, []string{"--target-rps=1000", "--duration=5m"})
	require.Equal(t, 0, code)
	out := buf.String()
	assert.Contains(t, out, "large")
}

func TestRecommend_MediumRPSPicksMediumPreset(t *testing.T) {
	var buf bytes.Buffer
	code := runRecommendTo(&buf, []string{"--target-rps=200", "--duration=5m"})
	require.Equal(t, 0, code)
	out := buf.String()
	assert.Contains(t, out, "medium")
}

func TestRecommend_InvalidFlag(t *testing.T) {
	var buf bytes.Buffer
	code := runRecommendTo(&buf, []string{"--not-a-flag=1"})
	assert.Equal(t, 2, code)
}
