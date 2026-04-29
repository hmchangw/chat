package natsutil_test

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestWithRequestID_RoundTrip(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "req-abc-123")
	assert.Equal(t, "req-abc-123", natsutil.RequestIDFromContext(ctx))
}

func TestWithRequestID_EmptyIsNoOp(t *testing.T) {
	parent := context.Background()
	ctx := natsutil.WithRequestID(parent, "")
	assert.True(t, ctx == parent, "empty id must return the parent ctx unchanged")
	assert.Equal(t, "", natsutil.RequestIDFromContext(ctx))
}

func TestWithRequestID_OverwritesExistingValue(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "first")
	ctx = natsutil.WithRequestID(ctx, "second")
	assert.Equal(t, "second", natsutil.RequestIDFromContext(ctx))
}

func TestRequestIDFromContext_MissingReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", natsutil.RequestIDFromContext(context.Background()))
}

func TestContextWithRequestIDFromHeaders_HeaderPresent(t *testing.T) {
	h := nats.Header{}
	h.Set(natsutil.RequestIDHeader, "req-from-header")
	ctx := natsutil.ContextWithRequestIDFromHeaders(context.Background(), h)
	assert.Equal(t, "req-from-header", natsutil.RequestIDFromContext(ctx))
}

func TestContextWithRequestIDFromHeaders_NilHeaderIsNoOp(t *testing.T) {
	parent := context.Background()
	ctx := natsutil.ContextWithRequestIDFromHeaders(parent, nil)
	assert.True(t, ctx == parent)
	assert.Equal(t, "", natsutil.RequestIDFromContext(ctx))
}

func TestContextWithRequestIDFromHeaders_EmptyHeaderValueIsNoOp(t *testing.T) {
	parent := context.Background()
	ctx := natsutil.ContextWithRequestIDFromHeaders(parent, nats.Header{})
	assert.True(t, ctx == parent)
	assert.Equal(t, "", natsutil.RequestIDFromContext(ctx))
}

func TestHeaderForContext_WithID(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "req-xyz")
	h := natsutil.HeaderForContext(ctx)
	assert.NotNil(t, h)
	assert.Equal(t, "req-xyz", h.Get(natsutil.RequestIDHeader))
}

func TestHeaderForContext_WithoutIDReturnsNil(t *testing.T) {
	h := natsutil.HeaderForContext(context.Background())
	assert.Nil(t, h, "no request ID in ctx must return a nil header (not an empty one)")
}

func TestHeaderForContext_ReversibleViaContextFromHeaders(t *testing.T) {
	original := natsutil.WithRequestID(context.Background(), "round-trip-id")
	h := natsutil.HeaderForContext(original)
	recovered := natsutil.ContextWithRequestIDFromHeaders(context.Background(), h)
	assert.Equal(t, "round-trip-id", natsutil.RequestIDFromContext(recovered))
}

func TestRequestIDHeader_Constant(t *testing.T) {
	assert.Equal(t, "X-Request-ID", natsutil.RequestIDHeader)
}

func TestNewMsg_AttachesHeaderFromContext(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "req-newmsg-test")
	msg := natsutil.NewMsg(ctx, "chat.foo.bar", []byte("payload"))
	assert.Equal(t, "chat.foo.bar", msg.Subject)
	assert.Equal(t, []byte("payload"), msg.Data)
	assert.Equal(t, "req-newmsg-test", msg.Header.Get(natsutil.RequestIDHeader))
}

func TestNewMsg_NoIDLeavesHeaderNil(t *testing.T) {
	msg := natsutil.NewMsg(context.Background(), "chat.foo.bar", []byte("payload"))
	assert.Nil(t, msg.Header)
}
