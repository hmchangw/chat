package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestHandler_Healthz(t *testing.T) {
	h := newHandler(nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	c, _ := gin.CreateTestContext(w)
	c.Request = req
	h.healthz(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var got map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	assert.Equal(t, "ok", got["status"])
}

func TestRequestIDMiddleware_GeneratesID(t *testing.T) {
	h := newHandler(nil)

	r := gin.New()
	r.Use(requestIDMiddleware())
	r.GET("/healthz", h.healthz)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, w.Header().Get("X-Request-ID"), "response should have X-Request-ID header")
}

func TestRequestIDMiddleware_EchoesExistingID(t *testing.T) {
	h := newHandler(nil)

	r := gin.New()
	r.Use(requestIDMiddleware())
	r.GET("/healthz", h.healthz)

	const reqID = "my-existing-request-id"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", reqID)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, reqID, w.Header().Get("X-Request-ID"))
}

func TestStubRoutes_Return501(t *testing.T) {
	h := newHandler(nil)
	r := gin.New()
	registerRoutes(r, h)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/connect"},
		{http.MethodGet, "/api/connections"},
		{http.MethodDelete, "/api/connections/abc"},
		{http.MethodGet, "/api/mongo/abc/collections"},
		{http.MethodGet, "/api/mongo/abc/collections/rooms/docs"},
		{http.MethodPost, "/api/mongo/abc/collections/rooms/docs"},
		{http.MethodPut, "/api/mongo/abc/collections/rooms/docs/123"},
		{http.MethodDelete, "/api/mongo/abc/collections/rooms/docs/123"},
		{http.MethodGet, "/api/mongo/abc/collections/rooms/export"},
		{http.MethodPost, "/api/mongo/abc/collections/rooms/import"},
		{http.MethodGet, "/api/cassandra/abc/tables"},
		{http.MethodGet, "/api/cassandra/abc/tables/messages/rows"},
		{http.MethodPost, "/api/cassandra/abc/tables/messages/rows"},
		{http.MethodPut, "/api/cassandra/abc/tables/messages/rows"},
		{http.MethodDelete, "/api/cassandra/abc/tables/messages/rows"},
		{http.MethodGet, "/api/cassandra/abc/tables/messages/export"},
		{http.MethodPost, "/api/cassandra/abc/tables/messages/import"},
		{http.MethodGet, "/api/templates"},
		{http.MethodGet, "/api/templates/room"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			r.ServeHTTP(w, req)
			assert.Equal(t, http.StatusNotImplemented, w.Code)
		})
	}
}

func TestServeUI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newHandler(nil)
	r := gin.New()
	registerRoutes(r, h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
}

func TestServeUI_MissingFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newHandler(nil)
	// Use an empty FS so ReadFile("static/index.html") returns an error.
	h.staticFS = fstest.MapFS{}
	r := gin.New()
	registerRoutes(r, h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/unknown-path", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		input    string
		expected slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelInfo},
		{"invalid", slog.LevelInfo},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.expected, parseLogLevel(tc.input))
		})
	}
}

func TestNewHTTPServer(t *testing.T) {
	h := newHandler(nil)
	r := gin.New()
	registerRoutes(r, h)

	srv := newHTTPServer(8091, r)
	require.NotNil(t, srv)
	assert.Equal(t, ":8091", srv.Addr)
	assert.NotZero(t, srv.ReadTimeout)
	assert.NotZero(t, srv.WriteTimeout)
	assert.NotZero(t, srv.IdleTimeout)
}

func TestNewRouter(t *testing.T) {
	h := newHandler(nil)
	r := newRouter(h)
	require.NotNil(t, r)

	// Verify the router serves health check correctly.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, w.Header().Get("X-Request-ID"), "requestIDMiddleware should be wired")
}

func TestReaperShutdownFunc(t *testing.T) {
	t.Run("nil cancel is a no-op", func(t *testing.T) {
		var cancel context.CancelFunc
		fn := reaperShutdownFunc(&cancel)
		assert.NoError(t, fn(context.Background()))
	})

	t.Run("non-nil cancel is invoked", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		fn := reaperShutdownFunc(&cancel)
		assert.NoError(t, fn(context.Background()))
		// After the shutdown hook fires, the context should be cancelled.
		select {
		case <-ctx.Done():
		default:
			t.Fatal("expected context to be cancelled")
		}
	})
}

// Ensure the io/fs import is used (staticFS field type).
var _ fs.FS = fstest.MapFS{}
