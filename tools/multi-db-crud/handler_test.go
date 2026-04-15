package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
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
	r := newRouter(h, false)
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

// --- Task 3: connection handler tests ------------------------------------------------

// newTestRouter builds a Gin engine with a handler that uses the supplied mock
// registry, registering the real routes. It mirrors newRouter but skips the
// read-only middleware (those have dedicated tests below).
func newTestRouter(t *testing.T, reg registryAPI) *gin.Engine {
	t.Helper()
	h := newHandler(reg)
	r := gin.New()
	registerRoutes(r, h)
	return r
}

func decodeErrResp(t *testing.T, body *bytes.Buffer) errResp {
	t.Helper()
	var e errResp
	require.NoError(t, json.NewDecoder(body).Decode(&e))
	return e
}

func TestConnect_Success_Mongo(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)

	want := connInfo{
		ID:            "abc-123",
		Kind:          "mongo",
		Label:         "primary",
		CreatedAt:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		ServerVersion: "7.0.0",
	}
	reg.EXPECT().
		Connect(gomock.Any(), connectSpec{Kind: "mongo", URI: "mongodb://x", Label: "primary"}).
		Return(want, nil)

	r := newTestRouter(t, reg)

	body := `{"kind":"mongo","uri":"mongodb://x","label":"primary"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/connect", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var got connInfo
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	assert.Equal(t, want, got)
}

func TestConnect_Success_Cassandra(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)

	want := connInfo{
		ID:            "cass-7",
		Kind:          "cassandra",
		Label:         "ring",
		Keyspace:      "chat",
		CreatedAt:     time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
		ServerVersion: "4.1.0",
	}
	reg.EXPECT().
		Connect(gomock.Any(), connectSpec{Kind: "cassandra", URI: "h1,h2", Keyspace: "chat", Label: "ring"}).
		Return(want, nil)

	r := newTestRouter(t, reg)

	body := `{"kind":"cassandra","uri":"h1,h2","keyspace":"chat","label":"ring"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/connect", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var got connInfo
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	assert.Equal(t, want, got)
}

func TestConnect_BadJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	// Connect must never be called for malformed bodies.
	reg.EXPECT().Connect(gomock.Any(), gomock.Any()).Times(0)

	r := newTestRouter(t, reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/connect", strings.NewReader("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "bad_request", decodeErrResp(t, w.Body).Code)
}

