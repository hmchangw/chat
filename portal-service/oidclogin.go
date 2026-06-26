package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
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

// newOIDCLogin discovers the issuer and builds the oauth2 config + verifier.
func newOIDCLogin(ctx context.Context, issuer, clientID, secret, redirectURL string, scopes []string, httpClient *http.Client) (*oidcLogin, error) {
	provider, err := pkgoidc.DiscoverProvider(ctx, issuer, httpClient)
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
