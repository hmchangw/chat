package main

import "github.com/gin-gonic/gin"

func registerRoutes(r *gin.Engine, h *PortalHandler) {
	r.POST("/session/nats-jwt", h.HandleSessionNATSJWT)
	r.POST("/admin/cache/reload", h.HandleCacheReload)
	r.GET("/healthz", h.HandleHealth)
}
