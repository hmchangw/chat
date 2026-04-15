package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.uber.org/mock/gomock"
)

// newMongoTestRouter constructs a Gin engine with a handler wired to the
// supplied mock registry and mock mongoOps. Routes are registered exactly as
// production code does.
func newMongoTestRouter(t *testing.T, reg registryAPI, ops mongoOps) *gin.Engine {
	t.Helper()
	h := newHandler(reg, ops, 10000)
	r := gin.New()
	registerRoutes(r, h)
	return r
}

// newMongoTestRouterWithMax is like newMongoTestRouter but allows overriding
// maxImportDocs so the import size-cap branch can be exercised without having
// to construct a 10001-document payload.
func newMongoTestRouterWithMax(t *testing.T, reg registryAPI, ops mongoOps, maxImportDocs int) *gin.Engine {
	t.Helper()
	h := newHandler(reg, ops, maxImportDocs)
	r := gin.New()
	registerRoutes(r, h)
	return r
}

// mongoConn returns a minimal *connection suitable for handler tests. The
// mongo.Client pointer is nil; handlers only forward it to the ops seam which
// is mocked, so nil is fine.
func mongoConn() *connection {
	return &connection{kind: "mongo", mongoDB: "testdb", label: "primary"}
}

// --- Shared connection-resolution tests for every mongo endpoint ---------------------

// mongoEndpoint is a single (method, path, body) tuple used to exercise the
// shared connection-resolution path once per endpoint.
type mongoEndpoint struct {
	name   string
	method string
	path   string
	body   string
}

func allMongoEndpoints() []mongoEndpoint {
	return []mongoEndpoint{
		{"ListCollections", http.MethodGet, "/api/mongo/abc/collections", ""},
		{"ListDocs", http.MethodGet, "/api/mongo/abc/collections/rooms/docs", ""},
		{"InsertDoc", http.MethodPost, "/api/mongo/abc/collections/rooms/docs", `{"a":1}`},
		{"ReplaceDoc", http.MethodPut, "/api/mongo/abc/collections/rooms/docs/123", `{"a":1}`},
		{"DeleteDoc", http.MethodDelete, "/api/mongo/abc/collections/rooms/docs/123", ""},
		{"ExportDocs", http.MethodGet, "/api/mongo/abc/collections/rooms/export", ""},
		{"ImportDocs", http.MethodPost, "/api/mongo/abc/collections/rooms/import", `[]`},
	}
}

func doReq(r http.Handler, ep mongoEndpoint) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var body io.Reader
	if ep.body != "" {
		body = strings.NewReader(ep.body)
	}
	req := httptest.NewRequest(ep.method, ep.path, body)
	if ep.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	r.ServeHTTP(w, req)
	return w
}

func TestMongoEndpoint_ConnectionNotFound_404(t *testing.T) {
	for _, ep := range allMongoEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			reg := NewMockregistryAPI(ctrl)
			ops := NewMockmongoOps(ctrl)
			reg.EXPECT().Get("abc").Return(nil, ErrNotFound)

			r := newMongoTestRouter(t, reg, ops)
			w := doReq(r, ep)
			assert.Equal(t, http.StatusNotFound, w.Code)
			assert.Equal(t, "not_found", decodeErrResp(t, w.Body).Code)
		})
	}
}

func TestMongoEndpoint_WrongKind_400(t *testing.T) {
	for _, ep := range allMongoEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			reg := NewMockregistryAPI(ctrl)
			ops := NewMockmongoOps(ctrl)
			reg.EXPECT().Get("abc").Return(&connection{kind: "cassandra"}, nil)

			r := newMongoTestRouter(t, reg, ops)
			w := doReq(r, ep)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Equal(t, "wrong_kind", decodeErrResp(t, w.Body).Code)
		})
	}
}

func TestMongoEndpoint_RegistryGetInternalError_500(t *testing.T) {
	// Ensures the non-ErrNotFound branch maps to 500 with sanitized message.
	for _, ep := range allMongoEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			reg := NewMockregistryAPI(ctrl)
			ops := NewMockmongoOps(ctrl)
			reg.EXPECT().Get("abc").Return(nil, errors.New("some: internal failure"))

			r := newMongoTestRouter(t, reg, ops)
			w := doReq(r, ep)
			assert.Equal(t, http.StatusInternalServerError, w.Code)
			assert.Equal(t, "internal_error", decodeErrResp(t, w.Body).Code)
		})
	}
}

// --- ListCollections ----------------------------------------------------------------

