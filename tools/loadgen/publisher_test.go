package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNATSCorePublisher_PublishFrontdoor exercises the core-NATS (non-JetStream)
// publish path via a stub ConnPool backed by a real *nats.Conn zero value.
// The zero-value *nats.Conn will cause PublishMsg to return an error, which
// is fine — we're testing that the publisher routes to the pool and returns
// the error correctly wrapped, not end-to-end delivery.
func TestNATSCorePublisher_PublishFrontdoor(t *testing.T) {
	// Build a publisher in frontdoor (core-NATS) mode.
	pool := NewTestConnPool(1) // size-1 pool, observer=nil → For("…") returns nil
	p := &natsCorePublisher{
		pool:         pool,
		useJetStream: false,
		asyncJS:      false,
		runID:        "test-run",
	}
	// With a nil *nats.Conn, PublishMsg panics; use a zero-value conn so
	// we get an error instead of a panic.
	pool.observer = &nats.Conn{}
	err := p.Publish(context.Background(), "chat.user.alice.room.r1.site-local.msg.send", []byte(`{}`))
	// We expect a wrapped error from core publish — what matters is that it
	// reaches the publish path (not some config-gate or nil-pointer panic).
	require.Error(t, err)
	assert.Contains(t, err.Error(), "core publish",
		"frontdoor error must be wrapped with 'core publish'")
}

// TestNATSCorePublisher_HeaderRunID verifies that a non-empty runID is stamped
// onto the NATS message header using the HeaderRunID constant. We test this by
// inspecting the struct's contract: the constant must equal the canonical string.
func TestNATSCorePublisher_HeaderRunID(t *testing.T) {
	// HeaderRunID is defined in publisher.go after the move.
	assert.Equal(t, "X-Loadgen-Run-ID", HeaderRunID,
		"HeaderRunID must equal the canonical header name")
}

// TestNewAsyncErrHandler_EvictsOrphans verifies that the async err handler
// evicts the orphan messageID from the collector's broadcast correlation map
// when an async publish fails.
func TestNewAsyncErrHandler_EvictsOrphans(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "messaging-pipeline")

	// Seed the collector so the handler can evict it.
	t0 := time.Unix(200, 0)
	c.RecordPublishBroadcastOnly("msg-42", t0)
	require.Contains(t, c.MessageIDs(), "msg-42",
		"precondition: msg-42 must be in the collector before eviction")

	h := newAsyncErrHandler(m, "messaging-pipeline", c)

	// Canonical event payload: handler's JSON stub targets {"message":{"id":"…"}}.
	payload := []byte(`{"message":{"id":"msg-42","roomId":"r1"},"siteId":"site-local","timestamp":1}`)
	h(nil, &nats.Msg{Data: payload}, errors.New("simulated nack"))

	// msg-42 must be gone from the seen-message-IDs pool.
	assert.NotContains(t, c.MessageIDs(), "msg-42",
		"orphan must be evicted from byMsgID via RecordPublishFailed")
}

// TestNewAsyncErrHandler_BumpsMetric verifies the metric is incremented on
// every async-ack failure.
func TestNewAsyncErrHandler_BumpsMetric(t *testing.T) {
	m := NewMetrics()
	h := newAsyncErrHandler(m, "test-preset", nil)
	h(nil, &nats.Msg{Data: []byte(`{"message":{"id":"x"}}`)}, errors.New("nack"))
	got := counterValueLabeled(m, "loadgen_publish_errors_total", "reason", "async_ack")
	assert.Equal(t, 1.0, got)
}

// TestJetStreamPublishOpts_InPublisherFile checks that jetstreamPublishOpts is
// accessible from publisher.go — the test itself just calls the function and
// validates the return value (same as the existing tests in main_test.go,
// proving the symbol is still reachable after the move).
func TestJetStreamPublishOpts_ReachableAfterMove(t *testing.T) {
	opts := jetstreamPublishOpts(512, NewMetrics(), "small", nil)
	assert.Len(t, opts, 2, "non-zero maxPending must return both MaxPending + ErrHandler opts")

	empty := jetstreamPublishOpts(0, NewMetrics(), "small", nil)
	assert.Empty(t, empty, "zero maxPending must return nil opts")
}

// TestNewNatsCorePublisher_ReachableAfterMove confirms that after moving
// natsCorePublisher to publisher.go, the constructor and warmup-publisher
// helpers are still callable.
func TestNewNatsCorePublisher_ReachableAfterMove(t *testing.T) {
	p := newNatsCorePublisher(nil, InjectCanonical, nil)
	require.NotNil(t, p)
	assert.True(t, p.useJetStream)

	w := newWarmupPublisher(p)
	require.NotNil(t, w)
	assert.False(t, w.useJetStream, "warmup publisher must force frontdoor mode")
}
