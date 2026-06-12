package model

import "encoding/json"

// OplogEvent is the envelope the oplog-connector publishes to
// MIGRATION_OPLOG_{siteID} for each change tailed from the legacy source
// MongoDB. Documents stay opaque (json.RawMessage) so the connector remains
// collection-agnostic — the downstream transformer decodes them per
// collection. This is deferred decoding, not map[string]interface{}.
type OplogEvent struct {
	// EventID is the change-stream event id (_id._data). It is also set as the
	// Nats-Msg-Id header so JetStream dedup collapses replays and the
	// migration-handoff overlap.
	EventID string `json:"eventId" bson:"eventId"`
	// Op is the change-stream operation type: insert | update | replace | delete.
	Op string `json:"op" bson:"op"`
	// DB is the source database name.
	DB string `json:"db" bson:"db"`
	// Collection is the raw source collection name (e.g. rocketchat_message).
	Collection string `json:"coll" bson:"coll"`
	// DocumentKey is the changed document's key, opaque ({ _id: ... }).
	DocumentKey json.RawMessage `json:"documentKey" bson:"documentKey"`
	// ClusterTime is the source op time in unix milliseconds.
	ClusterTime int64 `json:"clusterTime" bson:"clusterTime"`
	// FullDocument is the document, present natively for insert and replace
	// (it's in the oplog entry — no lookup). Absent for update and delete.
	FullDocument json.RawMessage `json:"fullDocument,omitempty" bson:"fullDocument,omitempty"`
	// UpdateDescription is the raw change delta (updatedFields/removedFields/
	// truncatedArrays) for update ops. Absent otherwise. The connector forwards
	// it verbatim; the transformer applies it. No updateLookup post-image is
	// fetched — that is a lookup, which belongs downstream.
	UpdateDescription json.RawMessage `json:"updateDescription,omitempty" bson:"updateDescription,omitempty"`
	// SiteID is the site scope.
	SiteID string `json:"siteId" bson:"siteId"`
	// Timestamp is the event-level publish time in unix milliseconds.
	Timestamp int64 `json:"timestamp" bson:"timestamp"`
}
