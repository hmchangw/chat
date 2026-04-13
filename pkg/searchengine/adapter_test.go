package searchengine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTransport struct {
	handler func(req *http.Request) (*http.Response, error)
}

func (f *fakeTransport) Perform(req *http.Request) (*http.Response, error) {
	return f.handler(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestAdapter_Ping(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ft := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "/", req.URL.Path)
			return jsonResponse(200, `{}`), nil
		}}
		a := newAdapter(ft)
		err := a.Ping(context.Background())
		assert.NoError(t, err)
	})

	t.Run("server error", func(t *testing.T) {
		ft := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(503, `{}`), nil
		}}
		a := newAdapter(ft)
		err := a.Ping(context.Background())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "503")
	})
}

func TestAdapter_Bulk(t *testing.T) {
	t.Run("index and delete actions", func(t *testing.T) {
		var capturedBody string
		ft := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, http.MethodPost, req.Method)
			assert.Equal(t, "/_bulk", req.URL.Path)
			assert.Equal(t, "application/x-ndjson", req.Header.Get("Content-Type"))
			body, _ := io.ReadAll(req.Body)
			capturedBody = string(body)
			return jsonResponse(200, `{
				"items": [
					{"index": {"status": 201}},
					{"delete": {"status": 200}}
				]
			}`), nil
		}}
		a := newAdapter(ft)
		actions := []BulkAction{
			{Action: ActionIndex, Index: "msgs-2026-01", DocID: "m1", Version: 100, Doc: json.RawMessage(`{"msg":"hello"}`)},
			{Action: ActionDelete, Index: "msgs-2026-01", DocID: "m2", Version: 200},
		}
		results, err := a.Bulk(context.Background(), actions)
		require.NoError(t, err)
		require.Len(t, results, 2)
		assert.Equal(t, 201, results[0].Status)
		assert.Equal(t, 200, results[1].Status)

		lines := strings.Split(strings.TrimSpace(capturedBody), "\n")
		assert.Len(t, lines, 3) // index meta + doc + delete meta

		var indexMeta map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &indexMeta))
		idx := indexMeta["index"].(map[string]any)
		assert.Equal(t, "msgs-2026-01", idx["_index"])
		assert.Equal(t, "m1", idx["_id"])
		assert.Equal(t, "external", idx["version_type"])
		assert.Equal(t, float64(100), idx["version"])

		var deleteMeta map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[2]), &deleteMeta))
		del := deleteMeta["delete"].(map[string]any)
		assert.Equal(t, "m2", del["_id"])
	})

	t.Run("version conflict treated as result not error", func(t *testing.T) {
		ft := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(200, `{
				"items": [{"index": {"status": 409, "error": {"type": "version_conflict_engine_exception", "reason": "stale"}}}]
			}`), nil
		}}
		a := newAdapter(ft)
		results, err := a.Bulk(context.Background(), []BulkAction{
			{Action: ActionIndex, Index: "idx", DocID: "m1", Version: 1, Doc: json.RawMessage(`{}`)},
		})
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, 409, results[0].Status)
		assert.Equal(t, "stale", results[0].Error)
	})

	t.Run("HTTP error returns error", func(t *testing.T) {
		ft := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(503, `service unavailable`), nil
		}}
		a := newAdapter(ft)
		_, err := a.Bulk(context.Background(), []BulkAction{
			{Action: ActionIndex, Index: "idx", DocID: "m1", Version: 1, Doc: json.RawMessage(`{}`)},
		})
		assert.Error(t, err)
	})
}

func TestAdapter_UpsertTemplate(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ft := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, http.MethodPut, req.Method)
			assert.Equal(t, "/_index_template/my_template", req.URL.Path)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			body, _ := io.ReadAll(req.Body)
			assert.JSONEq(t, `{"index_patterns":["test-*"]}`, string(body))
			return jsonResponse(200, `{"acknowledged": true}`), nil
		}}
		a := newAdapter(ft)
		err := a.UpsertTemplate(context.Background(), "my_template", json.RawMessage(`{"index_patterns":["test-*"]}`))
		assert.NoError(t, err)
	})

	t.Run("error status", func(t *testing.T) {
		ft := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(400, `{"error":"bad request"}`), nil
		}}
		a := newAdapter(ft)
		err := a.UpsertTemplate(context.Background(), "t", json.RawMessage(`{}`))
		assert.Error(t, err)
	})
}

func TestAdapter_GetIndexMapping(t *testing.T) {
	t.Run("index exists", func(t *testing.T) {
		ft := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "/my-index/_mapping", req.URL.Path)
			return jsonResponse(200, `{"my-index":{"mappings":{"properties":{"msg":{"type":"text"}}}}}`), nil
		}}
		a := newAdapter(ft)
		mapping, err := a.GetIndexMapping(context.Background(), "my-index")
		require.NoError(t, err)
		require.NotNil(t, mapping)
		assert.Contains(t, string(mapping), "msg")
	})

	t.Run("index does not exist", func(t *testing.T) {
		ft := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(404, `{}`), nil
		}}
		a := newAdapter(ft)
		mapping, err := a.GetIndexMapping(context.Background(), "missing")
		require.NoError(t, err)
		assert.Nil(t, mapping)
	})
}
