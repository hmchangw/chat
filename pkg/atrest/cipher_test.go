package atrest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staticKEKLoader is a non-reloading KEKLoader for unit tests.
type staticKEKLoader struct {
	keys    map[int][]byte
	current int
}

func (s *staticKEKLoader) Current() (int, []byte) { return s.current, s.keys[s.current] }
func (s *staticKEKLoader) ByVersion(v int) ([]byte, bool) {
	k, ok := s.keys[v]
	return k, ok
}
func (s *staticKEKLoader) Close() error { return nil }

func newTestCipher(t *testing.T, store DEKStore) *cipherImpl {
	t.Helper()
	loader := &staticKEKLoader{
		keys:    map[int][]byte{1: bytes32('a'), 2: bytes32('b')},
		current: 2,
	}
	return newCipher(loader, store, newDEKCache(100, time.Hour)).(*cipherImpl)
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestCipher_RoundTrip(t *testing.T) {
	store := newFakeDEKStore()
	c := newTestCipher(t, store)
	ctx := context.Background()

	in := EncryptedFields{Msg: "hello world", SysMsgData: []byte{1, 2, 3}}
	payload, meta, err := c.Encrypt(ctx, "room1", in)
	require.NoError(t, err)
	assert.NotEmpty(t, payload)
	assert.Len(t, meta.Nonce, 12)

	out, err := c.Decrypt(ctx, "room1", payload, meta)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestCipher_LazyDEKCreation(t *testing.T) {
	store := newFakeDEKStore()
	c := newTestCipher(t, store)
	ctx := context.Background()

	row, err := store.Get(ctx, "room1")
	require.NoError(t, err)
	assert.Nil(t, row)

	_, _, err = c.Encrypt(ctx, "room1", EncryptedFields{Msg: "first"})
	require.NoError(t, err)

	row, err = store.Get(ctx, "room1")
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 2, row.KEKVersion) // current
	assert.Len(t, row.WrapNonce, 12)
}

func TestCipher_TamperedCiphertextRejected(t *testing.T) {
	store := newFakeDEKStore()
	c := newTestCipher(t, store)
	ctx := context.Background()

	payload, meta, err := c.Encrypt(ctx, "room1", EncryptedFields{Msg: "hello"})
	require.NoError(t, err)

	payload[0] ^= 0xFF
	_, err = c.Decrypt(ctx, "room1", payload, meta)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAuthFailed))
}

func TestCipher_KEKVersionUnknown(t *testing.T) {
	store := newFakeDEKStore()
	c := newTestCipher(t, store)
	ctx := context.Background()

	// Hand-craft a DEK row wrapped under a non-existent KEK version.
	require.NoError(t, store.Upsert(ctx, RoomDataKey{
		ID:         "room1",
		WrappedDEK: []byte("garbage"),
		WrapNonce:  make([]byte, 12),
		KEKVersion: 999,
	}))

	_, _, err := c.Encrypt(ctx, "room1", EncryptedFields{Msg: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKVersionUnknown))
}

func TestCipher_CacheHitAvoidsStore(t *testing.T) {
	store := newFakeDEKStore()
	c := newTestCipher(t, store)
	ctx := context.Background()

	_, _, err := c.Encrypt(ctx, "room1", EncryptedFields{Msg: "first"})
	require.NoError(t, err)

	// Replace store with a sentinel that fails any access.
	c.store = failingStore{}

	_, _, err = c.Encrypt(ctx, "room1", EncryptedFields{Msg: "second"})
	assert.NoError(t, err) // hit cache, no store call
}

type failingStore struct{}

func (failingStore) Get(context.Context, string) (*RoomDataKey, error) {
	return nil, errors.New("store should not be called")
}
func (failingStore) Upsert(context.Context, RoomDataKey) error  { return errors.New("nope") }
func (failingStore) Replace(context.Context, RoomDataKey) error { return errors.New("nope") }
