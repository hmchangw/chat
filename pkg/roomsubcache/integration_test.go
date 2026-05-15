//go:build integration

package roomsubcache_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hmchangw/chat/pkg/roomsubcache"
	"github.com/hmchangw/chat/pkg/testutil/testimages"
	"github.com/hmchangw/chat/pkg/valkeyutil"
)

// setupValkey starts a valkey/valkey:8 container and returns a connected
// valkeyutil.Client. The container is terminated via t.Cleanup.
func setupValkey(t *testing.T) valkeyutil.Client {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        testimages.Valkey,
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

	client, err := valkeyutil.Connect(ctx, fmt.Sprintf("%s:%s", host, port.Port()), "")
	require.NoError(t, err, "connect valkey")
	t.Cleanup(func() { _ = client.Close() }) // best-effort; ignore cleanup errors
	return client
}

func TestValkeyCache_Integration_SetGetInvalidate(t *testing.T) {
	client := setupValkey(t)
	cache := roomsubcache.NewValkeyCache(client)
	ctx := context.Background()

	members := []roomsubcache.Member{
		{ID: "u1", Account: "alice"},
		{ID: "u2", Account: "bob"},
	}
	require.NoError(t, cache.Set(ctx, "room-1", members, time.Minute))

	got, err := cache.Get(ctx, "room-1")
	require.NoError(t, err)
	assert.Equal(t, members, got)

	require.NoError(t, cache.Invalidate(ctx, "room-1"))

	_, err = cache.Get(ctx, "room-1")
	assert.ErrorIs(t, err, valkeyutil.ErrCacheMiss)
}

func TestValkeyCache_Integration_MissOnUnsetRoom(t *testing.T) {
	client := setupValkey(t)
	cache := roomsubcache.NewValkeyCache(client)
	ctx := context.Background()

	_, err := cache.Get(ctx, "never-set")
	assert.ErrorIs(t, err, valkeyutil.ErrCacheMiss)
}

func TestValkeyCache_Integration_TTLExpires(t *testing.T) {
	client := setupValkey(t)
	cache := roomsubcache.NewValkeyCache(client)
	ctx := context.Background()

	require.NoError(t, cache.Set(ctx, "room-ttl", []roomsubcache.Member{{ID: "u1", Account: "a"}}, time.Second))

	// Poll for expiry — Valkey honors TTL with sub-second granularity but
	// asserting on a precise deadline is flaky. Allow up to 5s.
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		_, lastErr = cache.Get(ctx, "room-ttl")
		if lastErr != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	assert.ErrorIs(t, lastErr, valkeyutil.ErrCacheMiss, "expected key to expire within 5s")
}

func TestValkeyCache_Integration_EmptyListIsCacheHit(t *testing.T) {
	client := setupValkey(t)
	cache := roomsubcache.NewValkeyCache(client)
	ctx := context.Background()

	require.NoError(t, cache.Set(ctx, "empty-room", []roomsubcache.Member{}, time.Minute))

	got, err := cache.Get(ctx, "empty-room")
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}
