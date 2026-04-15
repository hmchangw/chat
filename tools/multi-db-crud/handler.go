package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:generate mockgen -source=handler.go -destination=mock_registry_test.go -package=main -typed

// registryAPI is the subset of *registry that HTTP handlers consume. Defined
// in the consumer (handler) per CLAUDE.md so handler tests can mock it.
type registryAPI interface {
	Connect(ctx context.Context, spec connectSpec) (connInfo, error)
	Get(id string) (*connection, error)
	Close(id string) error
	List() []connInfo
	CloseAll()
}

// Compile-time assertion that *registry satisfies registryAPI.
var _ registryAPI = (*registry)(nil)

type handler struct {
	// staticFS overrides the embedded staticFiles in tests.
	staticFS fs.FS
	// reg is the connection registry. May be nil in some tests.
	reg registryAPI
}

func newHandler(reg registryAPI) *handler {
	return &handler{reg: reg}
}

// errResp is the uniform error shape for API responses (spec §7).
type errResp struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// replyError aborts the request with a JSON error body using the canonical shape.
func replyError(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, errResp{Error: msg, Code: code})
}

// sanitizeConnectError keeps only the first segment before ':' to avoid
// leaking driver internals (hosts, credentials) to clients. Full errors
// are logged server-side separately.
func sanitizeConnectError(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, ":"); i != -1 {
		return strings.TrimSpace(msg[:i])
	}
	return msg
}

// readOnlyMiddleware rejects non-GET requests with 403 when readOnly is true.
func readOnlyMiddleware(readOnly bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if readOnly && c.Request.Method != http.MethodGet {
			replyError(c, http.StatusForbidden, "read_only", "server is in read-only mode")
			return
		}
		c.Next()
	}
}

// connectRequest is the POST /api/connect body.
type connectRequest struct {
	Kind     string `json:"kind"     binding:"required,oneof=mongo cassandra"`
	URI      string `json:"uri"      binding:"required"`
	Keyspace string `json:"keyspace"`
	Label    string `json:"label"    binding:"required"`
}

func (h *handler) healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// connect opens a new database connection and returns its metadata.
func (h *handler) connect(c *gin.Context) {
	var req connectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		replyError(c, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Kind == "cassandra" && req.Keyspace == "" {
		replyError(c, http.StatusBadRequest, "bad_request", "keyspace is required for cassandra")
		return
	}

	info, err := h.reg.Connect(c.Request.Context(), connectSpec(req))
	if err != nil {
		if errors.Is(err, ErrUnknownKind) {
			replyError(c, http.StatusBadRequest, "unknown_kind", err.Error())
			return
		}
		slog.Warn("connect failed", "error", err)
		replyError(c, http.StatusBadGateway, "connect_failed", sanitizeConnectError(err))
		return
	}
	c.JSON(http.StatusOK, info)
}

// listConnections returns all currently registered connections. Always
// returns a JSON array (never null, never 404 for empty).
func (h *handler) listConnections(c *gin.Context) {
	conns := h.reg.List()
	if conns == nil {
		conns = []connInfo{}
	}
	c.JSON(http.StatusOK, conns)
}

// deleteConnection closes and removes a connection by ID.
func (h *handler) deleteConnection(c *gin.Context) {
	id := c.Param("id")
	if err := h.reg.Close(id); err != nil {
		if errors.Is(err, ErrNotFound) {
			replyError(c, http.StatusNotFound, "not_found", err.Error())
			return
		}
		slog.Warn("close connection failed", "error", err, "id", id)
		replyError(c, http.StatusInternalServerError, "internal_error", sanitizeConnectError(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// Stub handlers — all return 501 Not Implemented until fully implemented.

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
