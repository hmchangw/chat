//go:build e2e

package harness

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CaptureLogs registers a t.Cleanup that, ONLY if the test failed, writes
// the tail of each named service's container stdout to
// e2e/logs/<test-name>/<service>.log. On pass: nothing is written, keeping
// the logs/ tree small.
//
// Each test in chapter 12 that exercises multi-service interactions calls
// this with the services likely to carry diagnostic signal on failure
// (typically the workers either side of the federation hop).
func CaptureLogs(t *testing.T, stack *Stack, services ...string) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		dir := filepath.Join("logs", sanitizeTestName(t.Name()))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Logf("logs: mkdir %s: %v", dir, err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, svc := range services {
			dst := filepath.Join(dir, svc+".log")
			if err := captureOne(ctx, stack, svc, dst); err != nil {
				t.Logf("logs: capture %s: %v", svc, err)
			}
		}
	})
}

// captureOne writes the tail of svc's log to dst. Prefers the compose-module
// path (ServiceContainer.Logs); falls back to `docker logs --tail 1000` when
// the stack was reused (E2E_REUSE_STACK=1 -- no compose handle).
func captureOne(ctx context.Context, stack *Stack, svc, dst string) error {
	if stack != nil {
		if container, err := stack.ServiceContainer(ctx, svc); err == nil {
			r, lerr := container.Logs(ctx)
			if lerr == nil {
				defer r.Close()
				return writeTailToFile(r, dst, 1000)
			}
		}
	}
	// Fallback: shell out to `docker logs`. Works against any externally-
	// running container with the given name (compose-managed or not).
	out, err := exec.CommandContext(ctx, "docker", "logs", "--tail", "1000", svc).CombinedOutput()
	if err != nil {
		return err
	}
	return os.WriteFile(dst, out, 0o644)
}

// writeTailToFile reads r and writes only the last `lines` lines to dst.
// Useful when ServiceContainer.Logs streams the entire log -- we don't want
// the on-disk dump to balloon.
func writeTailToFile(r io.Reader, dst string, lines int) error {
	ring := make([]string, 0, lines)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if len(ring) == lines {
			ring = ring[1:]
		}
		ring = append(ring, scanner.Text())
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, line := range ring {
		if _, err := f.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return nil
}

// sanitizeTestName replaces filesystem-unsafe runes from t.Name() so the
// directory path is well-formed across OSes.
func sanitizeTestName(name string) string {
	return strings.NewReplacer(
		"/", "_",
		" ", "_",
		":", "_",
		string(os.PathSeparator), "_",
	).Replace(name)
}
