package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// fakeClock is a controllable clock used by tests to drive registry's
// time-dependent behavior (idle reaping, lastUsed touches, ordering).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{t: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newDummyMongo returns a *mongo.Client that does not require a server.
// mongo.Connect with a non-pinged URI succeeds and returns a usable handle
// that we never actually exercise in tests (the registry's closeMongo is swapped out).
func newDummyMongo(t *testing.T) *mongo.Client {
	t.Helper()
	c, err := mongo.Connect(options.Client().ApplyURI("mongodb://127.0.0.1:1"))
	require.NoError(t, err)
	return c
}

// newTestRegistry constructs a registry whose connect functions and close
// hooks are dependency-injected fakes that record activity. Returns the
// registry, the fake clock, and a counters struct for assertions.
type testCounters struct {
	mu             sync.Mutex
	mongoConnects  int
	cassConnects   int
	mongoCloses    int
	cassCloses     int
	cassConnectArg [][]string // hosts received by cassConnect
}

func (tc *testCounters) recordMongoConnect() { tc.mu.Lock(); tc.mongoConnects++; tc.mu.Unlock() }
func (tc *testCounters) recordCassConnect(h []string) {
	tc.mu.Lock()
	tc.cassConnects++
	tc.cassConnectArg = append(tc.cassConnectArg, h)
	tc.mu.Unlock()
}
func (tc *testCounters) recordMongoClose() { tc.mu.Lock(); tc.mongoCloses++; tc.mu.Unlock() }
func (tc *testCounters) recordCassClose()  { tc.mu.Lock(); tc.cassCloses++; tc.mu.Unlock() }

// addCassConnection directly inserts a cassandra connection into r.conns,
// bypassing r.Connect (which would invoke cassServerVersion on the fake
// session and panic on its uninitialized internals). The sentinel session
// pointer is only used to satisfy the c.cass != nil check in closeConnection;
// tests swap r.closeCass so the pointer is never dereferenced.
func addCassConnection(t *testing.T, r *registry, clk *fakeClock, label, keyspace string) string {
	t.Helper()
	id := "cass-" + label
	now := clk.Now()
	c := &connection{
		kind:         "cassandra",
		cass:         &gocql.Session{}, // sentinel; r.closeCass is swapped in tests
		cassKeyspace: keyspace,
		label:        label,
		createdAt:    now,
	}
	c.lastUsed.Store(now.UnixMilli())
	r.mu.Lock()
	r.conns[id] = c
	r.mu.Unlock()
	return id
}

func newTestRegistry(t *testing.T, idleTimeout time.Duration) (*registry, *fakeClock, *testCounters) {
	t.Helper()
	clk := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	tc := &testCounters{}

	r := newRegistry(idleTimeout)
	r.now = clk.Now
	r.mongoConnect = func(_ context.Context, _ string) (*mongo.Client, error) {
		tc.recordMongoConnect()
		return newDummyMongo(t), nil
	}
	r.cassConnect = func(hosts []string, _ string) (*gocql.Session, error) {
		tc.recordCassConnect(hosts)
		return nil, nil
	}
	r.closeMongo = func(_ context.Context, _ *mongo.Client) error {
		tc.recordMongoClose()
		return nil
	}
	r.closeCass = func(_ *gocql.Session) {
		tc.recordCassClose()
	}
	return r, clk, tc
}

