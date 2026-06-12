package main

import (
	"encoding/json"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// changeEvent is the connector-internal, decoded form of one Mongo
// change-stream event. Documents stay as raw BSON until buildEnvelope converts
// them to opaque JSON — the connector never interprets their contents.
type changeEvent struct {
	EventID           string   // _id._data — also the Nats-Msg-Id dedup key
	ResumeToken       bson.Raw // full change-stream _id document, fed back verbatim
	Op                string   // insert | update | replace | delete
	DB                string
	Collection        string   // raw source collection name
	DocumentKey       bson.Raw // { _id: ... }
	FullDocument      bson.Raw // document, native for insert/replace (no lookup)
	UpdateDescription bson.Raw // change delta, update only
	ClusterTimeMs     int64    // source op time, unix ms
}

// buildEnvelope maps a change event to its publish subject, dedup id, and the
// opaque OplogEvent envelope. nowMs is the event-level publish timestamp,
// injected so the function stays pure (no time.Now) and unit-testable.
func buildEnvelope(ev *changeEvent, siteID string, nowMs int64) (subj, msgID string, evt model.OplogEvent, err error) {
	subj = subject.MigrationOplog(siteID, ev.Collection, ev.Op)
	msgID = ev.EventID

	docKey, err := rawToJSON(ev.DocumentKey)
	if err != nil {
		return "", "", model.OplogEvent{}, fmt.Errorf("encode documentKey for %s: %w", msgID, err)
	}
	full, err := rawToJSON(ev.FullDocument)
	if err != nil {
		return "", "", model.OplogEvent{}, fmt.Errorf("encode fullDocument for %s: %w", msgID, err)
	}
	updateDesc, err := rawToJSON(ev.UpdateDescription)
	if err != nil {
		return "", "", model.OplogEvent{}, fmt.Errorf("encode updateDescription for %s: %w", msgID, err)
	}

	evt = model.OplogEvent{
		EventID:           ev.EventID,
		Op:                ev.Op,
		DB:                ev.DB,
		Collection:        ev.Collection,
		DocumentKey:       docKey,
		ClusterTime:       ev.ClusterTimeMs,
		FullDocument:      full,
		UpdateDescription: updateDesc,
		SiteID:            siteID,
		Timestamp:         nowMs,
	}
	return subj, msgID, evt, nil
}

// rawToJSON converts a raw BSON document to relaxed extended JSON, valid for
// the json.RawMessage envelope fields. Empty input → nil (so omitempty fires).
func rawToJSON(r bson.Raw) (json.RawMessage, error) {
	if len(r) == 0 {
		return nil, nil
	}
	b, err := bson.MarshalExtJSON(r, false, false)
	if err != nil {
		return nil, fmt.Errorf("marshal bson to ext json: %w", err)
	}
	return json.RawMessage(b), nil
}