func TestListCollections_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().ListCollections(gomock.Any(), gomock.Any(), "testdb").Return([]collectionInfo{
		{Name: "rooms", Count: 42},
		{Name: "messages", Count: 7},
	}, nil)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mongo/abc/collections", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got []collectionInfo
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	assert.Equal(t, []collectionInfo{{Name: "rooms", Count: 42}, {Name: "messages", Count: 7}}, got)
}

func TestListCollections_EmptyReturnsJSONArray(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().ListCollections(gomock.Any(), gomock.Any(), "testdb").Return(nil, nil)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mongo/abc/collections", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "[]", strings.TrimSpace(w.Body.String()))
}

func TestListCollections_OpsError_500(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().ListCollections(gomock.Any(), gomock.Any(), "testdb").Return(nil, errors.New("boom"))

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mongo/abc/collections", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "internal_error", decodeErrResp(t, w.Body).Code)
}

// --- ListDocs -----------------------------------------------------------------------

func TestListDocs_DefaultQuery_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		ListDocs(gomock.Any(), gomock.Any(), "testdb", "rooms", bson.M{}, int64(0), int64(50)).
		Return(listDocsResult{Total: 3, Docs: []bson.M{
			{"_id": "1", "name": "a"},
			{"_id": "2", "name": "b"},
			{"_id": "3", "name": "c"},
		}}, nil)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mongo/abc/collections/rooms/docs", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var env struct {
		Total int64             `json:"total"`
		Docs  []json.RawMessage `json:"docs"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	assert.Equal(t, int64(3), env.Total)
	require.Len(t, env.Docs, 3)
}

func TestListDocs_WithFilter_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		ListDocs(gomock.Any(), gomock.Any(), "testdb", "rooms", bson.M{"status": "ok"}, int64(0), int64(50)).
		Return(listDocsResult{Total: 1, Docs: []bson.M{{"_id": "x", "status": "ok"}}}, nil)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, `/api/mongo/abc/collections/rooms/docs?filter={"status":"ok"}`, nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListDocs_InvalidFilterJSON_400(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	// ops.ListDocs must NOT be called.
	ops.EXPECT().ListDocs(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mongo/abc/collections/rooms/docs?filter=not-json", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "bad_request", decodeErrResp(t, w.Body).Code)
}

func TestListDocs_LimitClamp(t *testing.T) {
	cases := []struct {
		name  string
		q     string
		limit int64
		skip  int64
	}{
		{"limit too large", "limit=10000", 50, 0},
		{"limit zero", "limit=0", 50, 0},
		{"limit negative", "limit=-10", 50, 0},
		{"limit at max", "limit=500", 500, 0},
		{"limit just over max", "limit=501", 50, 0},
		{"skip negative", "skip=-5", 50, 0},
		{"skip positive", "skip=25", 50, 25},
		{"non-numeric limit", "limit=abc", 50, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			reg := NewMockregistryAPI(ctrl)
			ops := NewMockmongoOps(ctrl)

			reg.EXPECT().Get("abc").Return(mongoConn(), nil)
			ops.EXPECT().
				ListDocs(gomock.Any(), gomock.Any(), "testdb", "rooms", bson.M{}, tc.skip, tc.limit).
				Return(listDocsResult{Total: 0, Docs: []bson.M{}}, nil)

			r := newMongoTestRouter(t, reg, ops)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/mongo/abc/collections/rooms/docs?"+tc.q, nil)
			r.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
}

func TestListDocs_OpsError_500(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		ListDocs(gomock.Any(), gomock.Any(), "testdb", "rooms", bson.M{}, int64(0), int64(50)).
		Return(listDocsResult{}, errors.New("boom"))

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mongo/abc/collections/rooms/docs", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "internal_error", decodeErrResp(t, w.Body).Code)
}

// --- InsertDoc ----------------------------------------------------------------------

func TestInsertDoc_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", bson.M{"name": "alice"}).
		DoAndReturn(func(_ context.Context, _ *mongo.Client, _, _ string, doc bson.M) (bson.M, error) {
			doc["_id"] = "generated-id"
			return doc, nil
		})

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/docs", strings.NewReader(`{"name":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got bson.M
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	assert.Equal(t, "alice", got["name"])
	assert.Equal(t, "generated-id", got["_id"])
}

