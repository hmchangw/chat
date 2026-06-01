package atrest

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"io"
	"sync"
	"sync/atomic"
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

// GenerateDataKey mints a 32-byte DEK from the wrapper's deterministic
// RNG and wraps it locally — modelling the Vault transit datakey path,
// where the DEK material originates inside the KEK provider rather
// than in the calling process.
func (w *staticKeyWrapper) GenerateDataKey(ctx context.Context) ([]byte, []byte, error) {
	dek := make([]byte, 32)
	if _, err := io.ReadFull(w.rng, dek); err != nil {
		return nil, nil, err
	}
	wrapped, err := w.Wrap(ctx, dek)
	if err != nil {
		return nil, nil, err
	}
	return dek, wrapped, nil
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

	in := EncryptedFields{Msg: "hello world", Attachments: [][]byte{{1, 2, 3}}}
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

func (f failingWrapper) GenerateDataKey(context.Context) ([]byte, []byte, error) {
	return nil, nil, f.err
}
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

// TestCipher_CreateDEK_GenerateDataKeyFails verifies that a failed
// KeyWrapper.GenerateDataKey call propagates as a "generate DEK" error,
// not a silent zero DEK.
func TestCipher_CreateDEK_GenerateDataKeyFails(t *testing.T) {
	store := newFakeDEKStore()
	c := newCipher(failingWrapper{err: errors.New("vault down")}, store, newDEKCache(100, time.Hour)).(*cipherImpl)
	_, _, err := c.Encrypt(context.Background(), "room1", EncryptedFields{Msg: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generate DEK")
}

// TestCipher_CreateDEK_ShortDEK verifies the post-GenerateDataKey length
// check: a KeyWrapper that returns a non-32-byte DEK must fail loudly
// rather than producing an AES key of the wrong size.
func TestCipher_CreateDEK_ShortDEK(t *testing.T) {
	store := newFakeDEKStore()
	c := newCipher(shortDEKWrapper{}, store, newDEKCache(100, time.Hour)).(*cipherImpl)
	_, _, err := c.Encrypt(context.Background(), "room1", EncryptedFields{Msg: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected 32 bytes")
}

// shortDEKWrapper returns a too-short DEK from GenerateDataKey to drive
// the length check in createDEK.
type shortDEKWrapper struct{}

func (shortDEKWrapper) GenerateDataKey(context.Context) ([]byte, []byte, error) {
	return make([]byte, 16), []byte("wrapped-blob"), nil
}
func (shortDEKWrapper) Wrap(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("unused")
}
func (shortDEKWrapper) Unwrap(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("unused")
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

// blockingWrapper wraps a real KeyWrapper but blocks every Wrap/Unwrap
// call on `release` until it's closed, and counts how many calls it
// observed. Used to drive cache-miss stampedes for singleflight tests.
type blockingWrapper struct {
	inner   KeyWrapper
	release chan struct{}
	mints   atomic.Int32
	unwraps atomic.Int32
}

func newBlockingWrapper(inner KeyWrapper) *blockingWrapper {
	return &blockingWrapper{inner: inner, release: make(chan struct{})}
}

// mints counts both Wrap and GenerateDataKey — every operation that
// produces a fresh wrapped DEK. After the move to Vault's datakey
// endpoint, first-creation goes through GenerateDataKey; Wrap survives
// for KEK rotation re-wraps. Tests assert on this single counter so
// they don't care which path the cipher took.
func (b *blockingWrapper) GenerateDataKey(ctx context.Context) ([]byte, []byte, error) {
	b.mints.Add(1)
	<-b.release
	return b.inner.GenerateDataKey(ctx)
}

func (b *blockingWrapper) Wrap(ctx context.Context, dek []byte) ([]byte, error) {
	b.mints.Add(1)
	<-b.release
	return b.inner.Wrap(ctx, dek)
}

func (b *blockingWrapper) Unwrap(ctx context.Context, ct []byte) ([]byte, error) {
	b.unwraps.Add(1)
	<-b.release
	return b.inner.Unwrap(ctx, ct)
}

// TestCipher_Singleflight_CoalescesConcurrentMisses verifies that N
// concurrent cache-miss callers for the same roomID result in exactly
// one underlying KeyWrapper call (either Wrap on first-ever-creation or
// Unwrap on a pre-seeded row), not N. Without singleflight every caller
// would race to Vault.
func TestCipher_Singleflight_CoalescesConcurrentMisses(t *testing.T) {
	store := newFakeDEKStore()
	bw := newBlockingWrapper(newStaticKeyWrapper(t))
	c := newCipher(bw, store, newDEKCache(100, time.Hour)).(*cipherImpl)

	const N = 32
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := c.Encrypt(context.Background(), "room-stampede", EncryptedFields{Msg: "x"})
			errs[i] = err
		}(i)
	}

	// Give all goroutines time to reach the blockingWrapper and pile up
	// on the singleflight slot. 100ms is a comfortable upper bound for
	// 32 goroutines to start on any reasonable runner.
	time.Sleep(100 * time.Millisecond)
	close(bw.release)
	wg.Wait()

	for i, e := range errs {
		require.NoErrorf(t, e, "goroutine %d encrypt failed", i)
	}
	// Exactly one Wrap call happened (the leader doing the lazy DEK
	// creation); no Unwrap (this room had no pre-seeded row).
	assert.Equal(t, int32(1), bw.mints.Load(), "singleflight should collapse %d goroutines to 1 Wrap", N)
	assert.Equal(t, int32(0), bw.unwraps.Load())
}

// TestCipher_Singleflight_PostMissCacheHitsFree confirms that once the
// singleflight leader populates the cache, subsequent reads hit it
// without entering the wrapper at all.
func TestCipher_Singleflight_PostMissCacheHitsFree(t *testing.T) {
	store := newFakeDEKStore()
	bw := newBlockingWrapper(newStaticKeyWrapper(t))
	close(bw.release) // non-blocking from the start
	c := newCipher(bw, store, newDEKCache(100, time.Hour)).(*cipherImpl)

	// Warm the cache.
	_, _, err := c.Encrypt(context.Background(), "warm-room", EncryptedFields{Msg: "first"})
	require.NoError(t, err)
	require.Equal(t, int32(1), bw.mints.Load())

	// Subsequent encrypts hit cache; wraps count must not grow.
	for i := 0; i < 10; i++ {
		_, _, err := c.Encrypt(context.Background(), "warm-room", EncryptedFields{Msg: "again"})
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), bw.mints.Load(), "cache hits must not call the wrapper")
}

// ctxAwareWrapper blocks until release is closed OR the caller's ctx is
// cancelled — modelling a real Vault client where Wrap propagates ctx
// cancellation. Used to verify the singleflight detaches the leader's
// cancellation from the in-flight fetch.
type ctxAwareWrapper struct {
	inner   KeyWrapper
	release chan struct{}
	mints   atomic.Int32
}

func newCtxAwareWrapper(inner KeyWrapper) *ctxAwareWrapper {
	return &ctxAwareWrapper{inner: inner, release: make(chan struct{})}
}

func (w *ctxAwareWrapper) GenerateDataKey(ctx context.Context) ([]byte, []byte, error) {
	w.mints.Add(1)
	select {
	case <-w.release:
		return w.inner.GenerateDataKey(ctx)
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func (w *ctxAwareWrapper) Wrap(ctx context.Context, dek []byte) ([]byte, error) {
	w.mints.Add(1)
	select {
	case <-w.release:
		return w.inner.Wrap(ctx, dek)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *ctxAwareWrapper) Unwrap(ctx context.Context, ct []byte) ([]byte, error) {
	select {
	case <-w.release:
		return w.inner.Unwrap(ctx, ct)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestCipher_Singleflight_LeaderCancelDoesNotPoisonWaiters verifies that
// when the singleflight leader's context is cancelled mid-fetch, coalesced
// waiters whose own contexts are still alive still receive the DEK
// successfully. Without context.WithoutCancel inside sf.Do, the leader's
// cancellation would propagate into the in-flight Wrap, returning
// Canceled, and every coalesced waiter would inherit it.
func TestCipher_Singleflight_LeaderCancelDoesNotPoisonWaiters(t *testing.T) {
	store := newFakeDEKStore()
	bw := newCtxAwareWrapper(newStaticKeyWrapper(t))
	c := newCipher(bw, store, newDEKCache(100, time.Hour)).(*cipherImpl)

	// Start the leader first so it grabs the singleflight slot.
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()

	var leaderErr error
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _, leaderErr = c.Encrypt(leaderCtx, "room-cancel", EncryptedFields{Msg: "x"})
	}()

	// Wait until the leader is inside the wrapper (singleflight slot occupied).
	require.Eventually(t, func() bool { return bw.mints.Load() >= 1 },
		2*time.Second, time.Millisecond, "leader must reach the wrapper")

	// Now start waiters; they'll coalesce on the same singleflight slot.
	const N = 8
	waiterErrs := make([]error, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, waiterErrs[i] = c.Encrypt(context.Background(), "room-cancel", EncryptedFields{Msg: "x"})
		}(i)
	}

	// Give waiters time to pile up on sf.Do.
	time.Sleep(50 * time.Millisecond)
	// Cancel the leader BEFORE releasing the wrapper. The leader's ctx is
	// now dead but its sf.Do closure runs against a detached bgCtx, so
	// the Wrap call must NOT see Canceled — it stays blocked on release.
	cancelLeader()
	time.Sleep(20 * time.Millisecond)
	close(bw.release)

	wg.Wait()
	<-leaderDone

	// Every waiter must have succeeded: the leader's cancellation must
	// not have propagated into the shared singleflight result.
	for i, err := range waiterErrs {
		require.NoErrorf(t, err, "waiter %d inherited leader's cancellation (got %v)", i, err)
	}
	// Sanity: the leader itself also succeeds — its Encrypt doesn't check
	// ctx after dekFor, and dekFor is now insulated from cancellation.
	require.NoError(t, leaderErr, "leader Encrypt should succeed once Wrap completes")
	// Exactly one wrapper call across all N+1 goroutines.
	assert.Equal(t, int32(1), bw.mints.Load(), "singleflight must coalesce to a single Wrap")
}
