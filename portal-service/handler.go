package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nkeys"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errhttp"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
)

// TokenVerifier validates an SSO token and returns OIDC claims.
type TokenVerifier interface {
	Validate(ctx context.Context, rawToken string) (pkgoidc.Claims, error)
}

type portalAuthRequest struct {
	SSOToken      string `json:"ssoToken" binding:"required"`
	NATSPublicKey string `json:"natsPublicKey" binding:"required"`
}

type devPortalAuthRequest struct {
	Account       string `json:"account" binding:"required"`
	NATSPublicKey string `json:"natsPublicKey" binding:"required"`
}

// portalAuthResponse relays the upstream user untouched and adds natsUrl.
type portalAuthResponse struct {
	NATSJWT string          `json:"natsJwt"`
	NATSURL string          `json:"natsUrl"`
	User    json.RawMessage `json:"user"`
}

type redirectResponse struct {
	RedirectTo string `json:"redirectTo"`
}

// upstreamAuthResponse is the auth-service /auth reply the portal relays.
type upstreamAuthResponse struct {
	NATSJWT string          `json:"natsJwt"`
	User    json.RawMessage `json:"user"`
}

// PortalHandler brokers credential issuance: verify SSO token, gate on the
// in-memory directory, route to the home site's auth-service.
type PortalHandler struct {
	verifier        TokenVerifier
	store           DirectoryStore
	dir             *dirMap
	forwarder       AuthForwarder
	siteAuth        map[string]string
	siteNATS        map[string]string
	siteFrontend    map[string]string   // siteID → normalized frontend origin
	frontendOrigins map[string]struct{} // normalized origin set (redirect check + CORS allowlist)
	adminBearer     string              // precomputed "Bearer <token>" for the reload guard
	devMode         bool
}

// PortalHandlerParams wires PortalHandler dependencies and site maps.
type PortalHandlerParams struct {
	Verifier         TokenVerifier
	Store            DirectoryStore
	Dir              *dirMap
	Forwarder        AuthForwarder
	SiteAuthURLs     map[string]string
	SiteNATSURLs     map[string]string
	SiteFrontendURLs map[string]string
	AdminToken       string
	DevMode          bool
}

// trimSiteMap trims whitespace around env-supplied site IDs and URLs.
func trimSiteMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

func NewPortalHandler(p *PortalHandlerParams) (*PortalHandler, error) {
	if p.AdminToken == "" {
		return nil, fmt.Errorf("admin token must not be empty")
	}
	siteAuth := trimSiteMap(p.SiteAuthURLs)
	siteNATS := trimSiteMap(p.SiteNATSURLs)
	rawFrontend := trimSiteMap(p.SiteFrontendURLs)
	siteFrontend := make(map[string]string, len(rawFrontend))
	frontendOrigins := make(map[string]struct{}, len(rawFrontend))
	for siteID, raw := range rawFrontend {
		origin, err := normalizeOrigin(raw)
		if err != nil {
			return nil, fmt.Errorf("normalize frontend url for site %q: %w", siteID, err)
		}
		siteFrontend[siteID] = origin
		frontendOrigins[origin] = struct{}{}
	}
	return &PortalHandler{
		verifier:        p.Verifier,
		store:           p.Store,
		dir:             p.Dir,
		forwarder:       p.Forwarder,
		siteAuth:        siteAuth,
		siteNATS:        siteNATS,
		siteFrontend:    siteFrontend,
		frontendOrigins: frontendOrigins,
		adminBearer:     "Bearer " + p.AdminToken,
		devMode:         p.DevMode,
	}, nil
}

// HandleSessionNATSJWT is the client-facing credential entry (issue + refresh).
func (h *PortalHandler) HandleSessionNATSJWT(c *gin.Context) {
	if h.devMode {
		h.handleDevSession(c)
		return
	}
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	var req portalAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errhttp.Write(ctx, c, errcode.BadRequest("ssoToken and natsPublicKey are required",
			errcode.WithReason(errcode.AuthMissingFields)))
		return
	}
	if !nkeys.IsValidPublicUserKey(req.NATSPublicKey) {
		errhttp.Write(ctx, c, errcode.BadRequest("invalid natsPublicKey format",
			errcode.WithReason(errcode.AuthInvalidNKey)))
		return
	}

	claims, err := h.verifier.Validate(ctx, req.SSOToken)
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
		// Blank account would allow chat.user..> permissions — refuse.
		errhttp.Write(ctx, c, errcode.Unauthenticated("token missing account claim",
			errcode.WithReason(errcode.AuthInvalidToken)))
		return
	}
	ctx = errcode.WithLogValues(ctx, "account", account)

	body, err := json.Marshal(portalAuthRequest{SSOToken: req.SSOToken, NATSPublicKey: req.NATSPublicKey})
	if err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("marshal forward body: %w", err))
		return
	}
	h.completeSession(ctx, c, account, body)
}

