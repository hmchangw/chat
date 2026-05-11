package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/nats.go"
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

func TestRateBucketLabel(t *testing.T) {
	cases := []struct {
		rate int
		want string
	}{
		{0, "0"},
		{1, "0-10"},
		{10, "0-10"},
		{11, "10-50"},
		{500, "200-500"},
		{1000, "500-1000"},
		{9999, "5000-10000"},
		{10000, "5000-10000"},
		{15000, "10000+"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, rateBucketLabel(tc.rate), "rateBucketLabel(%d)", tc.rate)
	}
}

func TestPublishedMetric_HasConnAndRateBucketLabels(t *testing.T) {
	// The Phase 3 §3.6 verification gate: loadgen_published_total must
	// admit conn_id and rate_bucket labels.
	m := NewMetrics()
	m.Published.WithLabelValues("test", "measured", "0", "100-200").Inc()
	m.Published.WithLabelValues("test", "measured", "1", "100-200").Inc()
	m.Published.WithLabelValues("test", "measured", "2", "200-500").Inc()

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != "loadgen_published_total" {
			continue
		}
		// Three unique label combinations.
		require.Len(t, mf.GetMetric(), 3)
		seenConnIDs := map[string]struct{}{}
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "conn_id" {
					seenConnIDs[lp.GetValue()] = struct{}{}
				}
			}
		}
		assert.Equal(t, map[string]struct{}{"0": {}, "1": {}, "2": {}}, seenConnIDs)
		return
	}
	t.Fatal("loadgen_published_total not found")
}

func TestConnPool_ForReturnsObserverWhenSizeOne(t *testing.T) {
	// Size==1 pool short-circuits For() to the observer (which is nil
	// in NewTestConnPool, so we just verify the contract holds).
	pool := NewTestConnPool(1)
	// IndexFor must return 0 regardless of user.
	assert.Equal(t, 0, pool.IndexFor("anyone"))
	assert.Equal(t, 0, pool.IndexFor("else"))
	// Size reports 1.
	assert.Equal(t, 1, pool.Size())
}

func TestConnPool_DrainIsIdempotent(t *testing.T) {
	// With a test pool (no real conns), Drain is a no-op that doesn't panic.
	pool := NewTestConnPool(4)
	pool.Drain()
	pool.Drain() // second call must not panic
}

func TestConnPool_ObserverPublic(t *testing.T) {
	pool := NewTestConnPool(3)
	// NewTestConnPool leaves observer nil — accessor is well-defined.
	assert.Nil(t, pool.Observer(), "test pool has no observer; method must still be callable")
}

func TestNewConnPoolWithObserver_RequiresObserver(t *testing.T) {
	_, err := NewConnPoolWithObserver(nil, "", "", 1, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "observer")
}

func TestNewConnPoolWithObserver_SizeOneSkipsDialFn(t *testing.T) {
	// With dataSize <= 1, the dial fn is never called; observer alone is the pool.
	// Pass a dial fn that would fail if invoked to lock down the contract.
	failDial := func(_, _ string) (*nats.Conn, error) {
		return nil, errors.New("dial must NOT be called for size <= 1")
	}
	// We can't construct a real *nats.Conn without dialing, so use the
	// hash-only path: pool returned must report Size()==1 and observer nil.
	// Note: passing a non-nil observer is required, but for size <= 1 the
	// observer is the only conn used; a nil-ish stub works.
	stub := &nats.Conn{}
	pool, err := NewConnPoolWithObserver(stub, "", "", 1, failDial)
	require.NoError(t, err)
	assert.Equal(t, 1, pool.Size())
	assert.Same(t, stub, pool.Observer())
}

