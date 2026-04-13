package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/searchengine"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store

// Store defines the search engine operations needed by the handler.
type Store interface {
	Bulk(ctx context.Context, actions []searchengine.BulkAction) ([]searchengine.BulkResult, error)
}
