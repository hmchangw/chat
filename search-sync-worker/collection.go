package main

import (
	"encoding/json"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/searchengine"
)

// Collection defines a search-indexable data source. Each collection
// encapsulates its own stream config, ES template, and document mapping.
// To add a new collection (e.g., room search), implement this interface.
type Collection interface {
	// StreamConfig returns the JetStream stream to create/update and consume
	// from. Each collection supplies a native jetstream.StreamConfig so it
	// can configure Subjects, Sources, SubjectTransforms, etc. as needed.
	StreamConfig(siteID string) jetstream.StreamConfig
	// ConsumerName returns the durable consumer name for this collection.
	ConsumerName() string
	// FilterSubjects returns the list of subjects this collection's consumer
	// should subscribe to. An empty slice means "all subjects in the stream".
	FilterSubjects(siteID string) []string
	// TemplateName returns the ES index template name. Empty string means the
	// collection has no index template to upsert (e.g., single-index targets).
	TemplateName() string
	// TemplateBody returns the ES index template JSON. nil means no template.
	TemplateBody() json.RawMessage
	// BuildAction converts raw JetStream message data into one or more
	// BulkActions. An empty slice means the event should be acked without
	// any ES write (e.g., filtered out).
	BuildAction(data []byte) ([]searchengine.BulkAction, error)
}