// handleDevSession accepts {account, natsPublicKey} without OIDC; local dev only.
func (h *PortalHandler) handleDevSession(c *gin.Context) {
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	var req devPortalAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errhttp.Write(ctx, c, errcode.BadRequest("account and natsPublicKey are required",
			errcode.WithReason(errcode.AuthMissingFields)))
		return
	}
	if !nkeys.IsValidPublicUserKey(req.NATSPublicKey) {
		errhttp.Write(ctx, c, errcode.BadRequest("invalid natsPublicKey format",
			errcode.WithReason(errcode.AuthInvalidNKey)))
		return
	}
	if strings.ContainsAny(req.Account, "*> \t") {
		errhttp.Write(ctx, c, errcode.BadRequest("invalid account format"))
		return
	}
	ctx = errcode.WithLogValues(ctx, "account", req.Account)

	body, err := json.Marshal(req)
	if err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("marshal forward body: %w", err))
		return
	}
	h.completeSession(ctx, c, req.Account, body)
}

// completeSession runs the shared tail: gate → redirect → forward → relay.
func (h *PortalHandler) completeSession(ctx context.Context, c *gin.Context, account string, forwardBody []byte) {
	rec, found := h.dir.Lookup(account)
	denyCase := ""
	switch {
	case !found:
		denyCase = "no_record"
	case rec.SiteID == "":
		denyCase = "empty_site_id"
	default:
		if _, ok := h.siteAuth[rec.SiteID]; !ok {
			denyCase = "site_missing_auth_url"
		} else if _, ok := h.siteNATS[rec.SiteID]; !ok {
			denyCase = "site_missing_nats_url"
		}
	}
	if denyCase != "" {
		vals := []any{"deny_case", denyCase}
		if found {
			vals = append(vals, "site_id", rec.SiteID)
		}
		ctx = errcode.WithLogValues(ctx, vals...)
		errhttp.Write(ctx, c, errcode.Forbidden("account is not ready for chat",
			errcode.WithReason(errcode.PortalAccountNotReady)))
		return
	}

	if target, redirect := h.crossSiteRedirect(c.GetHeader("Origin"), rec.SiteID); redirect {
		slog.InfoContext(ctx, "cross-site login redirected home", "request_id", c.GetString("request_id"), "account", account, "home_site", rec.SiteID)
		c.JSON(http.StatusOK, redirectResponse{RedirectTo: target})
		return
	}

	status, respBody, err := h.forwarder.Forward(ctx, h.siteAuth[rec.SiteID], forwardBody)
	if err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("forward auth request: %w", err))
		return
	}
	if status != http.StatusOK {
		// Relay a well-formed upstream errcode envelope as-is; anything else is internal.
		if e, ok := errcode.Parse(respBody); ok && e.Code.Valid() {
			errhttp.Write(ctx, c, e)
			return
		}
		errhttp.Write(ctx, c, fmt.Errorf("auth-service returned unexpected status %d", status))
		return
	}

	var upstream upstreamAuthResponse
	if err := json.Unmarshal(respBody, &upstream); err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("decode auth-service response: %w", err))
		return
	}
	if upstream.NATSJWT == "" {
		errhttp.Write(ctx, c, errors.New("auth-service response missing natsJwt"))
		return
	}
	c.JSON(http.StatusOK, portalAuthResponse{
		NATSJWT: upstream.NATSJWT,
		NATSURL: h.siteNATS[rec.SiteID],
		User:    upstream.User,
	})
}

// crossSiteRedirect reports whether a known foreign frontend should be sent
// home. Origin is a UX signal only — the gate is the token + directory.
func (h *PortalHandler) crossSiteRedirect(origin, homeSiteID string) (string, bool) {
	if origin == "" {
		return "", false
	}
	homeOrigin, hasHome := h.siteFrontend[homeSiteID]
	if !hasHome {
		return "", false
	}
	norm, err := normalizeOrigin(origin)
	if err != nil {
		return "", false
	}
	if _, known := h.frontendOrigins[norm]; !known {
		return "", false
	}
	if norm == homeOrigin {
		return "", false
	}
	return homeOrigin, true
}

// HandleCacheReload re-runs LoadAll and swaps the map; cron-driven, bearer-guarded.
func (h *PortalHandler) HandleCacheReload(c *gin.Context) {
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte(h.adminBearer)) != 1 {
		errhttp.Write(ctx, c, errcode.Unauthenticated("invalid admin token"))
		return
	}
	n, err := h.dir.Reload(ctx, h.store)
	if err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("reload directory: %w", err))
		return
	}
	slog.InfoContext(ctx, "directory reloaded", "request_id", c.GetString("request_id"), "records", n)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "records": n})
}

func (h *PortalHandler) HandleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "records": h.dir.Len()})
}
