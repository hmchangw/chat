package main

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed static/*
var staticFiles embed.FS

func (h *handler) serveUI(c *gin.Context) {
	fsys := h.staticFS
	if fsys == nil {
		fsys = staticFiles
	}
	data, err := fs.ReadFile(fsys, "static/index.html")
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}
