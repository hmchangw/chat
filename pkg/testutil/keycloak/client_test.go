package keycloak

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-resty/resty/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_PasswordGrant_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/realms/chatapp/protocol/openid-connect/token", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		form, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		assert.Equal(t, "password", form.Get("grant_type"))
		assert.Equal(t, "nats-chat", form.Get("client_id"))
		assert.Equal(t, "alice", form.Get("username"))
		assert.Equal(t, "password", form.Get("password"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
            "access_token":       "access-abc",
            "refresh_token":      "refresh-xyz",
            "expires_in":         300,
            "refresh_expires_in": 1800,
            "token_type":         "Bearer"
        }`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "chatapp")
	tok, err := c.PasswordGrant(t.Context(), "nats-chat", "alice", "password")
	require.NoError(t, err)
	assert.Equal(t, "access-abc", tok.AccessToken)
	assert.Equal(t, "refresh-xyz", tok.RefreshToken)
	assert.Equal(t, 300, tok.ExpiresIn)
	assert.Equal(t, 1800, tok.RefreshExpiresIn)
	assert.Equal(t, "Bearer", tok.TokenType)
}

func TestClient_PasswordGrant_InvalidCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid user credentials"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "chatapp")
	_, err := c.PasswordGrant(t.Context(), "nats-chat", "alice", "wrong")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidCredentials), "expected ErrInvalidCredentials, got %v", err)
}

func TestClient_PasswordGrant_UnknownClient(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		oauthErr string
	}{
		{"401 unauthorized_client", http.StatusUnauthorized, "unauthorized_client"},
		{"400 invalid_client", http.StatusBadRequest, "invalid_client"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"` + tc.oauthErr + `"}`))
			}))
			t.Cleanup(srv.Close)

			c := NewClient(srv.URL, "chatapp")
			_, err := c.PasswordGrant(t.Context(), "bogus", "alice", "password")
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrUnknownClient), "expected ErrUnknownClient, got %v", err)
		})
	}
}

func TestClient_PasswordGrant_RealmNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`Realm does not exist`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "missing")
	_, err := c.PasswordGrant(t.Context(), "nats-chat", "alice", "password")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRealmNotFound), "expected ErrRealmNotFound, got %v", err)
}

func TestClient_PasswordGrant_GenericError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server_error","error_description":"boom"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "chatapp")
	_, err := c.PasswordGrant(t.Context(), "nats-chat", "alice", "password")
	require.Error(t, err)
	var kerr *Error
	require.ErrorAs(t, err, &kerr)
	assert.Equal(t, http.StatusInternalServerError, kerr.StatusCode)
	assert.Equal(t, "server_error", kerr.OAuthError)
	assert.Equal(t, "boom", kerr.Description)
	// Generic errors should not be classified as one of the sentinel categories.
	assert.False(t, errors.Is(err, ErrInvalidCredentials))
	assert.False(t, errors.Is(err, ErrUnknownClient))
	assert.False(t, errors.Is(err, ErrRealmNotFound))
}

func TestClient_AccessToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"only-this","token_type":"Bearer","expires_in":60}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "chatapp")
	tok, err := c.AccessToken(t.Context(), "nats-chat", "alice", "password")
	require.NoError(t, err)
	assert.Equal(t, "only-this", tok)
}

func TestClient_Refresh_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		form, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		assert.Equal(t, "refresh_token", form.Get("grant_type"))
		assert.Equal(t, "nats-chat", form.Get("client_id"))
		assert.Equal(t, "old-refresh", form.Get("refresh_token"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":300,"token_type":"Bearer"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "chatapp")
	tok, err := c.Refresh(t.Context(), "nats-chat", "old-refresh")
	require.NoError(t, err)
	assert.Equal(t, "new-access", tok.AccessToken)
	assert.Equal(t, "new-refresh", tok.RefreshToken)
}

func TestClient_PasswordGrant_ContextCanceled(t *testing.T) {
	// Server that hangs forever — exercises ctx cancellation.
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // pre-canceled

	c := NewClient(srv.URL, "chatapp")
	_, err := c.PasswordGrant(ctx, "nats-chat", "alice", "password")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "expected context.Canceled, got %v", err)
}

func TestError_Error(t *testing.T) {
	cases := []struct {
		name string
		err  Error
		want string
	}{
		{"with description", Error{StatusCode: 401, OAuthError: "invalid_grant", Description: "bad creds"}, "keycloak: 401 invalid_grant: bad creds"},
		{"oauth error only", Error{StatusCode: 401, OAuthError: "invalid_grant"}, "keycloak: 401 invalid_grant"},
		{"status only", Error{StatusCode: 500}, "keycloak: 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.err.Error())
		})
	}
}

func TestError_Is_UnknownTarget(t *testing.T) {
	// `Is` returns false for any sentinel it doesn't recognize — exercises the
	// default branch that the switch falls through.
	e := &Error{StatusCode: 500, OAuthError: "server_error"}
	assert.False(t, errors.Is(e, errors.New("other")))
}

func TestWithHTTPClient_Override(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "yes", r.Header.Get("X-Custom"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"x","token_type":"Bearer"}`))
	}))
	t.Cleanup(srv.Close)

	custom := resty.New().SetBaseURL(srv.URL).SetHeader("X-Custom", "yes")
	c := NewClient(srv.URL, "chatapp", WithHTTPClient(custom))
	_, err := c.PasswordGrant(t.Context(), "nats-chat", "alice", "password")
	require.NoError(t, err)
}

func TestAccessToken_PropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "chatapp")
	tok, err := c.AccessToken(t.Context(), "nats-chat", "alice", "wrong")
	require.Error(t, err)
	assert.Empty(t, tok)
	assert.True(t, errors.Is(err, ErrInvalidCredentials))
}

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	// Trailing slash on baseURL must not produce a double slash in the request path —
	// some Keycloak proxies treat // as a different route.
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"x","token_type":"Bearer"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(strings.TrimRight(srv.URL, "/")+"/", "chatapp")
	_, err := c.PasswordGrant(t.Context(), "nats-chat", "alice", "password")
	require.NoError(t, err)
	assert.Equal(t, "/realms/chatapp/protocol/openid-connect/token", got)
}
