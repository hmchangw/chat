package ginutil

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestRequestID_PassesValidInboundThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())

	var fromCtx, fromGin string
	r.GET("/test", func(c *gin.Context) {
		fromCtx = natsutil.RequestIDFromContext(c.Request.Context())
		fromGin = c.GetString("request_id")
		c.Status(http.StatusOK)
	})

	testID := "01893f8b-1c4a-7000-abcd-ef0123456789"
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(natsutil.RequestIDHeader, testID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, testID, fromGin, "gin context carries the ID under request_id")
	assert.Equal(t, testID, fromCtx, "request.Context() carries the ID via natsutil")
	assert.Equal(t, testID, w.Header().Get(natsutil.RequestIDHeader), "echoed in response header")
}

func TestRequestID_MintsWhenAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())

	var fromCtx, fromGin string
	r.GET("/test", func(c *gin.Context) {
		fromCtx = natsutil.RequestIDFromContext(c.Request.Context())
		fromGin = c.GetString("request_id")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.True(t, idgen.IsValidUUID(fromCtx), "minted ID must be a valid hyphenated UUID")
	assert.Equal(t, fromCtx, fromGin, "minted ID identical in ctx and gin key")
	assert.Equal(t, fromCtx, w.Header().Get(natsutil.RequestIDHeader), "same minted ID echoed in response")
}

func TestRequestID_RegeneratesOnMalformed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())

	var fromCtx string
	r.GET("/test", func(c *gin.Context) {
		fromCtx = natsutil.RequestIDFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(natsutil.RequestIDHeader, "not-a-uuidv7")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.NotEqual(t, "not-a-uuidv7", fromCtx, "malformed inbound ID must be replaced")
	assert.True(t, idgen.IsValidUUID(fromCtx), "regenerated ID must be a valid hyphenated UUID")
	assert.Equal(t, fromCtx, w.Header().Get(natsutil.RequestIDHeader), "echoed header matches the regenerated ID")
}

func TestAccessLog_PassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.Use(AccessLog())

	handlerCalled := false
	r.GET("/test", func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.True(t, handlerCalled, "downstream handler runs after AccessLog")
	assert.Equal(t, http.StatusOK, w.Code, "status passes through unchanged")
}
