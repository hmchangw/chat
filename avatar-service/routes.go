package main

import "github.com/gin-gonic/gin"

func registerRoutes(r *gin.Engine, h *handler) {
	r.GET("/healthz", h.HandleHealth)
	r.GET("/avatar/v1/room/:roomID", h.HandleRoomAvatar)
	r.GET("/avatar/v1/:accountName", h.HandleAccountAvatar)
}

// registerUploadRoutes is a temporary stub; Task 9 provides the real one.
func registerUploadRoutes(_ *gin.Engine, _ *handler) {}
