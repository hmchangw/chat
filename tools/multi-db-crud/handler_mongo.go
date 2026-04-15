package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// resolveMongoConn fetches the registered connection for id, verifies that
// it is a Mongo connection, and returns it. On any failure it writes an
// error response and returns (nil, false); the caller should bail out.
func (h *handler) resolveMongoConn(c *gin.Context, id string) (*connection, bool) {
	conn, err := h.reg.Get(id)
	if errors.Is(err, ErrNotFound) {
		replyError(c, http.StatusNotFound, "not_found", "connection not found")
		return nil, false
	}
	if err != nil {
		slog.Warn("registry get failed", "error", err, "id", id)
		replyError(c, http.StatusInternalServerError, "internal_error", sanitizeConnectError(err))
		return nil, false
	}
	if conn.kind != "mongo" {
		replyError(c, http.StatusBadRequest, "wrong_kind", "connection is not a mongo connection")
		return nil, false
	}
	return conn, true
}

// parseListDocsPaging reads and clamps the limit/skip query params to safe bounds.
// limit defaults to 50, is clamped to (0, 500]; skip defaults to 0, clamped to >=0.
func parseListDocsPaging(c *gin.Context) (limit, skip int64) {
	limit, _ = strconv.ParseInt(c.DefaultQuery("limit", "50"), 10, 64)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	skip, _ = strconv.ParseInt(c.DefaultQuery("skip", "0"), 10, 64)
	if skip < 0 {
		skip = 0
	}
	return limit, skip
}

// marshalDocsExtJSON converts a slice of bson.M into a slice of json.RawMessage
// using relaxed ExtJSON encoding. It is used by /api/mongo/.../docs and the
// POST insert response where the result set is small and paginated.
//
// Output uses relaxed ExtJSON (canonical=false) for readability in the UI;
// unmarshal auto-detects relaxed and canonical forms, so round-tripping
// docs through the editor or external tools (e.g. mongoexport) works.
//
// DO NOT REUSE FOR EXPORT: this helper holds every doc in memory. Export
// endpoints (Task 5) must stream doc-by-doc with chunked writes; see the
// dedicated export implementation there.
func marshalDocsExtJSON(docs []bson.M) ([]json.RawMessage, error) {
	items := make([]json.RawMessage, len(docs))
	for i, d := range docs {
		b, err := bson.MarshalExtJSON(d, false, false)
		if err != nil {
			return nil, err
		}
		items[i] = json.RawMessage(b)
	}
	return items, nil
}

// mongoListCollections handles GET /api/mongo/:id/collections.
func (h *handler) mongoListCollections(c *gin.Context) {
	id := c.Param("id")
	conn, ok := h.resolveMongoConn(c, id)
	if !ok {
		return
	}

	cols, err := h.mongo.ListCollections(c.Request.Context(), conn.mongo, conn.mongoDB)
	if err != nil {
		slog.Warn("list collections failed", "error", err, "id", id)
		replyError(c, http.StatusInternalServerError, "internal_error", "list collections failed")
		return
	}
	if cols == nil {
		cols = []collectionInfo{}
	}
	c.JSON(http.StatusOK, cols)
}

// mongoListDocs handles GET /api/mongo/:id/collections/:name/docs?limit=&skip=&filter=.
func (h *handler) mongoListDocs(c *gin.Context) {
	id := c.Param("id")
	conn, ok := h.resolveMongoConn(c, id)
	if !ok {
		return
	}
	coll := c.Param("name")

	filter := bson.M{}
	if raw := c.Query("filter"); raw != "" {
		if err := bson.UnmarshalExtJSON([]byte(raw), true, &filter); err != nil {
			replyError(c, http.StatusBadRequest, "bad_request", "invalid filter JSON")
			return
		}
	}

	limit, skip := parseListDocsPaging(c)

	res, err := h.mongo.ListDocs(c.Request.Context(), conn.mongo, conn.mongoDB, coll, filter, skip, limit)
	if err != nil {
		slog.Warn("list docs failed", "error", err, "id", id, "collection", coll)
		replyError(c, http.StatusInternalServerError, "internal_error", "list documents failed")
		return
	}

	items, err := marshalDocsExtJSON(res.Docs)
	if err != nil {
		slog.Warn("marshal docs failed", "error", err, "id", id, "collection", coll)
		replyError(c, http.StatusInternalServerError, "internal_error", "marshal documents failed")
		return
	}

	c.JSON(http.StatusOK, struct {
		Total int64             `json:"total"`
		Docs  []json.RawMessage `json:"docs"`
	}{Total: res.Total, Docs: items})
}

