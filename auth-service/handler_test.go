package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
)

// fakeValidator implements TokenValidator for testing.
type fakeValidator struct {
	account     string
	subject     string
	email       string
	name        string
	description string
	deptName    string
	deptId      string
	expired     bool
	invalid     bool
}

func (f *fakeValidator) Validate(_ context.Context, _ string) (pkgoidc.Claims, error) {
	if f.expired {
		return pkgoidc.Claims{}, pkgoidc.ErrTokenExpired
	}
	if f.invalid {
		return pkgoidc.Claims{}, fmt.Errorf("oidc token verification failed: invalid signature")
	}
	return pkgoidc.Claims{
		Subject:           f.subject,
		Email:             f.email,
		Name:              f.name,
		PreferredUsername: f.account,
		Description:       f.description,
		DeptName:          f.deptName,
		DeptID:            f.deptId,
	}, nil
}

// helper: create a fresh account signing key pair for tests.
func mustAccountKP(t *testing.T) nkeys.KeyPair {
	t.Helper()
	kp, err := nkeys.CreateAccount()
	require.NoError(t, err, "create account key")
	return kp
}

// helper: create a fresh user nkey public key for tests.
func mustUserNKey(t *testing.T) string {
	t.Helper()
	kp, err := nkeys.CreateUser()
	require.NoError(t, err, "create user key")
	pub, err := kp.PublicKey()
	require.NoError(t, err, "public key")
	return pub
}

// helper: set up gin engine with auth handler using a fake validator.
func setupRouter(t *testing.T, handler *AuthHandler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerRoutes(r, handler)
	return r
}

func TestHandleAuth_ValidToken(t *testing.T) {
	signingKP := mustAccountKP(t)
	userPub := mustUserNKey(t)

	validator := &fakeValidator{
		account:     "alice",
		subject:     "uuid-alice",
		email:       "alice@example.com",
		description: "E001, Alice Wang, 王小明",
		deptName:    "Engineering",
		deptId:      "ABC123",
	}
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour, false)
	router := setupRouter(t, handler)

	body := `{"ssoToken":"valid-token","natsPublicKey":"` + userPub + `"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp authResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Verify user info in response.
	assert.Equal(t, "alice@example.com", resp.UserInfo.Email)
	assert.Equal(t, "alice", resp.UserInfo.Account)
	assert.Equal(t, "E001", resp.UserInfo.EmployeeID)
	assert.Equal(t, "Alice Wang", resp.UserInfo.EngName)
	assert.Equal(t, "王小明", resp.UserInfo.ChineseName)
	assert.Equal(t, "Engineering", resp.UserInfo.DeptName)
	assert.Equal(t, "ABC123", resp.UserInfo.DeptID)

	// Decode and verify the NATS JWT.
	claims, err := jwt.DecodeUserClaims(resp.NATSJWT)
	require.NoError(t, err)
	assert.Equal(t, userPub, claims.Subject)

	// Check expiration is set (within 2 hours).
	require.NotZero(t, claims.Expires)
	expiresAt := time.Unix(claims.Expires, 0)
	assert.LessOrEqual(t, time.Until(expiresAt), 2*time.Hour+time.Minute)

	// Check publish permissions: chat.user.alice.> and _INBOX.>
	assert.Contains(t, []string(claims.Pub.Allow), "chat.user.alice.>")
	assert.Contains(t, []string(claims.Pub.Allow), "_INBOX.>")

	// Check subscribe permissions: chat.user.alice.>, chat.room.>, _INBOX.>
	assert.Contains(t, []string(claims.Sub.Allow), "chat.user.alice.>")
	assert.Contains(t, []string(claims.Sub.Allow), "chat.room.>")
	assert.Contains(t, []string(claims.Sub.Allow), "_INBOX.>")
}

func TestHandleAuth_ExpiredToken(t *testing.T) {
	signingKP := mustAccountKP(t)
	validator := &fakeValidator{expired: true}
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour, false)
	router := setupRouter(t, handler)

	userPub := mustUserNKey(t)
	body := `{"ssoToken":"expired-token","natsPublicKey":"` + userPub + `"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "expired")
}

