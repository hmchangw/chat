package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errhttp"
	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/subject"
)

// siteURL holds a site's externally reachable base URL, looked up by siteId
// from the PORTAL_SITE_URLS registry.
type siteURL struct {
	BaseURL string `json:"baseUrl"`
}

// parseSiteURLs decodes the PORTAL_SITE_URLS registry — a JSON object mapping
// siteId to that site's URLs — and requires every site to carry baseUrl.
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

type userInfoResponse struct {
	Account    string `json:"account"`
	EmployeeID string `json:"employeeId"`
	SiteID     string `json:"siteId"`
	BaseURL    string `json:"baseUrl"`
}

// PortalHandler resolves a user's home-site coordinates from the in-memory
// directory cache. Discovery only: it serves non-secret directory data and
// validates no token. The authoritative gate is auth-service.
type PortalHandler struct {
	cache             *directoryCache
	devMode           bool
	devFallbackSiteID string
	sites             map[string]siteURL
	auth              loginAuthenticator
	cookieSecure      bool
}

// NewPortalHandler creates a PortalHandler. devMode synthesizes a dev-site
// entry for accounts absent from the directory so local logins need no seeding.
func NewPortalHandler(cache *directoryCache, devMode bool, devFallbackSiteID string,
	sites map[string]siteURL, auth loginAuthenticator, cookieSecure bool) *PortalHandler {
	return &PortalHandler{
		cache: cache, devMode: devMode, devFallbackSiteID: devFallbackSiteID,
		sites: sites, auth: auth, cookieSecure: cookieSecure,
	}
}

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

// HandleUserInfo resolves the home-site coordinates for the `account` query
// parameter. No token is validated here; this endpoint is discovery only.
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

// HandleHealth is the liveness probe: the process is up and serving HTTP.
func (h *PortalHandler) HandleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// HandleReady is the readiness probe: fails until the directory cache holds data.
func (h *PortalHandler) HandleReady(c *gin.Context) {
	if !h.cache.Ready() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

const handshakeCookie = "portal_oidc_handshake"
const handshakeTTL = 5 * time.Minute

func (h *PortalHandler) setHandshake(c *gin.Context, state, nonce string) {
	// #nosec G124 -- Secure follows PORTAL_COOKIE_SECURE (true by default); HttpOnly + SameSite=Lax set
	// nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure -- Secure follows PORTAL_COOKIE_SECURE (true by default); HttpOnly + SameSite=Lax set
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

func (h *PortalHandler) clearHandshake(c *gin.Context) {
	// #nosec G124 -- deletion cookie mirroring setHandshake (Secure via PORTAL_COOKIE_SECURE; HttpOnly + SameSite=Lax)
	// nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure -- deletion cookie mirroring setHandshake (Secure via PORTAL_COOKIE_SECURE; HttpOnly + SameSite=Lax)
	http.SetCookie(c.Writer, &http.Cookie{
		Name: handshakeCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

// HandleLogin starts the OIDC authorization flow. In dev mode it redirects
// directly to the fallback site's base URL so no OIDC provider is needed.
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
	// state (CSRF) + nonce (replay): opaque tokens, idgen.GenerateID() is crypto/rand base62.
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

func (h *PortalHandler) HandleAuthCallback(c *gin.Context) {
	// Dev mode wires no OIDC authenticator (h.auth is nil); the route still
	// exists, so short-circuit before any h.auth call would panic.
	if h.devMode {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	requestID := c.GetString("request_id")
	ctx := c.Request.Context()

	state, nonce, ok := readHandshake(c)
	if !ok || state == "" || c.Query("state") != state {
		slog.WarnContext(ctx, "portal callback: state check failed", "request_id", requestID)
		c.Redirect(http.StatusFound, "/login")
		return
	}
	h.clearHandshake(c)

	account, err := h.auth.ExchangeAndVerify(ctx, c.Query("code"), nonce)
	if err != nil {
		slog.WarnContext(ctx, "portal callback: exchange/verify failed", "request_id", requestID, "error", err)
		c.Redirect(http.StatusFound, "/login")
		return
	}

	_, site, err := h.resolveSite(account)
	switch {
	case errors.Is(err, errAccountNotReady):
		slog.WarnContext(ctx, "portal callback: account not ready", "request_id", requestID, "account", account, "reason", errcode.PortalAccountNotReady)
		writeHTMLError(c, http.StatusForbidden,
			"Your account isn't set up for chat yet — contact your administrator.")
		return
	case err != nil:
		slog.ErrorContext(ctx, "portal callback: resolve failed", "request_id", requestID, "error", err)
		writeHTMLError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.Redirect(http.StatusFound, site.BaseURL)
}