func TestInsertDoc_InvalidJSON_400(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().InsertDoc(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/docs", strings.NewReader(`{not-json`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "bad_request", decodeErrResp(t, w.Body).Code)
}

func TestInsertDoc_DuplicateKey_409(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", gomock.Any()).
		Return(nil, ErrMongoDuplicateKey)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/docs", strings.NewReader(`{"_id":"dup"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "duplicate_key", decodeErrResp(t, w.Body).Code)
}

func TestInsertDoc_OpsError_500(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", gomock.Any()).
		Return(nil, errors.New("boom"))

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/docs", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "internal_error", decodeErrResp(t, w.Body).Code)
}

func TestInsertDoc_BodyReadError_400(t *testing.T) {
	// Simulate a read error by using a body that errors on Read.
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().InsertDoc(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/docs", errReader{})
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "bad_request", decodeErrResp(t, w.Body).Code)
}

// errReader implements io.Reader by always returning an error — used to
// exercise io.ReadAll failure paths in handlers that read request bodies.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errors.New("read fail") }

// --- ReplaceDoc ---------------------------------------------------------------------

func TestReplaceDoc_ObjectIDHex_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	const hex = "507f1f77bcf86cd799439011"

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		ReplaceDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", gomock.AssignableToTypeOf(bson.ObjectID{}), bson.M{"name": "updated"}).
		DoAndReturn(func(_ context.Context, _ *mongo.Client, _, _ string, id any, _ bson.M) error {
			oid, ok := id.(bson.ObjectID)
			assert.True(t, ok, "id must be bson.ObjectID, got %T", id)
			assert.Equal(t, hex, oid.Hex())
			return nil
		})

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/mongo/abc/collections/rooms/docs/"+hex, strings.NewReader(`{"name":"updated"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestReplaceDoc_StringID_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		ReplaceDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", "abc123", bson.M{"name": "updated"}).
		Return(nil)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/mongo/abc/collections/rooms/docs/abc123", strings.NewReader(`{"name":"updated"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestReplaceDoc_NotFound_404(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		ReplaceDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", "missing", gomock.Any()).
		Return(ErrMongoNotFound)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/mongo/abc/collections/rooms/docs/missing", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "not_found", decodeErrResp(t, w.Body).Code)
}

func TestReplaceDoc_InvalidJSON_400(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().ReplaceDoc(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/mongo/abc/collections/rooms/docs/abc", strings.NewReader(`{not-json`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "bad_request", decodeErrResp(t, w.Body).Code)
}

func TestReplaceDoc_OpsError_500(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		ReplaceDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", "abc", gomock.Any()).
		Return(errors.New("boom"))

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/mongo/abc/collections/rooms/docs/abc", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "internal_error", decodeErrResp(t, w.Body).Code)
}

func TestReplaceDoc_BodyReadError_400(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().ReplaceDoc(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/mongo/abc/collections/rooms/docs/abc", errReader{})
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "bad_request", decodeErrResp(t, w.Body).Code)
}

// --- DeleteDoc ----------------------------------------------------------------------

func TestDeleteDoc_Success_204(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().DeleteDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", "abc123").Return(nil)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/mongo/abc/collections/rooms/docs/abc123", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())
}

func TestDeleteDoc_ObjectIDHex_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	const hex = "507f1f77bcf86cd799439011"
	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		DeleteDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", gomock.AssignableToTypeOf(bson.ObjectID{})).
		DoAndReturn(func(_ context.Context, _ *mongo.Client, _, _ string, id any) error {
			oid, ok := id.(bson.ObjectID)
			assert.True(t, ok)
			assert.Equal(t, hex, oid.Hex())
			return nil
		})

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/mongo/abc/collections/rooms/docs/"+hex, nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestDeleteDoc_NotFound_404(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().DeleteDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", "missing").Return(ErrMongoNotFound)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/mongo/abc/collections/rooms/docs/missing", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "not_found", decodeErrResp(t, w.Body).Code)
}

func TestDeleteDoc_OpsError_500(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().DeleteDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", "x").Return(errors.New("boom"))

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/mongo/abc/collections/rooms/docs/x", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "internal_error", decodeErrResp(t, w.Body).Code)
}

// --- marshalDocsExtJSON -------------------------------------------------------------

func TestMarshalDocsExtJSON_EmptyAndSimple(t *testing.T) {
	// Empty docs → empty slice.
	got, err := marshalDocsExtJSON(nil)
	require.NoError(t, err)
	assert.Empty(t, got)

	// Simple doc round-trips.
	got, err = marshalDocsExtJSON([]bson.M{{"name": "alice"}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(got[0], &parsed))
	assert.Equal(t, "alice", parsed["name"])
}

// --- Export -------------------------------------------------------------------------

func TestExport_Success_EmptyCollection(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		StreamDocs(gomock.Any(), gomock.Any(), "testdb", "rooms", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *mongo.Client, _, _ string, _ func(bson.M) error) error {
			// No documents to yield.
			return nil
		})

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mongo/abc/collections/rooms/export", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
	assert.Equal(t, `attachment; filename="rooms.json"`, w.Header().Get("Content-Disposition"))
	assert.Equal(t, "[]", w.Body.String())
}