func TestHandleAuth_InvalidToken(t *testing.T) {
	signingKP := mustAccountKP(t)
	validator := &fakeValidator{invalid: true}
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour, false)
	router := setupRouter(t, handler)

	userPub := mustUserNKey(t)
	body := `{"ssoToken":"bad-token","natsPublicKey":"` + userPub + `"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "invalid SSO token")
}

func TestHandleAuth_InvalidNKey(t *testing.T) {
	signingKP := mustAccountKP(t)
	validator := &fakeValidator{account: "alice", subject: "uuid-alice"}
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour, false)
	router := setupRouter(t, handler)

	body := `{"ssoToken":"valid-token","natsPublicKey":"NOT-A-VALID-NKEY"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid natsPublicKey format")
}

func TestHandleAuth_MissingFields(t *testing.T) {
	signingKP := mustAccountKP(t)
	validator := &fakeValidator{account: "alice"}
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour, false)
	router := setupRouter(t, handler)

	tests := []struct {
		name string
		body string
	}{
		{"missing ssoToken", `{"natsPublicKey":"somekey"}`},
		{"missing natsPublicKey", `{"ssoToken":"tok"}`},
		{"empty body", `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestHandleAuth_PermissionsPerUser(t *testing.T) {
	signingKP := mustAccountKP(t)

	accounts := []string{"alice", "bob", "charlie"}
	for _, account := range accounts {
		t.Run(account, func(t *testing.T) {
			validator := &fakeValidator{account: account, subject: "uuid-" + account}
			handler := NewAuthHandler(validator, signingKP, 2*time.Hour, false)
			router := setupRouter(t, handler)

			userPub := mustUserNKey(t)
			body := `{"ssoToken":"token-` + account + `","natsPublicKey":"` + userPub + `"}`
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)

			var resp authResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

			claims, err := jwt.DecodeUserClaims(resp.NATSJWT)
			require.NoError(t, err)

			wantPub := "chat.user." + account + ".>"
			assert.Contains(t, []string(claims.Pub.Allow), wantPub)

			wantSub := "chat.user." + account + ".>"
			assert.Contains(t, []string(claims.Sub.Allow), wantSub)
		})
	}
}

func TestHandleAuth_DevMode_ValidRequest(t *testing.T) {
	signingKP := mustAccountKP(t)
	userPub := mustUserNKey(t)

	handler := NewAuthHandler(nil, signingKP, 2*time.Hour, true)
	router := setupRouter(t, handler)

	body := `{"account":"alice","natsPublicKey":"` + userPub + `"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp authResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, "alice", resp.UserInfo.Account)
	assert.Equal(t, "alice", resp.UserInfo.EngName)
	assert.Equal(t, "alice@dev.local", resp.UserInfo.Email)

	// Verify NATS JWT is valid and scoped to alice.
	claims, err := jwt.DecodeUserClaims(resp.NATSJWT)
	require.NoError(t, err)
	assert.Equal(t, userPub, claims.Subject)
	assert.Contains(t, []string(claims.Pub.Allow), "chat.user.alice.>")
	assert.Contains(t, []string(claims.Sub.Allow), "chat.user.alice.>")
}

func TestHandleAuth_DevMode_MissingAccount(t *testing.T) {
	signingKP := mustAccountKP(t)
	userPub := mustUserNKey(t)

	handler := NewAuthHandler(nil, signingKP, 2*time.Hour, true)
	router := setupRouter(t, handler)

	body := `{"natsPublicKey":"` + userPub + `"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "account")
}

func TestHandleAuth_DevMode_InvalidNKey(t *testing.T) {
	signingKP := mustAccountKP(t)

	handler := NewAuthHandler(nil, signingKP, 2*time.Hour, true)
	router := setupRouter(t, handler)

	body := `{"account":"alice","natsPublicKey":"NOT-VALID"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid natsPublicKey")
}

func TestHandleAuth_EmptyAccountFromOIDCRejected(t *testing.T) {
	signingKP := mustAccountKP(t)
	userPub := mustUserNKey(t)

	validator := &fakeValidator{
		account: "",
		name:    "",
		subject: "uuid-anon",
		email:   "anon@example.com",
	}
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour, false)
	router := setupRouter(t, handler)

	body := `{"ssoToken":"valid-token","natsPublicKey":"` + userPub + `"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.NotContains(t, w.Body.String(), "natsJwt")
}

func TestHandleAuth_FallsBackToNameWhenPreferredUsernameEmpty(t *testing.T) {
	signingKP := mustAccountKP(t)
	userPub := mustUserNKey(t)

	validator := &fakeValidator{
		account: "",
		name:    "bob",
		subject: "uuid-bob",
	}
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour, false)
	router := setupRouter(t, handler)

	body := `{"ssoToken":"valid-token","natsPublicKey":"` + userPub + `"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp authResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "bob", resp.UserInfo.Account)
}

func TestHandleAuth_OIDCAccountWithUnsafeChars(t *testing.T) {
	signingKP := mustAccountKP(t)
	userPub := mustUserNKey(t)

	cases := []struct {
		name    string
		account string
	}{
		{"contains_dot", "alice.smith"},
		{"single_dot", "."},
		{"wildcard_star", "*"},
		{"wildcard_gt", ">"},
		{"contains_space", "alice smith"},
		{"whitespace_only", "   "},
		{"contains_at", "alice@example.com"},
		{"contains_slash", "alice/smith"},
		{"contains_newline", "alice\n"},
		{"too_long", strings.Repeat("a", 65)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validator := &fakeValidator{account: tc.account, subject: "uuid-x"}
			handler := NewAuthHandler(validator, signingKP, 2*time.Hour, false)
			router := setupRouter(t, handler)

			body := `{"ssoToken":"valid-token","natsPublicKey":"` + userPub + `"}`
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusUnauthorized, w.Code, "account %q must be rejected", tc.account)
			assert.NotContains(t, w.Body.String(), "natsJwt")
		})
	}
}

