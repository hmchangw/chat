package main

import "github.com/gin-gonic/gin"

func registerRoutes(r *gin.Engine, h *AuthHandler) {
	r.POST("/auth", h.HandleAuth)
	r.GET("/healthz", h.HandleHealth)
}
