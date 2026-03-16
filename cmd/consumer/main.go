package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hmchangw/chat/internal/config"
	"github.com/hmchangw/chat/internal/consumer"
	"github.com/hmchangw/chat/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg := config.Load()

	cassandraStore, err := store.NewCassandraStore(cfg.Cassandra)
	if err != nil {
		slog.Error("failed to connect to cassandra", "error", err)
		os.Exit(1)
	}
	defer cassandraStore.Close()

	cons, err := consumer.New(cfg, cassandraStore)
	if err != nil {
		slog.Error("failed to create consumer", "error", err)
		os.Exit(1)
	}
	defer cons.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("starting nats-to-cassandra consumer")
	if err := cons.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("consumer exited with error", "error", err)
		os.Exit(1)
	}
	slog.Info("consumer shut down gracefully")
}
