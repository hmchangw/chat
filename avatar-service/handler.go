package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type handler struct {
	store    avatarStore
	blobs    blobStore
	cfg      config
	eidCache *ttlCache
}

func newHandler(store avatarStore, blobs blobStore, cfg *config) *handler {
	return &handler{
		store:    store,
		blobs:    blobs,
		cfg:      *cfg,
		eidCache: newTTLCache(50000, 10*time.Minute),
	}
}

func (h *handler) HandleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
