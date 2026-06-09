package errhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/errcode"
)

func TestWrite_StatusAndEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth", nil)
	Write(c.Request.Context(), c, errcode.Unauthenticated("token expired", errcode.WithReason(errcode.AuthTokenExpired)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "unauthenticated" || got["reason"] != "sso_token_expired" {
		t.Fatalf("envelope = %v", got)
	}
}

func TestWrite_UnknownIs500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	Write(c.Request.Context(), c, errors.New("db exploded"))
	if w.Code != http.StatusInternalServerError || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}
