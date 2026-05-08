package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnPool_ForUserHashesDeterministically(t *testing.T) {
	pool := NewTestConnPool(8) // helper exposed in connpool.go for tests
	got1 := pool.IndexFor("alice")
	for i := 0; i < 100; i++ {
		assert.Equal(t, got1, pool.IndexFor("alice"),
			"For(alice) must be stable across calls")
	}
}

func TestConnPool_DistributesUsersAcrossConnections(t *testing.T) {
	pool := NewTestConnPool(10)
	counts := make([]int, 10)
	for i := 0; i < 1000; i++ {
		idx := pool.IndexFor("user-" + itoa(i))
		require.GreaterOrEqual(t, idx, 0)
		require.Less(t, idx, 10)
		counts[idx]++
	}
	// Loose chi-square sanity: no bucket holds more than 200 (2x mean).
	for i, c := range counts {
		assert.LessOrEqual(t, c, 200,
			"connection %d holds too many users: %d (mean=100)", i, c)
		assert.GreaterOrEqual(t, c, 30,
			"connection %d under-allocated: %d", i, c)
	}
}

func TestUserFromSubject(t *testing.T) {
	cases := []struct{ in, want string }{
		{"chat.user.alice.request.rooms.list", "alice"},
		{"chat.user.user-1.request.room.r1.site-local.msg.history", "user-1"},
		{"chat.room.r1.event.member", "chat.room.r1.event.member"}, // not a user subject
		{"", ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, UserFromSubject(tc.in), "UserFromSubject(%q)", tc.in)
	}
}

func TestConnPool_SizeOneCollapsesToObserver(t *testing.T) {
	pool := NewTestConnPool(1)
	// With size 1, every user maps to index 0.
	for i := 0; i < 100; i++ {
		assert.Equal(t, 0, pool.IndexFor("user-"+itoa(i)))
	}
	assert.Equal(t, 1, pool.Size())
}

// Helper for the test data above.
func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [12]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
