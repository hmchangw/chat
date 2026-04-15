package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestParseDocID_ValidHex_ReturnsObjectID(t *testing.T) {
	const hex = "507f1f77bcf86cd799439011"
	got := parseDocID(hex)

	oid, ok := got.(bson.ObjectID)
	if !ok {
		t.Fatalf("expected bson.ObjectID, got %T", got)
	}
	assert.Equal(t, hex, oid.Hex())
}

func TestParseDocID_NonHex_ReturnsString(t *testing.T) {
	cases := []string{
		"abc123",                               // not valid hex length
		"not-an-objectid",                      // contains dash
		"123e4567-e89b-12d3-a456-426614174000", // UUID
		"ZZZZZZZZZZZZZZZZZZZZZZZZ",             // 24 chars but not hex
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got := parseDocID(in)
			s, ok := got.(string)
			if !ok {
				t.Fatalf("expected string, got %T", got)
			}
			assert.Equal(t, in, s)
		})
	}
}

func TestParseDocID_EmptyString_ReturnsEmptyString(t *testing.T) {
	got := parseDocID("")
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string, got %T", got)
	}
	assert.Equal(t, "", s)
}

func TestNewMongoOps_ReturnsNonNil(t *testing.T) {
	ops := newMongoOps()
	require.NotNil(t, ops)
	// Compile-time sanity: the concrete type satisfies mongoOps.
	var _ mongoOps = ops
}

// deadClient returns a *mongo.Client that points at an unreachable endpoint
// with tight timeouts. Every operation will fail quickly, which is enough to
// exercise the error-wrapping branches of mongoOpsImpl without running a real
// server. The integration tests in Task 9 cover the happy paths.
func deadClient(t *testing.T) *mongo.Client {
	t.Helper()
	uri := "mongodb://127.0.0.1:1/testdb?connectTimeoutMS=200&serverSelectionTimeoutMS=200&socketTimeoutMS=200"
	c, err := mongo.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.Disconnect(ctx)
	})
	return c
}

func TestMongoOpsImpl_ErrorPaths(t *testing.T) {
	ops := newMongoOps()
	c := deadClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	t.Run("ListCollections", func(t *testing.T) {
		_, err := ops.ListCollections(ctx, c, "testdb")
		assert.Error(t, err)
	})
	t.Run("ListDocs", func(t *testing.T) {
		_, err := ops.ListDocs(ctx, c, "testdb", "rooms", bson.M{}, 0, 10)
		assert.Error(t, err)
	})
	t.Run("InsertDoc", func(t *testing.T) {
		_, err := ops.InsertDoc(ctx, c, "testdb", "rooms", bson.M{"x": 1})
		assert.Error(t, err)
	})
	t.Run("ReplaceDoc", func(t *testing.T) {
		err := ops.ReplaceDoc(ctx, c, "testdb", "rooms", "id", bson.M{"x": 1})
		assert.Error(t, err)
	})
	t.Run("DeleteDoc", func(t *testing.T) {
		err := ops.DeleteDoc(ctx, c, "testdb", "rooms", "id")
		assert.Error(t, err)
	})
}
