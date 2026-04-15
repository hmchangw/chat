package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

//go:generate mockgen -source=mongo_ops.go -destination=mock_mongo_ops_test.go -package=main -typed

// Sentinel errors returned by mongoOps implementations. Handler code uses
// errors.Is to translate them into HTTP status codes.
var (
	// ErrMongoDuplicateKey is wrapped by InsertDoc when the underlying driver
	// reports a duplicate-key violation (11000). Handlers map this to HTTP 409.
	ErrMongoDuplicateKey = errors.New("duplicate key")
	// ErrMongoNotFound is returned by ReplaceDoc/DeleteDoc when MatchedCount
	// or DeletedCount is zero. Handlers map this to HTTP 404.
	ErrMongoNotFound = errors.New("not found")
)

// collectionInfo describes a Mongo collection surfaced via the API.
type collectionInfo struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// listDocsResult is the internal return shape for ListDocs. The handler
// re-marshals Docs using bson.MarshalExtJSON so ObjectIDs and dates round-trip
// cleanly through JSON — so this struct is not itself sent to clients.
type listDocsResult struct {
	Total int64    `json:"total"`
	Docs  []bson.M `json:"docs"`
}

// mongoOps is the narrow seam the HTTP handlers use to talk to MongoDB.
// Keeping it off *mongo.Client makes the handlers unit-testable with gomock.
type mongoOps interface {
	ListCollections(ctx context.Context, client *mongo.Client, db string) ([]collectionInfo, error)
	ListDocs(ctx context.Context, client *mongo.Client, db, coll string, filter bson.M, skip, limit int64) (listDocsResult, error)
	InsertDoc(ctx context.Context, client *mongo.Client, db, coll string, doc bson.M) (bson.M, error)
	ReplaceDoc(ctx context.Context, client *mongo.Client, db, coll string, id any, doc bson.M) error
	DeleteDoc(ctx context.Context, client *mongo.Client, db, coll string, id any) error
}

// mongoOpsImpl is the real implementation of mongoOps backed by the v2 driver.
type mongoOpsImpl struct{}

// newMongoOps returns the default concrete mongoOps.
func newMongoOps() *mongoOpsImpl { return &mongoOpsImpl{} }

// ListCollections returns each collection's name and an estimated document
// count. EstimatedDocumentCount is O(1) and good enough for a dev tool; we
// avoid CountDocuments here because the caller lists every collection.
func (mongoOpsImpl) ListCollections(ctx context.Context, client *mongo.Client, db string) ([]collectionInfo, error) {
	d := client.Database(db)
	names, err := d.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("list collection names: %w", err)
	}
	out := make([]collectionInfo, 0, len(names))
	for _, name := range names {
		count, err := d.Collection(name).EstimatedDocumentCount(ctx)
		if err != nil {
			return nil, fmt.Errorf("estimate count for %s: %w", name, err)
		}
		out = append(out, collectionInfo{Name: name, Count: count})
	}
	return out, nil
}

// ListDocs returns a page of documents matching filter, along with the total
// count for that same filter. CountDocuments is used (not Estimated) so the
// total respects the filter; the cost is acceptable for a dev tool.
func (mongoOpsImpl) ListDocs(ctx context.Context, client *mongo.Client, db, coll string, filter bson.M, skip, limit int64) (listDocsResult, error) {
	c := client.Database(db).Collection(coll)
	total, err := c.CountDocuments(ctx, filter)
	if err != nil {
		return listDocsResult{}, fmt.Errorf("count documents: %w", err)
	}
	cur, err := c.Find(ctx, filter, options.Find().SetSkip(skip).SetLimit(limit))
	if err != nil {
		return listDocsResult{}, fmt.Errorf("find documents: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()
	var docs []bson.M
	if err := cur.All(ctx, &docs); err != nil {
		return listDocsResult{}, fmt.Errorf("decode documents: %w", err)
	}
	if docs == nil {
		docs = []bson.M{}
	}
	return listDocsResult{Total: total, Docs: docs}, nil
}

// InsertDoc inserts doc and returns it with its resolved _id populated. A
// duplicate-key violation is wrapped with ErrMongoDuplicateKey so handlers
// can reply 409 without leaking driver internals.
func (mongoOpsImpl) InsertDoc(ctx context.Context, client *mongo.Client, db, coll string, doc bson.M) (bson.M, error) {
	c := client.Database(db).Collection(coll)
	res, err := c.InsertOne(ctx, doc)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, fmt.Errorf("%w: %v", ErrMongoDuplicateKey, err)
		}
		return nil, fmt.Errorf("insert document: %w", err)
	}
	if _, ok := doc["_id"]; !ok {
		doc["_id"] = res.InsertedID
	}
	return doc, nil
}

// ReplaceDoc replaces the document with the given _id. If no document matches,
// it returns ErrMongoNotFound (handler maps to 404).
func (mongoOpsImpl) ReplaceDoc(ctx context.Context, client *mongo.Client, db, coll string, id any, doc bson.M) error {
	c := client.Database(db).Collection(coll)
	res, err := c.ReplaceOne(ctx, bson.M{"_id": id}, doc)
	if err != nil {
		return fmt.Errorf("replace document: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrMongoNotFound
	}
	return nil
}

// DeleteDoc deletes the document with the given _id. If no document matches,
// it returns ErrMongoNotFound.
func (mongoOpsImpl) DeleteDoc(ctx context.Context, client *mongo.Client, db, coll string, id any) error {
	c := client.Database(db).Collection(coll)
	res, err := c.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	if res.DeletedCount == 0 {
		return ErrMongoNotFound
	}
	return nil
}

// parseDocID interprets docID first as a Mongo ObjectID hex string; if that
// fails, it returns the literal string value. Mongo _ids can be ObjectIDs,
// UUID strings, or arbitrary strings, so callers treat the return value as any.
func parseDocID(docID string) any {
	if oid, err := bson.ObjectIDFromHex(docID); err == nil {
		return oid
	}
	return docID
}
