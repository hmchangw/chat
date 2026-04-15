package main

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed static/*
var staticFiles embed.FS

func (h *handler) serveUI(c *gin.Context) {
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}
