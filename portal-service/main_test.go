package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireOIDCConfig(t *testing.T) {
	full := config{
		OIDCIssuerURL: "https://kc/realms/chatapp", OIDCClientID: "portal",
		OIDCClientSecret: "s", OIDCRedirectURL: "http://localhost:8081/auth/callback",
	}

	t.Run("dev mode skips", func(t *testing.T) {
		cfg := config{DevMode: true}
		require.NoError(t, requireOIDCConfig(&cfg))
	})
	t.Run("prod full passes", func(t *testing.T) {
		require.NoError(t, requireOIDCConfig(&full))
	})
	t.Run("prod missing issuer fails", func(t *testing.T) {
		cfg := full
		cfg.OIDCIssuerURL = ""
		err := requireOIDCConfig(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OIDC_ISSUER_URL")
	})
	t.Run("prod missing clientID fails", func(t *testing.T) {
		cfg := full
		cfg.OIDCClientID = ""
		err := requireOIDCConfig(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OIDC_CLIENT_ID")
	})
	t.Run("prod missing secret fails", func(t *testing.T) {
		cfg := full
		cfg.OIDCClientSecret = ""
		err := requireOIDCConfig(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OIDC_CLIENT_SECRET")
	})
	t.Run("prod missing redirect fails", func(t *testing.T) {
		cfg := full
		cfg.OIDCRedirectURL = ""
		err := requireOIDCConfig(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OIDC_REDIRECT_URL")
	})
	t.Run("prod multiple missing lists all four", func(t *testing.T) {
		cfg := config{}
		err := requireOIDCConfig(&cfg)
		require.Error(t, err)
		for _, k := range []string{"OIDC_ISSUER_URL", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_REDIRECT_URL"} {
			assert.Contains(t, err.Error(), k)
		}
	})
}
