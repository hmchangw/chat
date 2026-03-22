package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// TokenVerifier verifies an SSO token and returns the username.
// Implementations handle the actual SSO/OAuth verification logic.
type TokenVerifier interface {
	Verify(token string) (username string, err error)
}

// AuthHandler processes NATS auth_callout requests.
type AuthHandler struct {
	verifier   TokenVerifier
	signingKey nkeys.KeyPair
}

// NewAuthHandler creates an AuthHandler with the given token verifier
// and NATS account signing key for issuing user JWTs.
func NewAuthHandler(verifier TokenVerifier, signingKey nkeys.KeyPair) *AuthHandler {
	return &AuthHandler{
		verifier:   verifier,
		signingKey: signingKey,
	}
}

// Handle processes an authorization request from the NATS server.
// It extracts the SSO token from ConnectOptions, verifies it, and
// returns a signed user JWT with scoped permissions.
func (h *AuthHandler) Handle(req *jwt.AuthorizationRequest) (string, error) {
	token := req.ConnectOptions.Token
	if token == "" {
		return "", errors.New("missing auth token")
	}

	username, err := h.verifier.Verify(token)
	if err != nil {
		return "", err
	}

	uc := jwt.NewUserClaims(req.UserNkey)
	uc.Audience = "$G"
	uc.Expires = time.Now().Add(2 * time.Hour).Unix()

	// Publish permissions: user's own namespace + inbox for request-reply.
	uc.Pub.Allow.Add(fmt.Sprintf("chat.user.%s.>", username))
	uc.Pub.Allow.Add("_INBOX.>")

	// Subscribe permissions: user's own namespace, all rooms, and inbox.
	uc.Sub.Allow.Add(fmt.Sprintf("chat.user.%s.>", username))
	uc.Sub.Allow.Add("chat.room.>")
	uc.Sub.Allow.Add("_INBOX.>")

	return uc.Encode(h.signingKey)
}

// Authorizer returns an AuthorizerFn compatible with the callout.go library.
// This bridges the AuthHandler to the callout service registration.
func (h *AuthHandler) Authorizer() func(req *jwt.AuthorizationRequest) (string, error) {
	return h.Handle
}
