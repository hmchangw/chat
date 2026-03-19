package shutdown

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// Wait blocks until SIGINT or SIGTERM, then calls each shutdown function.
func Wait(ctx context.Context, shutdownFuncs ...func(context.Context) error) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down...")

	for _, fn := range shutdownFuncs {
		if err := fn(ctx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}
}
