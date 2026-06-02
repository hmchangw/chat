package cassrepo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int { return &v }

func TestCasDecrement_NilInitial_NoUpdateCalled(t *testing.T) {
	called := false
	result, err := casDecrement(3, nil, func(_ int, _ *int) (bool, *int, error) {
		called = true
		return true, nil, nil
	})
	require.NoError(t, err)
	assert.Nil(t, result, "nil initial must return nil (skip, not zero)")
	assert.False(t, called, "update must not be called when initial is nil")
}

func TestCasDecrement_SingleApply(t *testing.T) {
	initial := intPtr(3)
	result, err := casDecrement(3, initial, func(newVal int, expected *int) (bool, *int, error) {
		assert.Equal(t, 2, newVal)
		require.NotNil(t, expected)
		assert.Equal(t, 3, *expected)
		return true, nil, nil
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2, *result)
}

func TestCasDecrement_ZeroFloor(t *testing.T) {
	initial := intPtr(0)
	result, err := casDecrement(3, initial, func(newVal int, expected *int) (bool, *int, error) {
		assert.Equal(t, 0, newVal)
		require.NotNil(t, expected)
		assert.Equal(t, 0, *expected)
		return true, nil, nil
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, *result)
}

func TestCasDecrement_ConflictThenApply(t *testing.T) {
	initial := intPtr(3)
	calls := 0
	result, err := casDecrement(5, initial, func(newVal int, expected *int) (bool, *int, error) {
		calls++
		if calls == 1 {
			return false, intPtr(2), nil
		}
		assert.Equal(t, 1, newVal)
		require.NotNil(t, expected)
		assert.Equal(t, 2, *expected)
		return true, nil, nil
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, *result)
	assert.Equal(t, 2, calls)
}

// TestCasDecrement_ConflictReturnsNilCurrent verifies that when a CAS conflict
// returns current=nil (Cassandra column became null mid-retry, e.g. via TTL or
// concurrent null-write), casDecrement returns nil rather than materialising a
// zero. A nil result signals "nothing to decrement" — same semantics as nil initial.
func TestCasDecrement_ConflictReturnsNilCurrent(t *testing.T) {
	initial := intPtr(1)
	calls := 0
	result, err := casDecrement(5, initial, func(_ int, _ *int) (bool, *int, error) {
		calls++
		// CAS conflict: Cassandra returns nil for tcount (column is null).
		return false, nil, nil
	})
	require.NoError(t, err)
	assert.Nil(t, result, "nil mid-retry must return nil, not zero")
	// Must have been called exactly once: the first attempt. After current=nil is
	// returned, the function must return early rather than calling update(0, nil)
	// which would fire IF tcount=null and materialise a zero.
	assert.Equal(t, 1, calls, "must not call update(0,nil) after nil current — would materialise zero on null column")
}

func TestCasDecrement_ExhaustsRetries(t *testing.T) {
	initial := intPtr(5)
	current := intPtr(4)
	_, err := casDecrement(3, initial, func(_ int, _ *int) (bool, *int, error) {
		return false, current, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded 3 retries")
}
