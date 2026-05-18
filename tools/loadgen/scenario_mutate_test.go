package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMutateScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("message-mutate")
	require.True(t, ok)
	assert.Equal(t, "message-mutate", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestPickMutationKind_RespectsEditDeleteSplit(t *testing.T) {
	p := &Preset{EditRate: 0.7, DeleteRate: 0.3}
	rng := newDeterministicRand(42)
	edits := 0
	deletes := 0
	for i := 0; i < 1000; i++ {
		if pickMutationKind(p, rng) == "edit" {
			edits++
		} else {
			deletes++
		}
	}
	// 70% edit ± 5% tolerance.
	assert.InDelta(t, 700, edits, 50, "expected ~70%% edits; got %d", edits)
	assert.InDelta(t, 300, deletes, 50)
}

func TestEditAgeDistribution_PartitionsCorrectly(t *testing.T) {
	// Fill the "typo" (last 30s) ring with 10 recent messages,
	// "correction" (24h) ring with 100 older messages.
	// With distribution=0.7,0.3, expect ~70% picks from typo, ~30% from correction.
	typo := []string{}
	for i := 0; i < 10; i++ {
		typo = append(typo, "typo-msg-"+padID(i))
	}
	correction := []string{}
	for i := 0; i < 100; i++ {
		correction = append(correction, "corr-msg-"+padID(i))
	}

	rng := newDeterministicRand(42)
	typoPicks := 0
	correctionPicks := 0
	for i := 0; i < 1000; i++ {
		id, ring := pickEditTargetByAge(typo, correction, 0.7, rng)
		if ring == "typo" {
			typoPicks++
		} else {
			correctionPicks++
		}
		_ = id
	}
	assert.InDelta(t, 700, typoPicks, 50)
	assert.InDelta(t, 300, correctionPicks, 50)
}

func TestParseEditAgeDistribution_ValidAndInvalid(t *testing.T) {
	tests := []struct {
		in       string
		wantTypo float64
		wantErr  bool
	}{
		{"0.7,0.3", 0.7, false},
		{"0.5,0.5", 0.5, false},
		{"1.0,0.0", 1.0, false},
		{"0.7", 0, true},     // missing comma
		{"0.6,0.5", 0, true}, // doesn't sum to 1
		{"", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			typo, err := parseEditAgeDistribution(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.wantTypo, typo, 0.001)
		})
	}
}
