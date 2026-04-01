//go:build integration

package roomkeystore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// setupValkey starts a valkey/valkey:8 container and returns a connected valkeyStore.
// The container is terminated via t.Cleanup.
func setupValkey(t *testing.T, gracePeriod time.Duration) RoomKeyStore {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "valkey/valkey:8",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForLog("Ready to accept connections"),
		},
		Started: true,
	})
	require.NoError(t, err, "start valkey container")
	t.Cleanup(func() {
		_ = container.Terminate(ctx) // best-effort; ignore cleanup errors
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379")
	require.NoError(t, err)

	store, err := NewValkeyStore(Config{
		Addr:        fmt.Sprintf("%s:%s", host, port.Port()),
		GracePeriod: gracePeriod,
	})
	require.NoError(t, err, "create valkeyStore")
	return store
}

func TestValkeyStore_Integration_RoundTrip(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	pair := RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey}

	// Set
	err := store.Set(ctx, "room-1", "v1", pair)
	require.NoError(t, err)

	// Get — should return the stored pair with version
	got, err := store.Get(ctx, "room-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "v1", got.VersionID)
	assert.Equal(t, pubKey, got.KeyPair.PublicKey)
	assert.Equal(t, privKey, got.KeyPair.PrivateKey)

	// Delete
	err = store.Delete(ctx, "room-1")
	require.NoError(t, err)

	// Get after delete — should return nil, nil
	got, err = store.Get(ctx, "room-1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestValkeyStore_Integration_MissingKey(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	got, err := store.Get(ctx, "nonexistent-room")
	require.NoError(t, err)
	assert.Nil(t, got, "Get on missing key must return nil, nil")
}

func TestValkeyStore_Integration_RotateRoundTrip(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	oldPub := bytes.Repeat([]byte{0xAA}, 65)
	oldPriv := bytes.Repeat([]byte{0xBB}, 32)
	newPub := bytes.Repeat([]byte{0xCC}, 65)
	newPriv := bytes.Repeat([]byte{0xDD}, 32)

	// Set initial key pair.
	err := store.Set(ctx, "room-rot", "v1", RoomKeyPair{PublicKey: oldPub, PrivateKey: oldPriv})
	require.NoError(t, err)

	// Rotate to new key pair.
	err = store.Rotate(ctx, "room-rot", "v2", RoomKeyPair{PublicKey: newPub, PrivateKey: newPriv})
	require.NoError(t, err)

	// Get — should return new key pair as current.
	got, err := store.Get(ctx, "room-rot")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "v2", got.VersionID)
	assert.Equal(t, newPub, got.KeyPair.PublicKey)
	assert.Equal(t, newPriv, got.KeyPair.PrivateKey)

	// GetByVersion with old version — should return old key pair from previous slot.
	oldPair, err := store.GetByVersion(ctx, "room-rot", "v1")
	require.NoError(t, err)
	require.NotNil(t, oldPair)
	assert.Equal(t, oldPub, oldPair.PublicKey)
	assert.Equal(t, oldPriv, oldPair.PrivateKey)

	// GetByVersion with new version — should return new key pair from current slot.
	newPair, err := store.GetByVersion(ctx, "room-rot", "v2")
	require.NoError(t, err)
	require.NotNil(t, newPair)
	assert.Equal(t, newPub, newPair.PublicKey)
	assert.Equal(t, newPriv, newPair.PrivateKey)

	// GetByVersion with unknown version — should return nil, nil.
	unknown, err := store.GetByVersion(ctx, "room-rot", "v-unknown")
	require.NoError(t, err)
	assert.Nil(t, unknown)
}

func TestValkeyStore_Integration_GracePeriodExpiry(t *testing.T) {
	// Use a 1-second grace period so the test completes quickly.
	store := setupValkey(t, 1*time.Second)
	ctx := context.Background()

	oldPub := bytes.Repeat([]byte{0x01}, 65)
	oldPriv := bytes.Repeat([]byte{0x02}, 32)
	newPub := bytes.Repeat([]byte{0x03}, 65)
	newPriv := bytes.Repeat([]byte{0x04}, 32)

	err := store.Set(ctx, "room-grace", "v1", RoomKeyPair{PublicKey: oldPub, PrivateKey: oldPriv})
	require.NoError(t, err)

	err = store.Rotate(ctx, "room-grace", "v2", RoomKeyPair{PublicKey: newPub, PrivateKey: newPriv})
	require.NoError(t, err)

	// Immediately after rotate, old key should still be retrievable.
	oldPair, err := store.GetByVersion(ctx, "room-grace", "v1")
	require.NoError(t, err)
	require.NotNil(t, oldPair, "old key should be retrievable during grace period")

	// Wait for grace period to elapse. This sleep is intentional — we are
	// waiting for an external Valkey TTL, not synchronising goroutines.
	time.Sleep(2 * time.Second)

	// Old key should now be expired.
	oldPair, err = store.GetByVersion(ctx, "room-grace", "v1")
	require.NoError(t, err)
	assert.Nil(t, oldPair, "old key should be expired after grace period")

	// Current key should still be present (no TTL).
	got, err := store.Get(ctx, "room-grace")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "v2", got.VersionID)
}

func TestValkeyStore_Integration_RotateNoCurrentKey(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	err := store.Rotate(ctx, "room-empty", "v1", RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0x01}, 65),
		PrivateKey: bytes.Repeat([]byte{0x02}, 32),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoCurrentKey), "should return ErrNoCurrentKey")
}

func TestValkeyStore_Integration_DeleteBothKeys(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	// Set + Rotate to create both current and previous keys.
	err := store.Set(ctx, "room-del", "v1", RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0xAA}, 65),
		PrivateKey: bytes.Repeat([]byte{0xBB}, 32),
	})
	require.NoError(t, err)

	err = store.Rotate(ctx, "room-del", "v2", RoomKeyPair{
		PublicKey:  bytes.Repeat([]byte{0xCC}, 65),
		PrivateKey: bytes.Repeat([]byte{0xDD}, 32),
	})
	require.NoError(t, err)

	// Delete should remove both.
	err = store.Delete(ctx, "room-del")
	require.NoError(t, err)

	// Current key should be gone.
	got, err := store.Get(ctx, "room-del")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Previous key should also be gone.
	prev, err := store.GetByVersion(ctx, "room-del", "v1")
	require.NoError(t, err)
	assert.Nil(t, prev)
}