func TestConnect_InvalidBodies(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"MissingKind", `{"kind":"","uri":"x","label":"l"}`},
		{"InvalidKind", `{"kind":"redis","uri":"x","label":"l"}`},
		{"MissingLabel", `{"kind":"mongo","uri":"x","label":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			reg := NewMockregistryAPI(ctrl)
			reg.EXPECT().Connect(gomock.Any(), gomock.Any()).Times(0)

			r := newTestRouter(t, reg)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/connect", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Equal(t, "bad_request", decodeErrResp(t, w.Body).Code)
		})
	}
}

func TestConnect_CassandraMissingKeyspace(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	reg.EXPECT().Connect(gomock.Any(), gomock.Any()).Times(0)

	r := newTestRouter(t, reg)

	body := `{"kind":"cassandra","uri":"h1","label":"ring"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/connect", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	e := decodeErrResp(t, w.Body)
	assert.Equal(t, "bad_request", e.Code)
	assert.Contains(t, e.Error, "keyspace")
}

func TestConnect_UnknownKind_FromRegistry(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	reg.EXPECT().
		Connect(gomock.Any(), gomock.Any()).
		Return(connInfo{}, ErrUnknownKind)

	r := newTestRouter(t, reg)

	// Body passes the validator (mongo), but the registry defensively returns ErrUnknownKind.
	body := `{"kind":"mongo","uri":"x","label":"l"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/connect", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "unknown_kind", decodeErrResp(t, w.Body).Code)
}

func TestConnect_RegistryError_SanitizedResponse(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)

	driverErr := fmt.Errorf("could not connect to mongo: tcp: lookup nonexistent.host: no such host")
	reg.EXPECT().
		Connect(gomock.Any(), gomock.Any()).
		Return(connInfo{}, driverErr)

	r := newTestRouter(t, reg)

	body := `{"kind":"mongo","uri":"mongodb://nonexistent","label":"x"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/connect", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
	e := decodeErrResp(t, w.Body)
	assert.Equal(t, "connect_failed", e.Code)
	assert.Equal(t, "could not connect to mongo", e.Error)
	// Make sure sensitive fragments did NOT leak.
	assert.NotContains(t, e.Error, "tcp")
	assert.NotContains(t, e.Error, "lookup")
	assert.NotContains(t, e.Error, "nonexistent.host")
}

func TestListConnections_Empty(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	reg.EXPECT().List().Return(nil)

	r := newTestRouter(t, reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/connections", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "[]", strings.TrimSpace(w.Body.String()))
}

func TestListConnections_Populated(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := []connInfo{
		{ID: "a", Kind: "mongo", Label: "one", CreatedAt: t0},
		{ID: "b", Kind: "mongo", Label: "two", CreatedAt: t0.Add(time.Minute)},
		{ID: "c", Kind: "cassandra", Label: "three", Keyspace: "chat", CreatedAt: t0.Add(2 * time.Minute)},
	}
	reg.EXPECT().List().Return(entries)

	r := newTestRouter(t, reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/connections", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var got []connInfo
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	assert.Equal(t, entries, got)
}

func TestDeleteConnection_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	reg.EXPECT().Close("abc").Return(nil)

	r := newTestRouter(t, reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/connections/abc", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())
}

func TestDeleteConnection_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	reg.EXPECT().Close("missing").Return(ErrNotFound)

	r := newTestRouter(t, reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/connections/missing", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "not_found", decodeErrResp(t, w.Body).Code)
}

func TestDeleteConnection_InternalError(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	reg.EXPECT().Close("boom").Return(errors.New("shutdown: broken pipe: connection reset"))

	r := newTestRouter(t, reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/connections/boom", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	e := decodeErrResp(t, w.Body)
	assert.Equal(t, "internal_error", e.Code)
	// Sanitized message should NOT expose the driver-level fragments.
	assert.Equal(t, "shutdown", e.Error)
	assert.NotContains(t, e.Error, "broken pipe")
}

func TestSanitizeConnectError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want string
	}{
		{"with colon", errors.New("could not connect to mongo: detail: more"), "could not connect to mongo"},
		{"no colon", errors.New("opaque failure"), "opaque failure"},
		{"leading whitespace", errors.New("  prefix  : rest"), "prefix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeConnectError(tc.in))
		})
	}
}

// --- Read-only middleware tests -------------------------------------------------------

// newReadOnlyEngine builds a trivial Gin engine guarded by readOnlyMiddleware(ro).
// The inner handler increments `calls` so tests can assert short-circuiting.
func newReadOnlyEngine(ro bool, calls *int) *gin.Engine {
	r := gin.New()
	r.Use(readOnlyMiddleware(ro))
	pass := func(c *gin.Context) {
		*calls++
		c.Status(http.StatusOK)
	}
	r.GET("/x", pass)
	r.POST("/x", pass)
	r.PUT("/x", pass)
	r.DELETE("/x", pass)
	return r
}

func TestReadOnly_BlocksPost(t *testing.T) {
	calls := 0
	r := newReadOnlyEngine(true, &calls)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "read_only", decodeErrResp(t, w.Body).Code)
	assert.Equal(t, 0, calls, "downstream handler must not be invoked")
}

func TestReadOnly_BlocksDelete(t *testing.T) {
	calls := 0
	r := newReadOnlyEngine(true, &calls)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/x", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "read_only", decodeErrResp(t, w.Body).Code)
	assert.Equal(t, 0, calls)
}

func TestReadOnly_BlocksPut(t *testing.T) {
	calls := 0
	r := newReadOnlyEngine(true, &calls)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/x", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "read_only", decodeErrResp(t, w.Body).Code)
	assert.Equal(t, 0, calls)
}

func TestReadOnly_AllowsGet(t *testing.T) {
	calls := 0
	r := newReadOnlyEngine(true, &calls)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, calls)
}

func TestReadOnly_Disabled_AllowsAll(t *testing.T) {
	calls := 0
	r := newReadOnlyEngine(false, &calls)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, calls)
}
