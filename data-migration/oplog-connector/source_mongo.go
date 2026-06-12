package main

import (
	"bytes"
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// rawChangeDoc decodes the fields of a change-stream event we care about. All
// document bodies stay as raw BSON — opaque to the connector.
type rawChangeDoc struct {
	ID            bson.Raw `bson:"_id"` // resume token { _data: ... }
	OperationType string   `bson:"operationType"`
	Ns            struct {
		DB   string `bson:"db"`
		Coll string `bson:"coll"`
	} `bson:"ns"`
	DocumentKey       bson.Raw       `bson:"documentKey"`
	FullDocument      bson.Raw       `bson:"fullDocument"`
	UpdateDescription bson.Raw       `bson:"updateDescription"`
	ClusterTime       bson.Timestamp `bson:"clusterTime"`
}

// mongoChangeSource is a changeSource backed by a Mongo change stream over one
// collection.
type mongoChangeSource struct {
	cs *mongo.ChangeStream
}

// openMongoChangeSource opens a change stream on coll starting at sp. It does
// NO lookups: it relies on the native oplog content only — fullDocument for
// insert/replace, updateDescription (the delta) for update, documentKey for
// delete. updateLookup and pre-images are deliberately not requested; any
// enrichment is the downstream transformer's job.
func openMongoChangeSource(ctx context.Context, coll *mongo.Collection, sp startPoint) (*mongoChangeSource, error) {
	opts := options.ChangeStream()
	switch sp.Kind {
	case startAfterToken:
		opts.SetStartAfter(sp.Token)
	case startAtTime:
		opts.SetStartAtOperationTime(&bson.Timestamp{T: uint32(sp.TimeMs / 1000), I: 0})
	case startFromBeginning:
		// Mongo change streams cannot replay arbitrarily far back without a
		// token or operation time; "beginning" therefore behaves as "now".
		// The real start path is always a seed token or a stored checkpoint.
	case startFromNow:
		// default — stream from the current point.
	}

	cs, err := coll.Watch(ctx, mongo.Pipeline{}, opts)
	if err != nil {
		return nil, fmt.Errorf("open change stream on %q: %w", coll.Name(), err)
	}
	return &mongoChangeSource{cs: cs}, nil
}

func (m *mongoChangeSource) Next(ctx context.Context) (changeEvent, error) {
	if !m.cs.Next(ctx) {
		if err := m.cs.Err(); err != nil {
			return changeEvent{}, fmt.Errorf("change stream next: %w", err)
		}
		// Next returned false with no stream error → context ended/closed.
		if cause := context.Cause(ctx); cause != nil {
			return changeEvent{}, cause
		}
		return changeEvent{}, context.Canceled
	}

	var doc rawChangeDoc
	if err := m.cs.Decode(&doc); err != nil {
		return changeEvent{}, fmt.Errorf("decode change event: %w", err)
	}
	return doc.toChangeEvent(m.cs.ResumeToken()), nil
}

func (m *mongoChangeSource) Close(ctx context.Context) error {
	return m.cs.Close(ctx)
}

func (d *rawChangeDoc) toChangeEvent(resumeToken bson.Raw) changeEvent {
	var idDoc struct {
		Data string `bson:"_data"`
	}
	_ = bson.Unmarshal(d.ID, &idDoc)

	// Every bson.Raw here aliases the change stream's Current buffer, which the
	// driver documents as "only valid until the next call to Next". We clone all
	// of them so a changeEvent stays valid if it is buffered or held across a
	// Next() (e.g. by a downstream filter). bytes.Clone(nil) == nil, so omitempty
	// envelope fields still work.
	return changeEvent{
		EventID:           idDoc.Data,
		ResumeToken:       bson.Raw(bytes.Clone(resumeToken)),
		Op:                d.OperationType,
		DB:                d.Ns.DB,
		Collection:        d.Ns.Coll,
		DocumentKey:       bson.Raw(bytes.Clone(d.DocumentKey)),
		FullDocument:      bson.Raw(bytes.Clone(d.FullDocument)),
		UpdateDescription: bson.Raw(bytes.Clone(d.UpdateDescription)),
		// ClusterTime is a BSON timestamp (seconds, ordinal); we keep seconds only,
		// so events within the same second share this value — fine for the coarse
		// cross-collection sort, not a strict ordering key.
		ClusterTimeMs: int64(d.ClusterTime.T) * 1000,
	}
}
