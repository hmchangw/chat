//go:build e2e

package e2e

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	"github.com/hmchangw/chat/e2e/harness"
)

// workerLifecycle owns the Stop/Start of a single compose-managed service
// across both stack-ownership modes:
//
//   - When `make e2e` owns the lifecycle (testcontainers compose), uses
//     the container handle directly.
//   - When `E2E_REUSE_STACK=1` (compose handle absent), shells out to
//     `docker compose -f <file> stop|start <svc>`. Same wire effect; the
//     fallback adds ~2s per operation for the CLI roundtrip but doesn't
//     require chapter-7 lifecycle plumbing.
//
// Tests that previously skipped under REUSE mode (e.g. CatchUpAfterOutage)
// can now run in both modes.
type workerLifecycle struct {
	name      string
	container testcontainers.Container // nil when in REUSE-mode fallback
}

func newWorkerLifecycle(t *testing.T, ctx context.Context, name string) *workerLifecycle {
	t.Helper()
	c, err := stack.ServiceContainer(ctx, name)
	switch {
	case err == nil:
		return &workerLifecycle{name: name, container: c}
	case errors.Is(err, harness.ErrServiceContainerUnavailable):
		// REUSE mode -- shell-out fallback.
		return &workerLifecycle{name: name}
	default:
		t.Fatalf("ServiceContainer(%q): %v", name, err)
		return nil
	}
}

func (w *workerLifecycle) Stop(t *testing.T, ctx context.Context) {
	t.Helper()
	if w.container != nil {
		timeout := 10 * time.Second
		require.NoError(t, w.container.Stop(ctx, &timeout), "stop %s via compose handle", w.name)
		return
	}
	// Shell-out fallback. `docker stop` is simpler than `docker compose stop`
	// here -- we already know the container name from the e2e suite's
	// stable container_name conventions.
	out, err := exec.CommandContext(ctx, "docker", "stop", "--time", "10", w.name).CombinedOutput()
	require.NoError(t, err, "docker stop %s: %s", w.name, string(out))
}

func (w *workerLifecycle) Start(t *testing.T, ctx context.Context) {
	t.Helper()
	if w.container != nil {
		require.NoError(t, w.container.Start(ctx), "start %s via compose handle", w.name)
		return
	}
	out, err := exec.CommandContext(ctx, "docker", "start", w.name).CombinedOutput()
	require.NoError(t, err, "docker start %s: %s", w.name, string(out))
}
