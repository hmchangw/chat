package main

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// fakeKeyStore is a minimal RoomKeyProvider that records call counts and
// returns preconfigured per-room key pairs (or errors).
type fakeKeyStore struct {
	mu      sync.Mutex
	calls   map[string]int
	byRoom  map[string]*roomkeystore.VersionedKeyPair
	err     error
	block   chan struct{} // when non-nil, Get blocks on it before returning
	entered chan struct{} // signaled (non-blocking) on every Get entry
}

func newFakeKeyStore() *fakeKeyStore {
	return &fakeKeyStore{
		calls:  make(map[string]int),
		byRoom: make(map[string]*roomkeystore.VersionedKeyPair),
	}
}

func (f *fakeKeyStore) set(roomID string, key *roomkeystore.VersionedKeyPair) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byRoom[roomID] = key
}

func (f *fakeKeyStore) callCount(roomID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[roomID]
}

func (f *fakeKeyStore) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.calls {
		n += v
	}
	return n
}

func (f *fakeKeyStore) Get(_ context.Context, roomID string) (*roomkeystore.VersionedKeyPair, error) {
	f.mu.Lock()
	f.calls[roomID]++
	block := f.block
	entered := f.entered
	err := f.err
	key := f.byRoom[roomID]
	f.mu.Unlock()

	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		<-block
	}
	if err != nil {
		return nil, err
	}
	return key, nil
}

// makeKey returns a deterministic VersionedKeyPair for tests.
func makeKey(version int) *roomkeystore.VersionedKeyPair {
	return &roomkeystore.VersionedKeyPair{
		Version: version,
		KeyPair: roomkeystore.RoomKeyPair{
			PublicKey:  []byte("public-" + strconv.Itoa(version)),
			PrivateKey: []byte("private-" + strconv.Itoa(version)),
		},
	}
}

var _ RoomKeyProvider = (*CachedKeyProvider)(nil)

func TestCachedKeyProvider_MissPopulatesCacheAndReturnsValue(t *testing.T) {
	inner := newFakeKeyStore()
	want := makeKey(1)
	inner.set("room1", want)

	c := NewCachedKeyProvider(inner, time.Minute)

	got, err := c.Get(context.Background(), "room1")
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 1, inner.callCount("room1"))
}

func TestCachedKeyProvider_HitDoesNotCallInner(t *testing.T) {
	inner := newFakeKeyStore()
	inner.set("room1", makeKey(1))

	c := NewCachedKeyProvider(inner, time.Minute)

	// First call populates.
	_, err := c.Get(context.Background(), "room1")
	require.NoError(t, err)

	// Subsequent calls within TTL must not touch inner.
	for i := 0; i < 10; i++ {
		got, err := c.Get(context.Background(), "room1")
		require.NoError(t, err)
		assert.Equal(t, makeKey(1), got)
	}
	assert.Equal(t, 1, inner.callCount("room1"))
}

func TestCachedKeyProvider_ExpiredEntryRefetches(t *testing.T) {
	inner := newFakeKeyStore()
	inner.set("room1", makeKey(1))

	c := NewCachedKeyProvider(inner, 100*time.Millisecond)
	// Use a controllable clock so the test doesn't actually sleep.
	clock := time.Unix(0, 0)
	c.now = func() time.Time { return clock }

	_, err := c.Get(context.Background(), "room1")
	require.NoError(t, err)
	assert.Equal(t, 1, inner.callCount("room1"))

	// Within TTL: still cached.
	clock = clock.Add(99 * time.Millisecond)
	_, err = c.Get(context.Background(), "room1")
	require.NoError(t, err)
	assert.Equal(t, 1, inner.callCount("room1"))

	// Past TTL: refetched. Inner now returns a new version to verify we
	// actually went through to inner rather than serving the stale value.
	clock = clock.Add(time.Second)
	inner.set("room1", makeKey(2))

	got, err := c.Get(context.Background(), "room1")
	require.NoError(t, err)
	assert.Equal(t, makeKey(2), got)
	assert.Equal(t, 2, inner.callCount("room1"))
}

func TestCachedKeyProvider_ErrorPassesThroughAndIsNotCached(t *testing.T) {
	inner := newFakeKeyStore()
	wantErr := errors.New("valkey down")
	inner.err = wantErr

	c := NewCachedKeyProvider(inner, time.Minute)

	_, err := c.Get(context.Background(), "room1")
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, 1, inner.callCount("room1"))

	// Clear the error and ensure the next call retries (i.e. error was not
	// cached). The new value must be returned and inner is called again.
	inner.mu.Lock()
	inner.err = nil
	inner.mu.Unlock()
	inner.set("room1", makeKey(1))

	got, err := c.Get(context.Background(), "room1")
	require.NoError(t, err)
	assert.Equal(t, makeKey(1), got)
	assert.Equal(t, 2, inner.callCount("room1"))
}

