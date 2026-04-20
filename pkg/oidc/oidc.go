package oidc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Claims holds the validated identity extracted from an OIDC token.
type Claims struct {
	Subject           string
	Email             string
	Name              string
	PreferredUsername string
	GivenName         string
	FamilyName        string
	Description       string
	DeptID            string
	DeptName          string
	Extra             map[string]interface{}
}

// ErrTokenExpired is returned when the SSO token has passed its expiry time.
var ErrTokenExpired = fmt.Errorf("oidc: token has expired")

// Config controls how the OIDC validator behaves.
type Config struct {
	IssuerURL     string
	Audience      string
	TLSSkipVerify bool
}

// Validator verifies OIDC tokens against an issuer's JWKS endpoint.
type Validator struct {
	verifier   *oidc.IDTokenVerifier
	httpClient *http.Client
	audience   string
}

const issuerDiscoveryTimeout = 10 * time.Second

// NewValidator connects to the OIDC issuer and fetches its JWKS keys.
// Fails fast if the issuer is unreachable.
func NewValidator(ctx context.Context, cfg Config) (*Validator, error) {
	var httpClient *http.Client

	if cfg.TLSSkipVerify {
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // intentional for dev environments
			},
		}
		httpClient = &http.Client{
			Transport: transport,
			Timeout:   issuerDiscoveryTimeout,
		}
		ctx = oidc.ClientContext(ctx, httpClient)
	}

	// Ensure issuer discovery cannot hang indefinitely.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, issuerDiscoveryTimeout)
		defer cancel()
	}

	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("connect to oidc issuer %q: %w", cfg.IssuerURL, err)
	}

	oidcConfig := &oidc.Config{
		ClientID: cfg.Audience,
	}

	return &Validator{
		verifier:   provider.Verifier(oidcConfig),
		httpClient: httpClient,
		audience:   cfg.Audience,
	}, nil
}

// Validate verifies the raw OIDC token string and extracts user claims.
// Returns ErrTokenExpired if the token's exp claim is in the past — expiry
// is enforced by go-oidc's Verifier, we just translate its sentinel error.
func (v *Validator) Validate(ctx context.Context, rawToken string) (Claims, error) {
	// Re-attach the custom HTTP client so JWKS fetches also use TLSSkipVerify.
	if v.httpClient != nil {
		ctx = oidc.ClientContext(ctx, v.httpClient)
	}

	idToken, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		var expErr *oidc.TokenExpiredError
		if errors.As(err, &expErr) {
			return Claims{}, ErrTokenExpired
		}
		return Claims{}, fmt.Errorf("oidc token verification failed: %w", err)
	}

	var tokenClaims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		GivenName         string `json:"given_name"`
		FamilyName        string `json:"family_name"`
		Description       string `json:"description"`
		DeptID            string `json:"deptid"`
		DeptName          string `json:"deptname"`
	}

	if err := idToken.Claims(&tokenClaims); err != nil {
		return Claims{}, fmt.Errorf("parse oidc token claims: %w", err)
	}

	// Parse all claims into Extra for custom fields (roles, groups, etc.)
	var allClaims map[string]interface{}
	if err := idToken.Claims(&allClaims); err != nil {
		return Claims{}, fmt.Errorf("parse oidc extra claims: %w", err)
	}
	for _, key := range []string{
		"sub", "email", "name", "preferred_username",
		"given_name", "family_name", "description", "deptid", "deptname",
		"iss", "aud", "exp", "iat", "nbf", "jti",
		"typ", "sid", "at_hash", "email_verified",
	} {
		delete(allClaims, key)
	}

	return Claims{
		Subject:           idToken.Subject,
		Email:             tokenClaims.Email,
		Name:              tokenClaims.Name,
		PreferredUsername: tokenClaims.PreferredUsername,
		GivenName:         tokenClaims.GivenName,
		FamilyName:        tokenClaims.FamilyName,
		Description:       tokenClaims.Description,
		DeptID:            tokenClaims.DeptID,
		DeptName:          tokenClaims.DeptName,
		Extra:             allClaims,
	}, nil
}
