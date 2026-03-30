package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/hmchangw/chat/pkg/shutdown"
)

type config struct {
	Port int `env:"PORT" envDefault:"8090"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	hub := newNATSHub()
	h := newHandler(hub)

	mux := http.NewServeMux()
	h.registerRoutes(mux)

	srv := &http.Server{
		Addr:        fmt.Sprintf(":%d", cfg.Port),
		Handler:     mux,
		ReadTimeout: 30 * time.Second,
		// WriteTimeout deliberately omitted — SSE connections are long-lived.
		IdleTimeout: 60 * time.Second,
	}

	slog.Info("nats-debug starting", "port", cfg.Port)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	shutdown.Wait(context.Background(), 10*time.Second,
		func(ctx context.Context) error { return srv.Shutdown(ctx) },
		func(_ context.Context) error { hub.Disconnect(); return nil },
	)
}
