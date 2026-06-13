package natsutil_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestDebugHeader_Constant(t *testing.T) {
	assert.Equal(t, "X-Debug", natsutil.DebugHeader)
}

// The ladder is cumulative: off < flow < debug < trace. Downstream emission
// relies on this ordering, so lock it as an invariant.
func TestDebugLevel_LadderOrdering(t *testing.T) {
	assert.Less(t, natsutil.DebugOff, natsutil.DebugFlow)
	assert.Less(t, natsutil.DebugFlow, natsutil.DebugBasic)
	assert.Less(t, natsutil.DebugBasic, natsutil.DebugTrace)
}

func TestParseDebugLevel(t *testing.T) {
	cases := []struct {
		in   string
		want natsutil.DebugLevel
	}{
		// Off: empty, falsey, explicit off, and—strictly—anything unrecognized.
		{"", natsutil.DebugOff},
		{"0", natsutil.DebugOff},
		{"false", natsutil.DebugOff},
		{"off", natsutil.DebugOff},
		{"no", natsutil.DebugOff},
		{"garbage", natsutil.DebugOff},
		{"2", natsutil.DebugOff}, // no numeric ladder: 2 is NOT trace
		{"DEBUGGING", natsutil.DebugOff},
		// flow
		{"flow", natsutil.DebugFlow},
		// debug + its truthy/bool aliases
		{"debug", natsutil.DebugBasic},
		{"1", natsutil.DebugBasic},
		{"true", natsutil.DebugBasic},
		{"on", natsutil.DebugBasic},
		// trace (no bool alias — must be spelled out)
		{"trace", natsutil.DebugTrace},
		// case-insensitive + whitespace-trimmed
		{"Trace", natsutil.DebugTrace},
		{"  FLOW  ", natsutil.DebugFlow},
		{"\tDebug\n", natsutil.DebugBasic},
		{"TRUE", natsutil.DebugBasic},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, natsutil.ParseDebugLevel(tc.in))
		})
	}
}

func TestDebugLevel_String(t *testing.T) {
	assert.Equal(t, "", natsutil.DebugOff.String())
	assert.Equal(t, "flow", natsutil.DebugFlow.String())
	assert.Equal(t, "debug", natsutil.DebugBasic.String())
	assert.Equal(t, "trace", natsutil.DebugTrace.String())
}

// String→Parse round-trips for every non-off rung (canonical tokens only).
func TestDebugLevel_StringParseRoundTrip(t *testing.T) {
	for _, l := range []natsutil.DebugLevel{natsutil.DebugFlow, natsutil.DebugBasic, natsutil.DebugTrace} {
		assert.Equal(t, l, natsutil.ParseDebugLevel(l.String()))
	}
}

func TestWithDebugLevel_RoundTrip(t *testing.T) {
	ctx := natsutil.WithDebugLevel(context.Background(), natsutil.DebugTrace)
	assert.Equal(t, natsutil.DebugTrace, natsutil.DebugLevelFromContext(ctx))
}

func TestWithDebugLevel_OffIsNoOp(t *testing.T) {
	parent := context.Background()
	ctx := natsutil.WithDebugLevel(parent, natsutil.DebugOff)
	assert.True(t, ctx == parent, "DebugOff must return the parent ctx unchanged")
	assert.Equal(t, natsutil.DebugOff, natsutil.DebugLevelFromContext(ctx))
}

func TestWithDebugLevel_Overwrites(t *testing.T) {
	ctx := natsutil.WithDebugLevel(context.Background(), natsutil.DebugFlow)
	ctx = natsutil.WithDebugLevel(ctx, natsutil.DebugTrace)
	assert.Equal(t, natsutil.DebugTrace, natsutil.DebugLevelFromContext(ctx))
}

func TestDebugLevelFromContext_MissingReturnsOff(t *testing.T) {
	assert.Equal(t, natsutil.DebugOff, natsutil.DebugLevelFromContext(context.Background()))
}

// The debug ctx key must not collide with the request-ID ctx key.
func TestDebugLevel_IndependentOfRequestID(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "req-1")
	ctx = natsutil.WithDebugLevel(ctx, natsutil.DebugBasic)
	assert.Equal(t, "req-1", natsutil.RequestIDFromContext(ctx))
	assert.Equal(t, natsutil.DebugBasic, natsutil.DebugLevelFromContext(ctx))
}

func TestHeaderForContext_EmitsDebugAlongsideRequestID(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "req-xyz")
	ctx = natsutil.WithDebugLevel(ctx, natsutil.DebugTrace)
	h := natsutil.HeaderForContext(ctx)
	assert.Equal(t, "req-xyz", h.Get(natsutil.RequestIDHeader))
	assert.Equal(t, "trace", h.Get(natsutil.DebugHeader))
}

// Debug intent must propagate even on the (rare) path with no request ID.
func TestHeaderForContext_EmitsDebugWithoutRequestID(t *testing.T) {
	ctx := natsutil.WithDebugLevel(context.Background(), natsutil.DebugFlow)
	h := natsutil.HeaderForContext(ctx)
	assert.NotNil(t, h)
	assert.Equal(t, "flow", h.Get(natsutil.DebugHeader))
	assert.Equal(t, "", h.Get(natsutil.RequestIDHeader))
}

func TestHeaderForContext_NoDebugHeaderWhenOff(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "req-xyz")
	h := natsutil.HeaderForContext(ctx)
	_, present := h[natsutil.DebugHeader]
	assert.False(t, present, "X-Debug must be absent when no rung requested")
}

func TestNewMsg_AttachesDebugFromContext(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "req-1")
	ctx = natsutil.WithDebugLevel(ctx, natsutil.DebugBasic)
	msg := natsutil.NewMsg(ctx, "chat.foo.bar", []byte("p"))
	assert.Equal(t, "debug", msg.Header.Get(natsutil.DebugHeader))
}

// The header X-Debug round-trips back to the same rung via ParseDebugLevel.
func TestHeaderForContext_DebugRoundTrip(t *testing.T) {
	for _, l := range []natsutil.DebugLevel{natsutil.DebugFlow, natsutil.DebugBasic, natsutil.DebugTrace} {
		ctx := natsutil.WithDebugLevel(context.Background(), l)
		h := natsutil.HeaderForContext(ctx)
		assert.Equal(t, l, natsutil.ParseDebugLevel(h.Get(natsutil.DebugHeader)))
	}
}
