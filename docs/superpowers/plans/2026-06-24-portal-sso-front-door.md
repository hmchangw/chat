# Portal SSO Login Front-Door Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a confidential-OIDC SSO front-door to `portal-service` (`/login` + `/auth/callback` → redirect to the home-site chat-frontend `baseUrl`), trim `/api/userInfo`, and revert chat-frontend to static per-site config.

**Architecture:** `portal-service` (Go/Gin) gains a confidential OIDC web-app login that reuses its in-process directory cache to resolve `account → siteId → baseUrl` and redirects the browser to the site root; chat-frontend then silently re-auths against the shared Keycloak `chatapp` realm and connects with static `AUTH_URL`/`NATS_URL`/`SITE_ID`. Spec: `docs/superpowers/specs/2026-06-24-portal-sso-front-door-design.md`.

**Tech Stack:** Go 1.25, Gin, `golang.org/x/oauth2`, `github.com/coreos/go-oidc/v3`, `go.uber.org/mock`, `testify`; React + vitest; Keycloak.

---

## File Structure

**portal-service (Go, `package main`):**
- `oidclogin.go` *(new)* — `loginAuthenticator` interface + `oidcLogin` impl (oauth2 + go-oidc) + mockgen directive. One responsibility: the OIDC client.
- `handler.go` *(modify)* — login/callback handlers, cookie + HTML-error helpers, `resolveSite` helper, trimmed `userInfoResponse`/`siteURL`/`parseSiteURLs`.
- `store.go` *(modify)* — drop `NATSURL` from `employee`.
- `store_mongo.go` *(modify)* — drop `natsUrl` from the projection.
- `main.go` *(modify)* — OIDC + cookie config, dev-site validation, build/inject `loginAuthenticator`.
- `routes.go` *(modify)* — register `/login`, `/auth/callback`.
- `oidclogin_test.go`, `handler_test.go` *(modify/new)*, `mock_oidclogin_test.go` *(generated)*.
- `deploy/docker-compose.yml` *(modify)*, `auth-service/deploy/keycloak/realm-export.json` *(modify)*.

**chat-frontend (JS):**
- `src/lib/runtimeConfig.js` *(modify)* + `.test.js`.
- `src/context/NatsContext/NatsContext.jsx` *(modify)* + `.test.jsx`.
- `deploy/config.js.template`, `deploy/30-render-config.sh`, `deploy/docker-compose.yml`, `docker-local/setup.sh` *(modify)*.

**docs:** `docs/client-api.md` *(modify)*.

This plan has two independently-testable parts: **Part A (portal-service, Tasks 1–9)** and **Part B (chat-frontend + docs, Tasks 10–13)**.

---

# Part A — portal-service

## Task 1: `loginAuthenticator` interface + `AuthCodeURL`

**Files:**
- Create: `portal-service/oidclogin.go`
- Test: `portal-service/oidclogin_test.go`
- Generated: `portal-service/mock_oidclogin_test.go`

- [ ] **Step 1: Write the failing test**

Create `portal-service/oidclogin_test.go`:

```go
package main

import (
	"net/url"
	"strings"
	"testing"

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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `undefined: oidcLogin`.

- [ ] **Step 3: Write `oidclogin.go`**

Create `portal-service/oidclogin.go`:

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

//go:generate mockgen -source=oidclogin.go -destination=mock_oidclogin_test.go -package=main

// loginAuthenticator drives the confidential OIDC web-app login: build the
// authorize URL, then exchange and verify the returned code.
type loginAuthenticator interface {
	AuthCodeURL(state, nonce string) string
	ExchangeAndVerify(ctx context.Context, code, expectedNonce string) (account string, err error)
}

type oidcLogin struct {
	oauth      oauth2.Config
	verifier   *oidc.IDTokenVerifier
	httpClient *http.Client
}

func (o *oidcLogin) AuthCodeURL(state, nonce string) string {
	return o.oauth.AuthCodeURL(state, oidc.Nonce(nonce))
}

// newOIDCLogin discovers the issuer and builds the oauth2 config + verifier.
func newOIDCLogin(ctx context.Context, issuer, clientID, secret, redirectURL string, scopes []string, httpClient *http.Client) (*oidcLogin, error) {
	if httpClient != nil {
		ctx = oidc.ClientContext(ctx, httpClient)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", issuer, err)
	}
	return &oidcLogin{
		oauth: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: secret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  redirectURL,
			Scopes:       scopes,
		},
		verifier:   provider.Verifier(&oidc.Config{ClientID: clientID}),
		httpClient: httpClient,
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=portal-service`
Expected: PASS.

- [ ] **Step 5: Generate the mock**

Run: `make generate SERVICE=portal-service`
Expected: creates `portal-service/mock_oidclogin_test.go` with `MockloginAuthenticator`.

- [ ] **Step 6: Commit**

```bash
git add portal-service/oidclogin.go portal-service/oidclogin_test.go portal-service/mock_oidclogin_test.go
git commit -m "feat(portal): loginAuthenticator interface + AuthCodeURL"
```

---

## Task 2: `ExchangeAndVerify`

