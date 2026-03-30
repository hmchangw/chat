package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/gin-gonic/gin"
	"github.com/nats-io/nkeys"

	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
	"github.com/hmchangw/chat/pkg/shutdown"
)

type config struct {
	Port           string        `env:"PORT"                  envDefault:"8080"`
	AuthSigningKey string        `env:"AUTH_SIGNING_KEY,required"`
	NATSJWTExpiry  time.Duration `env:"NATS_JWT_EXPIRY"       envDefault:"2h"`

	// OIDC settings for SSO token verification.
	OIDCIssuerURL string `env:"OIDC_ISSUER_URL,required"`
	OIDCAudience  string `env:"OIDC_AUDIENCE,required"`
	OIDCVerifyAZP bool   `env:"OIDC_VERIFY_AZP"           envDefault:"false"`
	TLSSkipVerify bool   `env:"TLS_SKIP_VERIFY"            envDefault:"false"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := env.ParseAs[config]()
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	signingKP, err := nkeys.FromSeed([]byte(cfg.AuthSigningKey))
	if err != nil {
		return fmt.Errorf("parse signing key: %w", err)
	}

	ctx := context.Background()

	// Initialize OIDC validator — connects to issuer and fetches JWKS keys.
	oidcValidator, err := pkgoidc.NewValidator(ctx, pkgoidc.Config{
		IssuerURL:     cfg.OIDCIssuerURL,
		Audience:      cfg.OIDCAudience,
		VerifyAZP:     cfg.OIDCVerifyAZP,
		TLSSkipVerify: cfg.TLSSkipVerify,
	})
	if err != nil {
		return fmt.Errorf("create oidc validator: %w", err)
	}
	slog.Info("oidc validator initialized", "issuer", cfg.OIDCIssuerURL)

	handler := NewAuthHandler(oidcValidator, signingKP, cfg.NATSJWTExpiry)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	registerRoutes(r, handler)

	addr := fmt.Sprintf(":%s", cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("auth service starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
		}
	}()

	shutdown.Wait(ctx, 25*time.Second, func(ctx context.Context) error {
		slog.Info("shutting down auth service")
		return srv.Shutdown(ctx)
	})

	return nil
}
