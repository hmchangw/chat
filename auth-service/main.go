package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	callout "github.com/synadia-io/callout.go"

	"github.com/hmchangw/chat/pkg/shutdown"
)

type config struct {
	NatsURL        string `env:"NATS_URL"          envDefault:"nats://localhost:4222"`
	NatsCreds      string `env:"NATS_CREDS"`
	AuthSigningKey string `env:"AUTH_SIGNING_KEY,required"`
}

func main() {
	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	signingKP, err := nkeys.FromSeed([]byte(cfg.AuthSigningKey))
	if err != nil {
		slog.Error("parse signing key failed", "error", err)
		os.Exit(1)
	}

	var opts []nats.Option
	if cfg.NatsCreds != "" {
		opts = append(opts, nats.UserCredentials(cfg.NatsCreds))
	}
	opts = append(opts, nats.Name("auth-service"))

	nc, err := nats.Connect(cfg.NatsURL, opts...)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}
	defer nc.Close()
	slog.Info("connected to NATS", "url", cfg.NatsURL)

	// TODO: Replace SSOTokenVerifier with actual SSO implementation.
	verifier := &SSOTokenVerifier{}
	handler := NewAuthHandler(verifier, signingKP)

	svc, err := callout.NewAuthorizationService(nc,
		callout.Authorizer(handler.Authorizer()),
		callout.ResponseSignerKey(signingKP),
	)
	if err != nil {
		slog.Error("create auth callout service failed", "error", err)
		os.Exit(1)
	}
	slog.Info("auth callout service started")

	shutdown.Wait(context.Background(), func(ctx context.Context) error {
		slog.Info("stopping auth callout service")
		if err := svc.Stop(); err != nil {
			return fmt.Errorf("stop callout service: %w", err)
		}
		nc.Close()
		return nil
	})
}

// SSOTokenVerifier implements TokenVerifier using actual SSO validation.
// This is a placeholder — replace with real SSO/OAuth token verification.
type SSOTokenVerifier struct{}

func (v *SSOTokenVerifier) Verify(token string) (string, error) {
	// TODO: Implement actual SSO token verification.
	return "", fmt.Errorf("SSO token verification not implemented")
}
