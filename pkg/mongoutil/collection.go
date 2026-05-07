package mongoutil

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Collection wraps *mongo.Collection. Goroutine-safe.
type Collection[T any] struct {
	col  *mongo.Collection
	name string
}

func NewCollection[T any](col *mongo.Collection) *Collection[T] {
	return &Collection[T]{col: col, name: col.Name()}
}

// FindOne returns (nil, nil) on no match.
func (c *Collection[T]) FindOne(ctx context.Context, filter any, opts ...QueryOption) (*T, error) {
	var result T
	err := c.col.FindOne(ctx, filter, apply(opts).findOneOpts()).Decode(&result)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding %s: %w", c.name, err)
	}
	return &result, nil
}

func (c *Collection[T]) FindByID(ctx context.Context, id string, opts ...QueryOption) (*T, error) {
	return c.FindOne(ctx, bson.M{"_id": id}, opts...)
}

// FindMany returns []T{} (not nil) on no match so JSON marshals to [].
func (c *Collection[T]) FindMany(ctx context.Context, filter any, opts ...QueryOption) ([]T, error) {
	cursor, err := c.col.Find(ctx, filter, apply(opts).findOpts())
	if err != nil {
		return nil, fmt.Errorf("querying %s: %w", c.name, err)
	}
	var results []T
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decoding %s results: %w", c.name, err)
	}
	if results == nil {
		results = []T{}
	}
	return results, nil
}

func (c *Collection[T]) Raw() *mongo.Collection { return c.col }

func (c *Collection[T]) Aggregate(ctx context.Context, pipeline bson.A) ([]T, error) {
	cursor, err := c.col.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregating %s: %w", c.name, err)
	}
	var results []T
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decoding %s aggregate: %w", c.name, err)
	}
	if results == nil {
		results = []T{}
	}
	return results, nil
}

// AggregatePaged appends a $facet stage. Watch the 16 MB BSON limit on the facet output.
func (c *Collection[T]) AggregatePaged(ctx context.Context, pipeline bson.A, req OffsetPageRequest) (OffsetPage[T], error) {
	facet := bson.D{{Key: "$facet", Value: bson.M{
		"data": bson.A{
			bson.D{{Key: "$skip", Value: req.Offset}},
			bson.D{{Key: "$limit", Value: req.Limit}},
		},
		"total": bson.A{
			bson.D{{Key: "$count", Value: "count"}},
		},
	}}}
	full := make(bson.A, 0, len(pipeline)+1)
	full = append(full, pipeline...)
	full = append(full, facet)

	cursor, err := c.col.Aggregate(ctx, full)
	if err != nil {
		return OffsetPage[T]{}, fmt.Errorf("aggregating %s: %w", c.name, err)
	}
	var wrapper []facetResult[T]
	if err := cursor.All(ctx, &wrapper); err != nil {
		return OffsetPage[T]{}, fmt.Errorf("decoding %s facet: %w", c.name, err)
	}
	if len(wrapper) == 0 {
		return EmptyPage[T](), nil
	}
	data := wrapper[0].Data
	if data == nil {
		data = []T{}
	}
	var total int64
	if len(wrapper[0].Total) > 0 {
		total = wrapper[0].Total[0].Count
	}
	return OffsetPage[T]{Data: data, Total: total}, nil
}

type facetResult[T any] struct {
	Data  []T           `bson:"data"`
	Total []countResult `bson:"total"`
}

type countResult struct {
	Count int64 `bson:"count"`
}

// BulkWrite executes models with SetOrdered(false). Empty input -> (nil, nil).
// On partial failure returns (*BulkResult, wrapped err); use errors.As(&mongo.BulkWriteException{}).
func (c *Collection[T]) BulkWrite(ctx context.Context, models []mongo.WriteModel) (*BulkResult, error) {
	if len(models) == 0 {
		return nil, nil
	}
	res, err := c.col.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	mapped := fromDriverResult(res)
	if err != nil {
		return mapped, fmt.Errorf("bulk write %s: %w", c.name, err)
	}
	return mapped, nil
}

// BulkUpsert sends {$set: item} per item with _id stripped (immutable in Mongo).
// $set is MERGE not REPLACE: stored fields not in T are preserved.
// omitempty caveat: zero-valued fields are dropped from $set.
// createdAt-style fields are rewritten on every call — use BulkWrite with $setOnInsert if that matters.
// Empty input -> (nil, nil); callers must nil-check.
func (c *Collection[T]) BulkUpsert(ctx context.Context, items []T, filter func(T) any) (*BulkResult, error) {
	if len(items) == 0 {
		return nil, nil
	}
	models := make([]mongo.WriteModel, 0, len(items))
	for _, it := range items {
		setDoc, err := bsonSetWithoutID(it)
		if err != nil {
			return nil, fmt.Errorf("bulk upsert %s marshal item: %w", c.name, err)
		}
		models = append(models, UpsertModel(filter(it), bson.M{"$set": setDoc}))
	}
	return c.BulkWrite(ctx, models)
}

// BulkUpsertByID is BulkUpsert with bson.M{"_id": idFn(item)} as the filter.
// Cheapest possible upsert pattern (_id is always indexed).
func (c *Collection[T]) BulkUpsertByID(ctx context.Context, items []T, idFn func(T) string) (*BulkResult, error) {
	return c.BulkUpsert(ctx, items, func(item T) any {
		return bson.M{"_id": idFn(item)}
	})
}

// InsertMany sends docs unordered. Returns count of successful inserts (from len(InsertedIDs)).
// Detect duplicate-key collisions with mongo.IsDuplicateKeyError(err).
// Empty input -> (0, nil).
func (c *Collection[T]) InsertMany(ctx context.Context, items []T) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	docs := make([]any, 0, len(items))
	for _, it := range items {
		docs = append(docs, it)
	}
	res, err := c.col.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	var inserted int64
	if res != nil {
		inserted = int64(len(res.InsertedIDs))
	}
	if err != nil {
		return inserted, fmt.Errorf("insert many %s: %w", c.name, err)
	}
	return inserted, nil
}
