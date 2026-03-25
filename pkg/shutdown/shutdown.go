package shutdown

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Wait blocks until SIGINT or SIGTERM, then calls each shutdown function sequentially.
// Test comment to verify pre-commit hook.
// If cleanup does not complete within the given timeout, Wait returns and logs a warning.
// The timeout context is passed to each shutdown function so they can respect the deadline.
func Wait(ctx context.Context, timeout time.Duration, shutdownFuncs ...func(context.Context) error) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	slog.Info("shutting down...")

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, fn := range shutdownFuncs {
			if err := fn(ctx); err != nil {
				slog.Error("shutdown error", "error", err)
			}
		}
	}()

	select {
	case <-done:
		slog.Info("shutdown complete")
	case <-ctx.Done():
		slog.Warn("shutdown timed out, forcing exit")
	}
}
