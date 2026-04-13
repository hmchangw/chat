package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/shutdown"
)

type config struct {
	Port             int    `env:"PORT"              envDefault:"8091"`
	KeycloakURL      string `env:"KEYCLOAK_URL"      envDefault:"http://localhost:9090"`
	KeycloakRealm    string `env:"KEYCLOAK_REALM"    envDefault:"chatapp"`
	KeycloakClientID string `env:"KEYCLOAK_CLIENT_ID" envDefault:"nats-chat"`
	AuthServiceURL   string `env:"AUTH_SERVICE_URL"   envDefault:"http://localhost:8080"`
	NatsURL          string `env:"NATS_URL"           envDefault:"nats://localhost:4222"`
	SiteID           string `env:"SITE_ID"            envDefault:"site-local"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	nc, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		slog.Error("connect to NATS", "error", err)
		os.Exit(1)
	}

	sm := newSessionManager()
	h := newHandler(cfg, nc, sm)

	mux := http.NewServeMux()
	h.registerRoutes(mux)

	srv := &http.Server{
		Addr:        fmt.Sprintf(":%d", cfg.Port),
		Handler:     mux,
		ReadTimeout: 30 * time.Second,
		// WriteTimeout deliberately omitted — SSE connections are long-lived.
		IdleTimeout: 60 * time.Second,
	}

	slog.Info("mchat starting", "port", cfg.Port)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	shutdown.Wait(context.Background(), 25*time.Second,
		func(ctx context.Context) error { return srv.Shutdown(ctx) },
		func(_ context.Context) error { sm.closeAll(); return nil },
		func(_ context.Context) error { nc.Drain(); return nil },
	)
}
