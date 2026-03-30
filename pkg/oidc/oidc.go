package oidc

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Claims holds the validated identity extracted from an OIDC token.
type Claims struct {
	Subject           string
	Email             string
	Name              string
	PreferredUsername  string
	GivenName         string
	FamilyName        string
	Extra             map[string]interface{}
}

// ErrTokenExpired is returned when the SSO token has passed its expiry time.
var ErrTokenExpired = fmt.Errorf("oidc: token has expired")

// Config controls how the OIDC validator behaves.
type Config struct {
	IssuerURL     string
	Audience      string
	TLSSkipVerify bool

	// VerifyAZP checks the "azp" (authorized party) claim instead of "aud".
	// Keycloak often sets aud to "account" and puts the client_id in azp.
	VerifyAZP bool
}

// Validator verifies OIDC tokens against an issuer's JWKS endpoint.
type Validator struct {
	verifier  *oidc.IDTokenVerifier
	audience  string
	verifyAZP bool
}

// NewValidator connects to the OIDC issuer and fetches its JWKS keys.
// Fails fast if the issuer is unreachable.
func NewValidator(ctx context.Context, cfg Config) (*Validator, error) {
	if cfg.TLSSkipVerify {
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // intentional for dev environments
			},
		}
		client := &http.Client{Transport: transport}
		ctx = oidc.ClientContext(ctx, client)
	}

	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("connect to oidc issuer %q: %w", cfg.IssuerURL, err)
	}

	oidcConfig := &oidc.Config{
		ClientID: cfg.Audience,
	}

	// When verifying azp instead of aud, tell go-oidc to skip its aud check.
	if cfg.VerifyAZP {
		oidcConfig.SkipClientIDCheck = true
	}

	return &Validator{
		verifier:  provider.Verifier(oidcConfig),
		audience:  cfg.Audience,
		verifyAZP: cfg.VerifyAZP,
	}, nil
}

// Validate verifies the raw OIDC token string and extracts user claims.
// Returns ErrTokenExpired if the token's exp claim is in the past.
func (v *Validator) Validate(ctx context.Context, rawToken string) (Claims, error) {
	idToken, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		if isExpiredError(err) {
			return Claims{}, ErrTokenExpired
		}
		return Claims{}, fmt.Errorf("oidc token verification failed: %w", err)
	}

	if idToken.Expiry.Before(time.Now()) {
		return Claims{}, ErrTokenExpired
	}

	var tokenClaims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername  string `json:"preferred_username"`
		GivenName         string `json:"given_name"`
		FamilyName        string `json:"family_name"`
		AZP               string `json:"azp"`
	}

	if err := idToken.Claims(&tokenClaims); err != nil {
		return Claims{}, fmt.Errorf("parse oidc token claims: %w", err)
	}

	if v.verifyAZP {
		if tokenClaims.AZP != v.audience {
			return Claims{}, fmt.Errorf("oidc azp claim %q does not match expected audience %q", tokenClaims.AZP, v.audience)
		}
	}

	// Parse all claims into Extra for custom fields (roles, groups, etc.)
	var allClaims map[string]interface{}
	if err := idToken.Claims(&allClaims); err != nil {
		return Claims{}, fmt.Errorf("parse oidc extra claims: %w", err)
	}
	for _, key := range []string{
		"sub", "email", "name", "preferred_username",
		"given_name", "family_name",
		"iss", "aud", "exp", "iat", "nbf", "jti",
		"azp", "typ", "sid", "at_hash", "email_verified",
	} {
		delete(allClaims, key)
	}

	return Claims{
		Subject:          idToken.Subject,
		Email:            tokenClaims.Email,
		Name:             tokenClaims.Name,
		PreferredUsername: tokenClaims.PreferredUsername,
		GivenName:        tokenClaims.GivenName,
		FamilyName:       tokenClaims.FamilyName,
		Extra:            allClaims,
	}, nil
}

func isExpiredError(err error) bool {
	return strings.Contains(err.Error(), "expired")
}
