package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errhttp"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
	"github.com/hmchangw/chat/pkg/subject"
)

// siteIDPlaceholder is the token in PORTAL_AUTH_URL_TEMPLATE that is replaced
// with the employee's siteId to form the home site's auth-service URL.
const siteIDPlaceholder = "{siteId}"

// TokenValidator validates an SSO token and returns OIDC claims.
type TokenValidator interface {
	Validate(ctx context.Context, rawToken string) (pkgoidc.Claims, error)
}

type lookupRequest struct {
	SSOToken string `json:"ssoToken" binding:"required"`
}

type devLookupRequest struct {
	Account string `json:"account" binding:"required"`
}

type lookupResponse struct {
	Account        string `json:"account"`
	EmployeeID     string `json:"employeeId"`
	AuthServiceURL string `json:"authServiceUrl"`
	NATSURL        string `json:"natsUrl"`
	SiteID         string `json:"siteId"`
}

// PortalHandler resolves a user's home-site coordinates from the in-memory
// directory cache. Discovery only — the authoritative provisioning gate is
// auth-service.
type PortalHandler struct {
	validator          TokenValidator
	cache              *directoryCache
	devMode            bool
	devFallbackSiteID  string
	devFallbackNatsURL string
	authURLTemplate    string
}

// NewPortalHandler creates a PortalHandler; devMode skips OIDC and enables the
// site fallback. authURLTemplate maps a siteId to that site's auth-service
// base URL by substituting "{siteId}"; a template without the placeholder is
// used verbatim (single-site deployments). Panics if validator is nil outside
// devMode — fail at startup.
func NewPortalHandler(validator TokenValidator, cache *directoryCache, devMode bool, devFallbackSiteID, devFallbackNatsURL, authURLTemplate string) *PortalHandler {
	if !devMode && validator == nil {
		panic("portal handler: validator is required when devMode is false")
	}
	return &PortalHandler{
		validator:          validator,
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

// HandleLookup validates the SSO token and resolves the account's home-site coordinates.
func (h *PortalHandler) HandleLookup(c *gin.Context) {
	if h.devMode {
		h.handleDevLookup(c)
		return
	}

	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	var req lookupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errhttp.Write(ctx, c, errcode.BadRequest("ssoToken is required",
			errcode.WithReason(errcode.AuthMissingFields)))
		return
	}

	claims, err := h.validator.Validate(ctx, req.SSOToken)
	if err != nil {
		if errors.Is(err, pkgoidc.ErrTokenExpired) {
			errhttp.Write(ctx, c, errcode.Unauthenticated("SSO token has expired, please re-login",
				errcode.WithReason(errcode.AuthTokenExpired)))
			return
		}
		errhttp.Write(ctx, c, errcode.Unauthenticated("invalid SSO token",
			errcode.WithReason(errcode.AuthInvalidToken),
			errcode.WithCause(err)))
		return
	}

	account := claims.Account()
	if account == "" {
		errhttp.Write(ctx, c, errcode.Unauthenticated("token missing account claim",
			errcode.WithReason(errcode.AuthInvalidToken)))
		return
	}

	h.resolve(ctx, c, account, false)
}

// handleDevLookup accepts a raw account without OIDC, for local development.
func (h *PortalHandler) handleDevLookup(c *gin.Context) {
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	var req devLookupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errhttp.Write(ctx, c, errcode.BadRequest("account is required",
			errcode.WithReason(errcode.AuthMissingFields)))
		return
	}
	h.resolve(ctx, c, req.Account, true)
}

// resolve answers from the directory cache — a single in-memory lookup, no
// per-request datastore round trips. devFallback substitutes the dev site so
// local logins need no per-account seeding.
func (h *PortalHandler) resolve(ctx context.Context, c *gin.Context, account string, devFallback bool) {
	if !subject.IsValidAccountToken(account) {
		errhttp.Write(ctx, c, errcode.BadRequest("account must be a single NATS subject token (no '.', '*', '>' or whitespace)"))
		return
	}
	ctx = errcode.WithLogValues(ctx, "account", account)

	e, ok := h.cache.Get(account)
	if !ok {
		if !devFallback {
			errhttp.Write(ctx, c, errcode.Forbidden("account not ready for chat",
				errcode.WithReason(errcode.PortalAccountNotReady)))
			return
		}
		e = employee{Account: account, SiteID: h.devFallbackSiteID, NATSURL: h.devFallbackNatsURL}
	}

	c.JSON(http.StatusOK, lookupResponse{
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
