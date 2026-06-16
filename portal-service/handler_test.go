package main

import (
	"context"
	"encoding/json"
	"errors"
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
	c.replace(emps)
	return c
}

// testSites is the per-site URL registry used by the handler tests, with a
// distinct domain per site to prove URLs are looked up, not templated.
var testSites = map[string]siteURL{
	"site-a":     {AuthServiceURL: "https://auth.site-a.example.com", BaseURL: "https://site-a.example.com"},
	"site-b":     {AuthServiceURL: "https://auth.site-b.example.com", BaseURL: "https://site-b.example.com"},
	"site-local": {AuthServiceURL: "https://auth.site-local.example.com", BaseURL: "http://localhost:3000"},
}

// fakeProvisionChecker is a hand-written provisionChecker stub; the real users
// collection is exercised in integration_test.go.
type fakeProvisionChecker struct {
	provisioned bool
	err         error
}

func (f fakeProvisionChecker) AccountProvisioned(_ context.Context, _, _ string) (bool, error) {
	return f.provisioned, f.err
}

// newTestHandler builds a PortalHandler with the test site registry, the local
// dev-fallback coordinates, and a provisioning check that always passes.
func newTestHandler(cache *directoryCache, devMode bool) *PortalHandler {
	return newTestHandlerWithProvision(cache, devMode, fakeProvisionChecker{provisioned: true})
}

// newTestHandlerWithProvision builds a PortalHandler with an explicit
// provisioning stub, for cases that assert on the users-collection check.
func newTestHandlerWithProvision(cache *directoryCache, devMode bool, pc provisionChecker) *PortalHandler {
	return NewPortalHandler(cache, pc, devMode, "site-local", "ws://localhost:9222", testSites)
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
		BaseURL:        "https://site-a.example.com",
		NATSURL:        "wss://nats-3.site-a.example.com",
		SiteID:         "site-a",
	}, resp)
}

func TestHandleUserInfo_PerSiteURLs(t *testing.T) {
	t.Run("each site resolves its own auth and base URL from the registry", func(t *testing.T) {
		h := newTestHandler(cacheWith(aliceEmployee, bobEmployee), false)
		r := setupRouter(t, h)

		var alice userInfoResponse
		require.NoError(t, json.Unmarshal(getUserInfo(t, r, "alice").Body.Bytes(), &alice))
		assert.Equal(t, "https://auth.site-a.example.com", alice.AuthServiceURL)
		assert.Equal(t, "https://site-a.example.com", alice.BaseURL)

		var bob userInfoResponse
		require.NoError(t, json.Unmarshal(getUserInfo(t, r, "bob").Body.Bytes(), &bob))
		assert.Equal(t, "https://auth.site-b.example.com", bob.AuthServiceURL)
		assert.Equal(t, "https://site-b.example.com", bob.BaseURL)
	})

	t.Run("a site missing from the registry is a server error, not a client error", func(t *testing.T) {
		orphan := employee{Account: "carol", EmployeeID: "E003", SiteID: "site-unknown", NATSURL: "wss://nats.example.com"}
		h := newTestHandler(cacheWith(orphan), false)
		w := getUserInfo(t, setupRouter(t, h), "carol")
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeInternal)
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

func TestHandleUserInfo_NotProvisionedInUsers(t *testing.T) {
	// The account is in the HR directory but absent from the users collection —
	// not yet a real chat user, so the login is refused with the same reason as
	// a directory miss.
	h := newTestHandlerWithProvision(cacheWith(aliceEmployee), false, fakeProvisionChecker{provisioned: false})
	w := getUserInfo(t, setupRouter(t, h), "alice")

	assert.Equal(t, http.StatusForbidden, w.Code)
	errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeForbidden)
	errtest.AssertReason(t, w.Body.Bytes(), errcode.PortalAccountNotReady)
}

func TestHandleUserInfo_ProvisionCheckError(t *testing.T) {
	// A users-collection query failure fails closed as internal, not leaked.
	h := newTestHandlerWithProvision(cacheWith(aliceEmployee), false, fakeProvisionChecker{err: errors.New("mongo down")})
	w := getUserInfo(t, setupRouter(t, h), "alice")

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeInternal)
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
			AuthServiceURL: "https://auth.site-local.example.com", BaseURL: "http://localhost:3000",
			NATSURL: "ws://localhost:9222", SiteID: "site-local",
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

func TestParseSiteURLs(t *testing.T) {
	t.Run("valid registry decodes per-site URLs", func(t *testing.T) {
		sites, err := parseSiteURLs(`{"site-a":{"authServiceUrl":"https://auth.a.com","baseUrl":"https://a.com"},"site-b":{"authServiceUrl":"https://auth.b.com","baseUrl":"https://b.com"}}`)
		require.NoError(t, err)
		assert.Equal(t, siteURL{AuthServiceURL: "https://auth.a.com", BaseURL: "https://a.com"}, sites["site-a"])
		assert.Equal(t, siteURL{AuthServiceURL: "https://auth.b.com", BaseURL: "https://b.com"}, sites["site-b"])
	})

	for _, tt := range []struct{ name, raw string }{
		{"not JSON", "site-a=https://auth.a.com"},
		{"empty object", "{}"},
		{"missing baseUrl", `{"site-a":{"authServiceUrl":"https://auth.a.com"}}`},
		{"missing authServiceUrl", `{"site-a":{"baseUrl":"https://a.com"}}`},
	} {
		t.Run(tt.name+" is rejected", func(t *testing.T) {
			_, err := parseSiteURLs(tt.raw)
			assert.Error(t, err)
		})
	}
}