// mongoInsertDoc handles POST /api/mongo/:id/collections/:name/docs.
func (h *handler) mongoInsertDoc(c *gin.Context) {
	id := c.Param("id")
	conn, ok := h.resolveMongoConn(c, id)
	if !ok {
		return
	}
	coll := c.Param("name")

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		replyError(c, http.StatusBadRequest, "bad_request", "failed to read request body")
		return
	}
	var doc bson.M
	if err := bson.UnmarshalExtJSON(body, true, &doc); err != nil {
		replyError(c, http.StatusBadRequest, "bad_request", "invalid document JSON")
		return
	}

	inserted, err := h.mongo.InsertDoc(c.Request.Context(), conn.mongo, conn.mongoDB, coll, doc)
	if err != nil {
		if errors.Is(err, ErrMongoDuplicateKey) {
			replyError(c, http.StatusConflict, "duplicate_key", "document with this _id already exists")
			return
		}
		slog.Warn("insert doc failed", "error", err, "id", id, "collection", coll)
		replyError(c, http.StatusInternalServerError, "internal_error", "insert document failed")
		return
	}

	b, err := bson.MarshalExtJSON(inserted, false, false)
	if err != nil {
		slog.Warn("marshal inserted doc failed", "error", err, "id", id, "collection", coll)
		replyError(c, http.StatusInternalServerError, "internal_error", "marshal document failed")
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", b)
}

// mongoReplaceDoc handles PUT /api/mongo/:id/collections/:name/docs/:docID.
func (h *handler) mongoReplaceDoc(c *gin.Context) {
	id := c.Param("id")
	conn, ok := h.resolveMongoConn(c, id)
	if !ok {
		return
	}
	coll := c.Param("name")
	docID := parseDocID(c.Param("docID"))

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		replyError(c, http.StatusBadRequest, "bad_request", "failed to read request body")
		return
	}
	var doc bson.M
	if err := bson.UnmarshalExtJSON(body, true, &doc); err != nil {
		replyError(c, http.StatusBadRequest, "bad_request", "invalid document JSON")
		return
	}

	if err := h.mongo.ReplaceDoc(c.Request.Context(), conn.mongo, conn.mongoDB, coll, docID, doc); err != nil {
		if errors.Is(err, ErrMongoNotFound) {
			replyError(c, http.StatusNotFound, "not_found", "document not found")
			return
		}
		slog.Warn("replace doc failed", "error", err, "id", id, "collection", coll)
		replyError(c, http.StatusInternalServerError, "internal_error", "replace document failed")
		return
	}
	c.Status(http.StatusNoContent)
}

// importFailure records a single document-level import failure. The handler
// aggregates these in importResult so clients can report per-document errors
// without a follow-up API call.
type importFailure struct {
	Index int    `json:"index"`
	ID    any    `json:"_id,omitempty"`
	Error string `json:"error"`
}

// importResult is the JSON body returned by the import endpoint.
type importResult struct {
	Inserted int             `json:"inserted"`
	Failed   []importFailure `json:"failed"`
}

