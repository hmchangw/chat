//go:build e2e

// Direct HTTP coverage for auth-service (the NATS handshake path doesn't
// exercise malformed-body rejection or /healthz).

package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuthHTTP_HealthzReturnsOK verifies the basic ping contract that the
// docker-compose healthcheck depends on. Status 200 + "ok" body keyword.
func TestAuthHTTP_HealthzReturnsOK(t *testing.T) {
	t.Parallel()
	site := stack.SiteA

	resp, err := httpGet(t, site.AuthURL+"/healthz", 5*time.Second)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"status":"ok"`)
}

// TestAuthHTTP_AuthRejectsEmptyBody covers handler.go:79 — both required
// fields missing should produce 400 with a structured error message.
func TestAuthHTTP_AuthRejectsEmptyBody(t *testing.T) {
	t.Parallel()
	site := stack.SiteA

	resp, err := httpPostJSON(t, site.AuthURL+"/auth", map[string]string{}, 5*time.Second)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "required",
		"empty-body rejection must mention the required-fields contract; got %s", body)
}

// TestAuthHTTP_AuthRejectsMissingNkey covers handler.go:79 — only ssoToken
// supplied, natsPublicKey missing.
func TestAuthHTTP_AuthRejectsMissingNkey(t *testing.T) {
	t.Parallel()
	site := stack.SiteA

	resp, err := httpPostJSON(t, site.AuthURL+"/auth",
		map[string]string{"ssoToken": "doesnt-matter-validation-fires-first"},
		5*time.Second)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestAuthHTTP_AuthRejectsBadNkey covers handler.go:84 — well-formed body
// with a syntactically-invalid NATS public key. Should NOT reach the
// Keycloak SSO step.
func TestAuthHTTP_AuthRejectsBadNkey(t *testing.T) {
	t.Parallel()
	site := stack.SiteA

	resp, err := httpPostJSON(t, site.AuthURL+"/auth", map[string]string{
		"ssoToken":      "any-string-keycloak-step-is-after-this",
		"natsPublicKey": "this-is-not-a-real-nkey",
	}, 5*time.Second)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "natsPublicKey",
		"bad-nkey error must mention the field; got %s", body)
}

// TestAuthHTTP_AuthRejectsBadSSOToken covers handler.go:91-97 — well-formed
// body with valid nkey but garbage SSO token. Keycloak rejects → 401.
func TestAuthHTTP_AuthRejectsBadSSOToken(t *testing.T) {
	t.Parallel()
	site := stack.SiteA

	kp, err := nkeys.CreateUser()
	require.NoError(t, err)
	pub, err := kp.PublicKey()
	require.NoError(t, err)

	resp, err := httpPostJSON(t, site.AuthURL+"/auth", map[string]string{
		"ssoToken":      "definitely.not.a.valid.jwt",
		"natsPublicKey": pub,
	}, 10*time.Second)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"bad SSO token must yield 401; got %d", resp.StatusCode)
}

// TestAuthHTTP_AuthHappyPath round-trips a real Keycloak password grant
// + auth-service POST /auth and asserts the returned natsJwt decodes as
// a NATS JWT with the expected `sub` (account == "alice").
func TestAuthHTTP_AuthHappyPath(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	id := site.Authenticate(t, ctx, "alice")
	require.NotEmpty(t, id.NATSJWT)

	// JWT shape: header.payload.signature
	parts := strings.Split(id.NATSJWT, ".")
	require.Len(t, parts, 3, "natsJwt must be a 3-segment JWT; got %q", id.NATSJWT)

	// NATS user JWTs encode account-scope under nats.pub.allow rather
	// than a standard `name` claim.
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err, "JWT payload must be base64-url")
	var claims struct {
		Iss  string `json:"iss"`
		NATS struct {
			Pub struct {
				Allow []string `json:"allow"`
			} `json:"pub"`
		} `json:"nats"`
	}
	require.NoError(t, json.Unmarshal(payloadBytes, &claims))
	assert.NotEmpty(t, claims.Iss, "JWT must have iss claim (the signing operator/account)")
	assert.Contains(t, claims.NATS.Pub.Allow, "chat.user.alice.>",
		"JWT permissions must scope to the realm account; allow=%v", claims.NATS.Pub.Allow)
}

// httpGet / httpPostJSON: minimal non-Resty helpers so we exercise the raw
// HTTP surface (Resty wraps + retries can hide a 4xx from the test).
func httpGet(t *testing.T, url string, timeout time.Duration) (*http.Response, error) {
	t.Helper()
	c := &http.Client{Timeout: timeout}
	return c.Get(url)
}

func httpPostJSON(t *testing.T, url string, body any, timeout time.Duration) (*http.Response, error) {
	t.Helper()
	buf, err := json.Marshal(body)
	require.NoError(t, err)
	c := &http.Client{Timeout: timeout}
	return c.Post(url, "application/json", bytes.NewReader(buf))
}
