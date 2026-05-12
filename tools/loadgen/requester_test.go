package main

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNATSRequester_FieldWiring verifies that natsRequester is constructable
// from requester.go with the expected field layout and that the Requester
// interface is satisfied. Full request delivery is covered by integration tests.
func TestNATSRequester_FieldWiring(t *testing.T) {
	pool := &ConnPool{}
	r := &natsRequester{pool: pool, runID: "run-xyz"}
	require.NotNil(t, r)
	assert.Equal(t, "run-xyz", r.runID)
	assert.Same(t, pool, r.pool)
}

// TestNATSRequester_ImplementsRequesterInterface verifies that *natsRequester
// satisfies the Requester interface without requiring a live NATS connection.
func TestNATSRequester_ImplementsRequesterInterface(t *testing.T) {
	var _ Requester = (*natsRequester)(nil)
}

// TestNATSRequester_EmptyRunID verifies that a zero runID is valid (the
// implementation skips header-stamping when runID is empty).
func TestNATSRequester_EmptyRunID(t *testing.T) {
	pool := &ConnPool{}
	r := &natsRequester{pool: pool, runID: ""}
	assert.Equal(t, "", r.runID)
}

// TestNATSRequester_PoolRouting verifies that natsRequester.pool is the
// one used for routing. The For method is exercised at the ConnPool level
// (connpool_test.go); here we only confirm that a pool of size 1 with a
// nil observer can be assigned without issue.
func TestNATSRequester_PoolRouting(t *testing.T) {
	// Size-1 pool with no connections — tests struct wiring, not delivery.
	pool := NewTestConnPool(1)
	r := &natsRequester{pool: pool, runID: "run-abc"}
	assert.NotNil(t, r.pool)
	// IndexFor is deterministic regardless of the runID.
	assert.Equal(t, 0, r.pool.IndexFor("alice"))
	assert.Equal(t, 0, r.pool.IndexFor("bob"))
}

// TestNATSRequester_Request_WrapsTransportError exercises the Request method
// error-wrapping path. A size-1 pool backed by a zero-value *nats.Conn will
// cause RequestMsgWithContext to return an error, which we verify is wrapped
// with the "nats request" prefix. This tests the error-handling path that was
// missing from the original requester_test.go suite.
func TestNATSRequester_Request_WrapsTransportError(t *testing.T) {
	// Build a requester with a size-1 pool backed by a zero-value conn.
	pool := NewTestConnPool(1)
	pool.observer = &nats.Conn{} // zero-value conn → RequestMsgWithContext will error
	r := &natsRequester{
		pool:  pool,
		runID: "test-run",
	}

	// Call Request with a short timeout — the zero-value conn will cause an error.
	ctx := context.Background()
	_, err := r.Request(ctx, "test.subject", []byte("{}"), 100*time.Millisecond)

	// Verify the error is wrapped with the "nats request" prefix.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nats request",
		"Request error must be wrapped with 'nats request' prefix")
}
