package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
)

const maxAccountLength = 64

// validateAccount ensures the account name is safe to interpolate into NATS
// subject permission patterns. NATS subjects use '.', '*', '>' as delimiters
// and wildcards; any of those characters in an account would widen the
// issued JWT's permissions beyond the intended user namespace.
func validateAccount(account string) error {
	if account == "" {
		return errors.New("account is empty")
	}
	if len(account) > maxAccountLength {
		return fmt.Errorf("account exceeds %d characters", maxAccountLength)
	}
	for _, r := range account {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return fmt.Errorf("account contains unsupported character %q", r)
		}
	}
	return nil
}

// TokenValidator validates an SSO token and returns OIDC claims.
type TokenValidator interface {
	Validate(ctx context.Context, rawToken string) (pkgoidc.Claims, error)
}

type authRequest struct {
	SSOToken      string `json:"ssoToken" binding:"required"`
	NATSPublicKey string `json:"natsPublicKey" binding:"required"`
}

type devAuthRequest struct {
	Account       string `json:"account" binding:"required"`
	NATSPublicKey string `json:"natsPublicKey" binding:"required"`
}

type authResponse struct {
	NATSJWT  string       `json:"natsJwt"`
	UserInfo userInfoResp `json:"user"`
}

type userInfoResp struct {
	Email       string `json:"email"`
	Account     string `json:"account"`
	EmployeeID  string `json:"employeeId"`
	EngName     string `json:"engName"`
	ChineseName string `json:"chineseName"`
	DeptName    string `json:"deptName"`
	DeptID      string `json:"deptId"`
}

// AuthHandler processes auth requests, validates SSO tokens via OIDC,
// and returns signed NATS user JWTs with scoped permissions.
type AuthHandler struct {
	validator  TokenValidator
	signingKey nkeys.KeyPair
	jwtExpiry  time.Duration
	devMode    bool
}

// NewAuthHandler creates an AuthHandler with the given token validator,
// NATS account signing key, and JWT expiry duration.
func NewAuthHandler(validator TokenValidator, signingKey nkeys.KeyPair, jwtExpiry time.Duration, devMode bool) *AuthHandler {
	return &AuthHandler{
		validator:  validator,
		signingKey: signingKey,
		jwtExpiry:  jwtExpiry,
		devMode:    devMode,
	}
}

// HandleAuth validates the SSO token, resolves permissions based on
// the user account, and returns a signed NATS JWT.
func (h *AuthHandler) HandleAuth(c *gin.Context) {
	if h.devMode {
		h.handleDevAuth(c)
		return
	}

	var req authRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ssoToken and natsPublicKey are required"})
		return
	}

	if !nkeys.IsValidPublicUserKey(req.NATSPublicKey) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid natsPublicKey format"})
		return
	}

	claims, err := h.validator.Validate(c.Request.Context(), req.SSOToken)
	if err != nil {
		if errors.Is(err, pkgoidc.ErrTokenExpired) {
			slog.Warn("sso token expired", "error", err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "SSO token has expired, please re-login"})
			return
		}
		slog.Error("oidc validation failed", "error", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid SSO token"})
		return
	}

	account := claims.PreferredUsername
	if account == "" {
		account = claims.Name
	}
	if err := validateAccount(account); err != nil {
		slog.Warn("oidc claims produced invalid account", "error", err, "subject", claims.Subject)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user account is invalid"})
		return
	}

	natsJWT, err := h.signNATSJWT(req.NATSPublicKey, account)
	if err != nil {
		slog.Error("nats jwt signing failed", "error", err, "account", account)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate NATS token"})
		return
	}

	slog.Debug("auth success", "account", account, "subject", claims.Subject)

	// Parse description field: "employeeId, engName, chineseName"
	employeeID, engName, chineseName := parseDescription(claims.Description)

	c.JSON(http.StatusOK, authResponse{
		NATSJWT: natsJWT,
		UserInfo: userInfoResp{
			Email:       claims.Email,
			Account:     account,
			EmployeeID:  employeeID,
			EngName:     engName,
			ChineseName: chineseName,
			DeptName:    claims.DeptName,
			DeptID:      claims.DeptID,
		},
	})
}

// handleDevAuth handles auth in dev mode: accepts account name directly
// without OIDC validation, for use during local development only.
func (h *AuthHandler) handleDevAuth(c *gin.Context) {
	var req devAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "account and natsPublicKey are required"})
		return
	}

	if !nkeys.IsValidPublicUserKey(req.NATSPublicKey) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid natsPublicKey format"})
		return
	}

	if err := validateAccount(req.Account); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account"})
		return
	}

	natsJWT, err := h.signNATSJWT(req.NATSPublicKey, req.Account)
	if err != nil {
		slog.Error("nats jwt signing failed", "error", err, "account", req.Account)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate NATS token"})
		return
	}

	slog.Debug("dev auth success", "account", req.Account)

	c.JSON(http.StatusOK, authResponse{
		NATSJWT: natsJWT,
		UserInfo: userInfoResp{
			Email:   req.Account + "@dev.local",
			Account: req.Account,
			EngName: req.Account,
		},
	})
}

// signNATSJWT creates a signed NATS user JWT with permissions scoped
// to the user's namespace and standard chat subjects.
func (h *AuthHandler) signNATSJWT(userPubKey, account string) (string, error) {
	uc := jwt.NewUserClaims(userPubKey)
	uc.Expires = time.Now().Add(h.jwtExpiry).Unix()

	// Publish permissions: user's own namespace + inbox for request-reply.
	uc.Pub.Allow.Add(fmt.Sprintf("chat.user.%s.>", account))
	uc.Pub.Allow.Add("_INBOX.>")

	// Subscribe permissions: user's own namespace, all rooms, and inbox.
	uc.Sub.Allow.Add(fmt.Sprintf("chat.user.%s.>", account))
	uc.Sub.Allow.Add("chat.room.>")
	uc.Sub.Allow.Add("_INBOX.>")

	return uc.Encode(h.signingKey)
}

// parseDescription splits the description field "employeeId, engName, chineseName"
// into its three components.
func parseDescription(desc string) (employeeID, engName, chineseName string) {
	parts := strings.SplitN(desc, ",", 3)
	if len(parts) >= 1 {
		employeeID = strings.TrimSpace(parts[0])
	}
	if len(parts) >= 2 {
		engName = strings.TrimSpace(parts[1])
	}
	if len(parts) >= 3 {
		chineseName = strings.TrimSpace(parts[2])
	}
	return
}

func (h *AuthHandler) HandleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