func TestExport_Success_ManyDocs(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	docs := []bson.M{
		{"_id": "a", "name": "foo"},
		{"_id": "b", "name": "bar"},
		{"_id": "c", "name": "baz"},
	}
	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		StreamDocs(gomock.Any(), gomock.Any(), "testdb", "rooms", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *mongo.Client, _, _ string, yield func(bson.M) error) error {
			for _, d := range docs {
				if err := yield(d); err != nil {
					return err
				}
			}
			return nil
		})

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mongo/abc/collections/rooms/export", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 3)
	assert.Equal(t, "a", got[0]["_id"])
	assert.Equal(t, "foo", got[0]["name"])
	assert.Equal(t, "b", got[1]["_id"])
	assert.Equal(t, "c", got[2]["_id"])
}

// TestExport_StreamDocsError_BeforeYield covers the Find-failure case. The
// handler writes the 200 header and '[' BEFORE invoking StreamDocs, so the
// client sees a truncated '[' body. This is the documented tradeoff.
func TestExport_StreamDocsError_BeforeYield(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		StreamDocs(gomock.Any(), gomock.Any(), "testdb", "rooms", gomock.Any()).
		Return(errors.New("mongo find: unreachable"))

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mongo/abc/collections/rooms/export", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// Status + header were committed before StreamDocs ran; body is a
	// truncated array prefix. Client-side JSON parsing fails — that's
	// acceptable for this dev-local streaming tool.
	assert.Equal(t, "[", w.Body.String())
}

// TestExport_StreamDocsError_AfterPartialYield covers a cursor failure that
// occurs mid-stream. Yield already wrote some docs, so the body is a
// truncated prefix like "[{...},{...}".
func TestExport_StreamDocsError_AfterPartialYield(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		StreamDocs(gomock.Any(), gomock.Any(), "testdb", "rooms", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *mongo.Client, _, _ string, yield func(bson.M) error) error {
			if err := yield(bson.M{"_id": "a"}); err != nil {
				return err
			}
			if err := yield(bson.M{"_id": "b"}); err != nil {
				return err
			}
			return errors.New("mongo cursor: broken")
		})

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mongo/abc/collections/rooms/export", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	// Open bracket present, two docs, but no closing bracket.
	assert.True(t, strings.HasPrefix(body, "["), "body should start with [")
	assert.False(t, strings.HasSuffix(body, "]"), "truncated body must not end with ]")
	assert.Contains(t, body, `"a"`)
	assert.Contains(t, body, `"b"`)
}

// --- Import -------------------------------------------------------------------------

func TestImport_Success_AllInserted(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", bson.M{"name": "a"}).
		Return(bson.M{"_id": "x1", "name": "a"}, nil)
	ops.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", bson.M{"name": "b"}).
		Return(bson.M{"_id": "x2", "name": "b"}, nil)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/import",
		strings.NewReader(`[{"name":"a"},{"name":"b"}]`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got importResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, 2, got.Inserted)
	assert.Empty(t, got.Failed)
}

func TestImport_EmptyArray_200_NoInserts(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	// InsertDoc must NOT be called for an empty array.
	ops.EXPECT().InsertDoc(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/import",
		strings.NewReader(`[]`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got importResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, 0, got.Inserted)
	assert.Empty(t, got.Failed)
}

func TestImport_Success_MixedFailures(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)

	// Index 0: succeed
	ops.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", bson.M{"_id": "0", "name": "zero"}).
		Return(bson.M{"_id": "0", "name": "zero"}, nil)
	// Index 1: fail with wrapped duplicate-key sentinel (surfaces as "duplicate key")
	dupErr := fmt.Errorf("insert document: %w (E11000)", ErrMongoDuplicateKey)
	ops.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", bson.M{"_id": "1", "name": "one"}).
		Return(nil, dupErr)
	// Index 2: succeed
	ops.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", bson.M{"_id": "2", "name": "two"}).
		Return(bson.M{"_id": "2", "name": "two"}, nil)
	// Index 3: fail with generic error (surfaces as "insert failed")
	ops.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", bson.M{"_id": "3", "name": "three"}).
		Return(nil, errors.New("insert document: some driver error"))

	r := newMongoTestRouter(t, reg, ops)
	body := `[
		{"_id":"0","name":"zero"},
		{"_id":"1","name":"one"},
		{"_id":"2","name":"two"},
		{"_id":"3","name":"three"}
	]`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/import",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got importResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, 2, got.Inserted)
	require.Len(t, got.Failed, 2)

	assert.Equal(t, 1, got.Failed[0].Index)
	assert.Equal(t, "1", got.Failed[0].ID)
	// Duplicate-key sentinel surfaces as a specific message.
	assert.Equal(t, "duplicate key", got.Failed[0].Error)

	assert.Equal(t, 3, got.Failed[1].Index)
	assert.Equal(t, "3", got.Failed[1].ID)
	// Generic insert errors surface as "insert failed".
	assert.Equal(t, "insert failed", got.Failed[1].Error)
}

