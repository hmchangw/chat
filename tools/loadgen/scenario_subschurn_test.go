package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubsChurnScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("subscription-churn")
	require.True(t, ok)
	assert.Equal(t, "subscription-churn", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestChurnStateMachine_PreventsDoubleAdd(t *testing.T) {
	sm := newChurnStateMachine()
	sm.OnJoin("user1", "room1")
	// Second join on the same pair should not produce a "valid join" action.
	require.True(t, sm.IsMember("user1", "room1"))
	valid := sm.CanJoin("user1", "room1")
	assert.False(t, valid, "user1 already in room1; second join must be invalid")
}

func TestChurnStateMachine_PreventsDoubleRemove(t *testing.T) {
	sm := newChurnStateMachine()
	sm.OnLeave("user1", "room1")
	// Leave on a non-member must not produce a "valid leave" action.
	require.False(t, sm.IsMember("user1", "room1"))
	valid := sm.CanLeave("user1", "room1")
	assert.False(t, valid, "user1 not in room1; leave must be invalid")
}

func TestChurnStateMachine_AlternatesValidJoinLeave(t *testing.T) {
	sm := newChurnStateMachine()
	assert.True(t, sm.CanJoin("user1", "room1"))
	assert.False(t, sm.CanLeave("user1", "room1"))

	sm.OnJoin("user1", "room1")
	assert.False(t, sm.CanJoin("user1", "room1"))
	assert.True(t, sm.CanLeave("user1", "room1"))

	sm.OnLeave("user1", "room1")
	assert.True(t, sm.CanJoin("user1", "room1"))
	assert.False(t, sm.CanLeave("user1", "room1"))
}

func TestSeedWithChurnFixtures_CreatesPrefixedPool(t *testing.T) {
	// Verify that when --include-churn-fixtures is on, BuildFixtures creates
	// a separate set of users/rooms with the "loadgen-churn-" prefix.
	p := &Preset{Name: "test", Users: 100, Rooms: 50, DMRatio: 0.6}
	f := BuildFixtures(p, 42, "site-local")
	f = augmentWithChurnFixtures(&f, p, 42)
	// 20 churn-rooms + 50 churn-users (or whatever the augment produces).
	churnUsers := 0
	for _, u := range f.Users {
		if hasChurnPrefix(u.ID) {
			churnUsers++
		}
	}
	assert.Greater(t, churnUsers, 0, "augment must add churn-prefixed users")
}
