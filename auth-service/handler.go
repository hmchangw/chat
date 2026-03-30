package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
)

// TokenValidator validates an SSO token and returns OIDC claims.
type TokenValidator interface {
	Validate(ctx context.Context, rawToken string) (pkgoidc.Claims, error)
}

type authRequest struct {
	SSOToken      string `json:"ssoToken" binding:"required"`
	NATSPublicKey string `json:"natsPublicKey" binding:"required"`
}

type authResponse struct {
	NATSJWT  string       `json:"natsJwt"`
	UserInfo userInfoResp `json:"user"`
}

type userInfoResp struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferredUsername"`
	GivenName         string `json:"givenName"`
	FamilyName        string `json:"familyName"`
}

// AuthHandler processes auth requests, validates SSO tokens via OIDC,
// and returns signed NATS user JWTs with scoped permissions.
type AuthHandler struct {
	validator  TokenValidator
	signingKey nkeys.KeyPair
	jwtExpiry  time.Duration
}

// NewAuthHandler creates an AuthHandler with the given token validator,
// NATS account signing key, and JWT expiry duration.
func NewAuthHandler(validator TokenValidator, signingKey nkeys.KeyPair, jwtExpiry time.Duration) *AuthHandler {
	return &AuthHandler{
		validator:  validator,
		signingKey: signingKey,
		jwtExpiry:  jwtExpiry,
	}
}

// HandleAuth validates the SSO token, resolves permissions based on
// the username, and returns a signed NATS JWT.
func (h *AuthHandler) HandleAuth(c *gin.Context) {
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

	username := claims.PreferredUsername
	if username == "" {
		username = claims.Subject
	}

	natsJWT, err := h.signNATSJWT(req.NATSPublicKey, username)
	if err != nil {
		slog.Error("nats jwt signing failed", "error", err, "username", username)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate NATS token"})
		return
	}

	slog.Info("auth success", "username", username, "subject", claims.Subject)

	c.JSON(http.StatusOK, authResponse{
		NATSJWT: natsJWT,
		UserInfo: userInfoResp{
			Subject:           claims.Subject,
			Email:             claims.Email,
			Name:              claims.Name,
			PreferredUsername: claims.PreferredUsername,
			GivenName:         claims.GivenName,
			FamilyName:        claims.FamilyName,
		},
	})
}

// signNATSJWT creates a signed NATS user JWT with permissions scoped
// to the user's namespace and standard chat subjects.
func (h *AuthHandler) signNATSJWT(userPubKey, username string) (string, error) {
	uc := jwt.NewUserClaims(userPubKey)
	uc.Expires = time.Now().Add(h.jwtExpiry).Unix()

	// Publish permissions: user's own namespace + inbox for request-reply.
	uc.Pub.Allow.Add(fmt.Sprintf("chat.user.%s.>", username))
	uc.Pub.Allow.Add("_INBOX.>")

	// Subscribe permissions: user's own namespace, all rooms, and inbox.
	uc.Sub.Allow.Add(fmt.Sprintf("chat.user.%s.>", username))
	uc.Sub.Allow.Add("chat.room.>")
	uc.Sub.Allow.Add("_INBOX.>")

	return uc.Encode(h.signingKey)
}

func (h *AuthHandler) HandleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
