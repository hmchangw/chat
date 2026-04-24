package main

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/idgen"
)

const requestIDHeader = "X-Request-ID"

// requestIDMiddleware assigns a unique request ID to each request.
// Uses the incoming X-Request-ID header if present, otherwise generates one.
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestIDHeader)
		if id == "" {
			id = idgen.GenerateID()
		}
		c.Set("request_id", id)
		c.Header(requestIDHeader, id)
		c.Next()
	}
}

// accessLogMiddleware logs method, path, status, and latency for each request.
func accessLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		slog.Info("request",
			"request_id", c.GetString("request_id"),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}