**Files:**
- Modify: `portal-service/oidclogin.go`
- Test: `portal-service/oidclogin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `portal-service/oidclogin_test.go`, merging these into the existing `import` block from Task 1 (Go allows only one):

```go
import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

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
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `oidcLogin` has no `ExchangeAndVerify` method (undefined). (If the `go-jose/v4` import is unresolved, run `go mod tidy` and re-check its major version in `go.sum`.)

- [ ] **Step 3: Implement `ExchangeAndVerify`**

In `portal-service/oidclogin.go`, add the `ExchangeAndVerify` method:

```go
func (o *oidcLogin) ExchangeAndVerify(ctx context.Context, code, expectedNonce string) (string, error) {
	// Bind the configured client so exchange + JWKS fetch use its TLS/transport,
	// not just discovery (oidc.ClientContext sets the oauth2.HTTPClient key).
	if o.httpClient != nil {
		ctx = oidc.ClientContext(ctx, o.httpClient)
	}
	tok, err := o.oauth.Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("exchange auth code: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return "", fmt.Errorf("token response has no id_token")
	}
	idTok, err := o.verifier.Verify(ctx, rawID)
	if err != nil {
		return "", fmt.Errorf("verify id token: %w", err)
	}
	// go-oidc does not check nonce on its own — compare explicitly.
	if idTok.Nonce != expectedNonce {
		return "", fmt.Errorf("id token nonce mismatch")
	}
	var claims struct {
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idTok.Claims(&claims); err != nil {
		return "", fmt.Errorf("parse id token claims: %w", err)
	}
	if claims.PreferredUsername == "" {
		return "", fmt.Errorf("id token has blank preferred_username")
	}
	return claims.PreferredUsername, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=portal-service`
Expected: PASS (both happy path and wrong-nonce rejection).

- [ ] **Step 5: Commit**

```bash
git add portal-service/oidclogin.go portal-service/oidclogin_test.go go.mod go.sum
git commit -m "feat(portal): ExchangeAndVerify with explicit nonce check"
```

---

## Task 3: OIDC + cookie config and validation

**Files:**
- Modify: `portal-service/main.go`
- Test: `portal-service/main_test.go` *(new)*

- [ ] **Step 1: Write the failing test**

Create `portal-service/main_test.go`:

```go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireOIDCConfig(t *testing.T) {
	full := config{
		OIDCIssuerURL: "https://kc/realms/chatapp", OIDCClientID: "portal",
		OIDCClientSecret: "s", OIDCRedirectURL: "http://localhost:8081/auth/callback",
	}

	t.Run("dev mode skips", func(t *testing.T) {
		cfg := config{DevMode: true}
		require.NoError(t, requireOIDCConfig(cfg))
	})
	t.Run("prod full passes", func(t *testing.T) {
		require.NoError(t, requireOIDCConfig(full))
	})
	t.Run("prod missing issuer fails", func(t *testing.T) {
		cfg := full
		cfg.OIDCIssuerURL = ""
		err := requireOIDCConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OIDC_ISSUER_URL")
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `config` has no OIDC fields; `requireOIDCConfig` undefined.

- [ ] **Step 3: Add config fields + validator**

In `portal-service/main.go`, add to the `config` struct (after `MongoPassword`):

```go
	OIDCIssuerURL    string   `env:"OIDC_ISSUER_URL"`
	OIDCClientID     string   `env:"OIDC_CLIENT_ID"`
	OIDCClientSecret string   `env:"OIDC_CLIENT_SECRET"`
	OIDCRedirectURL  string   `env:"OIDC_REDIRECT_URL"`
	OIDCScopes       []string `env:"OIDC_SCOPES" envSeparator:" " envDefault:"openid profile email"`
	CookieSecure     bool     `env:"PORTAL_COOKIE_SECURE" envDefault:"true"`
	TLSSkipVerify    bool     `env:"TLS_SKIP_VERIFY" envDefault:"false"`
