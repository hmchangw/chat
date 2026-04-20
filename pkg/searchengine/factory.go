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
