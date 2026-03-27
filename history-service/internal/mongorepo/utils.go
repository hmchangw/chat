package mongorepo

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// QueryOptions configures optional query behavior for Collection methods.
// Use the typed terminal methods (findOneOpts, findOpts) to convert into
// the appropriate mongo-driver options for each operation.
type QueryOptions struct {
	Projection any
	Sort       any
	Limit      *int64
	Skip       *int64
}

// findOneOpts produces mongo FindOne options. Only projection is applied —
// sort, limit, and skip are not relevant for single-document lookups.
func (qo *QueryOptions) findOneOpts() *options.FindOneOptionsBuilder {
	opts := options.FindOne()
	if qo.Projection != nil {
		opts.SetProjection(qo.Projection)
	}
	return opts
}

// findOpts produces mongo Find options with all applicable settings.
func (qo *QueryOptions) findOpts() *options.FindOptionsBuilder {
	opts := options.Find()
	if qo.Projection != nil {
		opts.SetProjection(qo.Projection)
	}
	if qo.Sort != nil {
		opts.SetSort(qo.Sort)
	}
	if qo.Limit != nil {
		opts.SetLimit(*qo.Limit)
	}
	if qo.Skip != nil {
		opts.SetSkip(*qo.Skip)
	}
	return opts
}

// QueryOption is a functional option for configuring queries.
type QueryOption func(*QueryOptions)

// WithProjection sets which fields to include or exclude from results.
// Use bson.M{"field": 1} to include, bson.M{"field": 0} to exclude.
func WithProjection(projection any) QueryOption {
	return func(o *QueryOptions) {
		o.Projection = projection
	}
}

// WithSort sets the sort order for results. Only applies to FindMany.
// Use bson.M{"field": 1} for ascending, bson.M{"field": -1} for descending.
func WithSort(sort any) QueryOption {
	return func(o *QueryOptions) {
		o.Sort = sort
	}
}

// WithLimit sets the maximum number of results to return. Only applies to FindMany.
func WithLimit(limit int64) QueryOption {
	return func(o *QueryOptions) {
		o.Limit = &limit
	}
}

// WithSkip sets the number of results to skip. Only applies to FindMany.
func WithSkip(skip int64) QueryOption {
	return func(o *QueryOptions) {
		o.Skip = &skip
	}
}

func apply(opts []QueryOption) *QueryOptions {
	qo := &QueryOptions{}
	for _, opt := range opts {
		opt(qo)
	}
	return qo
}

// Collection is a type-safe wrapper around *mongo.Collection.
// It handles decoding, ErrNoDocuments normalization, and consistent error wrapping.
type Collection[T any] struct {
	col  *mongo.Collection
	name string
}

// NewCollection creates a typed collection wrapper.
func NewCollection[T any](col *mongo.Collection) *Collection[T] {
	return &Collection[T]{col: col, name: col.Name()}
}

// FindOne returns the first document matching the filter decoded into *T.
// Returns (nil, nil) when no document matches — not an error.
// Supports WithProjection only; sort/limit/skip are ignored.
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

// FindByID is a shortcut for finding a document by its _id field.
func (c *Collection[T]) FindByID(ctx context.Context, id string, opts ...QueryOption) (*T, error) {
	return c.FindOne(ctx, bson.M{"_id": id}, opts...)
}

// FindMany returns all documents matching the filter decoded into []T.
// Returns an empty slice (not nil) when no documents match.
// Supports WithProjection, WithSort, WithLimit, WithSkip.
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

// Raw returns the underlying *mongo.Collection for escape-hatch scenarios.
func (c *Collection[T]) Raw() *mongo.Collection { return c.col }