```

Add the validator function (leave `DevFallbackNatsURL` in place for now — it is removed atomically in Task 5, so no signature breaks mid-plan):

```go
// requireOIDCConfig fails fast when the prod OIDC fields are missing
// (caarlos0/env can't express conditional-required).
func requireOIDCConfig(cfg config) error {
	if cfg.DevMode {
		return nil
	}
	missing := []string{}
	if cfg.OIDCIssuerURL == "" {
		missing = append(missing, "OIDC_ISSUER_URL")
	}
	if cfg.OIDCClientID == "" {
		missing = append(missing, "OIDC_CLIENT_ID")
	}
	if cfg.OIDCClientSecret == "" {
		missing = append(missing, "OIDC_CLIENT_SECRET")
	}
	if cfg.OIDCRedirectURL == "" {
		missing = append(missing, "OIDC_REDIRECT_URL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required OIDC config when DEV_MODE=false: %v", missing)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=portal-service`
Expected: PASS (no signatures change in this task).

- [ ] **Step 5: Commit**

```bash
git add portal-service/main.go portal-service/main_test.go
git commit -m "feat(portal): OIDC + cookie config with conditional prod validation"
```

---

## Task 4: state/nonce + handshake cookie helpers

**Files:**
- Modify: `portal-service/handler.go`
- Test: `portal-service/handler_test.go`

- [ ] **Step 1: Write the failing test**

Append to `portal-service/handler_test.go`:

```go
import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandshakeCookie_RoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &PortalHandler{cookieSecure: true}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/login", nil)
	h.setHandshake(c, "st", "no")

	res := w.Result()
	require.Len(t, res.Cookies(), 1)
	ck := res.Cookies()[0]
	assert.Equal(t, handshakeCookie, ck.Name)
	assert.True(t, ck.HttpOnly)
	assert.True(t, ck.Secure)
	assert.Equal(t, http.SameSiteLaxMode, ck.SameSite)

	// Read it back from a fresh request that carries the cookie.
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	c2.Request.AddCookie(ck)
	st, no, ok := readHandshake(c2)
	assert.True(t, ok)
	assert.Equal(t, "st", st)
	assert.Equal(t, "no", no)
}

func TestReadHandshake_Missing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	_, _, ok := readHandshake(c)
	assert.False(t, ok)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `PortalHandler` has no `cookieSecure`; `setHandshake`/`readHandshake`/`handshakeCookie` undefined.

- [ ] **Step 3: Implement helpers**

In `portal-service/handler.go`, add the field `cookieSecure bool` to `PortalHandler` and these helpers (add imports `net/http`, `strings`, `time`). State/nonce are generated with `idgen.GenerateID()` in `HandleLogin` (Task 6), so no token generator lives here:

```go
const handshakeCookie = "portal_oidc_handshake"
const handshakeTTL = 5 * time.Minute

func (h *PortalHandler) setHandshake(c *gin.Context, state, nonce string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     handshakeCookie,
		Value:    state + "." + nonce,
		Path:     "/",
		MaxAge:   int(handshakeTTL.Seconds()),
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func readHandshake(c *gin.Context) (state, nonce string, ok bool) {
	v, err := c.Cookie(handshakeCookie)
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func clearHandshake(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: handshakeCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=portal-service`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add portal-service/handler.go portal-service/handler_test.go
git commit -m "feat(portal): state/nonce handshake cookie helpers"
```

---

## Task 5: `/api/userInfo` trim + `resolveSite` + dead-field cleanup

**Files:**
- Modify: `portal-service/handler.go`, `portal-service/store.go`, `portal-service/store_mongo.go`, `portal-service/main.go`
- Test: `portal-service/handler_test.go`

- [ ] **Step 1: Write the failing test**

Append to `portal-service/handler_test.go`:

```go
import "errors"

func TestParseSiteURLs_BaseURLOnly(t *testing.T) {
	sites, err := parseSiteURLs(`{"site-a":{"baseUrl":"https://a.example.com"}}`)
	require.NoError(t, err)
	assert.Equal(t, "https://a.example.com", sites["site-a"].BaseURL)

	_, err = parseSiteURLs(`{"site-a":{}}`)
	require.Error(t, err) // baseUrl required
}

func TestResolveSite(t *testing.T) {
	h := &PortalHandler{
		cache: cacheWith(employee{Account: "alice", EmployeeID: "E1", SiteID: "site-a"}),
		sites: map[string]siteURL{"site-a": {BaseURL: "https://a.example.com"}},
	}
	e, site, err := h.resolveSite("alice")
	require.NoError(t, err)
	assert.Equal(t, "E1", e.EmployeeID)
	assert.Equal(t, "https://a.example.com", site.BaseURL)

	_, _, err = h.resolveSite("ghost")
	assert.ErrorIs(t, err, errAccountNotReady)
}
```

Add this test helper to `handler_test.go` (the cache is an in-process struct):

```go
func cacheWith(emps ...employee) *directoryCache {
	c := newDirectoryCache()
	c.replace(emps)
	return c
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `parseSiteURLs` still requires `authServiceUrl`; `resolveSite`/`errAccountNotReady` undefined.

- [ ] **Step 3: Trim the structs and add `resolveSite`**

In `portal-service/handler.go`:

Replace `siteURL` + `parseSiteURLs` field requirement (drop `AuthServiceURL`):

```go
type siteURL struct {
	BaseURL string `json:"baseUrl"`
}

func parseSiteURLs(raw string) (map[string]siteURL, error) {
	var sites map[string]siteURL
	if err := json.Unmarshal([]byte(raw), &sites); err != nil {
		return nil, fmt.Errorf("decode site URL registry: %w", err)
	}
	if len(sites) == 0 {
		return nil, fmt.Errorf("site URL registry is empty")
	}
	for id, s := range sites {
		if s.BaseURL == "" {
			return nil, fmt.Errorf("site %q: baseUrl is required", id)
		}
	}
	return sites, nil
}
```

Trim `userInfoResponse`:

```go
type userInfoResponse struct {
	Account    string `json:"account"`
	EmployeeID string `json:"employeeId"`
	SiteID     string `json:"siteId"`
	BaseURL    string `json:"baseUrl"`
}
```

Add sentinel errors + `resolveSite`, and rewrite `resolve`/`HandleUserInfo` to use it. Replace the existing `resolve` method with:

```go
var (
	errAccountNotReady = errors.New("account not ready")
	errSiteMissing     = errors.New("site missing from registry")
)

// resolveSite maps an account to its directory entry and site URLs, applying
// the dev fallback. Sentinel errors classify the failure.
func (h *PortalHandler) resolveSite(account string) (employee, siteURL, error) {
	e, ok := h.cache.Get(account)
	if !ok {
		if !h.devMode {
			return employee{}, siteURL{}, errAccountNotReady
		}
		e = employee{Account: account, SiteID: h.devFallbackSiteID}
	}
	site, ok := h.sites[e.SiteID]
	if !ok {
		return e, siteURL{}, fmt.Errorf("%w: siteId %q", errSiteMissing, e.SiteID)
	}
	return e, site, nil
}
```

Rewrite `HandleUserInfo` to call `resolveSite` (keep its `account`-validation and `errhttp` JSON envelope):

```go
func (h *PortalHandler) HandleUserInfo(c *gin.Context) {
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))
	account := c.Query("account")
	if account == "" {
		errhttp.Write(ctx, c, errcode.BadRequest("account is required", errcode.WithReason(errcode.AuthMissingFields)))
		return
	}
	if !subject.IsValidAccountToken(account) {
		errhttp.Write(ctx, c, errcode.BadRequest("account must be a single NATS subject token (no '.', '*', '>' or whitespace)"))
		return
	}
	ctx = errcode.WithLogValues(ctx, "account", account)
	e, site, err := h.resolveSite(account)
	switch {
	case errors.Is(err, errAccountNotReady):
		errhttp.Write(ctx, c, errcode.Forbidden("account not ready for chat", errcode.WithReason(errcode.PortalAccountNotReady)))
		return
	case err != nil:
		errhttp.Write(ctx, c, err)
		return
	}
	c.JSON(http.StatusOK, userInfoResponse{
		Account: e.Account, EmployeeID: e.EmployeeID, SiteID: e.SiteID, BaseURL: site.BaseURL,
	})
}
```

Update `NewPortalHandler` to drop `devFallbackNatsURL` and add `auth loginAuthenticator` + `cookieSecure bool` parameters (the `auth`/`cookieSecure` are used in Tasks 6–8; add them now so the signature is stable):

```go
func NewPortalHandler(cache *directoryCache, devMode bool, devFallbackSiteID string,
	sites map[string]siteURL, auth loginAuthenticator, cookieSecure bool) *PortalHandler {
	return &PortalHandler{
		cache: cache, devMode: devMode, devFallbackSiteID: devFallbackSiteID,
		sites: sites, auth: auth, cookieSecure: cookieSecure,
	}
}
```

And the struct:

```go
type PortalHandler struct {
	cache             *directoryCache
	devMode           bool
	devFallbackSiteID string
	sites             map[string]siteURL
	auth              loginAuthenticator
	cookieSecure      bool
}
```

In `portal-service/store.go`, drop `NATSURL` from `employee`:

```go
type employee struct {
	Account    string `json:"account"    bson:"account"`
	EmployeeID string `json:"employeeId" bson:"employeeId"`
	SiteID     string `json:"siteId"     bson:"siteId"`
	UserID     string `json:"userId"     bson:"userId"`
}
```

In `portal-service/store_mongo.go`, drop the `natsUrl` projection line (the `$project` stage):

```go
			bson.D{{Key: "$project", Value: bson.D{
				{Key: "_id", Value: 0},
				{Key: "account", Value: 1},
				{Key: "employeeId", Value: 1},
				{Key: "siteId", Value: 1},
				{Key: "userId", Value: bson.D{{Key: "$arrayElemAt", Value: bson.A{"$provisioned._id", 0}}}},
			}}},
```

In `portal-service/main.go`, remove the now-unused `DevFallbackNatsURL` config field, and update the `NewPortalHandler` call site to the new signature (Task 8 finalizes the `auth` build; for now pass `nil, cfg.CookieSecure`):

```go
	handler := NewPortalHandler(cache, cfg.DevMode, cfg.DevFallbackSiteID, sites, nil, cfg.CookieSecure)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=portal-service`
Expected: PASS. Existing `handler_test.go` userInfo tests that asserted `authServiceUrl`/`natsUrl` must be updated to the 4-field shape — fix any that now fail by removing those assertions. Also update `portal-service/integration_test.go` if it asserts `natsUrl` on a decoded `employee` (drop those assertions).

- [ ] **Step 5: Commit**

```bash
git add portal-service/handler.go portal-service/store.go portal-service/store_mongo.go portal-service/main.go portal-service/handler_test.go
git commit -m "feat(portal): trim /api/userInfo to 4 fields + resolveSite helper"
```

---

## Task 6: `HandleLogin` + route

**Files:**
- Modify: `portal-service/handler.go`, `portal-service/routes.go`
- Test: `portal-service/handler_test.go`

- [ ] **Step 1: Write the failing test**

Append to `portal-service/handler_test.go`:

```go
func TestHandleLogin_Prod(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctrl := gomock.NewController(t)
	auth := NewMockloginAuthenticator(ctrl)
	auth.EXPECT().AuthCodeURL(gomock.Any(), gomock.Any()).
		DoAndReturn(func(state, nonce string) string {
			return "http://kc/auth?state=" + state + "&nonce=" + nonce
		})
	h := &PortalHandler{auth: auth, cookieSecure: true}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/login", nil)
	h.HandleLogin(c)

	assert.Equal(t, http.StatusFound, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "state=")
	require.Len(t, w.Result().Cookies(), 1)
	assert.Equal(t, handshakeCookie, w.Result().Cookies()[0].Name)
}

func TestHandleLogin_DevMissingSite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &PortalHandler{devMode: true, devFallbackSiteID: "nope", sites: map[string]siteURL{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/login", nil)
	h.HandleLogin(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleLogin_Dev(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &PortalHandler{devMode: true, devFallbackSiteID: "site-local",
		sites: map[string]siteURL{"site-local": {BaseURL: "http://localhost:3000"}}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/login", nil)
	h.HandleLogin(c)
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "http://localhost:3000", w.Header().Get("Location"))
}
```

Add the `gomock` import: `"go.uber.org/mock/gomock"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `HandleLogin` undefined.

- [ ] **Step 3: Implement `HandleLogin`**

In `portal-service/handler.go` (add the import `"github.com/hmchangw/chat/pkg/idgen"` — `HandleLogin` uses `idgen.GenerateID()` for state/nonce):

```go
func (h *PortalHandler) HandleLogin(c *gin.Context) {
	if h.devMode {
		site, ok := h.sites[h.devFallbackSiteID]
		if !ok {
			c.String(http.StatusInternalServerError, "dev fallback site not configured")
			return
		}
		c.Redirect(http.StatusFound, site.BaseURL)
		return
	}
	state := idgen.GenerateID()
	nonce := idgen.GenerateID()
	h.setHandshake(c, state, nonce)
	c.Redirect(http.StatusFound, h.auth.AuthCodeURL(state, nonce))
}

// writeHTMLError renders a minimal browser-facing error page (not the JSON
// errcode envelope, which stays for /api/userInfo).
func writeHTMLError(c *gin.Context, status int, msg string) {
	c.Data(status, "text/html; charset=utf-8",
		[]byte("<!doctype html><html><body><p>"+template.HTMLEscapeString(msg)+"</p></body></html>"))
}
```

In `portal-service/routes.go`, add: `r.GET("/login", h.HandleLogin)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=portal-service`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add portal-service/handler.go portal-service/routes.go portal-service/handler_test.go
git commit -m "feat(portal): /login endpoint (dev redirect + prod authorize)"
```

---

## Task 7: `HandleAuthCallback` + route

**Files:**
- Modify: `portal-service/handler.go`, `portal-service/routes.go`
- Test: `portal-service/handler_test.go`

- [ ] **Step 1: Write the failing test**

Append to `portal-service/handler_test.go`:

```go
func callbackCtx(t *testing.T, query string, cookie *http.Cookie) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/auth/callback?"+query, nil)
	if cookie != nil {
		c.Request.AddCookie(cookie)
	}
	return c, w
}

func TestHandleAuthCallback_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctrl := gomock.NewController(t)
	auth := NewMockloginAuthenticator(ctrl)
	auth.EXPECT().ExchangeAndVerify(gomock.Any(), "thecode", "no").Return("alice", nil)
	h := &PortalHandler{
		auth:  auth,
		cache: cacheWith(employee{Account: "alice", EmployeeID: "E1", SiteID: "site-a"}),
		sites: map[string]siteURL{"site-a": {BaseURL: "https://a.example.com"}},
	}
	c, w := callbackCtx(t, "state=st&code=thecode", &http.Cookie{Name: handshakeCookie, Value: "st.no"})
	h.HandleAuthCallback(c)
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "https://a.example.com", w.Header().Get("Location"))
}

func TestHandleAuthCallback_StateMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &PortalHandler{}
	c, w := callbackCtx(t, "state=WRONG&code=x", &http.Cookie{Name: handshakeCookie, Value: "st.no"})
	h.HandleAuthCallback(c)
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))
}

func TestHandleAuthCallback_NotProvisioned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctrl := gomock.NewController(t)
	auth := NewMockloginAuthenticator(ctrl)
	auth.EXPECT().ExchangeAndVerify(gomock.Any(), "c", "no").Return("ghost", nil)
	h := &PortalHandler{auth: auth, cache: cacheWith(), sites: map[string]siteURL{}}
	c, w := callbackCtx(t, "state=st&code=c", &http.Cookie{Name: handshakeCookie, Value: "st.no"})
	h.HandleAuthCallback(c)
	assert.Equal(t, http.StatusForbidden, w.Code)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `HandleAuthCallback` undefined.

- [ ] **Step 3: Implement `HandleAuthCallback`**

In `portal-service/handler.go`:

```go
func (h *PortalHandler) HandleAuthCallback(c *gin.Context) {
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	state, nonce, ok := readHandshake(c)
	if !ok || state == "" || c.Query("state") != state {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	clearHandshake(c)

	account, err := h.auth.ExchangeAndVerify(ctx, c.Query("code"), nonce)
	if err != nil {
		slog.WarnContext(ctx, "portal callback: exchange/verify failed", "error", err.Error())
		c.Redirect(http.StatusFound, "/login")
		return
	}

	_, site, err := h.resolveSite(account)
	switch {
	case errors.Is(err, errAccountNotReady):
		writeHTMLError(c, http.StatusForbidden,
			"Your account isn't set up for chat yet — contact your administrator.")
		return
	case err != nil:
		slog.ErrorContext(ctx, "portal callback: resolve failed", "error", err.Error())
		writeHTMLError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.Redirect(http.StatusFound, site.BaseURL)
}
```

Add `"log/slog"` to the imports if not present. In `portal-service/routes.go`, add: `r.GET("/auth/callback", h.HandleAuthCallback)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=portal-service`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add portal-service/handler.go portal-service/routes.go portal-service/handler_test.go
git commit -m "feat(portal): /auth/callback verifies, resolves baseUrl, redirects"
```

---

## Task 8: `main.go` wiring

**Files:**
- Modify: `portal-service/main.go`

- [ ] **Step 1: Wire the authenticator + dev-site validation**

In `portal-service/main.go` `run()`, after `parseSiteURLs` and before building the handler, add:

```go
	if err := requireOIDCConfig(cfg); err != nil {
		return err
	}

	var auth loginAuthenticator
	if cfg.DevMode {
		if _, ok := sites[cfg.DevFallbackSiteID]; !ok {
			return fmt.Errorf("dev fallback site %q missing from PORTAL_SITE_URLS", cfg.DevFallbackSiteID)
		}
	} else {
		var httpClient *http.Client
		if cfg.TLSSkipVerify {
			// #nosec G402 -- opt-in via TLS_SKIP_VERIFY for dev/staging only
			httpClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}} //nolint:gosec
		}
		oidcAuth, err := newOIDCLogin(ctx, cfg.OIDCIssuerURL, cfg.OIDCClientID,
			cfg.OIDCClientSecret, cfg.OIDCRedirectURL, cfg.OIDCScopes, httpClient)
		if err != nil {
			return fmt.Errorf("init oidc login: %w", err)
		}
		auth = oidcAuth
	}
```

Update the handler construction:

```go
	handler := NewPortalHandler(cache, cfg.DevMode, cfg.DevFallbackSiteID, sites, auth, cfg.CookieSecure)
```

Add imports `crypto/tls` and `net/http` if missing.

- [ ] **Step 2: Build to verify it compiles**

Run: `make build SERVICE=portal-service`
Expected: builds cleanly.

- [ ] **Step 3: Run the full service test suite + lint**

Run: `make test SERVICE=portal-service && make lint`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add portal-service/main.go
git commit -m "feat(portal): wire OIDC login authenticator + dev-site validation"
```

---

## Task 9: Keycloak `portal` client + deploy env

**Files:**
- Modify: `auth-service/deploy/keycloak/realm-export.json`, `portal-service/deploy/docker-compose.yml`

- [ ] **Step 1: Add the `portal` client to the realm export**

In `auth-service/deploy/keycloak/realm-export.json`, add a second client object to the `clients` array (alongside `nats-chat`), in the same `chatapp` realm:

```json
{
  "clientId": "portal",
  "name": "Portal SSO Front-Door",
  "enabled": true,
  "publicClient": false,
  "secret": "portal-dev-secret",
  "standardFlowEnabled": true,
  "directAccessGrantsEnabled": false,
  "implicitFlowEnabled": false,
  "redirectUris": ["http://localhost:8081/auth/callback"],
  "webOrigins": ["http://localhost:8081"],
  "protocol": "openid-connect"
}
```

- [ ] **Step 2: Add the OIDC env block to portal's compose**

In `portal-service/deploy/docker-compose.yml`, keep `DEV_MODE=true` as the default for the portal service and add a commented block documenting the real-OIDC path; remove the `PORTAL_DEV_FALLBACK_NATS_URL` line if present. Add under the portal service `environment:`:

```yaml
      DEV_MODE: "true"
      # Flip DEV_MODE to "false" to exercise real Keycloak login locally:
      # OIDC_ISSUER_URL: "http://localhost:8180/realms/chatapp"
      # OIDC_CLIENT_ID: "portal"
      # OIDC_CLIENT_SECRET: "portal-dev-secret"
      # OIDC_REDIRECT_URL: "http://localhost:8081/auth/callback"
      # PORTAL_COOKIE_SECURE: "false"
```

- [ ] **Step 3: Verify the realm JSON is valid**

Run: `python3 -m json.tool auth-service/deploy/keycloak/realm-export.json > /dev/null && echo OK`
Expected: `OK`.

- [ ] **Step 4: Commit**

```bash
git add auth-service/deploy/keycloak/realm-export.json portal-service/deploy/docker-compose.yml
git commit -m "feat(portal): keycloak portal client + local OIDC env"
```

---

# Part B — chat-frontend + docs

## Task 10: `runtimeConfig` → static `AUTH_URL`/`NATS_URL`/`SITE_ID`

**Files:**
- Modify: `chat-frontend/src/lib/runtimeConfig.js`
- Test: `chat-frontend/src/lib/runtimeConfig.test.js`

- [ ] **Step 1: Write the failing test**

Replace the `PORTAL_URL` cases in `chat-frontend/src/lib/runtimeConfig.test.js` with:

```js
it('AUTH_URL/NATS_URL/SITE_ID default to the local stack', async () => {
  const { AUTH_URL, NATS_URL, SITE_ID } = await import('./runtimeConfig.js')
  expect(AUTH_URL).toBe('http://localhost:8080')
  expect(NATS_URL).toBe('ws://localhost:9222')
  expect(SITE_ID).toBe('site-local')
})

it('reads AUTH_URL/NATS_URL/SITE_ID from window.__APP_CONFIG__', async () => {
  window.__APP_CONFIG__ = { AUTH_URL: 'https://auth.a.example.com', NATS_URL: 'wss://nats.a.example.com', SITE_ID: 'site-a' }
  const { AUTH_URL, NATS_URL, SITE_ID } = await import('./runtimeConfig.js')
  expect(AUTH_URL).toBe('https://auth.a.example.com')
  expect(NATS_URL).toBe('wss://nats.a.example.com')
  expect(SITE_ID).toBe('site-a')
})
```

(Keep any test that resets modules/`window.__APP_CONFIG__` between cases.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd chat-frontend && npm test -- runtimeConfig`
Expected: FAIL — `AUTH_URL`/`NATS_URL`/`SITE_ID` undefined; `PORTAL_URL` still exported.

- [ ] **Step 3: Rewrite `runtimeConfig.js`**

Replace the `PORTAL_URL` export with:

```js
export const AUTH_URL =
  runtime.AUTH_URL || import.meta.env.VITE_AUTH_URL || 'http://localhost:8080'

export const NATS_URL =
  runtime.NATS_URL || import.meta.env.VITE_NATS_URL || 'ws://localhost:9222'

export const SITE_ID =
  runtime.SITE_ID || import.meta.env.VITE_SITE_ID || 'site-local'
```

Leave `DEV_MODE`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID` unchanged.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd chat-frontend && npm test -- runtimeConfig`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/runtimeConfig.js chat-frontend/src/lib/runtimeConfig.test.js
git commit -m "feat(frontend): static AUTH_URL/NATS_URL/SITE_ID config"
```

---

## Task 11: `NatsContext.connect()` drops the portal lookup

**Files:**
- Modify: `chat-frontend/src/context/NatsContext/NatsContext.jsx`
- Test: `chat-frontend/src/context/NatsContext/NatsContext.test.jsx`

- [ ] **Step 1: Update the test**

In `NatsContext.test.jsx`, remove the `/api/userInfo` mock branch and its `toHaveBeenNthCalledWith(1, '.../api/userInfo...')` assertion. Assert that `connect({ mode: 'sso', ssoToken, account })`:
- calls `fetch` with the static `AUTH_URL` + `/auth` (no `/api/userInfo` call),
- dials NATS with `servers: 'ws://localhost:9222'` (the default `NATS_URL`),
- sets `user.siteId` to `'site-local'` (the default `SITE_ID`).

```js
it('connect() posts to AUTH_URL/auth and tags user.siteId from SITE_ID', async () => {
  global.fetch = vi.fn(async () => ({
    ok: true,
    json: async () => ({ natsJwt: 'jwt', user: { account: 'alice' } }),
  }))
  // ...render the provider, call connect({ mode: 'sso', ssoToken: 't', account: 'alice' })...
  expect(global.fetch).toHaveBeenCalledTimes(1)
  expect(global.fetch).toHaveBeenCalledWith('http://localhost:8080/auth', expect.any(Object))
  expect(natsConnectMock).toHaveBeenCalledWith(expect.objectContaining({ servers: 'ws://localhost:9222' }))
  // user.siteId === 'site-local'
})
```

(Match the file's existing render/mock harness — reuse its `natsConnect` mock and provider-render helper.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd chat-frontend && npm test -- NatsContext`
Expected: FAIL — connect still fetches `/api/userInfo` from `PORTAL_URL`.

- [ ] **Step 3: Rewrite `connect()`**

In `NatsContext.jsx`:

Change the import (line 4): `import { AUTH_URL, NATS_URL, SITE_ID } from '@/lib/runtimeConfig'`.

Replace `authUrlRef` + `getAuthUrl` (lines ~41-42) with a static getter:

```js
  const getAuthUrl = useCallback(() => AUTH_URL, [])
```

Replace the body of `connectToNats` (the portal-lookup + auth + connect block) with:

```js
  const connectToNats = useCallback(async (opts) => {
    const myGen = ++connectGenRef.current
    setError(null)
    const { mode, account, ssoToken } = opts || {}

    const nkey = createUser()
    const natsPublicKey = nkey.getPublicKey()
    const body = mode === 'sso' ? { ssoToken, natsPublicKey } : { account, natsPublicKey }

    const authResp = await fetch(`${AUTH_URL}/auth`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    if (!authResp.ok) {
      await throwEnvelopeError(authResp, 'Auth failed')
    }
    const { natsJwt, user: userInfo } = await authResp.json()

    try {
      setCredentials({ jwt: natsJwt, seed: nkey.getSeed(), natsPublicKey, refreshable: mode === 'sso' })
      const nc = await natsConnect({ servers: NATS_URL, authenticator })
      ncRef.current = nc
      setUser({ ...userInfo, siteId: SITE_ID })
      setConnected(true)
      nc.closed().then((err) => {
        if (myGen !== connectGenRef.current) return
        if (err) setError(`Disconnected: ${err.message}`)
        setConnected(false)
      })
    } catch (err) {
      stop()
      throw err
    }
  }, [authenticator, setCredentials, stop])
```

Remove the now-unused `authUrlRef` ref declaration.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd chat-frontend && npm test -- NatsContext`
Expected: PASS.

- [ ] **Step 5: Typecheck + commit**

Run: `cd chat-frontend && npm run typecheck`
Expected: clean.

```bash
git add chat-frontend/src/context/NatsContext/NatsContext.jsx chat-frontend/src/context/NatsContext/NatsContext.test.jsx
git commit -m "feat(frontend): connect via static AUTH_URL/NATS_URL, siteId from SITE_ID"
```

---

## Task 12: deploy config plumbing

**Files:**
- Modify: `chat-frontend/deploy/config.js.template`, `chat-frontend/deploy/30-render-config.sh`, `chat-frontend/deploy/docker-compose.yml`, `docker-local/setup.sh`

- [ ] **Step 1: Update `config.js.template`**

Replace the `PORTAL_URL` line with:

```js
  AUTH_URL: "${AUTH_URL}",
  NATS_URL: "${NATS_URL}",
  SITE_ID: "${SITE_ID}",
```

- [ ] **Step 2: Update `30-render-config.sh`**

Replace the `PORTAL_URL` require/export/envsubst with `AUTH_URL`/`NATS_URL`/`SITE_ID`:

```sh
: "${AUTH_URL:?AUTH_URL is required (auth-service base URL)}"
: "${NATS_URL:?NATS_URL is required (NATS WebSocket URL)}"
: "${SITE_ID:?SITE_ID is required}"
: "${DEV_MODE:=false}"
: "${OIDC_ISSUER_URL:=}"
: "${OIDC_CLIENT_ID:=nats-chat}"
export AUTH_URL NATS_URL SITE_ID DEV_MODE OIDC_ISSUER_URL OIDC_CLIENT_ID

envsubst '${AUTH_URL} ${NATS_URL} ${SITE_ID} ${DEV_MODE} ${OIDC_ISSUER_URL} ${OIDC_CLIENT_ID}' \
  < /etc/config.js.template \
  > /usr/share/nginx/html/config.js
```

Update the trailing `echo` to print the new vars.

- [ ] **Step 3: Update compose + setup.sh**

In `chat-frontend/deploy/docker-compose.yml`, replace `PORTAL_URL: ${PORTAL_URL:-...}` with `AUTH_URL`/`NATS_URL`/`SITE_ID` lines (defaults `http://localhost:8080`, `ws://localhost:9222`, `site-local`).

In `docker-local/setup.sh`, replace the `VITE_PORTAL_URL=...` lines (write/append) with:

```sh
VITE_AUTH_URL=http://localhost:8080
VITE_NATS_URL=ws://localhost:9222
VITE_SITE_ID=site-local
```

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/deploy/config.js.template chat-frontend/deploy/30-render-config.sh chat-frontend/deploy/docker-compose.yml docker-local/setup.sh
git commit -m "feat(frontend): deploy config serves static auth/NATS/site vars"
```

---

## Task 13: `docs/client-api.md`

**Files:**
- Modify: `docs/client-api.md`

- [ ] **Step 1: Update the connection narrative (§2.1)**

Rewrite §2.1's "three-step sequence" sentence to drop the portal lookup: login mints a NATS JWT at the statically-configured `AUTH_URL`, then the client connects to the static `NATS_URL`; `siteId` is static client config, not discovered. Note the new SSO front-door: `portal-service` also serves `GET /login` (→ Keycloak → 302 to the home-site `baseUrl`) — a browser redirect flow, not a JSON RPC.

- [ ] **Step 2: Trim the §2.3 response table**

In §2.3 (`GET /api/userInfo`), reduce the response field table and JSON example to `{account, employeeId, siteId, baseUrl}` (remove the `authServiceUrl` and `natsUrl` rows). Note the endpoint is retained but no longer on the connect path.

- [ ] **Step 3: Commit**

```bash
git add docs/client-api.md
git commit -m "docs: portal SSO front-door + trimmed /api/userInfo in client-api"
```

---

## Final verification

- [ ] `make generate SERVICE=portal-service` (mock current), `make fmt`.
- [ ] `make lint && make test SERVICE=portal-service` — green, ≥80% on changed files (`go tool cover -func`).
- [ ] `make sast` — gosec/govulncheck/semgrep clean (watch the cookie / `crypto/rand` / redirect / `InsecureSkipVerify` code).
- [ ] `make build SERVICE=portal-service`.
- [ ] `make test-integration SERVICE=portal-service` (Docker) — store/cache tests green after dropping `natsUrl`.
- [ ] `cd chat-frontend && npm run typecheck && npm test && npm run build`.

---

## Self-review notes

- **Spec coverage:** `/login`+`/auth/callback` (T6–7), confidential OIDC client (T1–2), config + validation (T3), cookie/state/nonce (T4), `/api/userInfo` trim + dead-field cleanup (T5), dev-mode guard (T6 + T8), main wiring (T8), Keycloak client + deploy (T9), frontend static revert (T10–12), docs (T13). The "multi-site `nats-chat` redirect URIs" item is ops/IaC (spec out-of-scope) — not a code task.
- **Nonce:** enforced in T2 (`idTok.Nonce != expectedNonce`) with a wrong-nonce test — matches the spec's "go-oidc doesn't check nonce automatically."
- **Type consistency:** `resolveSite` (returns `employee, siteURL, error`) is the spec's shared resolver, used by `HandleUserInfo` (T5) and `HandleAuthCallback` (T7); `loginAuthenticator` signature is identical in T1, the mock, and the handlers.
- **Risk to verify during impl:** the `go-jose/v4` import major version (T2) — confirm against `go.sum` after `go mod tidy`.
