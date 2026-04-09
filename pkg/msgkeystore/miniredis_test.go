package msgkeystore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMiniredis starts an in-process miniredis server and returns a connected KeyStore.
// The server is closed via t.Cleanup.
func setupMiniredis(t *testing.T, gracePeriod time.Duration) (KeyStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	store, err := NewValkeyStore(Config{
		Addr:        mr.Addr(),
		GracePeriod: gracePeriod,
	})
	require.NoError(t, err, "create valkeyStore")
	return store, mr
}

func TestValkeyStore_Miniredis_RoundTrip(t *testing.T) {
	store, _ := setupMiniredis(t, time.Hour)
	ctx := context.Background()

	aesKey := make([]byte, 32)
	for i := range aesKey {
		aesKey[i] = byte(i)
	}

	// Set
	ver, err := store.Set(ctx, "room-1", aesKey)
	require.NoError(t, err)
	assert.Equal(t, 0, ver)

	// Get — should return stored key with version
	got, err := store.Get(ctx, "room-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 0, got.Version)
	assert.Equal(t, aesKey, got.Key)

	// Delete
	err = store.Delete(ctx, "room-1")
	require.NoError(t, err)

	// Get after delete — should return nil, nil
	got, err = store.Get(ctx, "room-1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestValkeyStore_Miniredis_MissingKey(t *testing.T) {
	store, _ := setupMiniredis(t, time.Hour)
	ctx := context.Background()

	got, err := store.Get(ctx, "nonexistent-room")
	require.NoError(t, err)
	assert.Nil(t, got, "Get on missing key must return nil, nil")
}

func TestValkeyStore_Miniredis_RotateRoundTrip(t *testing.T) {
	store, _ := setupMiniredis(t, time.Hour)
	ctx := context.Background()

	oldKey := make([]byte, 32)
	for i := range oldKey {
		oldKey[i] = byte(i)
	}
	newKey := make([]byte, 32)
	for i := range newKey {
		newKey[i] = byte(i + 100)
	}

	// Set initial key.
	ver, err := store.Set(ctx, "room-rot", oldKey)
	require.NoError(t, err)
	assert.Equal(t, 0, ver)

	// Rotate to new key.
	ver, err = store.Rotate(ctx, "room-rot", newKey)
	require.NoError(t, err)
	assert.Equal(t, 1, ver)

	// Get — should return new key as current.
	got, err := store.Get(ctx, "room-rot")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 1, got.Version)
	assert.Equal(t, newKey, got.Key)

	// GetByVersion with old version — should return old key from previous slot.
	oldResult, err := store.GetByVersion(ctx, "room-rot", 0)
	require.NoError(t, err)
	require.NotNil(t, oldResult)
	assert.Equal(t, oldKey, oldResult)

	// GetByVersion with new version — should return new key from current slot.
	newResult, err := store.GetByVersion(ctx, "room-rot", 1)
	require.NoError(t, err)
	require.NotNil(t, newResult)
	assert.Equal(t, newKey, newResult)

	// GetByVersion with unknown version — should return nil, nil.
	unknown, err := store.GetByVersion(ctx, "room-rot", 999)
	require.NoError(t, err)
	assert.Nil(t, unknown)
}

func TestValkeyStore_Miniredis_GracePeriodExpiry(t *testing.T) {
	store, mr := setupMiniredis(t, 1*time.Second)
	ctx := context.Background()

	oldKey := make([]byte, 32)
	for i := range oldKey {
		oldKey[i] = byte(i)
	}
	newKey := make([]byte, 32)
	for i := range newKey {
		newKey[i] = byte(i + 50)
	}

	_, err := store.Set(ctx, "room-grace", oldKey)
	require.NoError(t, err)

	_, err = store.Rotate(ctx, "room-grace", newKey)
	require.NoError(t, err)

	// Immediately after rotate, old key should still be retrievable.
	oldResult, err := store.GetByVersion(ctx, "room-grace", 0)
	require.NoError(t, err)
	require.NotNil(t, oldResult, "old key should be retrievable during grace period")

	// Fast-forward miniredis time past the grace period.
	mr.FastForward(2 * time.Second)

	// Old key should now be expired.
	oldResult, err = store.GetByVersion(ctx, "room-grace", 0)
	require.NoError(t, err)
	assert.Nil(t, oldResult, "old key should be expired after grace period")

	// Current key should still be present (no TTL).
	got, err := store.Get(ctx, "room-grace")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 1, got.Version)
}

func TestValkeyStore_Miniredis_RotateNoCurrentKey(t *testing.T) {
	store, _ := setupMiniredis(t, time.Hour)
	ctx := context.Background()

	_, err := store.Rotate(ctx, "room-empty", make([]byte, 32))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoCurrentKey), "should return ErrNoCurrentKey")
}

func TestValkeyStore_Miniredis_DeleteBothKeys(t *testing.T) {
	store, _ := setupMiniredis(t, time.Hour)
	ctx := context.Background()

	oldKey := make([]byte, 32)
	for i := range oldKey {
		oldKey[i] = 0xAA
	}
	newKey := make([]byte, 32)
	for i := range newKey {
		newKey[i] = 0xBB
	}

	// Set + Rotate to create both current and previous keys.
	_, err := store.Set(ctx, "room-del", oldKey)
	require.NoError(t, err)

	_, err = store.Rotate(ctx, "room-del", newKey)
	require.NoError(t, err)

	// Delete should remove both.
	err = store.Delete(ctx, "room-del")
	require.NoError(t, err)

	// Current key should be gone.
	got, err := store.Get(ctx, "room-del")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Previous key should also be gone.
	prev, err := store.GetByVersion(ctx, "room-del", 0)
	require.NoError(t, err)
	assert.Nil(t, prev)
}
