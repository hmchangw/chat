package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errtest"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
)

// fakeValidator implements TokenValidator for testing.
type fakeValidator struct {
	account string
	name    string
	expired bool
	invalid bool
}

func (f *fakeValidator) Validate(_ context.Context, _ string) (pkgoidc.Claims, error) {
	if f.expired {
		return pkgoidc.Claims{}, pkgoidc.ErrTokenExpired
	}
	if f.invalid {
		return pkgoidc.Claims{}, fmt.Errorf("oidc token verification failed: invalid signature")
	}
	return pkgoidc.Claims{PreferredUsername: f.account, Name: f.name}, nil
}

func setupRouter(t *testing.T, h *PortalHandler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerRoutes(r, h)
	return r
}

func postLookup(t *testing.T, r *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/lookup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
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
func newTestHandler(v TokenValidator, cache *directoryCache, devMode bool) *PortalHandler {
	return NewPortalHandler(v, cache, devMode, "site-local", "ws://localhost:9222", "https://auth.{siteId}.example.com")
}

func TestHandleLookup_HappyPath(t *testing.T) {
	h := newTestHandler(&fakeValidator{account: "alice"}, cacheWith(aliceEmployee), false)
	w := postLookup(t, setupRouter(t, h), `{"ssoToken":"valid-token"}`)

	require.Equal(t, http.StatusOK, w.Code)
	var resp lookupResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, lookupResponse{
		Account:        "alice",
		EmployeeID:     "E001",
		AuthServiceURL: "https://auth.site-a.example.com",
		NATSURL:        "wss://nats-3.site-a.example.com",
		SiteID:         "site-a",
	}, resp)
}

func TestHandleLookup_AuthURLTemplate(t *testing.T) {
	t.Run("placeholder replaced with the employee siteId", func(t *testing.T) {
		h := newTestHandler(nil, cacheWith(bobEmployee), true)
		w := postLookup(t, setupRouter(t, h), `{"account":"bob"}`)
		require.Equal(t, http.StatusOK, w.Code)
		var resp lookupResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "https://auth.site-b.example.com", resp.AuthServiceURL)
	})

	t.Run("template without placeholder is used verbatim", func(t *testing.T) {
		// Single-site deployments configure a fixed URL.
		h := NewPortalHandler(nil, cacheWith(aliceEmployee), true, "site-local", "ws://localhost:9222", "http://localhost:8080")
		w := postLookup(t, setupRouter(t, h), `{"account":"alice"}`)
		require.Equal(t, http.StatusOK, w.Code)
		var resp lookupResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "http://localhost:8080", resp.AuthServiceURL)
	})
}

func TestHandleLookup_InvalidAccountFormat(t *testing.T) {
	// Same token invariant auth-service enforces at minting — refusing here
	// keeps the portal from blessing an account the next step must reject.
	t.Run("prod: dotted claim refused", func(t *testing.T) {
		h := newTestHandler(&fakeValidator{account: "john.doe"}, cacheWith(aliceEmployee), false)
		w := postLookup(t, setupRouter(t, h), `{"ssoToken":"valid-token"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeBadRequest)
	})
	t.Run("dev: wildcard body refused", func(t *testing.T) {
		h := newTestHandler(nil, cacheWith(aliceEmployee), true)
		w := postLookup(t, setupRouter(t, h), `{"account":"mal*ory"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeBadRequest)
	})
}

func TestNewPortalHandler_NilValidatorPanics(t *testing.T) {
	assert.Panics(t, func() { newTestHandler(nil, newDirectoryCache(), false) })
}

func TestHandleLookup_TokenErrors(t *testing.T) {
	tests := []struct {
		name       string
		validator  *fakeValidator
		wantReason errcode.Reason
	}{
		{"expired token", &fakeValidator{expired: true}, errcode.AuthTokenExpired},
		{"invalid token", &fakeValidator{invalid: true}, errcode.AuthInvalidToken},
		{"blank account claim", &fakeValidator{}, errcode.AuthInvalidToken},
		// name is a user-editable display claim — it must never become the principal.
		{"name-only claim refused", &fakeValidator{name: "Alice W"}, errcode.AuthInvalidToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.validator, cacheWith(aliceEmployee), false)
			w := postLookup(t, setupRouter(t, h), `{"ssoToken":"tok"}`)

			assert.Equal(t, http.StatusUnauthorized, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeUnauthenticated)
			errtest.AssertReason(t, w.Body.Bytes(), tt.wantReason)
		})
	}
}

