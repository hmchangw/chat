# Search Sync Worker — Part 2: pkg/searchengine Adapter

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a backend-agnostic search engine adapter package supporting both Elasticsearch and OpenSearch via a shared HTTP Transporter interface.

**Architecture:** Three layers — `Transporter` (raw HTTP, satisfied by ES/OS clients natively), `SearchEngine` interface (domain operations), `httpAdapter` (builds HTTP requests, parses responses). Factory function selects backend by config.

**Tech Stack:** Go, `elastic/go-elasticsearch/v8`, `opensearch-project/opensearch-go/v4`

**Spec:** `docs/superpowers/specs/2026-04-07-search-sync-worker-design.md` — "Search Engine Adapter Pattern" section

**Depends on:** Nothing (independent of Part 1)

---

## File Structure

| Action | File | Purpose |
|--------|------|---------|
| Create | `pkg/searchengine/searchengine.go` | Transporter, SearchEngine interface, BulkAction, BulkResult types |
| Create | `pkg/searchengine/adapter.go` | httpAdapter implementing SearchEngine via raw HTTP on Transporter |
| Create | `pkg/searchengine/adapter_test.go` | Unit tests using fakeTransport |
| Create | `pkg/searchengine/factory.go` | `New(backend, url)` factory function |

---

### Task 3: Define SearchEngine interface and types

**Files:**
- Create: `pkg/searchengine/searchengine.go`

- [ ] **Step 1: Create `pkg/searchengine/searchengine.go`**

```go
package searchengine

import (
	"context"
	"encoding/json"
	"net/http"
)

// Transporter performs raw HTTP requests. Both elastic/go-elasticsearch and
// opensearch-go clients implement this interface natively.
type Transporter interface {
	Perform(req *http.Request) (*http.Response, error)
}

// ActionType represents the type of bulk action.
type ActionType string

const (
	ActionIndex  ActionType = "index"
	ActionDelete ActionType = "delete"
)

// BulkAction represents a single action in a bulk request.
type BulkAction struct {
	Action  ActionType
	Index   string
	DocID   string
	Version int64           // used as ES external version
	Doc     json.RawMessage // nil for delete actions
}

// BulkResult represents the result of a single bulk action item.
type BulkResult struct {
	Status int
	Error  string
}

// SearchEngine defines domain operations for search indexing.
type SearchEngine interface {
	Ping(ctx context.Context) error
	Bulk(ctx context.Context, actions []BulkAction) ([]BulkResult, error)
	UpsertTemplate(ctx context.Context, name string, body json.RawMessage) error
	GetIndexMapping(ctx context.Context, index string) (json.RawMessage, error)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./pkg/searchengine/`
Expected: Success (no errors)

- [ ] **Step 3: Commit**

```bash
git add pkg/searchengine/searchengine.go
git commit -m "feat(searchengine): add SearchEngine interface and types"
```

---

### Task 4: Implement and test httpAdapter

**Files:**
- Create: `pkg/searchengine/adapter.go`
- Create: `pkg/searchengine/adapter_test.go`

- [ ] **Step 1: Write failing tests in `pkg/searchengine/adapter_test.go`**

```go
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

// fakeTransport captures requests and returns configured responses.
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

		// Verify NDJSON body format
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
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `go test ./pkg/searchengine/ -v`
Expected: FAIL — `newAdapter` undefined

- [ ] **Step 3: Implement `pkg/searchengine/adapter.go`**

```go
package searchengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// bulkActionMeta is the metadata line in an ES _bulk NDJSON request.
type bulkActionMeta struct {
	Index       string `json:"_index"`
	ID          string `json:"_id"`
	Version     int64  `json:"version,omitempty"`
	VersionType string `json:"version_type,omitempty"`
}

// bulkResponse is the top-level ES _bulk response.
type bulkResponse struct {
	Items []map[string]bulkItemResult `json:"items"`
}

