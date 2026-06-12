package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errtest"
)

func setupRouter(t *testing.T, h *PortalHandler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerRoutes(r, h)
	return r
}

// getUserInfo issues GET /api/userInfo with account as a query parameter. An
// empty account is sent with no query parameter at all (the missing-param case).
func getUserInfo(t *testing.T, r *gin.Engine, account string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/userInfo"
	if account != "" {
		target += "?account=" + url.QueryEscape(account)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func getPath(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// cacheWith returns a directory cache populated with the given entries.
func cacheWith(emps ...employee) *directoryCache {
	c := newDirectoryCache()
	if err := c.replace(emps); err != nil {
		panic(err)
	}
	return c
}

// newTestHandler builds a PortalHandler with the standard test auth-URL
// template and the local dev-fallback coordinates.
func newTestHandler(cache *directoryCache, devMode bool) *PortalHandler {
	return NewPortalHandler(cache, devMode, "site-local", "ws://localhost:9222", "https://auth.{siteId}.example.com")
}

func TestHandleUserInfo_HappyPath(t *testing.T) {
	h := newTestHandler(cacheWith(aliceEmployee), false)
	w := getUserInfo(t, setupRouter(t, h), "alice")

	require.Equal(t, http.StatusOK, w.Code)
	var resp userInfoResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, userInfoResponse{
		Account:        "alice",
		EmployeeID:     "E001",
		AuthServiceURL: "https://auth.site-a.example.com",
		NATSURL:        "wss://nats-3.site-a.example.com",
		SiteID:         "site-a",
	}, resp)
}

func TestHandleUserInfo_AuthURLTemplate(t *testing.T) {
	t.Run("placeholder replaced with the employee siteId", func(t *testing.T) {
		h := newTestHandler(cacheWith(bobEmployee), false)
		w := getUserInfo(t, setupRouter(t, h), "bob")
		require.Equal(t, http.StatusOK, w.Code)
		var resp userInfoResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "https://auth.site-b.example.com", resp.AuthServiceURL)
	})

	t.Run("template without placeholder is used verbatim", func(t *testing.T) {
		// Single-site deployments configure a fixed URL.
		h := NewPortalHandler(cacheWith(aliceEmployee), false, "site-local", "ws://localhost:9222", "http://localhost:8080")
		w := getUserInfo(t, setupRouter(t, h), "alice")
		require.Equal(t, http.StatusOK, w.Code)
		var resp userInfoResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "http://localhost:8080", resp.AuthServiceURL)
	})
}

func TestHandleUserInfo_InvalidAccountFormat(t *testing.T) {
	// Same token invariant auth-service enforces at minting — refusing here
	// keeps the portal from blessing an account the next step must reject.
	for _, tt := range []struct{ name, account string }{
		{"dotted account refused", "john.doe"},
		{"wildcard account refused", "mal*ory"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(cacheWith(aliceEmployee), false)
			w := getUserInfo(t, setupRouter(t, h), tt.account)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeBadRequest)
		})
	}
}

func TestHandleUserInfo_MissingAccount(t *testing.T) {
	h := newTestHandler(cacheWith(aliceEmployee), false)
	w := getUserInfo(t, setupRouter(t, h), "")

	assert.Equal(t, http.StatusBadRequest, w.Code)
	errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeBadRequest)
	errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthMissingFields)
}

func TestHandleUserInfo_AccountNotReady(t *testing.T) {
	t.Run("account absent from a populated cache", func(t *testing.T) {
		h := newTestHandler(cacheWith(aliceEmployee), false)
		w := getUserInfo(t, setupRouter(t, h), "mallory")

		assert.Equal(t, http.StatusForbidden, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeForbidden)
		errtest.AssertReason(t, w.Body.Bytes(), errcode.PortalAccountNotReady)
	})

	t.Run("cache not yet loaded", func(t *testing.T) {
		h := newTestHandler(newDirectoryCache(), false)
		w := getUserInfo(t, setupRouter(t, h), "alice")

		assert.Equal(t, http.StatusForbidden, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeForbidden)
		errtest.AssertReason(t, w.Body.Bytes(), errcode.PortalAccountNotReady)
	})
}

func TestHandleUserInfo_DevMode(t *testing.T) {
	t.Run("known account resolves normally", func(t *testing.T) {
		h := newTestHandler(cacheWith(aliceEmployee), true)
		w := getUserInfo(t, setupRouter(t, h), "alice")
		require.Equal(t, http.StatusOK, w.Code)
		var resp userInfoResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "site-a", resp.SiteID)
	})

	t.Run("unknown account falls back to the dev site", func(t *testing.T) {
		h := newTestHandler(cacheWith(aliceEmployee), true)
		w := getUserInfo(t, setupRouter(t, h), "newdev")
		require.Equal(t, http.StatusOK, w.Code)
		var resp userInfoResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, userInfoResponse{
			Account: "newdev", EmployeeID: "",
			AuthServiceURL: "https://auth.site-local.example.com", NATSURL: "ws://localhost:9222",
			SiteID: "site-local",
		}, resp)
	})

	t.Run("fallback works before the cache is loaded", func(t *testing.T) {
		h := newTestHandler(newDirectoryCache(), true)
		w := getUserInfo(t, setupRouter(t, h), "newdev")
		require.Equal(t, http.StatusOK, w.Code)
		var resp userInfoResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "site-local", resp.SiteID)
	})

	t.Run("bad account format is refused even in dev mode", func(t *testing.T) {
		h := newTestHandler(cacheWith(aliceEmployee), true)
		w := getUserInfo(t, setupRouter(t, h), "mal*ory")
		assert.Equal(t, http.StatusBadRequest, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeBadRequest)
	})

	t.Run("missing account is bad request", func(t *testing.T) {
		h := newTestHandler(cacheWith(aliceEmployee), true)
		w := getUserInfo(t, setupRouter(t, h), "")
		assert.Equal(t, http.StatusBadRequest, w.Code)
		errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthMissingFields)
	})
}

func TestHandleHealth_AlwaysOK(t *testing.T) {
	// Liveness must not depend on directory data being loaded.
	h := newTestHandler(newDirectoryCache(), false)
	w := getPath(t, setupRouter(t, h), "/healthz")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}

func TestHandleReady(t *testing.T) {
	tests := []struct {
		name     string
		cache    *directoryCache
		wantCode int
	}{
		{"cache never loaded", newDirectoryCache(), http.StatusServiceUnavailable},
		{"cache loaded but empty", cacheWith(), http.StatusServiceUnavailable},
		{"cache holds directory data", cacheWith(aliceEmployee), http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.cache, false)
			w := getPath(t, setupRouter(t, h), "/readyz")
			assert.Equal(t, tt.wantCode, w.Code)
		})
	}
}
