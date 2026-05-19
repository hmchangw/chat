//go:build integration

package valkeyutil

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

// setupClusterClient starts a single-node cluster-mode Valkey container and
// returns a Client backed by universalClient. ConnectCluster itself cannot
// be used here because its default auto-discovery follows CLUSTER SLOTS, which
// returns the container-internal 127.0.0.1:6379 — unreachable from the host.
// Instead we apply the ClusterSlots override so go-redis routes all commands
// to the externally-mapped address, bypassing the port-translation problem.
// ConnectCluster's error-wrapping path is covered by TestConnectCluster_ErrorPath.
func setupClusterClient(t *testing.T) Client {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: testimages.Valkey,
			Cmd: []string{
				"valkey-server",
				"--cluster-enabled", "yes",
				"--cluster-config-file", "nodes.conf",
				"--cluster-node-timeout", "5000",
				"--save", "",
			},
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForLog("Ready to accept connections"),
		},
		Started: true,
	})
	require.NoError(t, err, "start valkey cluster container")
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379")
	require.NoError(t, err)
	addr := fmt.Sprintf("%s:%s", host, port.Port())

	exitCode, _, err := container.Exec(ctx, []string{"valkey-cli", "CLUSTER", "ADDSLOTSRANGE", "0", "16383"})
	require.NoError(t, err, "exec cluster addslotsrange")
	require.Equal(t, 0, exitCode, "cluster addslotsrange must exit 0")

	require.Eventually(t, func() bool {
		_, out, execErr := container.Exec(ctx, []string{"valkey-cli", "CLUSTER", "INFO"})
		if execErr != nil {
			return false
		}
		buf, _ := io.ReadAll(out)
		return strings.Contains(string(buf), "cluster_state:ok")
	}, 10*time.Second, 100*time.Millisecond, "cluster must reach ok state")

	c := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{addr},
		ClusterSlots: func(ctx context.Context) ([]redis.ClusterSlot, error) {
			return []redis.ClusterSlot{
				{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: addr}}},
			}, nil
		},
	})
	t.Cleanup(func() { _ = c.Close() })

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	require.NoError(t, c.Ping(pingCtx).Err(), "ping valkey cluster")

	return &universalClient{c: c}
}

func TestClusterRedisClient_Integration_GetSetDel(t *testing.T) {
	client := setupClusterClient(t)
	ctx := context.Background()

	require.NoError(t, client.Set(ctx, "k1", "hello", time.Hour))

	val, err := client.Get(ctx, "k1")
	require.NoError(t, err)
	assert.Equal(t, "hello", val)

	require.NoError(t, client.Del(ctx, "k1"))

	_, err = client.Get(ctx, "k1")
	assert.ErrorIs(t, err, ErrCacheMiss)
}

func TestClusterRedisClient_Integration_CacheMiss(t *testing.T) {
	client := setupClusterClient(t)
	ctx := context.Background()

	_, err := client.Get(ctx, "no-such-key")
	assert.ErrorIs(t, err, ErrCacheMiss)
}

func TestClusterRedisClient_Integration_DelEmpty(t *testing.T) {
	client := setupClusterClient(t)
	ctx := context.Background()

	require.NoError(t, client.Del(ctx))
}
