package oidc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContainsAudience(t *testing.T) {
	cases := []struct {
		name      string
		tokenAud  []string
		allowed   []string
		wantMatch bool
	}{
		{"single token aud matches single allowed", []string{"a"}, []string{"a"}, true},
		{"token aud matches one of many allowed", []string{"b"}, []string{"a", "b", "c"}, true},
		{"one of many token auds matches allowed", []string{"x", "b"}, []string{"a", "b"}, true},
		{"no match", []string{"x"}, []string{"a", "b"}, false},
		{"empty token aud", nil, []string{"a"}, false},
		{"empty allowed", []string{"a"}, nil, false},
		{"both empty", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantMatch, containsAudience(tc.tokenAud, tc.allowed))
		})
	}
}

func TestNewValidator_RejectsEmptyAudiences(t *testing.T) {
	_, err := NewValidator(t.Context(), Config{
		IssuerURL: "http://example.invalid",
		Audiences: nil,
	})
	assert.ErrorIs(t, err, ErrNoAudiences)
}

func TestClaims_Account(t *testing.T) {
	tests := []struct {
		name   string
		claims Claims
		want   string
	}{
		{"preferred_username wins", Claims{PreferredUsername: "alice", Name: "Alice W"}, "alice"},
		{"name alone is not an account", Claims{Name: "Alice W"}, ""},
		{"both blank is blank", Claims{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.claims.Account())
		})
	}
}

func TestHTTPClient(t *testing.T) {
	require.Nil(t, HTTPClient(false))
	c := HTTPClient(true)
	require.NotNil(t, c)
	tr, ok := c.Transport.(*http.Transport)
	require.True(t, ok)
	assert.True(t, tr.TLSClientConfig.InsecureSkipVerify)
	assert.Equal(t, uint16(tls.VersionTLS12), tr.TLSClientConfig.MinVersion)
	assert.Equal(t, issuerDiscoveryTimeout, c.Timeout)
}

func TestDiscoverProvider(t *testing.T) {
	mux := http.NewServeMux()
	var issuer string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/auth",
			"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	issuer = ts.URL
	p, err := DiscoverProvider(context.Background(), issuer, ts.Client())
	require.NoError(t, err)
	assert.Equal(t, issuer+"/auth", p.Endpoint().AuthURL)
}
