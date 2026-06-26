package main

import "github.com/gin-gonic/gin"

func registerRoutes(r *gin.Engine, h *PortalHandler) {
	r.GET("/api/userInfo", h.HandleUserInfo)
	// /login + /auth/callback are top-level browser redirects (not CORS/XHR); the
	// handshake cookie is HttpOnly + SameSite=Lax, so wildcard CORS stays safe.
	r.GET("/login", h.HandleLogin)
	r.GET("/auth/callback", h.HandleAuthCallback)
	r.GET("/healthz", h.HandleHealth)
	r.GET("/readyz", h.HandleReady)
}
