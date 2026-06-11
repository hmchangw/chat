package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestRestyForwarder_PostsBodyAndPropagatesRequestID(t *testing.T) {
	var gotPath, gotContentType, gotRequestID string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotRequestID = r.Header.Get(natsutil.RequestIDHeader)
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"natsJwt":"jwt-x","user":{"account":"alice"}}`))
	}))
	t.Cleanup(upstream.Close)

	ctx := natsutil.WithRequestID(context.Background(), "01970a4f-8c2d-7c9a-abcd-e0123456789f")
	f := newRestyForwarder(2*time.Second, false)

	status, body, err := f.Forward(ctx, upstream.URL, []byte(`{"ssoToken":"tok","natsPublicKey":"U..."}`))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{"natsJwt":"jwt-x","user":{"account":"alice"}}`, string(body))
	assert.Equal(t, "/auth", gotPath)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, "01970a4f-8c2d-7c9a-abcd-e0123456789f", gotRequestID)
	assert.JSONEq(t, `{"ssoToken":"tok","natsPublicKey":"U..."}`, string(gotBody))
}

func TestRestyForwarder_TrimsTrailingSlash(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)

	f := newRestyForwarder(2*time.Second, false)
	_, _, err := f.Forward(context.Background(), upstream.URL+"/", []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "/auth", gotPath)
}

func TestRestyForwarder_ReturnsUpstreamErrorStatusAndBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid SSO token","code":"unauthenticated","reason":"invalid_sso_token"}`))
	}))
	t.Cleanup(upstream.Close)

	f := newRestyForwarder(2*time.Second, false)
	status, body, err := f.Forward(context.Background(), upstream.URL, []byte(`{}`))
	require.NoError(t, err) // non-2xx is NOT a transport error
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Contains(t, string(body), "invalid_sso_token")
}

func TestRestyForwarder_TransportErrorIsWrapped(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	upstream.Close() // dead endpoint

	f := newRestyForwarder(500*time.Millisecond, false)
	_, _, err := f.Forward(context.Background(), upstream.URL, []byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "post to auth-service")
}

func TestRestyForwarder_TLSSkipVerify(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)

	t.Run("skip=true accepts self-signed cert", func(t *testing.T) {
		f := newRestyForwarder(2*time.Second, true)
		status, _, err := f.Forward(context.Background(), upstream.URL, []byte(`{}`))
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, status)
	})

	t.Run("skip=false rejects self-signed cert", func(t *testing.T) {
		f := newRestyForwarder(2*time.Second, false)
		_, _, err := f.Forward(context.Background(), upstream.URL, []byte(`{}`))
		require.Error(t, err)
	})
}
