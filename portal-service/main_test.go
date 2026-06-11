package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caarlos0/env/v11"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateConfig(t *testing.T) {
	base := config{
		DevMode:          false,
		OIDCIssuerURL:    "http://keycloak/realms/chatapp",
		OIDCAudiences:    []string{"nats-chat"},
		SiteAuthURLs:     map[string]string{"site-a": "http://auth-a:8080"},
		SiteNATSURLs:     map[string]string{"site-a": "wss://nats-a:9222"},
		SiteFrontendURLs: map[string]string{"site-a": "https://chat-a.example.com"},
	}

	t.Run("prod with oidc passes", func(t *testing.T) {
		assert.NoError(t, validateConfig(&base))
	})

	t.Run("prod without issuer fails", func(t *testing.T) {
		cfg := base
		cfg.OIDCIssuerURL = ""
		assert.Error(t, validateConfig(&cfg))
	})

	t.Run("prod without audiences fails", func(t *testing.T) {
		cfg := base
		cfg.OIDCAudiences = nil
		assert.Error(t, validateConfig(&cfg))
	})

	t.Run("dev mode skips oidc requirements", func(t *testing.T) {
		cfg := base
		cfg.DevMode = true
		assert.NoError(t, validateConfig(&cfg))
	})

	t.Run("empty site maps fail even in dev mode", func(t *testing.T) {
		cfg := config{DevMode: true}
		assert.Error(t, validateConfig(&cfg))
	})

	t.Run("all site maps present passes", func(t *testing.T) {
		cfg := base
		cfg.SiteAuthURLs = map[string]string{"site-a": "http://auth-a:8080"}
		cfg.SiteNATSURLs = map[string]string{"site-a": "wss://nats-a:9222"}
		cfg.SiteFrontendURLs = map[string]string{"site-a": "https://chat-a.example.com"}
		assert.NoError(t, validateConfig(&cfg))
	})
}

// Site maps use "=" between key and value because the URLs themselves contain ":".
func TestConfig_SiteMapParsing(t *testing.T) {
	t.Setenv("MONGO_URI", "mongodb://localhost:27017")
	t.Setenv("ADMIN_TOKEN", "tok")
	t.Setenv("SITE_AUTH_URLS", "site-a=http://auth-a:8080,site-b=http://auth-b:8080")
	t.Setenv("SITE_NATS_URLS", "site-a=wss://nats-a:9222")
	t.Setenv("SITE_FRONTEND_URLS", "site-a=https://chat-a.example.com")

	cfg, err := env.ParseAs[config]()
	require.NoError(t, err)
	assert.Equal(t, "http://auth-a:8080", cfg.SiteAuthURLs["site-a"])
	assert.Equal(t, "http://auth-b:8080", cfg.SiteAuthURLs["site-b"])
	assert.Equal(t, "wss://nats-a:9222", cfg.SiteNATSURLs["site-a"])
	assert.Equal(t, "https://chat-a.example.com", cfg.SiteFrontendURLs["site-a"])
	assert.Equal(t, "8080", cfg.Port)
}

func TestBodyLimitMiddleware_RejectsOversizedBody(t *testing.T) {
	p := testParams(t)
	h, err := NewPortalHandler(&p)
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(bodyLimitMiddleware())
	registerRoutes(r, h)

	big := strings.Repeat("x", maxBodyBytes+1)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session/nats-jwt",
		strings.NewReader(`{"ssoToken":"`+big+`","natsPublicKey":"U"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	// MaxBytesReader causes ShouldBindJSON to fail → handler's missing_fields 400 path fires.
	assert.Equal(t, http.StatusBadRequest, w.Code, "oversized body must be rejected, not buffered")
}