func TestHandleAuth_DevMode_AccountWithUnsafeChars(t *testing.T) {
	signingKP := mustAccountKP(t)
	userPub := mustUserNKey(t)

	cases := []string{
		"alice.smith",
		"*",
		">",
		"alice smith",
		".",
		"alice@example.com",
		"alice/smith",
		strings.Repeat("a", 65),
	}
	for _, account := range cases {
		t.Run(account, func(t *testing.T) {
			handler := NewAuthHandler(nil, signingKP, 2*time.Hour, true)
			router := setupRouter(t, handler)

			payload, err := json.Marshal(map[string]string{
				"account":       account,
				"natsPublicKey": userPub,
			})
			require.NoError(t, err)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(string(payload)))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code, "account %q must be rejected", account)
			assert.NotContains(t, w.Body.String(), "natsJwt")
		})
	}
}

func TestValidateAccount(t *testing.T) {
	cases := []struct {
		name    string
		account string
		wantErr bool
	}{
		{"simple", "alice", false},
		{"alphanumeric", "alice123", false},
		{"underscore", "alice_smith", false},
		{"dash", "alice-smith", false},
		{"mixed_case", "AliceSmith", false},
		{"digits_only", "12345", false},
		{"max_length", strings.Repeat("a", 64), false},

		{"empty", "", true},
		{"whitespace_only", "   ", true},
		{"contains_dot", "alice.smith", true},
		{"contains_space", "alice smith", true},
		{"contains_at", "alice@example.com", true},
		{"contains_slash", "alice/smith", true},
		{"wildcard_star", "*", true},
		{"wildcard_gt", ">", true},
		{"contains_newline", "alice\n", true},
		{"contains_tab", "alice\t", true},
		{"contains_null", "alice\x00", true},
		{"over_max_length", strings.Repeat("a", 65), true},
		{"contains_unicode", "café", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAccount(tc.account)
			if tc.wantErr {
				assert.Error(t, err, "account %q should be rejected", tc.account)
			} else {
				assert.NoError(t, err, "account %q should be accepted", tc.account)
			}
		})
	}
}

func TestHandleHealth(t *testing.T) {
	signingKP := mustAccountKP(t)
	handler := NewAuthHandler(&fakeValidator{}, signingKP, 2*time.Hour, false)
	router := setupRouter(t, handler)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}
