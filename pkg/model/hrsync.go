package model

import "encoding/json"

// HRSyncEvent is the envelope `hr-syncer` publishes on every
// `hr.sync.*` subject. Mirrors OutboxEvent: small fixed metadata plus
// an opaque payload the consumer types per-subject.
//
// Wire format of `Payload` depends on `Gzip`:
//
//   - Gzip=false: Payload is a JSON array whose element shape varies
//     by subject. Embedded verbatim inside the envelope JSON.
//
//   - Gzip=true: Payload is a JSON string carrying the BASE64-encoded
//     gzip-compressed bytes of the array. This is required because
//     `json.RawMessage` must be valid JSON — raw binary bytes cannot
//     ride inside the envelope directly. Go's `json.Unmarshal` decodes
//     a JSON string into `[]byte` via base64 automatically, so the
//     consumer just unmarshals into `[]byte` and then gunzips. The
//     wire layout therefore looks like:
//
//     {"timestamp":...,"gzip":true,"payload":"H4sIAAAAA..."}
//
//     where the payload value is a JSON-quoted base64 blob, not a JSON
//     array literal.
//
// Each consumer defines its own local projection struct carrying only
// the fields it reads (the projection type lives in the consumer, not
// in this package, so that hr-syncer's full Employee/Org/User shapes
// remain owned by hr-syncer). For example, `search-sync-worker`'s
// spotlight-org collection unmarshals `hr.sync.{siteID}.employees.upsert`
// payloads into its own `SpotlightOrgIndex` rows.
//
// Timestamp is set at the publish site in milliseconds since epoch;
// consumers reject `Timestamp <= 0`. BatchID is a UUIDv7 used for
// end-to-end tracing of one cron run.
type HRSyncEvent struct {
	Timestamp int64           `json:"timestamp" bson:"timestamp"`
	BatchID   string          `json:"batchId"   bson:"batchId"`
	Gzip      bool            `json:"gzip"      bson:"gzip"`
	Payload   json.RawMessage `json:"payload"   bson:"payload"`
}
