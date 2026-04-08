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
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour)
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
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour)
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
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour)
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
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour)
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
	handler := NewAuthHandler(validator, signingKP, 2*time.Hour)
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
			handler := NewAuthHandler(validator, signingKP, 2*time.Hour)
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

func TestHandleHealth(t *testing.T) {
	signingKP := mustAccountKP(t)
	handler := NewAuthHandler(&fakeValidator{}, signingKP, 2*time.Hour)
	router := setupRouter(t, handler)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}
