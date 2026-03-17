package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hmchangw/chat/internal/bridge"
	"github.com/hmchangw/chat/internal/config"
	"github.com/nats-io/nats.go"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "error", err)
		os.Exit(1)
	}

	nc, err := nats.Connect(cfg.NATSUrl,
		nats.Name("chat-bridge"),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.Warn("nats disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("nats reconnected", "url", nc.ConnectedUrl())
		}),
	)
	if err != nil {
		logger.Error("nats connect failed", "error", err)
		os.Exit(1)
	}
	defer nc.Drain()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	b, err := bridge.New(ctx, nc, cfg, logger)
	if err != nil {
		logger.Error("bridge init failed", "error", err)
		os.Exit(1)
	}

	if err := b.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("bridge exited with error", "error", err)
		os.Exit(1)
	}

	logger.Info("bridge shut down gracefully")
}
