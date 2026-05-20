//go:build integration

package testutil

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

var (
	valkeyOnce      sync.Once
	valkeyContainer testcontainers.Container
	valkeyAddr      string
	valkeyInitErr   error
)

func ensureValkey() (string, error) {
	valkeyOnce.Do(func() {
		ctx := context.Background()
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        testimages.Valkey,
				ExposedPorts: []string{"6379/tcp"},
				Cmd:          []string{"valkey-server", "--save", "", "--appendonly", "no"},
				WaitingFor:   wait.ForLog("Ready to accept connections").WithStartupTimeout(30 * time.Second),
			},
			Started: true,
		})
		if err != nil {
			valkeyInitErr = fmt.Errorf("start valkey: %w", err)
			return
		}
		host, err := container.Host(ctx)
		if err != nil {
			_ = container.Terminate(ctx)
			valkeyInitErr = fmt.Errorf("get valkey host: %w", err)
			return
		}
		port, err := container.MappedPort(ctx, "6379")
		if err != nil {
			_ = container.Terminate(ctx)
			valkeyInitErr = fmt.Errorf("get valkey port: %w", err)
			return
		}
		valkeyContainer = container
		valkeyAddr = fmt.Sprintf("%s:%s", host, port.Port())
	})
	return valkeyAddr, valkeyInitErr
}

// Valkey returns the addr (host:port) of a process-shared Valkey container.
// Persistence is disabled (--save '' --appendonly no) so the data plane
// is purely in-memory; callers wanting per-test isolation should namespace
// their keys or FLUSHDB on cleanup.
func Valkey(t *testing.T) string {
	t.Helper()
	addr, err := ensureValkey()
	if err != nil {
		t.Fatalf("testutil.Valkey: %v", err)
	}
	return addr
}

// EnsureValkey starts the shared Valkey container if not already started.
// No-t variant intended for TestMain pre-warming.
func EnsureValkey() error { _, err := ensureValkey(); return err }

// FlushValkey wipes the shared Valkey keyspace. Intended for per-test
// cleanup so sibling tests don't see each other's keys. Uses a raw
// go-redis client so we don't need to expose FLUSHDB on the production
// valkeyutil interface. Failure is a test failure — leftover state would
// silently break the next sibling test.
func FlushValkey(t *testing.T) {
	t.Helper()
	addr := Valkey(t)
	rc := goredis.NewClient(&goredis.Options{Addr: addr})
	defer func() { _ = rc.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rc.FlushDB(ctx).Err(); err != nil {
		t.Errorf("flush shared valkey: %v", err)
	}
}

// TerminateValkey stops the shared Valkey container. Best-effort, idempotent.
func TerminateValkey() {
	if valkeyContainer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := valkeyContainer.Terminate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "terminate shared valkey: %v\n", err)
	}
	valkeyContainer = nil
}
