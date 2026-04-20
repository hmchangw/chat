package main

import (
	"encoding/json"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/searchengine"
)

// Collection defines a search-indexable data source. Each collection
// encapsulates its own stream config, ES template, and document mapping.
type Collection interface {
	StreamConfig(siteID string) jetstream.StreamConfig
	ConsumerName() string
	FilterSubjects(siteID string) []string
	TemplateName() string
	TemplateBody() json.RawMessage
	BuildAction(data []byte) ([]searchengine.BulkAction, error)
}