func TestHandleLookup_MissingBody(t *testing.T) {
	h := newTestHandler(&fakeValidator{account: "alice"}, cacheWith(aliceEmployee), false)
	router := setupRouter(t, h)

	for _, tt := range []struct{ name, body string }{
		{"empty object", `{}`},
		{"empty body", ``},
		{"wrong field", `{"account":"alice"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := postLookup(t, router, tt.body)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeBadRequest)
			errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthMissingFields)
		})
	}
}

func TestHandleLookup_AccountNotReady(t *testing.T) {
	t.Run("account absent from a populated cache", func(t *testing.T) {
		h := newTestHandler(&fakeValidator{account: "mallory"}, cacheWith(aliceEmployee), false)
		w := postLookup(t, setupRouter(t, h), `{"ssoToken":"tok"}`)

		assert.Equal(t, http.StatusForbidden, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeForbidden)
		errtest.AssertReason(t, w.Body.Bytes(), errcode.PortalAccountNotReady)
	})

	t.Run("cache not yet loaded", func(t *testing.T) {
		h := newTestHandler(&fakeValidator{account: "alice"}, newDirectoryCache(), false)
		w := postLookup(t, setupRouter(t, h), `{"ssoToken":"tok"}`)

		assert.Equal(t, http.StatusForbidden, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeForbidden)
		errtest.AssertReason(t, w.Body.Bytes(), errcode.PortalAccountNotReady)
	})
}

func TestHandleLookup_DevMode(t *testing.T) {
	t.Run("known account resolves normally", func(t *testing.T) {
		h := newTestHandler(nil, cacheWith(aliceEmployee), true)
		w := postLookup(t, setupRouter(t, h), `{"account":"alice"}`)
		require.Equal(t, http.StatusOK, w.Code)
		var resp lookupResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "site-a", resp.SiteID)
	})

	t.Run("unknown account falls back to the dev site", func(t *testing.T) {
		h := newTestHandler(nil, cacheWith(aliceEmployee), true)
		w := postLookup(t, setupRouter(t, h), `{"account":"newdev"}`)
		require.Equal(t, http.StatusOK, w.Code)
		var resp lookupResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, lookupResponse{
			Account: "newdev", EmployeeID: "",
			AuthServiceURL: "https://auth.site-local.example.com", NATSURL: "ws://localhost:9222",
			SiteID: "site-local",
		}, resp)
	})

	t.Run("fallback works before the cache is loaded", func(t *testing.T) {
		h := newTestHandler(nil, newDirectoryCache(), true)
		w := postLookup(t, setupRouter(t, h), `{"account":"newdev"}`)
		require.Equal(t, http.StatusOK, w.Code)
		var resp lookupResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "site-local", resp.SiteID)
	})

	t.Run("missing account is bad request", func(t *testing.T) {
		h := newTestHandler(nil, cacheWith(aliceEmployee), true)
		w := postLookup(t, setupRouter(t, h), `{}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthMissingFields)
	})
}

func TestHandleHealth_AlwaysOK(t *testing.T) {
	// Liveness must not depend on directory data being loaded.
	h := newTestHandler(&fakeValidator{}, newDirectoryCache(), false)
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
			h := newTestHandler(&fakeValidator{}, tt.cache, false)
			w := getPath(t, setupRouter(t, h), "/readyz")
			assert.Equal(t, tt.wantCode, w.Code)
		})
	}
}
