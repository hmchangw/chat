package model

import "encoding/json"

// HRSyncEvent is the envelope hr-syncer publishes on every hr.sync.*
// subject. Each consumer defines its own local projection struct
// for the payload elements.
//
// When Gzip=true the payload rides as a JSON string of base64(gzip(JSON
// array)) — json.RawMessage must itself be valid JSON, so raw binary
// bytes can't be embedded directly. Decoders unmarshal into []byte
// (which base64-decodes) and then gunzip.
type HRSyncEvent struct {
	Timestamp int64           `json:"timestamp" bson:"timestamp"`
	BatchID   string          `json:"batchId"   bson:"batchId"`
	Gzip      bool            `json:"gzip"      bson:"gzip"`
	Payload   json.RawMessage `json:"payload"   bson:"payload"`
}
