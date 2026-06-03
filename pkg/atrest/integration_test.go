//go:build integration

package atrest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/testutil"
)

func newTestVaultWrapper(t *testing.T, ctx context.Context) KeyWrapper {
	t.Helper()
	v := testutil.Vault(t, ctx)
	w, err := NewVaultKeyWrapper(ctx, VaultConfig{
		Address:      v.Address,
		TransitMount: v.TransitMount,
		TransitKey:   v.TransitKey,
		Token:        v.Token,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		// Best-effort: tests can't meaningfully act on a Close failure.
		_ = w.Close()
	})
	return w
}

func TestIntegration_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "atrest_test")
	store := NewMongoDEKStore(db.Collection(CollectionName))
	wrapper := newTestVaultWrapper(t, ctx)

	c := NewCipher(wrapper, store, Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})

	in := EncryptedFields{Msg: "secret", Attachments: [][]byte{[]byte("file1")}}
	payload, meta, err := c.Encrypt(ctx, "room-A", in)
	require.NoError(t, err)

	out, err := c.Decrypt(ctx, "room-A", payload, meta)
	require.NoError(t, err)
	assert.Equal(t, in, out)

	// Confirm the row landed in Mongo with a Vault-shaped ciphertext.
	var row RoomDataKey
	require.NoError(t, db.Collection(CollectionName).FindOne(ctx, bson.M{"_id": "room-A"}).Decode(&row))
	assert.NotEmpty(t, row.WrappedDEK)
	assert.True(t, len(row.WrappedDEK) > len("vault:v1:"), "Vault ciphertext should be longer than the prefix")
}

func TestIntegration_ConcurrentFirstWriteRace(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "atrest_test")
	store := NewMongoDEKStore(db.Collection(CollectionName))
	wrapper := newTestVaultWrapper(t, ctx)

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
			c := NewCipher(wrapper, store, Config{DEKCacheSize: 1, DEKCacheTTL: time.Hour})
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
	c := NewCipher(wrapper, store, Config{DEKCacheSize: 1, DEKCacheTTL: time.Hour})
	for i := 0; i < N; i++ {
		out, err := c.Decrypt(ctx, "room-race", results[i], metas[i])
		require.NoError(t, err)
		assert.Equal(t, "x", out.Msg)
	}
}

func TestIntegration_VaultKeyRotation(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "atrest_test")
	store := NewMongoDEKStore(db.Collection(CollectionName))

	v := testutil.Vault(t, ctx)
	wrapper, err := NewVaultKeyWrapper(ctx, VaultConfig{
		Address:      v.Address,
		TransitMount: v.TransitMount,
		TransitKey:   v.TransitKey,
		Token:        v.Token,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		// Best-effort: tests can't meaningfully act on a Close failure.
		_ = wrapper.Close()
	})

	c := NewCipher(wrapper, store, Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})
	payload, meta, err := c.Encrypt(ctx, "room-rot", EncryptedFields{Msg: "before"})
	require.NoError(t, err)

	// Rotate the transit key in Vault — produces a new key version.
	require.NoError(t, v.Rotate(ctx))

	// A fresh Cipher (cleared cache) still decrypts the old payload —
	// Vault's transit engine accepts older versions until the key's
	// min_decryption_version is bumped.
	c2 := NewCipher(wrapper, store, Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})
	out, err := c2.Decrypt(ctx, "room-rot", payload, meta)
	require.NoError(t, err)
	assert.Equal(t, "before", out.Msg)
}