// mongoExportDocs handles GET /api/mongo/:id/collections/:name/export.
//
// The handler streams a JSON array of documents encoded as relaxed ExtJSON.
// Because output is streamed, the 200 status and opening '[' are written
// BEFORE the cursor is even opened — if StreamDocs fails partway through,
// the client receives a truncated JSON array and the error is logged server
// side. This is the accepted tradeoff for never buffering the full
// collection in memory; Task 5 spec explicitly calls it out.
func (h *handler) mongoExportDocs(c *gin.Context) {
	id := c.Param("id")
	conn, ok := h.resolveMongoConn(c, id)
	if !ok {
		return
	}
	coll := c.Param("name")

	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename=%q`, coll+".json"))
	c.Writer.WriteHeader(http.StatusOK)

	if _, err := c.Writer.WriteString("["); err != nil {
		slog.Warn("export open bracket write failed", "error", err, "id", id, "collection", coll)
		return
	}
	first := true
	err := h.mongo.StreamDocs(c.Request.Context(), conn.mongo, conn.mongoDB, coll, func(doc bson.M) error {
		raw, err := bson.MarshalExtJSON(doc, false, false) // relaxed ExtJSON
		if err != nil {
			return fmt.Errorf("marshal doc: %w", err)
		}
		if !first {
			if _, err := c.Writer.WriteString(","); err != nil {
				return err
			}
		}
		first = false
		if _, err := c.Writer.Write(raw); err != nil {
			return err
		}
		// Flush so progressive output works through proxies / fetch streams.
		c.Writer.Flush()
		return nil
	})
	if err != nil {
		// Headers + partial body already sent — we cannot change status. Log
		// and abort. The client sees a truncated JSON array.
		slog.Warn("mongo export failed", "error", err, "id", id, "collection", coll)
		return
	}
	if _, err := c.Writer.WriteString("]"); err != nil {
		slog.Warn("export close bracket write failed", "error", err, "id", id, "collection", coll)
	}
}

// mongoImportDocs handles POST /api/mongo/:id/collections/:name/import.
//
// The request body must be a JSON array of documents (ExtJSON). Each doc is
// inserted via InsertDoc; per-doc failures are collected and returned in the
// response so the caller can surface them without a follow-up API call.
// No upsert semantics — duplicate _ids count as failures.
func (h *handler) mongoImportDocs(c *gin.Context) {
	id := c.Param("id")
	conn, ok := h.resolveMongoConn(c, id)
	if !ok {
		return
	}
	coll := c.Param("name")

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		replyError(c, http.StatusBadRequest, "bad_request", "failed to read request body")
		return
	}
	var docs []bson.M
	if err := bson.UnmarshalExtJSON(body, true, &docs); err != nil {
		replyError(c, http.StatusBadRequest, "bad_request", "invalid JSON array")
		return
	}
	if len(docs) > h.maxImportDocs {
		replyError(c, http.StatusBadRequest, "too_large",
			fmt.Sprintf("import exceeds MAX_IMPORT_DOCS=%d", h.maxImportDocs))
		return
	}

	result := importResult{Failed: []importFailure{}}
	for i, doc := range docs {
		_, err := h.mongo.InsertDoc(c.Request.Context(), conn.mongo, conn.mongoDB, coll, doc)
		if err != nil {
			var docID any
			if doc != nil {
				docID = doc["_id"]
			}
			result.Failed = append(result.Failed, importFailure{
				Index: i,
				ID:    docID,
				Error: sanitizeConnectError(err),
			})
			continue
		}
		result.Inserted++
	}
	c.JSON(http.StatusOK, result)
}

// mongoDeleteDoc handles DELETE /api/mongo/:id/collections/:name/docs/:docID.
func (h *handler) mongoDeleteDoc(c *gin.Context) {
	id := c.Param("id")
	conn, ok := h.resolveMongoConn(c, id)
	if !ok {
		return
	}
	coll := c.Param("name")
	docID := parseDocID(c.Param("docID"))

	if err := h.mongo.DeleteDoc(c.Request.Context(), conn.mongo, conn.mongoDB, coll, docID); err != nil {
		if errors.Is(err, ErrMongoNotFound) {
			replyError(c, http.StatusNotFound, "not_found", "document not found")
			return
		}
		slog.Warn("delete doc failed", "error", err, "id", id, "collection", coll)
		replyError(c, http.StatusInternalServerError, "internal_error", "delete document failed")
		return
	}
	c.Status(http.StatusNoContent)
}