type bulkItemResult struct {
	Status int `json:"status"`
	Error  struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"error"`
}

type httpAdapter struct {
	transport Transporter
}

func newAdapter(transport Transporter) *httpAdapter {
	return &httpAdapter{transport: transport}
}

func (a *httpAdapter) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if err != nil {
		return fmt.Errorf("create ping request: %w", err)
	}
	resp, err := a.transport.Perform(req)
	if err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (a *httpAdapter) Bulk(ctx context.Context, actions []BulkAction) ([]BulkResult, error) {
	var buf bytes.Buffer
	for _, action := range actions {
		meta := bulkActionMeta{
			Index:       action.Index,
			ID:          action.DocID,
			Version:     action.Version,
			VersionType: "external",
		}
		switch action.Action {
		case ActionIndex:
			line, _ := json.Marshal(map[string]bulkActionMeta{"index": meta})
			buf.Write(line)
			buf.WriteByte('\n')
			buf.Write(action.Doc)
			buf.WriteByte('\n')
		case ActionDelete:
			line, _ := json.Marshal(map[string]bulkActionMeta{"delete": meta})
			buf.Write(line)
			buf.WriteByte('\n')
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/_bulk", &buf)
	if err != nil {
		return nil, fmt.Errorf("create bulk request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")

	resp, err := a.transport.Perform(req)
	if err != nil {
		return nil, fmt.Errorf("bulk request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bulk: status %d, body: %s", resp.StatusCode, body)
	}

	var bulkResp bulkResponse
	if err := json.NewDecoder(resp.Body).Decode(&bulkResp); err != nil {
		return nil, fmt.Errorf("decode bulk response: %w", err)
	}

	results := make([]BulkResult, len(bulkResp.Items))
	for i, item := range bulkResp.Items {
		for _, detail := range item {
			results[i] = BulkResult{
				Status: detail.Status,
				Error:  detail.Error.Reason,
			}
		}
	}
	return results, nil
}

func (a *httpAdapter) UpsertTemplate(ctx context.Context, name string, body json.RawMessage) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("/_index_template/%s", name), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create template request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.transport.Perform(req)
	if err != nil {
		return fmt.Errorf("upsert template: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upsert template: status %d, body: %s", resp.StatusCode, respBody)
	}
	return nil
}

func (a *httpAdapter) GetIndexMapping(ctx context.Context, index string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("/%s/_mapping", index), nil)
	if err != nil {
		return nil, fmt.Errorf("create mapping request: %w", err)
	}

	resp, err := a.transport.Perform(req)
	if err != nil {
		return nil, fmt.Errorf("get index mapping: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get index mapping: status %d, body: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read mapping response: %w", err)
	}
	return body, nil
}
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `go test ./pkg/searchengine/ -v -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/searchengine/adapter.go pkg/searchengine/adapter_test.go
git commit -m "feat(searchengine): implement httpAdapter with Bulk, Ping, UpsertTemplate, GetIndexMapping"
```

---

### Task 5: Factory function

**Files:**
- Create: `pkg/searchengine/factory.go`

- [ ] **Step 1: Add elasticsearch dependency**

Run: `go get github.com/elastic/go-elasticsearch/v8@latest`

- [ ] **Step 2: Create `pkg/searchengine/factory.go`**

```go
package searchengine

import (
	"context"
	"fmt"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
)

// New creates a SearchEngine for the given backend ("elasticsearch" or "opensearch").
// It verifies connectivity via Ping before returning.
func New(ctx context.Context, backend, url string) (SearchEngine, error) {
	var transport Transporter
	switch backend {
	case "elasticsearch":
		client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{url}})
		if err != nil {
			return nil, fmt.Errorf("create elasticsearch client: %w", err)
		}
		transport = client
	default:
		return nil, fmt.Errorf("unsupported search backend: %s", backend)
	}

	adapter := newAdapter(transport)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := adapter.Ping(pingCtx); err != nil {
		return nil, fmt.Errorf("search engine ping failed: %w", err)
	}

	return adapter, nil
}
```

Note: OpenSearch support can be added later by importing `github.com/opensearch-project/opensearch-go/v4` and adding a `case "opensearch"` branch.

- [ ] **Step 3: Verify it compiles**

Run: `go build ./pkg/searchengine/`
Expected: Success

- [ ] **Step 4: Run all searchengine tests**

Run: `go test ./pkg/searchengine/ -v -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/searchengine/factory.go go.mod go.sum
git commit -m "feat(searchengine): add factory function with Elasticsearch backend"
```
