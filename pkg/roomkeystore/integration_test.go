//go:build integration

package roomkeystore

import (
	"bytes"
	"context"
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
func setupValkey(t *testing.T, ttl time.Duration) *valkeyStore {
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
		Addr:   fmt.Sprintf("%s:%s", host, port.Port()),
		KeyTTL: ttl,
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
	err := store.Set(ctx, "room-1", pair)
	require.NoError(t, err)

	// Get — should return the stored pair
	got, err := store.Get(ctx, "room-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, pubKey, got.PublicKey)
	assert.Equal(t, privKey, got.PrivateKey)

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

func TestValkeyStore_Integration_TTLExpiry(t *testing.T) {
	// Use a 1-second TTL so the test completes quickly.
	store := setupValkey(t, 1*time.Second)
	ctx := context.Background()

	pubKey := bytes.Repeat([]byte{0x01}, 65)
	privKey := bytes.Repeat([]byte{0x02}, 32)

	err := store.Set(ctx, "room-ttl", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
	require.NoError(t, err)

	// Confirm key exists before expiry.
	got, err := store.Get(ctx, "room-ttl")
	require.NoError(t, err)
	require.NotNil(t, got)

	// Wait for TTL to elapse. This sleep is intentional — we are waiting for
	// an external Valkey TTL, not synchronising goroutines.
	time.Sleep(2 * time.Second)

	// Key should now be gone.
	got, err = store.Get(ctx, "room-ttl")
	require.NoError(t, err)
	assert.Nil(t, got, "key should be expired after TTL")
}
