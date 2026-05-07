package atrest

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staticKeyWrapper is a deterministic in-memory KeyWrapper for unit
// tests. It uses AES-256-GCM under a fixed key to wrap/unwrap, with a
// single random nonce per Wrap call.
type staticKeyWrapper struct {
	aead cipher.AEAD
	rng  io.Reader
}

func newStaticKeyWrapper(t *testing.T) *staticKeyWrapper {
	t.Helper()
	key := bytes32('k')
	block, err := aes.NewCipher(key)
	require.NoError(t, err)
	g, err := cipher.NewGCM(block)
	require.NoError(t, err)
	return &staticKeyWrapper{aead: g, rng: deterministicRand{seed: 'r'}}
}

func (w *staticKeyWrapper) Wrap(_ context.Context, dek []byte) ([]byte, error) {
	nonce := make([]byte, w.aead.NonceSize())
	if _, err := io.ReadFull(w.rng, nonce); err != nil {
		return nil, err
	}
	ct := w.aead.Seal(nonce, nonce, dek, nil)
	return ct, nil
}

func (w *staticKeyWrapper) Unwrap(_ context.Context, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < w.aead.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce := ciphertext[:w.aead.NonceSize()]
	ct := ciphertext[w.aead.NonceSize():]
	return w.aead.Open(nil, nonce, ct, nil)
}

// deterministicRand returns the same byte over and over so the static
// wrapper is reproducible in tests; the real Vault wrapper uses Vault's
// own randomness.
type deterministicRand struct{ seed byte }

func (d deterministicRand) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = d.seed
	}
	return len(p), nil
}

func newTestCipher(t *testing.T, store DEKStore) *cipherImpl {
	t.Helper()
	wrapper := newStaticKeyWrapper(t)
	return newCipher(wrapper, store, newDEKCache(100, time.Hour)).(*cipherImpl)
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
	assert.NotEmpty(t, row.WrappedDEK)
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

// failingWrapper makes Unwrap fail; used to verify error propagation
// when a stored row's WrappedDEK can't be unwrapped.
type failingWrapper struct{ err error }

func (f failingWrapper) Wrap(context.Context, []byte) ([]byte, error)   { return nil, f.err }
func (f failingWrapper) Unwrap(context.Context, []byte) ([]byte, error) { return nil, f.err }

func TestCipher_UnwrapFails(t *testing.T) {
	store := newFakeDEKStore()
	// Pre-seed a row whose ciphertext can't be unwrapped under the test
	// wrapper (any non-empty bytes; failingWrapper rejects all).
	require.NoError(t, store.Upsert(context.Background(), RoomDataKey{
		ID:         "room1",
		WrappedDEK: []byte("not-a-real-vault-blob"),
	}))
	c := newCipher(failingWrapper{err: errors.New("vault down")}, store, newDEKCache(100, time.Hour)).(*cipherImpl)

	_, _, err := c.Encrypt(context.Background(), "room1", EncryptedFields{Msg: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault down")
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

// errReader returns the supplied error on every Read.
type errReader struct{ err error }

func (e errReader) Read(_ []byte) (int, error) { return 0, e.err }

// upsertFailStore makes Upsert fail; used to exercise the early-return
// path in createDEK.
type upsertFailStore struct {
	upsertErr error
}

func (s upsertFailStore) Get(context.Context, string) (*RoomDataKey, error) {
	return nil, nil
}
func (s upsertFailStore) Upsert(context.Context, RoomDataKey) error  { return s.upsertErr }
func (s upsertFailStore) Replace(context.Context, RoomDataKey) error { return nil }

func TestCipher_CreateDEK_UpsertFails(t *testing.T) {
	store := upsertFailStore{upsertErr: errors.New("mongo down")}
	c := newTestCipher(t, store)
	_, _, err := c.Encrypt(context.Background(), "room1", EncryptedFields{Msg: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mongo down")
}

// upsertOKThenGetNilStore: Upsert succeeds but the post-Upsert Get
// returns (nil, nil). createDEK treats this as a synthetic "row missing
// after upsert" error.
type upsertOKThenGetNilStore struct{}

func (upsertOKThenGetNilStore) Get(context.Context, string) (*RoomDataKey, error) {
	return nil, nil
}
func (upsertOKThenGetNilStore) Upsert(context.Context, RoomDataKey) error  { return nil }
func (upsertOKThenGetNilStore) Replace(context.Context, RoomDataKey) error { return nil }

func TestCipher_CreateDEK_PostUpsertGetReturnsNil(t *testing.T) {
	c := newTestCipher(t, upsertOKThenGetNilStore{})
	_, _, err := c.Encrypt(context.Background(), "room1", EncryptedFields{Msg: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing after upsert")
}

// TestCipher_CreateDEK_RandReaderFails verifies that DEK generation
// failures inside createDEK propagate cleanly.
func TestCipher_CreateDEK_RandReaderFails(t *testing.T) {
	store := newFakeDEKStore()
	c := newTestCipher(t, store)
	c.randReader = errReader{err: errors.New("rng dead")}
	_, _, err := c.Encrypt(context.Background(), "room1", EncryptedFields{Msg: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generate DEK")
}

// staticKeyWrapper Wrap/Unwrap symmetry self-check (independent of
// Cipher) — guards against accidental regressions in the test fake.
func TestStaticKeyWrapper_RoundTrip(t *testing.T) {
	w := newStaticKeyWrapper(t)
	dek := bytes32('d')
	ct, err := w.Wrap(context.Background(), dek)
	require.NoError(t, err)
	got, err := w.Unwrap(context.Background(), ct)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(dek, got))
}
