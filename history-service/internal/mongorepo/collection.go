package mongorepo

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

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
