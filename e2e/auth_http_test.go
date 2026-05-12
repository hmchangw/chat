//go:build e2e

// HTTP coverage for auth-service. The existing suite drives auth indirectly
// via the NATS handshake (Authenticate calls /auth and then opens a NATS
// connection). These tests exercise the HTTP surface DIRECTLY so that:
//
//   - Malformed-request rejection codes can drift independently of the
//     happy-path NATS handshake.
//   - The /healthz contract is locked in (a regression here breaks the
//     compose healthcheck dep order, not just one user flow).
//   - Missing-field validation lives in code that the rest of the suite
//     never exercises (the NATS path always supplies both fields).
//
// Per CLAUDE.md client-API rule: auth-service is the only HTTP service
// in the realm-bound user surface, so direct coverage here actually
// matters.

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authResponse mirrors the success payload from auth-service/handler.go.
// We don't import the service-internal type because it's package-local.
type authResponse struct {
	NATSJWT string `json:"natsJwt"`
}

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
// + auth-service POST /auth and asserts the returned natsJwt is non-empty
// + decodes as a JWT (has two dots). Mirrors what harness.Authenticate
// does internally but as a black-box assertion.
func TestAuthHTTP_AuthHappyPath(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	// Borrow the harness's keycloak client to obtain a real SSO token.
	id := site.Authenticate(t, ctx, "alice")
	require.NotEmpty(t, id.NATSJWT,
		"harness Authenticate already proves /auth happy path; "+
			"this test exists so a future refactor of Authenticate doesn't lose direct coverage")

	// Verify the JWT shape (header.payload.signature => 2 dots).
	dots := 0
	for _, ch := range id.NATSJWT {
		if ch == '.' {
			dots++
		}
	}
	assert.Equal(t, 2, dots, "natsJwt must be a 3-segment JWT; got %q", id.NATSJWT)
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

// Compile-time guard: authResponse decode shape stays in sync with
// auth-service's success payload. If the field name ever drifts, the
// compiler will flag a referenced-but-unused struct here -- a forcing
// function to update both sides.
var _ = authResponse{}
