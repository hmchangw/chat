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
func (c *Collection[T]) FindOne(ctx context.Context, filter bson.D) (*T, error) {
	var result T
	err := c.col.FindOne(ctx, filter).Decode(&result)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding %s: %w", c.name, err)
	}
	return &result, nil
}

// FindByID is a shortcut for finding a document by its _id field.
func (c *Collection[T]) FindByID(ctx context.Context, id string) (*T, error) {
	return c.FindOne(ctx, bson.D{{Key: "_id", Value: id}})
}

// FindMany returns all documents matching the filter decoded into []T.
// Returns an empty slice (not nil) when no documents match.
func (c *Collection[T]) FindMany(ctx context.Context, filter bson.D) ([]T, error) {
	cursor, err := c.col.Find(ctx, filter)
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
