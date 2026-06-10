package main

import "github.com/gin-gonic/gin"

func registerRoutes(r *gin.Engine, h *handler) {
	r.GET("/healthz", h.HandleHealth)
	r.GET("/avatar/v1/room/:roomID", h.HandleRoomAvatar)
	r.GET("/avatar/v1/:accountName", h.HandleAccountAvatar)
}
