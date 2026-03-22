package shutdown_test

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/shutdown"
)

func TestWaitCallsCleanup(t *testing.T) {
	called := false
	cleanup := func(ctx context.Context) error {
		called = true
		return nil
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	}()

	shutdown.Wait(context.Background(), cleanup)

	if !called {
		t.Error("cleanup function was not called")
	}
}