func TestRegistry_Connect_Mongo_Success(t *testing.T) {
	r, _, tc := newTestRegistry(t, time.Hour)

	info, err := r.Connect(context.Background(), connectSpec{
		Kind: "mongo", URI: "mongodb://example/", Label: "primary",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, info.ID)
	assert.Equal(t, "mongo", info.Kind)
	assert.Equal(t, "primary", info.Label)
	assert.Empty(t, info.Keyspace)
	assert.Equal(t, 1, tc.mongoConnects)

	list := r.List()
	require.Len(t, list, 1)
	assert.Equal(t, info.ID, list[0].ID)
	assert.Equal(t, "primary", list[0].Label)
}

func TestRegistry_Connect_Cassandra_Success(t *testing.T) {
	r, _, tc := newTestRegistry(t, time.Hour)

	info, err := r.Connect(context.Background(), connectSpec{
		Kind: "cassandra", URI: "10.0.0.1:9042", Keyspace: "chat", Label: "ks-1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, info.ID)
	assert.Equal(t, "cassandra", info.Kind)
	assert.Equal(t, "ks-1", info.Label)
	assert.Equal(t, "chat", info.Keyspace)
	assert.Equal(t, 1, tc.cassConnects)

	list := r.List()
	require.Len(t, list, 1)
	assert.Equal(t, "chat", list[0].Keyspace)
}

func TestRegistry_Connect_UnknownKind_Error(t *testing.T) {
	r, _, _ := newTestRegistry(t, time.Hour)
	_, err := r.Connect(context.Background(), connectSpec{Kind: "redis", URI: "x"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownKind)
	assert.Empty(t, r.List())
}

func TestRegistry_Connect_DriverError_NotStored(t *testing.T) {
	r, _, _ := newTestRegistry(t, time.Hour)
	r.mongoConnect = func(_ context.Context, _ string) (*mongo.Client, error) {
		return nil, errors.New("unreachable")
	}
	_, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://x", Label: "x"})
	require.Error(t, err)
	assert.Empty(t, r.List())

	// Cassandra branch as well.
	r.cassConnect = func(_ []string, _ string) (*gocql.Session, error) {
		return nil, errors.New("dial fail")
	}
	_, err = r.Connect(context.Background(), connectSpec{Kind: "cassandra", URI: "h", Keyspace: "k", Label: "x"})
	require.Error(t, err)
	assert.Empty(t, r.List())
}

func TestRegistry_Connect_URIParsing_Cassandra(t *testing.T) {
	r, _, tc := newTestRegistry(t, time.Hour)

	_, err := r.Connect(context.Background(), connectSpec{
		Kind: "cassandra", URI: "host1:9042, host2:9042 , host3:9042", Keyspace: "k", Label: "l",
	})
	require.NoError(t, err)
	require.Len(t, tc.cassConnectArg, 1)
	assert.Equal(t, []string{"host1:9042", "host2:9042", "host3:9042"}, tc.cassConnectArg[0])
}

func TestRegistry_Get_Miss(t *testing.T) {
	r, _, _ := newTestRegistry(t, time.Hour)
	_, err := r.Get("missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRegistry_Get_TouchesLastUsed(t *testing.T) {
	r, clk, _ := newTestRegistry(t, time.Hour)
	info, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://x", Label: "l"})
	require.NoError(t, err)

	r.mu.RLock()
	originalLastUsed := r.conns[info.ID].lastUsed.Load()
	r.mu.RUnlock()

	clk.Advance(5 * time.Minute)

	conn, err := r.Get(info.ID)
	require.NoError(t, err)
	require.NotNil(t, conn)

	assert.Greater(t, conn.lastUsed.Load(), originalLastUsed)
	assert.Equal(t, clk.Now().UnixMilli(), conn.lastUsed.Load())
}

func TestRegistry_Close_Success(t *testing.T) {
	r, _, tc := newTestRegistry(t, time.Hour)
	info, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://x", Label: "l"})
	require.NoError(t, err)

	require.NoError(t, r.Close(info.ID))
	assert.Empty(t, r.List())
	assert.Equal(t, 1, tc.mongoCloses)
}

func TestRegistry_Close_Cassandra_Success(t *testing.T) {
	r, clk, tc := newTestRegistry(t, time.Hour)
	id := addCassConnection(t, r, clk, "l", "k")

	require.NoError(t, r.Close(id))
	assert.Empty(t, r.List())
	assert.Equal(t, 1, tc.cassCloses)
}

func TestRegistry_Close_Miss(t *testing.T) {
	r, _, _ := newTestRegistry(t, time.Hour)
	err := r.Close("missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRegistry_CloseAll(t *testing.T) {
	r, clk, tc := newTestRegistry(t, time.Hour)

	_, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://x", Label: "a"})
	require.NoError(t, err)
	_, err = r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://y", Label: "b"})
	require.NoError(t, err)
	addCassConnection(t, r, clk, "c", "k")

	r.CloseAll()
	assert.Empty(t, r.List())
	assert.Equal(t, 2, tc.mongoCloses)
	assert.Equal(t, 1, tc.cassCloses)
}

func TestRegistry_Reap_EvictsIdle(t *testing.T) {
	r, clk, tc := newTestRegistry(t, 10*time.Minute)
	info, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://x", Label: "l"})
	require.NoError(t, err)

	clk.Advance(11 * time.Minute)
	r.Reap()

	assert.Empty(t, r.List())
	assert.Equal(t, 1, tc.mongoCloses)

	_, err = r.Get(info.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRegistry_Reap_KeepsFresh(t *testing.T) {
	r, clk, tc := newTestRegistry(t, 10*time.Minute)
	info, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://x", Label: "l"})
	require.NoError(t, err)

	clk.Advance(5 * time.Minute)
	r.Reap()

	assert.Len(t, r.List(), 1)
	assert.Equal(t, 0, tc.mongoCloses)

	_, err = r.Get(info.ID)
	require.NoError(t, err)
}

func TestRegistry_Reap_EvictsMultiple(t *testing.T) {
	r, clk, tc := newTestRegistry(t, 10*time.Minute)

	// One old mongo and one old cassandra connection at t=0.
	idleA, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://a", Label: "a"})
	require.NoError(t, err)
	idleB := addCassConnection(t, r, clk, "b", "k")

	// Advance and create a fresh connection.
	clk.Advance(11 * time.Minute)
	fresh, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://c", Label: "c"})
	require.NoError(t, err)

	r.Reap()

	list := r.List()
	require.Len(t, list, 1)
	assert.Equal(t, fresh.ID, list[0].ID)
	assert.Equal(t, 1, tc.mongoCloses)
	assert.Equal(t, 1, tc.cassCloses)

	_, err = r.Get(idleA.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = r.Get(idleB)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRegistry_Concurrent_ConnectGetClose_UnderRace(t *testing.T) {
	r, _, _ := newTestRegistry(t, time.Hour)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			info, err := r.Connect(context.Background(), connectSpec{
				Kind: "mongo", URI: "mongodb://x", Label: "l",
			})
			if err != nil {
				return
			}
			// Best-effort Get + List + Close. Errors are tolerated since
			// other goroutines may close concurrently in some scenarios.
			_, _ = r.Get(info.ID)
			_ = r.List()
			_ = r.Close(info.ID)
		}(i)
	}
	wg.Wait()
	assert.Empty(t, r.List())
}

func TestRegistry_List_SortedByCreatedAt(t *testing.T) {
	r, clk, _ := newTestRegistry(t, time.Hour)

	a, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://a", Label: "a"})
	require.NoError(t, err)
	clk.Advance(time.Minute)
	b, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://b", Label: "b"})
	require.NoError(t, err)
	clk.Advance(time.Minute)
	c, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://c", Label: "c"})
	require.NoError(t, err)

	list := r.List()
	require.Len(t, list, 3)
	assert.Equal(t, a.ID, list[0].ID)
	assert.Equal(t, b.ID, list[1].ID)
	assert.Equal(t, c.ID, list[2].ID)
	// Verify createdAt ascending.
	assert.True(t, list[0].CreatedAt.Before(list[1].CreatedAt))
	assert.True(t, list[1].CreatedAt.Before(list[2].CreatedAt))
}

func TestRunReaper_StopsOnContextCancel(t *testing.T) {
	r, _, _ := newTestRegistry(t, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runReaper(ctx, r, 10*time.Millisecond)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runReaper did not return after context cancel")
	}
}

func TestRunReaper_TickInvokesReap(t *testing.T) {
	r, clk, tc := newTestRegistry(t, 10*time.Minute)

	// Make a connection that is already stale.
	_, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://x", Label: "l"})
	require.NoError(t, err)
	clk.Advance(11 * time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runReaper(ctx, r, 5*time.Millisecond)

	require.Eventually(t, func() bool {
		tc.mu.Lock()
		defer tc.mu.Unlock()
		return tc.mongoCloses == 1
	}, time.Second, 5*time.Millisecond)
}

func TestSplitAndTrim(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"host1", []string{"host1"}},
		{" host1 , host2 ", []string{"host1", "host2"}},
		{"a,,b", []string{"a", "b"}},
		{"  ,  ,  ", []string{}},
		{"", []string{}},
	}
	for _, c := range cases {
		got := splitAndTrim(c.in)
		if len(c.want) == 0 {
			assert.Empty(t, got, "input %q", c.in)
			continue
		}
		assert.Equal(t, c.want, got, "input %q", c.in)
	}
}

func TestMongoServerVersion_NilClient(t *testing.T) {
	assert.Empty(t, mongoServerVersion(context.Background(), nil))
}

func TestCassServerVersion_NilSession(t *testing.T) {
	assert.Empty(t, cassServerVersion(nil))
}

func TestMongoServerVersion_UnreachableReturnsEmpty(t *testing.T) {
	// Dialing a closed port via our dummy client should fail buildInfo
	// within the internal 3s timeout and return "" — exercising the error
	// branch of mongoServerVersion without needing a real server.
	c := newDummyMongo(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.Disconnect(ctx)
	})
	// Short deadline prevents any SDAM retries from stretching the test.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got := mongoServerVersion(ctx, c)
	assert.Empty(t, got)
}

func TestCloseConnection_NilMongo_NoPanic(t *testing.T) {
	// Swap closeMongo to verify we never invoke it when mongo is nil.
	r := newRegistry(time.Hour)
	called := false
	r.closeMongo = func(_ context.Context, _ *mongo.Client) error {
		called = true
		return nil
	}

	c := &connection{kind: "mongo", mongo: nil, label: "nil-client"}
	assert.NotPanics(t, func() { r.closeConnection(c) })
	assert.False(t, called, "closeMongo must not be invoked when client is nil")
}

func TestCloseConnection_NilCass_NoPanic(t *testing.T) {
	// Mirror the mongo nil-check: closeCass must not be invoked when session is nil.
	r := newRegistry(time.Hour)
	called := false
	r.closeCass = func(_ *gocql.Session) {
		called = true
	}

	c := &connection{kind: "cassandra", cass: nil, label: "nil-session"}
	assert.NotPanics(t, func() { r.closeConnection(c) })
	assert.False(t, called, "closeCass must not be invoked when session is nil")
}

func TestCloseConnection_UnknownKind_NoPanic(t *testing.T) {
	// Registry callers should never construct an unknown-kind connection,
	// but defensive coverage for the switch default matters.
	r := newRegistry(time.Hour)
	assert.NotPanics(t, func() {
		r.closeConnection(&connection{kind: "redis", label: "x"})
	})
}

func TestCloseMongo_ReturnsError(t *testing.T) {
	// Exercise the branch in closeConnection where the injected closeMongo
	// returns an error (it's logged, not returned).
	r := newRegistry(time.Hour)
	r.mongoConnect = func(_ context.Context, _ string) (*mongo.Client, error) {
		return newDummyMongo(t), nil
	}
	r.closeMongo = func(_ context.Context, _ *mongo.Client) error {
		return errors.New("disconnect-failed")
	}
	info, err := r.Connect(context.Background(), connectSpec{Kind: "mongo", URI: "mongodb://127.0.0.1:1", Label: "l"})
	require.NoError(t, err)
	// Close should succeed even though closeMongo returned an error.
	require.NoError(t, r.Close(info.ID))
	assert.Empty(t, r.List())
}

func TestNewRegistry_Defaults(t *testing.T) {
	r := newRegistry(42 * time.Minute)
	assert.NotNil(t, r.conns)
	assert.Equal(t, 42*time.Minute, r.idleTimeout)
	assert.NotNil(t, r.now)
	assert.NotNil(t, r.mongoConnect)
	assert.NotNil(t, r.cassConnect)
	assert.NotNil(t, r.closeMongo)
	assert.NotNil(t, r.closeCass)
}

func TestDefaultMongoConnect_Unreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// 127.0.0.1:1 is the standard "definitely nothing listening" target.
	_, err := defaultMongoConnect(ctx, "mongodb://127.0.0.1:1/?connectTimeoutMS=500&serverSelectionTimeoutMS=500")
	require.Error(t, err)
}