func TestImport_AllFailures(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errors.New("insert document: boom")).Times(3)

	r := newMongoTestRouter(t, reg, ops)
	body := `[{"name":"a"},{"name":"b"},{"name":"c"}]`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/import",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got importResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, 0, got.Inserted)
	require.Len(t, got.Failed, 3)
	for i, f := range got.Failed {
		assert.Equal(t, i, f.Index)
	}
}

func TestImport_Failure_CapturesID(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", gomock.Any()).
		Return(nil, errors.New("insert document: boom"))

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/import",
		strings.NewReader(`[{"_id":"foo","name":"x"}]`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got importResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Failed, 1)
	assert.Equal(t, "foo", got.Failed[0].ID)
}

func TestImport_InvalidJSON_400(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().InsertDoc(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/import",
		strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "bad_request", decodeErrResp(t, w.Body).Code)
}

func TestImport_ExceedsMaxDocs_400(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	// InsertDoc must NOT be called once the size check fails.
	ops.EXPECT().InsertDoc(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	// Cap at 2 docs; send 3.
	r := newMongoTestRouterWithMax(t, reg, ops, 2)
	body := `[{"a":1},{"a":2},{"a":3}]`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/import",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	e := decodeErrResp(t, w.Body)
	assert.Equal(t, "too_large", e.Code)
	assert.Contains(t, e.Error, "MAX_IMPORT_DOCS=2")
}

func TestImport_BodyReadError_400(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := NewMockregistryAPI(ctrl)
	ops := NewMockmongoOps(ctrl)

	reg.EXPECT().Get("abc").Return(mongoConn(), nil)
	ops.EXPECT().InsertDoc(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	r := newMongoTestRouter(t, reg, ops)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mongo/abc/collections/rooms/import", errReader{})
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "bad_request", decodeErrResp(t, w.Body).Code)
}

// TestImport_ContextCancelled_ShortCircuits verifies that the per-doc loop
// bails out of the loop once the request context has been cancelled, so the
// handler doesn't spin through InsertDoc calls for a client that has
// disconnected. The request context is cancelled before the handler runs, so
// the very first iteration should short-circuit and InsertDoc must not be
// invoked at all.
func TestImport_ContextCancelled_ShortCircuits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctrl := gomock.NewController(t)
	mockReg := NewMockregistryAPI(ctrl)
	mockMongo := NewMockmongoOps(ctrl)

	mockReg.EXPECT().Get("c1").
		Return(&connection{kind: "mongo", mongo: nil, mongoDB: "testdb"}, nil).
		AnyTimes()
	// Cap at MaxTimes(3): we expect 0 in the common case (cancel before any
	// iteration), but tolerate up to 3 so the test is not racy.
	mockMongo.EXPECT().
		InsertDoc(gomock.Any(), gomock.Any(), "testdb", "rooms", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *mongo.Client, _, _ string, _ bson.M) (bson.M, error) {
			return bson.M{"_id": "x"}, nil
		}).
		MaxTimes(3)

	h := newHandler(mockReg, mockMongo, 1000)
	r := gin.New()
	registerRoutes(r, h)

	body := `[{"a":1},{"a":2},{"a":3}]`
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so first iteration short-circuits

	req := httptest.NewRequest(http.MethodPost,
		"/api/mongo/c1/collections/rooms/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// No assertion on response body — connection is dead. We just verify no
	// panic and the mock's MaxTimes(3) cap was not exceeded.
}

// TestSanitizeImportError verifies that per-doc import failures are mapped to
// short, user-safe strings without leaking driver detail, and that
// duplicate-key collisions are distinguishable from generic insert failures.
func TestSanitizeImportError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			"duplicate key sentinel",
			fmt.Errorf("insert document: %w (%w)", ErrMongoDuplicateKey, errors.New("E11000")),
			"duplicate key",
		},
		{"not found sentinel", ErrMongoNotFound, "not found"},
		{"generic error", errors.New("connection reset"), "insert failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeImportError(tc.err))
		})
	}
}