func TestCachedKeyProvider_NilResultIsNotCached(t *testing.T) {
	// When the inner store reports no key (nil, nil) for an unprovisioned
	// room, we must NOT cache that absence — otherwise a newly-provisioned
	// key wouldn't be picked up for up to TTL seconds, and broadcasts
	// would keep failing during room setup.
	inner := newFakeKeyStore()
	// Intentionally leave inner.byRoom["room1"] unset → Get returns nil.

	c := NewCachedKeyProvider(inner, time.Minute)

	got, err := c.Get(context.Background(), "room1")
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Equal(t, 1, inner.callCount("room1"))

	// Provision the key; the next call must reach inner and return it.
	inner.set("room1", makeKey(1))

	got, err = c.Get(context.Background(), "room1")
	require.NoError(t, err)
	assert.Equal(t, makeKey(1), got)
	assert.Equal(t, 2, inner.callCount("room1"))
}

func TestCachedKeyProvider_SingleflightCoalescesConcurrentMissesOnSameRoom(t *testing.T) {
	inner := newFakeKeyStore()
	inner.set("room1", makeKey(1))
	// Block inside inner.Get so many callers pile up at the singleflight
	// gate before any of them returns.
	inner.block = make(chan struct{})
	inner.entered = make(chan struct{}, 1)

	c := NewCachedKeyProvider(inner, time.Minute)

	const N = 100
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*roomkeystore.VersionedKeyPair, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = c.Get(context.Background(), "room1")
		}(i)
	}
	close(start)

	// Wait for the first inner call to be in-flight, then release it.
	<-inner.entered
	close(inner.block)
	wg.Wait()

	for i := 0; i < N; i++ {
		require.NoError(t, errs[i], "goroutine %d", i)
		assert.Equal(t, makeKey(1), results[i], "goroutine %d", i)
	}
	// Singleflight + post-flight cache population guarantee exactly one
	// inner call: callers that enter while the flight is active join it;
	// callers that arrive after it finishes hit the populated cache.
	assert.Equal(t, 1, inner.callCount("room1"))
}

func TestCachedKeyProvider_DifferentRoomsAreNotCoalesced(t *testing.T) {
	inner := newFakeKeyStore()
	const N = 20
	for i := 0; i < N; i++ {
		inner.set("room"+strconv.Itoa(i), makeKey(i))
	}

	c := NewCachedKeyProvider(inner, time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := c.Get(context.Background(), "room"+strconv.Itoa(i))
			assert.NoError(t, err)
			assert.Equal(t, makeKey(i), got)
		}(i)
	}
	wg.Wait()

	// Each room should have been fetched exactly once.
	for i := 0; i < N; i++ {
		assert.Equal(t, 1, inner.callCount("room"+strconv.Itoa(i)), "room %d", i)
	}
	assert.Equal(t, N, inner.totalCalls())
}

func TestCachedKeyProvider_NewVersionAfterExpiryIsReturned(t *testing.T) {
	// Models the key-rotation scenario: after rotation, inner returns a
	// new version. Once the cached entry expires, that new version must
	// be picked up.
	inner := newFakeKeyStore()
	inner.set("room1", makeKey(1))

	c := NewCachedKeyProvider(inner, 10*time.Millisecond)
	clock := time.Unix(0, 0)
	c.now = func() time.Time { return clock }

	got, err := c.Get(context.Background(), "room1")
	require.NoError(t, err)
	assert.Equal(t, 1, got.Version)

	// Simulate rotation: inner now serves a newer version.
	inner.set("room1", makeKey(2))

	// Within TTL we keep encrypting with the old version (intentional —
	// this is the correctness contract we documented).
	got, err = c.Get(context.Background(), "room1")
	require.NoError(t, err)
	assert.Equal(t, 1, got.Version)

	// Past TTL we pick up the new version.
	clock = clock.Add(time.Second)
	got, err = c.Get(context.Background(), "room1")
	require.NoError(t, err)
	assert.Equal(t, 2, got.Version)
}

func TestCachedKeyProvider_HighConcurrencyNoRace(t *testing.T) {
	// Run with -race to catch data races. Many goroutines hammering both
	// the same and different rooms, with occasional expirations forcing
	// re-fetches.
	inner := newFakeKeyStore()
	const Rooms = 8
	for i := 0; i < Rooms; i++ {
		inner.set("room"+strconv.Itoa(i), makeKey(i))
	}

	c := NewCachedKeyProvider(inner, 5*time.Millisecond)

	const Workers = 32
	const Iter = 200
	var ops atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < Workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < Iter; i++ {
				_, err := c.Get(context.Background(), "room"+strconv.Itoa((w+i)%Rooms))
				if err != nil {
					t.Errorf("Get failed: %v", err)
					return
				}
				ops.Add(1)
			}
		}(w)
	}
	wg.Wait()
	assert.Equal(t, int64(Workers*Iter), ops.Load())
}