func TestNewConnPoolWithObserver_RequiresDialWhenMulti(t *testing.T) {
	stub := &nats.Conn{}
	_, err := NewConnPoolWithObserver(stub, "", "", 4, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dial")
}

func TestNewConnPool_RequiresDialFn(t *testing.T) {
	_, err := NewConnPool("nats://localhost:4222", "", 2, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dial")
}

func TestConnPool_ForReturnsFromConnsSlice(t *testing.T) {
	// With size>1 and nil conns (NewTestConnPool's shape), For returns the
	// nil slot at the hashed index. Verifies we don't crash and stays
	// deterministic.
	pool := NewTestConnPool(4)
	got := pool.For("alice")
	assert.Nil(t, got, "test pool's conns are all nil; For must return that without crashing")
	// Calling twice produces the same result (deterministic hash).
	assert.Same(t, got, pool.For("alice"))
}

// TestNewConnPool_StubDial exercises the multi-dial happy path of
// NewConnPool without a real NATS server. The stub returns a fresh
// *nats.Conn each call (without going through nats.Connect), so the
// pool ends up with N distinct slots. Closes the previous 11.8%
// coverage gap on NewConnPool.
func TestNewConnPool_StubDial(t *testing.T) {
	calls := 0
	stubDial := func(_, _ string) (*nats.Conn, error) {
		calls++
		return &nats.Conn{}, nil
	}
	pool, err := NewConnPool("nats://stub", "", 3, stubDial)
	require.NoError(t, err)
	require.NotNil(t, pool)
	// NewConnPool opens observer + N data conns when N>1.
	assert.Equal(t, 4, calls, "NewConnPool with dataSize=3 must dial observer + 3 data conns")
	assert.Equal(t, 3, pool.Size())
	assert.NotNil(t, pool.Observer())
}

func TestNewConnPool_SizeOneOpensOnlyObserver(t *testing.T) {
	calls := 0
	stubDial := func(_, _ string) (*nats.Conn, error) {
		calls++
		return &nats.Conn{}, nil
	}
	pool, err := NewConnPool("nats://stub", "", 1, stubDial)
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "size=1 opens only the observer connection")
	assert.Equal(t, 1, pool.Size())
}

func TestNewConnPool_ObserverDialFailure(t *testing.T) {
	stubDial := func(_, _ string) (*nats.Conn, error) {
		return nil, errors.New("connect refused")
	}
	_, err := NewConnPool("nats://stub", "", 3, stubDial)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "observer")
}

// Mid-pool dial-failure path triggers nats.Conn.Drain on prior conns
// which panics on a zero-value &nats.Conn{}. Covered by integration
// tests against real NATS via testcontainers; not unit-testable here.

func TestNewConnPoolWithObserver_MultiDialPath(t *testing.T) {
	stubDial := func(_, _ string) (*nats.Conn, error) {
		return &nats.Conn{}, nil
	}
	stub := &nats.Conn{}
	pool, err := NewConnPoolWithObserver(stub, "nats://stub", "", 4, stubDial)
	require.NoError(t, err)
	assert.Equal(t, 4, pool.Size())
	assert.Same(t, stub, pool.Observer())
}

func TestLoadCredsDir_Empty(t *testing.T) {
	got, err := LoadCredsDir("")
	require.NoError(t, err)
	assert.Empty(t, got, "empty dir → empty slice, no error")
}

func TestLoadCredsDir_NonExistent(t *testing.T) {
	_, err := LoadCredsDir("/nonexistent-path-no-really")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read creds dir")
}

func TestLoadCredsDir_PicksOnlyCredsFiles(t *testing.T) {
	dir := t.TempDir()
	// Write a mix of *.creds and decoys (ignored).
	for _, n := range []string{"alice.creds", "bob.creds", "readme.txt", "ignored.bak"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("stub"), 0o600))
	}
	// And a subdirectory — also ignored.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0o700))

	got, err := LoadCredsDir(dir)
	require.NoError(t, err)
	assert.Len(t, got, 2, "must pick only *.creds")
	// Sorted lexically → alice before bob.
	assert.True(t, len(got) == 2 && filepath.Base(got[0]) == "alice.creds")
	assert.True(t, len(got) == 2 && filepath.Base(got[1]) == "bob.creds")
}

func TestNewConnPoolWithCreds_FallsBackWhenCredsListEmpty(t *testing.T) {
	calls := 0
	credUsed := ""
	stubDial := func(_, creds string) (*nats.Conn, error) {
		calls++
		credUsed = creds
		return &nats.Conn{}, nil
	}
	pool, err := NewConnPoolWithCreds(&nats.Conn{}, "nats://stub", "/path/to/global.creds", nil, 3, stubDial)
	require.NoError(t, err)
	assert.Equal(t, 3, pool.Size())
	assert.Equal(t, 3, calls)
	assert.Equal(t, "/path/to/global.creds", credUsed, "empty credsFiles → cfg fallback")
}

func TestNewConnPoolWithCreds_RotatesAcrossDataConns(t *testing.T) {
	credsFiles := []string{"/a.creds", "/b.creds"}
	got := []string{}
	stubDial := func(_, creds string) (*nats.Conn, error) {
		got = append(got, creds)
		return &nats.Conn{}, nil
	}
	pool, err := NewConnPoolWithCreds(&nats.Conn{}, "nats://stub", "", credsFiles, 4, stubDial)
	require.NoError(t, err)
	assert.Equal(t, 4, pool.Size())
	assert.Equal(t, []string{"/a.creds", "/b.creds", "/a.creds", "/b.creds"}, got,
		"data conns must rotate creds round-robin")
}
