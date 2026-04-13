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
