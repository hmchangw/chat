package restyutil

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_DefaultTimeout(t *testing.T) {
	c := New("http://example")
	assert.Equal(t, defaultTimeout, c.GetClient().Timeout)
}

func TestNew_BaseURL(t *testing.T) {
	c := New("http://example.test")
	assert.Equal(t, "http://example.test", c.BaseURL)
}

func TestWithTimeout_Override(t *testing.T) {
	c := New("http://example", WithTimeout(2*time.Second))
	assert.Equal(t, 2*time.Second, c.GetClient().Timeout)
}

func TestWithHeader(t *testing.T) {
	c := New("http://example", WithHeader("X-App", "loadgen"))
	assert.Equal(t, "loadgen", c.Header.Get("X-App"))
}

func TestWithBearerToken(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	c := New(srv.URL, WithBearerToken("abc123"))
	_, err := c.R().Get("/")
	require.NoError(t, err)
	assert.Equal(t, "Bearer abc123", got)
}

func TestWithRetries(t *testing.T) {
	c := New("http://example", WithRetries(3, 10*time.Millisecond, 100*time.Millisecond))
	assert.Equal(t, 3, c.RetryCount)
	assert.Equal(t, 10*time.Millisecond, c.RetryWaitTime)
	assert.Equal(t, 100*time.Millisecond, c.RetryMaxWaitTime)
}

// End-to-end: real httptest server, GET, JSON decode into a struct.
func TestNew_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/ping", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"msg":"pong"}`))
	}))
	defer srv.Close()

	type result struct {
		OK  bool   `json:"ok"`
		Msg string `json:"msg"`
	}
	c := New(srv.URL)
	var got result
	resp, err := c.R().SetResult(&got).Get("/ping")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode())
	assert.True(t, got.OK)
	assert.Equal(t, "pong", got.Msg)
}

// Verify retries actually fire on 5xx responses when WithRetries is set.
func TestWithRetries_FiresOn5xx(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, WithRetries(2, time.Millisecond, 5*time.Millisecond)).
		AddRetryCondition(func(r *resty.Response, _ error) bool {
			return r.StatusCode() >= 500
		})

	resp, err := c.R().Get("/")
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode())
	assert.Equal(t, 3, hits, "1 initial + 2 retries")
}
