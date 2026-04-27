package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestRequestIDMiddleware_AttachesIDToRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requestIDMiddleware())

	var fromCtx string
	var fromGin string
	r.GET("/test", func(c *gin.Context) {
		fromCtx = natsutil.RequestIDFromContext(c.Request.Context())
		fromGin = c.GetString("request_id")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(natsutil.RequestIDHeader, "auth-svc-test-id")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, "auth-svc-test-id", fromGin, "Gin context still carries the ID under request_id")
	assert.Equal(t, "auth-svc-test-id", fromCtx, "request.Context() must also carry the ID via natsutil")
	assert.Equal(t, "auth-svc-test-id", w.Header().Get(natsutil.RequestIDHeader), "echoed in response header")
}

func TestRequestIDMiddleware_GeneratesAndAttachesWhenHeaderAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requestIDMiddleware())

	var fromCtx string
	var fromGin string
	r.GET("/test", func(c *gin.Context) {
		fromCtx = natsutil.RequestIDFromContext(c.Request.Context())
		fromGin = c.GetString("request_id")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.NotEmpty(t, fromCtx, "minted request ID must be attached to request.Context()")
	assert.Equal(t, fromCtx, fromGin, "minted ID must be identical in ctx and Gin keys map")
	assert.Equal(t, fromCtx, w.Header().Get(natsutil.RequestIDHeader),
		"the same minted ID must be echoed in the response header")
}

func TestAccessLogMiddleware_LogsAndPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// requestIDMiddleware first, so the access log gets a non-empty request_id field.
	r.Use(requestIDMiddleware())
	r.Use(accessLogMiddleware())

	handlerCalled := false
	r.GET("/test", func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.True(t, handlerCalled, "downstream handler must run after accessLogMiddleware")
	assert.Equal(t, http.StatusOK, w.Code, "status passes through unchanged")
}
