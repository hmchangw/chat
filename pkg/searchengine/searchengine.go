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
	ActionUpdate ActionType = "update"
)

// BulkAction represents a single action in a bulk request.
//
// For ActionUpdate, Doc contains the full ES update body (doc / script /
// upsert) and Version is ignored. The _update operation is read-modify-write
// on the ES side and does not accept `version`/`version_type=external`; that
// parameter pair is only valid for `index` (full-document replacement).
type BulkAction struct {
	Action  ActionType
	Index   string
	DocID   string
	Version int64           // used as ES external version (ignored for ActionUpdate)
	Doc     json.RawMessage // index: full doc; update: update body; delete: nil
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
