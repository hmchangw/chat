package main

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSustainableOps(t *testing.T) {
	tests := []struct {
		name        string
		pools       CandidatePools
		usersPerAdd int
		want        int
	}{
		{"nil pools", nil, 10, 0},
		{"empty pools", CandidatePools{}, 10, 0},
		{"zero usersPerAdd", CandidatePools{"r1": make([]string, 50)}, 0, 0},
		{"negative usersPerAdd", CandidatePools{"r1": make([]string, 50)}, -1, 0},
		{"single room exact", CandidatePools{"r1": make([]string, 50)}, 10, 5},
		{"single room floor", CandidatePools{"r1": make([]string, 55)}, 10, 5},
		{"multi room", CandidatePools{"r1": make([]string, 50), "r2": make([]string, 30)}, 10, 8},
		{"room below threshold contributes zero", CandidatePools{"r1": make([]string, 9)}, 10, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SustainableOps(tc.pools, tc.usersPerAdd))
		})
	}
}

func TestValidateSustainedCapacity_WithinBudget(t *testing.T) {
	// capacity = 2 rooms * floor(50/10) = 10 ops; demand = 5/s * 1s = 5 ops.
	pools := CandidatePools{"r1": make([]string, 50), "r2": make([]string, 50)}
	err := ValidateSustainedCapacity("members-x", pools, 5, time.Second, 10)
	assert.NoError(t, err)
}

func TestValidateSustainedCapacity_AtBudgetBoundary(t *testing.T) {
	// capacity = 10 ops; demand = 10/s * 1s = 10 ops — exactly equal must pass.
	pools := CandidatePools{"r1": make([]string, 50), "r2": make([]string, 50)}
	err := ValidateSustainedCapacity("members-x", pools, 10, time.Second, 10)
	assert.NoError(t, err)
}

func TestValidateSustainedCapacity_Oversubscribed(t *testing.T) {
	// capacity = floor(60000/10) = 6000 ops; demand = 100/s * 120s = 12000 ops.
	// Achievable bounds are non-degenerate: maxRate=50, maxDuration=60s.
	pools := CandidatePools{"r1": make([]string, 60000)}
	err := ValidateSustainedCapacity("members-x", pools, 100, 120*time.Second, 10)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInsufficientPool),
		"oversubscribed validation must wrap ErrInsufficientPool")
	// The message must be actionable: name the preset and the achievable bounds.
	msg := err.Error()
	assert.Contains(t, msg, "members-x")
	assert.Contains(t, msg, "--rate 50")
	assert.Contains(t, msg, "--duration 1m0s")
}

func TestValidateSustainedCapacity_TooSmallForWorkload(t *testing.T) {
	// capacity = 25 ops; at rate=100/60s the achievable bounds round to zero,
	// so the message must steer to a larger preset rather than "--rate 0".
	pools := CandidatePools{"r1": make([]string, 250)}
	err := ValidateSustainedCapacity("members-small", pools, 100, 60*time.Second, 10)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInsufficientPool))
	msg := err.Error()
	assert.Contains(t, msg, "members-small")
	assert.Contains(t, msg, "larger preset")
	assert.NotContains(t, msg, "--rate 0", "must not suggest a nonsensical zero rate")
	assert.NotContains(t, msg, "--duration 0s", "must not suggest a nonsensical zero duration")
}

func TestValidateSustainedCapacity_ZeroUsersPerAdd(t *testing.T) {
	pools := CandidatePools{"r1": make([]string, 50)}
	err := ValidateSustainedCapacity("members-x", pools, 10, time.Second, 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInsufficientPool))
}

// The default members-sustained invocation (rate=100, duration=60s,
// usersPerAdd=10) must be satisfiable by the members-medium preset so the
// documented out-of-box command completes instead of aborting on exhaustion.
func TestMembersMedium_SustainsDefaultInvocation(t *testing.T) {
	p, ok := BuiltinMembersPreset("members-medium")
	require.True(t, ok)
	_, pools := BuildMembersFixtures(&p, 42, "site-A")

	const (
		defaultRate        = 100
		defaultDuration    = 60 * time.Second
		defaultUsersPerAdd = 10
	)
	require.NoError(t,
		ValidateSustainedCapacity(p.Name, pools, defaultRate, defaultDuration, defaultUsersPerAdd),
		"members-medium must sustain the default 100rps/60s/10-per-add invocation")

	// Stays under MAX_ROOM_SIZE (1000): baseline members + full pool per room.
	for roomID, pool := range pools {
		assert.LessOrEqual(t, p.BaselineSize+len(pool), 1000,
			"room %s baseline+pool must fit under MAX_ROOM_SIZE", roomID)
	}
}
