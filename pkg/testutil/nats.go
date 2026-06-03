//go:build integration

package testutil

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	natsmod "github.com/testcontainers/testcontainers-go/modules/nats"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

var (
	natsOnce      sync.Once
	natsContainer testcontainers.Container
	natsStopProc  func()
	natsURL       string
	natsInitErr   error
)

// JetStream is enabled unconditionally so consumers that publish/consume
// through streams (search-sync-worker, inbox-worker, etc.) Just Work
// against the shared NATS instance. Consumers that only use core NATS
// request/reply pay nothing extra — JS is dormant until used.
//
// Backing instance is either a `nats-server -js` subprocess (when the
// binary is on PATH) or a testcontainers NATS container.
func ensureNATS() (string, error) {
	natsOnce.Do(func() {
		if u, stop, err := startNATSBinary(); err == nil {
			natsURL = u
			natsStopProc = stop
			return
		}
		ctx := context.Background()
		c, err := natsmod.Run(ctx, testimages.NATS,
			testcontainers.WithCmdArgs("--jetstream"),
			testcontainers.WithWaitStrategy(wait.ForLog("Server is ready").WithStartupTimeout(60*time.Second)),
		)
		if err != nil {
			natsInitErr = fmt.Errorf("start nats: %w", err)
			return
		}
		url, err := c.ConnectionString(ctx)
		if err != nil {
			_ = c.Terminate(ctx)
			natsInitErr = fmt.Errorf("get nats url: %w", err)
			return
		}
		natsContainer = c
		natsURL = url
	})
	return natsURL, natsInitErr
}

// NATS returns the URL of a process-shared NATS container with JetStream
// enabled.
func NATS(t *testing.T) string {
	t.Helper()
	u, err := ensureNATS()
	if err != nil {
		t.Fatalf("testutil.NATS: %v", err)
	}
	return u
}

// EnsureNATS starts the shared NATS container if not already started.
// No-t variant intended for TestMain pre-warming.
func EnsureNATS() error { _, err := ensureNATS(); return err }

// TerminateNATS stops the shared NATS instance (subprocess or
// container). Best-effort, idempotent.
func TerminateNATS() {
	if natsStopProc != nil {
		natsStopProc()
		natsStopProc = nil
		return
	}
	if natsContainer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := natsContainer.Terminate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "terminate shared nats: %v\n", err)
	}
	natsContainer = nil
}
