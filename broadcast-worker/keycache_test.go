package main

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// fakeKeyProvider is a minimal RoomKeyProvider that records calls and
// returns preconfigured keys. Tests assert call counts to confirm cache
// hits do not reach the inner provider.
type fakeKeyProvider struct {
	mu       sync.Mutex
	calls    []string
	byRoomID map[string]*roomkeystore.VersionedKeyPair
	err      error
}

func newFakeKeyProvider() *fakeKeyProvider {
	return &fakeKeyProvider{byRoomID: make(map[string]*roomkeystore.VersionedKeyPair)}
}

func (f *fakeKeyProvider) Get(_ context.Context, roomID string) (*roomkeystore.VersionedKeyPair, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, roomID)
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.byRoomID[roomID]; ok {
		return v, nil
	}
	return nil, nil
}

func (f *fakeKeyProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeKeyProvider) set(roomID string, v *roomkeystore.VersionedKeyPair) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byRoomID[roomID] = v
}

func keyVal(version int) *roomkeystore.VersionedKeyPair {
	return &roomkeystore.VersionedKeyPair{
		Version: version,
		KeyPair: roomkeystore.RoomKeyPair{
			PublicKey:  []byte{byte(version)},
			PrivateKey: []byte{byte(version)},
		},
	}
}

// Compile-time check: CachedRoomKeyProvider satisfies the interface the
// handler depends on.
var _ RoomKeyProvider = (*CachedRoomKeyProvider)(nil)

func TestNewCachedRoomKeyProvider_ConstructsEmpty(t *testing.T) {
	inner := newFakeKeyProvider()
	c := NewCachedRoomKeyProvider(inner, 10, time.Minute)
	require.NotNil(t, c)
	assert.Equal(t, 0, inner.callCount())
}

func TestCachedRoomKeyProvider_MissCallsInner(t *testing.T) {
	inner := newFakeKeyProvider()
	inner.set("room-1", keyVal(7))
	c := NewCachedRoomKeyProvider(inner, 10, time.Minute)

	got, err := c.Get(context.Background(), "room-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 7, got.Version)
	assert.Equal(t, 1, inner.callCount())
}

func TestCachedRoomKeyProvider_HitServedFromCache(t *testing.T) {
	inner := newFakeKeyProvider()
	inner.set("room-1", keyVal(7))
	c := NewCachedRoomKeyProvider(inner, 10, time.Minute)

	_, _ = c.Get(context.Background(), "room-1") // prime
	got, err := c.Get(context.Background(), "room-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 7, got.Version)
	assert.Equal(t, 1, inner.callCount(), "hit should not call inner")
}

func TestCachedRoomKeyProvider_NilResultNotCached(t *testing.T) {
	// Inner returns (nil, nil) for an absent key. Caching that would make
	// the cache mask a later key creation for the remainder of the TTL.
	inner := newFakeKeyProvider()
	c := NewCachedRoomKeyProvider(inner, 10, time.Minute)

	got, err := c.Get(context.Background(), "room-1")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Key appears later — second call must reach inner.
	inner.set("room-1", keyVal(1))
	got2, err := c.Get(context.Background(), "room-1")
	require.NoError(t, err)
	require.NotNil(t, got2)
	assert.Equal(t, 2, inner.callCount(), "absent-key result must not be cached")
}

func TestCachedRoomKeyProvider_InnerErrorPropagatedNotCached(t *testing.T) {
	innerErr := errors.New("valkey down")
	inner := newFakeKeyProvider()
	inner.err = innerErr
	c := NewCachedRoomKeyProvider(inner, 10, time.Minute)

	_, err := c.Get(context.Background(), "room-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, innerErr)

	// Recovery: inner starts succeeding. Cache must not have stored the error.
	inner.err = nil
	inner.set("room-1", keyVal(3))
	got, err := c.Get(context.Background(), "room-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 3, got.Version)
}

func TestCachedRoomKeyProvider_TTLExpiredReFetches(t *testing.T) {
	inner := newFakeKeyProvider()
	inner.set("room-1", keyVal(1))
	c := NewCachedRoomKeyProvider(inner, 10, time.Second)

	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return base }

	_, err := c.Get(context.Background(), "room-1")
	require.NoError(t, err)
	assert.Equal(t, 1, inner.callCount())

	// Advance past TTL; cached entry should be treated as a miss.
	c.now = func() time.Time { return base.Add(2 * time.Second) }
	inner.set("room-1", keyVal(2)) // rotated meanwhile

	got, err := c.Get(context.Background(), "room-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 2, got.Version, "stale entry should re-fetch the current key")
	assert.Equal(t, 2, inner.callCount())
}

func TestCachedRoomKeyProvider_LRUEvictionOnOverflow(t *testing.T) {
	inner := newFakeKeyProvider()
	inner.set("a", keyVal(1))
	inner.set("b", keyVal(2))
	inner.set("c", keyVal(3))
	c := NewCachedRoomKeyProvider(inner, 2, time.Minute)

	ctx := context.Background()
	_, _ = c.Get(ctx, "a")
	_, _ = c.Get(ctx, "b")
	_, _ = c.Get(ctx, "c") // evicts a
	_, _ = c.Get(ctx, "a") // miss again

	assert.Equal(t, 4, inner.callCount())
}

func TestCachedRoomKeyProvider_AccessPromotesToMRU(t *testing.T) {
	inner := newFakeKeyProvider()
	inner.set("a", keyVal(1))
	inner.set("b", keyVal(2))
	inner.set("c", keyVal(3))
	c := NewCachedRoomKeyProvider(inner, 2, time.Minute)

	ctx := context.Background()
	_, _ = c.Get(ctx, "a")
	_, _ = c.Get(ctx, "b")
	_, _ = c.Get(ctx, "a") // touch a → b is now LRU
	_, _ = c.Get(ctx, "c") // evicts b

	before := inner.callCount()
	_, _ = c.Get(ctx, "a")
	assert.Equal(t, before, inner.callCount(), "a should still be cached")
	_, _ = c.Get(ctx, "b")
	assert.Equal(t, before+1, inner.callCount(), "b should have been evicted")
}

func TestCachedRoomKeyProvider_ConcurrentSafe(t *testing.T) {
	const (
		goroutines = 32
		iterations = 200
		rooms      = 16
	)
	inner := newFakeKeyProvider()
	for i := 0; i < rooms; i++ {
		inner.set("room-"+strconv.Itoa(i), keyVal(i+1))
	}
	c := NewCachedRoomKeyProvider(inner, rooms, time.Minute)

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				idx := (seed*iterations + i) % rooms
				_, err := c.Get(ctx, "room-"+strconv.Itoa(idx))
				require.NoError(t, err)
			}
		}(g)
	}
	wg.Wait()
}
