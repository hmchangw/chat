package main

import (
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
	DocumentKey              bson.Raw       `bson:"documentKey"`
	FullDocument             bson.Raw       `bson:"fullDocument"`
	FullDocumentBeforeChange bson.Raw       `bson:"fullDocumentBeforeChange"`
	ClusterTime              bson.Timestamp `bson:"clusterTime"`
}

// mongoChangeSource is a changeSource backed by a Mongo change stream over one
// collection.
type mongoChangeSource struct {
	cs *mongo.ChangeStream
}

// openMongoChangeSource opens a change stream on coll starting at sp. preimage
// requests fullDocumentBeforeChange (requires changeStreamPreAndPostImages on
// the source collection).
func openMongoChangeSource(ctx context.Context, coll *mongo.Collection, sp startPoint, preimage bool) (*mongoChangeSource, error) {
	opts := options.ChangeStream().SetFullDocument(options.UpdateLookup)
	if preimage {
		opts.SetFullDocumentBeforeChange(options.WhenAvailable)
	}
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

	return changeEvent{
		EventID:       idDoc.Data,
		ResumeToken:   append(bson.Raw(nil), resumeToken...), // copy; driver reuses its buffer
		Op:            d.OperationType,
		DB:            d.Ns.DB,
		Collection:    d.Ns.Coll,
		DocumentKey:   d.DocumentKey,
		FullDocument:  d.FullDocument,
		PreImage:      d.FullDocumentBeforeChange,
		ClusterTimeMs: int64(d.ClusterTime.T) * 1000,
	}
}
