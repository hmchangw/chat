package main

import (
	"bytes"
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// rawChangeDoc decodes the change-stream fields we use; document bodies stay opaque raw BSON.
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

// mongoChangeSource is a changeSource backed by a Mongo change stream over one collection.
type mongoChangeSource struct {
	cs *mongo.ChangeStream
}

// openMongoChangeSource opens a change stream at sp with NO lookups/pre-images — native oplog only (fullDocument, updateDescription, documentKey); enrichment is the transformer's job.
func openMongoChangeSource(ctx context.Context, coll *mongo.Collection, sp startPoint) (*mongoChangeSource, error) {
	opts := options.ChangeStream()
	switch sp.Kind {
	case startAfterToken:
		opts.SetStartAfter(sp.Token)
	case startAtTime:
		secs := sp.TimeMs / 1000
		if secs < 0 || secs > int64(^uint32(0)) {
			return nil, fmt.Errorf("startAtOperationTime out of range: %dms", sp.TimeMs)
		}
		// #nosec G115 -- secs bounded to [0, math.MaxUint32] by the check above
		opts.SetStartAtOperationTime(&bson.Timestamp{T: uint32(secs), I: 0})
	case startFromBeginning:
		// Mongo can't replay arbitrarily far back without a token/time, so "beginning"
		// behaves as "now"; the real start is always a seed token or stored checkpoint.
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
	ce, err := doc.toChangeEvent(m.cs.ResumeToken())
	if err != nil {
		return changeEvent{}, fmt.Errorf("change event %q: %w", doc.OperationType, err)
	}
	return ce, nil
}

func (m *mongoChangeSource) Close(ctx context.Context) error {
	return m.cs.Close(ctx)
}

func (d *rawChangeDoc) toChangeEvent(resumeToken bson.Raw) (changeEvent, error) {
	var idDoc struct {
		Data string `bson:"_data"`
	}
	if err := bson.Unmarshal(d.ID, &idDoc); err != nil {
		return changeEvent{}, fmt.Errorf("decode resume token _id: %w", err)
	}
	if idDoc.Data == "" {
		// Mongo guarantees a non-empty _data on every change event; an empty one is a
		// serious driver/server anomaly — fail loud rather than emit an undedup-able event.
		return changeEvent{}, fmt.Errorf("resume token _id._data is empty")
	}

	// Each bson.Raw aliases the stream's Current buffer (valid only until the next Next),
	// so clone to stay valid if held across a Next(). bytes.Clone(nil)==nil preserves omitempty.
	return changeEvent{
		EventID:           idDoc.Data,
		ResumeToken:       bson.Raw(bytes.Clone(resumeToken)),
		Op:                d.OperationType,
		DB:                d.Ns.DB,
		Collection:        d.Ns.Coll,
		DocumentKey:       bson.Raw(bytes.Clone(d.DocumentKey)),
		FullDocument:      bson.Raw(bytes.Clone(d.FullDocument)),
		UpdateDescription: bson.Raw(bytes.Clone(d.UpdateDescription)),
		// Seconds only, so events within the same second share this value — fine for the
		// coarse cross-collection sort, not a strict ordering key.
		ClusterTimeMs: int64(d.ClusterTime.T) * 1000,
	}, nil
}
