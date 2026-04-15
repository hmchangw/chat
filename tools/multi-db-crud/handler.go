package main

import (
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
)

type handler struct {
	// staticFS overrides the embedded staticFiles in tests.
	staticFS fs.FS
}

func newHandler() *handler {
	return &handler{}
}

func (h *handler) healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Stub handlers — all return 501 Not Implemented until fully implemented.

func (h *handler) connect(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) listConnections(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) deleteConnection(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) mongoCollections(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) mongoDocs(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) mongoCreateDoc(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) mongoUpdateDoc(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) mongoDeleteDoc(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) cassandraTables(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) cassandraRows(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) cassandraCreateRow(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) cassandraUpdateRow(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) cassandraDeleteRow(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) mongoExport(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) mongoImport(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) cassandraExport(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) cassandraImport(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) listTemplates(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *handler) getTemplate(c *gin.Context) {
	c.Status(http.StatusNotImplemented)
}
