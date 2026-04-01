//go:build integration

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

type fakeValidator struct {
	username string
}

func (v *fakeValidator) Validate(_ context.Context, _ string) (pkgoidc.Claims, error) {
	return pkgoidc.Claims{
		Subject:           "uuid-" + v.username,
		PreferredUsername: v.username,
		Email:             v.username + "@example.com",
		Description:       "E001, Test User, 測試用戶",
		DeptName:          "QA",
		DeptID:            "ABC"
	}, nil
}

func TestAuthHandler_Integration(t *testing.T) {
	kp, err := nkeys.CreateAccount()
	require.NoError(t, err)

	userKP, err := nkeys.CreateUser()
	require.NoError(t, err)
	userPub, err := userKP.PublicKey()
	require.NoError(t, err)

	validator := &fakeValidator{username: "testuser"}
	handler := NewAuthHandler(validator, kp, 2*time.Hour)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerRoutes(r, handler)

	body := fmt.Sprintf(`{"ssoToken":"valid-token","natsPublicKey":"%s"}`, userPub)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp authResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Decode and verify the JWT.
	claims, err := jwt.DecodeUserClaims(resp.NATSJWT)
	require.NoError(t, err)

	// Verify publish permissions contain user namespace.
	assert.Contains(t, []string(claims.Pub.Allow), "chat.user.testuser.>")
	assert.Contains(t, []string(claims.Sub.Allow), "chat.room.>")
}
