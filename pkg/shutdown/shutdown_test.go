package shutdown_test

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

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

	shutdown.Wait(context.Background(), 5*time.Second, cleanup)

	if !called {
		t.Error("cleanup function was not called")
	}
}

func TestWaitTimesOutWhenCleanupHangs(t *testing.T) {
	hangingCleanup := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	}()

	start := time.Now()
	shutdown.Wait(context.Background(), 500*time.Millisecond, hangingCleanup)
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Errorf("expected shutdown to complete within ~500ms timeout, took %v", elapsed)
	}
}

func TestWaitCompletesBeforeTimeout(t *testing.T) {
	var order []string
	first := func(ctx context.Context) error {
		order = append(order, "first")
		return nil
	}
	second := func(ctx context.Context) error {
		order = append(order, "second")
		return nil
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	}()

	shutdown.Wait(context.Background(), 5*time.Second, first, second)

	assert.Equal(t, []string{"first", "second"}, order)
}
