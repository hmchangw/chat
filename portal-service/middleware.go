package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// normalizeOrigin reduces a URL/origin to lowercase scheme://host[:port],
// dropping default ports (http:80, https:443).
func normalizeOrigin(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("parse origin %q: %w", s, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("origin %q must include scheme and host", s)
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if (scheme == "http" && strings.HasSuffix(host, ":80")) || (scheme == "https" && strings.HasSuffix(host, ":443")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	return scheme + "://" + host, nil
}

// corsMiddleware allows only the configured frontend origins (the token rides
// in the body, not a cookie, so no credentialed-CORS handling is needed).
func corsMiddleware(allowed map[string]struct{}) gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin != "" {
			c.Header("Vary", "Origin")
			if norm, err := normalizeOrigin(origin); err == nil {
				if _, ok := allowed[norm]; ok {
					c.Header("Access-Control-Allow-Origin", norm)
					c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
					c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
					c.Header("Access-Control-Max-Age", "300")
				}
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
