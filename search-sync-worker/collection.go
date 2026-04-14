package main

import (
	"encoding/json"

	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/stream"
)

// Collection defines a search-indexable data source. Each collection
// encapsulates its own stream config, ES template, and document mapping.
// To add a new collection (e.g., room search), implement this interface.
type Collection interface {
	// StreamConfig returns the JetStream stream to consume from.
	StreamConfig(siteID string) stream.Config
	// ConsumerName returns the durable consumer name for this collection.
	ConsumerName() string
	// TemplateName returns the ES index template name.
	TemplateName() string
	// TemplateBody returns the ES index template JSON.
	TemplateBody() json.RawMessage
	// BuildAction converts raw JetStream message data into a BulkAction.
	BuildAction(data []byte) (searchengine.BulkAction, error)
}
