package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errhttp"
	"github.com/hmchangw/chat/pkg/subject"
)

// siteIDPlaceholder is the token in PORTAL_AUTH_URL_TEMPLATE that is replaced
// with the employee's siteId to form the home site's auth-service URL.
const siteIDPlaceholder = "{siteId}"

type userInfoResponse struct {
	Account        string `json:"account"`
	EmployeeID     string `json:"employeeId"`
	AuthServiceURL string `json:"authServiceUrl"`
	NATSURL        string `json:"natsUrl"`
	SiteID         string `json:"siteId"`
}

// PortalHandler resolves a user's home-site coordinates from the in-memory
// directory cache. Discovery only: it serves non-secret directory data keyed by
// account and validates no token. The authoritative gate is auth-service, which
// validates the SSO token and enforces provisioning before minting a JWT.
type PortalHandler struct {
	cache              *directoryCache
	devMode            bool
	devFallbackSiteID  string
	devFallbackNatsURL string
	authURLTemplate    string
}

// NewPortalHandler creates a PortalHandler. devMode synthesizes a dev-site
// entry for accounts absent from the directory so local logins need no seeding.
// authURLTemplate maps a siteId to that site's auth-service base URL by
// substituting "{siteId}"; a value without the placeholder is used verbatim
// (single-site deployments).
func NewPortalHandler(cache *directoryCache, devMode bool, devFallbackSiteID, devFallbackNatsURL, authURLTemplate string) *PortalHandler {
	return &PortalHandler{
		cache:              cache,
		devMode:            devMode,
		devFallbackSiteID:  devFallbackSiteID,
		devFallbackNatsURL: devFallbackNatsURL,
		authURLTemplate:    authURLTemplate,
	}
}

// authServiceURL derives a site's auth-service base URL from the template.
func (h *PortalHandler) authServiceURL(siteID string) string {
	return strings.ReplaceAll(h.authURLTemplate, siteIDPlaceholder, siteID)
}

// HandleUserInfo resolves the home-site coordinates for the `account` query
// parameter. The frontend supplies the account directly — derived from the SSO
// token's preferred_username claim in production, or the dev login form in dev.
// No token is validated here; this endpoint is discovery only.
func (h *PortalHandler) HandleUserInfo(c *gin.Context) {
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	account := c.Query("account")
	if account == "" {
		errhttp.Write(ctx, c, errcode.BadRequest("account is required",
			errcode.WithReason(errcode.AuthMissingFields)))
		return
	}
	h.resolve(ctx, c, account)
}

// resolve answers from the directory cache — a single in-memory lookup, no
// per-request datastore round trips. In devMode an account absent from the
// directory is synthesized onto the dev site.
func (h *PortalHandler) resolve(ctx context.Context, c *gin.Context, account string) {
	if !subject.IsValidAccountToken(account) {
		errhttp.Write(ctx, c, errcode.BadRequest("account must be a single NATS subject token (no '.', '*', '>' or whitespace)"))
		return
	}
	ctx = errcode.WithLogValues(ctx, "account", account)

	e, ok := h.cache.Get(account)
	if !ok {
		if !h.devMode {
			errhttp.Write(ctx, c, errcode.Forbidden("account not ready for chat",
				errcode.WithReason(errcode.PortalAccountNotReady)))
			return
		}
		e = employee{Account: account, SiteID: h.devFallbackSiteID, NATSURL: h.devFallbackNatsURL}
	}

	c.JSON(http.StatusOK, userInfoResponse{
		Account:        e.Account,
		EmployeeID:     e.EmployeeID,
		AuthServiceURL: h.authServiceURL(e.SiteID),
		NATSURL:        e.NATSURL,
		SiteID:         e.SiteID,
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
