package atrest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDEKStore is an in-memory DEKStore used to drive Cipher unit tests
// and to verify the contract assertions below before the Mongo impl exists.
type fakeDEKStore struct {
	mu   sync.Mutex
	data map[string]RoomDataKey
}

func newFakeDEKStore() *fakeDEKStore {
	return &fakeDEKStore{data: map[string]RoomDataKey{}}
}

func (f *fakeDEKStore) Get(_ context.Context, roomID string) (*RoomDataKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.data[roomID]
	if !ok {
		return nil, nil
	}
	cp := k
	return &cp, nil
}

func (f *fakeDEKStore) Upsert(_ context.Context, key RoomDataKey) error { //nolint:gocritic // hugeParam: key is passed by value to satisfy the DEKStore interface
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.data[key.ID]; ok {
		return nil // $setOnInsert: existing row wins
	}
	f.data[key.ID] = key
	return nil
}

func (f *fakeDEKStore) Replace(_ context.Context, key RoomDataKey) error { //nolint:gocritic // hugeParam: key is passed by value to satisfy the DEKStore interface
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key.ID] = key
	return nil
}

func TestFakeDEKStore_RoundTrip(t *testing.T) {
	s := newFakeDEKStore()
	ctx := context.Background()

	got, err := s.Get(ctx, "room1")
	require.NoError(t, err)
	assert.Nil(t, got)

	row := RoomDataKey{ID: "room1", WrappedDEK: []byte("wrapped"), WrapNonce: []byte("nonce"), KEKVersion: 1, CreatedAt: time.Now()}
	require.NoError(t, s.Upsert(ctx, row))

	got, err = s.Get(ctx, "room1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, row.WrappedDEK, got.WrappedDEK)

	// Upsert with same _id is a no-op.
	require.NoError(t, s.Upsert(ctx, RoomDataKey{ID: "room1", WrappedDEK: []byte("other"), KEKVersion: 9}))
	got, _ = s.Get(ctx, "room1")
	assert.Equal(t, []byte("wrapped"), got.WrappedDEK)

	// Replace overwrites.
	require.NoError(t, s.Replace(ctx, RoomDataKey{ID: "room1", WrappedDEK: []byte("re"), KEKVersion: 2}))
	got, _ = s.Get(ctx, "room1")
	assert.Equal(t, []byte("re"), got.WrappedDEK)
	assert.Equal(t, 2, got.KEKVersion)
}
