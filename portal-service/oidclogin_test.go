package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestOIDCLogin_AuthCodeURL(t *testing.T) {
	o := &oidcLogin{
		oauth: oauth2.Config{
			ClientID:    "portal",
			RedirectURL: "http://localhost:8081/auth/callback",
			Endpoint:    oauth2.Endpoint{AuthURL: "http://kc/realms/chatapp/protocol/openid-connect/auth"},
			Scopes:      []string{"openid", "profile", "email"},
		},
	}

	raw := o.AuthCodeURL("state123", "nonce456")

	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "state123", q.Get("state"))
	assert.Equal(t, "nonce456", q.Get("nonce"))
	assert.Equal(t, "portal", q.Get("client_id"))
	assert.Equal(t, "http://localhost:8081/auth/callback", q.Get("redirect_uri"))
	assert.True(t, strings.Contains(q.Get("scope"), "openid"))
}

func signIDToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT"))
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	jws, err := sig.Sign(payload)
	require.NoError(t, err)
	s, err := jws.CompactSerialize()
	require.NoError(t, err)
	return s
}

func TestOIDCLogin_ExchangeAndVerify(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	const issuer = "https://kc.test/realms/chatapp"
	const clientID = "portal"

	idToken := signIDToken(t, key, map[string]any{
		"iss": issuer, "aud": clientID, "sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"nonce": "nonce456", "preferred_username": "alice",
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "id_token": idToken,
		})
	}))
	defer ts.Close()

	o := &oidcLogin{
		oauth: oauth2.Config{
			ClientID: clientID, ClientSecret: "secret",
			Endpoint: oauth2.Endpoint{TokenURL: ts.URL},
		},
		verifier: oidc.NewVerifier(issuer,
			&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{key.Public()}},
			&oidc.Config{ClientID: clientID}),
	}

	account, err := o.ExchangeAndVerify(context.Background(), "code", "nonce456")
	require.NoError(t, err)
	assert.Equal(t, "alice", account)

	_, err = o.ExchangeAndVerify(context.Background(), "code", "WRONG_NONCE")
	assert.ErrorContains(t, err, "nonce mismatch")
}

// recordingTransport flags whether it round-tripped any request, letting a test
// assert the configured httpClient actually carried through a call.
type recordingTransport struct {
	used *bool
	base http.RoundTripper
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*rt.used = true
	return rt.base.RoundTrip(r)
}

func TestOIDCLogin_ExchangeAndVerify_UsesHTTPClient(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	const issuer = "https://kc.test/realms/chatapp"
	const clientID = "portal"

	idToken := signIDToken(t, key, map[string]any{
		"iss": issuer, "aud": clientID, "sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"nonce": "nonce456", "preferred_username": "alice",
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "id_token": idToken,
		})
	}))
	defer ts.Close()

	used := false
	o := &oidcLogin{
		oauth: oauth2.Config{
			ClientID: clientID, ClientSecret: "secret",
			Endpoint: oauth2.Endpoint{TokenURL: ts.URL},
		},
		verifier: oidc.NewVerifier(issuer,
			&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{key.Public()}},
			&oidc.Config{ClientID: clientID}),
		httpClient: &http.Client{Transport: &recordingTransport{used: &used, base: http.DefaultTransport}},
	}

	account, err := o.ExchangeAndVerify(context.Background(), "code", "nonce456")
	require.NoError(t, err)
	assert.Equal(t, "alice", account)
	assert.True(t, used, "configured httpClient must carry through the token exchange")
}

func TestOIDCLogin_ExchangeAndVerify_ErrorBranches(t *testing.T) {
	const issuer = "https://kc.test/realms/chatapp"
	const clientID = "portal"

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	makeVerifier := func(pubKey crypto.PublicKey) *oidc.IDTokenVerifier {
		return oidc.NewVerifier(issuer,
			&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{pubKey}},
			&oidc.Config{ClientID: clientID})
	}

	tests := []struct {
		name        string
		handler     http.HandlerFunc
		verifier    *oidc.IDTokenVerifier
		nonce       string
		errContains string
	}{
		{
			// (A) token endpoint returns HTTP 400
			name: "exchange failure",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "bad", http.StatusBadRequest)
			},
			verifier:    makeVerifier(key.Public()),
			nonce:       "n",
			errContains: "exchange auth code",
		},
		{
			// (B) 200 response but no id_token field
			name: "missing id_token",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": "at", "token_type": "Bearer",
				})
			},
			verifier:    makeVerifier(key.Public()),
			nonce:       "n",
			errContains: "no id_token",
		},
		{
			// (C) id_token signed with wrongKey, verifier has key's public key
			name: "verify failure",
			handler: func(w http.ResponseWriter, r *http.Request) {
				tok := signIDToken(t, wrongKey, map[string]any{
					"iss": issuer, "aud": clientID, "sub": "u1",
					"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
					"nonce": "n", "preferred_username": "alice",
				})
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": "at", "token_type": "Bearer", "id_token": tok,
				})
			},
			verifier:    makeVerifier(key.Public()),
			nonce:       "n",
			errContains: "verify id token",
		},
		{
			// (E) valid token but preferred_username is empty
			name: "blank preferred_username",
			handler: func(w http.ResponseWriter, r *http.Request) {
				tok := signIDToken(t, key, map[string]any{
					"iss": issuer, "aud": clientID, "sub": "u1",
					"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
					"nonce": "n",
				})
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": "at", "token_type": "Bearer", "id_token": tok,
				})
			},
			verifier:    makeVerifier(key.Public()),
			nonce:       "n",
			errContains: "blank preferred_username",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(tc.handler)
			defer ts.Close()

			o := &oidcLogin{
				oauth: oauth2.Config{
					ClientID: clientID, ClientSecret: "secret",
					Endpoint: oauth2.Endpoint{TokenURL: ts.URL},
				},
				verifier: tc.verifier,
			}

			_, err := o.ExchangeAndVerify(context.Background(), "code", tc.nonce)
			assert.ErrorContains(t, err, tc.errContains)
		})
	}
}

func TestNewOIDCLogin(t *testing.T) {
	mux := http.NewServeMux()
	var issuer string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/auth",
			"token_endpoint":         issuer + "/token",
			"jwks_uri":               issuer + "/keys",
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	issuer = ts.URL

	o, err := newOIDCLogin(context.Background(), issuer, "portal", "secret",
		"http://localhost:8081/auth/callback", []string{"openid"}, ts.Client())
	require.NoError(t, err)
	require.NotNil(t, o)
	assert.Equal(t, "portal", o.oauth.ClientID)
	assert.Equal(t, issuer+"/auth", o.oauth.Endpoint.AuthURL)
	assert.Equal(t, "http://localhost:8081/auth/callback", o.oauth.RedirectURL)
}
