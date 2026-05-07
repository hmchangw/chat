//go:build integration

package atrest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/testutil"
)

// writeKEKFile writes a single-version (v1) KEK file in dir and returns its path.
// Both base64 values decode to exactly 32 ASCII bytes.
func writeKEKFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "keks.json")
	body, err := json.Marshal(map[string]any{
		"current": 1,
		"keys": map[string]string{
			"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, body, 0o600))
	return path
}

func TestIntegration_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "atrest_test")
	store := NewMongoDEKStore(db.Collection(CollectionName))

	loader, err := NewFileKEKLoader(writeKEKFile(t, t.TempDir()), 0)
	require.NoError(t, err)
	t.Cleanup(func() {
		// Best-effort: tests can't meaningfully act on a Close failure.
		_ = loader.Close()
	})

	c := NewCipher(loader, store, Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})

	in := EncryptedFields{Msg: "secret", Attachments: [][]byte{[]byte("file1")}}
	payload, meta, err := c.Encrypt(ctx, "room-A", in)
	require.NoError(t, err)

	out, err := c.Decrypt(ctx, "room-A", payload, meta)
	require.NoError(t, err)
	assert.Equal(t, in, out)

	// Confirm the row landed in Mongo.
	var row RoomDataKey
	require.NoError(t, db.Collection(CollectionName).FindOne(ctx, bson.M{"_id": "room-A"}).Decode(&row))
	assert.Equal(t, 1, row.KEKVersion)
}

func TestIntegration_ConcurrentFirstWriteRace(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "atrest_test")
	store := NewMongoDEKStore(db.Collection(CollectionName))
	loader, err := NewFileKEKLoader(writeKEKFile(t, t.TempDir()), 0)
	require.NoError(t, err)
	t.Cleanup(func() {
		// Best-effort: tests can't meaningfully act on a Close failure.
		_ = loader.Close()
	})

	// One Cipher per goroutine to defeat the in-process cache.
	const N = 16
	results := make([][]byte, N)
	metas := make([]EncMeta, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := NewCipher(loader, store, Config{DEKCacheSize: 1, DEKCacheTTL: time.Hour})
			p, m, err := c.Encrypt(ctx, "room-race", EncryptedFields{Msg: "x"})
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = p
			metas[i] = m
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		require.NoErrorf(t, e, "goroutine %d encrypt failed", i)
	}

	// Exactly one DEK row exists.
	cur, err := db.Collection(CollectionName).Find(ctx, bson.M{"_id": "room-race"})
	require.NoError(t, err)
	var rows []RoomDataKey
	require.NoError(t, cur.All(ctx, &rows))
	require.Len(t, rows, 1)

	// All ciphertexts decrypt successfully under any cipher (the same DEK).
	c := NewCipher(loader, store, Config{DEKCacheSize: 1, DEKCacheTTL: time.Hour})
	for i := 0; i < N; i++ {
		out, err := c.Decrypt(ctx, "room-race", results[i], metas[i])
		require.NoError(t, err)
		assert.Equal(t, "x", out.Msg)
	}
}

func TestIntegration_KEKRotationReWrap(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "atrest_test")
	store := NewMongoDEKStore(db.Collection(CollectionName))

	dir := t.TempDir()
	path := filepath.Join(dir, "keks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 1,
		"keys": {"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}
	}`), 0o600))
	loader, err := newFileKEKLoaderWithInterval(path, 20*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() {
		// Best-effort: tests can't meaningfully act on a Close failure.
		_ = loader.Close()
	})

	c := NewCipher(loader, store, Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})
	payload, meta, err := c.Encrypt(ctx, "room-rot", EncryptedFields{Msg: "before"})
	require.NoError(t, err)

	// Sleep > 1s so the rewritten file's mod-time is strictly after the
	// loader's recorded mod-time on filesystems with second-level mtime.
	time.Sleep(1100 * time.Millisecond)

	// Rewrite the file with current=2.
	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 2,
		"keys": {
			"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
			"2": "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="
		}
	}`), 0o600))
	require.Eventually(t, func() bool {
		v, _ := loader.Current()
		return v == 2
	}, 3*time.Second, 30*time.Millisecond)

	// Manual re-wrap: read row, unwrap with v1, re-wrap with v2, Replace.
	row, err := store.Get(ctx, "room-rot")
	require.NoError(t, err)
	require.NotNil(t, row)
	kek1, _ := loader.ByVersion(1)
	dek, err := decryptGCM(kek1, row.WrappedDEK, row.WrapNonce)
	require.NoError(t, err)
	_, kek2 := loader.Current()
	wrapped, nonce, err := encryptGCM(kek2, dek, randReaderForTest())
	require.NoError(t, err)
	require.NoError(t, store.Replace(ctx, RoomDataKey{
		ID: row.ID, WrappedDEK: wrapped, WrapNonce: nonce, KEKVersion: 2, CreatedAt: row.CreatedAt,
	}))

	// New Cipher (no cache) decrypts the old payload.
	c2 := NewCipher(loader, store, Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})
	out, err := c2.Decrypt(ctx, "room-rot", payload, meta)
	require.NoError(t, err)
	assert.Equal(t, "before", out.Msg)
}

// randReaderForTest provides a source for nonce generation in the rotation
// test. Production uses crypto/rand.Reader; we use it directly here.
func randReaderForTest() io.Reader { return rand.Reader }
