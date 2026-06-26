package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/ginutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
	"github.com/hmchangw/chat/pkg/shutdown"
)

// cacheRetryInterval paces reload attempts after a failed directory load, so
// a Mongo blip at startup does not leave the portal unready for a full
// refresh interval.
const cacheRetryInterval = 30 * time.Second

type config struct {
	Port              string `env:"PORT"                         envDefault:"8081"`
	DevMode           bool   `env:"DEV_MODE"                     envDefault:"false"`
	DevFallbackSiteID string `env:"PORTAL_DEV_FALLBACK_SITE_ID"  envDefault:"site-local"`

	// SiteURLs is the per-site URL registry: a JSON object mapping siteId to
	// {baseUrl}, listed explicitly so sites on different domains each have one.
	SiteURLs string `env:"PORTAL_SITE_URLS,required"`

	// CacheRefreshInterval drives how often the directory is reloaded (the
	// hr_employee × users intersection via $lookup). Shorter than the daily HR
	// cron so a newly provisioned user appears within a couple of hours.
	CacheRefreshInterval time.Duration `env:"PORTAL_CACHE_REFRESH_INTERVAL" envDefault:"2h"`

	MongoURI      string `env:"MONGO_URI,required"`
	MongoDB       string `env:"MONGO_DB"       envDefault:"portal"`
	MongoUsername string `env:"MONGO_USERNAME" envDefault:""`
	MongoPassword string `env:"MONGO_PASSWORD" envDefault:""`

	OIDCIssuerURL    string   `env:"OIDC_ISSUER_URL"`
	OIDCClientID     string   `env:"OIDC_CLIENT_ID"`
	OIDCClientSecret string   `env:"OIDC_CLIENT_SECRET"`
	OIDCRedirectURL  string   `env:"OIDC_REDIRECT_URL"`
	OIDCScopes       []string `env:"OIDC_SCOPES" envSeparator:" " envDefault:"openid profile email"`
	CookieSecure     bool     `env:"PORTAL_COOKIE_SECURE" envDefault:"true"`
	TLSSkipVerify    bool     `env:"TLS_SKIP_VERIFY" envDefault:"false"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

// requireOIDCConfig fails fast when the prod OIDC fields are missing
// (caarlos0/env can't express conditional-required).
func requireOIDCConfig(cfg *config) error {
	if cfg.DevMode {
		return nil
	}
	var missing []string
	if cfg.OIDCIssuerURL == "" {
		missing = append(missing, "OIDC_ISSUER_URL")
	}
	if cfg.OIDCClientID == "" {
		missing = append(missing, "OIDC_CLIENT_ID")
	}
	if cfg.OIDCClientSecret == "" {
		missing = append(missing, "OIDC_CLIENT_SECRET")
	}
	if cfg.OIDCRedirectURL == "" {
		missing = append(missing, "OIDC_REDIRECT_URL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required OIDC config when DEV_MODE=false: %v", missing)
	}
	return nil
}

func run() error {
	cfg, err := env.ParseAs[config]()
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	sites, err := parseSiteURLs(cfg.SiteURLs)
	if err != nil {
		return fmt.Errorf("parse site URL registry: %w", err)
	}

	ctx := context.Background()

	if err := requireOIDCConfig(&cfg); err != nil {
		return fmt.Errorf("check oidc config: %w", err)
	}

	var auth loginAuthenticator
	if cfg.DevMode {
		if _, ok := sites[cfg.DevFallbackSiteID]; !ok {
			return fmt.Errorf("dev fallback site %q missing from PORTAL_SITE_URLS", cfg.DevFallbackSiteID)
		}
	} else {
		httpClient := pkgoidc.HTTPClient(cfg.TLSSkipVerify)
		oidcAuth, err := newOIDCLogin(ctx, cfg.OIDCIssuerURL, cfg.OIDCClientID,
			cfg.OIDCClientSecret, cfg.OIDCRedirectURL, cfg.OIDCScopes, httpClient)
		if err != nil {
			return fmt.Errorf("init oidc login: %w", err)
		}
		auth = oidcAuth
	}

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
	if err != nil {
		return fmt.Errorf("connect mongo: %w", err)
	}

	store := newMongoDirectoryStore(mongoClient.Database(cfg.MongoDB))
	if err := store.EnsureIndexes(ctx); err != nil {
		return fmt.Errorf("ensure directory indexes: %w", err)
	}

	// Populate the directory cache in the background; /readyz stays
	// unavailable until the first successful load.
	cache := newDirectoryCache()
	refreshCtx, refreshCancel := context.WithCancel(ctx)
	defer refreshCancel()
	var refreshWG sync.WaitGroup
	refreshWG.Go(func() {
		cache.RefreshLoop(refreshCtx, store, cfg.CacheRefreshInterval, cacheRetryInterval)
	})

	slog.Info("directory config", "sites", len(sites), "refreshInterval", cfg.CacheRefreshInterval.String())

	handler := NewPortalHandler(cache, cfg.DevMode, cfg.DevFallbackSiteID, sites, auth, cfg.CookieSecure)
	if cfg.DevMode {
		slog.Info("dev mode enabled — unknown accounts fall back to the dev site")
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(ginutil.RequestID())
	r.Use(ginutil.AccessLog())
	r.Use(ginutil.CORS())
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
		shutdown.Wait(ctx, 25*time.Second, func(ctx context.Context) error {
			slog.Info("shutting down portal service")
			err := srv.Shutdown(ctx)
			refreshCancel()
			refreshWG.Wait()
			mongoutil.Disconnect(ctx, mongoClient)
			return err
		})
	}()

	err = <-srvErr
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen portal server: %w", err)
	}
	<-shutdownDone

	return nil
}
