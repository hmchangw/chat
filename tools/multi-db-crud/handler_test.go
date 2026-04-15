package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestHandler_Healthz(t *testing.T) {
	h := newHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	c, _ := gin.CreateTestContext(w)
	c.Request = req
	h.healthz(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var got map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	assert.Equal(t, "ok", got["status"])
}

func TestRequestIDMiddleware_GeneratesID(t *testing.T) {
	h := newHandler()

	r := gin.New()
	r.Use(requestIDMiddleware())
	r.GET("/healthz", h.healthz)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, w.Header().Get("X-Request-ID"), "response should have X-Request-ID header")
}

func TestRequestIDMiddleware_EchoesExistingID(t *testing.T) {
	h := newHandler()

	r := gin.New()
	r.Use(requestIDMiddleware())
	r.GET("/healthz", h.healthz)

	const reqID = "my-existing-request-id"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", reqID)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, reqID, w.Header().Get("X-Request-ID"))
}
