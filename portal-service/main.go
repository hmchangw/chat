package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/ginutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
	"github.com/hmchangw/chat/pkg/shutdown"
)

// maxBodyBytes caps inbound request bodies; the session payload is tiny.
const maxBodyBytes = 64 << 10

type config struct {
	Port    string `env:"PORT"     envDefault:"8080"`
	DevMode bool   `env:"DEV_MODE" envDefault:"false"`

	// OIDC settings — required when DEV_MODE is false.
	OIDCIssuerURL string   `env:"OIDC_ISSUER_URL"`
	OIDCAudiences []string `env:"OIDC_AUDIENCES" envSeparator:","`
	TLSSkipVerify bool     `env:"TLS_SKIP_VERIFY" envDefault:"false"`

	MongoURI            string `env:"MONGO_URI,required"`
	MongoUsername       string `env:"MONGO_USERNAME" envDefault:""`
	MongoPassword       string `env:"MONGO_PASSWORD" envDefault:""`
	MongoDB             string `env:"MONGO_DB"       envDefault:"chat"`
	DirectoryCollection string `env:"DIRECTORY_COLLECTION" envDefault:"directory"`

	// "=" key/value separator: the URL values contain ":".
	SiteAuthURLs     map[string]string `env:"SITE_AUTH_URLS,required"     envSeparator:"," envKeyValSeparator:"="`
	SiteNATSURLs     map[string]string `env:"SITE_NATS_URLS,required"     envSeparator:"," envKeyValSeparator:"="`
	SiteFrontendURLs map[string]string `env:"SITE_FRONTEND_URLS,required" envSeparator:"," envKeyValSeparator:"="`

	AuthForwardTimeout time.Duration `env:"AUTH_FORWARD_TIMEOUT" envDefault:"10s"`
	AdminToken         string        `env:"ADMIN_TOKEN,required"`
}

func validateConfig(cfg *config) error {
	if len(cfg.SiteAuthURLs) == 0 || len(cfg.SiteNATSURLs) == 0 || len(cfg.SiteFrontendURLs) == 0 {
		return fmt.Errorf("SITE_AUTH_URLS, SITE_NATS_URLS, and SITE_FRONTEND_URLS must be non-empty")
	}
	if !cfg.DevMode && (cfg.OIDCIssuerURL == "" || len(cfg.OIDCAudiences) == 0) {
		return fmt.Errorf("OIDC_ISSUER_URL and OIDC_AUDIENCES are required when DEV_MODE is false")
	}
	return nil
}

// bodyLimitMiddleware rejects oversized request bodies before any handler buffering.
func bodyLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
		c.Next()
	}
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
	if err := validateConfig(&cfg); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	ctx := context.Background()

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
	if err != nil {
		return fmt.Errorf("connect mongo: %w", err)
	}
	store := newMongoDirectoryStore(mongoClient.Database(cfg.MongoDB), cfg.DirectoryCollection)

	// Bulk-load the directory before serving; an unloadable directory is fatal.
	dir := newDirMap()
	n, err := dir.Reload(ctx, store)
	if err != nil {
		return fmt.Errorf("initial directory load: %w", err)
	}
	slog.Info("directory loaded", "records", n)

	var verifier TokenVerifier
	if cfg.DevMode {
		slog.Info("dev mode enabled — OIDC validation disabled")
	} else {
		oidcValidator, err := pkgoidc.NewValidator(ctx, pkgoidc.Config{
			IssuerURL:     cfg.OIDCIssuerURL,
			Audiences:     cfg.OIDCAudiences,
			TLSSkipVerify: cfg.TLSSkipVerify,
		})
		if err != nil {
			return fmt.Errorf("create oidc validator: %w", err)
		}
		slog.Info("oidc validator initialized", "issuer", cfg.OIDCIssuerURL)
		verifier = oidcValidator
	}

	handler, err := NewPortalHandler(&PortalHandlerParams{
		Verifier:         verifier,
		Store:            store,
		Dir:              dir,
		Forwarder:        newRestyForwarder(cfg.AuthForwardTimeout, cfg.TLSSkipVerify),
		SiteAuthURLs:     cfg.SiteAuthURLs,
		SiteNATSURLs:     cfg.SiteNATSURLs,
		SiteFrontendURLs: cfg.SiteFrontendURLs,
		AdminToken:       cfg.AdminToken,
		DevMode:          cfg.DevMode,
	})
	if err != nil {
		return fmt.Errorf("create portal handler: %w", err)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(bodyLimitMiddleware())
	r.Use(ginutil.RequestID())
	r.Use(ginutil.AccessLog())
	r.Use(corsMiddleware(handler.frontendOrigins))
	registerRoutes(r, handler)

	addr := fmt.Sprintf(":%s", cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	srvErr := make(chan error, 1)
	go func() {
		slog.Info("portal service starting", "addr", addr)
		srvErr <- srv.ListenAndServe()
	}()

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		// HTTP service cleanup order: stop the server, then disconnect Mongo.
		shutdown.Wait(ctx, 25*time.Second,
			func(ctx context.Context) error {
				slog.Info("shutting down portal service")
				if err := srv.Shutdown(ctx); err != nil {
					return fmt.Errorf("shutdown http server: %w", err)
				}
				return nil
			},
			func(ctx context.Context) error {
				mongoutil.Disconnect(ctx, mongoClient)
				return nil
			},
		)
	}()

	err = <-srvErr
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen portal server: %w", err)
	}
	<-shutdownDone

	return nil
}
