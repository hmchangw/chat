// Package keycloak is a small Keycloak token client for use from tests
// (e2e suites and integration tests). It performs the OAuth 2.0 Resource
// Owner Password Credentials grant against a Keycloak realm and returns
// the issued tokens — direct user impersonation that's only acceptable
// in test code.
//
// The package is dependency-free at the Keycloak topology level — callers
// supply the baseURL of an already-running Keycloak (compose, testcontainers,
// or a shared dev instance). Realm import / user provisioning is the
// caller's responsibility.
package keycloak

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-resty/resty/v2"

	"github.com/hmchangw/chat/pkg/restyutil"
)

// Sentinel errors for the OAuth failure modes test code typically branches on.
var (
	ErrInvalidCredentials = errors.New("keycloak: invalid credentials")
	ErrUnknownClient      = errors.New("keycloak: unknown or unauthorized client")
	ErrRealmNotFound      = errors.New("keycloak: realm not found")
)

// Tokens mirrors the subset of the Keycloak token-endpoint response the
// tests care about.
type Tokens struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	TokenType        string `json:"token_type"`
}

// Error is the structured form of a non-2xx token response.
type Error struct {
	StatusCode  int
	OAuthError  string
	Description string
}

func (e *Error) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("keycloak: %d %s: %s", e.StatusCode, e.OAuthError, e.Description)
	}
	if e.OAuthError != "" {
		return fmt.Sprintf("keycloak: %d %s", e.StatusCode, e.OAuthError)
	}
	return fmt.Sprintf("keycloak: %d", e.StatusCode)
}

// Is lets errors.Is match a *Error against the sentinel categories
// based on status + OAuth error code.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrInvalidCredentials:
		return e.StatusCode == http.StatusUnauthorized && e.OAuthError == "invalid_grant"
	case ErrUnknownClient:
		return (e.StatusCode == http.StatusUnauthorized && e.OAuthError == "unauthorized_client") ||
			(e.StatusCode == http.StatusBadRequest && e.OAuthError == "invalid_client")
	case ErrRealmNotFound:
		return e.StatusCode == http.StatusNotFound
	}
	return false
}

// Client talks to a single Keycloak realm's token endpoint.
type Client struct {
	http  *resty.Client
	realm string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the underlying Resty client. Useful when callers
// already have a configured client (custom TLS, proxy, etc.).
func WithHTTPClient(c *resty.Client) Option {
	return func(k *Client) { k.http = c }
}

// NewClient creates a Keycloak token client for the given realm.
// baseURL is the Keycloak root, e.g. "http://localhost:8180".
func NewClient(baseURL, realm string, opts ...Option) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	c := &Client{
		http:  restyutil.New(baseURL),
		realm: realm,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// PasswordGrant performs an OAuth 2.0 Resource Owner Password Credentials grant.
func (c *Client) PasswordGrant(ctx context.Context, clientID, username, password string) (Tokens, error) {
	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {clientID},
		"username":   {username},
		"password":   {password},
	}
	return c.tokenRequest(ctx, form)
}

// AccessToken is a convenience wrapper around PasswordGrant returning just
// the access token string.
func (c *Client) AccessToken(ctx context.Context, clientID, username, password string) (string, error) {
	tok, err := c.PasswordGrant(ctx, clientID, username, password)
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// Refresh exchanges a refresh token for fresh access + refresh tokens.
func (c *Client) Refresh(ctx context.Context, clientID, refreshToken string) (Tokens, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}
	return c.tokenRequest(ctx, form)
}

func (c *Client) tokenRequest(ctx context.Context, form url.Values) (Tokens, error) {
	path := fmt.Sprintf("/realms/%s/protocol/openid-connect/token", c.realm)

	resp, err := c.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/x-www-form-urlencoded").
		SetHeader("Accept", "application/json").
		SetBody(form.Encode()).
		Post(path)
	if err != nil {
		return Tokens{}, fmt.Errorf("keycloak token request: %w", err)
	}

	if resp.IsError() {
		return Tokens{}, parseErrorResponse(resp)
	}

	var tokens Tokens
	if err := json.Unmarshal(resp.Body(), &tokens); err != nil {
		return Tokens{}, fmt.Errorf("keycloak token response: %w", err)
	}
	return tokens, nil
}

// parseErrorResponse builds a *Error from a non-2xx response, attempting
// to decode the standard OAuth error JSON. Non-JSON bodies (e.g. Keycloak's
// 404 "Realm does not exist" plain-text page) leave OAuthError/Description empty.
func parseErrorResponse(resp *resty.Response) error {
	var body struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(resp.Body(), &body) // best-effort; non-JSON is fine

	return &Error{
		StatusCode:  resp.StatusCode(),
		OAuthError:  body.Error,
		Description: body.ErrorDescription,
	}
}
