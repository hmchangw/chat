package main

import "github.com/gin-gonic/gin"

// registerRoutes wires health plus the authenticated, traced /api/v4 group.
func registerRoutes(r *gin.Engine, h *Handler, v TokenValidator, devMode bool) {
	r.GET("/healthz", h.HandleHealth)

	api := r.Group("/api/v4")
	api.Use(otelMiddleware())
	api.Use(authMiddleware(v, devMode))
	api.POST("/rooms/:roomId/upload/protected-images", h.HandleUploadProtectedImages)
	api.GET("/rooms/:roomId/protected-image/:fileId", h.HandleDownloadProtectedImage)
}
