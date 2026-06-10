package main

import "github.com/gin-gonic/gin"

func registerRoutes(r *gin.Engine, h *handler) {
	r.GET("/healthz", h.HandleHealth)
	// read endpoints are registered in Task 7/8.
}

// registerUploadRoutes is a temporary stub; Task 9 provides the real one.
func registerUploadRoutes(_ *gin.Engine, _ *handler) {}
