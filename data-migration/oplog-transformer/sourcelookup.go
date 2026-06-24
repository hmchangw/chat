package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// sourceLookup fetches the current full message doc from the source by _id.
type sourceLookup interface {
	// FindByID returns the raw BSON-extended-JSON document, or (nil, nil) if absent.
	FindByID(ctx context.Context, id string) ([]byte, error)
}

type mongoSourceLookup struct {
	coll *mongo.Collection
}

func newMongoSourceLookup(coll *mongo.Collection) *mongoSourceLookup {
	return &mongoSourceLookup{coll: coll}
}

// FindByID reads the doc and re-encodes it as relaxed extended JSON, matching the shape
// messagemap expects (same as the connector emits).
func (m *mongoSourceLookup) FindByID(ctx context.Context, id string) (out []byte, err error) {
	ctx, span := otel.Tracer("oplog-transformer").Start(ctx, "source.findByID")
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	var raw bson.Raw
	if derr := m.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&raw); errors.Is(derr, mongo.ErrNoDocuments) {
		return nil, nil // a miss is not a span error — the named err stays nil
	} else if derr != nil {
		err = derr
	}
	if err != nil {
		return nil, fmt.Errorf("source find %q: %w", id, err)
	}
	out, err = bson.MarshalExtJSON(raw, false, false)
	if err != nil {
		return nil, fmt.Errorf("encode source doc %q: %w", id, err)
	}
	return out, nil
}
