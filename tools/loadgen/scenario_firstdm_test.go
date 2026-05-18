package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFirstDMScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("first-dm")
	require.True(t, ok)
	assert.Equal(t, "first-dm", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestFirstDMPool_MonotonicConsumption(t *testing.T) {
	pool := newFirstDMPool([][2]string{
		{"u1", "u2"},
		{"u3", "u4"},
		{"u5", "u6"},
	}, false /* recycle */)

	p1, ok := pool.Next()
	require.True(t, ok)
	assert.Equal(t, [2]string{"u1", "u2"}, p1)

	p2, ok := pool.Next()
	require.True(t, ok)
	assert.Equal(t, [2]string{"u3", "u4"}, p2)

	p3, ok := pool.Next()
	require.True(t, ok)
	assert.Equal(t, [2]string{"u5", "u6"}, p3)

	// Exhausted.
	_, ok = pool.Next()
	assert.False(t, ok, "pool must return false when exhausted (non-recycle)")
}

func TestFirstDMPool_RecycleWraps(t *testing.T) {
	pool := newFirstDMPool([][2]string{
		{"u1", "u2"},
		{"u3", "u4"},
	}, true /* recycle */)

	_, ok := pool.Next()
	require.True(t, ok)
	_, ok = pool.Next()
	require.True(t, ok)
	// Third call should wrap around.
	p, ok := pool.Next()
	require.True(t, ok, "recycle must return true after wraparound")
	assert.Equal(t, [2]string{"u1", "u2"}, p, "wrap returns to first pair")
}

func TestAugmentWithFirstDMFixtures_AddsPairs(t *testing.T) {
	p := &Preset{Name: "test", Users: 10, Rooms: 5}
	f := BuildFixtures(p, 42, "site-local")
	initialUsers := len(f.Users)

	augmentWithFirstDMFixtures(&f, p, 5)

	// Should add 10 users (5 pairs × 2 users).
	assert.Equal(t, initialUsers+10, len(f.Users), "expected 10 new users (5 pairs × 2)")

	// All new users have the loadgen-firstdm- prefix.
	firstDMUsers := 0
	for _, u := range f.Users {
		if hasFirstDMPrefix(u.ID) {
			firstDMUsers++
		}
	}
	assert.Equal(t, 10, firstDMUsers)
}
