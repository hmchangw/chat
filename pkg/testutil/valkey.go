//go:build integration

package testutil

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

// StartValkeyCluster starts a single-node cluster-mode Valkey container,
// assigns all 16384 hash slots to that node, and returns a connected
// *redis.ClusterClient. The ClusterSlots override routes traffic to the
// externally-mapped address rather than the internal 127.0.0.1:6379 that
// the node announces to peers — required for testcontainer port mapping.
// The container and client are terminated/closed via t.Cleanup.
func StartValkeyCluster(t *testing.T) *redis.ClusterClient {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        testimages.Valkey,
			ExposedPorts: []string{"6379/tcp"},
			Cmd: []string{
				"valkey-server",
				"--cluster-enabled", "yes",
				"--cluster-config-file", "nodes.conf",
				"--cluster-node-timeout", "5000",
				"--save", "",
			},
			WaitingFor: wait.ForLog("Ready to accept connections"),
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
		ClusterSlots: func(_ context.Context) ([]redis.ClusterSlot, error) {
			return []redis.ClusterSlot{
				{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: addr}}},
			}, nil
		},
	})
	t.Cleanup(func() { _ = c.Close() })

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	require.NoError(t, c.Ping(pingCtx).Err(), "ping valkey cluster")

	return c
}
