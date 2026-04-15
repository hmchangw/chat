package main

import (
	"context"
	"errors"
	"fmt"
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

// probeErr is a concrete error type used to verify that errors.As can reach
// the driver error through the multi-%w wrapping used by InsertDoc.
type probeErr struct{ msg string }

func (p *probeErr) Error() string { return p.msg }

// TestInsertDoc_DuplicateKeyWrapping_PreservesBothChains verifies the multi-%w
// wrapping form used by InsertDoc keeps the sentinel and the driver error
// reachable so handlers can errors.Is the sentinel and diagnostics can
// errors.As into driver types. See Fix 1 in the code review.
func TestInsertDoc_DuplicateKeyWrapping_PreservesBothChains(t *testing.T) {
	// Construct a fake error manually wrapped the same way, verify errors.Is and
	// errors.As against the sentinel and against a probe type.
	inner := &probeErr{msg: "E11000 duplicate key error: index ix_foo"}
	wrapped := fmt.Errorf("insert document: %w (%w)", ErrMongoDuplicateKey, inner)

	// The sentinel is reachable via errors.Is for HTTP 409 translation.
	require.True(t, errors.Is(wrapped, ErrMongoDuplicateKey))
	// The driver error value is also reachable via errors.Is.
	require.True(t, errors.Is(wrapped, inner))
	// errors.As can extract the concrete driver error type from the chain.
	var probe *probeErr
	require.True(t, errors.As(wrapped, &probe))
	require.Equal(t, inner, probe)
	// The driver's message is preserved in the wrapped error's text.
	require.Contains(t, wrapped.Error(), "E11000 duplicate key")
}
