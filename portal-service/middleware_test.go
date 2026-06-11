package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/ginutil"
)

func newMiddlewareRouter(allowed map[string]struct{}) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ginutil.RequestID())
	r.Use(corsMiddleware(allowed))
	r.POST("/probe", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"rid": c.GetString("request_id")}) })
	return r
}

func TestNormalizeOrigin(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"plain origin", "https://chat-a.example.com", "https://chat-a.example.com", false},
		{"uppercase host lowered", "HTTPS://Chat-A.Example.com", "https://chat-a.example.com", false},
		{"port kept", "http://localhost:5173", "http://localhost:5173", false},
		{"path stripped", "https://chat-a.example.com/rooms/1", "https://chat-a.example.com", false},
		{"trailing slash stripped", "https://chat-a.example.com/", "https://chat-a.example.com", false},
		{"whitespace trimmed", "  https://chat-a.example.com ", "https://chat-a.example.com", false},
		{"default https port stripped", "https://chat-a.example.com:443", "https://chat-a.example.com", false},
		{"default http port stripped", "http://chat-a.example.com:80", "http://chat-a.example.com", false},
		{"missing scheme rejected", "chat-a.example.com", "", true},
		{"empty rejected", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeOrigin(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCORSMiddleware(t *testing.T) {
	allowed := map[string]struct{}{"https://chat-a.example.com": {}}

	t.Run("allowed origin echoed", func(t *testing.T) {
		r := newMiddlewareRouter(allowed)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.Header.Set("Origin", "HTTPS://Chat-A.Example.com")
		r.ServeHTTP(w, req)
		assert.Equal(t, "https://chat-a.example.com", w.Header().Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "Origin", w.Header().Get("Vary"))
	})

	t.Run("unknown origin gets no CORS headers", func(t *testing.T) {
		r := newMiddlewareRouter(allowed)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		r.ServeHTTP(w, req)
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("preflight short-circuits with 204", func(t *testing.T) {
		r := newMiddlewareRouter(allowed)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/probe", nil)
		req.Header.Set("Origin", "https://chat-a.example.com")
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Equal(t, "https://chat-a.example.com", w.Header().Get("Access-Control-Allow-Origin"))
		assert.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "POST")
	})

	t.Run("unknown origin still gets Vary", func(t *testing.T) {
		r := newMiddlewareRouter(allowed)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		r.ServeHTTP(w, req)
		assert.Equal(t, "Origin", w.Header().Get("Vary"))
	})

	t.Run("disallowed-origin preflight gets 204 without CORS headers", func(t *testing.T) {
		r := newMiddlewareRouter(allowed)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/probe", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("malformed origin gets no CORS allow header", func(t *testing.T) {
		r := newMiddlewareRouter(allowed)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.Header.Set("Origin", "not a url")
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	})
}
